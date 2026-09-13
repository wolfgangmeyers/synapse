package managed

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRPCClientBuiltinToolLifecycleIsOrderedAndRedacted(t *testing.T) {
	reader, writer := io.Pipe()
	client := NewRPCClient(io.Discard, reader, func() error { return writer.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	events := make(chan BuiltinToolLifecycleEvent, 2)
	client.SetBuiltinToolLifecycleEventHandler(func(event BuiltinToolLifecycleEvent) { events <- event })

	require.NoError(t, writeRPC(writer, map[string]any{"type": "tool_execution_start", "toolCallId": "call-1", "toolName": "bash", "args": map[string]any{"command": "private command"}}))
	require.NoError(t, writeRPC(writer, map[string]any{"type": "tool_execution_update", "toolCallId": "call-1", "toolName": "bash", "args": map[string]any{"command": "private command"}, "partialResult": map[string]any{"content": "private output"}}))
	require.NoError(t, writeRPC(writer, map[string]any{"type": "tool_execution_end", "toolCallId": "call-1", "toolName": "bash", "result": map[string]any{"content": "private output"}, "isError": false}))

	first := <-events
	second := <-events
	require.Equal(t, BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "call-1", ToolName: "bash", Watermark: first.Watermark}, first)
	require.Equal(t, BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "call-1", ToolName: "bash", Watermark: second.Watermark}, second)
	require.Less(t, first.Watermark, second.Watermark)
	select {
	case event := <-events:
		t.Fatalf("update leaked into lifecycle receiver: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}

	for _, malformed := range []map[string]any{
		{"type": "tool_execution_start", "toolCallId": "call-2", "toolName": "shell"},
		{"type": "tool_execution_end", "toolCallId": "call-2", "toolName": "read"},
		{"type": "tool_execution_end", "toolCallId": 2, "toolName": "read", "isError": true},
		{"type": "custom_message", "toolCallId": "call-2", "toolName": "bash", "isError": false},
	} {
		require.NoError(t, writeRPC(writer, malformed))
	}
	select {
	case event := <-events:
		t.Fatalf("malformed lifecycle event was accepted: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

type builtinLifecycleReceiverRPC struct {
	*goalProjectionRPC
	handler func(BuiltinToolLifecycleEvent)
}

func (r *builtinLifecycleReceiverRPC) SetBuiltinToolLifecycleEventHandler(handler func(BuiltinToolLifecycleEvent)) {
	r.handler = handler
}

type builtinLifecycleTerminalReceiverRPC struct {
	*builtinLifecycleReceiverRPC
	terminalHandler func(error)
}

func (r *builtinLifecycleTerminalReceiverRPC) SetRPCReaderTerminalHandler(handler func(error)) {
	r.terminalHandler = handler
}

func waitForBuiltinToolLifecycleQueue(t *testing.T, binding *managedBinding) {
	t.Helper()
	require.Eventually(t, func() bool {
		binding.builtinToolQueueSerial.Lock()
		defer binding.builtinToolQueueSerial.Unlock()
		return !binding.builtinToolDraining && len(binding.builtinToolQueue) == 0
	}, time.Second, time.Millisecond)
}

func TestBuiltinToolLifecycleCreatesOneEntryAndBuffersEndBeforeStart(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &builtinLifecycleReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installBuiltinToolLifecycleReceiver(binding)
	require.NotNil(t, bridge.handler)

	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "call-1", ToolName: "edit", IsError: true, Watermark: 1})
	waitForBuiltinToolLifecycleQueue(t, binding)
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Empty(t, page.Entries)
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "call-1", ToolName: "edit", Watermark: 2})

	require.Eventually(t, func() bool {
		page, lookupErr := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		return lookupErr == nil && len(page.Entries) == 1 && page.Entries[0].Presentation.Outcome == "failed"
	}, time.Second, time.Millisecond)
	page, err = durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Equal(t, "Edit failed", page.Entries[0].Message)

	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "call-1", ToolName: "edit", IsError: true, Watermark: 3})
	waitForBuiltinToolLifecycleQueue(t, binding)
	page, err = durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1, "duplicate terminal frames must not create a second Complete entry")
}

func TestBuiltinToolLifecycleReaderEOFInterruptsExactFenceAndRejectsLateEnd(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &builtinLifecycleTerminalReceiverRPC{builtinLifecycleReceiverRPC: &builtinLifecycleReceiverRPC{goalProjectionRPC: rpc}}
	binding.rpc = bridge
	manager.installBuiltinToolLifecycleReceiver(binding)
	require.NotNil(t, bridge.handler)
	require.NotNil(t, bridge.terminalHandler)

	binding.builtinToolSerial.Lock()
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "call-1", ToolName: "write", Watermark: 1})
	bridge.terminalHandler(io.EOF)
	require.Eventually(t, func() bool {
		binding.builtinToolQueueSerial.Lock()
		defer binding.builtinToolQueueSerial.Unlock()
		return binding.builtinToolDraining && len(binding.builtinToolQueue) == 1 && binding.builtinToolQueue[0].reconcile
	}, time.Second, time.Millisecond)
	binding.builtinToolSerial.Unlock()

	require.Eventually(t, func() bool {
		page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		return err == nil && len(page.Entries) == 1 && page.Entries[0].Presentation.Outcome == "interrupted"
	}, time.Second, time.Millisecond)
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "call-1", ToolName: "write", Watermark: 2})
	waitForBuiltinToolLifecycleQueue(t, binding)
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, "interrupted", page.Entries[0].Presentation.Outcome)
}

func TestBuiltinToolLifecycleSettledInterruptsAndRejectsLateEnd(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &builtinLifecycleReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installBuiltinToolLifecycleReceiver(binding)
	require.NotNil(t, bridge.handler)

	binding.builtinToolSerial.Lock()
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "call-1", ToolName: "read", Watermark: 1})
	settled := make(chan struct{})
	go func() {
		manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(1), "state": "idle"})
		close(settled)
	}()
	select {
	case <-settled:
	case <-time.After(time.Second):
		binding.builtinToolSerial.Unlock()
		t.Fatal("parent settlement waited on builtin lifecycle persistence")
	}
	require.Eventually(t, func() bool {
		binding.builtinToolQueueSerial.Lock()
		defer binding.builtinToolQueueSerial.Unlock()
		return binding.builtinToolDraining && len(binding.builtinToolQueue) == 1 && binding.builtinToolQueue[0].reconcile
	}, time.Second, time.Millisecond)
	binding.builtinToolSerial.Unlock()
	require.Eventually(t, func() bool {
		page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		return err == nil && len(page.Entries) == 1 && page.Entries[0].Presentation.Outcome == "interrupted"
	}, time.Second, time.Millisecond)
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "call-1", ToolName: "read", Watermark: 2})
	waitForBuiltinToolLifecycleQueue(t, binding)
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, "interrupted", page.Entries[0].Presentation.Outcome)
}

func TestBuiltinToolLifecycleInterruptsExactCurrentBinding(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &builtinLifecycleReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installBuiltinToolLifecycleReceiver(binding)
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "call-1", ToolName: "write", Watermark: 1})
	require.Eventually(t, func() bool {
		page, lookupErr := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		return lookupErr == nil && len(page.Entries) == 1 && page.Entries[0].Presentation.Outcome == "active"
	}, time.Second, time.Millisecond)
	manager.interruptBuiltinToolInvocations(binding, fence)

	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, "Write interrupted", page.Entries[0].Message)

	stale := fence
	stale.ProcessIncarnation = "stale-incarnation"
	manager.interruptBuiltinToolInvocations(binding, stale)
	page, err = durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, store.ManagedBuiltinToolInterrupted, store.ManagedBuiltinToolOutcome(page.Entries[0].Presentation.Outcome))
}

func TestBuiltinToolLifecycleFIFOHandlesOneBindingInArrivalOrder(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &builtinLifecycleReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installBuiltinToolLifecycleReceiver(binding)
	published := make(chan string, 4)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		published <- entry.Presentation.ToolName + ":" + entry.Presentation.Outcome
	})

	binding.builtinToolSerial.Lock()
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "bash-call", ToolName: "bash", Watermark: 1})
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "bash-call", ToolName: "bash", Watermark: 2})
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "read-call", ToolName: "read", Watermark: 3})
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "read-call", ToolName: "read", Watermark: 4})
	binding.builtinToolSerial.Unlock()

	var got []string
	require.Eventually(t, func() bool {
		for len(got) < 4 {
			select {
			case event := <-published:
				got = append(got, event)
			default:
				return false
			}
		}
		return true
	}, time.Second, time.Millisecond)
	require.Equal(t, []string{"bash:active", "bash:success", "read:active", "read:success"}, got)
	waitForBuiltinToolLifecycleQueue(t, binding)
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
}

func TestBuiltinToolLifecycleQueueBoundsOverflowAndRecovers(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &builtinLifecycleReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installBuiltinToolLifecycleReceiver(binding)
	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		if entry.Presentation.ToolName != "bash" || entry.Presentation.Outcome != "active" {
			return
		}
		once.Do(func() { close(blocked) })
		<-release
	})

	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "anchor", ToolName: "bash", Watermark: 1})
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("first lifecycle callback did not block")
	}
	for index := 0; index <= maxQueuedManagedBuiltinToolLifecycle; index++ {
		bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: fmt.Sprintf("overflow-%d", index), ToolName: "read", Watermark: uint64(index + 2)})
	}
	require.Eventually(t, func() bool {
		binding.builtinToolQueueSerial.Lock()
		defer binding.builtinToolQueueSerial.Unlock()
		return binding.builtinToolOverflowed && len(binding.builtinToolQueue) == 0
	}, time.Second, time.Millisecond)
	close(release)

	require.Eventually(t, func() bool {
		page, lookupErr := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		return lookupErr == nil && len(page.Entries) == 1 && page.Entries[0].Presentation.Outcome == "interrupted"
	}, time.Second, time.Millisecond)
	waitForBuiltinToolLifecycleQueue(t, binding)
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "recovered", ToolName: "read", Watermark: 100})
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "recovered", ToolName: "read", Watermark: 101})
	require.Eventually(t, func() bool {
		page, lookupErr := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		if lookupErr != nil || len(page.Entries) != 2 {
			return false
		}
		for _, entry := range page.Entries {
			if entry.Presentation.ToolName == "read" && entry.Presentation.Outcome == "success" {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
}

func TestBuiltinToolLifecycleQueueRejectsStaleBindingBeforeDurableWrite(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &builtinLifecycleReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installBuiltinToolLifecycleReceiver(binding)

	binding.builtinToolSerial.Lock()
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "stale-call", ToolName: "write", Watermark: 1})
	require.Eventually(t, func() bool {
		binding.builtinToolQueueSerial.Lock()
		defer binding.builtinToolQueueSerial.Unlock()
		return binding.builtinToolDraining
	}, time.Second, time.Millisecond)
	replacement := &managedBinding{agentID: binding.agentID, lane: binding.lane, process: binding.process, rpc: binding.rpc, identity: binding.identity, sessionID: binding.sessionID, sessionEpoch: binding.sessionEpoch, runtimeGeneration: "replacement-generation", manifestRevision: binding.manifestRevision, manifestHash: binding.manifestHash}
	manager.mu.Lock()
	manager.bindings[fence.AgentID] = replacement
	manager.mu.Unlock()
	binding.builtinToolSerial.Unlock()

	waitForBuiltinToolLifecycleQueue(t, binding)
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Empty(t, page.Entries)
}

func TestBuiltinToolLifecycleQueueShutdownDrainsAndDoesNotLeakWorker(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &builtinLifecycleReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installBuiltinToolLifecycleReceiver(binding)
	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		if entry.Presentation.ToolName != "bash" || entry.Presentation.Outcome != "active" {
			return
		}
		once.Do(func() { close(blocked) })
		<-release
	})
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "shutdown-call", ToolName: "bash", Watermark: 1})
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("lifecycle callback did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before admitted lifecycle work drained: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for the lifecycle worker")
	}
	binding.builtinToolQueueSerial.Lock()
	require.True(t, binding.builtinToolQueueStopped)
	require.False(t, binding.builtinToolDraining)
	require.Empty(t, binding.builtinToolQueue)
	binding.builtinToolQueueSerial.Unlock()
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "shutdown-call", ToolName: "bash", Watermark: 2})
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, "interrupted", page.Entries[0].Presentation.Outcome)
}
