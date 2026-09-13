package managed

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func writeRPC(writer io.Writer, value map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "%s\n", encoded)
	return err
}

func readJSONLRequests(reader io.Reader) <-chan map[string]any {
	requests := make(chan map[string]any, 8)
	go func() {
		defer close(requests)
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			var request map[string]any
			if json.Unmarshal(scanner.Bytes(), &request) == nil {
				requests <- request
			}
		}
	}()
	return requests
}

func requireRequest(t *testing.T, requests <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case request := <-requests:
		require.NotNil(t, request)
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for JSONL request")
		return nil
	}
}

func policyDetails(generation string, epoch int64) map[string]any {
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{
		"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false,
	}}
}

func snapshotDetails() map[string]any {
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": "owner", "sequence": 0, "runs": []any{}}
}

func directMailboxAttempt(id string) store.ManagedDirectAttempt {
	return store.ManagedDirectAttempt{MessageID: id, SessionEntryWatermark: "cursor-entry", Message: domain.Message{Body: "persisted body"}, Fence: store.ManagedRuntimeFence{AgentID: "agent-a", PiSessionID: "session-a", SessionEpoch: 2, ProcessPID: 3, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}}
}

func TestRPCClientManagedGoalEvaluationConsumesOnlyItsExactFencedResult(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	fence := store.ManagedRuntimeFence{AgentID: "agent-a", PiSessionID: "session-a", SessionEpoch: 2, ProcessPID: 3, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "123e4567-e89b-42d3-a456-426614174000", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
	policy := store.ManagedGoalPolicy{EvaluatorID: "123e4567-e89b-42d3-a456-426614174001", EvaluatorGoalRevision: 7, EvaluatorSettledSequence: 11, Config: store.ManagedGoalConfig{Goal: "finish the router"}}
	started := make(chan error, 1)
	go func() { started <- client.StartManagedGoalEvaluation(context.Background(), fence, policy) }()
	request := requireRequest(t, requests)
	require.Equal(t, "prompt", request["type"])
	command := request["message"].(string)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(command, "/mecha-policy-apply-v1 "))
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.Equal(t, policy.EvaluatorID, envelope["evaluator"].(map[string]any)["evaluation_id"])
	require.Equal(t, fence.ManifestHash, envelope["manifest_sha256"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "command": "prompt", "success": true}))
	require.NoError(t, <-started)

	result := make(chan struct{ outcome, reason string; err error }, 1)
	go func() { outcome, reason, waitErr := client.WaitForManagedGoalEvaluation(context.Background()); result <- struct{ outcome, reason string; err error }{outcome, reason, waitErr} }()
	// The matching request receipt and a stale result cannot settle the token.
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": map[string]any{"protocol": "mecha.policy/v1", "manifest_sha256": fence.ManifestHash, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "evaluator": map[string]any{"evaluation_id": policy.EvaluatorID, "goal_revision": policy.EvaluatorGoalRevision, "settled_sequence": policy.EvaluatorSettledSequence, "phase": "requested"}}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": map[string]any{"protocol": "mecha.policy/v1", "manifest_sha256": fence.ManifestHash, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "evaluator": map[string]any{"evaluation_id": "123e4567-e89b-42d3-a456-426614174002", "goal_revision": policy.EvaluatorGoalRevision, "settled_sequence": policy.EvaluatorSettledSequence, "phase": "result", "outcome": "DONE", "reason": "stale"}}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": map[string]any{"protocol": "mecha.policy/v1", "manifest_sha256": fence.ManifestHash, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "evaluator": map[string]any{"evaluation_id": policy.EvaluatorID, "goal_revision": policy.EvaluatorGoalRevision, "settled_sequence": policy.EvaluatorSettledSequence, "phase": "result", "outcome": "NOT_DONE", "reason": "work remains"}}}}))
	got := <-result
	require.NoError(t, got.err)
	require.Equal(t, store.ManagedGoalEvaluationNotDone, got.outcome)
	require.Equal(t, "work remains", got.reason)
}

func TestRPCClientSuppressesEvaluatorSettledEvent(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	fence := store.ManagedRuntimeFence{AgentID: "agent-a", PiSessionID: "session-a", SessionEpoch: 2, ProcessPID: 3, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "123e4567-e89b-42d3-a456-426614174000", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
	client.goalEvaluationToken = &managedGoalEvaluationToken{evaluationID: "123e4567-e89b-42d3-a456-426614174001", goalRevision: 1, settled: 3, fence: fence}
	activity := make(chan map[string]any, 1)
	client.SetParentActivityEventHandler(func(event map[string]any) { activity <- event })
	result := make(chan error, 1)
	go func() { _, _, err := client.WaitForManagedGoalEvaluation(context.Background()); result <- err }()
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": map[string]any{"protocol": "mecha.policy/v1", "manifest_sha256": fence.ManifestHash, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "evaluator": map[string]any{"evaluation_id": "123e4567-e89b-42d3-a456-426614174001", "goal_revision": int64(1), "settled_sequence": int64(3), "phase": "result", "outcome": "DONE", "reason": "complete"}}}}))
	require.NoError(t, <-result)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	select {
	case event := <-activity:
		t.Fatalf("evaluator settled event reached ordinary activity handling: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRPCClientManagedGoalEvaluationTimeoutLeavesNoArmedToken(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	client.goalEvaluationToken = &managedGoalEvaluationToken{evaluationID: "id", goalRevision: 1, settled: 1}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, _, err := client.WaitForManagedGoalEvaluation(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	client.mu.Lock()
	require.Nil(t, client.goalEvaluationToken)
	client.mu.Unlock()
}

func TestRPCClientCompactUsesPiNativeCommand(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	result := make(chan error, 1)
	go func() { result <- client.Compact(context.Background(), "Keep the delivery handoff.") }()
	request := requireRequest(t, requests)
	require.Equal(t, "compact", request["type"])
	require.Equal(t, "Keep the delivery handoff.", request["customInstructions"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "command": "compact", "success": true, "data": map[string]any{}}))
	require.NoError(t, <-result)
}

func TestRPCClientPostCompactionNotificationUsesPiCommand(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	result := make(chan error, 1)
	envelope := map[string]any{"protocol": "mecha.post-compaction/v1", "notification": "COMPACTION-CERT-DONE", "runtime_generation": "generation", "session_epoch": int64(2), "manifest_sha256": strings.Repeat("a", 64)}
	go func() { result <- client.PostCompactionNotification(context.Background(), envelope) }()
	request := requireRequest(t, requests)
	command, ok := request["message"].(string)
	require.True(t, ok)
	encoded := strings.TrimPrefix(command, "/mecha-post-compaction-notification-v1 ")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var sent map[string]any
	require.NoError(t, json.Unmarshal(decoded, &sent))
	require.Equal(t, envelope["notification"], sent["notification"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "command": "prompt", "success": true}))
	require.NoError(t, <-result)
}

func TestRPCClientDoesNotBacklogPostCompactionNotifications(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	for _, eventType := range []string{"message_start", "message_end"} {
		require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": eventType, "message": map[string]any{"customType": "mecha.post_compaction.v1", "display": true, "content": "COMPACTION-CERT-DONE"}}))
	}
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.eventSequence == 2 && len(client.backlog) == 0
	}, time.Second, time.Millisecond)
}

func TestRPCClientDeliversOnlyValidatedPostCompactionNotification(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	received := make(chan map[string]any, 1)
	client.SetParentCompactionNotificationHandler(func(event map[string]any) { received <- event })
	// Partial frames and wrong bindings are never terminal notification facts.
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_start", "message": map[string]any{"customType": "mecha.post_compaction.v1", "display": true}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.post_compaction.v1", "display": true, "details": map[string]any{"protocol": "wrong"}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.post_compaction.v1", "display": true, "content": "COMPACTION-CERT-DONE", "details": map[string]any{"protocol": "mecha.post-compaction/v1", "runtime_generation": "generation", "session_epoch": 2, "manifest_sha256": strings.Repeat("a", 64)}}}))
	select {
	case event := <-received:
		require.Equal(t, int64(3), event["sequence"])
		require.Equal(t, "generation", event["runtime_generation"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the validated post-compaction notification")
	}
	select {
	case extra := <-received:
		t.Fatalf("unexpected notification: %#v", extra)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestManagedDirectReceiptRequiresMatchingAppendMarker(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	direct, ok := client.(DirectMailboxRPC)
	require.True(t, ok)
	requests := readJSONLRequests(stdinReader)
	events, err := direct.PromptManagedMailbox(context.Background(), directMailboxAttempt("message-a"))
	require.NoError(t, err)
	request := requireRequest(t, requests)
	require.Equal(t, "prompt", request["type"])
	require.Contains(t, request["message"], "/mecha-mailbox-deliver-v1 ")
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "persisted-entry", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": "generation", "session_epoch": 2}}}))
	select {
	case event := <-events:
		require.NotNil(t, event.Receipt)
		require.Equal(t, "persisted-entry", event.Receipt.EntryIdentity)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for direct mailbox receipt")
	}
}

func TestManagedDirectMailboxAllowsAnEmptyBody(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	direct := client.(DirectMailboxRPC)
	requests := readJSONLRequests(stdinReader)
	attempt := directMailboxAttempt("empty-message")
	attempt.Message.Body = ""
	events, err := direct.PromptManagedMailbox(context.Background(), attempt)
	require.NoError(t, err)
	request := requireRequest(t, requests)
	command := request["message"].(string)
	encoded := strings.TrimPrefix(command, "/mecha-mailbox-deliver-v1 ")
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(payload, &envelope))
	require.Equal(t, "", envelope["content"])

	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "empty-entry", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "empty-message", "runtime_generation": "generation", "session_epoch": 2}}}))
	select {
	case event := <-events:
		require.NotNil(t, event.Receipt)
		require.Equal(t, "empty-entry", event.Receipt.EntryIdentity)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for empty direct mailbox receipt")
	}
}

func TestManagedDirectReceiptDiagnostics(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	direct := client.(DirectMailboxRPC)
	requests := readJSONLRequests(stdinReader)
	// This append belongs to an older command. PromptManagedMailbox retains it
	// as a metadata-only stale diagnostic instead of accepting it.
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": "generation", "session_epoch": 2}}}))
	require.Eventually(t, func() bool {
		client.(*rpcClient).mu.Lock()
		defer client.(*rpcClient).mu.Unlock()
		return client.(*rpcClient).eventSequence == 1
	}, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	events, err := direct.PromptManagedMailbox(ctx, directMailboxAttempt("message-a"))
	require.NoError(t, err)
	request := requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "turn_started"}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "other.custom.v1", "details": map[string]any{}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.mailbox.appended.v1"}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "other", "runtime_generation": "generation", "session_epoch": 2}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "entry-a", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": "generation", "session_epoch": 2}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "entry-a-duplicate", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": "generation", "session_epoch": 2}}}))
	var codes []string
	for index := 0; index < 5; index++ {
		select {
		case event := <-events:
			if event.DiagnosticCode != "" {
				codes = append(codes, event.DiagnosticCode)
			}
			if event.Receipt != nil {
				require.Equal(t, "entry-a", event.Receipt.EntryIdentity)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for direct mailbox diagnostics")
		}
	}
	cancel()
	for range events {
	}
	// Normal turn/activity frames and unrelated model output are expected while
	// the direct turn runs. Only malformed candidate receipts are diagnostic.
	require.ElementsMatch(t, []string{"stale_receipt", "malformed_receipt", "mismatched_receipt", "duplicate_receipt"}, codes)
}

func TestManagedDirectMailboxLeavesPolicyReceiptForItsFencedWaiter(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	direct := DirectMailboxRPC(client)
	requests := readJSONLRequests(stdinReader)

	events, err := direct.PromptManagedMailbox(context.Background(), directMailboxAttempt("message-a"))
	require.NoError(t, err)
	mailboxPrompt := requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": mailboxPrompt["id"], "type": "response", "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "mailbox-entry", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": "generation", "session_epoch": 2}}}))
	select {
	case event := <-events:
		require.NotNil(t, event.Receipt)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for direct mailbox receipt")
	}

	applied := make(chan error, 1)
	go func() {
		_, applyErr := client.ApplyPolicy(context.Background(), map[string]any{"protocol": "mecha.policy/v1"})
		applied <- applyErr
	}()
	policyPrompt := requireRequest(t, requests)
	require.True(t, strings.HasPrefix(policyPrompt["message"].(string), "/mecha-policy-apply-v1 "))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": policyPrompt["id"], "type": "response", "data": map[string]any{}}))
	require.NoError(t, <-applied)

	waited := make(chan struct {
		receipt map[string]any
		err     error
	}, 1)
	go func() {
		receipt, waitErr := client.WaitForPolicyApplied(context.Background(), "generation", 2)
		waited <- struct {
			receipt map[string]any
			err     error
		}{receipt: receipt, err: waitErr}
	}()
	retainedPolicy := policyDetails("generation", 2)
	retainedPolicy["applied"].(map[string]any)["goal_revision"] = int64(1)
	retainedPolicy["applied"].(map[string]any)["future_extension_field"] = map[string]any{"enabled": true}
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": retainedPolicy}}))
	result := <-waited
	require.NoError(t, result.err)
	require.Equal(t, "generation", result.receipt["runtime_generation"])
	require.Equal(t, json.Number("1"), result.receipt["applied"].(map[string]any)["goal_revision"])
}

func TestManagedDirectMailboxCapturesAssistantReplyUntilSettled(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	direct := client.(DirectMailboxRPC)
	requests := readJSONLRequests(stdinReader)
	events, err := direct.PromptManagedMailbox(context.Background(), directMailboxAttempt("message-a"))
	require.NoError(t, err)
	request := requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "append-entry", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": "generation", "session_epoch": 2}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_start"}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "interim-entry", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "working"}}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "reply-entry", "role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "hidden"}, map[string]any{"type": "text", "text": "reply works"}}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))

	var gotReceipt, gotSettled bool
	var reply *DirectMailboxReply
	for index := 0; index < 2; index++ {
		select {
		case event := <-events:
			if event.Receipt != nil {
				gotReceipt = true
				require.Equal(t, "append-entry", event.Receipt.EntryIdentity)
			}
			if event.Reply != nil {
				reply = event.Reply
			}
			gotSettled = gotSettled || event.Settled
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for direct mailbox turn")
		}
	}
	require.True(t, gotReceipt)
	require.True(t, gotSettled)
	require.Equal(t, &DirectMailboxReply{EntryIdentity: "reply-entry", Body: "reply works"}, reply)
}

func TestManagedDirectMailboxWaitsForRootControlFollowUpBeforeSettling(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	direct := client.(DirectMailboxRPC)
	requests := readJSONLRequests(stdinReader)
	attempt := directMailboxAttempt("message-a")
	generation := "123e4567-e89b-42d3-a456-426614174003"
	attempt.Fence.RuntimeGeneration = generation
	events, err := direct.PromptManagedMailbox(context.Background(), attempt)
	require.NoError(t, err)
	prompt := requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": prompt["id"], "type": "response", "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "append-entry", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": generation, "session_epoch": 2}}}))
	select {
	case event := <-events:
		require.Equal(t, "append-entry", event.Receipt.EntryIdentity)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for direct mailbox receipt")
	}

	requestID := "123e4567-e89b-42d3-a456-426614174001"
	definitionSHA := strings.Repeat("b", 64)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.control.request.v1", "display": false, "content": "Router control request queued.", "details": map[string]any{
		"protocol": "mecha.control/v1", "request_id": requestID, "tool_call_id": "call-1", "capability_id": "web_search", "tool_name": "web_search__" + definitionSHA,
		"definition_id": "def_" + definitionSHA, "definition_sha256": definitionSHA, "operation_id": "web.search", "manifest_sha256": strings.Repeat("a", 64),
		"runtime_generation": generation, "session_epoch": 2, "context_ref": "root", "arguments": map[string]any{"query": "RFC 9110"},
	}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "provisional", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "I am waiting."}}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	select {
	case event := <-events:
		t.Fatalf("root control let the provisional turn settle direct delivery: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "mcr1_result", "customType": "mecha.control.result.v1", "display": false, "details": map[string]any{
		"protocol": "mecha.control/v1", "request_id": requestID, "result_id": "123e4567-e89b-42d3-a456-426614174002", "target_session_epoch": 2,
		"runtime_generation": generation, "manifest_sha256": strings.Repeat("a", 64), "status": "ok", "details": map[string]any{},
	}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "final", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "RFC 9110 is HTTP Semantics."}}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	select {
	case event := <-events:
		require.True(t, event.Settled)
		require.Equal(t, &DirectMailboxReply{EntryIdentity: "final", Body: "RFC 9110 is HTTP Semantics."}, event.Reply)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the root-control follow-up to settle")
	}
}

func TestManagedDirectMatcherClearsAfterCanceledWait(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	direct := client.(DirectMailboxRPC)
	requests := readJSONLRequests(stdinReader)
	ctx, cancel := context.WithCancel(context.Background())
	events, err := direct.PromptManagedMailbox(ctx, directMailboxAttempt("message-a"))
	require.NoError(t, err)
	_ = requireRequest(t, requests)
	cancel()
	for range events {
	}
	require.Eventually(t, func() bool {
		client.(*rpcClient).mu.Lock()
		defer client.(*rpcClient).mu.Unlock()
		return client.(*rpcClient).directMailbox == nil
	}, time.Second, time.Millisecond)
	second, err := direct.PromptManagedMailbox(context.Background(), directMailboxAttempt("message-b"))
	require.NoError(t, err)
	_ = requireRequest(t, requests)
	// The second command proves the timed-out matcher was removed. Closing the
	// client ends its waiter without accepting any prompt response as a receipt.
	require.NotNil(t, second)
}

func TestManagedDirectRecoveryUsesExactPersistedEntry(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	direct := client.(DirectMailboxRPC)
	requests := readJSONLRequests(stdinReader)
	result := make(chan struct {
		recovery DirectMailboxRecovery
		err      error
	}, 1)
	go func() {
		recovery, err := direct.RecoverManagedMailbox(context.Background(), directMailboxAttempt("message-a"))
		result <- struct {
			recovery DirectMailboxRecovery
			err      error
		}{recovery, err}
	}()
	request := requireRequest(t, requests)
	require.Equal(t, "get_entries", request["type"])
	require.Equal(t, "cursor-entry", request["since"])
	entries := []any{
		map[string]any{"id": "wrong-content", "type": "custom_message", "customType": "mecha.mailbox.appended.v1", "content": "other", "display": false, "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": "generation", "session_epoch": 2}},
		map[string]any{"id": "right", "type": "custom_message", "customType": "mecha.mailbox.appended.v1", "content": "persisted body", "display": false, "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": "generation", "session_epoch": 2}},
	}
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "data": map[string]any{"entries": entries, "leafId": "right"}}))
	outcome := <-result
	require.NoError(t, outcome.err)
	require.NotNil(t, outcome.recovery.Receipt)
	require.Equal(t, "right", outcome.recovery.Receipt.EntryIdentity)
	require.Contains(t, outcome.recovery.Diagnostics, "mismatched_receipt")
}

func TestRPCClientOrdersCallsBoundsResponsesAndUsesAcceptedTools(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	go func() {
		scanner := bufio.NewScanner(stdinReader)
		for scanner.Scan() {
			var request map[string]any
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				return
			}
			id, _ := request["id"].(string)
			typ, _ := request["type"].(string)
			response := map[string]any{"id": id, "type": "response", "success": true, "data": map[string]any{}}
			switch typ {
			case "get_state":
				response["data"] = map[string]any{"sessionId": "session"}
			case "get_commands":
				response["data"] = map[string]any{"commands": []any{"goal"}}
			case "prompt":
				if message, _ := request["message"].(string); strings.HasPrefix(message, "/mecha-bootstrap-v1 ") {
					_ = writeRPC(stdoutWriter, map[string]any{"customType": "mecha.tools.accepted.v1", "details": map[string]any{"runtime_generation": "generation", "session_epoch": 1, "manifest_revision": 1, "manifest_sha256": strings.Repeat("a", 64), "active_tools": []string{"goal"}}})
				}
			}
			if err := writeRPC(stdoutWriter, response); err != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	state, err := client.GetState(ctx)
	require.NoError(t, err)
	require.Equal(t, "session", state["sessionId"])
	commands, err := client.GetCommands(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"goal"}, commands)
	envelope := map[string]any{"desired_manifest": map[string]any{"tools": []any{map[string]any{"name": "goal"}}}}
	require.NoError(t, client.Bootstrap(ctx, envelope))
	require.NoError(t, client.WaitForReceipt(ctx, "generation", strings.Repeat("a", 64), 1, 1, "mecha.tools.accepted.v1"))
	active, err := client.GetActiveTools(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"goal"}, active)
	require.NoError(t, client.Prompt(ctx, "after-ready"))
}

func TestRPCReadinessSourceCommandsUseDirectJSONL(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	policyResult := make(chan struct {
		value map[string]any
		err   error
	}, 1)
	go func() {
		value, err := client.ApplyPolicy(ctx, map[string]any{"protocol": "mecha.policy/v1"})
		policyResult <- struct {
			value map[string]any
			err   error
		}{value, err}
	}()
	policyRequest := requireRequest(t, requests)
	require.Equal(t, "prompt", policyRequest["type"])
	require.True(t, strings.HasPrefix(policyRequest["message"].(string), "/mecha-policy-apply-v1 "))
	policyID := policyRequest["id"].(string)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_start", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": policyDetails("generation", 1)}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": policyID, "type": "response", "success": true, "data": map[string]any{"success": true, "command": "prompt"}}))
	policy := <-policyResult
	require.NoError(t, policy.err)
	require.Equal(t, map[string]any{"success": true, "command": "prompt"}, policy.value)

	waitPolicy := make(chan error, 1)
	go func() {
		_, err := client.WaitForPolicyApplied(ctx, "generation", 1)
		waitPolicy <- err
	}()
	select {
	case err := <-waitPolicy:
		t.Fatalf("message_start satisfied the policy receipt: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": policyDetails("generation", 1)}}))
	require.NoError(t, <-waitPolicy)

	snapshotResult := make(chan struct {
		value map[string]any
		err   error
	}, 1)
	go func() {
		value, err := client.RequestChildSnapshot(ctx, map[string]any{"protocol": "mecha.child-snapshot/v1"})
		snapshotResult <- struct {
			value map[string]any
			err   error
		}{value, err}
	}()
	snapshotRequest := requireRequest(t, requests)
	require.Equal(t, "prompt", snapshotRequest["type"])
	require.True(t, strings.HasPrefix(snapshotRequest["message"].(string), "/mecha-child-snapshot-v1 "))
	snapshotID := snapshotRequest["id"].(string)
	// The persisted receipt may precede Pi's normal prompt acknowledgement.
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.child.snapshot.v1", "details": snapshotDetails()}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": snapshotID, "type": "response", "success": true, "data": map[string]any{"success": true, "command": "prompt"}}))
	snapshot := <-snapshotResult
	require.NoError(t, snapshot.err)
	require.Equal(t, map[string]any{"success": true, "command": "prompt"}, snapshot.value)
	gotSnapshot, err := client.WaitForChildSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, "snapshot", gotSnapshot["snapshot_id"])
}

type blockingJSONLWriter struct {
	entered chan struct{}
	release chan struct{}
	stdout  io.Writer
	once    sync.Once
}

func (w *blockingJSONLWriter) Write(value []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	var request map[string]any
	if err := json.Unmarshal(bytesTrimSpace(value), &request); err != nil {
		return 0, err
	}
	if err := writeRPC(w.stdout, map[string]any{"id": request["id"], "type": "response", "success": true, "data": map[string]any{"success": true, "command": "prompt"}}); err != nil {
		return 0, err
	}
	return len(value), nil
}

func bytesTrimSpace(value []byte) []byte { return []byte(strings.TrimSpace(string(value))) }

func TestPrivateCommandCapturesWatermarkInsideItsWriteSerializer(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	writer := &blockingJSONLWriter{entered: make(chan struct{}), release: make(chan struct{}), stdout: stdoutWriter}
	client := NewRPCClient(writer, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	result := make(chan error, 1)
	go func() {
		_, err := client.ApplyPolicy(context.Background(), map[string]any{"protocol": "mecha.policy/v1"})
		result <- err
	}()
	<-writer.entered

	// This lock attempt models the read loop trying to append a receipt while
	// the private JSONL write is blocked. It cannot enter until the write ends.
	acquired := make(chan struct{}, 1)
	go func() {
		client.mu.Lock()
		client.mu.Unlock()
		acquired <- struct{}{}
	}()
	select {
	case <-acquired:
		t.Fatal("private command released the matcher lock before its JSONL write")
	case <-time.After(25 * time.Millisecond):
	}
	close(writer.release)
	require.NoError(t, <-result)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": policyDetails("generation", 1)}}))
	_, err := client.WaitForPolicyApplied(context.Background(), "generation", 1)
	require.NoError(t, err)
}

func TestLateResponsesNeverOccupyReadinessBacklog(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := client.Prompt(ctx, "cancelled")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	request := requireRequest(t, requests)
	for index := 0; index < 129; index++ {
		id := fmt.Sprintf("late-%d", index)
		if index == 0 {
			id = request["id"].(string)
		}
		require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": id, "type": "response", "success": true, "data": map[string]any{}}))
	}
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.terminal == nil && client.eventSequence == 0 && len(client.backlog) == 0
	}, time.Second, time.Millisecond)
}

func TestRPCReadinessMatcherRejectsStaleMalformedAndOverflowTraffic(t *testing.T) {
	newClient := func(t *testing.T) (*rpcClient, *io.PipeWriter) {
		t.Helper()
		reader, writer := io.Pipe()
		client := NewRPCClient(io.Discard, reader, func() error { return writer.Close() }).(*rpcClient)
		t.Cleanup(func() { _ = client.Close() })
		return client, writer
	}
	watermark := func(client *rpcClient) uint64 {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.eventSequence
	}
	event := func(generation string, epoch int64) map[string]any {
		return map[string]any{"customType": "mecha.policy.applied.v1", "details": policyDetails(generation, epoch)}
	}

	t.Run("unrelated notifications remain ordered", func(t *testing.T) {
		client, writer := newClient(t)
		require.NoError(t, writeRPC(writer, map[string]any{"type": "unrelated", "value": "kept"}))
		require.NoError(t, writeRPC(writer, event("generation", 1)))
		require.Eventually(t, func() bool { return watermark(client) == 2 }, time.Second, time.Millisecond)
		_, err := client.waitForReceipt(context.Background(), 0, "mecha.policy.applied.v1", func(receipt map[string]any) error {
			_, err := normalizeAppliedPolicyReceipt(receiptDetails(receipt)["applied"])
			return err
		})
		require.NoError(t, err)
		client.mu.Lock()
		require.Len(t, client.backlog, 1)
		require.Equal(t, "unrelated", client.backlog[0].value["type"])
		client.mu.Unlock()
	})

	for _, test := range []struct {
		name      string
		watermark func(*rpcClient) uint64
		event     map[string]any
	}{
		{"stale", func(client *rpcClient) uint64 { return watermark(client) }, event("generation", 1)},
		{"wrong generation", func(*rpcClient) uint64 { return 0 }, event("other", 1)},
		{"wrong epoch", func(*rpcClient) uint64 { return 0 }, event("generation", 2)},
		{"malformed", func(*rpcClient) uint64 { return 0 }, map[string]any{"customType": "mecha.policy.applied.v1", "details": map[string]any{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, writer := newClient(t)
			require.NoError(t, writeRPC(writer, test.event))
			require.Eventually(t, func() bool { return watermark(client) == 1 }, time.Second, time.Millisecond)
			_, err := client.waitForReceipt(context.Background(), test.watermark(client), "mecha.policy.applied.v1", func(receipt map[string]any) error {
				details := receiptDetails(receipt)
				if details == nil || details["runtime_generation"] != "generation" || !sameEpoch(details["session_epoch"], 1) {
					return errors.New("receipt does not match")
				}
				_, err := normalizeAppliedPolicyReceipt(details["applied"])
				return err
			})
			require.Error(t, err)
			require.Error(t, client.terminalError())
		})
	}

	t.Run("129th notification terminalizes", func(t *testing.T) {
		client, writer := newClient(t)
		for index := 0; index < 129; index++ {
			require.NoError(t, writeRPC(writer, map[string]any{"type": "unrelated", "index": index}))
		}
		require.Eventually(t, func() bool { return errors.Is(client.terminalError(), ErrReadinessBacklogFull) }, time.Second, time.Millisecond)
	})
}

func TestRPCClientWakesWaitersOnEOFAndClose(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	requests := readJSONLRequests(stdinReader)
	result := make(chan error, 1)
	go func() {
		_, err := client.GetState(context.Background())
		result <- err
	}()
	_ = requireRequest(t, requests)
	require.NoError(t, stdoutWriter.Close())
	require.ErrorIs(t, <-result, io.EOF)
	require.NoError(t, client.Close())

	otherOutput, otherWriter := io.Pipe()
	other := NewRPCClient(io.Discard, otherOutput, func() error { return otherWriter.Close() }).(*rpcClient)
	require.NoError(t, other.Close())
	err := other.Prompt(context.Background(), "after-close")
	require.Contains(t, err.Error(), "closed")
}

func TestRestartTurnSettlerTreatsAgentSettledAsDrainBoundary(t *testing.T) {
	newClient := func(t *testing.T) (*rpcClient, *io.PipeWriter) {
		t.Helper()
		reader, writer := io.Pipe()
		client := NewRPCClient(io.Discard, reader, func() error { return writer.Close() }).(*rpcClient)
		t.Cleanup(func() { _ = client.Close() })
		return client, writer
	}
	fence := store.ManagedRuntimeFence{AgentID: "agent", PiSessionID: "session", SessionEpoch: 1, ProcessPID: 17, ProcessStartMarker: "marker", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
	t.Run("post-watermark settle accepts drain and leaves continuation alone", func(t *testing.T) {
		client, writer := newClient(t)
		result := make(chan error, 1)
		go func() { result <- client.SettlePreparedRestartTurn(context.Background(), fence) }()
		require.Eventually(t, func() bool {
			client.mu.Lock()
			defer client.mu.Unlock()
			return client.settlementToken != nil
		}, time.Second, time.Millisecond)
		require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled"}))
		require.NoError(t, <-result)
		// A settled handler may intentionally start a continuation. It is not
		// treated as target work and is left in the old pipe's normal backlog.
		require.NoError(t, writeRPC(writer, map[string]any{"type": "turn_started", "source": "old-continuation"}))
		require.Eventually(t, func() bool {
			client.mu.Lock()
			defer client.mu.Unlock()
			return len(client.backlog) == 1 && client.backlog[0].value["source"] == "old-continuation"
		}, time.Second, time.Millisecond)
	})
	t.Run("stale malformed and EOF fail", func(t *testing.T) {
		client, writer := newClient(t)
		require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled"}))
		require.Eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return client.eventSequence == 1 }, time.Second, time.Millisecond)
		require.Error(t, client.SettlePreparedRestartTurn(context.Background(), fence))
		client, writer = newClient(t)
		result := make(chan error, 1)
		go func() { result <- client.SettlePreparedRestartTurn(context.Background(), fence) }()
		require.Eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return client.settlementToken != nil }, time.Second, time.Millisecond)
		require.NoError(t, writeRPC(writer, map[string]any{"type": "agent_settled", "extra": true}))
		require.Error(t, <-result)
		client, writer = newClient(t)
		result = make(chan error, 1)
		go func() { result <- client.SettlePreparedRestartTurn(context.Background(), fence) }()
		require.Eventually(t, func() bool { client.mu.Lock(); defer client.mu.Unlock(); return client.settlementToken != nil }, time.Second, time.Millisecond)
		require.NoError(t, writer.Close())
		require.ErrorIs(t, <-result, io.EOF)
	})
}

func TestRPCClientRejectsNonCanonicalManagedCommandBeforeReadiness(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	go func() {
		_ = writeRPC(stdoutWriter, map[string]any{
			"id": "managed-1", "type": "response", "success": true,
			"data": map[string]any{"commands": []any{"/goal:1"}},
		})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.GetCommands(ctx)
	require.EqualError(t, err, `managed command inventory contains non-canonical command "/goal:1"`)
}

func TestRPCClientRejectsOversizedLine(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	go func() { _, _ = stdoutWriter.Write([]byte(strings.Repeat("x", MaxRPCLineBytes+1) + "\n")) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.GetState(ctx)
	require.Error(t, err)
}

func TestRPCClientDeliversOnlyValidParentCompactionEventsOutsideReadinessBacklog(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	var live ParentCompactionRPC = client
	received := make(chan map[string]any, 1)
	live.SetParentCompactionEventHandler(func(event map[string]any) { received <- event })
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "compaction_start"}))
	select {
	case event := <-received:
		require.Equal(t, "compacting", event["state"])
		require.EqualValues(t, 1, event["sequence"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for parent compaction")
	}
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "compaction_end", "aborted": true}))
	select {
	case event := <-received:
		require.Equal(t, "idle", event["state"])
		require.Equal(t, "aborted", event["outcome"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for parent compaction terminal")
	}
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.eventSequence == 2 && len(client.backlog) == 0
	}, time.Second, time.Millisecond)
}

func TestRPCClientDeliversParentActivityWithoutBlockingDirectSettlement(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	direct := DirectMailboxRPC(client)
	activity := make(chan map[string]any, 2)
	ParentActivityRPC(client).SetParentActivityEventHandler(func(event map[string]any) { activity <- event })
	requests := readJSONLRequests(stdinReader)
	events, err := direct.PromptManagedMailbox(context.Background(), directMailboxAttempt("message-a"))
	require.NoError(t, err)
	request := requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "append-entry", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": "generation", "session_epoch": 2}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_start"}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))

	var receipt, settled bool
	for index := 0; index < 2; index++ {
		select {
		case event := <-events:
			receipt = receipt || event.Receipt != nil
			settled = settled || event.Settled
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for direct mailbox settlement")
		}
	}
	require.True(t, receipt)
	require.True(t, settled)
	activityBySequence := map[int64]string{}
	for range 2 {
		select {
		case event := <-activity:
			sequence, ok := event["sequence"].(int64)
			require.True(t, ok)
			state, ok := event["state"].(string)
			require.True(t, ok)
			activityBySequence[sequence] = state
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for parent activity")
		}
	}
	require.Equal(t, map[int64]string{2: "active", 3: "idle"}, activityBySequence)
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.eventSequence == 3 && len(client.backlog) == 0
	}, time.Second, time.Millisecond)
}

func TestRPCClientReportsReaderEOFSeparatelyFromExplicitClose(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	terminal := make(chan error, 1)
	client.SetRPCReaderTerminalHandler(func(err error) { terminal <- err })
	require.NoError(t, stdoutWriter.Close())
	select {
	case err := <-terminal:
		require.ErrorIs(t, err, io.EOF)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for RPC reader EOF")
	}
	require.NoError(t, client.Close())
}

func TestRPCClientReportsReaderMalformedFrame(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	terminal := make(chan error, 1)
	client.SetRPCReaderTerminalHandler(func(err error) { terminal <- err })
	_, err := stdoutWriter.Write([]byte("not json\n"))
	require.NoError(t, err)
	select {
	case err := <-terminal:
		require.EqualError(t, err, "Pi emitted malformed RPC JSON")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for RPC reader error")
	}
	require.NoError(t, client.Close())
}

func TestRPCClientDeliversChildDeltasInStdoutOrder(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	client.SetChildDeltaEventHandler(func(delta map[string]any) {
		sequence := delta["sequence"].(int64)
		if sequence == 1 {
			close(firstStarted)
			<-releaseFirst
			return
		}
		close(secondStarted)
	})
	delta := func(sequence int64) map[string]any {
		return map[string]any{"protocol": "mecha.children/v1", "owner_session_id": "owner", "sequence": sequence, "task_id": "task", "run_id": "run", "parent_entry_id": "entry", "changes": map[string]any{"lifecycle_state": "running"}}
	}
	writeDelta := func(sequence int64) error {
		return writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.child.delta.v1", "content": "Managed child lifecycle updated.", "display": false, "details": delta(sequence)}})
	}
	require.NoError(t, writeDelta(1))
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first child delta")
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- writeDelta(2) }()
	select {
	case <-secondStarted:
		t.Fatal("later child delta overtook the first callback")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case err := <-writeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("timed out writing second child delta")
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second child delta")
	}
}

func TestRPCClientDeliversOnlyValidChildDeltasOutsideReadinessBacklog(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	var live ChildDeltaRPC = client
	received := make(chan map[string]any, 1)
	live.SetChildDeltaEventHandler(func(delta map[string]any) { received <- delta })

	valid := map[string]any{"protocol": "mecha.children/v1", "owner_session_id": "owner", "sequence": 1, "task_id": "task", "run_id": "run", "parent_entry_id": "entry", "changes": map[string]any{"lifecycle_state": "running"}}
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{
		"customType": "mecha.child.delta.v1", "content": "Managed child lifecycle updated.", "display": false, "details": valid,
	}}))
	select {
	case delta := <-received:
		require.Equal(t, "owner", delta["owner_session_id"])
		require.EqualValues(t, 1, delta["sequence"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for child delta")
	}

	// A malformed delta is consumed as its own event class and cannot poison a
	// readiness receipt wait or fill the bounded readiness backlog.
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{
		"customType": "mecha.child.delta.v1", "content": "Managed child lifecycle updated.", "display": false, "details": map[string]any{"protocol": "mecha.children/v1"},
	}}))
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.eventSequence == 2 && len(client.backlog) == 0
	}, time.Second, time.Millisecond)
	select {
	case delta := <-received:
		t.Fatalf("malformed child delta reached callback: %#v", delta)
	default:
	}
}
