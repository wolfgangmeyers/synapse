package managed

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const composedOrdinaryToolName = "external_tool_v2"

const (
	composedOrdinarySecret = "ordinary-secret-marker"
	composedOrdinaryPath   = "/private/ordinary/path"
	composedOrdinaryOutput = "ordinary-private-output"
)

type composedOrdinaryRPC struct {
	*rpcClient
	parentIdle chan struct{}
}

func (r *composedOrdinaryRPC) SetParentActivityEventHandler(handler func(map[string]any)) {
	r.rpcClient.SetParentActivityEventHandler(func(event map[string]any) {
		if event["state"] == "idle" {
			select {
			case r.parentIdle <- struct{}{}:
			default:
			}
		}
		handler(event)
	})
}

type composedPublication struct {
	entry       store.ManagedHistoryEntry
	historySize int
	err         error
}

func newComposedOrdinaryLifecycleFixture(t *testing.T) (*Manager, *store.Store, *managedBinding, store.ManagedRuntimeFence, *composedOrdinaryRPC, *io.PipeWriter) {
	t.Helper()
	manager, durable, binding, fence, _ := readyGoalProjectionFixture(t)
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	rpc := &composedOrdinaryRPC{rpcClient: client, parentIdle: make(chan struct{}, 2)}
	binding.rpc = rpc
	manager.installParentActivityReceiver(binding)
	manager.installBuiltinToolLifecycleReceiver(binding)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return manager, durable, binding, fence, rpc, stdoutWriter
}

func writeComposedOrdinaryInvocation(t *testing.T, writer io.Writer, callID string, isError bool) {
	t.Helper()
	require.NoError(t, writeRPC(writer, map[string]any{
		"type":       "tool_execution_start",
		"toolCallId": callID,
		"toolName":   composedOrdinaryToolName,
		"args": map[string]any{
			"path":      composedOrdinaryPath,
			"api_token": composedOrdinarySecret,
		},
	}))
	require.NoError(t, writeRPC(writer, map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": callID,
		"toolName":   composedOrdinaryToolName,
		"result": map[string]any{
			"secret": composedOrdinarySecret,
			"output": composedOrdinaryOutput,
		},
		"isError": isError,
	}))
}

func nextComposedPublication(t *testing.T, published <-chan composedPublication) composedPublication {
	t.Helper()
	select {
	case observation := <-published:
		return observation
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ordinary tool publication")
		return composedPublication{}
	}
}

func waitForRPCSequence(t *testing.T, rpc *composedOrdinaryRPC, sequence uint64) {
	t.Helper()
	require.Eventually(t, func() bool {
		rpc.mu.Lock()
		defer rpc.mu.Unlock()
		return rpc.eventSequence >= sequence
	}, time.Second, time.Millisecond)
}

func assertComposedOrdinaryInputOnly(t *testing.T, durable *store.Store, fence store.ManagedRuntimeFence) []store.ManagedHistoryEntry {
	t.Helper()
	page, err := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 20)
	require.NoError(t, err)
	serialized := fmt.Sprintf("%#v", page.Entries)
	require.NotContains(t, serialized, composedOrdinarySecret)
	require.NotContains(t, serialized, composedOrdinaryOutput)
	for _, entry := range page.Entries {
		require.JSONEq(t, `{"api_token":"[REDACTED]","path":"`+composedOrdinaryPath+`"}`, string(entry.ToolInput))
	}
	return page.Entries
}

func TestComposedOrdinaryToolCompletionPersistsRedactedInputAndExcludesResults(t *testing.T) {
	manager, durable, binding, fence, _, stdoutWriter := newComposedOrdinaryLifecycleFixture(t)
	published := make(chan composedPublication, 2)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		if entry.Presentation.Outcome == "active" {
			return
		}
		page, err := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 20)
		published <- composedPublication{entry: entry, historySize: len(page.Entries), err: err}
	})

	writeComposedOrdinaryInvocation(t, stdoutWriter, "ordinary-success", false)
	writeComposedOrdinaryInvocation(t, stdoutWriter, "ordinary-failure", true)
	first := nextComposedPublication(t, published)
	second := nextComposedPublication(t, published)
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.Equal(t, 1, first.historySize)
	require.Equal(t, 2, second.historySize)
	require.Equal(t, "success", first.entry.Presentation.Outcome)
	require.Equal(t, "failed", second.entry.Presentation.Outcome)
	require.Equal(t, composedOrdinaryToolName, first.entry.Presentation.ToolName)
	require.Equal(t, composedOrdinaryToolName, second.entry.Presentation.ToolName)
	require.Equal(t, fence.AgentID, binding.agentID)

	rows := assertComposedOrdinaryInputOnly(t, durable, fence)
	require.Len(t, rows, 2)
	// Complete history is returned newest first.
	require.Equal(t, second.entry, rows[0])
	require.Equal(t, first.entry, rows[1])
}

func TestComposedOrdinaryToolCompletionDuplicateReplayPublishesOnce(t *testing.T) {
	manager, durable, _, fence, rpc, stdoutWriter := newComposedOrdinaryLifecycleFixture(t)
	published := make(chan store.ManagedHistoryEntry, 2)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		if entry.Presentation.Outcome != "active" {
			published <- entry
		}
	})

	writeComposedOrdinaryInvocation(t, stdoutWriter, "ordinary-replay", false)
	writeComposedOrdinaryInvocation(t, stdoutWriter, "ordinary-replay", false)
	entry := func() store.ManagedHistoryEntry {
		select {
		case value := <-published:
			return value
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for first ordinary publication")
			return store.ManagedHistoryEntry{}
		}
	}()
	require.Equal(t, "success", entry.Presentation.Outcome)
	waitForRPCSequence(t, rpc, 4)
	require.Never(t, func() bool {
		select {
		case <-published:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, time.Millisecond)
	require.Len(t, assertComposedOrdinaryInputOnly(t, durable, fence), 1)
}

func TestComposedOrdinaryToolCompletionRejectsMalformedFrames(t *testing.T) {
	manager, durable, _, fence, rpc, stdoutWriter := newComposedOrdinaryLifecycleFixture(t)
	published := make(chan store.ManagedHistoryEntry, 1)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) { published <- entry })
	base := map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": "malformed-call",
		"toolName":   composedOrdinaryToolName,
		"result":     map[string]any{"output": composedOrdinaryOutput},
		"isError":    false,
	}
	frames := []map[string]any{
		func() map[string]any { frame := cloneJSONRecord(base); frame["future"] = true; return frame }(),
		func() map[string]any { frame := cloneJSONRecord(base); delete(frame, "result"); return frame }(),
		func() map[string]any { frame := cloneJSONRecord(base); frame["isError"] = "false"; return frame }(),
	}
	for _, frame := range frames {
		require.NoError(t, writeRPC(stdoutWriter, frame))
	}
	waitForRPCSequence(t, rpc, uint64(len(frames)))
	require.Never(t, func() bool {
		select {
		case <-published:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, time.Millisecond)
	require.Empty(t, assertComposedOrdinaryInputOnly(t, durable, fence))
}

func TestComposedOrdinaryToolCompletionRejectsStaleBinding(t *testing.T) {
	manager, durable, oldBinding, fence, oldRPC, oldWriter := newComposedOrdinaryLifecycleFixture(t)
	published := make(chan store.ManagedHistoryEntry, 1)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		if entry.Presentation.Outcome != "active" {
			published <- entry
		}
	})

	replacementReader, replacementWriter := io.Pipe()
	replacementClient := NewRPCClient(io.Discard, replacementReader, func() error { return replacementWriter.Close() }).(*rpcClient)
	replacementRPC := &composedOrdinaryRPC{rpcClient: replacementClient, parentIdle: make(chan struct{}, 2)}
	replacementBinding := &managedBinding{
		agentID: oldBinding.agentID, lane: oldBinding.lane, process: oldBinding.process,
		rpc: replacementRPC, identity: oldBinding.identity, sessionID: oldBinding.sessionID,
		sessionEpoch: oldBinding.sessionEpoch, runtimeGeneration: oldBinding.runtimeGeneration,
		manifestRevision: oldBinding.manifestRevision, manifestHash: oldBinding.manifestHash,
	}
	manager.mu.Lock()
	manager.bindings[fence.AgentID] = replacementBinding
	manager.mu.Unlock()
	manager.installBuiltinToolLifecycleReceiver(replacementBinding)
	t.Cleanup(func() { require.NoError(t, replacementClient.Close()) })

	writeComposedOrdinaryInvocation(t, oldWriter, "stale-binding", false)
	waitForRPCSequence(t, oldRPC, 2)
	require.Never(t, func() bool {
		select {
		case <-published:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, time.Millisecond)
	require.Empty(t, assertComposedOrdinaryInputOnly(t, durable, fence))

	writeComposedOrdinaryInvocation(t, replacementWriter, "stale-binding", true)
	var entry store.ManagedHistoryEntry
	select {
	case entry = <-published:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement publication")
	}
	require.Equal(t, "failed", entry.Presentation.Outcome)
	rows := assertComposedOrdinaryInputOnly(t, durable, fence)
	require.Len(t, rows, 1)
	require.Equal(t, entry, rows[0])
}

func TestComposedOrdinaryToolCompletionIsPublishedBeforeParentSettlement(t *testing.T) {
	manager, durable, _, fence, rpc, stdoutWriter := newComposedOrdinaryLifecycleFixture(t)
	publishedStarted := make(chan struct{})
	durableAtPublication := make(chan bool, 1)
	releasePublication := make(chan struct{})
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		if entry.Presentation.Outcome == "active" {
			return
		}
		page, err := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 20)
		durableAtPublication <- err == nil && len(page.Entries) == 1
		close(publishedStarted)
		<-releasePublication
	})

	writeComposedOrdinaryInvocation(t, stdoutWriter, "settlement-order", false)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	select {
	case <-publishedStarted:
	case <-time.After(time.Second):
		t.Fatal("ordinary terminal was not published")
	}
	require.True(t, <-durableAtPublication, "publication must observe the committed terminal row")
	select {
	case <-rpc.parentIdle:
		t.Fatal("parent settlement overtook terminal publication")
	default:
	}
	close(releasePublication)
	select {
	case <-rpc.parentIdle:
	case <-time.After(time.Second):
		t.Fatal("parent settlement was not observed after terminal publication")
	}
	require.Len(t, assertComposedOrdinaryInputOnly(t, durable, fence), 1)
}

func TestComposedOrdinaryToolCompletionSurvivesPublisherPanic(t *testing.T) {
	manager, durable, _, fence, _, stdoutWriter := newComposedOrdinaryLifecycleFixture(t)
	published := make(chan struct{}, 1)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		if entry.Presentation.Outcome == "active" {
			return
		}
		published <- struct{}{}
		panic("private publisher panic")
	})
	writeComposedOrdinaryInvocation(t, stdoutWriter, "publisher-panic", false)
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("publisher was not called")
	}
	require.Eventually(t, func() bool {
		page, err := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 20)
		return err == nil && len(page.Entries) == 1
	}, time.Second, time.Millisecond)
	require.Len(t, assertComposedOrdinaryInputOnly(t, durable, fence), 1)
}
