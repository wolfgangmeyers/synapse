package managed

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedStage1ReservedBeforeRunnerWithoutStockArtifacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	started := make(chan store.ManagedAgentRecord, 1)
	var processCalls, rpcCalls atomic.Int32
	manager, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessFactory: &rootRedProcessFactory{calls: &processCalls}, RPCFactory: rootRedRPCFactory{calls: &rpcCalls}, CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedRunnerFactory: func(_ context.Context, saved store.ManagedAgentRecord) error {
		actual, e := db.ManagedAgent(ctx, saved.AgentID)
		if e != nil {
			return e
		}
		if actual.Runtime != "embedded" || actual.LaunchOperationID == "" {
			return os.ErrInvalid
		}
		started <- *actual
		return nil
	}})
	require.NoError(t, err)
	defer manager.Close(ctx)
	request := LaunchRequest{Name: "stage1-root", Cwd: root, InitialInstruction: "hello", Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "11111111-1111-4111-8111-111111111111"}
	result, err := manager.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "embedded", result.Agent.Runtime)
	select {
	case saved := <-started:
		require.Equal(t, result.Agent.PiSessionID, saved.PiSessionID)
	case <-time.After(3 * time.Second):
		t.Fatal("runner not dispatched")
	}
	require.EqualValues(t, 0, processCalls.Load())
	require.EqualValues(t, 0, rpcCalls.Load())
	_, err = os.Stat(filepath.Join(root, "sessions"))
	require.True(t, os.IsNotExist(err))
	saved, err := db.ManagedAgent(ctx, request.Name)
	require.NoError(t, err)
	require.Equal(t, "embedded", saved.Runtime)
	op, err := db.ManagedOperation(ctx, request.RequestID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
	replay, err := manager.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, saved.PiSessionID, replay.Agent.PiSessionID)
	request.Runtime = "rpc"
	_, err = manager.LaunchEmbeddedRoot(ctx, request)
	require.Error(t, err, "same identity cannot switch runtime")
}
