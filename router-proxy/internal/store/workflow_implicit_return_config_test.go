package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// IR-001, IR-011: definition flags survive each supported copy path; old bundles default off.
func TestImplicitReturnDefinitionPortability(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "source"})
	require.NoError(t, err)
	yes := true
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{WorkflowID: &wf.ID, Name: "enabled", AllowImplicitReturn: &yes})
	require.NoError(t, err)
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{WorkflowID: &wf.ID, Name: "disabled"})
	require.NoError(t, err)
	check := func(states []WorkflowState) {
		t.Helper()
		values := map[string]bool{}
		for _, st := range states {
			values[st.Name] = st.AllowImplicitReturn
		}
		require.Equal(t, map[string]bool{"enabled": true, "disabled": false}, values)
	}
	dup, err := s.DuplicateWorkflow(ctx, wf.ID, "", "duplicate")
	require.NoError(t, err)
	states, err := s.ListWorkflowStatesByWorkflow(ctx, dup.ID)
	require.NoError(t, err)
	check(states)
	bundle, err := s.ExportWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	data, err := json.Marshal(bundle)
	require.NoError(t, err)
	require.NotContains(t, string(data), "predecessor")
	bundle.Workflow.Name = "imported"
	_, states, err = s.ImportWorkflow(ctx, *bundle)
	require.NoError(t, err)
	check(states)
	all, err := s.ExportAllWorkflows(ctx)
	require.NoError(t, err)
	require.NoError(t, s.ReplaceAllWorkflows(ctx, all.Workflows))
	all, err = s.ExportAllWorkflows(ctx)
	require.NoError(t, err)
	for _, b := range all.Workflows {
		values := map[string]bool{}
		for _, st := range b.States {
			values[st.Name] = st.AllowImplicitReturn
		}
		require.Equal(t, map[string]bool{"enabled": true, "disabled": false}, values)
	}
	var old WorkflowStateExport
	require.NoError(t, json.Unmarshal([]byte(`{"name":"old"}`), &old))
	require.False(t, old.AllowImplicitReturn)
}

// IR-001: migration preserves populated definitions and defaults the new flag off.
func TestImplicitReturnMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	legacy, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "129_workflow_allow_implicit_return")]})
	require.NoError(t, err)
	state, err := legacy.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "legacy"})
	// Old schema cannot use the updated state writer, so seed the old schema directly.
	_ = state
	require.Error(t, err)
	_, err = legacy.db.ExecContext(ctx, `INSERT INTO workflow_states (id, name, description, entry_text, created_at, updated_at) VALUES ('legacy-id', 'legacy', '', '', 'now', 'now')`)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())
	upgraded, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer upgraded.Close()
	got, err := upgraded.GetWorkflowState(ctx, "legacy-id")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.AllowImplicitReturn)
}
