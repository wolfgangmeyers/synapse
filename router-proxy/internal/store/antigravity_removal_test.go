package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// AGR-007: real SQLite legacy rows, replacement and rollback at the store boundary.
func TestAntigravityRemovalLegacySnapshotAtomicRefresh(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	_, err = durable.db.Exec(`INSERT OR REPLACE INTO managed_provider_status(provider,label,authenticated,status,updated_at) VALUES ('antigravity','Google Antigravity',1,'authenticated','2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	_, err = durable.db.Exec(`INSERT INTO managed_provider_models(provider,model_id,name,available,updated_at) VALUES ('antigravity','gemini-3.8-flash','Legacy',1,'2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	providers := []ManagedProviderStatus{}
	models := []ManagedProviderModel{}
	for _, provider := range []string{"google", "google-vertex", "openai-codex", "cursor"} {
		providers = append(providers, ManagedProviderStatus{Provider: provider, Label: managedProviderLabels[provider], Authenticated: true, Status: "authenticated"})
		models = append(models, ManagedProviderModel{Provider: provider, ID: "fixture-model", Name: "Fixture", Available: true})
	}
	assertLegacy := func(want int) {
		for _, table := range []string{"managed_provider_status", "managed_provider_models"} {
			var count int
			require.NoError(t, durable.db.QueryRow("SELECT count(*) FROM "+table+" WHERE provider='antigravity'").Scan(&count))
			require.Equal(t, want, count)
		}
	}
	before, err := durable.ManagedProviderSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, before.Providers, 4, "legacy rows must be hidden even before refresh")
	invalid := append([]ManagedProviderStatus(nil), providers...)
	invalid[3] = ManagedProviderStatus{Provider: "antigravity", Label: "Google Antigravity", Status: "signed_out"}
	duplicate := append([]ManagedProviderStatus(nil), providers...)
	duplicate[3] = duplicate[0]
	for _, statuses := range [][]ManagedProviderStatus{providers[:3], invalid, duplicate} {
		_, err := durable.CommitManagedProviderSnapshot(ctx, statuses, models)
		require.Error(t, err)
		after, err := durable.ManagedProviderSnapshot(ctx)
		require.NoError(t, err)
		require.Equal(t, before, after)
		assertLegacy(1)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = durable.CommitManagedProviderSnapshot(canceled, providers, models)
	require.Error(t, err)
	assertLegacy(1)
	// Fail after stale-row deletion and provider upserts to prove actual rollback.
	_, err = durable.db.Exec(`CREATE TRIGGER reject_fixture_model BEFORE INSERT ON managed_provider_models BEGIN SELECT RAISE(ABORT,'deterministic snapshot failure'); END`)
	require.NoError(t, err)
	_, err = durable.CommitManagedProviderSnapshot(ctx, providers, models)
	require.Error(t, err)
	assertLegacy(1)
	after, err := durable.ManagedProviderSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = durable.db.Exec(`DROP TRIGGER reject_fixture_model`)
	require.NoError(t, err)
	for revision := int64(1); revision <= 2; revision++ {
		got, err := durable.CommitManagedProviderSnapshot(ctx, providers, models)
		require.NoError(t, err)
		require.Equal(t, revision, got)
		assertLegacy(0)
		after, err := durable.ManagedProviderSnapshot(ctx)
		require.NoError(t, err)
		require.Len(t, after.Providers, 4)
		require.ElementsMatch(t, models, after.Models)
	}
}

// AGR-004: omission inherits each supported parent, not a universal Vertex override.
func TestAntigravityRemovalChildInheritance(t *testing.T) {
	for _, provider := range []string{"google", "google-vertex", "openai-codex", ""} {
		t.Run("parent-"+provider, func(t *testing.T) {
			ctx := context.Background()
			durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
			require.NoError(t, err)
			defer durable.Close()
			parent := subagentParent(t, durable, "inherit-parent")
			model := "inherited-model"
			if provider == "" {
				model = ""
			}
			_, err = durable.db.Exec(`UPDATE managed_agents SET selected_model_provider=?,selected_model_id=? WHERE agent_id=?`, provider, model, parent.AgentID)
			require.NoError(t, err)
			childID := "subagent-inherited"
			require.NoError(t, durable.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: childID, ParentAgentID: parent.AgentID, Description: "inherit", SubagentType: "general-purpose", Status: "queued", MaxTurns: 1}))
			var gotProvider, gotModel string
			require.NoError(t, durable.db.QueryRow(`SELECT selected_model_provider,selected_model_id FROM managed_agents WHERE agent_id=?`, childID).Scan(&gotProvider, &gotModel))
			if provider == "" {
				provider, model = "google-vertex", "gemini-3.8-flash"
			}
			require.Equal(t, provider, gotProvider)
			require.Equal(t, model, gotModel)
		})
	}
}
