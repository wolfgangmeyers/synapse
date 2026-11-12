package managed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// WJ-01, WJ-02: the tool response preserves guidance and requests a journal entry;
// invalid reason text must not change the binding.
func TestWorkflowJournalTransitionAndExitGuidance(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := subagentTestParent(t, db, "journal-guidance")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Journal flow"})
	require.NoError(t, err)
	canExit := true
	target, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Finish", WorkflowID: &wf.ID, EntryText: "Keep existing guidance", AllowExit: &canExit})
	require.NoError(t, err)
	source, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Start", WorkflowID: &wf.ID, Transitions: []store.WorkflowTransitionConfig{{TargetStateID: target.ID}}})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &source.ID, nil))
	tool := (&Manager{store: db}).embeddedWorkflowTransitionTool(embeddedOwner{binding: binding}, "workflow_transition")
	_, err = tool.Execute(ctx, "oversize", map[string]any{"action": "transition", "target_state": target.ID, "reason": strings.Repeat("é", 2049)}, nil)
	require.Error(t, err)
	current, err := db.GetManagedAgentActiveWorkflowState(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, source.ID, current.ID)
	result, err := tool.Execute(ctx, "transition", map[string]any{"action": "transition", "target_state": target.ID}, nil)
	require.NoError(t, err)
	text := result.Content[0].(ai.TextContent).Text
	require.Contains(t, text, "Successfully transitioned to workflow state: Finish.")
	require.Contains(t, text, "Keep existing guidance")
	require.Contains(t, text, "workflow_journal_note")
	require.Contains(t, text, "run_id")
	require.Contains(t, text, "target_event_id")
	require.NotContains(t, text, " and event_id=")
	retry, err := tool.Execute(ctx, "transition", map[string]any{"action": "transition", "target_state": target.ID}, nil)
	require.NoError(t, err)
	require.Contains(t, retry.Content[0].(ai.TextContent).Text, "Successfully transitioned")
	_, err = tool.Execute(ctx, "oversize-exit", map[string]any{"action": "exit", "reason": strings.Repeat("x", 4097)}, nil)
	require.Error(t, err)
	current, err = db.GetManagedAgentActiveWorkflowState(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, target.ID, current.ID)
	result, err = tool.Execute(ctx, "exit", map[string]any{"action": "exit"}, nil)
	require.NoError(t, err)
	text = result.Content[0].(ai.TextContent).Text
	require.Contains(t, text, "Successfully exited workflow state")
	require.Contains(t, text, "workflow_journal_note")
	require.Contains(t, text, "run_id")
	require.Contains(t, text, "target_event_id")
	require.NotContains(t, text, " and event_id=")
	_, err = tool.Execute(ctx, "exit", map[string]any{"action": "exit"}, nil)
	require.NoError(t, err)
}

// WJ-09: exit must not clear a newer binding captured after its snapshot.
func TestWorkflowJournalExitRejectsStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "exit.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := subagentTestParent(t, db, "journal-exit-fence")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "flow"})
	require.NoError(t, err)
	allowed, denied := true, false
	first, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "first", WorkflowID: &wf.ID, AllowExit: &allowed})
	require.NoError(t, err)
	second, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "second", WorkflowID: &wf.ID, AllowExit: &denied})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &first.ID, nil))
	mgr := &Manager{store: db}
	mgr.workflowTransitionCapturedTestHook = func() {
		require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &second.ID, nil))
	}
	_, err = mgr.embeddedWorkflowTransitionTool(embeddedOwner{binding: binding}, "workflow_transition").Execute(ctx, "exit", map[string]any{"action": "exit"}, nil)
	require.Error(t, err)
	current, err := db.GetManagedAgentActiveWorkflowState(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, second.ID, current.ID)
}

func TestWorkflowJournalNoteToolAdmissionAndTarget(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "notes.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := subagentTestParent(t, db, "journal-notes")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Notes"})
	require.NoError(t, err)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Working", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &state.ID, nil))
	tool := (&Manager{store: db}).embeddedWorkflowJournalNoteTool(embeddedOwner{binding: binding})
	_, err = tool.Execute(ctx, "blank", map[string]any{"source": "proactive", "text": "  "}, nil)
	require.Error(t, err)
	_, err = tool.Execute(ctx, "note", map[string]any{"source": "human-requested", "text": "Useful context"}, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(tool.Parameters)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "target_event_id")
	_, err = tool.Execute(ctx, "missing", map[string]any{"source": "prompted", "text": "Entry", "run_id": "missing", "target_event_id": "missing"}, nil)
	require.Error(t, err)
}

func TestWorkflowJournalNoteDiscoverableForRestrictedWorkflow(t *testing.T) {
	require.True(t, store.ValidWorkflowTools["workflow_journal_note"], "note tool must be selectable in workflow restrictions")
	require.True(t, store.IsToolAllowedInWorkflowState("workflow_journal_note", []string{"workflow_journal_note"}))
	require.False(t, store.IsToolAllowedInWorkflowState("workflow_journal_note", []string{"read"}))
}
