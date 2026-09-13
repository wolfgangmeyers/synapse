package managed

import (
	"context"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// installParentActivityReceiver starts after readiness commits, so every Pi
// start/settled event is associated with the same durable binding that owns the
// managed row. It intentionally uses Pi's native lifecycle instead of treating
// an accepted direct prompt as work.
func (m *Manager) installParentActivityReceiver(binding *managedBinding) {
	rpc, ok := binding.rpc.(ParentActivityRPC)
	if !ok {
		return
	}
	rpc.SetParentActivityEventHandler(func(event map[string]any) {
		m.handleParentActivityEvent(binding, event)
	})
}

func (m *Manager) handleParentActivityEvent(binding *managedBinding, event map[string]any) {
	if binding == nil {
		return
	}
	m.startManagedTask(func() {
		binding.lane.serial.Lock()
		defer binding.lane.serial.Unlock()
		fence := bindingFence(binding.agentID, binding)
		if !m.liveBindingForFence(m.runCtx, binding, fence) {
			return
		}
		sequence, ok := readinessInteger(event["sequence"])
		state, stateOK := event["state"].(string)
		if !ok || !stateOK {
			return
		}
		updated, err := m.store.ApplyManagedParentActivity(context.Background(), fence, store.ManagedParentActivityEvent{
			Sequence: sequence,
			State:    state,
		})
		if err == nil && updated {
			m.publishProjection()
			if state == "idle" {
				m.wakeSnoozedGoalInLane(binding, fence)
				m.evaluateManagedGoalAtSettled(binding, fence, sequence)
			}
		}
	})
}
