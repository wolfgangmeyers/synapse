package managed

import (
	"context"
	"database/sql"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Unlike TestEmbeddedG1CommittedIntentTakeoverBeforeExecute, this blocks inside
// the SDK BeforeToolExecute callback, after intent but before callback return.
func TestEmbeddedG1ExactCallbackIntentTakeover(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "callback.db")
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
	var providerCalls, aExec, bExec, aWrapper atomic.Int32
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
	callbackExit := make(chan error, 1)
	aOpts.EmbeddedAfterCheckedIntent = func(callCtx context.Context) { close(entered); <-release; callbackExit <- callCtx.Err() }
	aOpts.EmbeddedAfterIntent = func(context.Context) { aWrapper.Add(1) }
	a, err := NewManager(aOpts)
	require.NoError(t, err)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = a.Close(ctx)
	}()
	binding := g1Launch(t, a, root)
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("callback did not reach checked intent")
	}
	t.Log("reached actual SDK callback after checked intent")
	stale := inspectManagedOwnerBinding(t, path, binding)
	intent, err := aDB.RecoverEmbeddedEffect(ctx, stale, "uncertain-effect")
	require.NoError(t, err)
	require.Equal(t, "unknown", intent.State)
	require.False(t, intent.ShouldReplay)
	require.Empty(t, intent.Result)
	require.Zero(t, aExec.Load())
	snap, err := aDB.ReopenEmbedded(ctx, stale)
	require.NoError(t, err)
	for _, e := range snap.Entries {
		require.NotEqual(t, "toolResult", e.Kind)
	}
	bOpts := options(bDB, g1Tool(func(context.Context) (agent.AgentToolResult, error) {
		bExec.Add(1)
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "new result"}}}, nil
	}))
	authorize := make(chan struct{})
	defer func() {
		select {
		case <-authorize:
		default:
			close(authorize)
		}
	}()
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
	// B must first see A's live lease, but its real retry timer must fit
	// inside the assertion window after the fixed clock crosses expiry.
	require.NoError(t, clock.Advance(embeddedOwnerTTL-time.Second))
	require.NoError(t, b.RecoverEmbeddedRoots(ctx))
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, stale.OwnerToken, inspectManagedOwnerBinding(t, path, binding).OwnerToken)
	require.EqualValues(t, 1, providerCalls.Load())
	t.Log("live lease rejected B takeover")
	require.NoError(t, clock.Advance(2*time.Second))
	// Read the exact persisted claim; inspecting only its fencing token misses
	// a stale callback that releases or shortens B's lease.
	ownerRead, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer ownerRead.Close()
	type ownerClaim struct {
		id, expiry string
		token      int64
	}
	readOwner := func() ownerClaim {
		t.Helper()
		var claim ownerClaim
		require.NoError(t, ownerRead.QueryRowContext(ctx, `SELECT owner_id, owner_token, owner_expires_at FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&claim.id, &claim.token, &claim.expiry))
		require.NotEmpty(t, claim.id)
		require.Positive(t, claim.token)
		require.NotEmpty(t, claim.expiry)
		return claim
	}
	if !assert.Eventually(t, func() bool { return inspectManagedOwnerBinding(t, path, binding).OwnerToken > stale.OwnerToken }, 3*time.Second, 10*time.Millisecond) {
		claim := readOwner()
		t.Fatalf("B takeover timed out: owner_id=%q owner_token=%d owner_expires_at=%q fixed_now=%s", claim.id, claim.token, claim.expiry, clock.Now().Format(time.RFC3339Nano))
	}
	current := inspectManagedOwnerBinding(t, path, binding)
	select {
	case <-bRestored:
	case <-time.After(3 * time.Second):
		t.Fatal("B did not restore")
	}
	require.EqualValues(t, 2, providerCalls.Load())
	bClaim := readOwner()
	require.Equal(t, current.OwnerToken, bClaim.token)
	close(release)
	select {
	case exitErr := <-callbackExit:
		require.NoError(t, exitErr, "test requires a live SDK callback context at release")
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not resume")
	}
	// Let the still-live SDK context pass admission before stopping A. Closing
	// A immediately would cancel the request and mask a missing owner recheck.
	time.Sleep(100 * time.Millisecond)
	require.Zero(t, aWrapper.Load(), "stale SDK callback must abort before wrapper admission")
	require.NoError(t, a.Close(ctx))
	afterStaleCallback := readOwner()
	require.Equal(t, bClaim.id, afterStaleCallback.id, "stale A must not release B's owner")
	require.Equal(t, bClaim.token, afterStaleCallback.token, "stale A must not replace B's token")
	require.GreaterOrEqual(t, afterStaleCallback.expiry, bClaim.expiry, "stale A must not shorten B's lease")
	require.Zero(t, aExec.Load())
	require.Zero(t, bExec.Load())
	require.EqualValues(t, 2, providerCalls.Load())
	recovered, err := bDB.RecoverEmbeddedEffect(ctx, current, "uncertain-effect")
	require.NoError(t, err)
	require.Equal(t, "unknown", recovered.State)
	require.False(t, recovered.ShouldReplay)
	require.Empty(t, recovered.Result)
	snap, err = bDB.ReopenEmbedded(ctx, current)
	require.NoError(t, err)
	for _, e := range snap.Entries {
		require.NotEqual(t, "toolResult", e.Kind)
	}
	row, err := bDB.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.NotEqual(t, "failed", row.LifecycleState)
	t.Log("stale callback aborted without checked denial result; B lifecycle preserved")
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
	require.False(t, recovered.ShouldReplay)
	t.Log("explicit new call ID executed exactly once; old call never replayed")
}
