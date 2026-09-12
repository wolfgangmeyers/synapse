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

func (m *Manager) installParentCompactionNotificationReceiver(binding *managedBinding) {
	rpc, ok := binding.rpc.(ParentCompactionNotificationRPC)
	if !ok {
		return
	}
	rpc.SetParentCompactionNotificationHandler(func(event map[string]any) {
		m.handleParentCompactionNotification(binding, event)
	})
}

func (m *Manager) handleParentCompactionEvent(binding *managedBinding, event map[string]any) {
	if binding == nil {
		return
	}
	// The RPC reader invokes this before it reads later stdout frames or EOF.
	// That ordering preserves a real start before a lost process is reconciled.
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
}

func (m *Manager) handleParentCompactionNotification(binding *managedBinding, event map[string]any) {
	if binding == nil {
		return
	}
	fence := bindingFence(binding.agentID, binding)
	if !m.liveBindingForFence(m.runCtx, binding, fence) {
		return
	}
	sequence, sequenceOK := readinessInteger(event["sequence"])
	generation, generationOK := event["runtime_generation"].(string)
	epoch, epochOK := readinessInteger(event["session_epoch"])
	manifest, manifestOK := event["manifest_sha256"].(string)
	if !sequenceOK || !generationOK || !epochOK || !manifestOK || generation != fence.RuntimeGeneration || epoch != fence.SessionEpoch || manifest != fence.ManifestHash {
		return
	}
	entry, created, err := m.store.RecordManagedParentCompactionNotification(context.Background(), fence, sequence)
	if err == nil && created {
		m.publishProjection()
		m.publishCompleteHistory(entry)
	}
}
