package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// Reset resolves durable replay before inspecting a live binding. New work then
// enters the persistent per-agent lane, so restart preparation, close, exit,
// and reset execution cannot interleave their process ownership decisions.
func (m *Manager) Reset(ctx context.Context, request ResetRequest) (ResetResult, error) {
	if request.AgentID == "" || request.Method == "" || request.CanonicalPath == "" || request.RequestID == "" || request.CanonicalBodyHash == "" {
		return ResetResult{}, errors.New("managed reset request is incomplete")
	}
	if existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); err == nil {
		if existing.CanonicalPath != request.CanonicalPath || existing.CanonicalBodyHash != request.CanonicalBodyHash {
			return ResetResult{}, &store.ManagedIdempotencyConflictError{}
		}
		return ResetResult{Operation: existing, Replayed: true}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ResetResult{}, err
	}
	lane := m.laneFor(request.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()

	// A second caller may have committed while this request waited for the lane.
	if existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); err == nil {
		if existing.CanonicalPath != request.CanonicalPath || existing.CanonicalBodyHash != request.CanonicalBodyHash {
			return ResetResult{}, &store.ManagedIdempotencyConflictError{}
		}
		return ResetResult{Operation: existing, Replayed: true}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ResetResult{}, err
	}

	// Preserve the public missing-agent shape even when no live binding exists.
	// The later reservation repeats this durable check under its writer lock.
	agent, err := m.store.ManagedAgent(ctx, request.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return ResetResult{}, &store.ManagedAgentNotFoundError{AgentID: request.AgentID}
	}
	if err != nil {
		return ResetResult{}, err
	}
	liveFence := storedAgentFence(agent)
	predecessor, retryErr := m.store.LatestRetryableManagedReset(ctx, request.AgentID)
	if retryErr != nil && !errors.Is(retryErr, sql.ErrNoRows) {
		return ResetResult{}, retryErr
	}

	var binding *managedBinding
	var source store.ManagedRuntimeFence
	var targetSession, targetGeneration string
	var targetEpoch int64
	var targetFence *store.ManagedRuntimeFence
	var targetArtifactID, retryOf, initialState, initialStage string
	launchMode := CreateNew
	if predecessor != nil {
		if predecessor.SourceFence == nil {
			return ResetResult{}, &store.ManagedResetConflictError{AgentID: request.AgentID}
		}
		source, retryOf = *predecessor.SourceFence, predecessor.ActionID
		switch predecessor.TerminalCode {
		case "RESET_TARGET_START_FAILED", "RESET_TARGET_IDENTITY_UNSAFE":
			if predecessor.Stage != "target_ready" {
				return ResetResult{}, &store.ManagedResetConflictError{AgentID: request.AgentID}
			}
			// A target-start retry owns no source work. It keeps the predecessor's
			// target artifact/session/epoch/generation, proves the exact retained
			// target group absent (or reaps that exact owned group), then clears the
			// full target fence before reserving ResumeExisting directly at target
			// start.
			artifact, retryErr := m.verifyRetainedResetTarget(ctx, predecessor)
			if retryErr != nil || predecessor.TargetFence == nil {
				return ResetResult{}, &store.ManagedResetConflictError{AgentID: request.AgentID}
			}
			if retryErr := m.clearRetainedResetTargetAfterExactAbsence(ctx, predecessor); retryErr != nil {
				return ResetResult{}, &store.ManagedResetConflictError{AgentID: request.AgentID}
			}
			agent, err = m.store.ManagedAgent(ctx, request.AgentID)
			if err != nil || !sameBindingFence(storedAgentFence(agent), source) {
				return ResetResult{}, &store.ManagedResetConflictError{AgentID: request.AgentID}
			}
			liveFence = storedAgentFence(agent)
			targetSession, targetEpoch = artifact.PiSessionID, artifact.SessionEpoch
			targetGeneration, targetArtifactID = predecessor.PlannedTargetRuntimeGeneration, artifact.ArtifactID
			initialState, initialStage, launchMode = "starting_target", "target_start", ResumeExisting
		case "RESET_OLD_ARTIFACT_CLEANUP_FAILED":
			if predecessor.Stage != "old_artifact_cleanup" || predecessor.TargetFence == nil || predecessor.TargetArtifactID == "" {
				return ResetResult{}, &store.ManagedResetConflictError{AgentID: request.AgentID}
			}
			candidate, live := m.bindingFor(request.AgentID)
			if !live || !m.liveBinding(agent, candidate) || !sameBindingFence(bindingFence(request.AgentID, candidate), *predecessor.TargetFence) {
				return ResetResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
			}
			binding, targetFence = candidate, predecessor.TargetFence
			targetArtifactID, targetGeneration = predecessor.TargetArtifactID, predecessor.PlannedTargetRuntimeGeneration
			targetEpoch = predecessor.TargetFence.SessionEpoch
			initialState, initialStage = "old_artifact_cleanup", "old_artifact_cleanup"
		default:
			return ResetResult{}, &store.ManagedResetConflictError{AgentID: request.AgentID}
		}
	} else {
		candidate, live := m.bindingFor(request.AgentID)
		if live && m.liveBinding(agent, candidate) {
			binding, source = candidate, bindingFence(request.AgentID, candidate)
			liveFence = source
		} else {
			// S3 clears the source binding after source-stop and recovery starts
			// without a binding. Reset still has to reach the store's precedence
			// transaction for an admitted or later lifecycle restart; the durable
			// source/target fence decides whether that handoff is safe.
			active, activeErr := m.store.ActiveManagedLifecycleAction(ctx, request.AgentID)
			if errors.Is(activeErr, sql.ErrNoRows) {
				return ResetResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
			}
			if activeErr != nil {
				return ResetResult{}, activeErr
			}
			if active.Kind != "restart" || active.SourceFence == nil {
				return ResetResult{}, &store.ManagedResetConflictError{AgentID: request.AgentID}
			}
			liveFence = storedAgentFence(agent)
			switch {
			case active.TargetFence != nil && sameBindingFence(liveFence, *active.TargetFence):
				source = *active.TargetFence
			case sameBindingFence(liveFence, *active.SourceFence):
				source = *active.SourceFence
			default:
				return ResetResult{}, &store.ManagedResetConflictError{AgentID: request.AgentID}
			}
		}
		targetEpoch = source.SessionEpoch + 1
		targetSession, err = m.ids.Next()
		if err != nil {
			return ResetResult{}, err
		}
		targetGeneration, err = m.ids.Next()
		if err != nil {
			return ResetResult{}, err
		}
		if err := m.ensureFreshManagedSession(ctx, request.AgentID, targetSession); err != nil {
			return ResetResult{}, err
		}
	}
	responseAgent := *agent
	responseAgent.LifecycleState, responseAgent.AcceptingPrompts = "resetting", false
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": PublicAgent(&responseAgent)})
	if err != nil {
		return ResetResult{}, err
	}
	operation := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, ResponseBody: body, CreatedAt: formatManagedTime(m.clock.Now())}
	admission, err := m.store.ReserveManagedReset(ctx, store.ManagedResetReservation{AgentID: request.AgentID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, SourceFence: source, LiveFence: liveFence, TargetFence: targetFence, TargetArtifactID: targetArtifactID, RetryOf: retryOf, InitialState: initialState, InitialStage: initialStage, PlannedTargetRuntimeGeneration: targetGeneration, Operation: operation})
	if err != nil {
		return ResetResult{}, err
	}
	if admission.Replayed {
		return ResetResult{Operation: admission.Operation, Replayed: true}, nil
	}
	if err := m.store.ClearManagedCrashHistory(ctx, request.AgentID); err != nil {
		return ResetResult{}, err
	}
	if binding != nil {
		binding.serial.Lock()
		if !m.liveBindingForFence(context.Background(), binding, liveFence) {
			binding.serial.Unlock()
			_ = m.store.FailManagedReset(context.Background(), admission.Action.ActionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, liveFence)
			return ResetResult{Agent: &responseAgent, Operation: admission.Operation}, nil
		}
		binding.operationID, binding.lifecycleAction = request.RequestID, "reset"
		binding.serial.Unlock()
		m.mu.Lock()
		if m.bindings[request.AgentID] == binding {
			m.operationIDs[request.AgentID] = request.RequestID
		}
		m.mu.Unlock()
	}
	m.startManagedTask(func() {
		lane.serial.Lock()
		defer lane.serial.Unlock()
		m.executeResetInLane(binding, admission.Action.ActionID, source, targetSession, targetGeneration, targetEpoch, launchMode)
	})
	return ResetResult{Agent: &responseAgent, Operation: admission.Operation}, nil
}

// ensureFreshManagedSession checks both the durable artifact ledger and the
// controlled root. It does not inspect arbitrary home directories or session
// contents; a colliding file name under the router-owned root is enough to
// refuse a supposedly fresh session ID.
func (m *Manager) ensureFreshManagedSession(ctx context.Context, agentID, sessionID string) error {
	// A candidate Pi session UUID is global. Looking only at this agent or one
	// epoch lets a retained not-materialized artifact collide without a file for
	// the controlled-root walk to find.
	if _, err := m.store.ManagedSessionArtifactForCandidate(ctx, sessionID); err == nil {
		return errors.New("managed session ID already has an artifact")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return m.artifacts.ensureUnusedSessionID(sessionID)
}

// verifyRetainedResetTarget accepts only the durable evidence required to
// resume a target: both artifacts remain materialized and retained, and the
// target artifact still names the target fence that the failed action held.
func (m *Manager) verifyRetainedResetTarget(ctx context.Context, action *store.ManagedLifecycleAction) (*store.ManagedSessionArtifactRecord, error) {
	if action == nil || action.SourceFence == nil || action.TargetFence == nil || action.TargetArtifactID == "" || (action.Stage != "target_ready" && action.Stage != "old_artifact_cleanup") {
		return nil, errors.New("reset target retry has incomplete lineage")
	}
	sourceArtifact, err := m.store.ManagedSessionArtifactForSession(ctx, action.AgentID, action.SourceFence.SessionEpoch, action.SourceFence.PiSessionID)
	if err != nil || sourceArtifact.CleanupState != "retained" || (sourceArtifact.DiscoveryState != "materialized" && sourceArtifact.DiscoveryState != "not_materialized") {
		return nil, errors.New("reset source artifact is not safely retained")
	}
	targetArtifact, err := m.store.ManagedSessionArtifact(ctx, action.TargetArtifactID)
	if err != nil || targetArtifact.AgentID != action.AgentID || targetArtifact.SessionEpoch != action.TargetFence.SessionEpoch || targetArtifact.PiSessionID != action.TargetFence.PiSessionID || targetArtifact.DiscoveryState != "materialized" || targetArtifact.CleanupState != "retained" {
		return nil, errors.New("reset target artifact is not safely retained")
	}
	return targetArtifact, nil
}

// clearRetainedResetTargetAfterExactAbsence owns the only probe/reap boundary
// for a target-start retry. It never signals present, mismatched, or unknown
// groups, and it records the cleared all-or-null target fence only after the
// exact target group is absent.
func (m *Manager) clearRetainedResetTargetAfterExactAbsence(ctx context.Context, action *store.ManagedLifecycleAction) error {
	if action == nil || action.SourceFence == nil || action.TargetFence == nil {
		return errors.New("reset target retry has no target fence")
	}
	identity := oldFenceIdentity(*action.TargetFence)
	switch m.processOps.Probe(identity).State {
	case ProcessAbsent:
	case ProcessOwned:
		if !m.reapRestartGroup(ctx, identity, nil) {
			return errors.New("reset target retry could not reap retained target")
		}
	default:
		return errors.New("reset target retry identity is unsafe")
	}
	return m.store.ClearResetTargetAfterVerifiedAbsence(ctx, action.ActionID, *action.SourceFence, *action.TargetFence)
}

func (m *Manager) executeResetInLane(sourceBinding *managedBinding, actionID string, source store.ManagedRuntimeFence, targetSession, targetGeneration string, targetEpoch int64, mode SessionLaunchMode) {
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil || action.Kind != "reset" || action.SourceFence == nil || !sameBindingFence(*action.SourceFence, source) {
		return
	}
	if action.State == "old_artifact_cleanup" && action.TargetFence != nil {
		m.completeResetOldArtifactCleanup(actionID, source, *action.TargetFence)
		if sourceBinding != nil {
			if completed, readErr := m.store.ManagedLifecycleAction(context.Background(), actionID); readErr == nil && completed.State == "succeeded" {
				sourceBinding.serial.Lock()
				if sourceBinding.operationID == actionID {
					sourceBinding.lifecycleAction = ""
				}
				sourceBinding.serial.Unlock()
			}
		}
		return
	}
	if action.State == "starting_target" && action.Stage == "target_start" {
		// Retry admission already proved the old target absent and cleared its
		// fence. Do not inspect, settle, close, or reap the original source.
		m.startResetTargetInLane(actionID, source, targetSession, targetGeneration, targetEpoch, mode)
		return
	}
	if action.State != "admitted" || action.Stage != "admission" {
		return
	}

	if sourceBinding == nil {
		// A retry may follow a target that was safely reaped before readiness.
		// The source artifact was captured by the predecessor; no RPC attachment
		// or process signal is permitted for that already-absent source.
		if artifact, readErr := m.store.ManagedSessionArtifactForSession(context.Background(), source.AgentID, source.SessionEpoch, source.PiSessionID); readErr != nil || artifact.CleanupState != "retained" {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
			return
		}
		if err := m.store.AdvanceManagedReset(context.Background(), actionID, "admitted", "admission", "awaiting_settle", "source_settle", source); err != nil {
			return
		}
		if err := m.store.AdvanceManagedReset(context.Background(), actionID, "awaiting_settle", "source_settle", "stopping_source", "source_stop", source); err != nil {
			return
		}
		_ = m.store.InterruptManagedParentWork(context.Background(), source)
		if err := m.store.MarkManagedResetSourceStopped(context.Background(), actionID, source); err != nil {
			return
		}
		m.startResetTargetInLane(actionID, source, targetSession, targetGeneration, targetEpoch, mode)
		return
	}
	sourceBinding.serial.Lock()
	if !m.restartBindingCurrent(sourceBinding, source, sourceBinding.operationID) || sourceBinding.lifecycleAction != "reset" {
		sourceBinding.serial.Unlock()
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		return
	}
	sourceRPC, sourceProcess, sourceWait := sourceBinding.rpc, sourceBinding.process, sourceBinding.waitDone
	sourceBinding.serial.Unlock()
	if sourceRPC == nil || sourceProcess == nil || sourceWait == nil {
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		return
	}
	state, err := sourceRPC.GetState(m.runCtx)
	if err != nil || !stateSessionMatches(state, source.PiSessionID) {
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		return
	}
	if _, err := m.artifacts.discover(context.Background(), source.AgentID, source.SessionEpoch, source.PiSessionID, state); err != nil {
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		return
	}
	if err := m.store.AdvanceManagedReset(context.Background(), actionID, "admitted", "admission", "awaiting_settle", "source_settle", source); err != nil {
		return
	}
	agent, err := m.store.ManagedAgent(context.Background(), source.AgentID)
	if err != nil {
		return
	}
	if agent.ActivityState == "active" {
		settler, ok := sourceRPC.(RestartTurnSettler)
		if !ok {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_RECOVERY_NEEDS_SETTLE", false, source)
			return
		}
		settleCtx, cancel := context.WithTimeout(m.runCtx, m.config.Limits.GracefulStop)
		err = settler.SettlePreparedRestartTurn(settleCtx, source)
		cancel()
		if err != nil {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_RECOVERY_NEEDS_SETTLE", false, source)
			return
		}
	}
	if err := m.store.AdvanceManagedReset(context.Background(), actionID, "awaiting_settle", "source_settle", "stopping_source", "source_stop", source); err != nil {
		return
	}
	if m.processOps.Probe(oldFenceIdentity(source)).State != ProcessOwned || m.processOps.CloseProcessInput(sourceProcess) != nil || !m.reapRestartGroup(m.runCtx, oldFenceIdentity(source), sourceWait) {
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		return
	}
	m.interruptBuiltinToolInvocations(sourceBinding, source)
	_ = m.store.InterruptManagedParentWork(context.Background(), source)
	if err := m.store.MarkManagedResetSourceStopped(context.Background(), actionID, source); err != nil {
		return
	}
	_ = sourceRPC.Close()
	sourceBinding.serial.Lock()
	sourceBinding.reaped = true
	sourceBinding.serial.Unlock()
	m.startResetTargetInLane(actionID, source, targetSession, targetGeneration, targetEpoch, mode)
}

func (m *Manager) startResetTargetInLane(actionID string, source store.ManagedRuntimeFence, targetSession, targetGeneration string, targetEpoch int64, mode SessionLaunchMode) {
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil || action.Kind != "reset" || action.State != "starting_target" || action.Stage != "target_start" || action.SourceFence == nil || !sameBindingFence(*action.SourceFence, source) {
		return
	}
	agent, err := m.store.ManagedAgent(context.Background(), source.AgentID)
	if err != nil || agent.LifecycleState != "resetting" || agent.ActivityState != "stopped" || agent.AcceptingPrompts || !agent.SlotHeld {
		return
	}
	if mode == CreateNew {
		if action.TargetArtifactID != "" || targetSession == "" || targetGeneration != action.PlannedTargetRuntimeGeneration {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
			return
		}
		if err := m.ensureFreshManagedSession(context.Background(), source.AgentID, targetSession); err != nil {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
			return
		}
	}
	epoch := targetEpoch
	if epoch <= source.SessionEpoch {
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		return
	}
	if mode == ResumeExisting {
		artifact, readErr := m.store.ManagedSessionArtifact(context.Background(), action.TargetArtifactID)
		if readErr != nil || artifact.AgentID != source.AgentID || artifact.DiscoveryState != "materialized" || artifact.CleanupState != "retained" || artifact.PiSessionID != targetSession || artifact.SessionEpoch != epoch || targetGeneration != action.PlannedTargetRuntimeGeneration {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
			return
		}
	}
	spec := CommandSpec{PiExecutable: m.config.PiExecutable, SessionRoot: m.config.SessionRoot, SessionID: targetSession, LaunchMode: mode, Cwd: agent.Cwd, Provider: agent.SelectedModel.Provider, ModelID: agent.SelectedModel.ID, MechaRPCExtension: m.config.MechaRPCExtension, PiSubagentsExtension: m.config.PiSubagentsExtension, CursorExtension: m.config.CursorExtension, TestOnlyExtensions: m.config.TestOnlyExtensions, Environment: ChildEnvironmentFor(m.config, targetGeneration, epoch)}
	process, err := m.processFactory.Start(m.runCtx, spec)
	if err != nil {
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_TARGET_START_FAILED", true, source)
		return
	}
	if m.afterResetTargetStartBeforeBind != nil && m.afterResetTargetStartBeforeBind(context.Background(), actionID, process) != nil {
		// This models a router crash after ProcessFactory.Start but before any
		// target fence/artifact commit. Recovery has no identity it may signal,
		// so it must terminalize this action without a second start.
		return
	}
	identity := process.Identity()
	candidate := &managedBinding{agentID: source.AgentID, lane: m.laneFor(source.AgentID), operationID: action.OperationID, lifecycleAction: "reset", process: process, identity: identity, sessionID: targetSession, sessionEpoch: epoch, runtimeGeneration: targetGeneration, manifestRevision: source.ManifestRevision, manifestHash: source.ManifestHash}
	m.watchRestartProcess(candidate)
	if ValidateIdentity(identity) != nil || identity.Incarnation == source.ProcessIncarnation || m.processOps.Probe(identity).State != ProcessOwned {
		if m.cleanupUninstalledTarget(process, identity, candidate.waitDone) {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_TARGET_IDENTITY_UNSAFE", true, source)
		} else {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		}
		return
	}
	rpc, err := m.rpcFactory.Open(m.runCtx, process)
	if err != nil {
		if m.cleanupUninstalledTarget(process, identity, candidate.waitDone) {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_TARGET_START_FAILED", true, source)
		} else {
			_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		}
		return
	}
	candidate.serial.Lock()
	candidate.rpc = rpc
	candidate.serial.Unlock()
	state, err := rpc.GetState(m.runCtx)
	artifact, artifactErr := m.artifacts.discover(context.Background(), source.AgentID, epoch, targetSession, state)
	if err != nil || artifactErr != nil || !stateSessionMatches(state, targetSession) {
		_ = rpc.Close()
		// Before BindManagedResetTarget no durable target fence exists. The
		// bounded rule leaves this process untouched and never starts another.
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		return
	}
	target := bindingFence(source.AgentID, candidate)
	if err := m.store.BindManagedResetTarget(context.Background(), actionID, source, target, artifact.ArtifactID); err != nil {
		_ = rpc.Close()
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, source)
		return
	}
	if !m.installBinding(candidate) {
		_ = rpc.Close()
		m.cleanupResetTarget(actionID, source, target, candidate, "RESET_TARGET_START_FAILED")
		return
	}
	if err := m.readinessGate(m.runCtx, candidate, target); err != nil {
		_ = rpc.Close()
		m.cleanupResetTarget(actionID, source, target, candidate, "RESET_TARGET_START_FAILED")
		return
	}
	select {
	case <-candidate.waitSignal:
		_ = rpc.Close()
		m.cleanupResetTarget(actionID, source, target, candidate, "RESET_TARGET_START_FAILED")
		return
	default:
	}
	if err := m.store.MarkManagedResetTargetReady(context.Background(), actionID, target); err != nil {
		_ = rpc.Close()
		m.cleanupResetTarget(actionID, source, target, candidate, "RESET_TARGET_START_FAILED")
		return
	}
	m.installRootControlReceiver(candidate)
	m.installChildDeltaReceiver(candidate)
	m.installParentCompactionReceiver(candidate)
	m.installParentCompactionNotificationReceiver(candidate)
	m.installParentActivityReceiver(candidate)
	m.installQualifiedGoalActivityReceiver(candidate)
	m.installBuiltinToolLifecycleReceiver(candidate)
	candidate.serial.Lock()
	candidate.lifecycleAction = "reset"
	candidate.serial.Unlock()
	m.completeResetOldArtifactCleanup(actionID, source, target)
	if action, err := m.store.ManagedLifecycleAction(context.Background(), actionID); err == nil && action.State == "succeeded" {
		candidate.serial.Lock()
		candidate.lifecycleAction = ""
		candidate.serial.Unlock()
		m.recoverManagedDirectAttemptsAsync(candidate.agentID)
		m.recoverManagedGoalProjection(candidate)
		m.watchReadyRestartTarget(candidate)
	}
}

func (m *Manager) cleanupResetTarget(actionID string, source, target store.ManagedRuntimeFence, binding *managedBinding, code string) {
	if binding == nil || !m.cleanupUninstalledTarget(binding.process, binding.identity, binding.waitDone) {
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_TARGET_CLEANUP_FAILED", false, target)
		return
	}
	binding.serial.Lock()
	binding.reaped = true
	binding.serial.Unlock()
	m.clearBinding(binding)
	_ = m.store.FailManagedReset(context.Background(), actionID, code, true, target)
}

func (m *Manager) completeResetOldArtifactCleanup(actionID string, source, target store.ManagedRuntimeFence) {
	if m.beforeResetOldArtifactCleanup != nil && m.beforeResetOldArtifactCleanup(context.Background(), actionID) != nil {
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_OLD_ARTIFACT_CLEANUP_FAILED", true, target)
		return
	}
	artifact, err := m.store.ManagedSessionArtifactForSession(context.Background(), source.AgentID, source.SessionEpoch, source.PiSessionID)
	if err != nil || m.artifacts.unlink(context.Background(), artifact.ArtifactID) != nil {
		_ = m.store.FailManagedReset(context.Background(), actionID, "RESET_OLD_ARTIFACT_CLEANUP_FAILED", true, target)
		return
	}
	_ = m.store.CompleteManagedReset(context.Background(), actionID, target)
}

// RecoverPendingManagedLaunches is deliberately terminalization-only. It
// probes only the post-027 launch group and never attaches or relaunches a
// desired-running row.
func (m *Manager) RecoverPendingManagedLaunches(ctx context.Context) error {
	agents, err := m.store.PendingManagedLaunches(ctx)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		lane := m.laneFor(agent.AgentID)
		lane.serial.Lock()
		fence := storedAgentFence(agent)
		code, message := "LAUNCH_RECOVERY_IDENTITY_UNSAFE", "managed launch identity could not be verified"
		if validRestartFence(fence) {
			switch m.processOps.Probe(oldFenceIdentity(fence)).State {
			case ProcessAbsent:
				code, message = "LAUNCH_RECOVERY_CANCELLED", "managed launch cancelled during recovery"
			case ProcessOwned:
				if m.reapRestartGroup(context.Background(), oldFenceIdentity(fence), nil) {
					code, message = "LAUNCH_RECOVERY_CANCELLED", "managed launch cancelled during recovery"
				} else {
					lane.serial.Unlock()
					return fmt.Errorf("managed launch recovery could not reap %s", agent.AgentID)
				}
			}
		}
		if validRestartFence(fence) {
			_ = m.store.InterruptManagedParentWork(context.Background(), fence)
		}
		if terminalErr := m.store.TerminalizeRecoveredManagedLaunch(ctx, agent.AgentID, agent.LaunchOperationID, code, message); terminalErr != nil {
			lane.serial.Unlock()
			return terminalErr
		}
		lane.serial.Unlock()
	}
	return nil
}

// RecoverManagedLifecycleControls resumes termination first: a durable
// terminate tombstone has already cancelled root controls and must outrank any
// reset or restart recovery that could otherwise revive its process.
func (m *Manager) RecoverManagedLifecycleControls(ctx context.Context) error {
	if err := m.store.ClearManagedLifecycleRecoveryClaims(ctx); err != nil {
		return err
	}
	terminations, err := m.store.ManagedTerminationActions(ctx)
	if err != nil {
		return err
	}
	for _, action := range terminations {
		lane := m.laneFor(action.AgentID)
		lane.serial.Lock()
		owner, idErr := m.ids.Next()
		if idErr != nil {
			lane.serial.Unlock()
			return idErr
		}
		if _, claimErr := m.store.ClaimManagedLifecycleRecovery(ctx, action.ActionID, owner); claimErr == nil {
			m.recoverManagedTerminationActionInLane(action)
		}
		lane.serial.Unlock()
	}
	actions, err := m.store.ManagedResetActions(ctx)
	if err != nil {
		return err
	}
	for _, action := range actions {
		lane := m.laneFor(action.AgentID)
		lane.serial.Lock()
		owner, idErr := m.ids.Next()
		if idErr != nil {
			lane.serial.Unlock()
			return idErr
		}
		claim, claimErr := m.store.ClaimManagedLifecycleRecovery(ctx, action.ActionID, owner)
		if claimErr != nil {
			lane.serial.Unlock()
			continue
		}
		m.recoverResetActionInLane(claim, action)
		lane.serial.Unlock()
	}
	return m.RecoverManagedLifecycleRestarts(ctx)
}

func (m *Manager) recoverResetActionInLane(claim *store.ManagedLifecycleRecoveryClaim, action *store.ManagedLifecycleAction) {
	if claim == nil || action == nil {
		return
	}
	probe := m.processOps.Probe(oldFenceIdentity(claim.Fence))
	switch action.Stage {
	case "source_settle":
		if probe.State == ProcessAbsent {
			_ = m.store.AdvanceManagedReset(context.Background(), action.ActionID, "awaiting_settle", "source_settle", "starting_target", "target_start", claim.Fence)
			return
		}
		_ = m.store.FailManagedReset(context.Background(), action.ActionID, "RESET_RECOVERY_NEEDS_SETTLE", false, claim.Fence)
	case "source_stop":
		if probe.State == ProcessAbsent || (probe.State == ProcessOwned && m.reapRestartGroup(context.Background(), oldFenceIdentity(claim.Fence), nil)) {
			_ = m.store.InterruptManagedParentWork(context.Background(), claim.Fence)
			_ = m.store.MarkManagedResetSourceStopped(context.Background(), action.ActionID, claim.Fence)
			return
		}
		_ = m.store.FailManagedReset(context.Background(), action.ActionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, claim.Fence)
	case "target_start":
		// A target may have started before its fence committed. The durable row
		// contains no identity for that window, so fail closed without a signal.
		_ = m.store.FailManagedReset(context.Background(), action.ActionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, claim.Fence)
	case "target_ready", "old_artifact_cleanup":
		// Bound-target recovery has an exact durable group. It first proves that
		// group absent (or safely reaps the owned group), clears every target
		// field with that proof, then resumes the retained session. It never
		// falls back to source settlement or allocates another session/epoch.
		if action.SourceFence == nil || action.TargetFence == nil || !sameBindingFence(*action.TargetFence, claim.Fence) {
			_ = m.store.FailManagedReset(context.Background(), action.ActionID, "RESET_TARGET_CLEANUP_FAILED", false, claim.Fence)
			return
		}
		artifact, artifactErr := m.verifyRetainedResetTarget(context.Background(), action)
		if artifactErr != nil {
			_ = m.store.FailManagedReset(context.Background(), action.ActionID, "RESET_TARGET_CLEANUP_FAILED", false, claim.Fence)
			return
		}
		switch probe.State {
		case ProcessAbsent:
		case ProcessOwned:
			if !m.reapRestartGroup(context.Background(), oldFenceIdentity(claim.Fence), nil) {
				_ = m.store.FailManagedReset(context.Background(), action.ActionID, "RESET_TARGET_CLEANUP_FAILED", false, claim.Fence)
				return
			}
		default:
			_ = m.store.FailManagedReset(context.Background(), action.ActionID, "RESET_TARGET_CLEANUP_FAILED", false, claim.Fence)
			return
		}
		if err := m.store.ClearResetTargetAfterVerifiedAbsence(context.Background(), action.ActionID, *action.SourceFence, claim.Fence); err != nil {
			return
		}
		m.startResetTargetInLane(action.ActionID, *action.SourceFence, artifact.PiSessionID, action.PlannedTargetRuntimeGeneration, artifact.SessionEpoch, ResumeExisting)
	default:
		_ = m.store.FailManagedReset(context.Background(), action.ActionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, claim.Fence)
	}
}

func (m *Manager) shutdownResetBinding(ctx context.Context, binding *managedBinding) (bool, error) {
	if binding == nil || binding.lane == nil {
		return false, nil
	}
	binding.lane.serial.Lock()
	defer binding.lane.serial.Unlock()
	binding.serial.Lock()
	if binding.lifecycleAction != "reset" {
		binding.serial.Unlock()
		return false, nil
	}
	fence, process, rpc, waitDone := bindingFence(binding.agentID, binding), binding.process, binding.rpc, binding.waitDone
	binding.serial.Unlock()
	action, err := m.store.ManagedLifecycleAction(context.Background(), binding.operationID)
	if err != nil || action.Kind != "reset" {
		return true, err
	}
	if process == nil || process.Identity() != binding.identity {
		return true, &store.ManagedRestartStaleError{AgentID: binding.agentID}
	}
	if rpc != nil {
		_ = rpc.Close()
	}
	if !m.reapRestartGroup(ctx, oldFenceIdentity(fence), waitDone) {
		return true, &store.ManagedRestartStaleError{AgentID: binding.agentID}
	}
	binding.serial.Lock()
	binding.reaped = true
	binding.serial.Unlock()
	if action.TargetFence != nil && sameBindingFence(*action.TargetFence, fence) {
		return true, m.store.ClearResetTargetAfterVerifiedAbsence(context.Background(), action.ActionID, *action.SourceFence, fence)
	}
	if action.SourceFence != nil && sameBindingFence(*action.SourceFence, fence) && action.State == "stopping_source" {
		_ = m.store.InterruptManagedParentWork(context.Background(), fence)
		return true, m.store.MarkManagedResetSourceStopped(context.Background(), action.ActionID, fence)
	}
	return true, nil
}
