package managed

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// A saved root must come back without a second Listen/Recover call after its
// previous process dies while the persisted owner lease is still live.
func TestEmbeddedSavedOwnerLeaseAutomaticallyRetries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "owner.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	aDB, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer aDB.Close()
	bDB, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer bDB.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	options := func(db *store.Store) ManagerOptions {
		return ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }}
	}
	entered := make(chan struct{})
	aOpts := options(aDB)
	aOpts.EmbeddedRunnerFactory = func(ctx context.Context, _ store.ManagedAgentRecord) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	a, err := NewManager(aOpts)
	require.NoError(t, err)
	defer a.Close(ctx)
	launched, err := a.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "saved-owner", Cwd: root, Runtime: "embedded", InitialInstruction: "wait", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-sol"}, RequestID: "f1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old owner did not start")
	}
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	before := inspectManagedOwnerBinding(t, path, binding)
	require.NoError(t, a.Close(ctx)) // dead process leaves an unexpired persisted claim
	var calls atomic.Int32
	bOpts := options(bDB)
	bOpts.EmbeddedRunnerFactory = func(context.Context, store.ManagedAgentRecord) error { calls.Add(1); return nil }
	b, err := NewManager(bOpts)
	require.NoError(t, err)
	defer b.Close(ctx)
	require.NoError(t, clock.Advance(29*time.Second))
	require.NoError(t, b.RecoverEmbeddedRoots(ctx))
	require.NoError(t, b.RecoverEmbeddedRoots(ctx))
	time.Sleep(100 * time.Millisecond) // ensure the first claim observes the live lease
	require.Equal(t, before.OwnerToken, inspectManagedOwnerBinding(t, path, binding).OwnerToken)
	require.NoError(t, clock.Advance(2*time.Second))
	require.Eventually(t, func() bool { return inspectManagedOwnerBinding(t, path, binding).OwnerToken > before.OwnerToken }, 3*time.Second, 25*time.Millisecond, "saved owner never retried after lease expiry")
	require.EqualValues(t, 1, calls.Load(), "repeated recovery must not run the same incarnation twice")
}
