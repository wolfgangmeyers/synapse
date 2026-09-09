package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// Terminate admits a fenced, idempotent agent tombstone. Admission cancels
// root-control work in the same durable transaction; execution then performs
// best-effort Abort followed by exact process-group cleanup.
type TerminateRequest struct {
	AgentID           string
	Method            string
	CanonicalPath     string
	RequestID         string
	CanonicalBodyHash string
}

type TerminateResult struct {
	Agent     *store.ManagedAgentRecord
	Operation *store.ManagedOperation
	Replayed  bool
}

func (m *Manager) Terminate(ctx context.Context, request TerminateRequest) (TerminateResult, error) {
	if request.AgentID == "" || request.Method == "" || request.CanonicalPath == "" || request.RequestID == "" || request.CanonicalBodyHash == "" {
		return TerminateResult{}, errors.New("managed termination request is incomplete")
	}
	if existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); err == nil {
		if existing.CanonicalPath != request.CanonicalPath || existing.CanonicalBodyHash != request.CanonicalBodyHash {
			return TerminateResult{}, &store.ManagedIdempotencyConflictError{}
		}
		return TerminateResult{Operation: existing, Replayed: true}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TerminateResult{}, err
	}
	lane := m.laneFor(request.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	if existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); err == nil {
		if existing.CanonicalPath != request.CanonicalPath || existing.CanonicalBodyHash != request.CanonicalBodyHash {
			return TerminateResult{}, &store.ManagedIdempotencyConflictError{}
		}
		return TerminateResult{Operation: existing, Replayed: true}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TerminateResult{}, err
	}

	agent, err := m.store.ManagedAgent(ctx, request.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return TerminateResult{}, &store.ManagedAgentNotFoundError{AgentID: request.AgentID}
	}
	if err != nil {
		return TerminateResult{}, err
	}
	var source store.ManagedRuntimeFence
	var sourcePtr *store.ManagedRuntimeFence
	var retryOf string
	predecessor, retryErr := m.store.LatestRetryableManagedTermination(ctx, request.AgentID)
	if retryErr == nil {
		retryOf = predecessor.ActionID
		if predecessor.SourceFence != nil {
			source, sourcePtr = *predecessor.SourceFence, predecessor.SourceFence
		}
	} else if !errors.Is(retryErr, sql.ErrNoRows) {
		return TerminateResult{}, retryErr
	} else if current := storedAgentFence(agent); validRestartFence(current) {
		source, sourcePtr = current, &source
	}

	responseAgent := *agent
	responseAgent.DesiredState = "terminated"
	responseAgent.LifecycleState = "terminating"
	responseAgent.AcceptingPrompts = false
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": PublicAgent(&responseAgent)})
	if err != nil {
		return TerminateResult{}, err
	}
	operation := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, ResponseBody: body, CreatedAt: formatManagedTime(m.clock.Now())}
	admission, err := m.store.ReserveManagedLifecycle(ctx, store.ManagedLifecycleReservation{AgentID: request.AgentID, Kind: "terminate", Source: "direct", Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, Fence: source, RetryOf: retryOf, Operation: operation})
	if err != nil {
		return TerminateResult{}, err
	}
	if admission.Replayed {
		return TerminateResult{Operation: admission.Operation, Replayed: true}, nil
	}

	var binding *managedBinding
	if sourcePtr != nil {
		if candidate, live := m.bindingFor(request.AgentID); live && m.liveBinding(agent, candidate) && sameBindingFence(bindingFence(request.AgentID, candidate), source) {
			candidate.serial.Lock()
			if !candidate.reaped {
				candidate.operationID, candidate.lifecycleAction = admission.Action.OperationID, "terminate"
				binding = candidate
			}
			candidate.serial.Unlock()
			if binding != nil {
				m.mu.Lock()
				if m.bindings[request.AgentID] == binding {
					m.operationIDs[request.AgentID] = admission.Action.OperationID
				}
				m.mu.Unlock()
			}
		}
	}
	m.startManagedTask(func() {
		lane.serial.Lock()
		defer lane.serial.Unlock()
		m.executeTerminationInLane(admission.Action.ActionID, binding)
	})
	return TerminateResult{Agent: &responseAgent, Operation: admission.Operation}, nil
}

func (m *Manager) executeTerminationInLane(actionID string, candidate *managedBinding) {
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil || action.Kind != "terminate" || lifecycleTerminal(action) {
		return
	}
	agent, err := m.store.ManagedAgent(context.Background(), action.AgentID)
	if err != nil || agent.DesiredState != "terminated" {
		return
	}
	source := action.SourceFence
	if action.State == "terminating" && action.Stage == "abort" {
		m.abortTerminationTurn(actionID, source, candidate)
		if err := m.store.AdvanceManagedTermination(context.Background(), actionID, "terminating", "abort", "terminating", "terminate_reap", source); err != nil {
			return
		}
		action, err = m.store.ManagedLifecycleAction(context.Background(), actionID)
		if err != nil {
			return
		}
	}
	if action.State == "terminating" && action.Stage == "terminate_reap" {
		if source != nil {
			identity := oldFenceIdentity(*source)
			switch m.processOps.Probe(identity).State {
			case ProcessAbsent:
			case ProcessOwned:
				var wait <-chan error
				if candidate != nil {
					candidate.serial.Lock()
					if candidate.lifecycleAction == "terminate" && !candidate.reaped && sameBindingFence(bindingFence(action.AgentID, candidate), *source) {
						wait = candidate.waitDone
					}
					candidate.serial.Unlock()
				}
				if !m.reapRestartGroup(context.Background(), identity, wait) {
					m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
					return
				}
			default:
				m.failTermination(actionID, "TERMINATION_PROCESS_IDENTITY_UNSAFE", false, source)
				return
			}
		}
		if candidate != nil {
			candidate.serial.Lock()
			candidate.reaped = true
			rpc := candidate.rpc
			candidate.serial.Unlock()
			if rpc != nil {
				_ = rpc.Close()
			}
			// Once exact absence is proven, no retry may use this stale binding.
			// Remaining work is durable direct-mail/artifact cleanup only.
			m.clearBinding(candidate)
			candidate = nil
		}
		if err := m.store.AdvanceManagedTermination(context.Background(), actionID, "terminating", "terminate_reap", "cleanup", "direct_mail_retire", source); err != nil {
			return
		}
		action, err = m.store.ManagedLifecycleAction(context.Background(), actionID)
		if err != nil {
			return
		}
	}
	if action.State == "cleanup" && action.Stage == "direct_mail_retire" {
		if err := m.store.RetireManagedRecipientSourceForTermination(context.Background(), action.AgentID, agent.Name); err != nil {
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
		if err := m.store.AdvanceManagedTermination(context.Background(), actionID, "cleanup", "direct_mail_retire", "cleanup", "agent_cleanup", source); err != nil {
			return
		}
		action, err = m.store.ManagedLifecycleAction(context.Background(), actionID)
		if err != nil {
			return
		}
	}
	if action.State == "cleanup" && action.Stage == "agent_cleanup" {
		if m.beforeTerminationArtifactCleanup != nil && m.beforeTerminationArtifactCleanup(context.Background(), actionID) != nil {
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
		artifacts, err := m.store.ManagedSessionArtifacts(context.Background(), action.AgentID)
		if err != nil {
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
		for _, artifact := range artifacts {
			if artifact.CleanupState != "unlinked" && m.artifacts.unlink(context.Background(), artifact.ArtifactID) != nil {
				m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
				return
			}
		}
		if err := m.store.AdvanceManagedTermination(context.Background(), actionID, "cleanup", "agent_cleanup", "cleanup", "durable_write", source); err != nil {
			return
		}
	}
	if action, err = m.store.ManagedLifecycleAction(context.Background(), actionID); err == nil && action.State == "cleanup" && action.Stage == "durable_write" {
		if err := m.store.CompleteManagedTermination(context.Background(), actionID); err != nil {
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
		if candidate != nil {
			m.clearBinding(candidate)
		}
	}
}

// shutdownTerminationBinding keeps Manager.Close on the durable terminate
// owner. Generic Close must not issue an unfenced Process.Stop while the
// termination reaper is proving the exact process group absent.
func (m *Manager) shutdownTerminationBinding(_ context.Context, binding *managedBinding) (bool, error) {
	if binding == nil || binding.lane == nil {
		return false, nil
	}
	binding.lane.serial.Lock()
	defer binding.lane.serial.Unlock()
	binding.serial.Lock()
	actionID, lifecycleAction := binding.operationID, binding.lifecycleAction
	binding.serial.Unlock()
	if lifecycleAction != "terminate" {
		return false, nil
	}
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if action.Kind != "terminate" || lifecycleTerminal(action) {
		return action.Kind == "terminate", nil
	}
	m.executeTerminationInLane(actionID, binding)
	action, err = m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil {
		return true, err
	}
	if lifecycleTerminal(action) {
		return true, nil
	}
	return true, errors.New("managed termination did not reach a terminal state during shutdown")
}

func (m *Manager) abortTerminationTurn(actionID string, source *store.ManagedRuntimeFence, candidate *managedBinding) {
	if source == nil {
		// A launch cancelled before it records a process fence owns no group and
		// therefore has no live RPC to diagnose or contact.
		return
	}
	if candidate == nil {
		_ = m.store.RecordManagedTerminationDiagnostic(context.Background(), actionID, "ABORT_UNAVAILABLE")
		return
	}
	candidate.serial.Lock()
	valid := candidate.lifecycleAction == "terminate" && !candidate.reaped && sameBindingFence(bindingFence(candidate.agentID, candidate), *source)
	rpc := candidate.rpc
	candidate.serial.Unlock()
	if !valid || rpc == nil {
		_ = m.store.RecordManagedTerminationDiagnostic(context.Background(), actionID, "ABORT_UNAVAILABLE")
		return
	}
	// Discover while the exact binding is still live so final cleanup owns the
	// current session file even when Abort returns EOF or never responds.
	stateCtx, stateCancel := context.WithTimeout(context.Background(), m.config.Limits.GracefulStop)
	state, stateErr := rpc.GetState(stateCtx)
	stateCancel()
	if stateErr != nil || !stateSessionMatches(state, source.PiSessionID) {
		_ = m.store.RecordManagedTerminationDiagnostic(context.Background(), actionID, "STATE_DISCOVERY_FAILED")
	} else if _, err := m.artifacts.discover(context.Background(), source.AgentID, source.SessionEpoch, source.PiSessionID, state); err != nil {
		_ = m.store.RecordManagedTerminationDiagnostic(context.Background(), actionID, "STATE_DISCOVERY_FAILED")
	}
	abortCtx, cancel := context.WithTimeout(context.Background(), m.config.Limits.GracefulStop)
	err := rpc.Abort(abortCtx)
	cancel()
	if err != nil {
		_ = m.store.RecordManagedTerminationDiagnostic(context.Background(), actionID, terminationAbortCode(err))
	}
}

func terminationAbortCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "ABORT_TIMEOUT"
	case errors.Is(err, io.EOF):
		return "ABORT_EOF"
	case errors.Is(err, ErrAbortResponseMalformed), strings.Contains(strings.ToLower(err.Error()), "malformed"):
		return "ABORT_MALFORMED"
	default:
		return "ABORT_ERROR"
	}
}

func (m *Manager) failTermination(actionID, code string, retryable bool, source *store.ManagedRuntimeFence) {
	_ = m.store.FailManagedTermination(context.Background(), actionID, code, retryable, source)
}

func (m *Manager) recoverManagedTerminationActionInLane(action *store.ManagedLifecycleAction) {
	if action == nil {
		return
	}
	// Recovery never recreates a binding or sends Abort to an unknown process.
	// executeTerminationInLane uses only the persisted fence to prove absence or
	// reap the exact group, then resumes the durable cleanup stage.
	m.executeTerminationInLane(action.ActionID, nil)
}
