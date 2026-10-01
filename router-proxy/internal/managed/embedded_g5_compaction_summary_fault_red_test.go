package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The fake only returns provider messages. The Manager and SDK must create the
// intent, pending result, one-shot claim, and checkpoint themselves.
func TestEmbeddedG5CompactionSummaryErrorAfterClaimColdReopenRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Now())
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const callID = "g5-summary-fault-compact"
	var requests atomic.Int32
	summaryEntered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_context", map[string]any{"action": "compact"}, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			if classifyG5CompactionRequest(h, opts, callID).kind == "summary" {
				summaryEntered <- struct{}{}
				<-release
				// Partial text with a terminal error must not become a checkpoint.
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "partial summary"}}, ai.StopError)
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unexpected normal continuation"}}, ai.StopStop)
		},
	})
	newManager := func(s *store.Store) *Manager {
		t.Helper()
		m, e := NewManager(ManagerOptions{Store: s, Manifest: manifest,
			Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:  testfixture.CodexAdmission(t),
		})
		require.NoError(t, e)
		return m
	}
	m := newManager(db)
	defer func() { unblock(); _ = m.Close(ctx); _ = db.Close() }()
	req := LaunchRequest{Name: "g5-summary-fault-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "compact your own session", RequestID: "a5555555-5555-4555-8555-555555555593"}
	launched, err := m.LaunchEmbeddedRoot(ctx, req)
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	select {
	case <-summaryEntered:
	case <-time.After(4 * time.Second):
		t.Fatal("G5 RED: checked SDK summary request not reached (claim/fault/reopen unexecuted)")
	}
	read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()
	var claimed, checkedResult, checkpoints int
	var effectState string
	require.NoError(t, read.QueryRowContext(ctx, `SELECT claimed FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=?`, callID, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&claimed))
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND kind='toolResult'`, binding.AgentID, binding.SessionID, binding.Epoch).Scan(&checkedResult))
	require.NoError(t, read.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=?`, callID, binding.AgentID).Scan(&effectState))
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=?`, binding.AgentID).Scan(&checkpoints))
	require.Equal(t, 1, checkedResult, "real SDK pending result must be saved before summary")
	require.Equal(t, "unknown", effectState)
	require.Zero(t, checkpoints)
	require.Equal(t, 1, claimed, "G5 RED: durable claim must precede the summarizer; error/reopen/conflict gates unexecuted")

	unblock()
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && a.LifecycleState == "failed"
	}, 3*time.Second, 10*time.Millisecond, "summary error must fail the SDK turn")
	failed, err := db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	// Never print LastErrorMessage: it may contain arbitrary provider output.
	category := "other/redacted"
	if failed.LastErrorMessage == "compaction summarization failed" {
		category = "summarization-failed"
	}
	t.Logf("persisted lifecycle=%s error_code=%s message_category=%s", failed.LifecycleState, failed.LastErrorCode, category)
	require.Equal(t, "EMBEDDED_START_FAILED", failed.LastErrorCode)
	require.EqualValues(t, 2, requests.Load(), "no ordinary provider request after failed summary")
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	require.NoError(t, clock.Advance(embeddedOwnerTTL+time.Second))
	db, err = store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	m = newManager(db)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	// A failed lifecycle is not a recoverable live owner. ReopenEmbedded and
	// effect/claim/checkpoint APIs require both a live owner and the managed
	// running fence; neither lease expiry nor a fabricated token revives it.
	cold, err := db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", cold.LifecycleState)
	require.Equal(t, failed.LastErrorCode, cold.LastErrorCode)
	role, err := db.SavedEmbeddedSessionRole(ctx, binding) // checked, read-only saved reservation
	require.NoError(t, err)
	require.Equal(t, "root", role)
	_, err = db.ReopenEmbedded(ctx, binding)
	require.Error(t, err, "failed lifecycle must not acquire a live reopen fence")
	m.mu.Lock()
	_, hasOwner := m.embeddedOwners[binding.AgentID]
	m.mu.Unlock()
	require.False(t, hasOwner, "failed roots are intentionally skipped by recovery")
	require.EqualValues(t, 2, requests.Load(), "cold recovery must not replay summary or normal provider")
	// Fixed metadata only: do not select entry bodies, effect intent, saved
	// result, last_error_message, or provider response text.
	var afterClaim, afterResults, afterCheckpoints int
	var afterEffect string
	require.NoError(t, read.QueryRowContext(ctx, `SELECT claimed FROM embedded_own_compaction_requests WHERE call_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&afterClaim))
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='toolResult'`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&afterResults))
	require.NoError(t, read.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, callID, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&afterEffect))
	require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&afterCheckpoints))
	require.Equal(t, 1, afterClaim, "one-shot claim remains spent after cold Store reopen")
	require.Equal(t, 1, afterResults, "checked pending result remains durable")
	require.Equal(t, "unknown", afterEffect, "failed summary remains unresolved, not replay permission")
	require.Zero(t, afterCheckpoints, "partial summary must never commit a checkpoint")
}
