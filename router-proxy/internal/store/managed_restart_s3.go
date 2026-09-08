package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ManagedLifecycleRestartReservation is the narrow admission boundary for a
// direct restart.  It is intentionally separate from the generic lifecycle
// writer: only this method may create a new restart action without a legacy
// 025 transition.
type ManagedLifecycleRestartReservation struct {
	AgentID, Method, CanonicalPath, RequestID, CanonicalBodyHash string
	Source                                                       string
	ParentActionID                                               string
	CurrentOperationID                                           string
	SourceFence                                                  ManagedRuntimeFence
	PlannedTargetRuntimeGeneration                               string
	Operation                                                    ManagedOperation
}

// RestartStartAttemptPrefix identifies the one durable interval after a
// ProcessFactory start may have created a child but before its target fence is
// installed.  It is deliberately stored in recovery_owner so migration 027
// remains unchanged.
const RestartStartAttemptPrefix = "restart-target-start:"

func restartStartAttempt(owner string) bool {
	return strings.HasPrefix(owner, RestartStartAttemptPrefix) && len(owner) > len(RestartStartAttemptPrefix)
}

func restartReservationInputValid(reservation ManagedLifecycleRestartReservation) bool {
	if !isLifecycleText(reservation.AgentID) || reservation.Method == "" || reservation.CanonicalPath == "" || reservation.RequestID == "" || reservation.CanonicalBodyHash == "" || reservation.CurrentOperationID == "" || !validManagedRuntimeFence(reservation.SourceFence) || reservation.SourceFence.AgentID != reservation.AgentID || reservation.PlannedTargetRuntimeGeneration == "" {
		return false
	}
	if reservation.Source != "direct" && reservation.Source != "restart_all_child" {
		return false
	}
	if reservation.Source == "direct" {
		return reservation.ParentActionID == ""
	}
	return managedCanonicalUUIDRE.MatchString(reservation.ParentActionID)
}

// ReserveManagedLifecycleRestart resolves replay before it reads the agent,
// then atomically creates a lifecycle-owned restart action and closes direct
// prompt admission.  It never writes managed_restart_transitions.
func (s *Store) ReserveManagedLifecycleRestart(ctx context.Context, reservation ManagedLifecycleRestartReservation) (ManagedLifecycleAdmission, error) {
	if reservation.Operation.OperationID == "" {
		reservation.Operation.OperationID = reservation.RequestID
	}
	if reservation.Operation.Method == "" {
		reservation.Operation.Method = reservation.Method
	}
	if reservation.Operation.CanonicalPath == "" {
		reservation.Operation.CanonicalPath = reservation.CanonicalPath
	}
	if reservation.Operation.RequestID == "" {
		reservation.Operation.RequestID = reservation.RequestID
	}
	if reservation.Operation.CanonicalBodyHash == "" {
		reservation.Operation.CanonicalBodyHash = reservation.CanonicalBodyHash
	}
	if reservation.Operation.State == "" {
		reservation.Operation.State = "pending"
	}
	if reservation.Operation.CreatedAt == "" {
		reservation.Operation.CreatedAt = formatTimestamp(s.clock.Now())
	}
	if reservation.Source == "" {
		reservation.Source = "direct"
	}
	// Only request identity is checked before replay. Source fences, bindings,
	// generations, and action shape are new-admission concerns and must not
	// turn a stored replay into a live-state read or a different error.
	if reservation.Method == "" || reservation.RequestID == "" || reservation.CanonicalPath == "" || reservation.CanonicalBodyHash == "" {
		return ManagedLifecycleAdmission{}, errors.New("managed lifecycle restart reservation is invalid")
	}

	var admission ManagedLifecycleAdmission
	err := s.runImmediate(ctx, func() error {
		existing, err := managedOperationForWorkerScopeTx(ctx, s.db, reservation.Method, reservation.RequestID)
		if err == nil {
			if existing.CanonicalPath != reservation.CanonicalPath || existing.CanonicalBodyHash != reservation.CanonicalBodyHash {
				return &ManagedIdempotencyConflictError{}
			}
			admission.Operation, admission.Replayed = existing, true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !restartReservationInputValid(reservation) || reservation.Operation.OperationID != reservation.RequestID || reservation.Operation.RequestID != reservation.RequestID || reservation.Operation.Method != reservation.Method || reservation.Operation.CanonicalPath != reservation.CanonicalPath || reservation.Operation.CanonicalBodyHash != reservation.CanonicalBodyHash || reservation.Operation.State != "pending" {
			return errors.New("managed lifecycle restart reservation is invalid")
		}

		agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, reservation.AgentID), nil)
		if errors.Is(err, sql.ErrNoRows) {
			return &ManagedAgentNotFoundError{AgentID: reservation.AgentID}
		}
		if err != nil {
			return err
		}
		if !sameManagedRestartFence(restartFenceFromAgent(agent), reservation.SourceFence) || agent.DesiredState != "running" || agent.LifecycleState != "running" || !agent.AcceptingPrompts || !agent.SlotHeld || (agent.ActivityState != "active" && agent.ActivityState != "idle") {
			return &ManagedAgentNotReadyError{AgentID: reservation.AgentID}
		}
		current, err := managedOperationForTx(ctx, s.db, reservation.CurrentOperationID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && current.State != "terminal") {
			return &ManagedLifecycleConflictError{AgentID: reservation.AgentID}
		}
		if err != nil {
			return err
		}
		var active int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE agent_id=? AND state NOT IN ('succeeded','failed','cancelled','superseded')`, reservation.AgentID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return &ManagedLifecycleConflictError{AgentID: reservation.AgentID}
		}
		var legacy int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_restart_transitions WHERE agent_id=?`, reservation.AgentID).Scan(&legacy); err != nil {
			return err
		}
		if legacy != 0 {
			return &ManagedLifecycleConflictError{AgentID: reservation.AgentID}
		}
		if reservation.Source == "restart_all_child" {
			parent, parentErr := managedLifecycleActionForTx(ctx, s.db, reservation.ParentActionID)
			if parentErr != nil || parent.Kind != "restart_all" {
				if parentErr != nil {
					return parentErr
				}
				return &ManagedLifecycleConflictError{AgentID: reservation.AgentID}
			}
		}
		if err := insertManagedOperation(ctx, s.db, reservation.Operation); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{reservation.Operation.OperationID, reservation.Operation.OperationID, reservation.AgentID, "restart", reservation.Source, nullIfEmpty(reservation.ParentActionID), nil, "lifecycle", "admitted", "admission"}
		args = append(args, lifecycleFenceValues(&reservation.SourceFence)...)
		args = append(args, lifecycleFenceValues(nil)...)
		args = append(args, reservation.PlannedTargetRuntimeGeneration, nil, now, now)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions (
			action_id,operation_id,agent_id,kind,source,parent_action_id,retry_of,execution_owner,state,stage,
			source_pi_session_id,source_session_epoch,source_process_pid,source_process_start_marker,source_process_incarnation,source_runtime_generation,source_manifest_revision,source_manifest_hash,
			target_pi_session_id,target_session_epoch,target_process_pid,target_process_start_marker,target_process_incarnation,target_runtime_generation,target_manifest_revision,target_manifest_hash,
			planned_target_runtime_generation,target_artifact_id,created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='restarting',accepting_prompts=0,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running' AND accepting_prompts=1 AND slot_held=1 AND activity_state IN ('active','idle')`, append([]any{now}, managedFenceValues(reservation.SourceFence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedAgentNotReadyError{AgentID: reservation.AgentID}
		}
		if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		action, err := managedLifecycleActionForTx(ctx, s.db, reservation.Operation.OperationID)
		if err != nil {
			return err
		}
		admission.Operation, admission.Action, admission.Agent = &reservation.Operation, action, agent
		return nil
	})
	return admission, err
}

func restartActionForTx(ctx context.Context, db *sql.DB, actionID string) (*ManagedLifecycleAction, error) {
	action, err := managedLifecycleActionForTx(ctx, db, actionID)
	if err != nil {
		return nil, err
	}
	if action.Kind != "restart" || action.ExecutionOwner != "lifecycle" || action.SourceFence == nil || !validManagedRuntimeFence(*action.SourceFence) {
		return nil, &ManagedRestartStaleError{AgentID: action.AgentID}
	}
	return action, nil
}

func retainedRestartArtifactTx(ctx context.Context, db *sql.DB, action *ManagedLifecycleAction) error {
	if action == nil || action.SourceFence == nil || !managedCanonicalUUIDRE.MatchString(action.TargetArtifactID) {
		return &ManagedRestartStaleError{AgentID: action.AgentID}
	}
	var artifactID string
	err := db.QueryRowContext(ctx, `SELECT artifact_id FROM managed_session_artifacts WHERE artifact_id=? AND agent_id=? AND session_epoch=? AND pi_session_id=? AND discovery_state='materialized' AND cleanup_state='retained'`, action.TargetArtifactID, action.AgentID, action.SourceFence.SessionEpoch, action.SourceFence.PiSessionID).Scan(&artifactID)
	if err != nil || artifactID != action.TargetArtifactID {
		if err != nil {
			return err
		}
		return &ManagedRestartStaleError{AgentID: action.AgentID}
	}
	return nil
}

func restartActionFence(action *ManagedLifecycleAction) (*ManagedRuntimeFence, string) {
	if action != nil && lifecycleTargetStage(action.Stage) && action.TargetFence != nil {
		return action.TargetFence, "target_"
	}
	if action == nil {
		return nil, "source_"
	}
	return action.SourceFence, "source_"
}

func (s *Store) updateLifecycleRestartActionTx(ctx context.Context, db *sql.DB, action *ManagedLifecycleAction, fromState, fromStage, toState, toStage string, fence ManagedRuntimeFence) error {
	if action == nil || action.State != fromState || action.Stage != fromStage || !lifecycleStateStageValid(toState, toStage) {
		return &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	expected, prefix := restartActionFence(action)
	if expected == nil || !sameManagedRestartFence(*expected, fence) {
		return &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	args := []any{toState, toStage, formatTimestamp(s.clock.Now()), action.ActionID, action.OperationID, fromState, fromStage}
	args = append(args, managedFenceValues(fence)[1:]...)
	result, err := db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state=?,stage=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND operation_id=? AND kind='restart' AND execution_owner='lifecycle' AND state=? AND stage=? AND `+managedLifecycleFenceWhere(prefix), args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	return nil
}

// AttachManagedLifecycleRestartResumeArtifact records the one materialized,
// retained source-session file that authorizes ResumeExisting.  The store
// checks durable ownership while the source process is still fenced.
func (s *Store) AttachManagedLifecycleRestartResumeArtifact(ctx context.Context, actionID string, source ManagedRuntimeFence, artifactID string) error {
	if !validManagedRuntimeFence(source) || !managedCanonicalUUIDRE.MatchString(artifactID) {
		return errors.New("managed lifecycle restart artifact is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "admitted" || action.Stage != "admission" || action.TargetArtifactID != "" || !sameManagedRestartFence(*action.SourceFence, source) {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		var found string
		if err := s.db.QueryRowContext(ctx, `SELECT artifact_id FROM managed_session_artifacts WHERE artifact_id=? AND agent_id=? AND session_epoch=? AND pi_session_id=? AND discovery_state='materialized' AND cleanup_state='retained'`, artifactID, source.AgentID, source.SessionEpoch, source.PiSessionID).Scan(&found); err != nil || found != artifactID {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET target_artifact_id=?,updated_at=? WHERE action_id=? AND operation_id=? AND kind='restart' AND execution_owner='lifecycle' AND state='admitted' AND stage='admission' AND target_artifact_id IS NULL AND `+managedLifecycleFenceWhere("source_"), append([]any{artifactID, formatTimestamp(s.clock.Now()), actionID, action.OperationID}, managedFenceValues(source)[1:]...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		return nil
	})
}

func (s *Store) AdvanceManagedLifecycleRestartToSettle(ctx context.Context, actionID string, source ManagedRuntimeFence) error {
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if err := retainedRestartArtifactTx(ctx, s.db, action); err != nil {
			return err
		}
		return s.updateLifecycleRestartActionTx(ctx, s.db, action, "admitted", "admission", "awaiting_settle", "source_settle", source)
	})
}

// AdvanceManagedLifecycleRestartToSourceStop permits an idle admitted source or
// a settled source.  Both paths require the attached continuation artifact;
// shutdown uses the same transaction and never bypasses that handoff.
func (s *Store) AdvanceManagedLifecycleRestartToSourceStop(ctx context.Context, actionID string, source ManagedRuntimeFence) error {
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if err := retainedRestartArtifactTx(ctx, s.db, action); err != nil {
			return err
		}
		switch {
		case action.State == "admitted" && action.Stage == "admission":
			return s.updateLifecycleRestartActionTx(ctx, s.db, action, "admitted", "admission", "stopping_source", "source_stop", source)
		case action.State == "awaiting_settle" && action.Stage == "source_settle":
			return s.updateLifecycleRestartActionTx(ctx, s.db, action, "awaiting_settle", "source_settle", "stopping_source", "source_stop", source)
		default:
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
	})
}

// MarkManagedLifecycleRestartSourceStopped persists source-group absence before
// target creation and changes the action and public projection together.
func (s *Store) MarkManagedLifecycleRestartSourceStopped(ctx context.Context, actionID string, source ManagedRuntimeFence) error {
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "stopping_source" || action.Stage != "source_stop" || !sameManagedRestartFence(*action.SourceFence, source) {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		if err := retainedRestartArtifactTx(ctx, s.db, action); err != nil {
			return err
		}
		if err := s.updateLifecycleRestartActionTx(ctx, s.db, action, "stopping_source", "source_stop", "starting_target", "target_start", source); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state='stopped',lifecycle_state='restarting',accepting_prompts=0,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='restarting' AND accepting_prompts=0 AND slot_held=1`, append([]any{now}, managedFenceValues(source)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ClaimManagedLifecycleRestartTargetStart writes the durable pre-start marker.
// A marker is not an ordinary recovery lease and must survive startup cleanup.
func (s *Store) ClaimManagedLifecycleRestartTargetStart(ctx context.Context, actionID, attemptID string, source ManagedRuntimeFence, plannedGeneration string) error {
	if !validManagedRuntimeFence(source) || attemptID == "" || plannedGeneration == "" {
		return errors.New("managed lifecycle restart target start is invalid")
	}
	owner := RestartStartAttemptPrefix + attemptID
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "starting_target" || action.Stage != "target_start" || action.TargetFence != nil || action.RecoveryOwner != "" || action.PlannedTargetRuntimeGeneration != plannedGeneration || !sameManagedRestartFence(*action.SourceFence, source) {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		if err := retainedRestartArtifactTx(ctx, s.db, action); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET recovery_owner=?,recovery_claimed_at=?,updated_at=? WHERE action_id=? AND operation_id=? AND kind='restart' AND execution_owner='lifecycle' AND state='starting_target' AND stage='target_start' AND recovery_owner IS NULL AND recovery_claimed_at IS NULL AND `+managedLifecycleNullFenceWhere("target_")+` AND `+managedLifecycleFenceWhere("source_"), append([]any{owner, now, now, actionID, action.OperationID}, append(managedFenceValues(source)[1:], []any{}...)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		return nil
	})
}

// ReleaseManagedLifecycleRestartTargetStartAttemptWithoutChild clears the
// marker only when ProcessFactory has proven that no child was created.
func (s *Store) ReleaseManagedLifecycleRestartTargetStartAttemptWithoutChild(ctx context.Context, actionID, attemptID string, source ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(source) || attemptID == "" {
		return errors.New("managed lifecycle restart start release is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "starting_target" || action.Stage != "target_start" || action.TargetFence != nil || action.RecoveryOwner != RestartStartAttemptPrefix+attemptID || !sameManagedRestartFence(*action.SourceFence, source) {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET recovery_owner=NULL,recovery_claimed_at=NULL,updated_at=? WHERE action_id=? AND operation_id=? AND kind='restart' AND execution_owner='lifecycle' AND state='starting_target' AND stage='target_start' AND recovery_owner=? AND `+managedLifecycleNullFenceWhere("target_")+` AND `+managedLifecycleFenceWhere("source_"), append([]any{formatTimestamp(s.clock.Now()), actionID, action.OperationID, RestartStartAttemptPrefix + attemptID}, managedFenceValues(source)[1:]...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		return nil
	})
}

// InstallManagedLifecycleRestartTarget consumes the exact pre-start marker as
// it installs the target fence and rolls readiness evidence in one transaction.
func (s *Store) InstallManagedLifecycleRestartTarget(ctx context.Context, actionID, attemptID string, source, target ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(source) || !validManagedRuntimeFence(target) || source.AgentID != target.AgentID || source.PiSessionID != target.PiSessionID || source.SessionEpoch != target.SessionEpoch || source.ProcessIncarnation == target.ProcessIncarnation || attemptID == "" {
		return errors.New("managed lifecycle restart target is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "starting_target" || action.Stage != "target_start" || action.TargetFence != nil || action.RecoveryOwner != RestartStartAttemptPrefix+attemptID || action.PlannedTargetRuntimeGeneration != target.RuntimeGeneration || !sameManagedRestartFence(*action.SourceFence, source) {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		if err := retainedRestartArtifactTx(ctx, s.db, action); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{target.PiSessionID, target.SessionEpoch, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration, target.ManifestRevision, target.ManifestHash, now, actionID, action.OperationID, RestartStartAttemptPrefix + attemptID}
		args = append(args, managedFenceValues(source)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET target_pi_session_id=?,target_session_epoch=?,target_process_pid=?,target_process_start_marker=?,target_process_incarnation=?,target_runtime_generation=?,target_manifest_revision=?,target_manifest_hash=?,state='awaiting_target_ready',stage='target_ready',recovery_owner=NULL,recovery_claimed_at=NULL,updated_at=? WHERE action_id=? AND operation_id=? AND kind='restart' AND execution_owner='lifecycle' AND state='starting_target' AND stage='target_start' AND recovery_owner=? AND `+managedLifecycleNullFenceWhere("target_")+` AND `+managedLifecycleFenceWhere("source_"), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,manifest_revision=?,manifest_hash=?,lifecycle_state='restarting',activity_state='starting',accepting_prompts=0,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='restarting' AND activity_state='stopped' AND accepting_prompts=0 AND slot_held=1`, append([]any{target.PiSessionID, target.SessionEpoch, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration, target.ManifestRevision, target.ManifestHash, now}, managedFenceValues(source)...)...)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		if err := s.rollManagedReadinessSourceFenceTx(ctx, target); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ResetManagedLifecycleRestartTargetToStopped requires an already absent or
// safely reaped target.  The caller owns that process proof; this transaction
// only performs the fenced durable handoff back to unmarked target_start.
func (s *Store) ResetManagedLifecycleRestartTargetToStopped(ctx context.Context, actionID string, source, target ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(source) || !validManagedRuntimeFence(target) || source.AgentID != target.AgentID {
		return errors.New("managed lifecycle restart target reset is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "awaiting_target_ready" || action.Stage != "target_ready" || action.TargetFence == nil || !sameManagedRestartFence(*action.SourceFence, source) || !sameManagedRestartFence(*action.TargetFence, target) {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		if err := retainedRestartArtifactTx(ctx, s.db, action); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{now, actionID, action.OperationID}
		args = append(args, managedFenceValues(target)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET target_pi_session_id=NULL,target_session_epoch=NULL,target_process_pid=NULL,target_process_start_marker=NULL,target_process_incarnation=NULL,target_runtime_generation=NULL,target_manifest_revision=NULL,target_manifest_hash=NULL,state='starting_target',stage='target_start',recovery_owner=NULL,recovery_claimed_at=NULL,updated_at=? WHERE action_id=? AND operation_id=? AND kind='restart' AND execution_owner='lifecycle' AND state='awaiting_target_ready' AND stage='target_ready' AND `+managedLifecycleFenceWhere("target_"), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,manifest_revision=?,manifest_hash=?,lifecycle_state='restarting',activity_state='stopped',accepting_prompts=0,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='restarting' AND slot_held=1`, append([]any{source.PiSessionID, source.SessionEpoch, source.ProcessPID, source.ProcessStartMarker, source.ProcessIncarnation, source.RuntimeGeneration, source.ManifestRevision, source.ManifestHash, now}, managedFenceValues(target)...)...)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		if err := s.rollManagedReadinessSourceFenceTx(ctx, source); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func restartFailureMessage(code string) string {
	return map[string]string{
		"RESTART_PREPARATION_FAILED":    "restart preparation failed",
		"RESTART_RECOVERY_NEEDS_SETTLE": "restart requires a live turn settle",
		"RESTART_IDENTITY_UNSAFE":       "restart process identity could not be verified",
		"RESTART_STOP_FAILED":           "restart process stop did not complete",
		"RESTART_TARGET_REAP_FAILED":    "replacement process cleanup did not complete",
	}[code]
}

func restartFailureAllowed(code, stage string) bool {
	if restartFailureMessage(code) == "" {
		return false
	}
	switch code {
	case "RESTART_PREPARATION_FAILED":
		return stage == "admission"
	case "RESTART_RECOVERY_NEEDS_SETTLE":
		return stage == "source_settle"
	case "RESTART_STOP_FAILED":
		return stage == "source_stop"
	case "RESTART_TARGET_REAP_FAILED":
		return stage == "target_ready"
	case "RESTART_IDENTITY_UNSAFE":
		return stage == "admission" || stage == "source_settle" || stage == "source_stop" || stage == "target_start" || stage == "target_ready"
	}
	return false
}

// RecordManagedLifecycleRestartFailure terminalizes exact lifecycle-owned
// restart work with fixed public-safe outcomes.  Preparation failure restores
// the source projection before it records the terminal operation.
func (s *Store) RecordManagedLifecycleRestartFailure(ctx context.Context, actionID, code string, fence ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(fence) || restartFailureMessage(code) == "" {
		return errors.New("managed lifecycle restart failure is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if lifecycleTerminalState(action.State) || !restartFailureAllowed(code, action.Stage) {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		expected, prefix := restartActionFence(action)
		if expected == nil || !sameManagedRestartFence(*expected, fence) {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{code, restartFailureMessage(code), now, now, actionID, action.OperationID, action.State, action.Stage}
		args = append(args, managedFenceValues(fence)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state='failed',terminal_code=?,terminal_message=?,retryable=0,completed_at=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND operation_id=? AND kind='restart' AND execution_owner='lifecycle' AND state=? AND stage=? AND `+managedLifecycleFenceWhere(prefix), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		if code == "RESTART_PREPARATION_FAILED" {
			result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='running',accepting_prompts=1,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='restarting' AND accepting_prompts=0 AND slot_held=1`, append([]any{now}, managedFenceValues(fence)...)...)
		} else {
			result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='failed',activity_state='failed',accepting_prompts=0,last_error_code=?,last_error_message=?,last_error_at=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND slot_held=1`, append([]any{code, restartFailureMessage(code), now, now}, managedFenceValues(fence)...)...)
		}
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), code, restartFailureMessage(code), action.OperationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// RecordManagedLifecycleRestartReplacementDiagnostic retains an unmarked
// target_start action after a verified cleanup.  It intentionally leaves the
// operation pending so one later startup recovery can resume that same action.
func (s *Store) RecordManagedLifecycleRestartReplacementDiagnostic(ctx context.Context, actionID string, source ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(source) {
		return errors.New("managed lifecycle restart diagnostic is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "starting_target" || action.Stage != "target_start" || action.TargetFence != nil || action.RecoveryOwner != "" || !sameManagedRestartFence(*action.SourceFence, source) {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET last_error_code='RESTART_REPLACEMENT_FAILED',last_error_message='replacement process did not become ready',last_error_at=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='restarting' AND activity_state='stopped' AND accepting_prompts=0 AND slot_held=1`, append([]any{now, now}, managedFenceValues(source)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// CompleteManagedLifecycleRestart confirms the five readiness sources before
// reopening prompt admission and terminalizing the immutable HTTP operation.
func (s *Store) CompleteManagedLifecycleRestart(ctx context.Context, actionID string, target ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(target) {
		return errors.New("managed lifecycle restart completion is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := restartActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "awaiting_target_ready" || action.Stage != "target_ready" || action.TargetFence == nil || !sameManagedRestartFence(*action.TargetFence, target) {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		if err := retainedRestartArtifactTx(ctx, s.db, action); err != nil {
			return err
		}
		if err := s.managedReadinessSourcesConfirmedForFenceTx(ctx, target); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{now, now, actionID, action.OperationID}
		args = append(args, managedFenceValues(target)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state='succeeded',stage='complete',terminal_code='RESTARTED',terminal_message='managed agent restarted',retryable=0,completed_at=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND operation_id=? AND kind='restart' AND execution_owner='lifecycle' AND state='awaiting_target_ready' AND stage='target_ready' AND `+managedLifecycleFenceWhere("target_"), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='restarting' AND activity_state='starting' AND accepting_prompts=0 AND slot_held=1`, append([]any{now}, managedFenceValues(target)...)...)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code='RESTARTED',terminal_message='managed agent restarted' WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), action.OperationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) ManagedLifecycleRestartActions(ctx context.Context) ([]*ManagedLifecycleAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, managedLifecycleActionSelect+` WHERE kind='restart' AND execution_owner='lifecycle' AND state NOT IN ('succeeded','failed','cancelled','superseded') ORDER BY created_at,action_id`)
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
	return actions, rows.Err()
}
