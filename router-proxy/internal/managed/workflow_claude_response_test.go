package managed

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// Claude emits text and then parks before its final. The real runtime must
// reset accounting while the checked response is still buffered.
func TestWorkflowClaudePartialResponseBeforeFinal(t *testing.T) {
	ctx := context.Background()
	r, p, _, cfg := claudeRuntimeFixture(t, `{"turns":[[{"text":"working now"},{"barrier":"partial-response"},{"text":"finished"}]]}`)
	_, err := p.db.SetManagedGoalDesiredState(ctx, p.binding.AgentID, true, "workflow")
	require.NoError(t, err)
	wf, err := p.db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "response"})
	require.NoError(t, err)
	state, err := p.db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "working", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, p.db.SetManagedAgentActiveWorkflowWithVariables(ctx, p.binding.AgentID, &wf.ID, &state.ID, nil))
	p.executionToken, err = p.db.EnterEmbeddedSDKExecution(ctx, p.binding, nil)
	require.NoError(t, err)
	lane := &managedAgentLane{}
	lane.consecutiveContinuations.Store(19)
	p.workflowNudge = &workflowNudgeObservation{lane: lane, p: p, limit: 20}
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, "nudge"); done <- err }()
	invocation := fakeBarrierInvocation(t, cfg.Cwd, "partial-response")
	require.Eventually(t, func() bool { return lane.consecutiveContinuations.Load() == 0 }, 5*time.Second, time.Millisecond)
	require.Zero(t, lane.workflowUnanswered.Load())
	select {
	case err := <-done:
		t.Fatalf("final arrived before barrier release: %v", err)
	default:
	}
	snap, err := p.db.ReopenEmbedded(ctx, p.binding)
	require.NoError(t, err)
	for _, entry := range snap.Entries {
		require.NotEqual(t, "assistant", entry.Kind, "partial must not need final append")
	}
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "barrier-"+invocation), nil, 0600))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("runtime did not finish")
	}
}
