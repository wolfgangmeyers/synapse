package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration131PreservesRecipesAndConstraints(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, "131_optional_retired_rpc_recipe")
	before, err := Open(ctx, path, Options{Migrations: migrations[:idx]})
	require.NoError(t, err)
	fence := seedManagedRootRecoveryAgent(t, before)
	legacy := managedRootRecoveryRecipeFixture()
	legacy.AntigravityExtension = "/managed/ag.ts"
	legacy, err = ManagedLaunchRecipeDigest(legacy)
	require.NoError(t, err)
	require.NoError(t, before.PutManagedLaunchRecipe(ctx, fence.AgentID, legacy))
	var oldRow string
	require.NoError(t, before.db.QueryRow(`SELECT json_array(agent_id,pi_executable,session_root,cwd,provider,model_id,mecha_rpc_extension,pi_subagents_extension,cursor_extension,test_extensions_json,environment_json,recipe_sha256,created_at,updated_at,antigravity_extension) FROM managed_launch_recipes WHERE agent_id=?`, fence.AgentID).Scan(&oldRow))
	var oldTriggers string
	require.NoError(t, before.db.QueryRow(`SELECT coalesce(group_concat(name || ':' || sql, char(10)), '') FROM (SELECT name, sql FROM sqlite_master WHERE type='trigger' ORDER BY name)`).Scan(&oldTriggers))
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()
	var newRow string
	require.NoError(t, after.db.QueryRow(`SELECT json_array(agent_id,pi_executable,session_root,cwd,provider,model_id,mecha_rpc_extension,pi_subagents_extension,cursor_extension,test_extensions_json,environment_json,recipe_sha256,created_at,updated_at,antigravity_extension) FROM managed_launch_recipes WHERE agent_id=?`, fence.AgentID).Scan(&newRow))
	require.Equal(t, oldRow, newRow)
	agent, err := after.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.NotNil(t, agent.LaunchRecipe)
	require.Equal(t, legacy.SHA256, agent.LaunchRecipe.SHA256)
	require.NoError(t, ValidateManagedLaunchRecipe(*agent.LaunchRecipe))
	var triggers string
	var index, fk int
	require.NoError(t, after.db.QueryRow(`SELECT coalesce(group_concat(name || ':' || sql, char(10)), '') FROM (SELECT name, sql FROM sqlite_master WHERE type='trigger' ORDER BY name)`).Scan(&triggers))
	require.Equal(t, oldTriggers, triggers)
	require.NoError(t, after.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='managed_launch_recipes_hash_idx'`).Scan(&index))
	require.Equal(t, 1, index)
	require.NoError(t, after.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	require.Equal(t, 1, fk)
	rows, err := after.db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	// Database constraints remain enforced independently of Go validation.
	_, err = after.db.Exec(`UPDATE managed_launch_recipes SET mecha_rpc_extension=? WHERE agent_id=?`, strings.Repeat("x", 4097), fence.AgentID)
	require.Error(t, err)
	_, err = after.db.Exec(`UPDATE managed_launch_recipes SET mecha_rpc_extension=NULL WHERE agent_id=?`, fence.AgentID)
	require.Error(t, err)
	_, err = after.db.Exec(`UPDATE managed_launch_recipes SET pi_subagents_extension='' WHERE agent_id=?`, fence.AgentID)
	require.Error(t, err)
	_, err = after.db.Exec(`UPDATE managed_launch_recipes SET recipe_sha256='bad' WHERE agent_id=?`, fence.AgentID)
	require.Error(t, err)
	_, err = after.db.Exec(`UPDATE managed_launch_recipes SET agent_id='missing-parent' WHERE agent_id=?`, fence.AgentID)
	require.Error(t, err)

	empty := legacy
	empty.MechaRPCExtension = ""
	empty, err = ManagedLaunchRecipeDigest(empty)
	require.NoError(t, err)
	require.NoError(t, after.runImmediate(ctx, func() error {
		return replaceManagedLaunchRecipeTx(ctx, after.db, fence.AgentID, empty, "2026-01-01T00:00:00.000Z")
	}))
	reloaded, err := after.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "", reloaded.LaunchRecipe.MechaRPCExtension)
	require.Equal(t, empty.SHA256, reloaded.LaunchRecipe.SHA256)
	require.NoError(t, ValidateManagedLaunchRecipe(*reloaded.LaunchRecipe))
	// Verify the parent relationship still specifies cascade.
	var table, from, to, onUpdate, onDelete, match string
	var id, seq int
	require.NoError(t, after.db.QueryRow(`PRAGMA foreign_key_list(managed_launch_recipes)`).Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match))
	require.Equal(t, "managed_agents", table)
	require.Equal(t, "agent_id", from)
	require.Equal(t, "agent_id", to)
	require.Equal(t, "CASCADE", onDelete)
}
