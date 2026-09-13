package managed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	managedGoalProjectionRetryDelay  = time.Second
	managedGoalProjectionMaxAttempts = 8
)

// executeManagedGoalControl is the one semantic owner for both a dynamic Pi
// goal tool request and a direct UI goal request. The durable desired revision
// commits before Pi is asked to project it, so a disconnect cannot lose a
// saved goal or make an old receipt authoritative.
func (m *Manager) executeManagedGoalControl(ctx context.Context, binding *managedBinding, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	arguments, ok := invocation.Arguments.(map[string]any)
	if !ok {
		return RootControlHandlerResult{}, errors.New("managed goal arguments are invalid")
	}
	action, _ := arguments["action"].(string)
	if action == "status" {
		return m.managedGoalStatus(ctx, invocation.Fence.AgentID)
	}
	if action != "set" && action != "off" {
		return RootControlHandlerResult{}, errors.New("managed goal action is invalid")
	}
	current, err := m.store.ManagedGoalPolicyFor(ctx, invocation.Fence.AgentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RootControlHandlerResult{}, err
	}
	settings := store.DefaultManagedGoalSettings()
	if current != nil {
		settings = current.Config.Settings
	}
	if raw, present := arguments["settings"]; present {
		var valid bool
		settings, valid = managedGoalSettingsFromArguments(raw)
		if !valid {
			return RootControlHandlerResult{}, errors.New("managed goal settings are invalid")
		}
	}
	config := store.ManagedGoalConfig{Enabled: action == "set", Settings: settings}
	if config.Enabled {
		config.Goal, _ = arguments["goal"].(string)
	}
	policy, err := m.store.UpdateManagedGoalPolicy(ctx, invocation.Fence, config)
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	// The saved desired revision is the durable result. Projection is separate:
	// callers see its pending state while the current session retries it.
	if err := m.projectManagedGoalPolicy(ctx, binding, invocation.Fence, policy, invocation.RequestID); err != nil {
		m.recordManagedGoalProjectionFailure(invocation.Fence, policy.DesiredRevision, err)
	}
	return m.managedGoalStatus(ctx, invocation.Fence.AgentID)
}

func managedGoalSettingsFromArguments(value any) (store.ManagedGoalSettings, bool) {
	raw, ok := value.(map[string]any)
	if !ok || len(raw) != 7 {
		return store.ManagedGoalSettings{}, false
	}
	asBool := func(name string) (bool, bool) { result, found := raw[name].(bool); return result, found }
	asPositive := func(name string) (int64, bool) {
		result, found := raw[name].(int64)
		return result, found && result > 0
	}
	asOptional := func(name string) (*int64, bool) {
		if raw[name] == nil {
			return nil, true
		}
		result, found := raw[name].(int64)
		if !found || result < 1 {
			return nil, false
		}
		return &result, true
	}
	goalLock, lockOK := asBool("goal_lock_enabled")
	snooze, snoozeOK := asBool("snooze_enabled")
	duration, durationOK := asPositive("snooze_duration_ms")
	turnLimit, turnOK := asOptional("snooze_turn_limit")
	budget, budgetOK := asOptional("snooze_budget_cap_ms")
	yield, yieldOK := asPositive("snooze_refill_yield_ms")
	elapsed, elapsedOK := asPositive("snooze_refill_elapsed_ms")
	if !lockOK || !snoozeOK || !durationOK || !turnOK || !budgetOK || !yieldOK || !elapsedOK || (turnLimit != nil && *turnLimit > 20) {
		return store.ManagedGoalSettings{}, false
	}
	return store.ManagedGoalSettings{GoalLockEnabled: goalLock, SnoozeEnabled: snooze, SnoozeDurationMS: duration, SnoozeTurnLimit: turnLimit, SnoozeBudgetCapMS: budget, SnoozeRefillYieldMS: yield, SnoozeRefillElapsedMS: elapsed}, true
}

func (m *Manager) managedGoalStatus(ctx context.Context, agentID string) (RootControlHandlerResult, error) {
	policy, err := m.store.ManagedGoalPolicyFor(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return RootControlHandlerResult{Status: "ok", Content: "Goal is off.", Details: map[string]any{"goal_state": managedGoalStatusDetails(nil)}}, nil
	}
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	content := "Goal is off."
	if policy.Config.Enabled {
		content = "Goal is active."
	}
	return RootControlHandlerResult{Status: "ok", Content: content, Details: map[string]any{"goal_state": managedGoalStatusDetails(policy)}}, nil
}

func managedGoalStatusDetails(policy *store.ManagedGoalPolicy) map[string]any {
	settings := store.DefaultManagedGoalSettings()
	result := map[string]any{"enabled": false, "goal": "", "desired_revision": int64(0), "applied_revision": int64(0), "projection_state": "applied"}
	if policy != nil {
		settings = policy.Config.Settings
		result["enabled"], result["goal"] = policy.Config.Enabled, policy.Config.Goal
		result["desired_revision"], result["applied_revision"], result["projection_state"] = policy.DesiredRevision, policy.AppliedRevision, policy.ProjectionState
	}
	result["goal_lock_enabled"] = settings.GoalLockEnabled
	result["snooze_enabled"] = settings.SnoozeEnabled
	result["snooze_duration_ms"] = settings.SnoozeDurationMS
	result["snooze_turn_limit"] = nullableInt64Value(settings.SnoozeTurnLimit)
	result["snooze_budget_cap_ms"] = nullableInt64Value(settings.SnoozeBudgetCapMS)
	result["snooze_refill_yield_ms"] = settings.SnoozeRefillYieldMS
	result["snooze_refill_elapsed_ms"] = settings.SnoozeRefillElapsedMS
	return result
}

// nullableInt64Value keeps optional goal settings in the JSON-only shape
// required by root-control results. A typed nil pointer in an interface is not
// JSON null and is rejected by the canonical result boundary.
func nullableInt64Value(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func managedGoalProjectionValue(policy *store.ManagedGoalPolicy, controlID string) map[string]any {
	settings := policy.Config.Settings
	goal := map[string]any{
		"action":     map[bool]string{true: "set", false: "off"}[policy.Config.Enabled],
		"control_id": controlID, "target_instance_id": policy.AgentID, "revision": policy.DesiredRevision,
		"settings": map[string]any{"goalLockEnabled": settings.GoalLockEnabled, "snoozeEnabled": settings.SnoozeEnabled, "snoozeDurationMs": settings.SnoozeDurationMS, "snoozeTurnLimit": settings.SnoozeTurnLimit, "snoozeBudgetCapMs": settings.SnoozeBudgetCapMS, "snoozeRefillYieldMs": settings.SnoozeRefillYieldMS, "snoozeRefillElapsedMs": settings.SnoozeRefillElapsedMS},
	}
	if policy.Config.Enabled {
		goal["goal"] = policy.Config.Goal
	}
	return goal
}

func (m *Manager) projectManagedGoalPolicy(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence, policy *store.ManagedGoalPolicy, controlID string) error {
	if policy == nil || policy.DesiredRevision < 1 || !m.rootBindingCurrent(ctx, binding, fence) {
		return errors.New("managed goal projection is stale")
	}
	rpc, ok := binding.rpc.(readinessSourceRPC)
	if !ok {
		return errors.New("managed Pi does not support goal projection")
	}
	desired, err := m.store.LoadManagedDesiredPolicy(ctx, fence.AgentID)
	if err != nil {
		return err
	}
	value, err := readinessPolicyValue(desired)
	if err != nil {
		return err
	}
	value["goal"] = managedGoalProjectionValue(policy, controlID)
	envelope := map[string]any{"protocol": "mecha.policy/v1", "policy": value, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "manifest_sha256": fence.ManifestHash}
	if _, err := rpc.ApplyPolicy(ctx, envelope); err != nil {
		return err
	}
	receipt, err := rpc.WaitForPolicyApplied(ctx, fence.RuntimeGeneration, fence.SessionEpoch)
	if err != nil {
		return err
	}
	applied, ok := receipt["applied"].(map[string]any)
	if !ok || !sameEpoch(applied["goal_revision"], policy.DesiredRevision) {
		return errors.New("managed goal receipt does not match desired revision")
	}
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return errors.New("managed runtime binding changed before goal confirmation")
	}
	confirmed, err := m.store.ConfirmManagedGoalPolicyApplied(context.Background(), fence, policy.DesiredRevision)
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("managed goal receipt is stale")
	}
	m.publishProjection()
	return nil
}

func (m *Manager) recordManagedGoalProjectionFailure(fence store.ManagedRuntimeFence, revision int64, cause error) {
	retryAt := formatManagedTime(m.clock.Now().Add(managedGoalProjectionRetryDelay))
	policy, err := m.store.MarkManagedGoalPolicyProjectionFailure(context.Background(), fence, revision, "GOAL_PROJECTION_FAILED", boundedRootError(cause), retryAt)
	if err != nil || policy == nil || policy.ProjectionAttempts >= managedGoalProjectionMaxAttempts {
		return
	}
	m.mu.Lock()
	binding := m.bindings[fence.AgentID]
	m.mu.Unlock()
	if binding == nil {
		return
	}
	m.startManagedTask(func() {
		timer := time.NewTimer(managedGoalProjectionRetryDelay)
		defer timer.Stop()
		select {
		case <-m.runCtx.Done():
			return
		case <-timer.C:
		}
		binding.serial.Lock()
		defer binding.serial.Unlock()
		if !m.rootBindingCurrent(m.runCtx, binding, fence) {
			return
		}
		pending, pendingErr := m.store.PendingManagedGoalPolicyForFence(context.Background(), fence)
		if pendingErr != nil {
			return
		}
		if retryErr := m.projectManagedGoalPolicy(m.runCtx, binding, fence, pending, fmt.Sprintf("retry-%d", pending.DesiredRevision)); retryErr != nil {
			m.recordManagedGoalProjectionFailure(fence, pending.DesiredRevision, retryErr)
		}
	})
}

func (m *Manager) recoverManagedGoalProjection(binding *managedBinding) {
	if binding == nil {
		return
	}
	m.startManagedTask(func() {
		// Lifecycle replacement and test probes update a binding under serial.
		// Recovery reads the same immutable fence under that lock before spawning
		// any independent waiters.
		binding.serial.Lock()
		fence := bindingFence(binding.agentID, binding)
		current := m.rootBindingCurrent(m.runCtx, binding, fence)
		if current {
			// A router or process replacement cannot inherit an in-flight evaluator.
			// Its old result remains fenced; a later native settled boundary decides
			// whether the current runtime needs a new evaluation.
			if changed, err := m.store.RecoverManagedGoalEvaluationForFence(context.Background(), fence); err == nil && changed {
				m.publishProjection()
			}
		}
		binding.serial.Unlock()
		if !current {
			return
		}
		m.scheduleManagedGoalSnoozeWakeup(binding, fence)
		m.startManagedGoalProjectionRecovery(binding, fence)
	})
}

func (m *Manager) startManagedGoalProjectionRecovery(binding *managedBinding, fence store.ManagedRuntimeFence) {
	m.startManagedTask(func() {
		binding.serial.Lock()
		defer binding.serial.Unlock()
		if !m.rootBindingCurrent(m.runCtx, binding, fence) {
			return
		}
		pending, err := m.store.PendingManagedGoalPolicyForFence(context.Background(), fence)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			return
		}
		if err := m.projectManagedGoalPolicy(m.runCtx, binding, fence, pending, fmt.Sprintf("recovery-%d", pending.DesiredRevision)); err != nil {
			m.recordManagedGoalProjectionFailure(fence, pending.DesiredRevision, err)
		}
	})
}
