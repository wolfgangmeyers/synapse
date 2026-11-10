package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestWorkflowImplicitReturnTool(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := subagentTestParent(t, db, "return-tool")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "flow"})
	require.NoError(t, err)
	enabled := true
	b, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "B", WorkflowID: &wf.ID, AllowImplicitReturn: &enabled})
	require.NoError(t, err)
	a, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "A", WorkflowID: &wf.ID, Transitions: []store.WorkflowTransitionConfig{{TargetStateID: b.ID}}})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &a.ID, nil))
	mgr := &Manager{store: db}
	owner := embeddedOwner{binding: binding, identity: "fixture:return-tool"}
	tool := mgr.embeddedWorkflowTransitionTool(owner, "mecha_workflow_transition")
	alias := mgr.embeddedWorkflowTransitionTool(owner, "workflow_transition")
	_, err = tool.Execute(ctx, "invalid", map[string]any{"action": "transition", "target_state": " Missing "}, nil)
	require.EqualError(t, err, `transition to "Missing" is not permitted from state "A" (allowed targets: B)`)
	result, err := tool.Execute(ctx, "one", map[string]any{"action": "transition", "target_state": " B "}, nil)
	require.NoError(t, err)
	require.Contains(t, result.Content[0].(ai.TextContent).Text, "- A")
	_, err = tool.Execute(ctx, "invalid-return", map[string]any{"action": "transition", "target_state": "missing"}, nil)
	require.EqualError(t, err, `transition to "missing" is not permitted from state "B" (allowed targets: A)`)
	_, err = alias.Execute(ctx, "two", map[string]any{"action": "transition", "target_state": "a"}, nil)
	require.NoError(t, err)
	current, err := db.GetManagedAgentActiveWorkflowState(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, a.ID, current.ID)
}

func TestRejectedWorkflowExitKeepsReminders(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := subagentTestParent(t, db, "exit-preserve")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "flow"})
	require.NoError(t, err)
	locked := false
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "locked", WorkflowID: &wf.ID, AllowExit: &locked, Reminders: []store.WorkflowReminderConfig{{ID: "rem", Type: store.WorkflowReminderOneShot, Body: "keep", DelaySeconds: 60}}})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &state.ID, nil))
	require.NoError(t, db.SpawnWorkflowStateReminders(ctx, binding.AgentID, state))
	mgr := &Manager{store: db}
	tool := mgr.embeddedWorkflowTransitionTool(embeddedOwner{binding: binding}, "workflow_transition")
	_, err = tool.Execute(ctx, "exit", map[string]any{"action": "exit"}, nil)
	require.Error(t, err)
	reminders, err := db.Reminders(ctx)
	require.NoError(t, err)
	require.Len(t, reminders, 1)
}
