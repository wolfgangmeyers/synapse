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
			require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "type": "response", "success": true}))
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

			// The evaluator result arrives after the waiter has canceled. It must
			// not complete a new evaluation or wake ordinary parent activity.
			require.NoError(t, writeRPC(stdoutWriter, managedGoalEvaluationReceipt(fence, policy.EvaluatorID, policy.EvaluatorGoalRevision, policy.EvaluatorSettledSequence, "DONE", "late result")))
			select {
			case err := <-waitResult:
				t.Fatalf("late evaluator result produced another completion: %v", err)
			default:
			}
			client.mu.Lock()
			require.Nil(t, client.goalEvaluationToken)
			client.mu.Unlock()
			active, err := client.GetActiveTools(context.Background())
			require.NoError(t, err)
			require.Equal(t, wantTools, active)
		})
	}
}
