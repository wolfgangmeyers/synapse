package managed

import (
	"context"
	"errors"

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
		if errors.Is(err, store.ErrManagedChildDeltaGap) || errors.Is(err, store.ErrManagedChildDeltaConflict) {
			updated, err = m.recoverManagedChildSnapshot(context.Background(), binding, fence)
		}
		if err != nil || !updated {
			return
		}
		m.publishProjection()
		if changes["lifecycle_state"] == "running" {
			m.recordManagedGoalActivity(binding, fence, managedGoalActivity{Kind: "child", ID: candidate.TaskID + ":" + candidate.RunID + ":" + candidate.ParentEntryID})
		}
		// The snapshot wakeup keeps the live child disclosure current. Publish
		// this exact durable fact too: a terminal child is no longer live work,
		// so Complete must not depend on a later history poll to show its result.
		if entry, historyErr := m.store.ManagedChildHistoryEntryForDelta(context.Background(), fence, candidate); historyErr == nil {
			m.publishCompleteHistory(entry)
		}
		m.scheduleManagedGoalSnoozeWakeup(binding, fence)
	})
}

// recoverManagedChildSnapshot is the one repair path for a bounded extension
// queue overflow, exhausted append retry, or conflicting same-sequence delta.
// It consumes a fresh package snapshot under the binding fence instead of
// guessing which delta or lifecycle state should win.
func (m *Manager) recoverManagedChildSnapshot(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence) (bool, error) {
	sourceRPC, ok := binding.rpc.(readinessSourceRPC)
	if !ok {
		return false, errors.New("managed Pi does not support child snapshot recovery")
	}
	if !m.liveBindingForFence(ctx, binding, fence) {
		return false, errors.New("managed runtime binding changed before child snapshot recovery")
	}
	envelope := map[string]any{"protocol": "mecha.child-snapshot/v1", "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "manifest_sha256": fence.ManifestHash}
	response, err := sourceRPC.RequestChildSnapshot(ctx, envelope)
	if err != nil {
		return false, err
	}
	var responseSnapshot map[string]any
	var responseCanonical []byte
	if !privateCommandAcknowledgement(response) {
		responseSnapshot, err = normalizeChildSnapshotReceipt(response)
		if err != nil {
			return false, err
		}
		responseCanonical, _, err = HashReadinessCanonicalJSON(responseSnapshot)
		if err != nil {
			return false, err
		}
	}
	receipt, err := sourceRPC.WaitForChildSnapshot(ctx)
	if err != nil {
		return false, err
	}
	snapshot, err := normalizeChildSnapshotReceipt(receipt)
	if err != nil {
		return false, err
	}
	canonical, hash, err := HashReadinessCanonicalJSON(snapshot)
	if err != nil {
		return false, err
	}
	if snapshot["owner_session_id"] != fence.PiSessionID || (responseSnapshot != nil && string(responseCanonical) != string(canonical)) {
		return false, errors.New("child snapshot recovery does not match current runtime")
	}
	sequence, ok := readinessInteger(snapshot["sequence"])
	if !ok || !m.liveBindingForFence(ctx, binding, fence) {
		return false, errors.New("managed runtime binding changed before child snapshot recovery confirmation")
	}
	return m.store.RecoverManagedChildSnapshot(context.Background(), fence, store.ManagedChildSnapshot{SnapshotID: snapshot["snapshot_id"].(string), OwnerSessionID: snapshot["owner_session_id"].(string), Sequence: sequence, Canonical: canonical, SHA256: hash})
}
