package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const managedArchiveRetention = 30 * 24 * time.Hour
const managedArchiveCountCap = 200
const managedArchiveAggregateBatchSize = 64

// managedArchiveReadCutoff is deliberately computed from the store clock so
// every archive read has the same strict age boundary as pruning.
func managedArchiveReadCutoff(now time.Time) string {
	return formatTimestamp(now.UTC().Add(-managedArchiveRetention))
}

func managedArchiveTableExistsTx(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_agent_archives'`).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

func managedLifecycleArchiveFieldsAvailableTx(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('managed_lifecycle_actions') WHERE name='archive_key'`).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

func loadManagedLifecycleArchiveFieldsTx(ctx context.Context, db *sql.DB, action *ManagedLifecycleAction) error {
	available, err := managedLifecycleArchiveFieldsAvailableTx(ctx, db)
	if err != nil || !available {
		return err
	}
	var archiveKey, disposition, checkpointID, frozenAt, outcome sql.NullString
	var cutoff, total, queued, inFlight, completed, released sql.NullInt64
	var totalOverflow, queuedOverflow, inFlightOverflow, completedOverflow, releasedOverflow sql.NullInt64
	var checkpointSequence, aggregateComplete sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT archive_key,delivery_cutoff_enqueue_sequence,delivery_aggregate_disposition,
		delivery_aggregate_total,delivery_aggregate_queued_never_attempted,delivery_aggregate_in_flight_or_unknown,
		delivery_aggregate_completed,delivery_aggregate_released_or_failed,delivery_aggregate_total_overflow,
		delivery_aggregate_queued_never_attempted_overflow,delivery_aggregate_in_flight_or_unknown_overflow,
		delivery_aggregate_completed_overflow,delivery_aggregate_released_or_failed_overflow,
		delivery_checkpoint_enqueue_sequence,delivery_checkpoint_message_id,delivery_aggregate_complete,
		delivery_frozen_at,archive_outcome FROM managed_lifecycle_actions WHERE action_id=?`, action.ActionID).Scan(
		&archiveKey, &cutoff, &disposition, &total, &queued, &inFlight, &completed, &released,
		&totalOverflow, &queuedOverflow, &inFlightOverflow, &completedOverflow, &releasedOverflow,
		&checkpointSequence, &checkpointID, &aggregateComplete, &frozenAt, &outcome)
	if err != nil {
		return err
	}
	action.ArchiveKey = archiveKey.String
	action.DeliveryCutoffEnqueueSequence = cutoff.Int64
	action.DeliveryAggregate.Disposition = disposition.String
	action.DeliveryAggregate.Total = int(total.Int64)
	action.DeliveryAggregate.QueuedNeverAttempted = int(queued.Int64)
	action.DeliveryAggregate.InFlightOrUnknown = int(inFlight.Int64)
	action.DeliveryAggregate.Completed = int(completed.Int64)
	action.DeliveryAggregate.ReleasedOrFailed = int(released.Int64)
	action.DeliveryAggregate.TotalOverflow = totalOverflow.Int64 != 0
	action.DeliveryAggregate.QueuedNeverAttemptedOverflow = queuedOverflow.Int64 != 0
	action.DeliveryAggregate.InFlightOrUnknownOverflow = inFlightOverflow.Int64 != 0
	action.DeliveryAggregate.CompletedOverflow = completedOverflow.Int64 != 0
	action.DeliveryAggregate.ReleasedOrFailedOverflow = releasedOverflow.Int64 != 0
	action.DeliveryCheckpointEnqueueSequence = checkpointSequence.Int64
	action.DeliveryCheckpointMessageID = checkpointID.String
	action.DeliveryAggregateComplete = aggregateComplete.Int64 != 0
	action.DeliveryFrozenAt = frozenAt.String
	action.ArchiveOutcome = outcome.String
	return nil
}

func retireManagedRecipientSourceTx(ctx context.Context, db *sql.DB, agentID, recipientName string, now string) error {
	source, err := recipientSourceForTx(ctx, db, recipientName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	sentinel := retiredManagedRecipientOwner(agentID)
	switch {
	case source.SourceKind == "managed" && source.OwnerIdentity == agentID && (source.State == "active" || source.State == "reserved"):
		_, err = db.ExecContext(ctx, `UPDATE recipient_sources
			SET owner_identity=?,state='retired',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
			WHERE recipient_name=?`, sentinel, now, recipientName)
	case source.SourceKind == "managed" && source.OwnerIdentity == agentID && source.State == "conflicted" && source.ConflictKind == "tmux":
		_, err = db.ExecContext(ctx, `UPDATE recipient_sources
			SET source_kind=?,owner_identity=?,state='active',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
			WHERE recipient_name=?`, source.ConflictKind, source.ConflictOwner, now, recipientName)
	case source.SourceKind == "tmux" && source.State == "conflicted" && source.ConflictKind == "managed" && source.ConflictOwner == agentID:
		_, err = db.ExecContext(ctx, `UPDATE recipient_sources
			SET state='active',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
			WHERE recipient_name=?`, now, recipientName)
	case source.SourceKind == "managed" && source.OwnerIdentity == sentinel && source.State == "retired":
		return nil
	default:
		return &DirectMailSourceMismatchError{}
	}
	return err
}

func managedTerminationLaunchBlockedTx(ctx context.Context, db *sql.DB, agentID, name string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM managed_lifecycle_actions action
		LEFT JOIN managed_agents old_agent ON old_agent.agent_id=action.agent_id
		WHERE action.kind='terminate' AND (action.agent_id=? OR old_agent.name=?)
		AND ((action.state NOT IN ('succeeded','cancelled','superseded')) OR (action.state='failed' AND (action.retryable=1 OR action.terminal_code='TERMINATION_PROCESS_IDENTITY_UNSAFE')))
		LIMIT 1`, agentID, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func managedTerminationSettlementFrozenForMessageTx(ctx context.Context, db *sql.DB, messageID string) (bool, error) {
	available, err := managedLifecycleArchiveFieldsAvailableTx(ctx, db)
	if err != nil || !available {
		return false, err
	}
	var found int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM managed_lifecycle_actions action
		LEFT JOIN managed_agents agent ON agent.agent_id=action.agent_id
		WHERE action.kind='terminate' AND action.archive_key IS NOT NULL
		AND action.state NOT IN ('succeeded','cancelled','superseded')
		AND (agent.name=(SELECT recipient FROM messages WHERE id=?)
			OR action.agent_id IN (SELECT owner_identity FROM message_target_snapshots WHERE message_id=? AND source_kind='managed'))
		LIMIT 1`, messageID, messageID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func managedTerminationSettlementFrozenTx(ctx context.Context, db *sql.DB, agentID string) (bool, error) {
	available, err := managedLifecycleArchiveFieldsAvailableTx(ctx, db)
	if err != nil || !available {
		return false, err
	}
	var found int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM managed_lifecycle_actions WHERE agent_id=? AND kind='terminate' AND archive_key IS NOT NULL AND state NOT IN ('succeeded','cancelled','superseded') LIMIT 1`, agentID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func managedTerminationArchiveActiveTx(ctx context.Context, db *sql.DB, agentID string) (bool, error) {
	available, err := managedLifecycleArchiveFieldsAvailableTx(ctx, db)
	if err != nil || !available {
		return false, err
	}
	var found int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM managed_lifecycle_actions WHERE agent_id=? AND kind='terminate' AND archive_key IS NOT NULL AND state NOT IN ('succeeded','cancelled','superseded') LIMIT 1`, agentID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func captureManagedTerminationAdmissionTx(ctx context.Context, db *sql.DB, actionID, agentID, recipientName, now string) error {
	var cutoff int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(enqueue_sequence),0) FROM messages`).Scan(&cutoff); err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET delivery_cutoff_enqueue_sequence=?,delivery_frozen_at=? WHERE action_id=? AND agent_id=? AND kind='terminate'`, cutoff, now, actionID, agentID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return &ManagedRestartStaleError{AgentID: agentID}
	}
	return retireManagedRecipientSourceTx(ctx, db, agentID, recipientName, now)
}

func copyManagedTerminationProgressTx(ctx context.Context, db *sql.DB, predecessorID, actionID string) error {
	var archiveKey, disposition, checkpointID, frozenAt, outcome sql.NullString
	var cutoff, total, queued, inFlight, completed, released sql.NullInt64
	var totalOverflow, queuedOverflow, inFlightOverflow, completedOverflow, releasedOverflow sql.NullInt64
	var checkpointSequence, aggregateComplete sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT archive_key,delivery_cutoff_enqueue_sequence,delivery_aggregate_disposition,
		delivery_aggregate_total,delivery_aggregate_queued_never_attempted,delivery_aggregate_in_flight_or_unknown,
		delivery_aggregate_completed,delivery_aggregate_released_or_failed,delivery_aggregate_total_overflow,
		delivery_aggregate_queued_never_attempted_overflow,delivery_aggregate_in_flight_or_unknown_overflow,
		delivery_aggregate_completed_overflow,delivery_aggregate_released_or_failed_overflow,
		delivery_checkpoint_enqueue_sequence,delivery_checkpoint_message_id,delivery_aggregate_complete,
		delivery_frozen_at,archive_outcome FROM managed_lifecycle_actions WHERE action_id=?`, predecessorID).Scan(
		&archiveKey, &cutoff, &disposition, &total, &queued, &inFlight, &completed, &released,
		&totalOverflow, &queuedOverflow, &inFlightOverflow, &completedOverflow, &releasedOverflow,
		&checkpointSequence, &checkpointID, &aggregateComplete, &frozenAt, &outcome); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET archive_key=?,delivery_cutoff_enqueue_sequence=?,delivery_aggregate_disposition=?,delivery_aggregate_total=?,delivery_aggregate_queued_never_attempted=?,delivery_aggregate_in_flight_or_unknown=?,delivery_aggregate_completed=?,delivery_aggregate_released_or_failed=?,delivery_aggregate_total_overflow=?,delivery_aggregate_queued_never_attempted_overflow=?,delivery_aggregate_in_flight_or_unknown_overflow=?,delivery_aggregate_completed_overflow=?,delivery_aggregate_released_or_failed_overflow=?,delivery_checkpoint_enqueue_sequence=?,delivery_checkpoint_message_id=?,delivery_aggregate_complete=?,delivery_frozen_at=?,archive_outcome=? WHERE action_id=?`,
		archiveKey, cutoff, disposition, total, queued, inFlight, completed, released, totalOverflow, queuedOverflow, inFlightOverflow, completedOverflow, releasedOverflow, checkpointSequence, checkpointID, aggregateComplete, frozenAt, outcome, actionID)
	return err
}

func scanManagedAgentArchive(scanner interface{ Scan(...any) error }) (*ManagedAgentArchive, error) {
	var archive ManagedAgentArchive
	var totalOverflow, queuedOverflow, inFlightOverflow, completedOverflow, releasedOverflow int
	if err := scanner.Scan(
		&archive.ArchiveKey,
		&archive.AgentID,
		&archive.Name,
		&archive.RecordKind,
		&archive.LifecycleState,
		&archive.TerminalOutcome,
		&archive.CompletedAt,
		&archive.Delivery.Disposition,
		&archive.Delivery.Total,
		&archive.Delivery.QueuedNeverAttempted,
		&archive.Delivery.InFlightOrUnknown,
		&archive.Delivery.Completed,
		&archive.Delivery.ReleasedOrFailed,
		&totalOverflow,
		&queuedOverflow,
		&inFlightOverflow,
		&completedOverflow,
		&releasedOverflow,
	); err != nil {
		return nil, err
	}
	archive.Delivery.TotalOverflow = totalOverflow != 0
	archive.Delivery.QueuedNeverAttemptedOverflow = queuedOverflow != 0
	archive.Delivery.InFlightOrUnknownOverflow = inFlightOverflow != 0
	archive.Delivery.CompletedOverflow = completedOverflow != 0
	archive.Delivery.ReleasedOrFailedOverflow = releasedOverflow != 0
	return &archive, nil
}

const managedArchiveSelect = `SELECT archive_key,agent_id,name,record_kind,lifecycle_state,terminal_outcome,completed_at,disposition,total,queued_never_attempted,in_flight_or_unknown,completed,released_or_failed,total_overflow,queued_never_attempted_overflow,in_flight_or_unknown_overflow,completed_overflow,released_or_failed_overflow FROM managed_agent_archives`

func managedAgentArchivesForTx(ctx context.Context, db *sql.DB, now time.Time) ([]*ManagedAgentArchive, error) {
	exists, err := managedArchiveTableExistsTx(ctx, db)
	if err != nil || !exists {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, managedArchiveSelect+` WHERE completed_at > ? ORDER BY completed_at DESC, archive_key DESC`, managedArchiveReadCutoff(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	archives := make([]*ManagedAgentArchive, 0)
	for rows.Next() {
		archive, scanErr := scanManagedAgentArchive(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		archives = append(archives, archive)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return archives, rows.Close()
}

// ManagedAgentArchives returns retained closed managed-agent archives in the
// same order used by the managed projection. Expired rows are invisible even
// if maintenance has not pruned them yet.
func (s *Store) ManagedAgentArchives(ctx context.Context) ([]*ManagedAgentArchive, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return managedAgentArchivesForTx(ctx, s.db, s.clock.Now().UTC())
}

// ManagedAgentArchive performs a canonical opaque-key lookup. A missing table,
// missing key, or expired key is reported as sql.ErrNoRows; callers must not
// fall back to a live agent or history lookup.
func (s *Store) ManagedAgentArchive(ctx context.Context, archiveKey string) (*ManagedAgentArchive, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if !managedCanonicalUUIDRE.MatchString(archiveKey) {
		return nil, errors.New("managed archive key is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	exists, err := managedArchiveTableExistsTx(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, sql.ErrNoRows
	}
	return scanManagedAgentArchive(s.db.QueryRowContext(ctx, managedArchiveSelect+` WHERE archive_key=? AND completed_at > ?`, archiveKey, managedArchiveReadCutoff(s.clock.Now().UTC())))
}

// ManagedAgentArchiveByKey is an explicit alias for callers that prefer the
// lookup operation to be visible in the method name.
func (s *Store) ManagedAgentArchiveByKey(ctx context.Context, archiveKey string) (*ManagedAgentArchive, error) {
	return s.ManagedAgentArchive(ctx, archiveKey)
}

func managedTerminationCleanupOutcomeTx(ctx context.Context, db *sql.DB, actionID string) (string, error) {
	var cleanupFailures int
	err := db.QueryRowContext(ctx, `WITH RECURSIVE lineage(action_id,retry_of) AS (
		SELECT action_id,retry_of FROM managed_lifecycle_actions WHERE action_id=?
		UNION ALL
		SELECT parent.action_id,parent.retry_of FROM managed_lifecycle_actions parent JOIN lineage child ON child.retry_of=parent.action_id
	)
	SELECT COUNT(*) FROM lineage JOIN managed_lifecycle_actions action ON action.action_id=lineage.action_id WHERE action.terminal_code='TERMINATION_CLEANUP_FAILED'`, actionID).Scan(&cleanupFailures)
	if err != nil {
		return "", err
	}
	if cleanupFailures > 0 {
		return "cleanup_failed", nil
	}
	return "terminated", nil
}

func managedArchiveMatches(existing *ManagedAgentArchive, want *ManagedAgentArchive) bool {
	return existing.ArchiveKey == want.ArchiveKey && existing.AgentID == want.AgentID && existing.Name == want.Name && existing.RecordKind == want.RecordKind && existing.LifecycleState == want.LifecycleState && existing.TerminalOutcome == want.TerminalOutcome && existing.CompletedAt == want.CompletedAt && existing.Delivery == want.Delivery
}

func insertManagedArchiveTx(ctx context.Context, db *sql.DB, archive *ManagedAgentArchive) error {
	_, err := db.ExecContext(ctx, `INSERT INTO managed_agent_archives (
		archive_key,agent_id,name,record_kind,lifecycle_state,terminal_outcome,completed_at,disposition,total,queued_never_attempted,in_flight_or_unknown,completed,released_or_failed,total_overflow,queued_never_attempted_overflow,in_flight_or_unknown_overflow,completed_overflow,released_or_failed_overflow)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(archive_key) DO NOTHING`,
		archive.ArchiveKey, archive.AgentID, archive.Name, archive.RecordKind, archive.LifecycleState, archive.TerminalOutcome, archive.CompletedAt, archive.Delivery.Disposition, archive.Delivery.Total, archive.Delivery.QueuedNeverAttempted, archive.Delivery.InFlightOrUnknown, archive.Delivery.Completed, archive.Delivery.ReleasedOrFailed, boolInt(archive.Delivery.TotalOverflow), boolInt(archive.Delivery.QueuedNeverAttemptedOverflow), boolInt(archive.Delivery.InFlightOrUnknownOverflow), boolInt(archive.Delivery.CompletedOverflow), boolInt(archive.Delivery.ReleasedOrFailedOverflow))
	if err != nil {
		return err
	}
	existing, err := scanManagedAgentArchive(db.QueryRowContext(ctx, managedArchiveSelect+` WHERE archive_key=?`, archive.ArchiveKey))
	if err != nil {
		return err
	}
	if !managedArchiveMatches(existing, archive) {
		return errors.New("managed archive key has conflicting immutable data")
	}
	return nil
}

func managedArchiveCandidatePredicate() string {
	return `(
		EXISTS (SELECT 1 FROM message_target_snapshots target WHERE target.message_id=messages.id AND target.source_kind='managed' AND target.owner_identity=? AND messages.enqueue_sequence<=?)
		OR (messages.state='queued' AND messages.recipient=? AND messages.enqueue_sequence<=? AND NOT EXISTS (SELECT 1 FROM message_target_snapshots target WHERE target.message_id=messages.id))
	)`
}

func finalizeManagedTerminationRemindersTx(ctx context.Context, db *sql.DB, reminderIncarnations map[string]string, name string, agentID string, cutoff int64) error {
	predicate := managedArchiveCandidatePredicate()
	rows, err := db.QueryContext(ctx, `SELECT id FROM reminders WHERE interval_ms IS NULL AND state='message_queued' AND linked_message_id IN (SELECT messages.id FROM messages WHERE `+predicate+`)`, agentID, cutoff, name, cutoff)
	if err != nil {
		return err
	}
	var oneShot []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		oneShot = append(oneShot, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM reminders WHERE interval_ms IS NULL AND state='message_queued' AND linked_message_id IN (SELECT messages.id FROM messages WHERE `+predicate+`)`, agentID, cutoff, name, cutoff); err != nil {
		return err
	}
	for _, id := range oneShot {
		delete(reminderIncarnations, id)
	}
	if _, err := db.ExecContext(ctx, `UPDATE reminders SET state='scheduled',linked_message_id=NULL WHERE interval_ms IS NOT NULL AND linked_message_id IN (SELECT messages.id FROM messages WHERE `+predicate+`) AND state IN ('scheduled','message_queued')`, agentID, cutoff, name, cutoff); err != nil {
		return err
	}
	return nil
}

func deleteManagedTerminationRawTx(ctx context.Context, db *sql.DB, reminderIncarnations map[string]string, action *ManagedLifecycleAction, name string) error {
	predicate := managedArchiveCandidatePredicate()
	predicateArgs := []any{action.AgentID, action.DeliveryCutoffEnqueueSequence, name, action.DeliveryCutoffEnqueueSequence}
	if err := finalizeManagedTerminationRemindersTx(ctx, db, reminderIncarnations, name, action.AgentID, action.DeliveryCutoffEnqueueSequence); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM managed_direct_delivery_attempts WHERE agent_id=? AND message_id IN (SELECT messages.id FROM messages WHERE `+predicate+`)`, append([]any{action.AgentID}, predicateArgs...)...); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM managed_direct_delivery_diagnostics WHERE agent_id=?`, action.AgentID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM messages WHERE `+predicate, predicateArgs...); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM managed_quiet_send_authorizations WHERE sender_agent_id=? OR recipient_agent_id=?`, action.AgentID, action.AgentID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM managed_agent_policies WHERE agent_id=?`, action.AgentID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM managed_launch_recipes WHERE agent_id=?`, action.AgentID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM managed_crash_recovery WHERE agent_id=?`, action.AgentID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM managed_goal_activity WHERE agent_id=?`, action.AgentID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM managed_goal_wakeups WHERE agent_id=?`, action.AgentID); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM managed_root_recovery_actions WHERE agent_id=?`, action.AgentID); err != nil {
		return err
	}
	if action.SourceFence != nil {
		fenceArgs := managedFenceValues(*action.SourceFence)
		if _, err := db.ExecContext(ctx, `DELETE FROM managed_parent_work WHERE `+managedFenceWhere(""), fenceArgs...); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM managed_builtin_tool_invocations WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_sha256=?`, fenceArgs...); err != nil {
			return err
		}
	}
	return nil
}

func pruneManagedArchivesTx(ctx context.Context, db *sql.DB, now time.Time) (int, error) {
	exists, err := managedArchiveTableExistsTx(ctx, db)
	if err != nil || !exists {
		return 0, err
	}
	cutoff := managedArchiveReadCutoff(now)
	result, err := db.ExecContext(ctx, `DELETE FROM managed_agent_archives WHERE completed_at<=?`, cutoff)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	result, err = db.ExecContext(ctx, `DELETE FROM managed_agent_archives WHERE archive_key IN (
		SELECT archive_key FROM (
			SELECT archive_key,ROW_NUMBER() OVER (ORDER BY completed_at DESC,archive_key DESC) AS ordinal
			FROM managed_agent_archives
		) WHERE ordinal>?
	)`, managedArchiveCountCap)
	if err != nil {
		return 0, err
	}
	extra, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(count + extra), nil
}

func (s *Store) managedArchiveFieldsEnabled(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, errors.New("store is closed")
	}
	return managedLifecycleArchiveFieldsAvailableTx(ctx, s.db)
}

func (s *Store) pruneManagedArchives(ctx context.Context) error {
	return s.runImmediate(ctx, func() error {
		exists, err := managedArchiveTableExistsTx(ctx, s.db)
		if err != nil || !exists {
			return err
		}
		removed, err := pruneManagedArchivesTx(ctx, s.db, s.clock.Now().UTC())
		if err != nil {
			return err
		}
		if removed == 0 {
			return nil
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// managedArchiveMessage is the bounded keyset row read by each aggregation
// chunk. The ID cursor is retained even though enqueue_sequence is currently
// unique so the ordering remains explicit if that storage detail changes.
type managedArchiveMessage struct {
	Sequence    int64
	ID          string
	Incarnation string
	State       string
}

func managedArchiveMessagesTx(ctx context.Context, db *sql.DB, agentID, name string, cutoff, checkpointSequence int64, checkpointID string, limit int) ([]managedArchiveMessage, error) {
	rows, err := db.QueryContext(ctx, `SELECT m.enqueue_sequence,m.id,COALESCE(m.message_incarnation_id,''),m.state
		FROM messages m
		WHERE m.enqueue_sequence<=?
		AND (
			EXISTS (SELECT 1 FROM message_target_snapshots target WHERE target.message_id=m.id AND target.source_kind='managed' AND target.owner_identity=?)
			OR (m.state='queued' AND m.recipient=? AND NOT EXISTS (SELECT 1 FROM message_target_snapshots target WHERE target.message_id=m.id))
		)
		AND (m.enqueue_sequence>? OR (m.enqueue_sequence=? AND m.id>?))
		ORDER BY m.enqueue_sequence ASC,m.id ASC LIMIT ?`, cutoff, agentID, name, checkpointSequence, checkpointSequence, checkpointID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]managedArchiveMessage, 0, limit)
	for rows.Next() {
		var message managedArchiveMessage
		if err := rows.Scan(&message.Sequence, &message.ID, &message.Incarnation, &message.State); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, rows.Close()
}

func managedArchiveMessageClassTx(ctx context.Context, db *sql.DB, message managedArchiveMessage) string {
	var managedIncarnation, managedState string
	managedErr := db.QueryRowContext(ctx, `SELECT message_incarnation_id,state FROM managed_direct_delivery_attempts WHERE message_id=? ORDER BY attempt_number DESC LIMIT 1`, message.ID).Scan(&managedIncarnation, &managedState)
	managedFound := managedErr == nil
	if managedErr != nil && !errors.Is(managedErr, sql.ErrNoRows) {
		return "in_flight_or_unknown"
	}

	var ordinaryState string
	ordinaryErr := db.QueryRowContext(ctx, `SELECT state FROM delivery_attempts WHERE message_id=? ORDER BY attempt_number DESC LIMIT 1`, message.ID).Scan(&ordinaryState)
	ordinaryFound := ordinaryErr == nil
	if ordinaryErr != nil && !errors.Is(ordinaryErr, sql.ErrNoRows) {
		return "in_flight_or_unknown"
	}

	if managedFound && ordinaryFound {
		return "in_flight_or_unknown"
	}
	if managedFound {
		if managedIncarnation != message.Incarnation {
			return "in_flight_or_unknown"
		}
		state := managedState
		switch state {
		case "direct_pending", "direct_prompted":
			return "in_flight_or_unknown"
		case "acknowledged":
			if message.State == "acknowledged" {
				return "completed"
			}
			return "in_flight_or_unknown"
		case "released", "failed":
			return "released_or_failed"
		default:
			return "in_flight_or_unknown"
		}
	}
	if ordinaryFound {
		state := ordinaryState
		switch state {
		case "leased", "injection_requested":
			return "in_flight_or_unknown"
		case "acknowledged":
			if message.State == "acknowledged" {
				return "completed"
			}
			return "in_flight_or_unknown"
		case "nacked", "timed_out":
			return "released_or_failed"
		default:
			return "in_flight_or_unknown"
		}
	}
	switch message.State {
	case "queued":
		return "queued_never_attempted"
	case "dead_letter":
		return "released_or_failed"
	default:
		return "in_flight_or_unknown"
	}
}

func managedArchiveDisposition(aggregate ManagedDeliveryAggregate) string {
	switch {
	case aggregate.InFlightOrUnknown > 0 || aggregate.InFlightOrUnknownOverflow:
		return "unknown"
	case aggregate.QueuedNeverAttempted > 0 || aggregate.QueuedNeverAttemptedOverflow:
		return "queued"
	case aggregate.ReleasedOrFailed > 0 || aggregate.ReleasedOrFailedOverflow:
		return "released_or_failed"
	case aggregate.Completed > 0 || aggregate.CompletedOverflow:
		return "completed"
	default:
		return "no_attempts"
	}
}

func managedArchiveAggregateUpdateArgs(aggregate ManagedDeliveryAggregate) []any {
	return []any{aggregate.Disposition, aggregate.Total, aggregate.QueuedNeverAttempted, aggregate.InFlightOrUnknown, aggregate.Completed, aggregate.ReleasedOrFailed,
		boolInt(aggregate.TotalOverflow), boolInt(aggregate.QueuedNeverAttemptedOverflow), boolInt(aggregate.InFlightOrUnknownOverflow), boolInt(aggregate.CompletedOverflow), boolInt(aggregate.ReleasedOrFailedOverflow)}
}

// AdvanceManagedTerminationDeliveryAggregate commits one bounded keyset chunk.
// It returns true only after a committed read proves there are no candidates
// after the stored enqueue-sequence/message-ID checkpoint.
func (s *Store) AdvanceManagedTerminationDeliveryAggregate(ctx context.Context, actionID string, batchSize int) (bool, error) {
	if actionID == "" {
		return false, errors.New("managed termination action is required")
	}
	if batchSize <= 0 || batchSize > managedArchiveAggregateBatchSize {
		batchSize = managedArchiveAggregateBatchSize
	}
	var complete bool
	err := s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Kind != "terminate" || action.State != "cleanup" || (action.Stage != "direct_mail_retire" && action.Stage != "agent_cleanup" && action.Stage != "durable_write") || action.ArchiveKey == "" || action.DeliveryFrozenAt == "" {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		if action.DeliveryAggregateComplete {
			complete = true
			return nil
		}
		var name string
		if err := s.db.QueryRowContext(ctx, `SELECT name FROM managed_agents WHERE agent_id=? AND desired_state='terminated'`, action.AgentID).Scan(&name); err != nil {
			return err
		}
		messages, err := managedArchiveMessagesTx(ctx, s.db, action.AgentID, name, action.DeliveryCutoffEnqueueSequence, action.DeliveryCheckpointEnqueueSequence, action.DeliveryCheckpointMessageID, batchSize)
		if err != nil {
			return err
		}
		aggregate := action.DeliveryAggregate
		if len(messages) == 0 {
			aggregate.Disposition = managedArchiveDisposition(aggregate)
			args := managedArchiveAggregateUpdateArgs(aggregate)
			args = append(args, true, action.ActionID)
			_, err = s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET delivery_aggregate_disposition=?,delivery_aggregate_total=?,delivery_aggregate_queued_never_attempted=?,delivery_aggregate_in_flight_or_unknown=?,delivery_aggregate_completed=?,delivery_aggregate_released_or_failed=?,delivery_aggregate_total_overflow=?,delivery_aggregate_queued_never_attempted_overflow=?,delivery_aggregate_in_flight_or_unknown_overflow=?,delivery_aggregate_completed_overflow=?,delivery_aggregate_released_or_failed_overflow=?,delivery_aggregate_complete=? WHERE action_id=?`, args...)
			complete = err == nil
			return err
		}
		for _, message := range messages {
			switch managedArchiveMessageClassTx(ctx, s.db, message) {
			case "queued_never_attempted":
				if aggregate.QueuedNeverAttempted >= managedArchiveCountCap {
					aggregate.QueuedNeverAttemptedOverflow = true
				} else {
					aggregate.QueuedNeverAttempted++
				}
			case "in_flight_or_unknown":
				if aggregate.InFlightOrUnknown >= managedArchiveCountCap {
					aggregate.InFlightOrUnknownOverflow = true
				} else {
					aggregate.InFlightOrUnknown++
				}
			case "completed":
				if aggregate.Completed >= managedArchiveCountCap {
					aggregate.CompletedOverflow = true
				} else {
					aggregate.Completed++
				}
			case "released_or_failed":
				if aggregate.ReleasedOrFailed >= managedArchiveCountCap {
					aggregate.ReleasedOrFailedOverflow = true
				} else {
					aggregate.ReleasedOrFailed++
				}
			}
			if aggregate.Total >= managedArchiveCountCap {
				aggregate.TotalOverflow = true
			} else {
				aggregate.Total++
			}
		}
		last := messages[len(messages)-1]
		aggregate.Disposition = managedArchiveDisposition(aggregate)
		remaining, err := managedArchiveMessagesTx(ctx, s.db, action.AgentID, name, action.DeliveryCutoffEnqueueSequence, last.Sequence, last.ID, 1)
		if err != nil {
			return err
		}
		complete = len(remaining) == 0
		args := managedArchiveAggregateUpdateArgs(aggregate)
		args = append(args, last.Sequence, last.ID, boolInt(complete), action.ActionID)
		_, err = s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET delivery_aggregate_disposition=?,delivery_aggregate_total=?,delivery_aggregate_queued_never_attempted=?,delivery_aggregate_in_flight_or_unknown=?,delivery_aggregate_completed=?,delivery_aggregate_released_or_failed=?,delivery_aggregate_total_overflow=?,delivery_aggregate_queued_never_attempted_overflow=?,delivery_aggregate_in_flight_or_unknown_overflow=?,delivery_aggregate_completed_overflow=?,delivery_aggregate_released_or_failed_overflow=?,delivery_checkpoint_enqueue_sequence=?,delivery_checkpoint_message_id=?,delivery_aggregate_complete=? WHERE action_id=? AND state='cleanup'`, args...)
		return err
	})
	return complete, err
}

// AggregateManagedTerminationDelivery is the descriptive alias used by
// lifecycle executors.
func (s *Store) AggregateManagedTerminationDelivery(ctx context.Context, actionID string, batchSize int) (bool, error) {
	return s.AdvanceManagedTerminationDeliveryAggregate(ctx, actionID, batchSize)
}
