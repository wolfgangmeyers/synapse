package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ManagedRootRecoveryOperatorReservation binds one operator request to the
// exact failed cold-recovery row observed by the caller.
type ManagedRootRecoveryOperatorReservation struct {
	ActionID        string
	ExpectedVersion int
	Kind            string
	Owner           string
	Operation       ManagedOperation
}

type ManagedRootRecoveryOperatorAdmission struct {
	Action    *ManagedRootRecoveryAction
	Operation *ManagedOperation
	Replayed  bool
}

func retryableFailedRootRecoveryCode(code string) bool {
	return code == "ORPHAN_IDENTITY_UNSAFE" || code == "RECOVERY_RECIPE_MISMATCH"
}

func rootRecoveryActionCurrentFence(action *ManagedRootRecoveryAction) ManagedRuntimeFence {
	fence := ManagedRuntimeFence{AgentID: action.AgentID, PiSessionID: action.OldPiSessionID, SessionEpoch: int64(action.OldSessionEpoch), ProcessPID: action.OldProcessPID, ProcessStartMarker: action.OldProcessStartMarker, ProcessIncarnation: action.OldProcessIncarnation, RuntimeGeneration: action.OldRuntimeGeneration, ManifestRevision: action.OldManifestRevision, ManifestHash: action.OldManifestHash}
	if action.TargetProcessPID > 0 {
		fence.ProcessPID = action.TargetProcessPID
		fence.ProcessStartMarker = action.TargetProcessStartMarker
		fence.ProcessIncarnation = action.TargetProcessIncarnation
		fence.RuntimeGeneration = action.TargetRuntimeGeneration
	}
	return fence
}

func rootRecoveryAgentMatchesAction(agent *ManagedAgentRecord, action *ManagedRootRecoveryAction) bool {
	if agent == nil || action == nil {
		return false
	}
	current := restartFenceFromAgent(agent)
	old := ManagedRuntimeFence{AgentID: action.AgentID, PiSessionID: action.OldPiSessionID, SessionEpoch: int64(action.OldSessionEpoch), ProcessPID: action.OldProcessPID, ProcessStartMarker: action.OldProcessStartMarker, ProcessIncarnation: action.OldProcessIncarnation, RuntimeGeneration: action.OldRuntimeGeneration, ManifestRevision: action.OldManifestRevision, ManifestHash: action.OldManifestHash}
	return sameManagedRestartFence(current, old) || (action.TargetProcessPID > 0 && sameManagedRestartFence(current, rootRecoveryActionCurrentFence(action)))
}

// ReserveManagedRootRecoveryOperator inserts the ordinary bounded replay row
// and claims one exact failed root-recovery version in the same transaction.
func (s *Store) ReserveManagedRootRecoveryOperator(ctx context.Context, reservation ManagedRootRecoveryOperatorReservation) (ManagedRootRecoveryOperatorAdmission, error) {
	if reservation.Operation.OperationID == "" {
		return ManagedRootRecoveryOperatorAdmission{}, errors.New("managed root recovery operator operation is required")
	}
	if reservation.Operation.Method == "" || reservation.Operation.RequestID == "" || reservation.Operation.CanonicalPath == "" || reservation.Operation.CanonicalBodyHash == "" {
		return ManagedRootRecoveryOperatorAdmission{}, errors.New("managed root recovery operator request is incomplete")
	}
	if reservation.Operation.OperationID != reservation.Operation.RequestID || !managedCanonicalUUIDRE.MatchString(reservation.Operation.RequestID) || !managedSHA256RE.MatchString(reservation.Operation.CanonicalBodyHash) {
		return ManagedRootRecoveryOperatorAdmission{}, errors.New("managed root recovery operator request is invalid")
	}
	if reservation.Kind != "retry" && reservation.Kind != "terminate" {
		return ManagedRootRecoveryOperatorAdmission{}, errors.New("managed root recovery operator kind is invalid")
	}
	if reservation.ActionID == "" || reservation.ExpectedVersion < 1 || reservation.Owner == "" || len([]byte(reservation.Owner)) > 128 {
		return ManagedRootRecoveryOperatorAdmission{}, errors.New("managed root recovery operator reservation is invalid")
	}
	if reservation.Operation.State == "" {
		reservation.Operation.State = "pending"
	}
	if reservation.Operation.CreatedAt == "" {
		reservation.Operation.CreatedAt = formatTimestamp(s.clock.Now())
	}
	var admission ManagedRootRecoveryOperatorAdmission
	err := s.runImmediate(ctx, func() error {
		existing, err := managedOperationForWorkerScopeTx(ctx, s.db, reservation.Operation.Method, reservation.Operation.RequestID)
		if err == nil {
			if existing.CanonicalPath != reservation.Operation.CanonicalPath || existing.CanonicalBodyHash != reservation.Operation.CanonicalBodyHash {
				return &ManagedIdempotencyConflictError{}
			}
			admission.Operation, admission.Replayed = existing, true
			action, actionErr := managedRootRecoveryActionForIDTx(ctx, s.db, reservation.ActionID)
			if actionErr == nil {
				admission.Action = action
			} else if !errors.Is(actionErr, sql.ErrNoRows) {
				return actionErr
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, reservation.ActionID)
		if err != nil {
			return err
		}
		if action.Version != reservation.ExpectedVersion || action.Stage != "failed" || !retryableFailedRootRecoveryCode(action.FailureCode) || (action.OperatorStage != "" && action.OperatorStage != "terminal") {
			return &ManagedLifecycleConflictError{AgentID: action.AgentID}
		}
		agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, action.AgentID), nil)
		if err != nil {
			return err
		}
		if agent.DesiredState != "running" || agent.LifecycleState != "failed" || agent.AcceptingPrompts || !rootRecoveryAgentMatchesAction(agent, action) {
			return &ManagedLifecycleConflictError{AgentID: action.AgentID}
		}
		if err := insertManagedOperation(ctx, s.db, reservation.Operation); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions
			SET operator_operation_id=?,operator_kind=?,operator_stage='reserved',operator_owner=?,operator_owner_epoch=COALESCE(operator_owner_epoch,0)+1,operator_agent_updated_at=?,version=version+1,updated_at=?
			WHERE action_id=? AND version=? AND stage='failed' AND failure_code IN ('ORPHAN_IDENTITY_UNSAFE','RECOVERY_RECIPE_MISMATCH') AND (operator_stage IS NULL OR operator_stage='terminal')`, reservation.Operation.OperationID, reservation.Kind, reservation.Owner, agent.UpdatedAt, now, action.ActionID, reservation.ExpectedVersion)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedLifecycleConflictError{AgentID: action.AgentID}
		}
		admission.Action, err = managedRootRecoveryActionForIDTx(ctx, s.db, action.ActionID)
		admission.Operation = &reservation.Operation
		return err
	})
	return admission, err
}

func (s *Store) LatestRetryableManagedRootRecovery(ctx context.Context, agentID string) (*ManagedRootRecoveryAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return managedRootRecoveryActionQuery(ctx, s.db, `WHERE agent_id=? AND stage='failed' AND failure_code IN ('ORPHAN_IDENTITY_UNSAFE','RECOVERY_RECIPE_MISMATCH') ORDER BY updated_at DESC,action_id DESC LIMIT 1`, agentID)
}

func (s *Store) ManagedRootRecoveryOperators(ctx context.Context) ([]*ManagedRootRecoveryAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+managedRootRecoveryActionColumns+` FROM managed_root_recovery_actions WHERE operator_stage IS NOT NULL AND operator_stage<>'terminal' ORDER BY updated_at,action_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var actions []*ManagedRootRecoveryAction
	for rows.Next() {
		action, err := scanManagedRootRecoveryAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

func scanManagedRootRecoveryAction(scanner interface{ Scan(...any) error }) (*ManagedRootRecoveryAction, error) {
	var action ManagedRootRecoveryAction
	var targetPID, unexpected, operatorOwnerEpoch sql.NullInt64
	var targetMarker, targetIncarnation, failureCode, failureMessage, operatorOperationID, operatorKind, operatorStage, operatorOwner, operatorAgentUpdatedAt, expires sql.NullString
	if err := scanner.Scan(&action.ActionID, &action.AgentID, &action.OldPiSessionID, &action.OldSessionEpoch, &action.OldProcessPID, &action.OldProcessStartMarker, &action.OldProcessIncarnation, &action.OldRuntimeGeneration, &action.OldManifestRevision, &action.OldManifestHash, &action.RecipeSHA256, &action.TargetRuntimeGeneration, &targetPID, &targetMarker, &targetIncarnation, &action.Stage, &unexpected, &failureCode, &failureMessage, &action.Version, &operatorOperationID, &operatorKind, &operatorStage, &operatorOwner, &operatorOwnerEpoch, &operatorAgentUpdatedAt, &action.CreatedAt, &action.UpdatedAt, &expires); err != nil {
		return nil, err
	}
	action.TargetProcessPID, action.TargetProcessStartMarker, action.TargetProcessIncarnation = int(targetPID.Int64), targetMarker.String, targetIncarnation.String
	action.UnexpectedExitCount, action.FailureCode, action.FailureMessage, action.ExpiresAt = int(unexpected.Int64), failureCode.String, failureMessage.String, expires.String
	action.OperatorOperationID, action.OperatorKind, action.OperatorStage, action.OperatorOwner = operatorOperationID.String, operatorKind.String, operatorStage.String, operatorOwner.String
	action.OperatorOwnerEpoch, action.OperatorAgentUpdatedAt = int(operatorOwnerEpoch.Int64), operatorAgentUpdatedAt.String
	return &action, nil
}

func (s *Store) TakeoverManagedRootRecoveryOperator(ctx context.Context, actionID string, version, ownerEpoch int, owner string) (*ManagedRootRecoveryAction, error) {
	if actionID == "" || version < 1 || ownerEpoch < 1 || owner == "" {
		return nil, errors.New("managed root recovery operator takeover is invalid")
	}
	var claimed *ManagedRootRecoveryAction
	err := s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.OperatorOwner == owner {
			claimed = action
			return nil
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET operator_owner=?,operator_owner_epoch=operator_owner_epoch+1,version=version+1,updated_at=? WHERE action_id=? AND version=? AND operator_owner_epoch=? AND operator_stage IN ('reserved','identity_resolved','executing')`, owner, formatTimestamp(s.clock.Now()), actionID, version, ownerEpoch)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		claimed, err = managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		return err
	})
	return claimed, err
}

func (s *Store) AdvanceManagedRootRecoveryOperator(ctx context.Context, actionID, owner string, ownerEpoch, version int, from, to string) (*ManagedRootRecoveryAction, error) {
	if actionID == "" || owner == "" || ownerEpoch < 1 || version < 1 || !((from == "reserved" && to == "identity_resolved") || (from == "identity_resolved" && to == "executing")) {
		return nil, errors.New("managed root recovery operator transition is invalid")
	}
	var advanced *ManagedRootRecoveryAction
	err := s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET operator_stage=?,version=version+1,updated_at=? WHERE action_id=? AND operator_owner=? AND operator_owner_epoch=? AND version=? AND operator_stage=?`, to, formatTimestamp(s.clock.Now()), actionID, owner, ownerEpoch, version, from)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		advanced, err = managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		return err
	})
	return advanced, err
}

func (s *Store) FailManagedRootRecoveryOperator(ctx context.Context, actionID, owner string, ownerEpoch, version, status int, headers map[string]string, body []byte, code, message string) error {
	if actionID == "" || owner == "" || ownerEpoch < 1 || version < 1 || status < 400 || len(body) > 64*1024 || code == "" || message == "" {
		return errors.New("managed root recovery operator failure is invalid")
	}
	encodedHeaders, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET operator_stage='terminal',version=version+1,updated_at=?,expires_at=? WHERE action_id=? AND operator_owner=? AND operator_owner_epoch=? AND version=? AND operator_stage IN ('reserved','identity_resolved','executing')`, now, formatTimestamp(s.clock.Now().Add(managedRootRecoveryRetention)), actionID, owner, ownerEpoch, version)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',response_status=?,response_headers_json=?,response_body=?,terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, status, string(encodedHeaders), string(body), now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), code, message, action.OperatorOperationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		return nil
	})
}

func replaceManagedLaunchRecipeTx(ctx context.Context, db *sql.DB, agentID string, recipe ManagedLaunchRecipe, now string) error {
	if err := ValidateManagedLaunchRecipe(recipe); err != nil {
		return err
	}
	testJSON, err := json.Marshal(recipe.TestOnlyExtensions)
	if err != nil {
		return err
	}
	envJSON, err := json.Marshal(recipe.Environment)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE managed_launch_recipes SET pi_executable=?,session_root=?,cwd=?,provider=?,model_id=?,mecha_rpc_extension=?,pi_subagents_extension=?,cursor_extension=?,antigravity_extension=?,test_extensions_json=?,environment_json=?,recipe_sha256=?,updated_at=? WHERE agent_id=?`, recipe.PiExecutable, recipe.SessionRoot, recipe.Cwd, recipe.Provider, recipe.ModelID, recipe.MechaRPCExtension, recipe.PiSubagentsExtension, recipe.CursorExtension, recipe.AntigravityExtension, string(testJSON), string(envJSON), recipe.SHA256, now, agentID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("managed launch recipe is unavailable")
	}
	return nil
}

func (s *Store) PrepareManagedRootRecoveryOperatorRetry(ctx context.Context, actionID, owner string, ownerEpoch, version int, recipe ManagedLaunchRecipe, targetGeneration string) (*ManagedRootRecoveryAction, *ManagedAgentRecord, error) {
	if actionID == "" || owner == "" || ownerEpoch < 1 || version < 1 || targetGeneration == "" {
		return nil, nil, errors.New("managed root recovery operator retry is invalid")
	}
	if err := ValidateManagedLaunchRecipe(recipe); err != nil {
		return nil, nil, err
	}
	var prepared *ManagedRootRecoveryAction
	var agent *ManagedAgentRecord
	err := s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.OperatorKind != "retry" || action.OperatorStage != "identity_resolved" || action.OperatorOwner != owner || action.OperatorOwnerEpoch != ownerEpoch || action.Version != version || action.Stage != "failed" || !retryableFailedRootRecoveryCode(action.FailureCode) {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		agent, err = scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, action.AgentID), nil)
		if err != nil {
			return err
		}
		if agent.UpdatedAt != action.OperatorAgentUpdatedAt || agent.DesiredState != "running" || agent.LifecycleState != "failed" || !rootRecoveryAgentMatchesAction(agent, action) {
			return &ManagedLifecycleConflictError{AgentID: action.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		if err := replaceManagedLaunchRecipeTx(ctx, s.db, action.AgentID, recipe, now); err != nil {
			return err
		}
		current := restartFenceFromAgent(agent)
		old := ManagedRuntimeFence{AgentID: action.AgentID, PiSessionID: action.OldPiSessionID, SessionEpoch: int64(action.OldSessionEpoch), ProcessPID: action.OldProcessPID, ProcessStartMarker: action.OldProcessStartMarker, ProcessIncarnation: action.OldProcessIncarnation, RuntimeGeneration: action.OldRuntimeGeneration, ManifestRevision: action.OldManifestRevision, ManifestHash: action.OldManifestHash}
		args := []any{old.PiSessionID, old.SessionEpoch, old.ProcessPID, old.ProcessStartMarker, old.ProcessIncarnation, old.RuntimeGeneration, old.ManifestRevision, old.ManifestHash, recipe.Provider, recipe.ModelID, now, action.AgentID, action.OperatorAgentUpdatedAt}
		args = append(args, managedFenceValues(current)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,manifest_revision=?,manifest_hash=?,selected_model_provider=?,selected_model_id=?,lifecycle_state='running',activity_state='stopped',accepting_prompts=0,slot_held=1,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,updated_at=? WHERE agent_id=? AND updated_at=? AND desired_state='running' AND lifecycle_state='failed' AND `+managedFenceWhere(""), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return &ManagedLifecycleConflictError{AgentID: action.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET recipe_sha256=?,target_runtime_generation=?,target_process_pid=NULL,target_process_start_marker=NULL,target_process_incarnation=NULL,stage='stopped',failure_code=NULL,failure_message=NULL,expires_at=NULL,operator_stage='executing',operator_agent_updated_at=?,version=version+1,updated_at=? WHERE action_id=? AND version=? AND operator_owner=? AND operator_owner_epoch=? AND operator_stage='identity_resolved' AND stage='failed'`, recipe.SHA256, targetGeneration, now, now, actionID, version, owner, ownerEpoch)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		if err := s.rollManagedReadinessSourceFenceTx(ctx, old); err != nil {
			return err
		}
		if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		prepared, err = managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		agent, err = scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, action.AgentID), nil)
		return err
	})
	return prepared, agent, err
}

func (s *Store) PromoteManagedRootRecoveryOperatorTermination(ctx context.Context, actionID, owner string, ownerEpoch, version int) (string, error) {
	if actionID == "" || owner == "" || ownerEpoch < 1 || version < 1 {
		return "", errors.New("managed root recovery operator termination is invalid")
	}
	var operationID string
	err := s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.OperatorKind != "terminate" || action.OperatorStage != "identity_resolved" || action.OperatorOwner != owner || action.OperatorOwnerEpoch != ownerEpoch || action.Version != version || action.Stage != "failed" {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, action.AgentID), nil)
		if err != nil {
			return err
		}
		if agent.UpdatedAt != action.OperatorAgentUpdatedAt || agent.DesiredState != "running" || agent.LifecycleState != "failed" || !rootRecoveryAgentMatchesAction(agent, action) {
			return &ManagedLifecycleConflictError{AgentID: action.AgentID}
		}
		var active int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE agent_id=? AND state NOT IN ('succeeded','failed','cancelled','superseded')`, action.AgentID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return &ManagedLifecycleConflictError{AgentID: action.AgentID}
		}
		archiveKey, err := s.ids.Next()
		if err != nil {
			return err
		}
		if !managedCanonicalUUIDRE.MatchString(archiveKey) {
			return errors.New("managed archive key is invalid")
		}
		now := formatTimestamp(s.clock.Now())
		operationID = action.OperatorOperationID
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions(action_id,operation_id,agent_id,kind,source,execution_owner,state,stage,archive_key,created_at,updated_at) VALUES(?,?,?,'terminate','direct','lifecycle','cleanup','direct_mail_retire',?,?,?)`, operationID, operationID, action.AgentID, archiveKey, now, now)
		if err != nil {
			return err
		}
		if err := captureManagedTerminationAdmissionTx(ctx, s.db, operationID, action.AgentID, agent.Name, now); err != nil {
			return err
		}
		if err := s.cancelManagedControlRequestsTx(ctx, action.AgentID, "CANCELLED_BY_TERMINATE", now); err != nil {
			return err
		}
		current := restartFenceFromAgent(agent)
		args := []any{now, action.AgentID, action.OperatorAgentUpdatedAt}
		args = append(args, managedFenceValues(current)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET desired_state='terminated',lifecycle_state='terminating',accepting_prompts=0,updated_at=? WHERE agent_id=? AND updated_at=? AND desired_state='running' AND lifecycle_state='failed' AND `+managedFenceWhere(""), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return &ManagedLifecycleConflictError{AgentID: action.AgentID}
		}
		if err := s.cancelPendingEmbeddedOwnRemindersTx(ctx, action.AgentID); err != nil {
			return err
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET operator_stage='executing',operator_agent_updated_at=?,version=version+1,updated_at=? WHERE action_id=? AND version=? AND operator_owner=? AND operator_owner_epoch=? AND operator_stage='identity_resolved'`, now, now, actionID, version, owner, ownerEpoch)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
	return operationID, err
}

func (s *Store) CompleteManagedRootRecoveryOperatorRetry(ctx context.Context, actionID, owner string, ownerEpoch int) error {
	return s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.OperatorStage == "terminal" {
			return nil
		}
		if action.OperatorKind != "retry" || action.OperatorStage != "executing" || action.OperatorOwner != owner || action.OperatorOwnerEpoch != ownerEpoch || (action.Stage != "ready" && action.Stage != "failed") {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		code, message := "RESTARTED", "managed agent restarted"
		if action.Stage == "failed" {
			code, message = action.FailureCode, action.FailureMessage
			if code == "" {
				code, message = "RECOVERY_READINESS_FAILED", "managed root recovery failed"
			}
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET operator_stage='terminal',version=version+1,updated_at=?,expires_at=? WHERE action_id=? AND operator_owner=? AND operator_owner_epoch=? AND operator_stage='executing' AND stage IN ('ready','failed')`, now, formatTimestamp(s.clock.Now().Add(managedRootRecoveryRetention)), actionID, owner, ownerEpoch)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return &ManagedRootRecoveryActionStaleError{ActionID: actionID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), code, RedactedManagedDiagnostic(message), action.OperatorOperationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			return fmt.Errorf("managed root recovery operator operation is stale")
		}
		return nil
	})
}
