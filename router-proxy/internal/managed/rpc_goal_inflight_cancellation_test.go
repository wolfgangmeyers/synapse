package managed

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestRPCClientStartTimeoutRetainsEvaluatorCleanupOwnership(t *testing.T) {
	fence := store.ManagedRuntimeFence{
		AgentID: "agent-a", PiSessionID: "session-a", SessionEpoch: 2, ProcessPID: 3,
		ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation",
		ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64),
	}
	policy := store.ManagedGoalPolicy{
		EvaluatorID: "evaluation", EvaluatorGoalRevision: 4, EvaluatorSettledSequence: 9,
		Config: store.ManagedGoalConfig{Goal: "finish the goal"},
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	startCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	startResult := make(chan error, 1)
	go func() { startResult <- client.StartManagedGoalEvaluation(startCtx, fence, policy) }()
	request := requireRequest(t, requests)
	require.Equal(t, "prompt", request["type"])
	require.Eventually(t, func() bool { return client.ManagedGoalEvaluationCleanupPending() }, time.Second, time.Millisecond)
	select {
	case err := <-startResult:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("Start did not observe its response deadline")
	}
	activity := make(chan map[string]any, 1)
	client.SetParentActivityEventHandler(func(event map[string]any) {
		if event["state"] == "idle" {
			activity <- event
		}
	})
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_start"}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	select {
	case event := <-activity:
		t.Fatalf("ambiguous Start timeout exposed evaluator activity: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}
	client.mu.Lock()
	require.NotNil(t, client.goalEvaluationCleanup)
	require.True(t, client.goalEvaluationCleanup.token.settledObserved)
	client.mu.Unlock()

	cleanupResult := make(chan error, 1)
	go func() {
		cleanupResult <- client.ReconcileManagedGoalEvaluation(context.Background(), fence, policy.EvaluatorID, policy.EvaluatorGoalRevision, policy.EvaluatorSettledSequence)
	}()
	abortRequest := requireRequest(t, requests)
	require.Equal(t, "abort", abortRequest["type"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": abortRequest["id"], "type": "response", "command": "abort", "success": true}))
	require.NoError(t, <-cleanupResult)
	require.False(t, client.ManagedGoalEvaluationCleanupPending())
	client.mu.Lock()
	require.False(t, client.suppressNextEvaluatorSettled)
	client.mu.Unlock()
}

func TestRPCClientInFlightGoalEvaluationCancellationRejectsLateResult(t *testing.T) {
	fence := store.ManagedRuntimeFence{
		AgentID: "agent-a", PiSessionID: "session-a", SessionEpoch: 2, ProcessPID: 3,
		ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation",
		ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64),
	}
	policy := store.ManagedGoalPolicy{
		EvaluatorID: "evaluation", EvaluatorGoalRevision: 4, EvaluatorSettledSequence: 9,
		Config: store.ManagedGoalConfig{Goal: "finish the goal"},
	}
	wantTools := []string{"bash", "edit", "goal.control", "read"}

	for _, test := range []struct {
		name      string
		waitCtx   func() (context.Context, context.CancelFunc)
		wantError error
	}{
		{name: "deadline", waitCtx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}, wantError: context.DeadlineExceeded},
		{name: "cancel", waitCtx: func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}, wantError: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdinReader, stdinWriter := io.Pipe()
			stdoutReader, stdoutWriter := io.Pipe()
			client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			requests := readJSONLRequests(stdinReader)
			client.mu.Lock()
			client.activeTools = append([]string(nil), wantTools...)
			client.mu.Unlock()

			started := make(chan struct{})
			startResult := make(chan error, 1)
			go func() {
				close(started)
				startResult <- client.StartManagedGoalEvaluation(context.Background(), fence, policy)
			}()
			<-started
			request := requireRequest(t, requests)
			require.Equal(t, "prompt", request["type"])
			require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "command": "prompt", "success": true, "data": map[string]any{}}))
			require.NoError(t, <-startResult)

			waitCtx, cancel := test.waitCtx()
			defer cancel()
			waitResult := make(chan error, 1)
			go func() {
				_, _, err := client.WaitForManagedGoalEvaluation(waitCtx)
				waitResult <- err
			}()
			if test.name == "cancel" {
				cancel()
			}
			select {
			case err := <-waitResult:
				require.ErrorIs(t, err, test.wantError)
			case <-time.After(time.Second):
				t.Fatal("in-flight evaluator did not observe cancellation")
			}
			secondPolicy := policy
			secondPolicy.EvaluatorID = "replacement-evaluation"
			require.Error(t, client.StartManagedGoalEvaluation(context.Background(), fence, secondPolicy), "cleanup ownership must block a replacement evaluator")

			// The waiter can cancel after prompt preflight without Pi admitting an
			// evaluator turn. Reconcile only after Pi's typed Abort response and its
			// stdout-order barrier; the next ordinary idle must remain visible.
			cleanupResult := make(chan error, 1)
			go func() {
				cleanupResult <- client.ReconcileManagedGoalEvaluation(context.Background(), fence, policy.EvaluatorID, policy.EvaluatorGoalRevision, policy.EvaluatorSettledSequence)
			}()
			abortRequest := requireRequest(t, requests)
			require.Equal(t, "abort", abortRequest["type"])
			require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": abortRequest["id"], "type": "response", "command": "abort", "success": true}))
			require.NoError(t, <-cleanupResult)

			activity := make(chan map[string]any, 1)
			client.SetParentActivityEventHandler(func(event map[string]any) { activity <- event })
			require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
			select {
			case event := <-activity:
				require.Equal(t, "idle", event["state"])
			case <-time.After(time.Second):
				t.Fatal("ordinary settled event was swallowed after no-admission cleanup")
			}

			// A late evaluator receipt cannot complete a new evaluation or wake
			// ordinary parent activity.
			require.NoError(t, writeRPC(stdoutWriter, managedGoalEvaluationReceipt(fence, policy.EvaluatorID, policy.EvaluatorGoalRevision, policy.EvaluatorSettledSequence, "DONE", "late result")))
			select {
			case err := <-waitResult:
				t.Fatalf("late evaluator result produced another completion: %v", err)
			default:
			}
			client.mu.Lock()
			goalToken := client.goalEvaluationToken
			cleanupOwner := client.goalEvaluationCleanup
			suppressed := client.suppressNextEvaluatorSettled
			client.mu.Unlock()
			require.Nil(t, goalToken)
			require.Nil(t, cleanupOwner)
			require.False(t, suppressed)
			active, err := client.GetActiveTools(context.Background())
			require.NoError(t, err)
			require.Equal(t, wantTools, active)
		})
	}
}
