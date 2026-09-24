package store

import (
	"context"
	"database/sql"
	"errors"
)

// ManagedParentActivityEvent is the closed, source-fenced form of Pi's native
// parent-turn lifecycle. Its sequence is assigned while decoding one Pi stdout
// stream, so a delayed settled event cannot clear a newer turn.
type ManagedParentActivityEvent struct {
	Sequence int64
	State    string
}

var ErrManagedParentActivitySourcePending = errors.New("managed parent activity source is pending")

func validManagedParentActivityEvent(event ManagedParentActivityEvent) bool {
	return event.Sequence >= 0 && (event.State == "active" || event.State == "idle")
}

// ApplyManagedParentActivity retains the latest parent-turn state beside the
// compaction state. Both use the same stdout sequence, which lets compaction
// remain visible until Pi explicitly ends it even when the parent turn settles
// first. Native events remain authoritative for long-running turn state.
// SetManagedDirectPromptActivity gives a successfully submitted direct prompt
// the same immediate visible working state as a tmux prompt. It intentionally
// does not alter the native source sequence or activity cache: the next Pi
// event still wins, while an accepted delivery can render before that event.
func (s *Store) SetManagedDirectPromptActivity(ctx context.Context, fence ManagedRuntimeFence, active bool) (bool, error) {
	if !validManagedRuntimeFence(fence) {
		return false, errors.New("managed direct activity fence is invalid")
	}
	updated := false
	err := s.runImmediate(ctx, func() error {
		var compacting, nativeActive int
		err := s.db.QueryRowContext(ctx, `SELECT compacting,active FROM managed_parent_compaction_state
			WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`,
			fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&compacting, &nativeActive)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
			}
			return err
		}
		// Legacy callers may ask for an immediate affordance before a direct turn
		// has been durably admitted. Once they clear it, derive state from every
		// durable source instead of allowing a reply receipt to clear child/tool
		// work or an unacknowledged final delivery.
		if !active {
			if err := s.recomputeManagedParentActivityTx(ctx, fence); err != nil {
				return err
			}
			if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
				return err
			}
			updated = true
			return nil
		}
		activity := managedParentProjectedActivity(compacting != 0, nativeActive != 0 || active)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running' AND activity_state IN ('active','idle','compacting')`, append([]any{activity, formatTimestamp(s.clock.Now())}, managedFenceValues(fence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		if err == nil {
			updated = true
		}
		return err
	})
	return updated, err
}

// ReconcileManagedParentIdleFromTranscript applies the completed-turn state
// only to the exact active runtime. Repeated or stale reconciliation is a no-op.
func (s *Store) ReconcileManagedParentIdleFromTranscript(ctx context.Context, fence ManagedRuntimeFence) (bool, error) {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return false, errors.New("managed transcript activity fence is invalid")
	}
	updated := false
	err := s.runImmediate(ctx, func() error {
		var sourcePresent int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_parent_compaction_state
			WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`,
			fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&sourcePresent)
		if errors.Is(err, sql.ErrNoRows) {
			var present int
			if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}
			return ErrManagedParentActivitySourcePending
		}
		if err != nil {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_compaction_state SET active=0,updated_at=?
			WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`,
			now, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='settled',settled_at=?,terminal_at=COALESCE(terminal_at,?) WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=?
			AND (state='pending_turn' OR (state='final_delivery_pending' AND NOT EXISTS (SELECT 1 FROM managed_parent_final_deliveries d WHERE d.work_id=managed_parent_work.work_id AND d.state IN ('pending','delivering'))))`, append([]any{now, now}, managedFenceValues(fence)...)...); err != nil {
			return err
		}
		changed, err := s.recomputeManagedParentActivityChangedTx(ctx, fence)
		if err != nil || !changed {
			return err
		}
		if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		updated = true
		return nil
	})
	return updated, err
}

func (s *Store) ApplyManagedParentActivity(ctx context.Context, fence ManagedRuntimeFence, event ManagedParentActivityEvent) (bool, error) {
	if !validManagedRuntimeFence(fence) || !validManagedParentActivityEvent(event) {
		return false, errors.New("managed parent activity event is invalid")
	}
	updated := false
	err := s.runImmediate(ctx, func() error {
		var lastSequence int64
		var compacting int
		err := s.db.QueryRowContext(ctx, `SELECT source_sequence,compacting FROM managed_parent_compaction_state
			WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`,
			fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&lastSequence, &compacting)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && event.Sequence <= lastSequence {
			return nil
		}

		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
			}
			return err
		}
		now := formatTimestamp(s.clock.Now())
		active := boolInt(event.State == "active")
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_parent_compaction_state(agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation,source_sequence,compacting,active,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT(agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation)
			DO UPDATE SET source_sequence=excluded.source_sequence, compacting=excluded.compacting, active=excluded.active, updated_at=excluded.updated_at`,
			fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, event.Sequence, compacting, active, now)
		if err != nil {
			return err
		}
		if event.State == "idle" {
			// Native settled is the authoritative turn boundary. Keep an already
			// queued final source active, but otherwise make the direct work a
			// non-active settled row that can be retained or completed safely.
			if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='settled',settled_at=?,terminal_at=COALESCE(terminal_at,?) WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=?
				AND (state='pending_turn' OR (state='final_delivery_pending' AND NOT EXISTS (SELECT 1 FROM managed_parent_final_deliveries d WHERE d.work_id=managed_parent_work.work_id AND d.state IN ('pending','delivering'))))`, append([]any{now, now}, managedFenceValues(fence)...)...); err != nil {
				return err
			}
		}
		changed, err := s.recomputeManagedParentActivityChangedTx(ctx, fence)
		if err != nil || !changed {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		if err == nil {
			updated = true
		}
		return err
	})
	return updated, err
}
