package managed

import (
	"context"
	"database/sql"
	"errors"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// startLifecycleRestart owns both preparation and execution. The preparation
// wait group is deliberately registered before Close can observe the binding:
// shutdown either sees the artifact handoff or performs that same handoff.
func (m *Manager) startLifecycleRestart(binding *managedBinding, actionID string, source store.ManagedRuntimeFence) {
	if binding == nil || binding.lane == nil {
		return
	}
	binding.preparationWG.Add(1)
	if !m.startManagedTask(func() {
		defer binding.preparationWG.Done()
		binding.lane.serial.Lock()
		defer binding.lane.serial.Unlock()
		m.executeLifecycleRestartInLane(binding, actionID, source)
	}) {
		binding.preparationWG.Done()
	}
}

func (m *Manager) lifecycleRestartAction(actionID string, source store.ManagedRuntimeFence) (*store.ManagedLifecycleAction, error) {
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil {
		return nil, err
	}
	if action.Kind != "restart" || action.ExecutionOwner != "lifecycle" || action.SourceFence == nil || !sameBindingFence(*action.SourceFence, source) {
		return nil, &store.ManagedRestartStaleError{AgentID: source.AgentID}
	}
	return action, nil
}

// attachLifecycleRestartArtifact is the one source-side continuation handoff.
// It gets state under the graceful deadline, records the controlled artifact,
// proves it is materialized and retained, and fences the attachment before any
// source input is closed.
func (m *Manager) attachLifecycleRestartArtifact(binding *managedBinding, actionID string, source store.ManagedRuntimeFence) (*store.ManagedSessionArtifactRecord, error) {
	if binding == nil {
		return nil, errors.New("managed restart binding is unavailable")
	}
	binding.serial.Lock()
	if binding.reaped || binding.operationID != actionID || !sameBindingFence(bindingFence(source.AgentID, binding), source) || binding.rpc == nil {
		binding.serial.Unlock()
		return nil, errors.New("managed restart binding changed")
	}
	rpc := binding.rpc
	binding.serial.Unlock()
	deadline, cancel := m.preparationDeadline(context.Background(), m.config.Limits.GracefulStop)
	if cancel == nil {
		cancel = func() {}
	}
	state, err := rpc.GetState(deadline)
	cancel()
	if err != nil || !stateSessionMatches(state, source.PiSessionID) {
		return nil, errors.New("managed restart source state is unavailable")
	}
	artifact, err := m.artifacts.discover(context.Background(), source.AgentID, source.SessionEpoch, source.PiSessionID, state)
	if err != nil {
		return nil, err
	}
	artifact, err = m.artifacts.verifyRetainedMaterialized(context.Background(), artifact.ArtifactID, source.AgentID, source.PiSessionID, source.SessionEpoch)
	if err != nil {
		return nil, err
	}
	if err := m.store.AttachManagedLifecycleRestartResumeArtifact(context.Background(), actionID, source, artifact.ArtifactID); err != nil {
		// A close may have attached the same artifact while GetState ran. That is
		// safe only when the durable action already names this exact authority.
		action, readErr := m.lifecycleRestartAction(actionID, source)
		if readErr == nil && action.TargetArtifactID == artifact.ArtifactID {
			return artifact, nil
		}
		return nil, err
	}
	return artifact, nil
}

func (m *Manager) executeLifecycleRestartInLane(binding *managedBinding, actionID string, source store.ManagedRuntimeFence) {
	action, err := m.lifecycleRestartAction(actionID, source)
	if err != nil || action.State != "admitted" || action.Stage != "admission" {
		return
	}
	if _, err := m.attachLifecycleRestartArtifact(binding, actionID, source); err != nil {
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_PREPARATION_FAILED", source)
		return
	}
	binding.serial.Lock()
	if !m.restartBindingCurrent(binding, source, actionID) || binding.lifecycleAction != "restart" {
		binding.serial.Unlock()
		return
	}
	sourceRPC, sourceProcess, sourceWait := binding.rpc, binding.process, binding.waitDone
	binding.serial.Unlock()
	if sourceRPC == nil || sourceProcess == nil || sourceWait == nil {
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	agent, err := m.store.ManagedAgent(context.Background(), source.AgentID)
	if err != nil || !sameBindingFence(storedAgentFence(agent), source) {
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	if agent.ActivityState == "active" {
		if err := m.store.AdvanceManagedLifecycleRestartToSettle(context.Background(), actionID, source); err != nil {
			return
		}
		settler, ok := sourceRPC.(RestartTurnSettler)
		if !ok {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_RECOVERY_NEEDS_SETTLE", source)
			return
		}
		// Close cancels runCtx before it waits for lifecycle work. Settlement is
		// still mandatory for an active source, so it has its own bounded context.
		settleCtx, cancel := context.WithTimeout(context.Background(), m.config.Limits.GracefulStop)
		err = settler.SettlePreparedRestartTurn(settleCtx, source)
		cancel()
		binding.serial.Lock()
		current := m.restartBindingMatches(binding, source, actionID, true) && binding.lifecycleAction == "restart"
		binding.serial.Unlock()
		if err != nil {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_RECOVERY_NEEDS_SETTLE", source)
			return
		}
		if !current {
			return
		}
	}
	if err := m.store.AdvanceManagedLifecycleRestartToSourceStop(context.Background(), actionID, source); err != nil {
		return
	}
	if !m.stopLifecycleRestartSourceInLane(binding, actionID, source, sourceRPC, sourceProcess, sourceWait) {
		return
	}
	if m.isClosed() {
		return
	}
	m.startLifecycleRestartTargetInLane(actionID, source)
}

// stopLifecycleRestartSourceInLane closes source input only after source_stop
// is durable. It reaps the exact group and records absence before the old RPC
// is closed or a target can be considered.
func (m *Manager) stopLifecycleRestartSourceInLane(binding *managedBinding, actionID string, source store.ManagedRuntimeFence, rpc RPC, process Process, waitDone <-chan error) bool {
	state := m.processOps.Probe(oldFenceIdentity(source)).State
	if state == ProcessOwned {
		if err := m.processOps.CloseProcessInput(process); err != nil {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_STOP_FAILED", source)
			return false
		}
		if !m.reapRestartGroup(context.Background(), oldFenceIdentity(source), waitDone) {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_STOP_FAILED", source)
			return false
		}
	} else if state != ProcessAbsent {
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return false
	}
	if err := m.store.MarkManagedLifecycleRestartSourceStopped(context.Background(), actionID, source); err != nil {
		return false
	}
	if rpc != nil {
		_ = rpc.Close()
	}
	if binding != nil {
		binding.serial.Lock()
		binding.reaped = true
		binding.serial.Unlock()
		m.clearBinding(binding)
	}
	return true
}

func (m *Manager) verifyLifecycleRestartArtifact(action *store.ManagedLifecycleAction, source store.ManagedRuntimeFence) (*store.ManagedSessionArtifactRecord, error) {
	if action == nil || action.TargetArtifactID == "" || action.SourceFence == nil || !sameBindingFence(*action.SourceFence, source) {
		return nil, errors.New("managed restart continuation artifact is missing")
	}
	return m.artifacts.verifyRetainedMaterialized(context.Background(), action.TargetArtifactID, source.AgentID, source.PiSessionID, source.SessionEpoch)
}

func (m *Manager) startLifecycleRestartTargetInLane(actionID string, source store.ManagedRuntimeFence) {
	action, err := m.lifecycleRestartAction(actionID, source)
	if err != nil || action.State != "starting_target" || action.Stage != "target_start" || action.TargetFence != nil || action.RecoveryOwner != "" {
		return
	}
	if _, err := m.verifyLifecycleRestartArtifact(action, source); err != nil {
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	agent, err := m.store.ManagedAgent(context.Background(), source.AgentID)
	if err != nil || !sameBindingFence(storedAgentFence(agent), source) || agent.LifecycleState != "restarting" || agent.ActivityState != "stopped" || agent.AcceptingPrompts || !agent.SlotHeld || action.PlannedTargetRuntimeGeneration == "" {
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	if m.manifest.CanonicalSHA256 != source.ManifestHash || manifestRevision(m.manifest) != source.ManifestRevision {
		_ = m.store.RecordManagedLifecycleRestartReplacementDiagnostic(context.Background(), actionID, source)
		return
	}
	// The action ID is immutable and unique for this one target-start window.
	// It avoids consuming a second request ID after admission while still
	// making the pre-bind marker durable and action-specific.
	attemptID := actionID
	if err := m.store.ClaimManagedLifecycleRestartTargetStart(context.Background(), actionID, attemptID, source, action.PlannedTargetRuntimeGeneration); err != nil {
		return
	}
	spec := CommandSpec{PiExecutable: m.config.PiExecutable, SessionRoot: m.config.SessionRoot, SessionID: source.PiSessionID, LaunchMode: ResumeExisting, Cwd: agent.Cwd, Provider: agent.SelectedModel.Provider, ModelID: agent.SelectedModel.ID, MechaRPCExtension: m.config.MechaRPCExtension, PiSubagentsExtension: m.config.PiSubagentsExtension, CursorExtension: m.config.CursorExtension, TestOnlyExtensions: m.config.TestOnlyExtensions, Environment: ChildEnvironmentFor(m.config, action.PlannedTargetRuntimeGeneration, source.SessionEpoch)}
	process, err := m.processFactory.Start(m.runCtx, spec)
	if err != nil {
		if StartErrorProvesNoChild(err) {
			_ = m.store.ReleaseManagedLifecycleRestartTargetStartAttemptWithoutChild(context.Background(), actionID, attemptID, source)
			_ = m.store.RecordManagedLifecycleRestartReplacementDiagnostic(context.Background(), actionID, source)
			return
		}
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	if m.afterLifecycleRestartTargetStartBeforeBind != nil && m.afterLifecycleRestartTargetStartBeforeBind(context.Background(), actionID, process) != nil {
		// The durable marker is already written. A crash here has no target fence,
		// so recovery must fail closed without signaling or replacing this process.
		return
	}
	identity := process.Identity()
	candidate := &managedBinding{agentID: source.AgentID, lane: m.laneFor(source.AgentID), operationID: actionID, lifecycleAction: "restart", process: process, identity: identity, sessionID: source.PiSessionID, sessionEpoch: source.SessionEpoch, runtimeGeneration: action.PlannedTargetRuntimeGeneration, manifestRevision: source.ManifestRevision, manifestHash: source.ManifestHash}
	m.watchLifecycleRestartProcess(candidate)
	if ValidateIdentity(identity) != nil || identity.Incarnation == source.ProcessIncarnation || m.processOps.Probe(identity).State != ProcessOwned {
		if m.cleanupUninstalledTarget(process, identity, candidate.waitDone) {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		} else {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		}
		return
	}
	rpc, err := m.rpcFactory.Open(m.runCtx, process)
	if err != nil {
		_ = m.cleanupUninstalledTarget(process, identity, candidate.waitDone)
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	candidate.serial.Lock()
	candidate.rpc = rpc
	candidate.serial.Unlock()
	state, stateErr := rpc.GetState(m.runCtx)
	if stateErr != nil || !stateSessionMatches(state, source.PiSessionID) {
		_ = rpc.Close()
		_ = m.cleanupUninstalledTarget(process, identity, candidate.waitDone)
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	// Discover refreshes the same durable source-session row. A different path
	// or inode is rejected by the artifact owner before we bind this target.
	artifact, artifactErr := m.artifacts.discover(context.Background(), source.AgentID, source.SessionEpoch, source.PiSessionID, state)
	if artifactErr != nil || artifact.ArtifactID != action.TargetArtifactID {
		_ = rpc.Close()
		_ = m.cleanupUninstalledTarget(process, identity, candidate.waitDone)
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	if _, artifactErr = m.verifyLifecycleRestartArtifact(action, source); artifactErr != nil {
		_ = rpc.Close()
		_ = m.cleanupUninstalledTarget(process, identity, candidate.waitDone)
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	target := bindingFence(source.AgentID, candidate)
	if err := m.store.InstallManagedLifecycleRestartTarget(context.Background(), actionID, attemptID, source, target); err != nil {
		_ = rpc.Close()
		_ = m.cleanupUninstalledTarget(process, identity, candidate.waitDone)
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	if !m.installBinding(candidate) {
		_ = rpc.Close()
		m.cleanupLifecycleRestartTarget(actionID, source, target, candidate)
		return
	}
	if err := m.readinessGate(m.runCtx, candidate, target); err != nil {
		_ = rpc.Close()
		m.cleanupLifecycleRestartTarget(actionID, source, target, candidate)
		return
	}
	select {
	case <-candidate.waitSignal:
		_ = rpc.Close()
		m.cleanupLifecycleRestartTarget(actionID, source, target, candidate)
		return
	default:
	}
	if err := m.store.CompleteManagedLifecycleRestart(context.Background(), actionID, target); err != nil {
		_ = rpc.Close()
		m.cleanupLifecycleRestartTarget(actionID, source, target, candidate)
		return
	}
	m.installRootControlReceiver(candidate)
	m.installChildDeltaReceiver(candidate)
	m.installParentCompactionReceiver(candidate)
	m.installParentCompactionNotificationReceiver(candidate)
	m.installParentActivityReceiver(candidate)
	candidate.serial.Lock()
	candidate.lifecycleAction = ""
	candidate.serial.Unlock()
	m.recoverManagedDirectAttemptsAsync(candidate.agentID)
	m.recoverManagedGoalProjection(candidate)
	m.watchReadyRestartTarget(candidate)
}

func (m *Manager) cleanupLifecycleRestartTarget(actionID string, source, target store.ManagedRuntimeFence, binding *managedBinding) {
	if binding == nil || !m.cleanupUninstalledTarget(binding.process, binding.identity, binding.waitDone) {
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_TARGET_REAP_FAILED", target)
		return
	}
	binding.serial.Lock()
	binding.reaped = true
	binding.serial.Unlock()
	m.clearBinding(binding)
	if err := m.store.ResetManagedLifecycleRestartTargetToStopped(context.Background(), actionID, source, target); err == nil {
		_ = m.store.RecordManagedLifecycleRestartReplacementDiagnostic(context.Background(), actionID, source)
	}
}

// watchLifecycleRestartProcess adds lifecycle-owned exit arbitration to the
// shared waiter. A source or target exit is serialized through the same lane as
// Close, so a stale watcher cannot reach a generic shutdown path.
func (m *Manager) watchLifecycleRestartProcess(binding *managedBinding) {
	m.watchRestartProcess(binding)
	go func() {
		<-binding.waitSignal
		_ = m.recordLifecycleRestartExit(binding)
	}()
}

// reapLifecycleRestartBinding is the lifecycle-only close fallback after a
// fixed terminal outcome. It deliberately never calls Process.Stop.
func (m *Manager) reapLifecycleRestartBinding(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence, process Process, rpc RPC, waitDone <-chan error, closeInput bool) error {
	if process == nil || process.Identity() != binding.identity {
		return &store.ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	state := m.processOps.Probe(oldFenceIdentity(fence)).State
	if state == ProcessOwned && closeInput {
		if err := m.processOps.CloseProcessInput(process); err != nil {
			return err
		}
	} else if state != ProcessOwned && state != ProcessAbsent {
		return &store.ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	if !m.reapRestartGroup(ctx, oldFenceIdentity(fence), waitDone) {
		return &store.ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	if rpc != nil {
		_ = rpc.Close()
	}
	binding.serial.Lock()
	binding.reaped = true
	binding.serial.Unlock()
	m.clearBinding(binding)
	return nil
}

// shutdownLifecycleRestartBinding handles only a durable lifecycle-owned
// restart. It runs before legacy 025 or generic shutdown and never calls
// Process.Stop. Close deliberately leaves unmarked target_start for recovery.
func (m *Manager) shutdownLifecycleRestartBinding(ctx context.Context, binding *managedBinding) (bool, error) {
	if binding == nil || binding.lane == nil {
		return false, nil
	}
	binding.preparationWG.Wait()
	binding.lane.serial.Lock()
	defer binding.lane.serial.Unlock()
	binding.serial.Lock()
	actionID, fence, process, rpc, waitDone := binding.operationID, bindingFence(binding.agentID, binding), binding.process, binding.rpc, binding.waitDone
	lifecycleAction := binding.lifecycleAction
	binding.serial.Unlock()
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if action.Kind != "restart" || action.ExecutionOwner != "lifecycle" {
		return false, nil
	}
	sourceMatches := action.SourceFence != nil && sameBindingFence(*action.SourceFence, fence)
	targetMatches := action.TargetFence != nil && sameBindingFence(*action.TargetFence, fence)
	if !sourceMatches && !targetMatches {
		return true, nil
	}
	if lifecycleTerminal(action) {
		// A shutdown may have made a source-settle failure terminal while Close
		// was waiting. The selected lifecycle binding still gets exact reaping.
		if lifecycleAction == "restart" {
			return true, m.reapLifecycleRestartBinding(ctx, binding, fence, process, rpc, waitDone, sourceMatches)
		}
		return false, nil
	}
	if action.State == "starting_target" && action.Stage == "target_start" && storeRestartStartMarker(action.RecoveryOwner) {
		if action.SourceFence != nil {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_IDENTITY_UNSAFE", *action.SourceFence)
		}
		return true, nil
	}
	if targetMatches {
		if err := m.reapLifecycleRestartBinding(ctx, binding, fence, process, rpc, waitDone, false); err != nil {
			return true, m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_TARGET_REAP_FAILED", fence)
		}
		return true, m.store.ResetManagedLifecycleRestartTargetToStopped(context.Background(), action.ActionID, *action.SourceFence, fence)
	}
	if action.State == "admitted" && action.Stage == "admission" && action.TargetArtifactID == "" {
		if _, attachErr := m.attachLifecycleRestartArtifact(binding, action.ActionID, fence); attachErr != nil {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_PREPARATION_FAILED", fence)
			return true, m.reapLifecycleRestartBinding(ctx, binding, fence, process, rpc, waitDone, true)
		}
		action, err = m.store.ManagedLifecycleAction(context.Background(), action.ActionID)
		if err != nil {
			return true, err
		}
	}
	if action.Stage == "admission" || action.Stage == "source_settle" {
		agent, agentErr := m.store.ManagedAgent(context.Background(), fence.AgentID)
		if agentErr != nil {
			return true, agentErr
		}
		if agent.ActivityState == "active" {
			if action.Stage == "admission" {
				if err := m.store.AdvanceManagedLifecycleRestartToSettle(context.Background(), action.ActionID, fence); err != nil {
					return true, err
				}
			}
			settler, ok := rpc.(RestartTurnSettler)
			if !ok {
				_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_RECOVERY_NEEDS_SETTLE", fence)
				return true, m.reapLifecycleRestartBinding(ctx, binding, fence, process, rpc, waitDone, true)
			}
			settleCtx, cancel := context.WithTimeout(context.Background(), m.config.Limits.GracefulStop)
			settleErr := settler.SettlePreparedRestartTurn(settleCtx, fence)
			cancel()
			if settleErr != nil {
				_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_RECOVERY_NEEDS_SETTLE", fence)
				return true, m.reapLifecycleRestartBinding(ctx, binding, fence, process, rpc, waitDone, true)
			}
		}
		if err := m.store.AdvanceManagedLifecycleRestartToSourceStop(context.Background(), action.ActionID, fence); err != nil {
			return true, err
		}
	}
	if !m.stopLifecycleRestartSourceInLane(binding, action.ActionID, fence, rpc, process, waitDone) {
		return true, nil
	}
	return true, nil
}

func lifecycleTerminal(action *store.ManagedLifecycleAction) bool {
	if action == nil {
		return true
	}
	switch action.State {
	case "succeeded", "failed", "cancelled", "superseded":
		return true
	default:
		return false
	}
}

func storeRestartStartMarker(value string) bool {
	return len(value) > len(store.RestartStartAttemptPrefix) && value[:len(store.RestartStartAttemptPrefix)] == store.RestartStartAttemptPrefix
}

// recordLifecycleRestartExit schedules the same lane owner used by close and
// execution. It selects lifecycle ownership from the durable action before the
// legacy transition or generic watcher can touch this binding.
func (m *Manager) recordLifecycleRestartExit(binding *managedBinding) bool {
	if binding == nil {
		return false
	}
	binding.serial.Lock()
	actionID, fence := binding.operationID, bindingFence(binding.agentID, binding)
	binding.serial.Unlock()
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil || action.Kind != "restart" || action.ExecutionOwner != "lifecycle" || lifecycleTerminal(action) {
		return false
	}
	matches := (action.SourceFence != nil && sameBindingFence(*action.SourceFence, fence)) || (action.TargetFence != nil && sameBindingFence(*action.TargetFence, fence)) || (action.State == "starting_target" && action.Stage == "target_start" && storeRestartStartMarker(action.RecoveryOwner))
	if !matches {
		return false
	}
	m.startManagedTask(func() {
		if binding.lane == nil {
			return
		}
		binding.lane.serial.Lock()
		defer binding.lane.serial.Unlock()
		m.handleLifecycleRestartExitInLane(binding, actionID, fence)
	})
	return true
}

func (m *Manager) handleLifecycleRestartExitInLane(binding *managedBinding, actionID string, fence store.ManagedRuntimeFence) {
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil || action.Kind != "restart" || action.ExecutionOwner != "lifecycle" || lifecycleTerminal(action) {
		return
	}
	if action.State == "starting_target" && action.Stage == "target_start" && storeRestartStartMarker(action.RecoveryOwner) {
		if action.SourceFence != nil {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_IDENTITY_UNSAFE", *action.SourceFence)
		}
		return
	}
	state := m.processOps.Probe(oldFenceIdentity(fence)).State
	if action.TargetFence != nil && sameBindingFence(*action.TargetFence, fence) {
		if state == ProcessAbsent {
			_ = m.store.ResetManagedLifecycleRestartTargetToStopped(context.Background(), actionID, *action.SourceFence, fence)
		} else {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, "RESTART_TARGET_REAP_FAILED", fence)
		}
		m.clearBinding(binding)
		return
	}
	if action.SourceFence == nil || !sameBindingFence(*action.SourceFence, fence) {
		return
	}
	if state != ProcessAbsent {
		code := "RESTART_IDENTITY_UNSAFE"
		if action.Stage == "source_stop" {
			code = "RESTART_STOP_FAILED"
		}
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), actionID, code, fence)
		return
	}
	if action.Stage == "admission" || action.Stage == "source_settle" {
		_ = m.store.AdvanceManagedLifecycleRestartToSourceStop(context.Background(), actionID, fence)
	}
	if action.Stage == "source_stop" || action.Stage == "admission" || action.Stage == "source_settle" {
		_ = m.store.MarkManagedLifecycleRestartSourceStopped(context.Background(), actionID, fence)
	}
	binding.serial.Lock()
	binding.reaped = true
	binding.serial.Unlock()
	m.clearBinding(binding)
}

// RecoverManagedLifecycleRestarts claims only lifecycle-owned restart actions.
// It never scans desired-running rows or considers legacy025 bridge entries.
func (m *Manager) RecoverManagedLifecycleRestarts(ctx context.Context) error {
	actions, err := m.store.ManagedLifecycleRestartActions(ctx)
	if err != nil {
		return err
	}
	for _, action := range actions {
		if action == nil || action.SourceFence == nil {
			continue
		}
		lane := m.laneFor(action.AgentID)
		lane.serial.Lock()
		m.recoverLifecycleRestartActionInLane(action)
		lane.serial.Unlock()
	}
	return nil
}

func (m *Manager) recoverLifecycleRestartActionInLane(action *store.ManagedLifecycleAction) {
	if action == nil || action.SourceFence == nil {
		return
	}
	source := *action.SourceFence
	if action.Stage == "target_start" {
		if storeRestartStartMarker(action.RecoveryOwner) {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_IDENTITY_UNSAFE", source)
			return
		}
		m.startLifecycleRestartTargetInLane(action.ActionID, source)
		return
	}
	// The action ID is already unique. Recovery does not mint request IDs: its
	// durable lease only needs an owner distinct from the pre-start marker.
	owner := "restart-recovery-" + action.ActionID
	claim, err := m.store.ClaimManagedLifecycleRecovery(context.Background(), action.ActionID, owner)
	if err != nil {
		return
	}
	if action.Stage == "admission" {
		if _, err := m.verifyLifecycleRestartArtifact(action, source); err != nil {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_PREPARATION_FAILED", source)
			return
		}
		probe := m.processOps.Probe(oldFenceIdentity(source)).State
		agent, agentErr := m.store.ManagedAgent(context.Background(), source.AgentID)
		if agentErr != nil {
			return
		}
		if probe == ProcessAbsent {
			_ = m.store.AdvanceManagedLifecycleRestartToSourceStop(context.Background(), action.ActionID, source)
			if m.store.MarkManagedLifecycleRestartSourceStopped(context.Background(), action.ActionID, source) == nil {
				m.startLifecycleRestartTargetInLane(action.ActionID, source)
			}
			return
		}
		if probe == ProcessOwned && agent.ActivityState == "idle" {
			if m.store.AdvanceManagedLifecycleRestartToSourceStop(context.Background(), action.ActionID, source) == nil && m.reapRestartGroup(context.Background(), oldFenceIdentity(source), nil) && m.store.MarkManagedLifecycleRestartSourceStopped(context.Background(), action.ActionID, source) == nil {
				m.startLifecycleRestartTargetInLane(action.ActionID, source)
			}
			return
		}
		if agent.ActivityState == "active" {
			if m.store.AdvanceManagedLifecycleRestartToSettle(context.Background(), action.ActionID, source) == nil {
				_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_RECOVERY_NEEDS_SETTLE", source)
			}
			return
		}
		_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_IDENTITY_UNSAFE", source)
		return
	}
	if action.Stage == "source_settle" {
		if m.processOps.Probe(oldFenceIdentity(source)).State == ProcessAbsent {
			if m.store.AdvanceManagedLifecycleRestartToSourceStop(context.Background(), action.ActionID, source) == nil && m.store.MarkManagedLifecycleRestartSourceStopped(context.Background(), action.ActionID, source) == nil {
				m.startLifecycleRestartTargetInLane(action.ActionID, source)
			}
		} else {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_RECOVERY_NEEDS_SETTLE", source)
		}
		return
	}
	if action.Stage == "source_stop" {
		switch m.processOps.Probe(oldFenceIdentity(source)).State {
		case ProcessAbsent:
		case ProcessOwned:
			if !m.reapRestartGroup(context.Background(), oldFenceIdentity(source), nil) {
				_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_STOP_FAILED", source)
				return
			}
		default:
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_IDENTITY_UNSAFE", source)
			return
		}
		if m.store.MarkManagedLifecycleRestartSourceStopped(context.Background(), action.ActionID, source) == nil {
			m.startLifecycleRestartTargetInLane(action.ActionID, source)
		}
		return
	}
	if action.Stage == "target_ready" && action.TargetFence != nil {
		if _, err := m.verifyLifecycleRestartArtifact(action, source); err != nil {
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_TARGET_REAP_FAILED", *action.TargetFence)
			return
		}
		target := *action.TargetFence
		switch m.processOps.Probe(oldFenceIdentity(target)).State {
		case ProcessAbsent:
		case ProcessOwned:
			if !m.reapRestartGroup(context.Background(), oldFenceIdentity(target), nil) {
				_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_TARGET_REAP_FAILED", target)
				return
			}
		default:
			_ = m.store.RecordManagedLifecycleRestartFailure(context.Background(), action.ActionID, "RESTART_TARGET_REAP_FAILED", target)
			return
		}
		if m.store.ResetManagedLifecycleRestartTargetToStopped(context.Background(), action.ActionID, source, target) == nil {
			m.startLifecycleRestartTargetInLane(action.ActionID, source)
		}
	}
	_ = claim // claim is cleared by every fenced state write above.
}

// adoptLifecycleRestartBindingInLane transfers a live idle/launch binding to
// a committed lifecycle restart. Restart All persists admission before its
// post-commit handoff, so the durable action is the authority for this narrow
// binding update. The existing binding must still match the child's full
// source fence and own a terminal predecessor; reset and other active work
// never get overwritten by a late dispatcher task.
func (m *Manager) adoptLifecycleRestartBindingInLane(ctx context.Context, action *store.ManagedLifecycleAction) (*managedBinding, bool) {
	if action == nil || action.ActionID != action.OperationID || action.Kind != "restart" || action.ExecutionOwner != "lifecycle" || action.State != "admitted" || action.Stage != "admission" || action.SourceFence == nil {
		return nil, false
	}
	binding, found := m.bindingFor(action.AgentID)
	if !found {
		return nil, false
	}
	binding.serial.Lock()
	defer binding.serial.Unlock()
	if !m.liveBindingForFence(ctx, binding, *action.SourceFence) {
		return nil, false
	}
	predecessor, err := m.store.ManagedOperation(ctx, binding.operationID)
	if err != nil || predecessor.State != "terminal" || restartActionHigherPriority(binding.lifecycleAction) {
		return nil, false
	}
	binding.operationID, binding.lifecycleAction = action.ActionID, "restart"
	m.mu.Lock()
	if m.bindings[action.AgentID] == binding {
		m.operationIDs[action.AgentID] = action.ActionID
	}
	m.mu.Unlock()
	return binding, true
}

// ExecuteLifecycleRestartAction is the narrow future-child seam. S2 does not
// create a parent, batch item, or child; a later owner can pass an already
// admitted, fully fenced restart action into this common executor.
func (m *Manager) ExecuteLifecycleRestartAction(actionID string) {
	action, err := m.store.ManagedLifecycleAction(context.Background(), actionID)
	if err != nil || action.Kind != "restart" || action.ExecutionOwner != "lifecycle" || action.SourceFence == nil {
		return
	}
	lane := m.laneFor(action.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	if binding, adopted := m.adoptLifecycleRestartBindingInLane(context.Background(), action); adopted {
		m.executeLifecycleRestartInLane(binding, actionID, *action.SourceFence)
		return
	}
	m.recoverLifecycleRestartActionInLane(action)
}
