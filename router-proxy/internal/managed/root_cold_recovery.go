package managed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

		TestOnlyExtensions: append([]string(nil), m.config.TestOnlyExtensions...),
		Environment:        stable,
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
	if err := m.RecoverManagedRootRecoveryOperators(ctx); err != nil {
		return err
	}
	agents, err := m.store.ManagedAgents(ctx)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if agent == nil || agent.Runtime == "embedded" {
			// Embedded sessions recover through their saved role and owner lease,
			// never through the RPC process cold-recovery path.
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
		// A live binding identifies the replacement owned by this router. Only
		// that binding may retry the notice here; an unbound ready action belongs
		// to an older runtime and must be superseded before a new notice is made.
		if binding, bound := m.bindingFor(agent.AgentID); bound {
			wakeup, wakeupErr := m.store.QueueManagedRootRecoveryWakeupForAgent(ctx, agent.AgentID)
			if wakeupErr != nil {
				return wakeupErr
			}
			if wakeup != nil && !managedRootInitialReplayPending(binding) {
				m.OfferDirect(agent.AgentID)
			}
		}
		if _, bound := m.bindingFor(agent.AgentID); bound {
			continue
		}
		marker, markerErr := m.store.ManagedOrderlyStopMarker(ctx, agent.AgentID)
		if markerErr != nil {
			return markerErr
		}
		if marker != nil {
			if !sameBindingFence(marker.ManagedRuntimeFence, storedAgentFence(agent)) {
				continue
			}
			// Confirmed markers take the existing restart path without probing the
			// old process. An unconfirmed marker must settle its exact old fence
			// before any retained replacement target is considered.
			if !marker.Confirmed {
				confirmed, err := m.confirmUnconfirmedOrderlyStop(ctx, marker.ManagedRuntimeFence)
				if err != nil {
					return err
				}
				if !confirmed {
					continue
				}
				marker.Confirmed = true
			}
		}
		cleanupOnly, cleanupErr = m.cleanupFailedColdRecoveryTarget(ctx, agent.AgentID)
		if cleanupErr != nil {
			return cleanupErr
		}
		if cleanupOnly {
			continue
		}
		if marker != nil {
			if err := m.recoverManagedOrderlyRoot(ctx, agent, marker); err != nil {
				return err
			}
			if binding, live := m.bindingFor(agent.AgentID); live {
				if err := m.finishManagedRootRecovery(ctx, binding); err != nil {
					return err
				}
			}
			continue
		}
		crashRecovery, crashErr := m.store.ManagedCrashRecovery(ctx, agent.AgentID)
		if crashErr != nil {
			return crashErr
		}
		// A stopped desired-running row without a crash deadline is a plain
		// persisted stop. It has no recovery owner and must stay stopped.
		if agent.ActivityState == "stopped" && agent.LastErrorCode == "" && crashRecovery.RetryAt == "" {
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
			if err := m.finishManagedRootRecovery(ctx, binding); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecoverBootTimeGoalContinuations scans for agents where goal policy is enabled,
// and if idle with no active subagents and goal is not yet completed, resumes work automatically on router restart.
func (m *Manager) RecoverBootTimeGoalContinuations(ctx context.Context) error {
	agents, err := m.store.ManagedAgents(ctx)
	if err != nil {
		return err
	}
	now := formatManagedTime(m.clock.Now())
	for _, agent := range agents {
		if agent == nil || agent.DesiredState != "running" {
			continue
		}
		if m.hasActiveSubagents(agent.AgentID) {
			continue
		}
		policy, err := m.store.ManagedGoalPolicyFor(ctx, agent.AgentID)
		if err != nil || !isEmbeddedGoalActive(policy, now) {
			continue
		}
		if agent.Runtime != "embedded" {
			if agent.ActivityState == "idle" {
				m.OfferDirect(agent.AgentID)
			}
			continue
		}
		m.MaybeResumeEmbeddedGoal(agent.AgentID)
	}
	return nil
}

// isEmbeddedGoalActive returns true if the policy is enabled, not completed/done, not evaluating, and not snoozed.
func isEmbeddedGoalActive(policy *store.ManagedGoalPolicy, now string) bool {
	if policy == nil || !policy.Config.Enabled {
		return false
	}
	if policy.EvaluatorState == "done" || policy.LastOutcome == store.EmbeddedGoalEvaluationDone || policy.LastOutcome == store.EmbeddedGoalEvaluationCompleted {
		return false
	}
	if policy.EvaluatorState == "evaluating" {
		return false
	}
	if policy.SnoozedUntil != "" && policy.SnoozedUntil > now {
		return false
	}
	return true
}

// MaybeResumeEmbeddedGoal resumes an idle embedded agent's standing goal if enabled and active.
func (m *Manager) MaybeResumeEmbeddedGoal(agentID string) {
	if m.isClosed() || agentID == "" {
		return
	}
	curAgent, err := m.store.ManagedAgent(m.runCtx, agentID)
	if err != nil || curAgent == nil || curAgent.ActivityState != "idle" || curAgent.LifecycleState != "running" || curAgent.DesiredState != "running" {
		return
	}
	if m.hasActiveSubagents(agentID) {
		return
	}
	policy, err := m.store.ManagedGoalPolicyFor(m.runCtx, agentID)
	if err != nil {
		return
	}
	now := formatManagedTime(m.clock.Now())
	pref, err := m.store.CurrentEmbeddedGoalSnoozePreference(m.runCtx, agentID)
	if err != nil {
		return // Missing authority is not permission to dispatch fresh work.
	}
	if pref != nil {
		if pref.SnoozeUntil != "" && pref.SnoozeUntil > now {
			return
		}
	}
	if !isEmbeddedGoalActive(policy, now) {
		return
	}
	m.startManagedTask(func() {
		m.kickoffIdleGoalContinuation(store.EmbeddedBinding{AgentID: agentID})
	})
}

// ResumeIdleEmbeddedGoals scans for idle embedded agents where goal policy is enabled,
// and resumes work if idle with no active subagents and not already done or snoozed.
func (m *Manager) ResumeIdleEmbeddedGoals(ctx context.Context) error {
	agents, err := m.store.ManagedAgents(ctx)
	if err != nil {
		return err
	}
	now := formatManagedTime(m.clock.Now())
	for _, agent := range agents {
		if agent == nil || agent.Runtime != "embedded" || agent.DesiredState != "running" || agent.LifecycleState != "running" || agent.ActivityState != "idle" {
			continue
		}
		if m.hasActiveSubagents(agent.AgentID) {
			continue
		}
		policy, err := m.store.ManagedGoalPolicyFor(ctx, agent.AgentID)
		if err != nil || !isEmbeddedGoalActive(policy, now) {
			continue
		}
		m.MaybeResumeEmbeddedGoal(agent.AgentID)
	}
	return nil
}

const (
	managedInitialReplayTranscriptPoll = 25 * time.Millisecond
	// Stock Pi can emit delayed active after durable assistant persistence, so
	// transcript completion must remain stable before final idle authority.
	managedInitialReplayTranscriptQuiescence = 5 * time.Second
)

func managedRootInitialReplayPending(binding *managedBinding) bool {
	if binding == nil {
		return false
	}
	binding.serial.Lock()
	defer binding.serial.Unlock()
	return binding.initialReplayPending
}

func (m *Manager) managedRootInitialReplayCurrent(target *managedBinding, targetFence store.ManagedRuntimeFence) bool {
	if target == nil {
		return false
	}
	target.serial.Lock()
	defer target.serial.Unlock()
	current, bound := m.bindingFor(target.agentID)
	return bound && current == target && target.initialReplayPending && sameBindingFence(bindingFence(target.agentID, target), targetFence) && m.liveBindingForFence(m.runCtx, target, targetFence)
}

func (m *Manager) completeManagedRootInitialReplay(target *managedBinding, targetFence store.ManagedRuntimeFence, runFollowups bool) error {
	if !m.managedRootInitialReplayCurrent(target, targetFence) {
		return nil
	}
	updated, err := m.store.ReconcileManagedParentIdleFromTranscript(context.Background(), targetFence)
	if err != nil {
		return err
	}
	if updated {
		m.publishProjection()
	}

	target.serial.Lock()
	current, bound := m.bindingFor(target.agentID)
	live := bound && current == target && target.initialReplayPending && sameBindingFence(bindingFence(target.agentID, target), targetFence) && m.liveBindingForFence(m.runCtx, target, targetFence)
	if live {
		target.initialReplayPending = false
	}
	target.serial.Unlock()
	if !live || !runFollowups {
		return nil
	}
	return m.finishManagedRootRecovery(context.Background(), target)
}

func (m *Manager) failManagedRootInitialReplay(action *store.ManagedRootRecoveryAction, old, targetFence store.ManagedRuntimeFence, target *managedBinding, orderlyStop bool, targetRPC RPC) {
	if !m.managedRootInitialReplayCurrent(target, targetFence) || m.runCtx.Err() != nil {
		return
	}
	_ = closeRPCAndWait(targetRPC)
	m.clearBinding(target)
	_ = m.failColdRecoveryReplacement(context.Background(), action, old, targetFence, target, orderlyStop, "RECOVERY_READINESS_FAILED", "replacement initial instruction replay failed")
}

// finishManagedRootRecovery preserves the post-recovery FIFO. None of this
// work may reach Pi while the resumed initial instruction is still being
// accepted, because a goal projection can arm an evaluator for that replay.
func (m *Manager) finishManagedRootRecovery(ctx context.Context, binding *managedBinding) error {
	if binding == nil || managedRootInitialReplayPending(binding) {
		return nil
	}
	m.recoverManagedGoalProjection(binding)
	m.startRootControlRecovery(binding)
	if err := m.recoverManagedDirectAttemptsForAgent(ctx, binding.agentID); err != nil {
		return err
	}
	m.OfferDirect(binding.agentID)
	if policy, err := m.store.ManagedGoalPolicyFor(ctx, binding.agentID); err == nil && policy != nil && policy.Config.Enabled {
		if saved, err := m.store.ManagedAgent(ctx, binding.agentID); err == nil && saved != nil && saved.ActivityState == "idle" {
			m.OfferDirect(saved.AgentID)
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
	if action.OperatorStage != "" && action.OperatorStage != "terminal" {
		return true, nil
	}
	if (action.Stage != "starting" && action.Stage != "failed") || action.TargetProcessPID <= 0 {
		return false, nil
	}
	identity := ProcessIdentity{PID: action.TargetProcessPID, StartMarker: action.TargetProcessStartMarker, Incarnation: action.TargetProcessIncarnation}
	if ValidateIdentity(identity) != nil {
		return true, nil
	}
	switch m.processOps.Probe(identity).State {
	case ProcessOwned:
		// A starting action may belong to another router that has already
		// crossed the start boundary. Without an owner lease, this router cannot
		// prove that the exact target is orphaned, so retain it untouched.
		if action.Stage == "starting" {
			return true, nil
		}
		if !m.reapRestartGroup(ctx, identity, nil) {
			return true, nil
		}
	case ProcessAbsent:
	default:
		return true, nil
	}
	current, currentErr := m.store.ManagedAgent(ctx, agentID)
	if currentErr != nil {
		return true, currentErr
	}
	currentFence := storedAgentFence(current)
	oldFence := rootRecoveryActionFence(action)
	marker, markerErr := m.store.ManagedOrderlyStopMarker(ctx, agentID)
	if markerErr != nil {
		return true, markerErr
	}
	if marker != nil && sameBindingFence(marker.ManagedRuntimeFence, oldFence) {
		target := marker.ManagedRuntimeFence
		target.ProcessPID = identity.PID
		target.ProcessStartMarker = identity.StartMarker
		target.ProcessIncarnation = identity.Incarnation
		target.RuntimeGeneration = action.TargetRuntimeGeneration
		// The store transaction checks whether the agent still has the target
		// fence or never installed it. A newer fence leaves this action retained.
		if !sameBindingFence(currentFence, target) && !sameBindingFence(currentFence, oldFence) {
			return true, nil
		}
		code, message := action.FailureCode, action.FailureMessage
		if code == "" {
			code, message = "RECOVERY_READINESS_FAILED", "replacement readiness gate failed"
		}
		if err := m.store.RestoreManagedOrderlyStopAfterVerifiedAbsence(ctx, action.ActionID, oldFence, target, code, message); err != nil {
			return true, err
		}
		return true, nil
	}
	if err := m.store.ClearManagedRootRecoveryTargetAfterVerifiedAbsence(ctx, action.ActionID, store.ManagedProcessIdentity{PID: identity.PID, StartMarker: identity.StartMarker, Incarnation: identity.Incarnation}); err != nil {
		return true, err
	}
	return true, nil
}

func (m *Manager) recoverManagedOrderlyRoot(ctx context.Context, agent *store.ManagedAgentRecord, marker *store.ManagedOrderlyStopMarkerRecord) error {
	if agent == nil || marker == nil || !sameBindingFence(storedAgentFence(agent), marker.ManagedRuntimeFence) {
		return nil
	}
	old := marker.ManagedRuntimeFence
	if !marker.Confirmed {
		confirmed, err := m.confirmUnconfirmedOrderlyStop(ctx, old)
		if err != nil {
			return err
		}
		if !confirmed {
			return nil
		}
	}

	// Inspect the prior action before changing a failed projection. Only these
	// failures have a proven-safe retry boundary; unknown or unsafe identities
	// must retain their failed projection and exact diagnostic.
	prior, priorErr := m.store.ManagedRootRecoveryAction(ctx, agent.AgentID)
	if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return priorErr
	}
	if priorErr == nil && rootRecoveryActionFence(prior) == old {
		switch {
		case prior.Stage == "claimed" || prior.Stage == "reaping" || prior.Stage == "stopped":
			recipe, err := m.managedLaunchRecipe(*agent)
			if err != nil {
				return err
			}
			lane := m.laneFor(agent.AgentID)
			lane.serial.Lock()
			defer lane.serial.Unlock()
			return m.recoverManagedRootInLane(ctx, agent, recipe, old, prior, true)
		case prior.Stage == "starting":
			if prior.TargetProcessPID > 0 {
				// Cleanup may reap a failed target, but a starting target owned by
				// another router has no lease that proves it is orphaned.
				_, cleanupErr := m.cleanupFailedColdRecoveryTarget(ctx, agent.AgentID)
				return cleanupErr
			}
			if err := m.store.FailManagedRootRecovery(ctx, prior.ActionID, "RECOVERY_TARGET_UNKNOWN", "replacement target identity was not recorded"); err != nil {
				return err
			}
			if err := m.store.MarkManagedFailedForFence(ctx, old, "RECOVERY_TARGET_UNKNOWN", "replacement target identity was not recorded"); err != nil {
				var stale *store.ManagedRestartStaleError
				if !errors.As(err, &stale) {
					return err
				}
			}
			m.publishProjection()
			return nil
		case prior.Stage == "failed" && prior.TargetProcessPID == 0:
			if !orderlyStopRecoveryFailureRetryable(prior.FailureCode) {
				return nil
			}
			if err := m.store.PrepareManagedOrderlyStopRetry(ctx, old); err != nil {
				return err
			}
			targetGeneration, err := m.ids.Next()
			if err != nil {
				return err
			}
			action, err := m.store.RetryManagedOrderlyStop(ctx, old, targetGeneration)
			if err != nil {
				var stale *store.ManagedRootRecoveryActionStaleError
				if errors.As(err, &stale) {
					return nil
				}
				return err
			}
			recipe, err := m.managedLaunchRecipe(*agent)
			if err != nil {
				return err
			}
			lane := m.laneFor(agent.AgentID)
			lane.serial.Lock()
			defer lane.serial.Unlock()
			return m.recoverManagedRootInLane(ctx, agent, recipe, old, action, true)
		default:
			return nil
		}
	}

	if err := m.store.PrepareManagedOrderlyStopRetry(ctx, old); err != nil {
		return err
	}
	recipe, err := m.managedLaunchRecipe(*agent)
	if err != nil {
		return err
	}
	targetGeneration, err := m.ids.Next()
	if err != nil {
		return err
	}
	action, _, err := m.store.ClaimManagedRootRecovery(ctx, agent.AgentID, old, recipe.SHA256, targetGeneration)
	if err != nil {
		return err
	}
	lane := m.laneFor(agent.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	return m.recoverManagedRootInLane(ctx, agent, recipe, old, action, true)
}

func (m *Manager) confirmUnconfirmedOrderlyStop(ctx context.Context, fence store.ManagedRuntimeFence) (bool, error) {
	identity := oldFenceIdentity(fence)
	switch m.processOps.Probe(identity).State {
	case ProcessAbsent:
	case ProcessOwned:
		if !m.reapRestartGroup(ctx, identity, nil) {
			return false, m.failUnconfirmedOrderlyStop(ctx, fence, "managed orderly stop process group could not be reaped")
		}
	default:
		return false, m.failUnconfirmedOrderlyStop(ctx, fence, "managed orderly stop process identity could not be verified")
	}
	if err := m.store.ConfirmManagedOrderlyStopped(ctx, fence); err != nil {
		var stale *store.ManagedRestartStaleError
		if !errors.As(err, &stale) {
			return false, err
		}
	}
	return true, nil
}

func (m *Manager) failUnconfirmedOrderlyStop(ctx context.Context, fence store.ManagedRuntimeFence, message string) error {
	if err := m.store.MarkManagedFailedForFence(ctx, fence, "ORPHAN_IDENTITY_UNSAFE", message); err != nil {
		var stale *store.ManagedRestartStaleError
		if !errors.As(err, &stale) {
			return err
		}
	}
	m.publishProjection()
	return nil
}

func orderlyStopRecoveryFailureRetryable(code string) bool {
	switch code {
	case "RECOVERY_TARGET_START_FAILED", "RECOVERY_READINESS_FAILED", "RECOVERY_LAUNCH_OWNER_MISSING":
		return true
	default:
		return false
	}
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
	// Preflight persists safe failures without returning an error. Do not
	// claim recovery or start a process after that durable rejection (or after
	// another owner has changed the fence or desired state).
	current, err := m.store.ManagedAgent(ctx, agent.AgentID)
	if err != nil {
		return err
	}
	if current == nil || current.LifecycleState == "failed" || current.DesiredState != "running" || !sameBindingFence(storedAgentFence(current), old) {
		return nil
	}
	recipe := *agent.LaunchRecipe

	action, actionErr := m.store.ManagedRootRecoveryAction(ctx, agent.AgentID)
	if actionErr != nil && !errors.Is(actionErr, sql.ErrNoRows) {
		return actionErr
	}
	if actionErr == nil {
		switch {
		case (action.Stage == "starting" || action.Stage == "failed") && action.TargetProcessPID > 0:
			// Cleanup is a separate startup pass. Until it proves this exact
			// target absent, this action is never eligible for a fresh claim.
			return nil
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
	return m.recoverManagedRootInLane(ctx, agent, recipe, old, action, false)
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
	} else if !m.config.AllowManagedLaunchRecipeMismatch {
		// Integrity was checked against the original persisted recipe above.
		// Ignore only the retired extension when comparing effective launch
		// configuration; retain the original recipe and hash for recovery ownership.
		effective := *agent.LaunchRecipe
		effective.AntigravityExtension = ""
		effective, effectiveErr := store.ManagedLaunchRecipeDigest(effective)
		expected, err := m.managedLaunchRecipe(*agent)
		if effectiveErr != nil || err != nil || store.ValidateManagedLaunchRecipe(expected) != nil || expected.SHA256 != effective.SHA256 {
			code, message = "RECOVERY_RECIPE_MISMATCH", "managed launch recipe does not match current manager configuration"
		}
	}
	if code == "" && (!validRestartFence(old) || m.manifest.CanonicalSHA256 != old.ManifestHash || manifestRevision(m.manifest) != old.ManifestRevision) {
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
				code, message = "ORPHAN_IDENTITY_UNSAFE", "preflight replacement process group could not be reaped"
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
		return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "reserved replacement process group could not be reaped")
	}
	return m.failColdRecoveryAction(ctx, action, "RECOVERY_TARGET_UNKNOWN", "replacement process was already reserved by an earlier recovery attempt")
}

func (m *Manager) failColdRecoveryReplacement(ctx context.Context, action *store.ManagedRootRecoveryAction, old, target store.ManagedRuntimeFence, targetBinding *managedBinding, orderlyStop bool, code, message string) error {
	if targetBinding == nil || !m.cleanupRecoveredTarget(targetBinding, target) {
		return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "readiness replacement cleanup failed after "+message)
	}
	if action == nil || !orderlyStop {
		return m.failColdRecoveryAction(ctx, action, code, message)
	}
	return m.store.RestoreManagedOrderlyStopAfterVerifiedAbsence(ctx, action.ActionID, old, target, code, message)
}

func (m *Manager) recoverManagedRootInLane(ctx context.Context, agent *store.ManagedAgentRecord, recipe store.ManagedLaunchRecipe, old store.ManagedRuntimeFence, action *store.ManagedRootRecoveryAction, orderlyStop bool) error {
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
		if orderlyStop {
			// Manager.Close already stopped this exact runtime after durably
			// recording the marker. Do not probe, signal, or count it as a crash.
			if err := m.store.AdvanceManagedRootRecovery(ctx, action.ActionID, "reaping", "stopped"); err != nil {
				return err
			}
			action.Stage = "stopped"
		} else {
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
	}
	if action.Stage == "starting" {
		// A previous owner durably reserved or installed the target. Pipes are
		// not reconnectable, so this router never starts a second target.
		if action.TargetProcessPID > 0 {
			identity := ProcessIdentity{PID: action.TargetProcessPID, StartMarker: action.TargetProcessStartMarker, Incarnation: action.TargetProcessIncarnation}
			if m.processOps.Probe(identity).State == ProcessOwned && !m.reapRestartGroup(ctx, identity, nil) {
				return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "previous-owner replacement process group could not be reaped")
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
	sessionRelativePath, err := m.store.BoundManagedPiSessionPath(ctx, agent.AgentID, old.SessionEpoch, old.PiSessionID)
	if err != nil {
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_SESSION_BINDING_INVALID", "bound managed session could not be resolved")
	}
	if err := m.store.BeginManagedRootRecoveryTarget(ctx, action.ActionID, action.TargetRuntimeGeneration); err != nil {
		var stale *store.ManagedRootRecoveryTargetStaleError
		if errors.As(err, &stale) {
			return nil
		}
		return err
	}
	spec := CommandSpec{PiExecutable: recipe.PiExecutable, SessionRoot: recipe.SessionRoot, SessionPath: filepath.Join(m.config.SessionRoot, sessionRelativePath), SessionID: old.PiSessionID, LaunchMode: ResumeExisting, Cwd: recipe.Cwd, Provider: recipe.Provider, ModelID: recipe.ModelID, MechaRPCExtension: recipe.MechaRPCExtension, PiSubagentsExtension: recipe.PiSubagentsExtension, CursorExtension: recipe.CursorExtension, TestOnlyExtensions: recipe.TestOnlyExtensions, Environment: recoveryEnvironment(recipe.Environment, m.config.TestOnlyEnvironment, m.config.RouterURL, action.TargetRuntimeGeneration, old.SessionEpoch)}
	process, err := m.processFactory.Start(m.runCtx, spec)
	if err != nil {
		code := "RECOVERY_TARGET_UNKNOWN"
		if StartErrorProvesNoChild(err) {
			code = "RECOVERY_TARGET_START_FAILED"
		}
		return m.failColdRecoveryAction(ctx, action, code, "replacement process did not start")
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
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "unrecorded replacement process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_TARGET_START_FAILED", "replacement process target could not be recorded")
	}
	action.TargetProcessPID = identity.PID
	action.TargetProcessStartMarker = identity.StartMarker
	action.TargetProcessIncarnation = identity.Incarnation
	targetFence := old
	targetFence.ProcessPID, targetFence.ProcessStartMarker, targetFence.ProcessIncarnation = identity.PID, identity.StartMarker, identity.Incarnation
	targetFence.RuntimeGeneration = action.TargetRuntimeGeneration
	if err := m.store.InstallManagedRootRecoveryFence(ctx, old, targetFence); err != nil {
		if !m.cleanupUninstalledTarget(process, identity, nil) {
			return m.failColdRecoveryAction(ctx, action, "ORPHAN_IDENTITY_UNSAFE", "uninstalled replacement fence process group could not be reaped")
		}
		return m.failColdRecoveryAction(ctx, action, "RECOVERY_TARGET_START_FAILED", "replacement process fence could not be installed")
	}
	target := &managedBinding{agentID: agent.AgentID, lane: m.laneFor(agent.AgentID), operationID: action.ActionID, lifecycleAction: "root-recovery", token: targetFence.RuntimeGeneration, process: process, identity: identity, sessionID: targetFence.PiSessionID, sessionEpoch: targetFence.SessionEpoch, runtimeGeneration: targetFence.RuntimeGeneration, manifestRevision: targetFence.ManifestRevision, manifestHash: targetFence.ManifestHash, sessionRelativePath: sessionRelativePath, sessionPath: spec.SessionPath}
	m.watchRestartProcess(target)
	targetRPC, err := m.rpcFactory.Open(m.runCtx, process)
	if err != nil {
		return m.failColdRecoveryReplacement(ctx, action, old, targetFence, target, orderlyStop, "RECOVERY_READINESS_FAILED", "replacement RPC could not open")
	}
	target.rpc = targetRPC
	if err := m.readinessGate(m.runCtx, target, targetFence); err != nil {
		// The watcher owns the only wait. Use the same nonblocking capture as an
		// ordinary launch so a concurrently published exit retains its code and
		// stderr without delaying readiness cleanup for a live target.
		diagnostic := readinessFailureDiagnostic(fmt.Errorf("replacement readiness gate failed: %w", err), spec)
		if waitErr, exited := captureReadinessExit(target); exited {
			diagnostic = readinessFailureDiagnostic(errors.New(ManagedProcessExitDiagnostic(waitErr)), spec)
		}
		_ = closeRPCAndWait(targetRPC)
		return m.failColdRecoveryReplacement(ctx, action, old, targetFence, target, orderlyStop, "RECOVERY_READINESS_FAILED", diagnostic)
	}
	// Legacy rows can retain a valid recovery recipe without the private launch
	// operation that owns the durable binding. Check that owner before installing
	// the binding or publishing the running lifecycle state.
	authoritativeAgent, err := m.store.ManagedAgent(ctx, agent.AgentID)
	if err != nil {
		_ = closeRPCAndWait(targetRPC)
		return m.failColdRecoveryReplacement(ctx, action, old, targetFence, target, orderlyStop, "RECOVERY_READINESS_FAILED", "replacement launch owner could not be read")
	}
	if authoritativeAgent.LaunchOperationID == "" {
		_ = closeRPCAndWait(targetRPC)
		return m.failColdRecoveryReplacement(ctx, action, old, targetFence, target, orderlyStop, "RECOVERY_LAUNCH_OWNER_MISSING", "managed launch operation owner is unavailable")
	}
	replayInitialInstruction := ""
	shouldPromptInitialInstruction := false
	if authoritativeAgent.PendingInitialInstruction != "" {
		replayState, scanErr := stockPiTranscriptReplayState(spec.SessionPath, authoritativeAgent.PendingInitialInstruction)
		if scanErr != nil {
			_ = closeRPCAndWait(targetRPC)
			return m.failColdRecoveryReplacement(ctx, action, old, targetFence, target, orderlyStop, "RECOVERY_READINESS_FAILED", "replacement transcript reconciliation failed")
		}
		switch replayState {
		case stockPiReplayAbsent:
			replayInitialInstruction = authoritativeAgent.PendingInitialInstruction
			shouldPromptInitialInstruction = true
		case stockPiReplayPending:
			replayInitialInstruction = authoritativeAgent.PendingInitialInstruction
		}
	}
	target.initialReplayPending = replayInitialInstruction != ""
	if !m.installBinding(target) {
		_ = closeRPCAndWait(targetRPC)
		return m.failColdRecoveryReplacement(ctx, action, old, targetFence, target, orderlyStop, "RECOVERY_READINESS_FAILED", "replacement binding could not be installed")
	}
	if err := m.store.CompleteManagedRootRecoveryReady(ctx, agent.AgentID, targetFence); err != nil {
		_ = closeRPCAndWait(targetRPC)
		m.clearBinding(target)
		return m.failColdRecoveryReplacement(ctx, action, old, targetFence, target, orderlyStop, "RECOVERY_READINESS_FAILED", "replacement readiness could not be committed")
	}
	// The replacement is already the authoritative running binding. If the
	// notice transaction fails, keep that binding and let the next sweep retry;
	// reaping it would leave desired-running with no recoverable owner.
	var wakeupErr error
	if replayInitialInstruction == "" {
		_, wakeupErr = m.store.QueueManagedRootRecoveryWakeup(ctx, action.ActionID)
	} else {
		wakeupErr = m.store.SuppressManagedRootRecoveryWakeup(ctx, action.ActionID)
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
		return wakeupErr
	}
	m.installRootControlReceiver(target)
	m.installChildDeltaReceiver(target)
	m.installParentCompactionReceiver(target)
	m.installParentCompactionNotificationReceiver(target)
	m.installParentActivityReceiver(target)
	m.installQualifiedGoalActivityReceiver(target)
	m.installBuiltinToolLifecycleReceiver(target)
	m.startRecoveredRootWatcher(target, targetFence)
	// Match fresh launch ordering: the replacement must be the committed,
	// receiver-equipped authoritative binding before a replay can produce
	// activity or agent_end events. Replay stays pending until the exact binding
	// and fence have a completed transcript turn.
	if replayInitialInstruction != "" {
		replayDeadline, hasReplayDeadline := ctx.Deadline()
		m.startManagedTask(func() {
			if !m.managedRootInitialReplayCurrent(target, targetFence) {
				return
			}
			replayCtx := m.runCtx
			cancelReplay := func() {}
			if hasReplayDeadline {
				replayCtx, cancelReplay = context.WithDeadline(m.runCtx, replayDeadline)
			}
			defer cancelReplay()

			var promptResult <-chan error
			if shouldPromptInitialInstruction {
				result := make(chan error, 1)
				if !m.startManagedTask(func() {
					result <- targetRPC.Prompt(replayCtx, replayInitialInstruction)
				}) {
					return
				}
				promptResult = result
			}
			ticker := time.NewTicker(managedInitialReplayTranscriptPoll)
			defer ticker.Stop()
			var completionObservedAt time.Time
			for {
				select {
				case err := <-promptResult:
					if err != nil {
						m.failManagedRootInitialReplay(action, old, targetFence, target, orderlyStop, targetRPC)
						return
					}
					promptResult = nil
				case <-ticker.C:
					status, scanErr := stockPiTranscriptReplayState(target.sessionPath, replayInitialInstruction)
					if scanErr != nil {
						m.failManagedRootInitialReplay(action, old, targetFence, target, orderlyStop, targetRPC)
						return
					}
					if status != stockPiReplayComplete {
						completionObservedAt = time.Time{}
						continue
					}
					if completionObservedAt.IsZero() {
						completionObservedAt = time.Now()
						continue
					}
					if time.Since(completionObservedAt) < managedInitialReplayTranscriptQuiescence {
						continue
					}
					if err := m.completeManagedRootInitialReplay(target, targetFence, false); err != nil {
						if errors.Is(err, store.ErrManagedParentActivitySourcePending) {
							continue
						}
						m.failManagedRootInitialReplay(action, old, targetFence, target, orderlyStop, targetRPC)
						return
					}
					return
				case <-replayCtx.Done():
					m.failManagedRootInitialReplay(action, old, targetFence, target, orderlyStop, targetRPC)
					return
				}
			}
		})
		return wakeupErr
	}
	return wakeupErr
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

func recoveryEnvironment(stable, testOnly []string, routerURL, generation string, epoch int64) []string {
	environment := make([]string, 0, len(stable)+len(testOnly)+3)
	seen := make(map[string]struct{}, cap(environment))
	appendUnique := func(values ...string) {
		for _, value := range values {
			key, _, _ := strings.Cut(value, "=")
			if key == "MECHA_ANTIGRAVITY_EXTENSION" || key == "ROUTER_MANAGED_ANTIGRAVITY_EXTENSION" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			environment = append(environment, value)
		}
	}
	appendUnique(stable...)
	appendUnique("MECHA_ROUTER_URL="+routerURL, "MECHA_RPC_RUNTIME_GENERATION="+generation, "MECHA_RPC_SESSION_EPOCH="+strconv.FormatInt(epoch, 10))
	appendUnique(testOnly...)
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
	fence := rootRecoveryActionFence(action)
	if action.TargetProcessPID > 0 {
		fence.ProcessPID = action.TargetProcessPID
		fence.ProcessStartMarker = action.TargetProcessStartMarker
		fence.ProcessIncarnation = action.TargetProcessIncarnation
		fence.RuntimeGeneration = action.TargetRuntimeGeneration
	}
	if err := m.store.MarkManagedFailedForFence(ctx, fence, code, message); err != nil {
		var stale *store.ManagedRestartStaleError
		if !errors.As(err, &stale) {
			return err
		}
		if action.TargetProcessPID > 0 {
			// Target installation can fail after the action records the child but
			// before the agent fence changes. Retry the exact old fence, never an
			// agent-id-only write. If that CAS also loses, a newer owner wins.
			oldFence := rootRecoveryActionFence(action)
			if oldErr := m.store.MarkManagedFailedForFence(ctx, oldFence, code, message); oldErr == nil {
				m.publishProjection()
				return nil
			} else if !errors.As(oldErr, &stale) {
				return oldErr
			}
		}
		// A newer owner replaced this fence. Its projection is authoritative;
		// never overwrite it through the agent-id-only failure path.
		return nil
	}
	m.publishProjection()
	return nil
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
