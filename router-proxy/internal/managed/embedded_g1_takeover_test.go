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
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// The pause is in the production tool wrapper, after the SDK callback has
// committed the effect intent and before the host's actual Execute.
func TestEmbeddedG1CommittedIntentTakeoverBeforeExecute(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "takeover.db")
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
	var providerCalls, aExec, bExec atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("g1_test_external_effect", map[string]any{}, "uncertain-effect")}, ai.StopToolUse)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "interruption acknowledged"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("g1_test_external_effect", map[string]any{}, "fresh-effect")}, ai.StopToolUse)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	credentials := testfixture.CodexAdmission(t)
	options := func(db *store.Store, tool *agent.AgentTool) ManagerOptions {
		return ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, EmbeddedExternalTool: tool, CodexCredentialStore: credentials}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	aOpts := options(aDB, g1Tool(func(context.Context) (agent.AgentToolResult, error) {
		aExec.Add(1)
		return agent.AgentToolResult{}, nil
	}))
	aOpts.EmbeddedAfterIntent = func(context.Context) { close(entered); <-release }
	a, err := NewManager(aOpts)
	require.NoError(t, err)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		require.NoError(t, a.Close(ctx))
	}()
	binding := g1Launch(t, a, root)
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("SDK did not commit intent and enter pre-Execute barrier")
	}
	stale := inspectManagedOwnerBinding(t, path, binding)
	intent, err := aDB.RecoverEmbeddedEffect(ctx, stale, "uncertain-effect")
	require.NoError(t, err)
	require.Equal(t, "unknown", intent.State)
	require.False(t, intent.ShouldReplay)
	require.Zero(t, aExec.Load())
	bOpts := options(bDB, g1Tool(func(context.Context) (agent.AgentToolResult, error) {
		bExec.Add(1)
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "new result"}}}, nil
	}))
	authorize := make(chan struct{})
	bRestored := make(chan struct{})
	var b *Manager
	bOpts.EmbeddedRunnerFactory = func(runCtx context.Context, saved store.ManagedAgentRecord) error {
		if err := b.runEmbeddedSession(runCtx, saved); err != nil {
			return err
		}
		close(bRestored)
		select {
		case <-authorize:
		case <-runCtx.Done():
			return runCtx.Err()
		}
		return b.runEmbeddedSessionPrompt(runCtx, saved, "explicitly authorize new effect")
	}
	b, err = NewManager(bOpts)
	require.NoError(t, err)
	defer b.Close(ctx)
	// Keep the lease live, but put recovery's real retry within the assertion window.
	require.NoError(t, clock.Advance(embeddedOwnerTTL-time.Second))
	require.NoError(t, b.RecoverEmbeddedRoots(ctx))
	// A still holds the lease. No dispatch or provider request may take it.
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, stale.OwnerToken, inspectManagedOwnerBinding(t, path, binding).OwnerToken)
	require.EqualValues(t, 1, providerCalls.Load())
	require.NoError(t, clock.Advance(2*time.Second))
	require.NoError(t, b.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool { return inspectManagedOwnerBinding(t, path, binding).OwnerToken > stale.OwnerToken }, 3*time.Second, 10*time.Millisecond)
	current := inspectManagedOwnerBinding(t, path, binding)
	select {
	case <-bRestored:
	case <-time.After(3 * time.Second):
		t.Fatal("B did not finish restoring unknown effect")
	}
	recovered, err := bDB.RecoverEmbeddedEffect(ctx, current, "uncertain-effect")
	require.NoError(t, err)
	require.Equal(t, "unknown", recovered.State)
	require.False(t, recovered.ShouldReplay)
	require.Empty(t, recovered.Result)
	require.EqualValues(t, 2, providerCalls.Load())
	require.Zero(t, bExec.Load())
	close(release)
	require.NoError(t, a.Close(ctx)) // stale A cannot fail B's lifecycle
	require.Zero(t, aExec.Load())
	require.Zero(t, bExec.Load())
	require.EqualValues(t, 2, providerCalls.Load())
	snap, err := bDB.ReopenEmbedded(ctx, current)
	require.NoError(t, err)
	for _, e := range snap.Entries {
		require.NotEqual(t, "toolResult", e.Kind)
	}
	// Recovery requests only the notice; the fresh effect needs an explicit turn.
	close(authorize)
	require.Eventually(t, func() bool {
		row, e := bDB.ManagedAgent(ctx, binding.AgentID)
		return e == nil && row.LifecycleState == "running" && bExec.Load() == 1
	}, 3*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 4, providerCalls.Load())
	require.Zero(t, aExec.Load())
	require.EqualValues(t, 1, bExec.Load())
	fresh, err := bDB.RecoverEmbeddedEffect(ctx, current, "fresh-effect")
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.State)
	require.NotEmpty(t, fresh.Intent)
	snap, err = bDB.ReopenEmbedded(ctx, current)
	require.NoError(t, err)
	var results int
	for _, e := range snap.Entries {
		if e.Kind == "toolResult" {
			results++
		}
	}
	require.Equal(t, 1, results)
	recovered, err = bDB.RecoverEmbeddedEffect(ctx, current, "uncertain-effect")
	require.NoError(t, err)
	require.Equal(t, "unknown", recovered.State)
	row, err := bDB.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", row.LifecycleState)

	// Close the second owner and reopen with a third Store/Manager. This is
	// explicit recovery after expiry, never an automatic replay or timer.
	require.NoError(t, b.Close(ctx))
	cDB, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer cDB.Close()
	c, err := NewManager(options(cDB, g1Tool(func(context.Context) (agent.AgentToolResult, error) {
		bExec.Add(1)
		return agent.AgentToolResult{}, nil
	})))
	require.NoError(t, err)
	defer c.Close(ctx)
	// Apply the same near-expiry ordering to the third owner's recovery.
	require.NoError(t, clock.Advance(embeddedOwnerTTL-time.Second))
	require.NoError(t, c.RecoverEmbeddedRoots(ctx))
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, current.OwnerToken, inspectManagedOwnerBinding(t, path, binding).OwnerToken)
	require.NoError(t, clock.Advance(2*time.Second))
	require.NoError(t, c.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool { return inspectManagedOwnerBinding(t, path, binding).OwnerToken > current.OwnerToken }, 3*time.Second, 10*time.Millisecond)
	third := inspectManagedOwnerBinding(t, path, binding)
	require.Eventually(t, func() bool {
		row, e := cDB.ManagedAgent(ctx, binding.AgentID)
		return e == nil && row.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	old, err := cDB.RecoverEmbeddedEffect(ctx, third, "uncertain-effect")
	require.NoError(t, err)
	require.Equal(t, "unknown", old.State)
	require.False(t, old.ShouldReplay)
	require.EqualValues(t, 4, providerCalls.Load())
	require.EqualValues(t, 1, bExec.Load())
}
