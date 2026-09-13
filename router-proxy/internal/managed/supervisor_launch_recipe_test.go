package managed

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestManagerLaunchPersistsImmutableNonSecretRecipe(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	config, paths := managedLaunchConfigFixture(t)
	config.TestOnlyEnvironment = []string{"API_TOKEN=secret-value", "MECHA_TEST_MODE=fixture"}
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	process := &launchProcess{identity: ProcessIdentity{PID: 811, StartMarker: "recipe-start", Incarnation: "recipe-inc"}, stopped: make(chan struct{})}
	rpc := &launchRPC{}
	factory := &launchFactory{process: process, rpc: rpc}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: config, Manifest: manifest, ProcessFactory: factory,
		RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	require.NoError(t, func() error {
		_, err := manager.Launch(ctx, LaunchRequest{Name: "recipe-agent", Cwd: paths["work"], RequestID: "recipe-request"})
		return err
	}())
	deadline := time.Now().Add(2 * time.Second)
	for {
		agent, readErr := durable.ManagedAgent(ctx, "recipe-agent")
		require.NoError(t, readErr)
		if agent.LifecycleState == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed agent did not become ready: %+v", agent)
		}
		time.Sleep(time.Millisecond)
	}
	recipe, err := durable.ManagedLaunchRecipe(ctx, "recipe-agent")
	require.NoError(t, err)
	require.Equal(t, config.PiExecutable, recipe.PiExecutable)
	require.Equal(t, config.SessionRoot, recipe.SessionRoot)
	canonicalWork, err := filepath.EvalSymlinks(paths["work"])
	require.NoError(t, err)
	require.Equal(t, canonicalWork, recipe.Cwd)
	require.Equal(t, "openai-codex", recipe.Provider)
	require.Equal(t, "gpt-5.6-terra", recipe.ModelID)
	require.Equal(t, config.MechaRPCExtension, recipe.MechaRPCExtension)
	require.Equal(t, config.PiSubagentsExtension, recipe.PiSubagentsExtension)
	require.Contains(t, recipe.Environment, "MECHA_TEST_MODE=fixture")
	require.NotContains(t, recipe.Environment, "MECHA_ROUTER_URL="+config.RouterURL)
	require.NotContains(t, recipe.Environment, "API_TOKEN=secret-value")
	require.NotEmpty(t, recipe.SHA256)
	_, err = store.ManagedLaunchRecipeDigest(*recipe)
	require.NoError(t, err)
	require.NoError(t, manager.Close(ctx))
}
