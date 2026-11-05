package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// RNT-006: old persisted defaults and overrides must not revive a removed tool.
func TestWorkerToolRemovalSavedDefaultsReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	require.NoError(t, cloneLocalTemplateDB(t, path))
	s, err := Open(ctx, path, Options{Migrations: []Migration{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	seedTestAgent(t, s, ctx, "legacy-worker")
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO managed_tool_defaults(tool_name,category,enabled,mcp_server_id,updated_at) VALUES('mecha_worker','Messaging & Routing',1,NULL,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_agent_tools(agent_id,tool_name,enabled,updated_at) VALUES('legacy-worker','mecha_worker',1,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	require.NoError(t, s.SetToolDefault(ctx, "bash", false))
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{Migrations: []Migration{}})
	require.NoError(t, err)
	defaults, err := s.GetToolDefaults(ctx)
	require.NoError(t, err)
	effective, err := s.GetAgentTools(ctx, "legacy-worker")
	require.NoError(t, err)
	for _, tools := range [][]ToolDefinition{defaults, effective} {
		names := map[string]ToolDefinition{}
		for _, tool := range tools {
			names[tool.ToolName] = tool
		}
		require.NotContains(t, names, "mecha_worker")
		require.Contains(t, names, "bash")
		require.False(t, names["bash"].Enabled, "reopen preserves unrelated saved defaults")
		require.True(t, names["mecha_mailbox_send"].Enabled)
		require.Contains(t, names, "worker_spawn")
		require.False(t, names["worker_spawn"].Enabled)
	}
	require.ErrorContains(t, s.SetToolDefault(ctx, "mecha_worker", true), "unknown tool")
	require.ErrorContains(t, s.SetAgentTool(ctx, "legacy-worker", "mecha_worker", true), "unknown tool")
	for _, tool := range CanonicalWorkflowTools {
		require.NotEqual(t, "mecha_worker", tool.ID)
	}
	require.False(t, ValidWorkflowTools["mecha_worker"])
}

// Keep the inert token: [] would widen a restricted state to unrestricted tools.
func TestWorkerToolRemovalWorkflowRoundtrip(t *testing.T) {
	for _, allowed := range [][]string{{"mecha_worker"}, {"read", "mecha_worker", "mecha_mailbox_send"}} {
		t.Run(allowed[0], func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Legacy restrictions"})
			require.NoError(t, err)
			state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{WorkflowID: &wf.ID, Name: "Restricted", AllowedTools: allowed})
			require.NoError(t, err)
			require.Equal(t, allowed, state.AllowedTools)
			updated, err := s.UpdateWorkflowState(ctx, state.ID, WorkflowStateRequest{Name: state.Name, EntryText: "Updated guidance", AllowedTools: state.AllowedTools})
			require.NoError(t, err)
			require.Equal(t, allowed, updated.AllowedTools)
			bundle, err := s.ExportWorkflow(ctx, wf.ID)
			require.NoError(t, err)
			require.Len(t, bundle.States, 1)
			require.Equal(t, allowed, bundle.States[0].AllowedTools)
			wire, err := json.Marshal(bundle)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(wire, bundle))
			imported, states, err := s.ImportWorkflow(ctx, *bundle)
			require.NoError(t, err)
			require.Len(t, states, 1)
			require.Equal(t, allowed, states[0].AllowedTools)
			reexported, err := s.ExportWorkflow(ctx, imported.ID)
			require.NoError(t, err)
			require.Len(t, reexported.States, 1)
			require.Equal(t, allowed, reexported.States[0].AllowedTools)
			require.NotEmpty(t, reexported.States[0].AllowedTools)
		})
	}
}
