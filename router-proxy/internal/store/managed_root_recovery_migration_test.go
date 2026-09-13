package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedRootRecoveryMigrationUpgradesExistingManagedRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "045_managed_root_cold_recovery")]})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "legacy-cold", Name: "legacy-cold", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "legacy-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "legacy-cold-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "legacy-cold-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, before.Close())
	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, after.Close()) })
	stored, err := after.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, agent.PiSessionID, stored.PiSessionID)
	var recipeCount, actionCount int
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM managed_launch_recipes`).Scan(&recipeCount))
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM managed_root_recovery_actions`).Scan(&actionCount))
	require.Zero(t, recipeCount, "legacy rows remain fail-closed without a recipe")
	require.Zero(t, actionCount)
}
