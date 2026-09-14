package managed

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRPCAbortUsesOnlyTheAbortWireCommandAndPropagatesErrors(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		result <- client.Abort(ctx)
	}()
	request := requireRequest(t, requests)
	require.Equal(t, "abort", request["type"])
	require.NotEmpty(t, request["id"])
	require.Len(t, request, 2, "Abort must not be encoded as a prompt or carry a payload")
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "error": map[string]any{"code": "aborted"}}))
	require.Error(t, <-result)
}

func TestRPCAbortRequiresTypedSuccessfulResponse(t *testing.T) {
	testCases := []struct {
		name     string
		response map[string]any
		want     error
	}{
		{name: "success", response: map[string]any{"type": "response", "command": "abort", "success": true}},
		{name: "missing success", response: map[string]any{"type": "response", "command": "abort"}, want: ErrAbortResponseMalformed},
		{name: "wrong command", response: map[string]any{"type": "response", "command": "get_state", "success": true}, want: ErrAbortResponseMalformed},
		{name: "rejected", response: map[string]any{"type": "response", "command": "abort", "success": false}, want: ErrAbortResponseRejected},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stdinReader, stdinWriter := io.Pipe()
			stdoutReader, stdoutWriter := io.Pipe()
			client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			requests := readJSONLRequests(stdinReader)
			result := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				result <- client.Abort(ctx)
			}()
			request := requireRequest(t, requests)
			response := make(map[string]any, len(testCase.response)+1)
			for key, value := range testCase.response {
				response[key] = value
			}
			response["id"] = request["id"]
			require.NoError(t, writeRPC(stdoutWriter, response))
			if testCase.want == nil {
				require.NoError(t, <-result)
			} else {
				require.ErrorIs(t, <-result, testCase.want)
			}
		})
	}
}

func TestRPCManagedGoalCleanupFailsClosedOnSemanticQueueOverflow(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() {
		_ = client.Close()
		client.WaitForClose()
	})
	requests := readJSONLRequests(stdinReader)
	fence := testManagedGoalCleanupFence()
	token := &managedGoalEvaluationToken{evaluationID: "evaluation", goalRevision: 1, settled: 3, fence: fence}
	client.mu.Lock()
	client.goalEvaluationCleanup = &managedGoalEvaluationCleanup{token: token}
	client.suppressNextEvaluatorSettled = true
	client.mu.Unlock()
	entered := make(chan struct{})
	release := make(chan struct{})
	require.True(t, client.enqueueSemantic(func() { close(entered); <-release }))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("blocking semantic callback did not start")
	}
	for index := 0; index < rpcSemanticQueueCapacity; index++ {
		require.True(t, client.enqueueSemantic(func() {}))
	}
	result := make(chan error, 1)
	go func() {
		result <- client.ReconcileManagedGoalEvaluation(context.Background(), fence, token.evaluationID, token.goalRevision, token.settled)
	}()
	request := requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "command": "abort", "success": true}))
	require.ErrorIs(t, <-result, ErrSemanticQueueFull)
	client.mu.Lock()
	require.NotNil(t, client.goalEvaluationCleanup)
	require.True(t, client.suppressNextEvaluatorSettled)
	client.mu.Unlock()
	close(release)
}

func testManagedGoalCleanupFence() store.ManagedRuntimeFence {
	return store.ManagedRuntimeFence{AgentID: "agent-a", PiSessionID: "session-a", SessionEpoch: 2, ProcessPID: 3, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
}

func TestRPCAbortDoesNotTouchEvaluatorCleanupOwner(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	fence := testManagedGoalCleanupFence()
	token := &managedGoalEvaluationToken{evaluationID: "evaluation", goalRevision: 1, settled: 3, fence: fence}
	owner := &managedGoalEvaluationCleanup{token: token}
	client.mu.Lock()
	client.goalEvaluationCleanup = owner
	client.suppressNextEvaluatorSettled = true
	client.mu.Unlock()
	result := make(chan error, 1)
	go func() { result <- client.Abort(context.Background()) }()
	request := requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "command": "abort", "success": true}))
	require.NoError(t, <-result)
	client.mu.Lock()
	require.Same(t, owner, client.goalEvaluationCleanup)
	require.True(t, client.suppressNextEvaluatorSettled)
	client.mu.Unlock()
}

func TestRPCManagedGoalCleanupFailsClosedOnAbortTimeout(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	fence := testManagedGoalCleanupFence()
	token := &managedGoalEvaluationToken{evaluationID: "evaluation", goalRevision: 1, settled: 3, fence: fence}
	client.mu.Lock()
	client.goalEvaluationCleanup = &managedGoalEvaluationCleanup{token: token}
	client.suppressNextEvaluatorSettled = true
	client.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- client.ReconcileManagedGoalEvaluation(ctx, fence, token.evaluationID, token.goalRevision, token.settled)
	}()
	_ = requireRequest(t, requests)
	require.ErrorIs(t, <-result, context.DeadlineExceeded)
	client.mu.Lock()
	require.NotNil(t, client.goalEvaluationCleanup)
	require.True(t, client.suppressNextEvaluatorSettled)
	client.mu.Unlock()
}

func TestRPCAbortPropagatesDeadlineAndEOF(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		stdinReader, stdinWriter := io.Pipe()
		stdoutReader, stdoutWriter := io.Pipe()
		client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		requests := readJSONLRequests(stdinReader)
		result := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			result <- client.Abort(ctx)
		}()
		_ = requireRequest(t, requests)
		require.ErrorIs(t, <-result, context.DeadlineExceeded)
	})
	t.Run("EOF", func(t *testing.T) {
		stdinReader, stdinWriter := io.Pipe()
		stdoutReader, stdoutWriter := io.Pipe()
		client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		requests := readJSONLRequests(stdinReader)
		result := make(chan error, 1)
		go func() { result <- client.Abort(context.Background()) }()
		_ = requireRequest(t, requests)
		require.NoError(t, stdoutWriter.Close())
		require.True(t, errors.Is(<-result, io.EOF))
	})
}
