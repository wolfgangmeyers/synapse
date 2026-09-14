package managed

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type childDeltaReceiverRPC struct {
	*goalProjectionRPC
	handler func(map[string]any)
}

func (r *childDeltaReceiverRPC) SetChildDeltaEventHandler(handler func(map[string]any)) {
	r.handler = handler
}

func managedChildDelta(sequence int64) map[string]any {
	changes := map[string]any{"lifecycle_state": "running"}
	if sequence == 1 {
		changes = map[string]any{
			"description": "Child", "model": nil, "lifecycle_state": "running", "started_at": "2026-01-01T00:00:00Z", "finished_at": nil, "duration_ms": nil,
			"compaction": map[string]any{"state": "idle", "count": 0, "started_at": nil, "last_outcome": nil},
		}
	}
	if sequence == 2 {
		changes = map[string]any{"lifecycle_state": "completed", "finished_at": "2026-01-01T00:00:01Z", "duration_ms": 1000}
	}
	return map[string]any{
		"protocol": "mecha.children/v1", "owner_session_id": "goal-session", "sequence": sequence,
		"task_id": "task", "run_id": "run", "parent_entry_id": "entry", "changes": changes,
	}
}

func TestChildDeltaReceiverAppliesArrivalOrderFromOneBindingFIFO(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &childDeltaReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installChildDeltaReceiver(binding)
	require.NotNil(t, bridge.handler)

	binding.lane.serial.Lock()
	bridge.handler(managedChildDelta(1))
	bridge.handler(managedChildDelta(2))
	binding.lane.serial.Unlock()

	require.Eventually(t, func() bool {
		source, err := durable.ManagedReadinessSource(ctx, fence.AgentID)
		return err == nil && source.Snapshot != nil && source.Snapshot.Sequence == 2
	}, time.Second, time.Millisecond)
}

func TestChildDeltaReceiverBoundsPendingFIFOAndUsesOneDrainWorker(t *testing.T) {
	manager, _, binding, _, rpc := readyGoalProjectionFixture(t)
	bridge := &childDeltaReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installChildDeltaReceiver(binding)
	require.NotNil(t, bridge.handler)

	before := runtime.NumGoroutine()
	binding.lane.serial.Lock()
	for sequence := int64(1); sequence <= int64(maxQueuedManagedChildDeltas+32); sequence++ {
		bridge.handler(managedChildDelta(sequence))
	}
	require.Eventually(t, func() bool {
		binding.childDeltaSerial.Lock()
		defer binding.childDeltaSerial.Unlock()
		return binding.childDeltaDraining && len(binding.childDeltaQueue) <= maxQueuedManagedChildDeltas
	}, time.Second, time.Millisecond)
	require.LessOrEqual(t, runtime.NumGoroutine(), before+8, "one blocked binding must not create a goroutine per child arrival")
	binding.lane.serial.Unlock()
	// The bound and draining state prove that the worker was admitted before
	// releasing the lane. Wait for that worker while the manager is still live;
	// the separate FIFO test checks durable arrival order.
	manager.startWG.Wait()
	binding.childDeltaSerial.Lock()
	require.False(t, binding.childDeltaDraining)
	require.Empty(t, binding.childDeltaQueue)
	binding.childDeltaSerial.Unlock()
}
