package managed

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// managedLaunchRecipe records the fixed, non-secret inputs used by a managed
// launch. Listener URL, runtime generation, and session epoch are per-runtime
// values and are supplied when a process starts.
func (m *Manager) managedLaunchRecipe(agent store.ManagedAgentRecord) (store.ManagedLaunchRecipe, error) {
	environment := ChildEnvironmentFor(m.config, "", agent.SessionEpoch)
	stable := make([]string, 0, len(environment))
	for _, value := range environment {
		key, _, _ := strings.Cut(value, "=")
		// These values are derived from the current router instance. Persisting
		// them would make a recipe fail after a normal listener restart.
		if key == "MECHA_ROUTER_URL" || key == "MECHA_RPC_RUNTIME_GENERATION" || key == "MECHA_RPC_SESSION_EPOCH" {
			continue
		}
		// Only the fixed launch environment is durable. Unknown test or host
		// variables may contain credentials and are intentionally excluded.
		if !store.IsManagedLaunchEnvironmentKey(key) {
			continue
		}
		stable = append(stable, value)
	}
	recipe := store.ManagedLaunchRecipe{
		PiExecutable:         m.config.PiExecutable,
		SessionRoot:          m.config.SessionRoot,
		Cwd:                  agent.Cwd,
		Provider:             agent.SelectedModel.Provider,
		ModelID:              agent.SelectedModel.ID,
		MechaRPCExtension:    m.config.MechaRPCExtension,
		PiSubagentsExtension: m.config.PiSubagentsExtension,
		CursorExtension:      m.config.CursorExtension,
		TestOnlyExtensions:   append([]string(nil), m.config.TestOnlyExtensions...),
		Environment:          stable,
	}
	return store.ManagedLaunchRecipeDigest(recipe)
}

// PruneManagedRootRecovery runs the bounded action-retention pass owned by the
// managed lifecycle. The transport calls it at each ordinary sweep boundary.
func (m *Manager) PruneManagedRootRecovery(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	_, err := m.store.PruneManagedRootRecovery(ctx)
	return err
}

// RecoverManagedRootColdStarts owns ordinary desired-running rows after a
// router restart. It is deliberately separate from pending-launch recovery:
// pending launches are terminalized, while this path resumes durable agents.
func (m *Manager) RecoverManagedRootColdStarts(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := m.PruneManagedRootRecovery(ctx); err != nil {
		return err
	}
	agents, err := m.store.ManagedAgents(ctx)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if agent == nil || agent.DesiredState != "running" {
			continue
		}
		if _, bound := m.bindingFor(agent.AgentID); bound {
			continue
		}
		// A crash after the target fence was installed leaves the durable row in
		// starting. Let recovery close that incomplete target; pending launches
		// remain owned by RecoverPendingManagedLaunches below this pass.
		if agent.LifecycleState != "running" && !m.hasColdRecoveryTarget(ctx, agent) {
			continue
		}
		if err := m.recoverManagedRoot(ctx, agent); err != nil {
			return err
		}
		if binding, live := m.bindingFor(agent.AgentID); live {
			m.recoverManagedGoalProjection(binding)
			if err := m.recoverManagedDirectAttemptsForAgent(ctx, agent.AgentID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) recoverManagedRoot(ctx context.Context, agent *store.ManagedAgentRecord) error {
	if agent.LaunchRecipe == nil {
		m.failColdRecovery(agent.AgentID, "RECOVERY_RECIPE_MISSING", "managed launch recipe is unavailable")
		return nil
	}
	recipe := *agent.LaunchRecipe
	if err := store.ValidateManagedLaunchRecipe(recipe); err != nil {
		m.failColdRecovery(agent.AgentID, "RECOVERY_RECIPE_MISMATCH", "managed launch recipe is invalid")
		return nil
	}
	expected, err := m.managedLaunchRecipe(*agent)
	if err != nil || expected.SHA256 != recipe.SHA256 {
		m.failColdRecovery(agent.AgentID, "RECOVERY_RECIPE_MISMATCH", "managed launch recipe does not match current manager configuration")
		return nil
	}
	old := storedAgentFence(agent)
	if !validRestartFence(old) || m.manifest.CanonicalSHA256 != old.ManifestHash || manifestRevision(m.manifest) != old.ManifestRevision {
		m.failColdRecovery(agent.AgentID, "ORPHAN_IDENTITY_UNSAFE", "managed process identity or manifest could not be verified")
		return nil
	}

	action, actionErr := m.store.ManagedRootRecoveryAction(ctx, agent.AgentID)
	if actionErr == nil {
		switch {
		case rootRecoveryActionFence(action) == old:
			if action.Stage == "ready" || action.Stage == "failed" {
				return nil
			}
		case rootRecoveryActionTargetsFence(action, old):
			if action.Stage == "ready" || action.Stage == "failed" {
				return nil
			}
			// The prior router installed the target fence before it stopped. Its
			// pipes cannot be reattached, so reap that exact target and publish a
			// truthful failed projection. Crucially, do not claim or start again.
			m.failColdRecoveryTarget(action, old)
			return nil
		default:
			action = nil
		}
	}
	if action == nil {
		targetGeneration, idErr := m.ids.Next()
		if idErr != nil {
			return idErr
		}
		action, _, actionErr = m.store.ClaimManagedRootRecovery(ctx, agent.AgentID, old, recipe.SHA256, targetGeneration)
		if actionErr != nil {
			// Another router owns the durable claim. Its single target start is
			// the safe result for this startup pass.
			return nil
		}
	}
	lane := m.laneFor(agent.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	return m.recoverManagedRootInLane(ctx, agent, recipe, old, action)
}

func rootRecoveryActionFence(action *store.ManagedRootRecoveryAction) store.ManagedRuntimeFence {
	if action == nil {
		return store.ManagedRuntimeFence{}
	}
	return store.ManagedRuntimeFence{AgentID: action.AgentID, PiSessionID: action.OldPiSessionID, SessionEpoch: int64(action.OldSessionEpoch), ProcessPID: action.OldProcessPID, ProcessStartMarker: action.OldProcessStartMarker, ProcessIncarnation: action.OldProcessIncarnation, RuntimeGeneration: action.OldRuntimeGeneration, ManifestRevision: action.OldManifestRevision, ManifestHash: action.OldManifestHash}
}

func rootRecoveryActionTargetsFence(action *store.ManagedRootRecoveryAction, fence store.ManagedRuntimeFence) bool {
	return action != nil && action.AgentID == fence.AgentID && action.OldPiSessionID == fence.PiSessionID && action.OldSessionEpoch == int(fence.SessionEpoch) && action.OldManifestRevision == fence.ManifestRevision && action.OldManifestHash == fence.ManifestHash && action.TargetProcessPID == fence.ProcessPID && action.TargetProcessStartMarker == fence.ProcessStartMarker && action.TargetProcessIncarnation == fence.ProcessIncarnation && action.TargetRuntimeGeneration == fence.RuntimeGeneration
}

func (m *Manager) hasColdRecoveryTarget(ctx context.Context, agent *store.ManagedAgentRecord) bool {
	action, err := m.store.ManagedRootRecoveryAction(ctx, agent.AgentID)
	return err == nil && action.Stage == "starting" && rootRecoveryActionTargetsFence(action, storedAgentFence(agent))
}

func (m *Manager) failColdRecoveryTarget(action *store.ManagedRootRecoveryAction, target store.ManagedRuntimeFence) {
	identity := oldFenceIdentity(target)
	if m.processOps.Probe(identity).State == ProcessOwned {
		_ = m.reapRestartGroup(context.Background(), identity, nil)
	}
	m.failColdRecoveryAction(action, "RECOVERY_TARGET_UNKNOWN", "replacement process was already reserved by an earlier recovery attempt")
}

func (m *Manager) recoverManagedRootInLane(ctx context.Context, agent *store.ManagedAgentRecord, recipe store.ManagedLaunchRecipe, old store.ManagedRuntimeFence, action *store.ManagedRootRecoveryAction) error {
	if action == nil {
		return nil
	}
	if action.Stage == "claimed" {
		if err := m.store.AdvanceManagedRootRecovery(ctx, action.ActionID, "claimed", "reaping"); err != nil {
			return nil
		}
		action.Stage = "reaping"
	}
	if action.Stage == "reaping" {
		probe := m.processOps.Probe(oldFenceIdentity(old)).State
		switch probe {
		case ProcessAbsent:
			_ = m.store.RecordManagedRootRecoveryUnexpectedExit(ctx, action.ActionID)
		case ProcessOwned:
			if !m.reapRestartGroup(ctx, oldFenceIdentity(old), nil) {
				m.failColdRecoveryAction(action, "ORPHAN_IDENTITY_UNSAFE", "managed orphan process group could not be reaped")
				return nil
			}
		default:
			// Present, mismatch, and unknown all fail closed. In particular, no
			// signal is sent after the PID/start-marker/group proof is lost.
			m.failColdRecoveryAction(action, "ORPHAN_IDENTITY_UNSAFE", "managed orphan process identity could not be verified")
			return nil
		}
		if err := m.store.InterruptManagedParentWork(ctx, old); err != nil {
			return err
		}
		if err := m.store.InterruptManagedControlRequests(ctx, old); err != nil {
			return err
		}
		if err := m.store.InterruptManagedGoalEvaluation(ctx, old); err != nil {
			return err
		}
		if _, err := m.store.RecordManagedParentProcessLost(ctx, old); err != nil {
			return err
		}
		m.interruptBuiltinToolInvocations(nil, old)
		if err := m.store.AdvanceManagedRootRecovery(ctx, action.ActionID, "reaping", "stopped"); err != nil {
			return nil
		}
		action.Stage = "stopped"
	}
	if action.Stage == "starting" {
		// A previous owner durably reserved or installed the target. Pipes are
		// not reconnectable, so this router never starts a second target.
		if action.TargetProcessPID > 0 {
			identity := ProcessIdentity{PID: action.TargetProcessPID, StartMarker: action.TargetProcessStartMarker, Incarnation: action.TargetProcessIncarnation}
			if m.processOps.Probe(identity).State == ProcessOwned {
				_ = m.reapRestartGroup(ctx, identity, nil)
			}
		}
		m.failColdRecoveryAction(action, "RECOVERY_TARGET_UNKNOWN", "replacement process was already reserved by an earlier recovery attempt")
		return nil
	}
	if action.Stage != "stopped" {
		return nil
	}
	if err := m.store.BeginManagedRootRecoveryTarget(ctx, action.ActionID, action.TargetRuntimeGeneration); err != nil {
		return nil
	}
	process, err := m.processFactory.Start(m.runCtx, CommandSpec{PiExecutable: recipe.PiExecutable, SessionRoot: recipe.SessionRoot, SessionID: old.PiSessionID, LaunchMode: ResumeExisting, Cwd: recipe.Cwd, Provider: recipe.Provider, ModelID: recipe.ModelID, MechaRPCExtension: recipe.MechaRPCExtension, PiSubagentsExtension: recipe.PiSubagentsExtension, CursorExtension: recipe.CursorExtension, TestOnlyExtensions: recipe.TestOnlyExtensions, Environment: recoveryEnvironment(recipe.Environment, m.config.RouterURL, action.TargetRuntimeGeneration, old.SessionEpoch)})
	if err != nil {
		m.failColdRecoveryAction(action, "RECOVERY_TARGET_START_FAILED", "replacement process did not start")
		return nil
	}
	if process == nil {
		m.failColdRecoveryAction(action, "RECOVERY_TARGET_START_FAILED", "replacement process did not start")
		return nil
	}
	identity := process.Identity()
	if ValidateIdentity(identity) != nil || m.processOps.Probe(identity).State != ProcessOwned || identity.Incarnation == old.ProcessIncarnation {
		// A returned process handle is cleaned up only after its exact group is
		// proved owned. Unknown or mismatched identities are never signaled.
		_ = m.cleanupUninstalledTarget(process, identity, nil)
		m.failColdRecoveryAction(action, "ORPHAN_IDENTITY_UNSAFE", "replacement process identity could not be verified")
		return nil
	}
	if err := m.store.InstallManagedRootRecoveryTarget(ctx, action.ActionID, store.ManagedProcessIdentity{PID: identity.PID, StartMarker: identity.StartMarker, Incarnation: identity.Incarnation}, action.TargetRuntimeGeneration); err != nil {
		if m.processOps.Probe(identity).State == ProcessOwned {
			_ = m.reapRestartGroup(ctx, identity, nil)
		}
		m.failColdRecoveryAction(action, "RECOVERY_TARGET_START_FAILED", "replacement process target could not be recorded")
		return nil
	}
	targetFence := old
	targetFence.ProcessPID, targetFence.ProcessStartMarker, targetFence.ProcessIncarnation = identity.PID, identity.StartMarker, identity.Incarnation
	targetFence.RuntimeGeneration = action.TargetRuntimeGeneration
	if err := m.store.InstallManagedRootRecoveryFence(ctx, old, targetFence); err != nil {
		if m.processOps.Probe(identity).State == ProcessOwned {
			_ = m.reapRestartGroup(ctx, identity, nil)
		}
		m.failColdRecoveryAction(action, "RECOVERY_TARGET_START_FAILED", "replacement process fence could not be installed")
		return nil
	}
	target := &managedBinding{agentID: agent.AgentID, lane: m.laneFor(agent.AgentID), operationID: action.ActionID, lifecycleAction: "root-recovery", token: targetFence.RuntimeGeneration, process: process, identity: identity, sessionID: targetFence.PiSessionID, sessionEpoch: targetFence.SessionEpoch, runtimeGeneration: targetFence.RuntimeGeneration, manifestRevision: targetFence.ManifestRevision, manifestHash: targetFence.ManifestHash}
	m.watchRestartProcess(target)
	targetRPC, err := m.rpcFactory.Open(m.runCtx, process)
	if err != nil {
		m.cleanupRecoveredTarget(target, targetFence)
		m.failColdRecoveryAction(action, "RECOVERY_READINESS_FAILED", "replacement RPC could not open")
		return nil
	}
	target.rpc = targetRPC
	if !m.installBinding(target) {
		_ = targetRPC.Close()
		m.cleanupRecoveredTarget(target, targetFence)
		m.failColdRecoveryAction(action, "RECOVERY_READINESS_FAILED", "replacement binding could not be installed")
		return nil
	}
	if err := m.readinessGate(m.runCtx, target, targetFence); err != nil {
		_ = targetRPC.Close()
		m.cleanupRecoveredTarget(target, targetFence)
		m.clearBinding(target)
		m.failColdRecoveryAction(action, "RECOVERY_READINESS_FAILED", "replacement readiness gate failed")
		return nil
	}
	if err := m.store.CompleteManagedRootRecoveryReady(ctx, agent.AgentID, targetFence); err != nil {
		_ = targetRPC.Close()
		m.cleanupRecoveredTarget(target, targetFence)
		m.clearBinding(target)
		m.failColdRecoveryAction(action, "RECOVERY_READINESS_FAILED", "replacement readiness could not be committed")
		return nil
	}
	target.serial.Lock()
	target.lifecycleAction = ""
	target.serial.Unlock()
	m.installRootControlReceiver(target)
	m.installChildDeltaReceiver(target)
	m.installParentCompactionReceiver(target)
	m.installParentCompactionNotificationReceiver(target)
	m.installParentActivityReceiver(target)
	m.installQualifiedGoalActivityReceiver(target)
	m.installBuiltinToolLifecycleReceiver(target)
	m.startRecoveredRootWatcher(target, targetFence)
	return nil
}

func recoveryEnvironment(stable []string, routerURL, generation string, epoch int64) []string {
	environment := append([]string(nil), stable...)
	environment = append(environment, "MECHA_ROUTER_URL="+routerURL, "MECHA_RPC_RUNTIME_GENERATION="+generation, "MECHA_RPC_SESSION_EPOCH="+strconv.FormatInt(epoch, 10))
	return environment
}

func (m *Manager) cleanupRecoveredTarget(target *managedBinding, fence store.ManagedRuntimeFence) {
	if target == nil || target.process == nil {
		return
	}
	if m.processOps.Probe(fenceToIdentity(fence)).State == ProcessOwned {
		_ = m.processOps.CloseProcessInput(target.process)
		_ = m.reapRestartGroup(context.Background(), fenceToIdentity(fence), target.waitDone)
	}
}

func fenceToIdentity(fence store.ManagedRuntimeFence) ProcessIdentity {
	return ProcessIdentity{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}
}

func (m *Manager) startRecoveredRootWatcher(binding *managedBinding, fence store.ManagedRuntimeFence) {
	m.startManagedTask(func() {
		select {
		case <-binding.waitSignal:
			binding.lane.serial.Lock()
			defer binding.lane.serial.Unlock()
			m.recordReadyBindingProcessExitInLane(binding, fence, "managed Pi exited after recovery")
		case <-m.runCtx.Done():
		}
	})
}

func (m *Manager) failColdRecoveryAction(action *store.ManagedRootRecoveryAction, code, message string) {
	if action == nil {
		return
	}
	_ = m.store.FailManagedRootRecovery(context.Background(), action.ActionID, code, message)
	m.failColdRecovery(action.AgentID, code, message)
}

func (m *Manager) failColdRecovery(agentID, code, message string) {
	_ = m.store.MarkManagedFailed(context.Background(), agentID, code, message)
	m.publishProjection()
}
