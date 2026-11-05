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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real Manager recovery, SDK, owner acquisition and reopened SQLite. Only the
// external provider is scripted; a failed entered turn requires a notice.
func TestEmbeddedColdInterruptedNoticeFinalHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const reply = "Recovered notice reply saved for the user"
	var calls, evaluations atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			calls.Add(1)
			msg := providers.FauxAssistantMessage(nil, ai.StopError)
			msg.ErrorMessage = "deterministic interrupted provider"
			return msg
		},
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			calls.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: reply}}, ai.StopStop)
		},
	})
	newManager := func() *Manager {
		m, e := NewManager(ManagerOptions{Store: db, Manifest: manifest, Clock: clock,
			Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:  testfixture.CodexAdmission(t),
			EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
				evaluations.Add(1)
				return store.EmbeddedGoalEvaluationNotDone, "not evaluated for notice", nil
			},
		})
		require.NoError(t, e)
		return m
	}
	m := newManager()
	t.Cleanup(func() { _ = m.Close(ctx); _ = db.Close() })
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "notice-history", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "entered work", RequestID: "a5555555-5555-4555-8555-555555555599"})
	require.NoError(t, err)
	m.startWG.Wait()
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	require.NoError(t, clock.Advance(embeddedOwnerTTL+time.Second))
	db, err = store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	m = newManager()
	published := make(chan store.ManagedHistoryEntry, 32)
	m.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) { published <- entry })
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	m.startWG.Wait()
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	var kind, state, body string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT w.kind,w.state,CAST(e.body AS TEXT) FROM embedded_restart_wake_slots w JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE w.agent_id=? AND w.slot='current'`, launched.Agent.AgentID).Scan(&kind, &state, &body))
	require.Equal(t, "interrupted", kind)
	require.Equal(t, "checked-complete", state)
	require.Contains(t, body, reply, "notice final must already be durably saved before testing projection")
	require.EqualValues(t, 2, calls.Load())
	require.Zero(t, evaluations.Load(), "notice must not acquire goal evaluation semantics")
	var executions, receipts, continuations int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_sessions WHERE agent_id=? AND execution_json IS NOT NULL`, launched.Agent.AgentID).Scan(&executions))
	require.Zero(t, executions)
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_goal_applied_receipts WHERE agent_id=?`, launched.Agent.AgentID).Scan(&receipts))
	require.Zero(t, receipts)
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&continuations))
	require.Zero(t, continuations)
	history, err := db.GetManagedCompleteHistory(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	matches := 0
	for _, entry := range history {
		if entry.MessageContent != nil && *entry.MessageContent == reply {
			matches++
			require.Equal(t, "message", entry.Presentation.Kind)
			require.Equal(t, launched.Agent.AgentID, entry.Presentation.From)
			require.Equal(t, "user", entry.Presentation.To)
		}
	}
	require.Equal(t, 1, matches, "checked notice final must project once into durable public history")
	publishedMatches := 0
	for len(published) > 0 {
		entry := <-published
		if entry.MessageContent != nil && *entry.MessageContent == reply {
			publishedMatches++
		}
	}
	require.Equal(t, 1, publishedMatches, "committed notice final must publish once")
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	db, err = store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	history, err = db.GetManagedCompleteHistory(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	matches = 0
	for _, entry := range history {
		if entry.MessageContent != nil && *entry.MessageContent == reply {
			matches++
		}
	}
	require.Equal(t, 1, matches, "fresh history reader retains exactly one reply")
}

// SCN-WFIG-015: a successful provider candidate is not a saved reply.
func TestEmbeddedColdInterruptedNoticeStaleOwnerNoHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const reply = "Stale recovery reply must never reach the user"
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var calls, evaluations atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			calls.Add(1)
			msg := providers.FauxAssistantMessage(nil, ai.StopError)
			msg.ErrorMessage = "deterministic interrupted provider"
			return msg
		},
		func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			calls.Add(1)
			close(entered)
			<-release
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: reply}}, ai.StopStop)
		},
	})
	newManager := func() *Manager {
		m, e := NewManager(ManagerOptions{Store: db, Manifest: manifest, Clock: clock,
			Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
			CodexCredentialStore:  testfixture.CodexAdmission(t),
			EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
				evaluations.Add(1)
				return store.EmbeddedGoalEvaluationNotDone, "not evaluated for notice", nil
			},
		})
		require.NoError(t, e)
		return m
	}
	m := newManager()
	t.Cleanup(func() { unblock(); _ = m.Close(ctx); _ = db.Close() })
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "notice-stale-history", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "entered work", RequestID: "a5555555-5555-4555-8555-555555555599"})
	require.NoError(t, err)
	m.startWG.Wait()
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	require.NoError(t, clock.Advance(embeddedOwnerTTL+time.Second))
	db, err = store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	m = newManager()
	published := make(chan store.ManagedHistoryEntry, 32)
	m.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) { published <- entry })
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	require.NoError(t, m.RecoverEmbeddedRoots(ctx))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery provider did not reach the blocked notice reply")
	}
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	binding := inspectManagedOwnerBinding(t, path, store.EmbeddedBinding{
		AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID,
		Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration,
	})
	var kind, state, digest string
	var head int64
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT w.kind,w.state,e.seq,e.digest FROM embedded_restart_wake_slots w JOIN embedded_entries e ON e.agent_id=w.agent_id AND e.session_id=w.session_id AND e.epoch=w.epoch AND e.generation=w.runtime_generation AND e.digest=w.digest WHERE w.agent_id=? AND w.slot='current'`, binding.AgentID).Scan(&kind, &state, &head, &digest))
	require.Equal(t, "interrupted", kind)
	require.Equal(t, "claimed", state, "takeover must happen after the real notice claim and before its final")
	token, err := db.AcquireEmbeddedOwner(ctx, binding, "notice-negative-replacement-owner", embeddedOwnerTTL)
	require.NoError(t, err)
	require.Greater(t, token, binding.OwnerToken, "replace the actual Manager owner through the Store API")
	unblock()
	settled := make(chan struct{})
	go func() { m.startWG.Wait(); close(settled) }()
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("stale notice turn did not settle after provider release")
	}
	require.EqualValues(t, 2, calls.Load(), "one failed entered turn and one notice candidate, no replay")
	require.Zero(t, evaluations.Load(), "notice must not acquire goal evaluation semantics")
	var afterHead int64
	var afterDigest string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT seq,digest FROM embedded_entries WHERE agent_id=? ORDER BY seq DESC LIMIT 1`, binding.AgentID).Scan(&afterHead, &afterDigest))
	require.Equal(t, head, afterHead, "stale final must not append to the checked SDK history")
	require.Equal(t, digest, afterDigest)
	var checked, receipts, continuations int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=?`, binding.AgentID).Scan(&checked))
	require.Zero(t, checked, "stale candidate must not gain a checked final origin")
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_applied_receipts WHERE agent_id=?`, binding.AgentID).Scan(&receipts))
	require.Zero(t, receipts)
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, binding.AgentID).Scan(&continuations))
	require.Zero(t, continuations)
	history, err := db.GetManagedCompleteHistory(ctx, binding.AgentID)
	require.NoError(t, err)
	matches := 0
	for _, entry := range history {
		if entry.MessageContent != nil && *entry.MessageContent == reply {
			matches++
		}
	}
	// Check both sinks even if one assertion fails: neither may expose success.
	requirements := assert.New(t)
	requirements.Zero(matches, "stale notice final must not appear in durable complete history")
	publishedMatches := 0
	for len(published) > 0 {
		entry := <-published
		if entry.MessageContent != nil && *entry.MessageContent == reply {
			publishedMatches++
		}
	}
	requirements.Zero(publishedMatches, "stale notice final must not publish a successful reply")
}
