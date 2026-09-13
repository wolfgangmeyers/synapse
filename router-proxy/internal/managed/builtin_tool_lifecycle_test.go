package managed

import (
	"context"
	"io"
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

func TestBuiltinToolLifecycleCreatesOneEntryAndBuffersEndBeforeStart(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	bridge := &builtinLifecycleReceiverRPC{goalProjectionRPC: rpc}
	binding.rpc = bridge
	manager.installBuiltinToolLifecycleReceiver(binding)
	require.NotNil(t, bridge.handler)

	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "call-1", ToolName: "edit", IsError: true, Watermark: 1})
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Empty(t, page.Entries)
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "call-1", ToolName: "edit", Watermark: 2})

	page, err = durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, "Edit failed", page.Entries[0].Message)
	require.Equal(t, "failed", page.Entries[0].Presentation.Outcome)

	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "call-1", ToolName: "edit", IsError: true, Watermark: 3})
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

	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "call-1", ToolName: "write", Watermark: 1})
	bridge.terminalHandler(io.EOF)

	require.Eventually(t, func() bool {
		page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		return err == nil && len(page.Entries) == 1 && page.Entries[0].Presentation.Outcome == "interrupted"
	}, time.Second, time.Millisecond)
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "call-1", ToolName: "write", Watermark: 2})
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

	bridge.handler(BuiltinToolLifecycleEvent{Phase: "start", ToolCallID: "call-1", ToolName: "read", Watermark: 1})
	manager.handleParentActivityEvent(binding, map[string]any{"sequence": int64(1), "state": "idle"})
	require.Eventually(t, func() bool {
		page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		return err == nil && len(page.Entries) == 1 && page.Entries[0].Presentation.Outcome == "interrupted"
	}, time.Second, time.Millisecond)
	bridge.handler(BuiltinToolLifecycleEvent{Phase: "end", ToolCallID: "call-1", ToolName: "read", Watermark: 2})
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
