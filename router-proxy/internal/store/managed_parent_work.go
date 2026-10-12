package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	managedFinalDeliveryMaxAttempts         = 5
	managedParentWorkTerminalPerAgent       = 200
	managedParentWorkTerminalRetention      = 30 * 24 * time.Hour
	managedParentActivityReconcileBatchSize = 256
)

// ManagedFinalDelivery is the durable source-backed reply passed to the
// existing conversation publisher. The publisher's successful return is the
// delivery boundary: the underlying conversation store uses this source ID to
// make a retry idempotent when a process dies after that call returns.
type ManagedFinalDelivery struct {
	DeliveryID string
	WorkID     string
	Attempt    ManagedDirectAttempt
	EntryID    string
	Body       string
}

func managedParentWorkID(attempt ManagedDirectAttempt) string {
	parts := []string{attempt.MessageID, attempt.MessageIncarnationID, fmt.Sprintf("%d", attempt.AttemptNumber), attempt.Fence.AgentID, attempt.Fence.PiSessionID, fmt.Sprintf("%d", attempt.Fence.SessionEpoch), attempt.Fence.ProcessIncarnation, attempt.Fence.RuntimeGeneration}
	return "mpw1_" + managedParentDigest(parts...)
}
func managedFinalDeliveryID(workID, entryID string) string {
	return "mpd1_" + managedParentDigest(workID, entryID)
}
func managedParentDigest(parts ...string) string {
	var value strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&value, "%d:%s", len(part), part)
	}
	sum := sha256.Sum256([]byte(value.String()))
	return fmt.Sprintf("%x", sum[:])
}
func validManagedFinalReply(entryID, body string) bool {
	return entryID != "" && utf8.ValidString(entryID) && len([]byte(entryID)) <= 256 && !strings.ContainsAny(entryID, "\x00\r\n") && body != "" && utf8.ValidString(body) && len([]byte(body)) <= 65536
}
func managedParentFenceArgs(fence ManagedRuntimeFence) []any { return managedFenceValues(fence) }

func (s *Store) openManagedParentWorkTx(ctx context.Context, attempt ManagedDirectAttempt, now string) error {
	if !validManagedRuntimeFence(attempt.Fence) || attempt.MessageID == "" || attempt.MessageIncarnationID == "" || attempt.AttemptNumber < 1 {
		return errors.New("managed parent work is invalid")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_parent_work(
		work_id,agent_id,message_id,message_incarnation_id,attempt_number,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,state,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'pending_turn',?)
		ON CONFLICT(message_id,message_incarnation_id,attempt_number) DO NOTHING`,
		managedParentWorkID(attempt), attempt.Fence.AgentID, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber,
		attempt.Fence.PiSessionID, attempt.Fence.SessionEpoch, attempt.Fence.ProcessPID, attempt.Fence.ProcessStartMarker, attempt.Fence.ProcessIncarnation, attempt.Fence.RuntimeGeneration, attempt.Fence.ManifestRevision, attempt.Fence.ManifestHash, now)
	return err
}
func managedParentWorkForAttemptTx(ctx context.Context, db *sql.DB, attempt ManagedDirectAttempt) (string, string, error) {
	var workID, state string
	err := db.QueryRowContext(ctx, `SELECT work_id,state FROM managed_parent_work WHERE message_id=? AND message_incarnation_id=? AND attempt_number=?
		AND agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=?`,
		attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber,
		attempt.Fence.AgentID, attempt.Fence.PiSessionID, attempt.Fence.SessionEpoch, attempt.Fence.ProcessPID, attempt.Fence.ProcessStartMarker, attempt.Fence.ProcessIncarnation, attempt.Fence.RuntimeGeneration, attempt.Fence.ManifestRevision, attempt.Fence.ManifestHash).Scan(&workID, &state)
	return workID, state, err
}

// QueueManagedFinalDelivery persists source text before transport. A reply seen
// after native settlement reopens only this exact settled turn for delivery.
func (s *Store) QueueManagedFinalDelivery(ctx context.Context, attempt ManagedDirectAttempt, entryID, body string) (*ManagedFinalDelivery, error) {
	if ctx == nil || !validManagedFinalReply(entryID, body) {
		return nil, errors.New("managed final delivery is invalid")
	}
	if strings.EqualFold(attempt.Message.From, "system") {
		human := s.lastHumanRecipientTx(ctx, attempt.Fence.AgentID)
		if human != "" && !strings.EqualFold(human, "system") {
			attempt.Message.From = human
		} else {
			attempt.Message.From = "user"
		}
	}
	var delivery *ManagedFinalDelivery
	err := s.runImmediate(ctx, func() error {
		if !validManagedRuntimeFence(attempt.Fence) || !managedParentFenceCurrentTx(ctx, s.db, attempt.Fence) {
			return ErrManagedBuiltinInvocationFenceInactive
		}
		workID, state, err := managedParentWorkForAttemptTx(ctx, s.db, attempt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || state == "completed" || state == "final_delivery_failed" || state == "interrupted" {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		id := managedFinalDeliveryID(workID, entryID)
		if _, err = s.db.ExecContext(ctx, `INSERT INTO managed_parent_final_deliveries(delivery_id,work_id,source_entry_identity,body,state,next_attempt_at,created_at)
			VALUES (?,?,?,?, 'pending', ?, ?)
			ON CONFLICT(work_id,source_entry_identity) DO NOTHING`, id, workID, entryID, body, now, now); err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='final_delivery_pending',terminal_at=NULL,reply_recipient=CASE WHEN reply_recipient='' THEN ? ELSE reply_recipient END WHERE work_id=? AND state IN ('pending_turn','settled')`, attempt.Message.From, workID); err != nil {
			return err
		}
		delivery = &ManagedFinalDelivery{DeliveryID: id, WorkID: workID, Attempt: attempt, EntryID: entryID, Body: body}
		return s.recomputeManagedParentActivityTx(ctx, attempt.Fence)
	})
	return delivery, err
}

// ClaimManagedFinalDelivery atomically obtains one due outbox entry. A crash
// during a call leaves delivering state for startup recovery; it never creates
// an unbounded retry loop.
func (s *Store) ClaimManagedFinalDelivery(ctx context.Context, delivery ManagedFinalDelivery) (*ManagedFinalDelivery, error) {
	if ctx == nil || delivery.DeliveryID == "" || !validManagedRuntimeFence(delivery.Attempt.Fence) {
		return nil, errors.New("managed final delivery claim is invalid")
	}
	var claimed *ManagedFinalDelivery
	err := s.runImmediate(ctx, func() error {
		if !managedParentFenceCurrentTx(ctx, s.db, delivery.Attempt.Fence) {
			return nil
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_parent_final_deliveries SET state='delivering',attempt_count=attempt_count+1,last_attempt_at=?,next_attempt_at=NULL
			WHERE delivery_id=? AND work_id=? AND source_entry_identity=? AND state='pending' AND attempt_count<? AND (next_attempt_at IS NULL OR next_attempt_at<=?)`, now, delivery.DeliveryID, delivery.WorkID, delivery.EntryID, managedFinalDeliveryMaxAttempts, now)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		claimed = &delivery
		return nil
	})
	return claimed, err
}

// RecoverManagedFinalDeliveries makes a pre-crash in-flight transport call
// retryable. The conversation's deterministic source ID prevents a duplicate
// visible final if that call committed before the crash.
func (s *Store) RecoverManagedFinalDeliveries(ctx context.Context, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return errors.New("managed final recovery fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		if !managedParentFenceCurrentTx(ctx, s.db, fence) {
			return nil
		}
		now := formatTimestamp(s.clock.Now())
		// A crashed fifth publisher attempt has already consumed the retry budget.
		// Terminalize it here instead of making it pending-but-unclaimable forever.
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_final_deliveries SET state='failed',failure_code='reply_recovery_attempts_exhausted',next_attempt_at=NULL,terminal_at=?
			WHERE state='delivering' AND attempt_count>=? AND work_id IN (SELECT work_id FROM managed_parent_work WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=? AND state='final_delivery_pending')`, append([]any{now, managedFinalDeliveryMaxAttempts}, managedParentFenceArgs(fence)...)...); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='final_delivery_failed',terminal_at=? WHERE work_id IN (
			SELECT work_id FROM managed_parent_work WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=? AND state='final_delivery_pending'
		) AND NOT EXISTS (SELECT 1 FROM managed_parent_final_deliveries d WHERE d.work_id=managed_parent_work.work_id AND d.state IN ('pending','delivering'))`, append([]any{now}, managedParentFenceArgs(fence)...)...); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `UPDATE managed_parent_final_deliveries SET state='pending',next_attempt_at=?
			WHERE state='delivering' AND attempt_count<? AND work_id IN (SELECT work_id FROM managed_parent_work WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=? AND state='final_delivery_pending')`, append([]any{now, managedFinalDeliveryMaxAttempts}, managedParentFenceArgs(fence)...)...)
		if err != nil {
			return err
		}
		return s.recomputeManagedParentActivityTx(ctx, fence)
	})
}

func managedFinalDeliveryRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return time.Second
	case 2:
		return 5 * time.Second
	case 3:
		return 30 * time.Second
	case 4:
		return 120 * time.Second
	default:
		return 0
	}
}

// FailManagedFinalDelivery records a bounded retry or one terminal failed
// state. A failed final never strands the parent activity projection.
func (s *Store) FailManagedFinalDelivery(ctx context.Context, delivery ManagedFinalDelivery, code string) (bool, error) {
	if ctx == nil || delivery.DeliveryID == "" || !validManagedRuntimeFence(delivery.Attempt.Fence) {
		return false, errors.New("managed final delivery failure is invalid")
	}
	terminal := false
	err := s.runImmediate(ctx, func() error {
		if !managedParentFenceCurrentTx(ctx, s.db, delivery.Attempt.Fence) {
			return nil
		}
		var attempts int
		err := s.db.QueryRowContext(ctx, `SELECT attempt_count FROM managed_parent_final_deliveries WHERE delivery_id=? AND work_id=? AND source_entry_identity=? AND state='delivering'`, delivery.DeliveryID, delivery.WorkID, delivery.EntryID).Scan(&attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		now := s.clock.Now()
		terminal = attempts >= managedFinalDeliveryMaxAttempts
		state := "pending"
		var due any = formatTimestamp(now.Add(managedFinalDeliveryRetryDelay(attempts)))
		var terminalAt any
		if terminal {
			state, due, terminalAt = "failed", nil, formatTimestamp(now)
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE managed_parent_final_deliveries SET state=?,next_attempt_at=?,failure_code=?,terminal_at=? WHERE delivery_id=? AND state='delivering'`, state, due, code, terminalAt, delivery.DeliveryID); err != nil {
			return err
		}
		if terminal {
			// One work row can retain more than one source observation. Do not let
			// one exhausted publisher hide a different final that is still due.
			if _, err = s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='final_delivery_failed',terminal_at=? WHERE work_id=? AND state='final_delivery_pending' AND NOT EXISTS (SELECT 1 FROM managed_parent_final_deliveries WHERE work_id=? AND state IN ('pending','delivering'))`, terminalAt, delivery.WorkID, delivery.WorkID); err != nil {
				return err
			}
		}
		return s.recomputeManagedParentActivityTx(ctx, delivery.Attempt.Fence)
	})
	return terminal, err
}

func (s *Store) MarkManagedFinalDeliveryDelivered(ctx context.Context, delivery ManagedFinalDelivery) (bool, error) {
	if ctx == nil || delivery.DeliveryID == "" || !validManagedFinalReply(delivery.EntryID, delivery.Body) || !validManagedRuntimeFence(delivery.Attempt.Fence) {
		return false, errors.New("managed final delivery acknowledgement is invalid")
	}
	changed := false
	err := s.runImmediate(ctx, func() error {
		if !managedParentFenceCurrentTx(ctx, s.db, delivery.Attempt.Fence) {
			return nil
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_parent_final_deliveries SET state='delivered',delivered_at=?,terminal_at=? WHERE delivery_id=? AND work_id=? AND source_entry_identity=? AND state='delivering'`, now, now, delivery.DeliveryID, delivery.WorkID, delivery.EntryID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		changed = true
		if _, err = s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='settled' WHERE work_id=? AND state='final_delivery_pending' AND settled_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM managed_parent_final_deliveries WHERE work_id=? AND state IN ('pending','delivering'))`, delivery.WorkID, delivery.WorkID); err != nil {
			return err
		}
		return s.recomputeManagedParentActivityTx(ctx, delivery.Attempt.Fence)
	})
	return changed, err
}

func managedParentFenceCurrentTx(ctx context.Context, db *sql.DB, fence ManagedRuntimeFence) bool {
	var present int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present)
	return err == nil
}
func managedParentSourceActiveTx(ctx context.Context, db *sql.DB, fence ManagedRuntimeFence, nativeActive bool) (bool, error) {
	if nativeActive {
		return true, nil
	}
	var exists int
	checks := []struct {
		query string
		args  []any
	}{
		{`SELECT 1 FROM managed_parent_work WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=? AND state IN ('pending_turn','final_delivery_pending') LIMIT 1`, managedParentFenceArgs(fence)},
		{`SELECT 1 FROM managed_direct_delivery_attempts WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=? AND state IN ('direct_pending','direct_prompted') LIMIT 1`, managedParentFenceArgs(fence)},
		{`SELECT 1 FROM managed_control_requests WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=? AND state NOT IN ('delivered','failed','cancelled') LIMIT 1`, managedParentFenceArgs(fence)},
		{`SELECT 1 FROM managed_builtin_tool_invocations WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_sha256=? AND state='active' LIMIT 1`, managedParentFenceArgs(fence)},
		{`SELECT 1 FROM managed_readiness_sources WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=? AND snapshot_canonical IS NOT NULL AND EXISTS (SELECT 1 FROM json_each(snapshot_canonical, '$.runs') WHERE json_extract(value, '$.lifecycle_state') IN ('queued','running')) LIMIT 1`, managedParentFenceArgs(fence)},
	}
	for _, check := range checks {
		err := db.QueryRowContext(ctx, check.query, check.args...).Scan(&exists)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	return false, nil
}
func (s *Store) recomputeManagedParentActivityTx(ctx context.Context, fence ManagedRuntimeFence) error {
	changed, err := s.recomputeManagedParentActivityChangedTx(ctx, fence)
	if err != nil || !changed {
		return err
	}
	_, err = s.bumpManagedProjectionVersion(ctx)
	return err
}

func (s *Store) recomputeManagedParentActivityChangedTx(ctx context.Context, fence ManagedRuntimeFence) (bool, error) {
	var compacting, nativeActive int
	err := s.db.QueryRowContext(ctx, `SELECT compacting,active FROM managed_parent_compaction_state WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&compacting, &nativeActive)
	if errors.Is(err, sql.ErrNoRows) {
		compacting, nativeActive = 0, 0
	} else if err != nil {
		return false, err
	}
	active, err := managedParentSourceActiveTx(ctx, s.db, fence, nativeActive != 0)
	if err != nil {
		return false, err
	}
	projected := managedParentProjectedActivity(compacting != 0, active)
	result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running' AND activity_state IN ('active','idle','compacting') AND activity_state != ?`, append([]any{projected, formatTimestamp(s.clock.Now())}, append(managedFenceValues(fence), projected)...)...)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *Store) currentManagedParentFenceTx(ctx context.Context, agentID string) (ManagedRuntimeFence, error) {
	var fence ManagedRuntimeFence
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash
		FROM managed_agents
		WHERE agent_id=? AND desired_state='running' AND lifecycle_state='running'
			AND pi_session_id IS NOT NULL AND process_pid IS NOT NULL AND process_start_marker IS NOT NULL
			AND process_incarnation IS NOT NULL AND runtime_generation IS NOT NULL`, agentID).Scan(
		&fence.AgentID, &fence.PiSessionID, &fence.SessionEpoch, &fence.ProcessPID, &fence.ProcessStartMarker,
		&fence.ProcessIncarnation, &fence.RuntimeGeneration, &fence.ManifestRevision, &fence.ManifestHash)
	return fence, err
}

func (s *Store) recomputeManagedParentActivityForAgentTx(ctx context.Context, agentID string) error {
	fence, err := s.currentManagedParentFenceTx(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.recomputeManagedParentActivityTx(ctx, fence)
}

// ReconcileManagedParentActivity repairs stale active projections left by an
// older router. It only reads current running fences and reuses the normal
// source precedence, so native activity and replacement runtimes remain safe.
func (s *Store) ReconcileManagedParentActivity(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, errors.New("context is required")
	}
	s.managedParentActivityReconcileMu.Lock()
	defer s.managedParentActivityReconcileMu.Unlock()
	changed := 0
	nextCursor := s.managedParentActivityReconcileCursor
	nextHighWater := s.managedParentActivityReconcileHighWater
	err := s.runImmediate(ctx, func() error {
		if nextHighWater == "" {
			var highWater sql.NullString
			if err := s.db.QueryRowContext(ctx, `SELECT MAX(agent_id) FROM managed_agents
				WHERE desired_state='running' AND lifecycle_state='running' AND activity_state IN ('active','idle','compacting')
					AND pi_session_id IS NOT NULL AND process_pid IS NOT NULL AND process_start_marker IS NOT NULL
					AND process_incarnation IS NOT NULL AND runtime_generation IS NOT NULL`).Scan(&highWater); err != nil {
				return err
			}
			if !highWater.Valid {
				nextCursor = ""
				return nil
			}
			nextCursor = ""
			nextHighWater = highWater.String
		}
		rows, err := s.db.QueryContext(ctx, `SELECT agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash
			FROM managed_agents
			WHERE desired_state='running' AND lifecycle_state='running' AND activity_state IN ('active','idle','compacting')
				AND pi_session_id IS NOT NULL AND process_pid IS NOT NULL AND process_start_marker IS NOT NULL
				AND process_incarnation IS NOT NULL AND runtime_generation IS NOT NULL
				AND agent_id > ? AND agent_id <= ?
			ORDER BY agent_id LIMIT ?`, nextCursor, nextHighWater, managedParentActivityReconcileBatchSize)
		if err != nil {
			return err
		}
		var fences []ManagedRuntimeFence
		for rows.Next() {
			var fence ManagedRuntimeFence
			if err := rows.Scan(&fence.AgentID, &fence.PiSessionID, &fence.SessionEpoch, &fence.ProcessPID, &fence.ProcessStartMarker, &fence.ProcessIncarnation, &fence.RuntimeGeneration, &fence.ManifestRevision, &fence.ManifestHash); err != nil {
				_ = rows.Close()
				return err
			}
			fences = append(fences, fence)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(fences) == 0 {
			// An exhausted high-water range starts a new pass on the next sweep.
			// Rows outside this range, including churn below the cursor, are included then.
			nextCursor = ""
			nextHighWater = ""
			return nil
		}
		for _, fence := range fences {
			wasChanged, err := s.recomputeManagedParentActivityChangedTx(ctx, fence)
			if err != nil {
				return err
			}
			if wasChanged {
				changed++
			}
		}
		nextCursor = fences[len(fences)-1].AgentID
		if changed > 0 {
			_, err := s.bumpManagedProjectionVersion(ctx)
			return err
		}
		return nil
	})
	if err == nil {
		s.managedParentActivityReconcileCursor = nextCursor
		s.managedParentActivityReconcileHighWater = nextHighWater
	}
	return changed, err
}

func (s *Store) RecomputeManagedParentActivity(ctx context.Context, fence ManagedRuntimeFence) (bool, error) {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return false, errors.New("managed parent activity fence is invalid")
	}
	updated := false
	err := s.runImmediate(ctx, func() error {
		if !managedParentFenceCurrentTx(ctx, s.db, fence) {
			return nil
		}
		if err := s.recomputeManagedParentActivityTx(ctx, fence); err != nil {
			return err
		}
		updated = true
		return nil
	})
	return updated, err
}
func (s *Store) SettleManagedParentDirectTurn(ctx context.Context, attempt ManagedDirectAttempt) error {
	if ctx == nil || !validManagedRuntimeFence(attempt.Fence) {
		return errors.New("managed parent turn fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		if !managedParentFenceCurrentTx(ctx, s.db, attempt.Fence) {
			return nil
		}
		now := formatTimestamp(s.clock.Now())
		workID, state, err := managedParentWorkForAttemptTx(ctx, s.db, attempt)
		if errors.Is(err, sql.ErrNoRows) || state == "completed" || state == "interrupted" || state == "final_delivery_failed" {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state=CASE WHEN state='final_delivery_pending' AND EXISTS (SELECT 1 FROM managed_parent_final_deliveries WHERE work_id=? AND state IN ('pending','delivering')) THEN 'final_delivery_pending' ELSE 'settled' END,settled_at=COALESCE(settled_at,?),terminal_at=CASE WHEN state='final_delivery_pending' AND EXISTS (SELECT 1 FROM managed_parent_final_deliveries WHERE work_id=? AND state IN ('pending','delivering')) THEN NULL ELSE COALESCE(terminal_at,?) END WHERE work_id=?`, workID, now, workID, now, workID); err != nil {
			return err
		}
		return s.recomputeManagedParentActivityTx(ctx, attempt.Fence)
	})
}
func interruptManagedParentWorkMatchingTx(ctx context.Context, db *sql.DB, workWhere string, workArgs []any, now string) error {
	if _, err := db.ExecContext(ctx, `UPDATE managed_parent_final_deliveries SET state='interrupted',terminal_at=? WHERE work_id IN (SELECT work_id FROM managed_parent_work WHERE `+workWhere+` AND state IN ('pending_turn','settled','final_delivery_pending')) AND state IN ('pending','delivering')`, append([]any{now}, workArgs...)...); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE managed_parent_work SET state='interrupted',terminal_at=? WHERE `+workWhere+` AND state IN ('pending_turn','settled','final_delivery_pending')`, append([]any{now}, workArgs...)...)
	return err
}

func interruptManagedParentWorkTx(ctx context.Context, db *sql.DB, fence ManagedRuntimeFence, now string) error {
	return interruptManagedParentWorkMatchingTx(ctx, db, managedFenceWhere(""), managedParentFenceArgs(fence), now)
}
func (s *Store) InterruptManagedParentWork(ctx context.Context, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return errors.New("managed parent work fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		if err := interruptManagedParentWorkTx(ctx, s.db, fence, formatTimestamp(s.clock.Now())); err != nil {
			return err
		}
		if managedParentFenceCurrentTx(ctx, s.db, fence) {
			return s.recomputeManagedParentActivityTx(ctx, fence)
		}
		return nil
	})
}
func (s *Store) InterruptManagedParentTurn(ctx context.Context, attempt ManagedDirectAttempt) error {
	return s.InterruptManagedParentWork(ctx, attempt.Fence)
}
func (s *Store) PendingManagedFinalDeliveries(ctx context.Context, fence ManagedRuntimeFence) ([]ManagedFinalDelivery, error) {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return nil, errors.New("managed final delivery fence is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT d.delivery_id,d.work_id,d.source_entry_identity,d.body,w.message_id,w.message_incarnation_id,w.attempt_number,w.reply_recipient FROM managed_parent_final_deliveries d JOIN managed_parent_work w ON w.work_id=d.work_id WHERE d.state='pending' AND w.agent_id=? AND w.pi_session_id=? AND w.session_epoch=? AND w.process_pid=? AND w.process_start_marker=? AND w.process_incarnation=? AND w.runtime_generation=? AND w.manifest_revision=? AND w.manifest_hash=? AND w.state='final_delivery_pending' ORDER BY d.created_at,d.delivery_id`, managedParentFenceArgs(fence)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ManagedFinalDelivery
	for rows.Next() {
		var value ManagedFinalDelivery
		value.Attempt.Fence = fence
		if err := rows.Scan(&value.DeliveryID, &value.WorkID, &value.EntryID, &value.Body, &value.Attempt.MessageID, &value.Attempt.MessageIncarnationID, &value.Attempt.AttemptNumber, &value.Attempt.Message.From); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
func (s *Store) NextManagedFinalDeliveryRetry(ctx context.Context, agentID string) (time.Time, bool, error) {
	if ctx == nil || agentID == "" {
		return time.Time{}, false, errors.New("managed final retry agent is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var due string
	err := s.db.QueryRowContext(ctx, `SELECT MIN(d.next_attempt_at) FROM managed_parent_final_deliveries d JOIN managed_parent_work w ON w.work_id=d.work_id WHERE w.agent_id=? AND d.state='pending' AND d.next_attempt_at IS NOT NULL`, agentID).Scan(&due)
	if errors.Is(err, sql.ErrNoRows) || due == "" {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	value, err := time.Parse("2006-01-02T15:04:05.000Z", due)
	return value, err == nil, err
}
func (s *Store) pruneManagedParentWorkTx(ctx context.Context, now string) error {
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", now)
	if err != nil {
		return err
	}
	terminal := `state IN ('settled','completed','final_delivery_failed','interrupted')`
	if _, err = s.db.ExecContext(ctx, `DELETE FROM managed_parent_work WHERE `+terminal+` AND terminal_at IS NOT NULL AND terminal_at<=?`, formatTimestamp(parsed.Add(-managedParentWorkTerminalRetention))); err != nil {
		return err
	}
	// Keep a bounded terminal archive per agent. Deleting the parent cascades
	// its redacted final-delivery rows, while live work remains outside retention.
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_parent_work WHERE work_id IN (
		SELECT work_id FROM (
			SELECT work_id,ROW_NUMBER() OVER (PARTITION BY agent_id ORDER BY terminal_at DESC,work_id DESC) AS ordinal
			FROM managed_parent_work WHERE `+terminal+`
		) WHERE ordinal>?
	)`, managedParentWorkTerminalPerAgent)
	return err
}
