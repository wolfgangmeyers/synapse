package managed

import (
	"context"
	"database/sql"
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
		if agent == nil {
			continue
		}
		cleanupOnly, cleanupErr := m.cleanupFailedProcessStopAgent(ctx, agent)
		if cleanupErr != nil {
			var stale *store.ManagedRestartStaleError
			if !errors.As(cleanupErr, &stale) {
				return cleanupErr
			}
			continue
		}
		if cleanupOnly || agent.DesiredState != "running" {
			continue
		}
		cleanupOnly, cleanupErr = m.cleanupFailedColdRecoveryTarget(ctx, agent.AgentID)
		if cleanupErr != nil {
			return cleanupErr
		}
		if cleanupOnly {
			continue
		}
		if _, bound := m.bindingFor(agent.AgentID); bound {
			continue
		}
		due, dueErr := m.store.ManagedCrashRecoveryDue(ctx, agent.AgentID)
		if dueErr != nil {
			return dueErr
		}
		if !due {
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

// cleanupFailedProcessStopAgent handles only a failed desired-running row
// whose exact old process identity is retained for cleanup. Unsafe probes stay
// retained so a later owner can retry without signalling another process.
func (m *Manager) cleanupFailedProcessStopAgent(ctx context.Context, agent *store.ManagedAgentRecord) (bool, error) {
	if agent == nil || agent.DesiredState != "running" || agent.LifecycleState != "failed" || agent.LastErrorCode != "PROCESS_STOP_FAILED" {
		return false, nil
	}
	fence := storedAgentFence(agent)
	if !validRestartFence(fence) {
		return true, nil
	}
	identity := oldFenceIdentity(fence)
	switch m.processOps.Probe(identity).State {
	case ProcessAbsent:
	case ProcessOwned:
		if !m.reapRestartGroup(ctx, identity, nil) {
			return true, nil
		}
	default:
		return true, nil
	}
	if err := m.store.PrepareManagedFailedProcessStopRecovery(ctx, fence); err != nil {
		return true, err
	}
	agent.LifecycleState = "running"
	agent.ActivityState = "stopped"
	agent.AcceptingPrompts = false
	agent.SlotHeld = true
	return false, nil
}

// cleanupFailedColdRecoveryTarget is a cleanup-only startup pass. A failed
// action's target identity is authoritative for this decision even when the
// agent row now carries a different fence or lifecycle stage. Unsafe probes
// stay retained so a later owner can retry without signalling another process.
func (m *Manager) cleanupFailedColdRecoveryTarget(ctx context.Context, agentID string) (bool, error) {
	action, err := m.store.ManagedRootRecoveryAction(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if action.Stage != "failed" || action.TargetProcessPID <= 0 {
		return false, nil
	}
	identity := ProcessIdentity{PID: action.TargetProcessPID, StartMarker: action.TargetProcessStartMarker, Incarnation: action.TargetProcessIncarnation}
	if ValidateIdentity(identity) != nil {
		return true, nil
	}
	switch m.processOps.Probe(identity).State {
	case ProcessOwned:
		if !m.reapRestartGroup(ctx, identity, nil) {
			return true, nil
		}
	case ProcessAbsent:
	default:
		return true, nil
	}
	if err := m.store.ClearManagedRootRecoveryTargetAfterVerifiedAbsence(ctx, action.ActionID, store.ManagedProcessIdentity{PID: identity.PID, StartMarker: identity.StartMarker, Incarnation: identity.Incarnation}); err != nil {
		return true, err
	}
	return true, nil
}

func (m *Manager) recoverManagedRoot(ctx context.Context, agent *store.ManagedAgentRecord) error {
	// Derive and probe the old identity before reading any restart recipe or
	// current manager configuration. A malformed preflight input must not make
	// us lose the only safe opportunity to reap the exact old process.
	old := storedAgentFence(agent)
	oldProbe := m.processOps.Probe(oldFenceIdentity(old)).State
	if err := m.validateColdRecoveryPreflight(ctx, agent, old, oldProbe); err != nil {
		return err
	}
	recipe := *agent.LaunchRecipe

	action, actionErr := m.store.ManagedRootRecoveryAction(ctx, agent.AgentID)
	if actionErr != nil && !errors.Is(actionErr, sql.ErrNoRows) {
		return actionErr
	}
	if actionErr == nil {
		switch {
		case action.Stage == "ready" && rootRecoveryActionTargetsFence(action, old):
			// A ready target belongs to the previous router, whose native RPC pipe
			// cannot be reconstructed. Reap only an exactly owned target and run a
			// fresh generation so this router cannot expose a binding-less agent.
			switch oldProbe {
			case ProcessOwned:
				if !m.reapRestartGroup(ctx, oldFenceIdentity(old), nil) {
					return m.failColdRecovery(agent.AgentID, "ORPHAN_IDENTITY_UNSAFE", "ready recovery target could not be reaped")
				}
			case ProcessAbsent:
			default:
				return m.failColdRecovery(agent.AgentID, "ORPHAN_IDENTITY_UNSAFE", "ready recovery target identity could not be verified")
			}
			if err := m.store.SupersedeManagedRootRecovery(ctx, action.ActionID); err != nil {
				return err
			}
			action = nil
		case action.Stage == "failed" && (action.FailureCode == "RECOVERY_SUPERSEDED" || rootRecoveryActionFence(action) == old):
			// A concurrent router may have terminalized the prior ready action;
			// this fence can still claim one fresh attempt.
			action = nil
		case action.Stage == "ready" || action.Stage == "failed":
			return m.failColdRecovery(agent.AgentID, "RECOVERY_READINESS_FAILED", "persisted recovery state has no attachable binding")
		case rootRecoveryActionTargetsFence(action, old):
			// The prior router installed the target fence before it stopped. Its
			// pipes cannot be reattached, so reap that exact target and publish a
			// truthful failed projection. Crucially, do not claim or start again.
			return m.failColdRecoveryTarget(ctx, action, old)
		case rootRecoveryActionFence(action) != old:
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

func (m *Manager) validateColdRecoveryPreflight(ctx context.Context, agent *store.ManagedAgentRecord, old store.ManagedRuntimeFence, oldProbe ProcessProbeState) error {
	var code, message string
	if agent.LaunchRecipe == nil {
		code, message = "RECOVERY_RECIPE_MISSING", "managed launch recipe is unavailable"
	} else if err := store.ValidateManagedLaunchRecipe(*agent.LaunchRecipe); err != nil {
		code, message = "RECOVERY_RECIPE_MISMATCH", "managed launch recipe is invalid"
	} else if expected, err := m.managedLaunchRecipe(*agent); err != nil || store.ValidateManagedLaunchRecipe(expected) != nil || expected.SHA256 != agent.LaunchRecipe.SHA256 {
		code, message = "RECOVERY_RECIPE_MISMATCH", "managed launch recipe does not match current manager configuration"
	} else if !validRestartFence(old) || m.manifest.CanonicalSHA256 != old.ManifestHash || manifestRevision(m.manifest) != old.ManifestRevision {
		code, message = "ORPHAN_IDENTITY_UNSAFE", "managed process identity or manifest could not be verified"
	}
	if code == "" {
		return nil
	}

	// The old fence was probed before this validation. Only that exact owned
	// identity can be signaled. Unsafe probe results remain untouched.
	if oldProbe == ProcessOwned && !m.reapRestartGroup(ctx, oldFenceIdentity(old), nil) {
		code, message = "ORPHAN_IDENTITY_UNSAFE", "managed orphan process group could not be reaped"
	}

	action, err := m.store.ManagedRootRecoveryAction(ctx, agent.AgentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && action.Stage != "ready" && action.Stage != "failed" {
		if action.TargetProcessPID > 0 {
			target := ProcessIdentity{PID: action.TargetProcessPID, StartMarker: action.TargetProcessStartMarker, Incarnation: action.TargetProcessIncarnation}
			if target != oldFenceIdentity(old) && m.processOps.Probe(target).State == ProcessOwned && !m.reapRestartGroup(ctx, target, nil) {
				code, message = "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped"
			}
		}
		if err := m.failColdRecoveryAction(ctx, action, code, message); err != nil {
			return err
		}
	}
	return m.failColdRecovery(agent.AgentID, code, message)
}

func (m *Manager) hasColdRecoveryTarget(ctx context.Context, agent *store.ManagedAgentRecord) bool {
	action, err := m.store.ManagedRootRecoveryAction(ctx, agent.AgentID)
	return err == nil && action.Stage == "starting" && rootRecoveryActionTargetsFence(action, storedAgentFence(agent))
}

func (m *Manager) failColdRecoveryTarget(ctx context.Context, action *store.ManagedRootRecoveryAction, target store.ManagedRuntimeFence) error {
	identity := oldFenceIdentity(target)
	if m.processOps.Probe(identity).State == ProcessOwned && !m.reapRestartGroup(ctx, identity, nil) {
		return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
	}
	return m.failColdRecoveryAction(ctx, action, "RECOVERY_TARGET_UNKNOWN", "replacement process was already reserved by an earlier recovery attempt")
}

func (m *Manager) recoverManagedRootInLane(ctx context.Context, agent *store.ManagedAgentRecord, recipe store.ManagedLaunchRecipe, old store.ManagedRuntimeFence, action *store.ManagedRootRecoveryAction) error {
	wasWaitingForCrashRetry := false
	if action == nil {
		return nil
	}
	if action.Stage == "claimed" {
		if err := m.store.AdvanceManagedRootRecovery(ctx, action.ActionID, "claimed", "reaping"); err != nil {
			return err
		}
		action.Stage = "reaping"
	}
	if action.Stage == "reaping" {
		probe := m.processOps.Probe(oldFenceIdentity(old)).State
		switch probe {
		case ProcessAbsent:
			if err := m.store.RecordManagedRootRecoveryUnexpectedExit(ctx, action.ActionID); err != nil {
				return err
			}
		case ProcessOwned:
			if !m.reapRestartGroup(ctx, oldFenceIdentity(old), nil) {
				return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "managed orphan process group could not be reaped")
			}
		default:
			// Present, mismatch, and unknown all fail closed. In particular, no
			// signal is sent after the PID/start-marker/group proof is lost.
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "managed orphan process identity could not be verified")
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
		// An already-stopped row is a persisted retry from an earlier router
		// owner; a live/idle row is the first crash observation for this recovery
		// action. Planned/failed lifecycle exits already have an owning action and
		// must not be converted into crash-policy history.
		wasWaitingForCrashRetry = agent.ActivityState == "stopped"
		current, err := m.store.ManagedAgent(ctx, agent.AgentID)
		if err != nil {
			return err
		}
		plannedProcessStop := current.LastErrorCode == "PROCESS_STOP_FAILED"
		if current.LifecycleState != "failed" && !plannedProcessStop {
			decision, err := m.store.RecordManagedUnexpectedExit(ctx, old, "", "managed Pi exited during router recovery")
			if err != nil {
				return err
			}
			if decision.CrashLoop {
				return m.failColdRecoveryAction(ctx, action, "CRASH_LOOP", "managed agent entered crash loop")
			}
		}
		m.interruptBuiltinToolInvocations(nil, old)
		if err := m.store.AdvanceManagedRootRecovery(ctx, action.ActionID, "reaping", "stopped"); err != nil {
			return err
		}
		action.Stage = "stopped"
	}
	if action.Stage == "starting" {
		// A previous owner durably reserved or installed the target. Pipes are
		// not reconnectable, so this router never starts a second target.
		if action.TargetProcessPID > 0 {
			identity := ProcessIdentity{PID: action.TargetProcessPID, StartMarker: action.TargetProcessStartMarker, Incarnation: action.TargetProcessIncarnation}
			if m.processOps.Probe(identity).State == ProcessOwned && !m.reapRestartGroup(ctx, identity, nil) {
				return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
			}
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_TARGET_UNKNOWN", "replacement process was already reserved by an earlier recovery attempt")
	}
	if action.Stage != "stopped" {
		return nil
	}
	// The persisted backoff is authoritative after an unclean router restart;
	// recheck it after the old process has been classified so a new crash gets
	// the same delay as a live watcher. A first observation from an idle row is
	// handled by this existing cold-recovery action and is not a duplicate.
	if wasWaitingForCrashRetry {
		due, err := m.store.ManagedCrashRecoveryDue(ctx, agent.AgentID)
		if err != nil || !due {
			return err
		}
	}
	if err := m.store.BeginManagedRootRecoveryTarget(ctx, action.ActionID, action.TargetRuntimeGeneration); err != nil {
		var stale *store.ManagedRootRecoveryTargetStaleError
		if errors.As(err, &stale) {
			return nil
		}
		return err
	}
	process, err := m.processFactory.Start(m.runCtx, CommandSpec{PiExecutable: recipe.PiExecutable, SessionRoot: recipe.SessionRoot, SessionID: old.PiSessionID, LaunchMode: ResumeExisting, Cwd: recipe.Cwd, Provider: recipe.Provider, ModelID: recipe.ModelID, MechaRPCExtension: recipe.MechaRPCExtension, PiSubagentsExtension: recipe.PiSubagentsExtension, CursorExtension: recipe.CursorExtension, TestOnlyExtensions: recipe.TestOnlyExtensions, Environment: recoveryEnvironment(recipe.Environment, m.config.RouterURL, action.TargetRuntimeGeneration, old.SessionEpoch)})
	if err != nil {
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_TARGET_START_FAILED", "replacement process did not start")
	}
	if process == nil {
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_TARGET_START_FAILED", "replacement process did not start")
	}
	identity := process.Identity()
	if ValidateIdentity(identity) != nil || m.processOps.Probe(identity).State != ProcessOwned || identity.Incarnation == old.ProcessIncarnation {
		// A returned process handle is cleaned up only after its exact group is
		// proved owned. Unknown or mismatched identities are never signaled.
		_ = m.cleanupUninstalledTarget(process, identity, nil)
		return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process identity could not be verified")
	}
	if err := m.store.InstallManagedRootRecoveryTarget(ctx, action.ActionID, store.ManagedProcessIdentity{PID: identity.PID, StartMarker: identity.StartMarker, Incarnation: identity.Incarnation}, action.TargetRuntimeGeneration); err != nil {
		if !m.cleanupUninstalledTarget(process, identity, nil) {
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_TARGET_START_FAILED", "replacement process target could not be recorded")
	}
	targetFence := old
	targetFence.ProcessPID, targetFence.ProcessStartMarker, targetFence.ProcessIncarnation = identity.PID, identity.StartMarker, identity.Incarnation
	targetFence.RuntimeGeneration = action.TargetRuntimeGeneration
	if err := m.store.InstallManagedRootRecoveryFence(ctx, old, targetFence); err != nil {
		if !m.cleanupUninstalledTarget(process, identity, nil) {
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_TARGET_START_FAILED", "replacement process fence could not be installed")
	}
	target := &managedBinding{agentID: agent.AgentID, lane: m.laneFor(agent.AgentID), operationID: action.ActionID, lifecycleAction: "root-recovery", token: targetFence.RuntimeGeneration, process: process, identity: identity, sessionID: targetFence.PiSessionID, sessionEpoch: targetFence.SessionEpoch, runtimeGeneration: targetFence.RuntimeGeneration, manifestRevision: targetFence.ManifestRevision, manifestHash: targetFence.ManifestHash}
	m.watchRestartProcess(target)
	targetRPC, err := m.rpcFactory.Open(m.runCtx, process)
	if err != nil {
		if !m.cleanupRecoveredTarget(target, targetFence) {
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_READINESS_FAILED", "replacement RPC could not open")
	}
	target.rpc = targetRPC
	if err := m.readinessGate(m.runCtx, target, targetFence); err != nil {
		_ = closeRPCAndWait(targetRPC)
		cleanupOK := m.cleanupRecoveredTarget(target, targetFence)
		if !cleanupOK {
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_READINESS_FAILED", "replacement readiness gate failed")
	}
	// Legacy rows can retain a valid recovery recipe without the private launch
	// operation that owns the durable binding. Check that owner before installing
	// the binding or publishing the running lifecycle state.
	authoritativeAgent, err := m.store.ManagedAgent(ctx, agent.AgentID)
	if err != nil {
		_ = closeRPCAndWait(targetRPC)
		cleanupOK := m.cleanupRecoveredTarget(target, targetFence)
		if !cleanupOK {
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_READINESS_FAILED", "replacement launch owner could not be read")
	}
	if authoritativeAgent.LaunchOperationID == "" {
		_ = closeRPCAndWait(targetRPC)
		cleanupOK := m.cleanupRecoveredTarget(target, targetFence)
		if !cleanupOK {
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_LAUNCH_OWNER_MISSING", "managed launch operation owner is unavailable")
	}
	if !m.installBinding(target) {
		_ = closeRPCAndWait(targetRPC)
		cleanupOK := m.cleanupRecoveredTarget(target, targetFence)
		if !cleanupOK {
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_READINESS_FAILED", "replacement binding could not be installed")
	}
	if err := m.store.CompleteManagedRootRecoveryReady(ctx, agent.AgentID, targetFence); err != nil {
		_ = closeRPCAndWait(targetRPC)
		cleanupOK := m.cleanupRecoveredTarget(target, targetFence)
		m.clearBinding(target)
		if !cleanupOK {
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "replacement process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_READINESS_FAILED", "replacement readiness could not be committed")
	}
	// Recovery temporarily uses its action ID as the binding owner so all work
	// stays fenced until the ready transaction commits. Restore the durable
	// launch owner before clearing that lifecycle hold; a stale callback must not
	// overwrite a newer binding or lifecycle action.
	restored, err := m.restoreRecoveredBindingOwner(ctx, target, action.ActionID, targetFence)
	if err != nil {
		return err
	}
	if !restored {
		return nil
	}
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

// restoreRecoveredBindingOwner applies the post-ready ownership handoff only
// while the exact recovery binding still owns the durable fence and process.
// The manager map is checked while the binding lock is held so a stale callback
// cannot update its operation ID after a newer owner has taken over.
func (m *Manager) restoreRecoveredBindingOwner(ctx context.Context, target *managedBinding, actionID string, targetFence store.ManagedRuntimeFence) (bool, error) {
	authoritativeAgent, err := m.store.ManagedAgent(ctx, target.agentID)
	if err != nil {
		return false, err
	}
	target.serial.Lock()
	defer target.serial.Unlock()
	valid := authoritativeAgent.LaunchOperationID != "" && target.operationID == actionID && target.lifecycleAction == "root-recovery" && !target.reaped && sameBindingFence(bindingFence(target.agentID, target), targetFence) && sameBindingFence(storedAgentFence(authoritativeAgent), targetFence) && m.liveBinding(authoritativeAgent, target)
	if !valid {
		return false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bindings[target.agentID] != target || m.closed {
		return false, nil
	}
	target.operationID = authoritativeAgent.LaunchOperationID
	target.lifecycleAction = ""
	m.operationIDs[target.agentID] = authoritativeAgent.LaunchOperationID
	return true, nil
}

func recoveryEnvironment(stable []string, routerURL, generation string, epoch int64) []string {
	environment := append([]string(nil), stable...)
	environment = append(environment, "MECHA_ROUTER_URL="+routerURL, "MECHA_RPC_RUNTIME_GENERATION="+generation, "MECHA_RPC_SESSION_EPOCH="+strconv.FormatInt(epoch, 10))
	return environment
}

func (m *Manager) cleanupRecoveredTarget(target *managedBinding, fence store.ManagedRuntimeFence) bool {
	if target == nil || target.process == nil {
		return false
	}
	return m.cleanupUninstalledTarget(target.process, fenceToIdentity(fence), target.waitDone)
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

func (m *Manager) failColdRecoveryAction(ctx context.Context, action *store.ManagedRootRecoveryAction, code, message string) error {
	if action == nil {
		return m.failColdRecovery("", code, message)
	}
	if err := m.store.FailManagedRootRecovery(ctx, action.ActionID, code, message); err != nil {
		return err
	}
	return m.failColdRecovery(action.AgentID, code, message)
}

func (m *Manager) failColdRecovery(agentID, code, message string) error {
	if agentID != "" {
		if err := m.store.MarkManagedFailed(context.Background(), agentID, code, message); err != nil {
			return err
		}
	}
	m.publishProjection()
	return nil
}
