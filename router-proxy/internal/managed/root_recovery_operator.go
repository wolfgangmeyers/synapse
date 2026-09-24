package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func failedRootRecoveryRetainedIdentity(action *store.ManagedRootRecoveryAction) ProcessIdentity {
	if action == nil {
		return ProcessIdentity{}
	}
	if action.TargetProcessPID > 0 {
		return ProcessIdentity{PID: action.TargetProcessPID, StartMarker: action.TargetProcessStartMarker, Incarnation: action.TargetProcessIncarnation}
	}
	return ProcessIdentity{PID: action.OldProcessPID, StartMarker: action.OldProcessStartMarker, Incarnation: action.OldProcessIncarnation}
}

func (m *Manager) reserveFailedRootRecoveryRetry(ctx context.Context, request RestartRequest, agent *store.ManagedAgentRecord, failed *store.ManagedRootRecoveryAction) (RestartResult, error) {
	if agent == nil || failed == nil {
		return RestartResult{}, &store.ManagedLifecycleConflictError{AgentID: request.AgentID}
	}
	responseAgent := *agent
	responseAgent.LifecycleState, responseAgent.AcceptingPrompts = "restarting", false
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": PublicAgent(&responseAgent)})
	if err != nil {
		return RestartResult{}, err
	}
	operation := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, ResponseBody: body, CreatedAt: formatManagedTime(m.clock.Now())}
	lane := m.laneFor(request.AgentID)
	lane.admission.Lock()
	defer lane.admission.Unlock()
	admission, err := m.store.ReserveManagedRootRecoveryOperator(ctx, store.ManagedRootRecoveryOperatorReservation{ActionID: failed.ActionID, ExpectedVersion: failed.Version, Kind: "retry", Owner: m.rootRecoveryOperatorOwner, Operation: operation})
	if err != nil {
		return RestartResult{}, err
	}
	if admission.Replayed {
		return RestartResult{Operation: admission.Operation, Replayed: true}, nil
	}
	resolved, err := m.resolveFailedRootRecoveryIdentity(ctx, admission.Action, false)
	if err != nil {
		return RestartResult{}, err
	}
	if !resolved {
		return RestartResult{}, &store.ManagedLifecycleConflictError{AgentID: request.AgentID}
	}
	m.startManagedTask(func() {
		lane.serial.Lock()
		defer lane.serial.Unlock()
		m.executeFailedRootRecoveryRetryInLane(admission.Action.ActionID)
	})
	return RestartResult{Agent: &responseAgent, Operation: admission.Operation}, nil
}

func (m *Manager) reserveFailedRootRecoveryTermination(ctx context.Context, request TerminateRequest, agent *store.ManagedAgentRecord, failed *store.ManagedRootRecoveryAction) (TerminateResult, error) {
	if agent == nil || failed == nil {
		return TerminateResult{}, &store.ManagedLifecycleConflictError{AgentID: request.AgentID}
	}
	responseAgent := *agent
	responseAgent.DesiredState, responseAgent.LifecycleState, responseAgent.AcceptingPrompts = "terminated", "terminating", false
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": PublicAgent(&responseAgent)})
	if err != nil {
		return TerminateResult{}, err
	}
	operation := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, ResponseBody: body, CreatedAt: formatManagedTime(m.clock.Now())}
	admission, err := m.store.ReserveManagedRootRecoveryOperator(ctx, store.ManagedRootRecoveryOperatorReservation{ActionID: failed.ActionID, ExpectedVersion: failed.Version, Kind: "terminate", Owner: m.rootRecoveryOperatorOwner, Operation: operation})
	if err != nil {
		return TerminateResult{}, err
	}
	if admission.Replayed {
		return TerminateResult{Operation: admission.Operation, Replayed: true}, nil
	}
	if _, err := m.resolveFailedRootRecoveryIdentity(ctx, admission.Action, true); err != nil {
		return TerminateResult{}, err
	}
	lane := m.laneFor(request.AgentID)
	m.startManagedTask(func() {
		lane.serial.Lock()
		defer lane.serial.Unlock()
		m.executeFailedRootRecoveryTerminationInLane(admission.Action.ActionID)
	})
	return TerminateResult{Agent: &responseAgent, Operation: admission.Operation}, nil
}

// resolveFailedRootRecoveryIdentity persists proof before any replacement or
// cleanup action. Retry requires absence or exact ownership. Termination may
// abandon an unbound mismatch without signalling it.
func (m *Manager) resolveFailedRootRecoveryIdentity(ctx context.Context, action *store.ManagedRootRecoveryAction, terminate bool) (bool, error) {
	if action == nil {
		return false, errors.New("managed root recovery operator action is unavailable")
	}
	identity := failedRootRecoveryRetainedIdentity(action)
	state := m.processOps.Probe(identity).State
	safe := state == ProcessAbsent
	if state == ProcessOwned {
		safe = m.reapRestartGroup(ctx, identity, nil)
	}
	if !safe && !terminate {
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "conflict", "message": "managed process identity could not be verified"}})
		err := m.store.FailManagedRootRecoveryOperator(context.Background(), action.ActionID, action.OperatorOwner, action.OperatorOwnerEpoch, action.Version, 409, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": action.OperatorOperationID}, body, "ORPHAN_IDENTITY_UNSAFE", "managed process identity could not be verified")
		return false, err
	}
	advanced, err := m.store.AdvanceManagedRootRecoveryOperator(context.Background(), action.ActionID, action.OperatorOwner, action.OperatorOwnerEpoch, action.Version, "reserved", "identity_resolved")
	if err != nil {
		return false, err
	}
	*action = *advanced
	return safe || terminate, nil
}

func (m *Manager) revalidateFailedRootRecoveryRetry(action *store.ManagedRootRecoveryAction) bool {
	identity := failedRootRecoveryRetainedIdentity(action)
	switch m.processOps.Probe(identity).State {
	case ProcessAbsent:
		return true
	case ProcessOwned:
		return m.reapRestartGroup(context.Background(), identity, nil)
	default:
		return false
	}
}

func (m *Manager) executeFailedRootRecoveryRetryInLane(actionID string) {
	action, err := m.store.ManagedRootRecoveryActionByID(context.Background(), actionID)
	if err != nil || action.OperatorKind != "retry" || action.OperatorOwner != m.rootRecoveryOperatorOwner {
		return
	}
	if action.OperatorStage == "identity_resolved" {
		if !m.revalidateFailedRootRecoveryRetry(action) {
			body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "conflict", "message": "managed process identity changed before retry"}})
			_ = m.store.FailManagedRootRecoveryOperator(context.Background(), action.ActionID, action.OperatorOwner, action.OperatorOwnerEpoch, action.Version, 409, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": action.OperatorOperationID}, body, "ORPHAN_IDENTITY_UNSAFE", "managed process identity changed before retry")
			return
		}
		agent, readErr := m.store.ManagedAgent(context.Background(), action.AgentID)
		if readErr != nil {
			return
		}
		recipe, recipeErr := m.managedLaunchRecipe(*agent)
		if recipeErr != nil {
			return
		}
		targetGeneration, idErr := m.ids.Next()
		if idErr != nil {
			return
		}
		action, agent, err = m.store.PrepareManagedRootRecoveryOperatorRetry(context.Background(), action.ActionID, action.OperatorOwner, action.OperatorOwnerEpoch, action.Version, recipe, targetGeneration)
		if err != nil {
			return
		}
	}
	if action.OperatorStage != "executing" {
		return
	}
	if action.Stage == "ready" || action.Stage == "failed" {
		_ = m.store.CompleteManagedRootRecoveryOperatorRetry(context.Background(), action.ActionID, action.OperatorOwner, action.OperatorOwnerEpoch)
		return
	}
	agent, err := m.store.ManagedAgent(context.Background(), action.AgentID)
	if err != nil || agent.LaunchRecipe == nil {
		return
	}
	old := rootRecoveryActionFence(action)
	if err := m.recoverManagedRootInLane(context.Background(), agent, *agent.LaunchRecipe, old, action, false); err != nil {
		return
	}
	_ = m.store.CompleteManagedRootRecoveryOperatorRetry(context.Background(), action.ActionID, action.OperatorOwner, action.OperatorOwnerEpoch)
}

func (m *Manager) executeFailedRootRecoveryTerminationInLane(actionID string) {
	action, err := m.store.ManagedRootRecoveryActionByID(context.Background(), actionID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil || action.OperatorKind != "terminate" || action.OperatorOwner != m.rootRecoveryOperatorOwner {
		return
	}
	if action.OperatorStage == "identity_resolved" {
		identity := failedRootRecoveryRetainedIdentity(action)
		if m.processOps.Probe(identity).State == ProcessOwned && !m.reapRestartGroup(context.Background(), identity, nil) {
			return
		}
		operationID, promoteErr := m.store.PromoteManagedRootRecoveryOperatorTermination(context.Background(), action.ActionID, action.OperatorOwner, action.OperatorOwnerEpoch, action.Version)
		if promoteErr != nil {
			return
		}
		m.executeTerminationInLane(operationID, nil)
		return
	}
	if action.OperatorStage == "executing" {
		if _, lifecycleErr := m.store.ManagedLifecycleAction(context.Background(), action.OperatorOperationID); lifecycleErr == nil {
			m.executeTerminationInLane(action.OperatorOperationID, nil)
		}
	}
}

// RecoverManagedRootRecoveryOperators takes over reservations left by a prior
// router and resumes them through the same per-agent lane as live requests.
func (m *Manager) RecoverManagedRootRecoveryOperators(ctx context.Context) error {
	actions, err := m.store.ManagedRootRecoveryOperators(ctx)
	if err != nil {
		return err
	}
	for _, listed := range actions {
		action := listed
		if action.OperatorOwner != m.rootRecoveryOperatorOwner {
			action, err = m.store.TakeoverManagedRootRecoveryOperator(ctx, action.ActionID, action.Version, action.OperatorOwnerEpoch, m.rootRecoveryOperatorOwner)
			if err != nil {
				var stale *store.ManagedRootRecoveryActionStaleError
				if errors.As(err, &stale) {
					continue
				}
				return err
			}
		}
		lane := m.laneFor(action.AgentID)
		lane.serial.Lock()
		if action.OperatorStage == "reserved" {
			_, err = m.resolveFailedRootRecoveryIdentity(ctx, action, action.OperatorKind == "terminate")
		}
		if err == nil {
			switch action.OperatorKind {
			case "retry":
				m.executeFailedRootRecoveryRetryInLane(action.ActionID)
			case "terminate":
				m.executeFailedRootRecoveryTerminationInLane(action.ActionID)
			}
		}
		lane.serial.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
