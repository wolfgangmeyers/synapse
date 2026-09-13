package managed

import (
	"context"
	"errors"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// managedGoalActivity preserves the local source spelling while the store owns
// its durable full-fence identity and duplicate rejection.
type managedGoalActivity = store.ManagedGoalActivity

func (m *Manager) executeManagedGoalSnooze(ctx context.Context, binding *managedBinding, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	arguments, ok := invocation.Arguments.(map[string]any)
	if !ok || len(arguments) != 0 {
		return RootControlHandlerResult{}, errors.New("managed goal snooze arguments are invalid")
	}
	policy, snoozed, err := m.store.SnoozeManagedGoalPolicy(ctx, invocation.Fence)
	if err != nil {
		return RootControlHandlerResult{}, err
	}
	if !snoozed || policy == nil {
		return RootControlHandlerResult{Status: "error", Content: "Goal snooze is unavailable.", Details: map[string]any{}, ErrorCode: "GOAL_SNOOZE_UNAVAILABLE", ErrorMessage: "goal snooze is unavailable"}, nil
	}
	m.publishProjection()
	m.scheduleManagedGoalSnoozeWakeup(binding, invocation.Fence)
	return RootControlHandlerResult{Status: "ok", Content: "Goal snoozed.", Details: map[string]any{}}, nil
}

// scheduleManagedGoalSnoozeWakeup derives each timer from SQLite. Any number
// of recovery callers may wait, but the due transition is one fenced update.
func (m *Manager) scheduleManagedGoalSnoozeWakeup(binding *managedBinding, fence store.ManagedRuntimeFence) {
	if binding == nil {
		return
	}
	m.startManagedTask(func() {
		for {
			binding.serial.Lock()
			current := m.liveBindingForFence(m.runCtx, binding, fence)
			binding.serial.Unlock()
			if !current {
				return
			}
			policy, err := m.store.ManagedGoalPolicyFor(context.Background(), fence.AgentID)
			if err != nil || policy.EvaluatorState != "snoozed" || policy.SnoozedUntil == "" || !store.ManagedGoalSnoozeFenceMatches(policy, fence) {
				return
			}
			due, err := time.Parse(time.RFC3339Nano, policy.SnoozedUntil)
			if err != nil {
				return
			}
			delay := due.Sub(m.clock.Now())
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-m.runCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			m.wakeSnoozedGoalInLane(binding, fence)
			// A due snooze blocked by real child work or compaction remains durable.
			// Stop here; the next qualifying native transition schedules one retry.
			return
		}
	})
}

func (m *Manager) wakeSnoozedGoalInLane(binding *managedBinding, fence store.ManagedRuntimeFence) {
	if binding == nil {
		return
	}
	binding.serial.Lock()
	defer binding.serial.Unlock()
	if !m.liveBindingForFence(m.runCtx, binding, fence) {
		return
	}
	policy, woke, err := m.store.WakeSnoozedManagedGoalPolicy(context.Background(), fence)
	if err != nil || !woke || policy == nil {
		return
	}
	m.publishProjection()
	if m.liveBindingForFence(m.runCtx, binding, fence) {
		m.OfferDirect(fence.AgentID)
	}
}

// installQualifiedGoalActivityReceiver leaves native builtin completion dormant
// until C4 provides a stable completion identity on the existing Pi telemetry
// stream. It does not infer success from agent_settled or introduce another
// telemetry channel.
func (m *Manager) installQualifiedGoalActivityReceiver(binding *managedBinding) {
	if binding == nil {
		return
	}
	rpc, ok := binding.rpc.(QualifiedGoalActivityRPC)
	if !ok {
		return
	}
	rpc.SetQualifiedGoalActivityEventHandler(func(activity store.ManagedGoalActivity) {
		binding.serial.Lock()
		fence := bindingFence(binding.agentID, binding)
		binding.serial.Unlock()
		m.recordManagedGoalActivity(binding, fence, activity)
	})
}

func (m *Manager) recordManagedGoalActivity(binding *managedBinding, fence store.ManagedRuntimeFence, activity managedGoalActivity) {
	if binding == nil || activity.ID == "" || (activity.Kind != "tool" && activity.Kind != "child" && activity.Kind != "builtin") {
		return
	}
	m.startManagedTask(func() {
		binding.lane.serial.Lock()
		defer binding.lane.serial.Unlock()
		if !m.liveBindingForFence(m.runCtx, binding, fence) {
			return
		}
		policy, rearmed, err := m.store.RecordManagedGoalMeaningfulActivity(context.Background(), fence, activity)
		if err == nil && policy != nil && rearmed {
			m.publishProjection()
		}
	})
}
