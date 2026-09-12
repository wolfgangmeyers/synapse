package managed

import (
	"context"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// installParentCompactionReceiver begins only once normal readiness has
// committed. A parent compaction has the same durable binding fence as child
// deltas, but a deliberately separate callback and store transition.
func (m *Manager) installParentCompactionReceiver(binding *managedBinding) {
	rpc, ok := binding.rpc.(ParentCompactionRPC)
	if !ok {
		return
	}
	rpc.SetParentCompactionEventHandler(func(event map[string]any) {
		m.handleParentCompactionEvent(binding, event)
	})
}

func (m *Manager) handleParentCompactionEvent(binding *managedBinding, event map[string]any) {
	if binding == nil {
		return
	}
	m.startManagedTask(func() {
		// The root-control lane can be waiting for Pi's compact RPC to return.
		// Native lifecycle events must not wait behind that caller or the UI sees
		// both cards only after compaction finishes. The fenced store transition
		// already rejects stale or out-of-order source sequences.
		fence := bindingFence(binding.agentID, binding)
		if !m.liveBindingForFence(m.runCtx, binding, fence) {
			return
		}
		sequence, ok := readinessInteger(event["sequence"])
		state, stateOK := event["state"].(string)
		if !ok || !stateOK {
			return
		}
		outcome, _ := event["outcome"].(string)
		transition := store.ManagedParentCompactionEvent{Sequence: sequence, State: state, Outcome: outcome}
		updated, err := m.store.ApplyManagedParentCompaction(context.Background(), fence, transition)
		if err == nil && updated {
			m.publishProjection()
			if entry, historyErr := m.store.ManagedParentCompactionHistoryEntry(context.Background(), fence, transition); historyErr == nil {
				m.publishCompleteHistory(entry)
			}
		}
	})
}
