package managed

import (
	"context"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// SetProjectionPublisher installs the router-owned UI wakeup after transport
// construction. It is a notification only: the browser still reads the durable
// projection, and the manager never imports or controls the HTTP boundary.
func (m *Manager) SetProjectionPublisher(publish func()) {
	m.mu.Lock()
	m.projectionPublisher = publish
	m.mu.Unlock()
}

func (m *Manager) publishProjection() {
	m.mu.Lock()
	publish := m.projectionPublisher
	m.mu.Unlock()
	if publish != nil {
		// The durable delta transaction has already committed. Invoke the
		// notifier in source order so a fast compaction start/end pair cannot be
		// reordered or dropped by independently scheduled publisher goroutines.
		publish()
	}
}

// installChildDeltaReceiver starts only after the binding is ready. The client
// is already tied to one Pi stdout stream; the durable fence and owner checks
// below additionally reject replacement-process and stale lifecycle events.
func (m *Manager) installChildDeltaReceiver(binding *managedBinding) {
	rpc, ok := binding.rpc.(ChildDeltaRPC)
	if !ok {
		return
	}
	rpc.SetChildDeltaEventHandler(func(delta map[string]any) {
		m.handleChildDeltaEvent(binding, delta)
	})
}

func (m *Manager) handleChildDeltaEvent(binding *managedBinding, delta map[string]any) {
	if binding == nil {
		return
	}
	m.startManagedTask(func() {
		binding.lane.serial.Lock()
		defer binding.lane.serial.Unlock()
		ctx := m.runCtx
		fence := bindingFence(binding.agentID, binding)
		normalized, err := normalizeChildDeltaReceipt(delta)
		if err != nil {
			return
		}
		sequence, ok := readinessInteger(normalized["sequence"])
		if !ok {
			return
		}
		changes, ok := normalized["changes"].(map[string]any)
		if !ok || len(changes) == 0 {
			return
		}
		candidate := store.ManagedChildDelta{
			OwnerSessionID: normalized["owner_session_id"].(string),
			TaskID:         normalized["task_id"].(string),
			RunID:          normalized["run_id"].(string),
			ParentEntryID:  normalized["parent_entry_id"].(string),
			Sequence:       sequence,
			Changes:        changes,
		}
		if !m.liveBindingForFence(ctx, binding, fence) {
			_ = m.store.RecordStaleManagedChildDelta(context.Background(), fence, candidate, "parent_fence_replaced")
			return
		}
		if candidate.OwnerSessionID != fence.PiSessionID {
			_ = m.store.RecordStaleManagedChildDelta(context.Background(), fence, candidate, "owner_session_mismatch")
			return
		}
		updated, err := m.store.ApplyManagedChildDelta(context.Background(), fence, candidate)
		if err == nil && updated {
			m.publishProjection()
		}
	})
}
