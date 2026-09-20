package store

import (
	"context"
	"errors"
	"time"
)

// ManagedTerminationActions returns unfinished direct termination actions in
// durable creation order. Startup recovery claims each row before it touches a
// process or direct-mail boundary.
func (s *Store) ManagedTerminationActions(ctx context.Context) ([]*ManagedLifecycleAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, managedLifecycleActionSelect+` WHERE kind='terminate' AND execution_owner='lifecycle' AND state NOT IN ('succeeded','failed','cancelled','superseded') ORDER BY created_at,action_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var actions []*ManagedLifecycleAction
	for rows.Next() {
		action, err := scanManagedLifecycleAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The store owns one SQLite connection. Close the action rows before the
	// archive-field lookups so the nested queries cannot wait on that same connection.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, action := range actions {
		if err := loadManagedLifecycleArchiveFieldsTx(ctx, s.db, action); err != nil {
			return nil, err
		}
	}
	return actions, nil
}

// ManagedSessionArtifacts returns every artifact still owned by the managed
// agent, including earlier reset sessions. Termination must prove each path is
// safely absent before its final durable transaction removes the ledger rows.
func (s *Store) ManagedSessionArtifacts(ctx context.Context, agentID string) ([]*ManagedSessionArtifactRecord, error) {
	if !isLifecycleText(agentID) {
		return nil, errors.New("managed session artifact agent is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, managedSessionArtifactSelect+` WHERE agent_id=? ORDER BY session_epoch,artifact_id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var artifacts []*ManagedSessionArtifactRecord
	for rows.Next() {
		artifact, err := scanManagedSessionArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

// AdvanceManagedTermination persists only the documented termination pipeline.
// A missing source fence is valid for a launch that was terminated before its
// first process binding; all other fences are matched exactly.
func (s *Store) AdvanceManagedTermination(ctx context.Context, actionID, fromState, fromStage, toState, toStage string, fence *ManagedRuntimeFence) error {
	if actionID == "" || !lifecycleStateStageValid(toState, toStage) || lifecycleTerminalState(toState) {
		return errors.New("managed termination transition is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Kind != "terminate" || action.State != fromState || action.Stage != fromStage || lifecycleTerminalState(action.State) {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		if (action.SourceFence == nil) != (fence == nil) || (fence != nil && (!validManagedRuntimeFence(*fence) || !sameManagedRestartFence(*action.SourceFence, *fence))) {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		args := []any{toState, toStage, formatTimestamp(s.clock.Now()), actionID, fromState, fromStage}
		where := `action_id=? AND kind='terminate' AND execution_owner='lifecycle' AND state=? AND stage=?`
		if fence == nil {
			where += ` AND source_pi_session_id IS NULL AND source_session_epoch IS NULL AND source_process_pid IS NULL AND source_process_start_marker IS NULL AND source_process_incarnation IS NULL AND source_runtime_generation IS NULL AND source_manifest_revision IS NULL AND source_manifest_hash IS NULL`
		} else {
			where += ` AND ` + managedLifecycleFenceWhere("source_")
			args = append(args, managedFenceValues(*fence)[1:]...)
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state=?,stage=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE `+where, args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		return nil
	})
}

func terminationFailureMessage(code string) string {
	return map[string]string{
		"TERMINATION_PROCESS_IDENTITY_UNSAFE": "managed termination could not verify the owned process identity",
		"TERMINATION_CLEANUP_FAILED":          "managed agent termination cleanup failed",
	}[code]
}

// FailManagedTermination preserves the exact failed stage as a retained
// tombstone. Safe cleanup boundaries are retryable; an identity mismatch is
// deliberately terminal because another process must never be signalled.
func (s *Store) FailManagedTermination(ctx context.Context, actionID, code string, retryable bool, fence *ManagedRuntimeFence) error {
	if code != "TERMINATION_PROCESS_IDENTITY_UNSAFE" && code != "TERMINATION_CLEANUP_FAILED" {
		return errors.New("managed termination failure is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Kind != "terminate" || lifecycleTerminalState(action.State) || (action.SourceFence == nil) != (fence == nil) || (fence != nil && (!validManagedRuntimeFence(*fence) || !sameManagedRestartFence(*action.SourceFence, *fence))) {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		if code == "TERMINATION_PROCESS_IDENTITY_UNSAFE" {
			if retryable || action.Stage != "terminate_reap" {
				return errors.New("managed termination identity failure is invalid")
			}
		} else if !retryable || (action.Stage != "terminate_reap" && action.Stage != "direct_mail_retire" && action.Stage != "agent_cleanup" && action.Stage != "durable_write") {
			return errors.New("managed termination cleanup failure is invalid")
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{code, terminationFailureMessage(code), boolInt(retryable), now, now, actionID, action.State, action.Stage}
		where := `action_id=? AND kind='terminate' AND execution_owner='lifecycle' AND state=? AND stage=?`
		if fence == nil {
			where += ` AND source_pi_session_id IS NULL AND source_session_epoch IS NULL AND source_process_pid IS NULL AND source_process_start_marker IS NULL AND source_process_incarnation IS NULL AND source_runtime_generation IS NULL AND source_manifest_revision IS NULL AND source_manifest_hash IS NULL`
		} else {
			where += ` AND ` + managedLifecycleFenceWhere("source_")
			args = append(args, managedFenceValues(*fence)[1:]...)
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state='failed',terminal_code=?,terminal_message=?,retryable=?,completed_at=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE `+where, args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='failed',activity_state='failed',accepting_prompts=0,last_error_code=?,last_error_message=?,last_error_at=?,updated_at=? WHERE agent_id=? AND desired_state='terminated'`, code, terminationFailureMessage(code), now, now, action.AgentID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), code, terminationFailureMessage(code), action.OperationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// LatestRetryableManagedTermination returns the exact retry predecessor after
// all replay checks have completed. No caller may infer retry lineage from a
// mutable public projection.
func (s *Store) LatestRetryableManagedTermination(ctx context.Context, agentID string) (*ManagedLifecycleAction, error) {
	if !isLifecycleText(agentID) {
		return nil, errors.New("managed termination agent is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	action, err := scanManagedLifecycleAction(s.db.QueryRowContext(ctx, managedLifecycleActionSelect+` WHERE agent_id=? AND kind='terminate' AND state='failed' AND retryable=1 AND NOT EXISTS (SELECT 1 FROM managed_lifecycle_actions successor WHERE successor.retry_of=managed_lifecycle_actions.action_id) ORDER BY completed_at DESC,action_id DESC LIMIT 1`, agentID))
	if err != nil {
		return nil, err
	}
	if err := loadManagedLifecycleArchiveFieldsTx(ctx, s.db, action); err != nil {
		return nil, err
	}
	return action, nil
}

// RecordManagedTerminationDiagnostic stores only a stable failure class. Raw
// RPC output and local paths stay outside durable HTTP-adjacent state.
func (s *Store) RecordManagedTerminationDiagnostic(ctx context.Context, actionID, code string) error {
	if actionID == "" || (code != "ABORT_TIMEOUT" && code != "ABORT_EOF" && code != "ABORT_MALFORMED" && code != "ABORT_ERROR" && code != "ABORT_UNAVAILABLE" && code != "STATE_DISCOVERY_FAILED") {
		return errors.New("managed termination diagnostic is invalid")
	}
	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `INSERT INTO managed_termination_diagnostics(action_id,code,recorded_at) VALUES (?,?,?) ON CONFLICT(action_id,code) DO NOTHING`, actionID, code, formatTimestamp(s.clock.Now()))
		return err
	})
}
