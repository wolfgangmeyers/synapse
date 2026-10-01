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

func TestEmbeddedSavedOwnerRetryGuards(t *testing.T) {
	for _, mode := range []string{"manager-close", "stopped-incarnation", "third-contender"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
			path := filepath.Join(root, "owner.db")
			aDB, err := store.Open(ctx, path, store.Options{Clock: clock})
			require.NoError(t, err)
			defer aDB.Close()
			bDB, err := store.Open(ctx, path, store.Options{Clock: clock})
			require.NoError(t, err)
			defer bDB.Close()
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			options := func(db *store.Store) ManagerOptions {
				return ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }}
			}
			started := make(chan struct{})
			aOpt := options(aDB)
			aOpt.EmbeddedRunnerFactory = func(runCtx context.Context, _ store.ManagedAgentRecord) error {
				close(started)
				<-runCtx.Done()
				return runCtx.Err()
			}
			a, err := NewManager(aOpt)
			require.NoError(t, err)
			defer a.Close(ctx)
			launched, err := a.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "saved-owner", Cwd: root, Runtime: "embedded", InitialInstruction: "wait", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-sol"}, RequestID: "f2222222-2222-4222-8222-222222222222"})
			require.NoError(t, err)
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("old owner did not start")
			}
			binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
			old := inspectManagedOwnerBinding(t, path, binding).OwnerToken
			require.NoError(t, a.Close(ctx))
			require.NoError(t, clock.Advance(29*time.Second))
			var calls atomic.Int32
			bOpt := options(bDB)
			bOpt.EmbeddedRunnerFactory = func(context.Context, store.ManagedAgentRecord) error { calls.Add(1); return nil }
			b, err := NewManager(bOpt)
			require.NoError(t, err)
			defer b.Close(ctx)
			require.NoError(t, b.RecoverEmbeddedRoots(ctx))
			time.Sleep(150 * time.Millisecond)
			require.Equal(t, old, inspectManagedOwnerBinding(t, path, binding).OwnerToken)
			switch mode {
			case "manager-close":
				require.NoError(t, b.Close(ctx))
			case "stopped-incarnation":
				require.NoError(t, bDB.MarkManagedFailed(ctx, binding.AgentID, "STOPPED", "obsolete"))
			case "third-contender":
				require.NoError(t, clock.Advance(2*time.Second))
				contender, err := bDB.AcquireEmbeddedOwner(ctx, binding, "third-contender", time.Second)
				require.NoError(t, err)
				require.Greater(t, contender, old)
				time.Sleep(1200 * time.Millisecond) // B's first timer must observe the third claim, not steal it.
				require.Equal(t, contender, inspectManagedOwnerBinding(t, path, binding).OwnerToken)
				require.Zero(t, calls.Load())
				require.NoError(t, clock.Advance(2*time.Second))
				require.Eventually(t, func() bool { return inspectManagedOwnerBinding(t, path, binding).OwnerToken > contender }, 3*time.Second, 20*time.Millisecond)
				require.EqualValues(t, 1, calls.Load())
				return
			}
			require.NoError(t, clock.Advance(3*time.Second))
			time.Sleep(1200 * time.Millisecond)
			require.Equal(t, old, inspectManagedOwnerBinding(t, path, binding).OwnerToken)
			require.Zero(t, calls.Load())
		})
	}
}
