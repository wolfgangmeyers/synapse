package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSubagentCannotReplayWorkflowTransitionReceipt(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "transition-receipt.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "receipt-parent")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Receipt ownership"})
	require.NoError(t, err)
	target, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Target", WorkflowID: &wf.ID})
	require.NoError(t, err)
	source, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Source", WorkflowID: &wf.ID, Transitions: []store.WorkflowTransitionConfig{{TargetStateID: target.ID}}})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, parent.AgentID, &wf.ID, &source.ID, nil))
	mgr := &Manager{store: db}
	args := map[string]any{"action": "transition", "target_state": target.ID}
	tool := mgr.embeddedWorkflowTransitionTool(embeddedOwner{binding: parent}, "workflow_transition")
	first, err := tool.Execute(ctx, "shared-transition-id", args, nil)
	require.NoError(t, err)
	// Same owner can replay its receipt.
	replay, err := tool.Execute(ctx, "shared-transition-id", args, nil)
	require.NoError(t, err)
	require.Equal(t, first, replay)

	childID := "receipt-child"
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: childID, ParentAgentID: parent.AgentID, Status: "queued"}))
	childTool := mgr.embeddedWorkflowTransitionTool(embeddedOwner{binding: store.EmbeddedBinding{AgentID: childID}}, "workflow_transition")
	_, err = childTool.Execute(ctx, "shared-transition-id", args, nil)
	require.ErrorContains(t, err, "subagents cannot invoke workflow state transitions")
}

func TestSubagentCannotReplayWorkflowJournalNoteReceipt(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "note-receipt.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "note-receipt-parent")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Note receipt"})
	require.NoError(t, err)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Active", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, parent.AgentID, &wf.ID, &state.ID, nil))
	mgr := &Manager{store: db}
	args := map[string]any{"source": "proactive", "text": "Important context"}
	tool := mgr.embeddedWorkflowJournalNoteTool(embeddedOwner{binding: parent})
	first, err := tool.Execute(ctx, "shared-note-id", args, nil)
	require.NoError(t, err)
	// Same owner can replay its receipt.
	replay, err := tool.Execute(ctx, "shared-note-id", args, nil)
	require.NoError(t, err)
	require.Equal(t, first, replay)

	childID := "note-receipt-child"
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: childID, ParentAgentID: parent.AgentID, Status: "queued"}))
	childTool := mgr.embeddedWorkflowJournalNoteTool(embeddedOwner{binding: store.EmbeddedBinding{AgentID: childID}})
	_, err = childTool.Execute(ctx, "shared-note-id", args, nil)
	require.ErrorContains(t, err, "subagents cannot write workflow journals")
}
