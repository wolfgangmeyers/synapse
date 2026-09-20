package store

import (
	"context"
	"database/sql"
	"errors"
)

// ManagedOrderlyStopMarkerRecord identifies the runtime that the router
// stopped intentionally. The embedded fence exposes the individual runtime
// fields while Fence keeps the record convenient for exact-fence callers.
type ManagedOrderlyStopMarkerRecord struct {
	ManagedRuntimeFence
	MarkedAt  string
	Confirmed bool
}

// MarkManagedOrderlyStopped atomically closes the managed runtime projection
// and records the exact fence before the caller signals the child process.
func (s *Store) MarkManagedOrderlyStopped(ctx context.Context, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return errors.New("managed orderly stop fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents
			SET activity_state='stopped', accepting_prompts=0, slot_held=0, updated_at=?
			WHERE `+managedFenceWhere("")+` AND desired_state='running'
				AND lifecycle_state IN ('starting','running')
				AND activity_state IN ('starting','waiting','active','idle','compacting')`, append([]any{now}, managedFenceValues(fence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			var existing int
			err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_orderly_stop_markers WHERE `+managedFenceWhere(""), managedFenceValues(fence)...).Scan(&existing)
			if err != nil {
				return err
			}
			if existing == 1 {
				return nil
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_orderly_stop_markers(
			agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,marked_at
		) VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(agent_id) DO UPDATE SET
			pi_session_id=excluded.pi_session_id,
			session_epoch=excluded.session_epoch,
			process_pid=excluded.process_pid,
			process_start_marker=excluded.process_start_marker,
			process_incarnation=excluded.process_incarnation,
			runtime_generation=excluded.runtime_generation,
			manifest_revision=excluded.manifest_revision,
			manifest_hash=excluded.manifest_hash,
			marked_at=excluded.marked_at,
			confirmed=CASE WHEN managed_orderly_stop_markers.confirmed=1
				AND managed_orderly_stop_markers.pi_session_id=excluded.pi_session_id
				AND managed_orderly_stop_markers.session_epoch=excluded.session_epoch
				AND managed_orderly_stop_markers.process_pid=excluded.process_pid
				AND managed_orderly_stop_markers.process_start_marker=excluded.process_start_marker
				AND managed_orderly_stop_markers.process_incarnation=excluded.process_incarnation
				AND managed_orderly_stop_markers.runtime_generation=excluded.runtime_generation
				AND managed_orderly_stop_markers.manifest_revision=excluded.manifest_revision
				AND managed_orderly_stop_markers.manifest_hash=excluded.manifest_hash
			THEN 1 ELSE 0 END`, append(managedFenceValues(fence), now)...)
		if err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ConfirmManagedOrderlyStopped records that the exact marked process stopped
// successfully. It is idempotent and never clears a newer marker.
func (s *Store) ConfirmManagedOrderlyStopped(ctx context.Context, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return errors.New("managed orderly stop confirmation fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `UPDATE managed_orderly_stop_markers
			SET confirmed=1
			WHERE `+managedFenceWhere("")+` AND confirmed=0`, managedFenceValues(fence)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 1 {
			return nil
		}
		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_orderly_stop_markers WHERE `+managedFenceWhere(""), managedFenceValues(fence)...).Scan(&present); err != nil {
			return err
		}
		if present == 1 {
			return nil
		}
		return &ManagedRestartStaleError{AgentID: fence.AgentID}
	})
}

// PrepareManagedOrderlyStopRetry restores a failed desired-running row after
// its replacement target failed before ownership was installed.
func (s *Store) PrepareManagedOrderlyStopRetry(ctx context.Context, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return errors.New("managed orderly stop retry fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents
			SET lifecycle_state='running',activity_state='stopped',accepting_prompts=0,slot_held=1,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,updated_at=?
			WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='failed'
				AND EXISTS (
					SELECT 1 FROM managed_orderly_stop_markers m
					WHERE `+managedFenceWhere("m.")+` AND m.agent_id=managed_agents.agent_id
				)`, append([]any{formatTimestamp(s.clock.Now())}, append(managedFenceValues(fence), managedFenceValues(fence)...)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 1 {
			_, err = s.bumpManagedProjectionVersion(ctx)
		}
		return err
	})
}

// RetryManagedOrderlyStop reopens a failed marker-owned recovery action after
// the failed target was never installed. It keeps the action identity while
// assigning one new target generation.
func (s *Store) RetryManagedOrderlyStop(ctx context.Context, fence ManagedRuntimeFence, targetGeneration string) (*ManagedRootRecoveryAction, error) {
	if ctx == nil || !validManagedRuntimeFence(fence) || targetGeneration == "" {
		return nil, errors.New("managed orderly stop retry is invalid")
	}
	returnAction := (*ManagedRootRecoveryAction)(nil)
	err := s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, managedRootRecoveryActionID(fence.AgentID, fence))
		if err != nil {
			return err
		}
		if action.Stage != "failed" || action.TargetProcessPID > 0 || !sameManagedRootRecoveryFence(action, fence) || !retryableManagedOrderlyStopFailure(action.FailureCode) {
			return errors.New("managed orderly stop recovery action is not retryable")
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions
			SET target_runtime_generation=?,stage='claimed',unexpected_exit_count=0,failure_code=NULL,failure_message=NULL,updated_at=?,expires_at=NULL
			WHERE action_id=? AND stage='failed' AND target_process_pid IS NULL`, targetGeneration, formatTimestamp(s.clock.Now()), action.ActionID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			// The action read above was only a snapshot. A different store owner
			// may have claimed it before this CAS; never return that owner's action
			// to the loser, or both managers could start a replacement.
			return &ManagedRootRecoveryActionStaleError{ActionID: action.ActionID}
		}
		returnAction, err = managedRootRecoveryActionForIDTx(ctx, s.db, action.ActionID)
		return err
	})
	return returnAction, err
}

func retryableManagedOrderlyStopFailure(code string) bool {
	switch code {
	case "RECOVERY_TARGET_START_FAILED", "RECOVERY_READINESS_FAILED", "RECOVERY_LAUNCH_OWNER_MISSING":
		return true
	default:
		return false
	}
}

// ManagedOrderlyStopMarker returns the retained orderly-stop marker for an
// agent with a desired-running lifecycle row, or nil when none is eligible.
func (s *Store) ManagedOrderlyStopMarker(ctx context.Context, agentID string) (*ManagedOrderlyStopMarkerRecord, error) {
	if ctx == nil || agentID == "" {
		return nil, errors.New("managed orderly stop marker agent is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	var marker ManagedOrderlyStopMarkerRecord
	var fence ManagedRuntimeFence
	var confirmed int
	err := s.db.QueryRowContext(ctx, `SELECT
		m.agent_id,m.pi_session_id,m.session_epoch,m.process_pid,m.process_start_marker,m.process_incarnation,m.runtime_generation,m.manifest_revision,m.manifest_hash,m.marked_at,m.confirmed
		FROM managed_orderly_stop_markers m
		JOIN managed_agents a ON a.agent_id=m.agent_id
		WHERE m.agent_id=? AND a.desired_state='running' AND a.lifecycle_state IN ('starting','running','failed')`, agentID).Scan(
		&fence.AgentID, &fence.PiSessionID, &fence.SessionEpoch, &fence.ProcessPID,
		&fence.ProcessStartMarker, &fence.ProcessIncarnation, &fence.RuntimeGeneration,
		&fence.ManifestRevision, &fence.ManifestHash, &marker.MarkedAt, &confirmed)
	if err == nil {
		marker.Confirmed = confirmed == 1
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	marker.ManagedRuntimeFence = fence
	return &marker, nil
}

// ConsumeManagedOrderlyStopMarker removes only a marker owned by the exact
// supplied runtime fence. The managed row must still be desired-running, while
// its current fence may be a fresh replacement committed after startup. A
// stale caller or newer marker leaves the row in place.
func (s *Store) ConsumeManagedOrderlyStopMarker(ctx context.Context, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return errors.New("managed orderly stop consume fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `DELETE FROM managed_orderly_stop_markers
			WHERE `+managedFenceWhere("")+` AND EXISTS (
				SELECT 1 FROM managed_agents a
				WHERE a.agent_id=? AND a.desired_state='running'
					AND a.lifecycle_state IN ('starting','running')
			)`, append(managedFenceValues(fence), fence.AgentID)...)
		if err != nil {
			return err
		}
		_, err = result.RowsAffected()
		return err
	})
}
