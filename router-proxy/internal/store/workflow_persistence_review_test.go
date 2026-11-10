package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// IR-001 and IR-010: upgrading an existing active binding must not infer history.
func TestImplicitReturnUpgradeActiveBinding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	legacy, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "129_workflow_allow_implicit_return")]})
	require.NoError(t, err)
	_, err = legacy.db.ExecContext(ctx, `INSERT INTO workflows (id,name,description,created_at,updated_at) VALUES ('wf','workflow','','now','now')`)
	require.NoError(t, err)
	_, err = legacy.db.ExecContext(ctx, `INSERT INTO workflow_states (id,workflow_id,name,description,entry_text,created_at,updated_at) VALUES ('st','wf','state','','','now','now')`)
	require.NoError(t, err)
	_, err = legacy.db.ExecContext(ctx, `INSERT INTO managed_agents (agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at,active_workflow_id,active_workflow_state_id,workflow_variables_json) VALUES ('agent','agent','/tmp','p','m','running','running','idle',1,1,'hash','now','now','wf','st','{"key":"value"}')`)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())
	for i := 0; i < 2; i++ {
		upgraded, err := Open(ctx, path, Options{})
		require.NoError(t, err)
		var workflow, state, vars string
		var predecessor sql.NullString
		var generation int64
		require.NoError(t, upgraded.db.QueryRowContext(ctx, `SELECT active_workflow_id,active_workflow_state_id,workflow_variables_json,workflow_predecessor_state_id,workflow_binding_generation FROM managed_agents WHERE agent_id='agent'`).Scan(&workflow, &state, &vars, &predecessor, &generation))
		require.Equal(t, "wf", workflow)
		require.Equal(t, "st", state)
		require.JSONEq(t, `{"key":"value"}`, vars)
		require.False(t, predecessor.Valid)
		require.Zero(t, generation)
		got, err := upgraded.GetWorkflowState(ctx, "st")
		require.NoError(t, err)
		require.False(t, got.AllowImplicitReturn)
		require.NoError(t, upgraded.Close())
	}
}

func TestImplicitReturnMigrationNumbersUnique(t *testing.T) {
	seen := map[string]string{}
	for _, migration := range DefaultMigrations() {
		// Historical duplicate 124 predates this feature; prohibit new collisions.
		if len(migration.ID) < 4 || migration.ID[:3] < "128" {
			continue
		}
		number := migration.ID[:3]
		require.Empty(t, seen[number], "migration %s collides with %s", migration.ID, seen[number])
		seen[number] = migration.ID
	}
}
