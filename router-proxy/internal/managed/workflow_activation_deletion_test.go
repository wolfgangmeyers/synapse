package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWorkflowAssignReplayAfterTargetDeletion(t *testing.T) {
	ctx := context.Background()
	mgr, s, owner, root := setupWorkflowAdminToolsFixture(t)
	defer s.Close()
	defer mgr.Close(ctx)
	target := subagentTestParent(t, s, "activation-target")
	state, err := s.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "original"})
	require.NoError(t, err)
	tool := mgr.embeddedWorkflowAssignTool(owner)
	on := map[string]any{"agent_id": target.AgentID, "action": "activate", "workflow_state_id": state.ID}
	off := map[string]any{"agent_id": target.AgentID, "action": "deactivate"}
	_, err = tool.Execute(ctx, "on", on, nil)
	require.NoError(t, err)
	_, err = tool.Execute(ctx, "off", off, nil)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", filepath.Join(root, "workflow_admin_tools_test.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`PRAGMA foreign_keys=ON`)
	require.NoError(t, err)
	var foreignKeys int
	require.NoError(t, db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
	require.Equal(t, 1, foreignKeys)
	_, err = mgr.Terminate(ctx, TerminateRequest{
		AgentID: target.AgentID, Method: "DELETE",
		CanonicalPath: "/v1/managed-agents/" + target.AgentID,
		RequestID: "123e4567-e89b-42d3-a456-426614174299",
		CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var count int
		return db.QueryRow(`SELECT COUNT(*) FROM managed_agents WHERE agent_id=?`, target.AgentID).Scan(&count) == nil && count == 0
	}, 3*time.Second, time.Millisecond)
	_, err = tool.Execute(ctx, "on", on, nil)
	var missing *store.ManagedAgentNotFoundError
	require.ErrorAs(t, err, &missing)
	_, err = tool.Execute(ctx, "off", off, nil)
	require.ErrorAs(t, err, &missing)
	// Deletion assertions are scoped to the target agent's journal records.
	var targetRuns, targetEvents, targetReceipts int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM workflow_journal_runs WHERE agent_id=?`, target.AgentID).Scan(&targetRuns))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM workflow_journal_events e JOIN workflow_journal_runs r ON r.id=e.run_id WHERE r.agent_id=?`, target.AgentID).Scan(&targetEvents))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM workflow_journal_receipts WHERE agent_id=?`, target.AgentID).Scan(&targetReceipts))
	require.Zero(t, targetRuns)
	require.Zero(t, targetEvents)
	require.Equal(t, 2, targetReceipts)
}
