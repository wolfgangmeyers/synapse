package managed

import (
	"context"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// AGR-006: exercise persisted historical recipes through the real SQLite store
// and supervisor, replacing only the process/RPC boundary with local fakes.
func TestColdRecoveryLegacyAntigravityRecipe(t *testing.T) {
	for _, provider := range []string{"google", "google-vertex", "openai-codex", "cursor"} {
		for _, variant := range []string{"extension only", "other configuration mismatch", "invalid old hash"} {
			t.Run(provider+"/"+variant, func(t *testing.T) {
				ctx := context.Background()
				manager, durable, agent, _, databasePath := setupColdRecovery(t, nil, nil, nil, nil, nil)
				bindColdRecoverySession(t, manager, durable, databasePath, agent.AgentID)
				db := installColdRecoveryTrigger(t, databasePath, "SELECT 1")
				defer db.Close()
				agent.SelectedModel = store.ManagedModel{Provider: provider, ID: "supported-test-model"}
				_, err := db.Exec(`UPDATE managed_agents SET selected_model_provider=?,selected_model_id=? WHERE agent_id=?`, provider, agent.SelectedModel.ID, agent.AgentID)
				require.NoError(t, err)
				recipe, err := manager.managedLaunchRecipe(agent)
				require.NoError(t, err)
				recipe.AntigravityExtension = "/retired/antigravity/index.ts"
				if variant == "other configuration mismatch" {
					recipe.CursorExtension = "/different/cursor/index.ts"
				}
				recipe, err = store.ManagedLaunchRecipeDigest(recipe)
				require.NoError(t, err)
				require.NoError(t, store.ValidateManagedLaunchRecipe(recipe))
				originalHash := recipe.SHA256
				if variant == "invalid old hash" {
					recipe.SHA256 = strings.Repeat("0", 64)
				}
				_, err = db.Exec(`UPDATE managed_launch_recipes SET provider=?,model_id=?,antigravity_extension=?,cursor_extension=?,recipe_sha256=? WHERE agent_id=?`, provider, recipe.ModelID, recipe.AntigravityExtension, recipe.CursorExtension, recipe.SHA256, agent.AgentID)
				require.NoError(t, err)
				manager.config.TestOnlyEnvironment = append(manager.config.TestOnlyEnvironment, "MECHA_ANTIGRAVITY_EXTENSION=/retired/env.ts", "ROUTER_MANAGED_ANTIGRAVITY_EXTENSION=/retired/router-env.ts")
				require.False(t, manager.config.AllowManagedLaunchRecipeMismatch)
				factory := manager.processFactory.(*launchFactory)
				err = manager.RecoverManagedRootColdStarts(ctx)
				if variant == "invalid old hash" {
					require.ErrorContains(t, err, "managed launch recipe hash is invalid")
					require.Zero(t, factory.startCalls)
					return
				}
				require.NoError(t, err)
				current, err := durable.ManagedAgent(ctx, agent.AgentID)
				require.NoError(t, err)
				if variant == "other configuration mismatch" {
					require.Equal(t, "RECOVERY_RECIPE_MISMATCH", current.LastErrorCode)
					require.Zero(t, factory.startCalls)
					return
				}
				require.Equal(t, "running", current.LifecycleState)
				require.Empty(t, current.LastErrorCode)
				require.Equal(t, 1, factory.startCalls)
				require.Equal(t, provider, factory.spec.Provider)
				require.Equal(t, agent.SelectedModel.ID, factory.spec.ModelID)
				require.Equal(t, ResumeExisting, factory.spec.LaunchMode)
				require.Equal(t, agent.PiSessionID, factory.spec.SessionID)
				require.Empty(t, factory.spec.AntigravityExtension)
				for _, value := range factory.spec.Environment {
					require.NotContains(t, value, "ANTIGRAVITY")
				}
				action, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
				require.NoError(t, err)
				require.Equal(t, "ready", action.Stage)
				require.Equal(t, originalHash, action.RecipeSHA256, "durable ownership retains the original integrity hash")
				require.Equal(t, originalHash, current.LaunchRecipe.SHA256)
				require.Equal(t, recipe.AntigravityExtension, current.LaunchRecipe.AntigravityExtension, "historical metadata remains inert")
			})
		}
	}
}
