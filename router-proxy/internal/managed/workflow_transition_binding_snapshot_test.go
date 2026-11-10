package managed

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestWorkflowTransitionRejectsSameStateReassignmentAfterCapture(t *testing.T) {
	ctx := context.Background()
	m, db, owner, _ := setupWorkflowAdminToolsFixture(t)
	defer db.Close()
	defer m.Close(ctx)
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "snapshot race"})
	require.NoError(t, err)
	b, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "B", WorkflowID: &wf.ID})
	require.NoError(t, err)
	a, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "A", WorkflowID: &wf.ID, Transitions: []store.WorkflowTransitionConfig{{TargetStateID: b.ID}}})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, owner.binding.AgentID, &wf.ID, &a.ID, nil))
	m.workflowTransitionCapturedTestHook = func() {
		require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, owner.binding.AgentID, &wf.ID, &a.ID, nil))
	}
	_, err = m.embeddedWorkflowTransitionTool(owner, "workflow_transition").Execute(ctx, "race", map[string]any{"action": "transition", "target_state": "B"}, nil)
	require.Error(t, err, "reassignment must invalidate captured transition")
	current, err := db.GetManagedAgentActiveWorkflowState(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, a.ID, current.ID)
}
