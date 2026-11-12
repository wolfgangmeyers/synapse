package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestWorkflowJournalReplayKeepsOriginalGuidance(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "replay.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := subagentTestParent(t, db, "replay-guidance")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Replay"})
	require.NoError(t, err)
	implicit := true
	target, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Target", WorkflowID: &wf.ID, EntryText: "original", AllowImplicitReturn: &implicit})
	require.NoError(t, err)
	source, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Source", WorkflowID: &wf.ID, Transitions: []store.WorkflowTransitionConfig{{TargetStateID: target.ID}}})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &source.ID, nil))
	tool := (&Manager{store: db}).embeddedWorkflowTransitionTool(embeddedOwner{binding: binding}, "workflow_transition")
	args := map[string]any{"action": "transition", "target_state": target.ID}
	first, err := tool.Execute(ctx, "same", args, nil)
	require.NoError(t, err)
	_, err = db.UpdateWorkflowState(ctx, target.ID, store.WorkflowStateRequest{Name: "Changed", WorkflowID: &wf.ID, EntryText: "changed"})
	require.NoError(t, err)
	second, err := tool.Execute(ctx, "same", args, nil)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestWorkflowJournalToolsReplayAfterAgentDeletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "deleted.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := subagentTestParent(t, db, "replay-deleted")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Replay deletion"})
	require.NoError(t, err)
	target, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Target", WorkflowID: &wf.ID})
	require.NoError(t, err)
	source, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Source", WorkflowID: &wf.ID, Transitions: []store.WorkflowTransitionConfig{{TargetStateID: target.ID}}})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &source.ID, nil))
	mgr := makeSubagentTestManager(t, db, nil, 4)
	defer mgr.Close(ctx)
	transition := mgr.embeddedWorkflowTransitionTool(embeddedOwner{binding: binding}, "workflow_transition")
	note := mgr.embeddedWorkflowJournalNoteTool(embeddedOwner{binding: binding})
	transitionArgs := map[string]any{"action": "transition", "target_state": target.ID}
	noteArgs := map[string]any{"source": "proactive", "text": "important note"}
	_, err = transition.Execute(ctx, "transition", transitionArgs, nil)
	require.NoError(t, err)
	_, err = note.Execute(ctx, "note", noteArgs, nil)
	require.NoError(t, err)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	conn, err := raw.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	require.NoError(t, err)
	var foreignKeys int
	require.NoError(t, conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys))
	require.Equal(t, 1, foreignKeys)
	_, err = mgr.Terminate(ctx, TerminateRequest{
		AgentID: binding.AgentID, Method: "DELETE",
		CanonicalPath: "/v1/managed-agents/" + binding.AgentID,
		RequestID: "123e4567-e89b-42d3-a456-426614174299",
		CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CleanupWorktree: false,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var count int
		return conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id=?`, binding.AgentID).Scan(&count) == nil && count == 0
	}, 3*time.Second, time.Millisecond)
	for _, query := range []struct {
		name string
		sql  string
		want int
	}{
		{"workflow_journal_runs", `SELECT COUNT(*) FROM workflow_journal_runs WHERE agent_id=?`, 0},
		{"workflow_journal_events", `SELECT COUNT(*) FROM workflow_journal_events e JOIN workflow_journal_runs r ON r.id=e.run_id WHERE r.agent_id=?`, 0},
		// Operation receipts survive deletion through their expiry window, but
		// replay still requires a live agent and must not authorize a tool call.
		{"workflow_journal_receipts", `SELECT COUNT(*) FROM workflow_journal_receipts WHERE agent_id=?`, 2},
	} {
		var count int
		require.NoError(t, conn.QueryRowContext(ctx, query.sql, binding.AgentID).Scan(&count))
		require.Equal(t, query.want, count, "unexpected %s count after agent deletion", query.name)
	}
	t.Run("transition", func(t *testing.T) {
		_, err := transition.Execute(ctx, "transition", transitionArgs, nil)
		require.NoError(t, err, "deleted agent must replay a transition receipt")
	})
	t.Run("note", func(t *testing.T) {
		_, err := note.Execute(ctx, "note", noteArgs, nil)
		require.Error(t, err, "deleted agent must not replay a note receipt")
	})
}
