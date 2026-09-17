package managed

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// directAcceptanceClient is the smallest direct-pipe fixture: requests travel
// through the real JSONL RPC client, while the test supplies the native stream.
func directAcceptanceClient(t *testing.T) (*rpcClient, <-chan map[string]any, *io.PipeWriter, <-chan map[string]any) {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	activity := make(chan map[string]any, 4)
	client.SetParentActivityEventHandler(func(event map[string]any) { activity <- event })
	return client, requests, stdoutWriter, activity
}

func directAcceptancePrompt(t *testing.T, direct DirectMailboxRPC, requests <-chan map[string]any, writer io.Writer, attemptID string) (<-chan DirectMailboxEvent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	events, err := direct.PromptManagedMailbox(ctx, directMailboxAttempt(attemptID))
	require.NoError(t, err)
	prompt := requireRequest(t, requests)
	require.Equal(t, "prompt", prompt["type"])
	acceptanceResponse(t, writer, prompt, true)
	return events, cancel
}

func acceptanceResponse(t *testing.T, writer io.Writer, prompt map[string]any, success bool) {
	t.Helper()
	response := map[string]any{"id": prompt["id"], "type": "response", "command": "prompt", "success": success, "data": map[string]any{}}
	if !success {
		response["error"] = "in-band prompt failure"
	}
	require.NoError(t, writeRPC(writer, response))
}

func acceptanceReceipt(id string) map[string]any {
	return map[string]any{"type": "message_end", "message": map[string]any{
		"id": id, "customType": "mecha.mailbox.appended.v1",
		"details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": id, "runtime_generation": "generation", "session_epoch": 2},
	}}
}

func collectAcceptanceSettlement(t *testing.T, events <-chan DirectMailboxEvent) (receipts, settlements int) {
	t.Helper()
	deadline := time.After(time.Second)
	for settlements == 0 {
		select {
		case event, open := <-events:
			require.True(t, open, "direct event stream closed before settlement")
			if event.Receipt != nil {
				receipts++
			}
			if event.Settled {
				settlements++
			}
		case <-deadline:
			t.Fatal("timed out waiting for direct settlement")
		}
	}
	// A duplicate native boundary must not produce another settlement event.
	require.Never(t, func() bool {
		select {
		case event := <-events:
			if event.Receipt != nil {
				receipts++
			}
			if event.Settled {
				settlements++
			}
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, time.Millisecond)
	return receipts, settlements
}

func requireOneIdle(t *testing.T, activity <-chan map[string]any) {
	t.Helper()
	select {
	case event := <-activity:
		require.Equal(t, "idle", event["state"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for parent idle activity")
	}
	require.Never(t, func() bool {
		select {
		case event := <-activity:
			t.Fatalf("duplicate parent idle activity: %#v", event)
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, time.Millisecond)
}

func TestManagedDirectTerminalSuccessAcceptsNextPrompt(t *testing.T) {
	client, requests, writer, activity := directAcceptanceClient(t)
	direct := DirectMailboxRPC(client)

	firstEvents, cancelFirst := directAcceptancePrompt(t, direct, requests, writer, "success-first")
	require.NoError(t, writeRPC(writer, acceptanceReceipt("success-first")))
	require.NoError(t, writeRPC(writer, map[string]any{"type": "message_end", "message": map[string]any{"id": "success-reply", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "done"}}}}))
	require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled"}))
	receipts, settlements := collectAcceptanceSettlement(t, firstEvents)
	require.Equal(t, 1, receipts)
	require.Equal(t, 1, settlements)
	requireOneIdle(t, activity)
	cancelFirst()
	for range firstEvents {
	}

	secondEvents, cancelSecond := directAcceptancePrompt(t, direct, requests, writer, "success-second")
	require.NoError(t, writeRPC(writer, acceptanceReceipt("success-second")))
	require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled"}))
	receipts, settlements = collectAcceptanceSettlement(t, secondEvents)
	require.Equal(t, 1, receipts)
	require.Equal(t, 1, settlements)
	requireOneIdle(t, activity)
	cancelSecond()
	for range secondEvents {
	}
}

func TestManagedDirectInBandTerminalErrorIsNotTransportEOF(t *testing.T) {
	client, requests, writer, activity := directAcceptanceClient(t)
	direct := DirectMailboxRPC(client)

	firstEvents, cancelFirst := directAcceptancePrompt(t, direct, requests, writer, "error-first")
	require.NoError(t, writeRPC(writer, acceptanceReceipt("error-first")))
	// Pi reports a terminal model error in-band as the final assistant entry;
	// the JSONL transport remains usable and must not be treated as EOF.
	require.NoError(t, writeRPC(writer, map[string]any{"type": "message_end", "message": map[string]any{"id": "error-reply", "role": "assistant", "stopReason": "error", "content": []any{map[string]any{"type": "text", "text": "model failed"}}}}))
	require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled"}))
	receipts, settlements := collectAcceptanceSettlement(t, firstEvents)
	require.Equal(t, 1, receipts)
	require.Equal(t, 1, settlements)
	requireOneIdle(t, activity)
	require.Nil(t, client.terminalError(), "an in-band model error must not terminalize the JSONL reader")
	cancelFirst()
	for range firstEvents {
	}

	secondEvents, cancelSecond := directAcceptancePrompt(t, direct, requests, writer, "error-second")
	require.NoError(t, writeRPC(writer, acceptanceReceipt("error-second")))
	require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled"}))
	receipts, settlements = collectAcceptanceSettlement(t, secondEvents)
	require.Equal(t, 1, receipts)
	require.Equal(t, 1, settlements)
	requireOneIdle(t, activity)
	cancelSecond()
	for range secondEvents {
	}
}

func TestManagedDirectCancellationAllowsNextPrompt(t *testing.T) {
	client, requests, writer, activity := directAcceptanceClient(t)
	direct := DirectMailboxRPC(client)

	firstEvents, cancelFirst := directAcceptancePrompt(t, direct, requests, writer, "cancel-first")
	require.NoError(t, writeRPC(writer, acceptanceReceipt("cancel-first")))
	select {
	case event := <-firstEvents:
		require.NotNil(t, event.Receipt)
		require.False(t, event.Settled)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancellation-turn receipt")
	}
	cancelFirst()
	for range firstEvents {
	}
	require.Never(t, func() bool {
		select {
		case event := <-activity:
			return event["state"] == "idle"
		default:
			return false
		}
	}, 50*time.Millisecond, time.Millisecond)

	secondEvents, cancelSecond := directAcceptancePrompt(t, direct, requests, writer, "cancel-second")
	require.NoError(t, writeRPC(writer, acceptanceReceipt("cancel-second")))
	require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled"}))
	receipts, settlements := collectAcceptanceSettlement(t, secondEvents)
	require.Equal(t, 1, receipts)
	require.Equal(t, 1, settlements)
	requireOneIdle(t, activity)
	cancelSecond()
	for range secondEvents {
	}
}

func TestManagedDirectOrdinaryToolSuccessAndFailureAcceptNextPrompt(t *testing.T) {
	for _, test := range []struct {
		name    string
		isError bool
	}{
		{name: "success", isError: false},
		{name: "failure", isError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, requests, writer, activity := directAcceptanceClient(t)
			direct := DirectMailboxRPC(client)
			toolEvents := make(chan BuiltinToolLifecycleEvent, 4)
			client.SetBuiltinToolLifecycleEventHandler(func(event BuiltinToolLifecycleEvent) { toolEvents <- event })

			firstEvents, cancelFirst := directAcceptancePrompt(t, direct, requests, writer, "tool-first")
			require.NoError(t, writeRPC(writer, acceptanceReceipt("tool-first")))
			require.NoError(t, writeRPC(writer, map[string]any{"type": "tool_execution_start", "toolCallId": "tool-call", "toolName": "bash", "args": map[string]any{}}))
			require.NoError(t, writeRPC(writer, map[string]any{"type": "tool_execution_end", "toolCallId": "tool-call", "toolName": "bash", "result": map[string]any{}, "isError": test.isError}))
			require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled"}))
			receipts, settlements := collectAcceptanceSettlement(t, firstEvents)
			require.Equal(t, 1, receipts)
			require.Equal(t, 1, settlements)
			requireOneIdle(t, activity)
			start, end := <-toolEvents, <-toolEvents
			require.Equal(t, "start", start.Phase)
			require.Equal(t, "end", end.Phase)
			require.Equal(t, test.isError, end.IsError)
			cancelFirst()
			for range firstEvents {
			}

			secondEvents, cancelSecond := directAcceptancePrompt(t, direct, requests, writer, "tool-second")
			require.NoError(t, writeRPC(writer, acceptanceReceipt("tool-second")))
			require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled"}))
			receipts, settlements = collectAcceptanceSettlement(t, secondEvents)
			require.Equal(t, 1, receipts)
			require.Equal(t, 1, settlements)
			requireOneIdle(t, activity)
			cancelSecond()
			for range secondEvents {
			}
		})
	}
}
