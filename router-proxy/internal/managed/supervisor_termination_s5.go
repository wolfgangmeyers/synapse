package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/gitworktree"
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
	CleanupWorktree   bool
}

type TerminateResult struct {
	Agent     *store.ManagedAgentRecord
	Operation *store.ManagedOperation
	Replayed  bool
}

func (m *Manager) Terminate(ctx context.Context, request TerminateRequest) (TerminateResult, error) {
	return m.terminateManaged(ctx, request)
}

func (m *Manager) TerminateManagedAgent(ctx context.Context, request TerminateRequest) (TerminateResult, error) {
	return m.Terminate(ctx, request)
}

func (m *Manager) terminateManaged(ctx context.Context, request TerminateRequest) (TerminateResult, error) {
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
	// Registered before lock defers so dispatch starts only after both locks release.
	reoffer := false
	defer func() {
		if reoffer {
			m.offerEmbeddedMail(request.AgentID)
		}
	}()
	lane.admission.Lock()
	admissionHeld := true
	defer func() {
		if admissionHeld {
			lane.admission.Unlock()
		}
	}()
	// A concurrent identical DELETE can finish after the fast replay lookup and
	// before this request enters the agent's admission lane. Replay again under
	// the lane before reading mutable agent state.
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
	failedRoot, rootErr := m.store.LatestRetryableManagedRootRecovery(ctx, request.AgentID)
	if rootErr == nil {
		return m.reserveFailedRootRecoveryTermination(ctx, request, agent, failedRoot)
	}
	if !errors.Is(rootErr, sql.ErrNoRows) {
		return TerminateResult{}, rootErr
	}
	if agent.Runtime == "embedded" {
		// Mail and turn settlement take admission while owning serial. Fence
		// new admissions, but never hold admission while waiting for serial.
		// Count independently of reset and other DELETEs so neither can
		// reopen the gate while another termination still owns it.
		lane.terminationPending++
		reoffer = true
		defer func() {
			if admissionHeld {
				lane.terminationPending--
				return
			}
			lane.admission.Lock()
			lane.terminationPending--
			lane.admission.Unlock()
		}()
	}
	claude := agent.Runtime == "embedded" && agent.SelectedModel.Provider == claudeCodeProvider
	if !claude {
		lane.admission.Unlock()
		admissionHeld = false
		if m.embeddedTerminationGateTestHook != nil {
			m.embeddedTerminationGateTestHook()
		}
		lane.serial.Lock()
		defer lane.serial.Unlock()
		// State may have changed while waiting for a turn or another DELETE.
		// Reacquire in the same serial -> admission order as turn admission.
		lane.admission.Lock()
		admissionHeld = true
		defer func() {
			if admissionHeld {
				lane.admission.Unlock()
				admissionHeld = false
			}
		}()
		if existing, lookupErr := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); lookupErr == nil {
			if existing.CanonicalPath != request.CanonicalPath || existing.CanonicalBodyHash != request.CanonicalBodyHash {
				return TerminateResult{}, &store.ManagedIdempotencyConflictError{}
			}
			return TerminateResult{Operation: existing, Replayed: true}, nil
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return TerminateResult{}, lookupErr
		}
		agent, err = m.store.ManagedAgent(ctx, request.AgentID)
		if errors.Is(err, sql.ErrNoRows) {
			return TerminateResult{}, &store.ManagedAgentNotFoundError{AgentID: request.AgentID}
		}
		if err != nil {
			return TerminateResult{}, err
		}
		failedRoot, rootErr = m.store.LatestRetryableManagedRootRecovery(ctx, request.AgentID)
		if rootErr == nil {
			return m.reserveFailedRootRecoveryTermination(ctx, request, agent, failedRoot)
		}
		if !errors.Is(rootErr, sql.ErrNoRows) {
			return TerminateResult{}, rootErr
		}
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
	reservation := store.ManagedLifecycleReservation{AgentID: request.AgentID, Kind: "terminate", Source: "direct", Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, Fence: source, RetryOf: retryOf, Operation: operation}
	admission, err := m.store.ReserveManagedLifecycle(ctx, reservation)
	admissionErr := err
	if err != nil && !admission.Committed {
		return TerminateResult{}, err
	}
	if admission.Replayed {
		return TerminateResult{Operation: admission.Operation, Replayed: true}, admissionErr
	}
	if claude {
		// Publish the committed terminating state before interrupting or draining.
		m.publishProjection()
	}
	if sourcePtr != nil {
		_ = m.store.InterruptManagedParentWork(context.Background(), source)
	}
	var claudeStopErr error
	if claude {
		// Capture identities while admission prevents a new turn from installing.
		m.activeSessionsMu.Lock()
		active := m.activeEmbeddedSessions[request.AgentID]
		m.activeSessionsMu.Unlock()
		m.claudeCodeProcesses.mu.Lock()
		entry := m.claudeCodeProcesses.entries[request.AgentID]
		m.claudeCodeProcesses.mu.Unlock()
		lane.admission.Unlock()
		admissionHeld = false
		if active != nil {
			active.Interrupt()
		}
		claudeStopErr = m.claudeCodeProcesses.stopEntry(request.AgentID, entry)
		lane.serial.Lock()
		defer lane.serial.Unlock()
	}
	m.cascadeAbortSubagents(request.AgentID)
	_ = m.store.CascadeTerminateSubagents(ctx, request.AgentID)
	// Publish the separate subagent cascade after it has been applied.
	m.publishProjection()

	var binding *managedBinding
	if sourcePtr != nil {
		if candidate, live := m.bindingFor(request.AgentID); live {
			candidate.serial.Lock()
			candidateLive := m.liveBinding(agent, candidate)
			candidateSource := bindingFence(request.AgentID, candidate)
			if candidateLive && sameBindingFence(candidateSource, source) && !candidate.reaped {
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
	cleanupWT := request.CleanupWorktree
	started := m.startManagedTask(func() {
		lane.serial.Lock()
		defer lane.serial.Unlock()
		if claudeStopErr != nil {
			// Cleanup failures are recorded at the reap stage, not the initial abort stage.
			if err := m.store.AdvanceManagedTermination(context.Background(), admission.Action.ActionID, "terminating", "abort", "terminating", "terminate_reap", sourcePtr); err != nil {
				log.Printf("managed termination: action %s: advance: %v", admission.Action.ActionID, err)
				m.failTermination(admission.Action.ActionID, "TERMINATION_CLEANUP_FAILED", true, sourcePtr)
				return
			}
			m.failTermination(admission.Action.ActionID, "TERMINATION_CLEANUP_FAILED", true, sourcePtr)
			m.publishProjection()
			return
		}
		m.executeTerminationInLane(admission.Action.ActionID, binding, cleanupWT)
	})
	if !started {
		admissionErr = errors.Join(admissionErr, errors.New("committed cleanup handoff unavailable: manager closed"))
	}
	return TerminateResult{Agent: &responseAgent, Operation: admission.Operation}, admissionErr
}

func (m *Manager) executeTerminationInLane(actionID string, candidate *managedBinding, cleanupWorktree ...bool) {
	// Every exit follows a durable lifecycle transition. Publishing afterward
	// lets UI-live replace the terminating row only after terminal cleanup has
	// removed it, and exposes a retained failure when cleanup cannot complete.
	defer m.publishProjection()
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil {
		log.Printf("managed termination: action %s: read action: %v", actionID, err)
		return
	}
	if action.Kind != "terminate" || lifecycleTerminal(action) {
		return
	}
	m.cascadeAbortSubagents(action.AgentID)
	ClearAgentContextUsage(action.AgentID)
	_ = m.store.CascadeTerminateSubagents(context.Background(), action.AgentID)
	agent, err := m.store.ManagedAgent(context.Background(), action.AgentID)
	if err != nil {
		log.Printf("managed termination: action %s: read agent: %v", actionID, err)
		m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, action.SourceFence)
		return
	}
	if agent.DesiredState != "terminated" {
		return
	}
	source := action.SourceFence
	if action.State == "terminating" && action.Stage == "abort" {
		if source == nil || m.processOps.Probe(oldFenceIdentity(*source)).State == ProcessOwned {
			m.abortTerminationTurn(actionID, source, candidate)
		} else {
			_ = m.store.RecordManagedTerminationDiagnostic(context.Background(), actionID, "ABORT_UNAVAILABLE")
		}
		if err := m.store.AdvanceManagedTermination(context.Background(), actionID, "terminating", "abort", "terminating", "terminate_reap", source); err != nil {
			log.Printf("managed termination: action %s: advance: %v", actionID, err)
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
		action, err = m.store.ManagedLifecycleAction(context.Background(), actionID)
		if err != nil {
			log.Printf("managed termination: action %s: read action: %v", actionID, err)
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
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
				// The exact fenced worker is not owned by this router. Never signal
				// the observed process; deletion still owns the durable agent row.
			}
		}
		if candidate != nil {
			if source != nil {
				m.interruptBuiltinToolInvocations(candidate, *source)
			}
			candidate.serial.Lock()
			candidate.reaped = true
			rpc := candidate.rpc
			candidate.serial.Unlock()
			if rpc != nil {
				_ = closeRPCAndWait(rpc)
			}
			// Once exact absence is proven, no retry may use this stale binding.
			// Remaining work is durable direct-mail/artifact cleanup only.
			m.clearBinding(candidate)
			candidate = nil
		}
		if err := m.store.AdvanceManagedTermination(context.Background(), actionID, "terminating", "terminate_reap", "cleanup", "direct_mail_retire", source); err != nil {
			log.Printf("managed termination: action %s: advance: %v", actionID, err)
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
		action, err = m.store.ManagedLifecycleAction(context.Background(), actionID)
		if err != nil {
			log.Printf("managed termination: action %s: read action: %v", actionID, err)
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
	}
	if action.State == "cleanup" && action.Stage == "direct_mail_retire" {
		if err := m.store.RetireManagedRecipientSourceForTermination(context.Background(), action.AgentID, agent.Name); err != nil {
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
		if err := m.store.AdvanceManagedTermination(context.Background(), actionID, "cleanup", "direct_mail_retire", "cleanup", "agent_cleanup", source); err != nil {
			log.Printf("managed termination: action %s: advance: %v", actionID, err)
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
		action, err = m.store.ManagedLifecycleAction(context.Background(), actionID)
		if err != nil {
			log.Printf("managed termination: action %s: read action: %v", actionID, err)
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
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
		doCleanupWT := true
		if len(cleanupWorktree) > 0 {
			doCleanupWT = cleanupWorktree[0]
		}
		if doCleanupWT && agent.Worktree && agent.WorktreeBasePath != "" && agent.Cwd != "" {
			_ = gitworktree.RemoveWorktree(context.Background(), agent.WorktreeBasePath, agent.Cwd, true)
		}
		if err := m.store.AdvanceManagedTermination(context.Background(), actionID, "cleanup", "agent_cleanup", "cleanup", "durable_write", source); err != nil {
			log.Printf("managed termination: action %s: advance: %v", actionID, err)
			m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
			return
		}
	}
	action, err = m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil {
		log.Printf("managed termination: action %s: read action: %v", actionID, err)
		m.failTermination(actionID, "TERMINATION_CLEANUP_FAILED", true, source)
		return
	}
	if action.State == "cleanup" && action.Stage == "durable_write" {
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
	if err := m.store.FailManagedTermination(context.Background(), actionID, code, retryable, source); err != nil {
		log.Printf("managed termination: action %s: record failure %s: %v", actionID, code, err)
		return
	}
	log.Printf("managed termination: action %s: %s (retryable=%t)", actionID, code, retryable)
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

func (m *Manager) cascadeAbortSubagents(parentAgentID string) {
	if parentAgentID == "" {
		return
	}
	m.subagentsMu.Lock()
	var toAbort []*subagentTask
	seen := make(map[*subagentTask]bool)
	matches := func(t *subagentTask) bool {
		if t == nil {
			return false
		}
		return t.parent.AgentID == parentAgentID || strings.HasPrefix(t.childID, "subagent-"+parentAgentID+"-")
	}
	for _, task := range m.subagentsActive {
		if matches(task) && !seen[task] {
			seen[task] = true
			toAbort = append(toAbort, task)
		}
	}
	for _, task := range m.subagentsRegistry {
		if matches(task) && !seen[task] {
			seen[task] = true
			toAbort = append(toAbort, task)
		}
	}
	filtered := m.subagentsQueue[:0]
	for _, task := range m.subagentsQueue {
		if matches(task) {
			task.mu.Lock()
			task.stopped = true
			if task.cancel != nil {
				task.cancel()
			}
			task.mu.Unlock()
		} else {
			filtered = append(filtered, task)
		}
	}
	m.subagentsQueue = filtered
	m.subagentsMu.Unlock()

	for _, task := range toAbort {
		task.mu.Lock()
		task.stopped = true
		if task.cancel != nil {
			task.cancel()
		}
		if task.activeSession != nil {
			task.activeSession.Abort()
		}
		task.mu.Unlock()
	}
}
