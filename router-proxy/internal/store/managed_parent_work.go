package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ManagedFinalDelivery is the source-backed reply work handed to the existing
// conversation path. Its fence is authoritative: a replacement cannot publish
// or acknowledge a response produced by an older runtime.
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

// QueueManagedFinalDelivery persists the final source before any conversation
// write. The source entry is the idempotency key; duplicate observations do
// not create another delivery and stale fences do not affect a replacement.
func (s *Store) QueueManagedFinalDelivery(ctx context.Context, attempt ManagedDirectAttempt, entryID, body string) (*ManagedFinalDelivery, error) {
	if ctx == nil || !validManagedFinalReply(entryID, body) {
		return nil, errors.New("managed final delivery is invalid")
	}
	var delivery *ManagedFinalDelivery
	err := s.runImmediate(ctx, func() error {
		if !validManagedRuntimeFence(attempt.Fence) || !managedParentFenceCurrentTx(ctx, s.db, attempt.Fence) {
			return ErrManagedBuiltinInvocationFenceInactive
		}
		workID, state, err := managedParentWorkForAttemptTx(ctx, s.db, attempt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // A reply before a durable direct turn is not a final source.
		}
		if err != nil || state == "completed" || state == "final_delivery_failed" || state == "interrupted" {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		id := managedFinalDeliveryID(workID, entryID)
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_parent_final_deliveries(delivery_id,work_id,source_entry_identity,body,state,created_at)
			VALUES (?,?,?,?, 'pending', ?)
			ON CONFLICT(work_id,source_entry_identity) DO NOTHING`, id, workID, entryID, body, now)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='final_delivery_pending' WHERE work_id=? AND state IN ('pending_turn','settled')`, workID)
		if err != nil {
			return err
		}
		delivery = &ManagedFinalDelivery{DeliveryID: id, WorkID: workID, Attempt: attempt, EntryID: entryID, Body: body}
		return nil
	})
	return delivery, err
}

// MarkManagedFinalDeliveryDelivered is the acknowledgment from the existing
// direct conversation publisher. It can clear only the exact source/fence that
// queued the work; a late publisher is harmless after replacement or reset.
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
		result, err := s.db.ExecContext(ctx, `UPDATE managed_parent_final_deliveries SET state='delivered',delivered_at=?
			WHERE delivery_id=? AND work_id=? AND source_entry_identity=? AND state='pending'`, now, delivery.DeliveryID, delivery.WorkID, delivery.EntryID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		changed = true
		// Keep final-delivery work active until the authoritative native settled
		// event arrives. An early publisher acknowledgement is not turn
		// settlement and must not clear the parent affordance.
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
	var compacting, nativeActive int
	err := s.db.QueryRowContext(ctx, `SELECT compacting,active FROM managed_parent_compaction_state WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&compacting, &nativeActive)
	if errors.Is(err, sql.ErrNoRows) {
		compacting, nativeActive = 0, 0
	} else if err != nil {
		return err
	}
	active, err := managedParentSourceActiveTx(ctx, s.db, fence, nativeActive != 0)
	if err != nil {
		return err
	}
	if !active {
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='completed',settled_at=COALESCE(settled_at,?),terminal_at=?
			WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=? AND state='settled'`, append([]any{now, now}, managedParentFenceArgs(fence)...)...); err != nil {
			return err
		}
	}
	activity := managedParentProjectedActivity(compacting != 0, active)
	_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running' AND activity_state IN ('active','idle','compacting')`, append([]any{activity, formatTimestamp(s.clock.Now())}, managedFenceValues(fence)...)...)
	return err
}

// RecomputeManagedParentActivity projects only durable authoritative sources.
// It is called after each source mutation and during ready/recovery; it has no
// timer and never reads transcript text.
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
		_, err := s.bumpManagedProjectionVersion(ctx)
		return err
	})
	return updated, err
}

// SettleManagedParentDirectTurn records the native direct-RPC settled boundary.
// A delivered final response then becomes terminal; an unacknowledged source
// remains active in final_delivery_pending until its publisher succeeds.
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
		if errors.Is(err, sql.ErrNoRows) || state == "completed" || state == "interrupted" {
			return nil
		}
		if err != nil {
			return err
		}
		if state == "pending_turn" {
			if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='settled',settled_at=? WHERE work_id=?`, now, workID); err != nil {
				return err
			}
		} else if state == "final_delivery_pending" {
			if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='settled',settled_at=? WHERE work_id=? AND NOT EXISTS (SELECT 1 FROM managed_parent_final_deliveries WHERE work_id=? AND state='pending')`, now, workID, workID); err != nil {
				return err
			}
		}
		return s.recomputeManagedParentActivityTx(ctx, attempt.Fence)
	})
}

// InterruptManagedParentWork closes only old-fence direct work. It is used by
// loss/replacement/termination cleanup so stale final replies cannot revive a
// new turn or leave retention unbounded.
func (s *Store) InterruptManagedParentWork(ctx context.Context, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return errors.New("managed parent work fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		_, err := s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='interrupted',terminal_at=? WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=? AND state IN ('pending_turn','settled','final_delivery_pending')`, append([]any{now}, managedParentFenceArgs(fence)...)...)
		return err
	})
}

// PendingManagedFinalDeliveries gives the current binding retryable source
// work after reconnect. A row remains pending until the direct publisher
// acknowledges it; no wall-clock inference promotes or clears it.
func (s *Store) PendingManagedFinalDeliveries(ctx context.Context, fence ManagedRuntimeFence) ([]ManagedFinalDelivery, error) {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return nil, errors.New("managed final delivery fence is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT d.delivery_id,d.work_id,d.source_entry_identity,d.body,w.message_id,w.message_incarnation_id,w.attempt_number
		FROM managed_parent_final_deliveries d JOIN managed_parent_work w ON w.work_id=d.work_id
		WHERE d.state='pending' AND w.agent_id=? AND w.pi_session_id=? AND w.session_epoch=? AND w.process_pid=? AND w.process_start_marker=? AND w.process_incarnation=? AND w.runtime_generation=? AND w.manifest_revision=? AND w.manifest_hash=? AND w.state='final_delivery_pending' ORDER BY d.created_at,d.delivery_id`, managedParentFenceArgs(fence)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ManagedFinalDelivery
	for rows.Next() {
		var value ManagedFinalDelivery
		value.Attempt.Fence = fence
		if err := rows.Scan(&value.DeliveryID, &value.WorkID, &value.EntryID, &value.Body, &value.Attempt.MessageID, &value.Attempt.MessageIncarnationID, &value.Attempt.AttemptNumber); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) pruneManagedParentWorkTx(ctx context.Context, _ string) error {
	// Pending source rows are never pruned: losing one would lose a final reply.
	_, err := s.db.ExecContext(ctx, `DELETE FROM managed_parent_work WHERE state IN ('completed','final_delivery_failed','interrupted')
		AND rowid NOT IN (SELECT rowid FROM managed_parent_work ORDER BY CASE WHEN state IN ('completed','final_delivery_failed','interrupted') THEN 0 ELSE 1 END, terminal_at DESC, rowid DESC LIMIT ?)`, maxManagedHistoryEntries)
	return err
}
