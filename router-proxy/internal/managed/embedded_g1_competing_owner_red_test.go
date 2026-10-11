package managed

import (
	"context"
	"database/sql"
	"os"
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

// The paused runner already holds Manager A's real claim. Manager B must
// conflict while it is live, then take over only after expiry.
func TestEmbeddedG1CompetingOwnersFencePausedRunner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700)) // fixture-owned Cwd, before either runner
	path := filepath.Join(t.TempDir(), "competing.db")
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
	var providerCalls, effects atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		providerCalls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("g1_test_external_effect", map[string]any{}, "competing-effect")}, ai.StopToolUse)
	}, func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		providerCalls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
	}})
	tool := g1Tool(func(context.Context) (agent.AgentToolResult, error) {
		effects.Add(1)
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "executed"}}}, nil
	})
	entered := make(chan store.EmbeddedBinding, 1)
	release := make(chan struct{})
	var a *Manager
	a, err = NewManager(ManagerOptions{Store: aDB, Manifest: manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedExternalTool:  tool,
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
			owner, ok := runCtx.Value(embeddedOwnerContextKey{}).(embeddedOwner)
			if !ok {
				return context.Canceled
			}
			entered <- owner.binding // after Manager's real claim, before prompt
			select {
			case <-release:
			case <-runCtx.Done():
				return runCtx.Err()
			}
			return a.runEmbeddedSession(runCtx, saved)
		},
	})
	require.NoError(t, err)
	defer a.Close(ctx)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	launched, err := a.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "competing-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "call g1_test_external_effect", RequestID: "a1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	var stale store.EmbeddedBinding
	select {
	case stale = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("owner A never reached pre-prompt pause")
	}
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	require.Equal(t, binding.AgentID, stale.AgentID)
	require.Positive(t, stale.OwnerToken)
	saved, err := aDB.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.NotEmpty(t, saved.LaunchOperationID)
	var b *Manager
	b, err = NewManager(ManagerOptions{Store: bDB, Manifest: manifest,
		Config:               Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:        CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedExternalTool: tool, CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
	})
	require.NoError(t, err)
	defer b.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, bDB, b)
	currentBinding := binding
	currentBinding.Epoch++
	require.NoError(t, bDB.ResetEmbeddedSessionEpoch(ctx, binding, currentBinding, "root"))
	require.NoError(t, b.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool {
		return inspectManagedOwnerBinding(t, path, currentBinding).Epoch == currentBinding.Epoch
	}, 3*time.Second, 100*time.Millisecond)
	current := inspectManagedOwnerBinding(t, path, currentBinding)
	require.Equal(t, currentBinding.Epoch, current.Epoch)
	require.ErrorContains(t, aDB.RecordEmbeddedEffectIntent(ctx, stale, "stale-effect", []byte(`{"intent":true}`)), "stale embedded owner")
	_, err = aDB.AppendEmbeddedEntryAt(ctx, stale, 0, "genesis", "stale-append", "user", []byte(`{"text":"stale"}`))
	require.ErrorContains(t, err, "stale embedded owner")
	require.ErrorContains(t, aDB.ReadyEmbeddedRoot(ctx, stale, saved.LaunchOperationID), "stale embedded owner")
	require.ErrorContains(t, aDB.AppendEmbeddedMailAndACK(ctx, stale, "not-yet-claimed", []byte("mail")), "stale embedded owner")
	// A's paused callback must be rejected before it can prompt. A stale
	// Manager must not mark the newer owner's managed lifecycle failed.
	close(release)
	defer func() {
		if t.Failed() {
			b.mu.Lock()
			processGeneration := b.embeddedStartupGeneration
			b.mu.Unlock()
			t.Logf("post-release clock=%s old_token=%d takeover_token=%d process_generation=%d provider_calls=%d effects=%d", clock.Now(), stale.OwnerToken, current.OwnerToken, processGeneration, providerCalls.Load(), effects.Load())
			cwdPrivacyManagedSnapshot(t, path)
			row, rowErr := bDB.ManagedAgent(ctx, binding.AgentID)
			t.Logf("post-release managed=%+v query_error=%v", row, rowErr)
			effect, effectErr := bDB.RecoverEmbeddedEffect(ctx, current, "competing-effect")
			t.Logf("post-release effect=%+v query_error=%v", effect, effectErr)
			read, readErr := sql.Open("sqlite", "file:"+path+"?mode=ro")
			if readErr != nil {
				t.Logf("post-release snapshot open_error=%v", readErr)
			} else {
				var snapshot string
				queryErr := read.QueryRowContext(ctx, `SELECT json_group_array(json_object('id',effect_id,'state',state,'intent',CAST(intent AS TEXT),'result',CAST(result AS TEXT))) FROM embedded_effects`).Scan(&snapshot)
				t.Logf("post-release persisted_effects=%s query_error=%v", snapshot, queryErr)
				_ = read.Close()
			}
		}
	}()
	require.Eventually(t, func() bool {
		saved, e := bDB.ManagedAgent(ctx, binding.AgentID)
		return e == nil && saved.LifecycleState == "running" && effects.Load() == 1
	}, 3*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, effects.Load())
	require.EqualValues(t, 2, providerCalls.Load())
	snap, err := bDB.ReopenEmbedded(ctx, current)
	require.NoError(t, err)
	require.NotEmpty(t, snap.Entries)
	last := snap.Entries[len(snap.Entries)-1]
	require.NoError(t, bDB.CommitEmbeddedCheckpoint(ctx, current, last.Sequence, last.Digest, []byte(`{"owner":"b"}`)))
	require.ErrorContains(t, aDB.CommitEmbeddedCheckpoint(ctx, stale, last.Sequence, last.Digest, []byte(`{"owner":"a"}`)), "stale embedded owner")
	snap, err = bDB.ReopenEmbedded(ctx, current)
	require.NoError(t, err)
	require.Equal(t, []byte(`{"owner":"b"}`), snap.Checkpoint.Body, "stale owner must not overwrite newer checkpoint")
}
