package managed

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

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
