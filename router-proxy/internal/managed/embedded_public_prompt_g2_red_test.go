package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// G2: exercise the public Manager admission, not runEmbeddedSessionPrompt or a test runner.
// The HTTP launch parser currently rejects runtime and has no prompt route; see report.
func TestEmbeddedPublicManagerPromptColdReopenRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	info, statErr := os.Stat(root)
	require.NoError(t, statErr)
	t.Logf("Cwd fixture source=embedded_public_prompt_g2_red_test.go:t.TempDir path=%s before_mode=%#o", root, info.Mode().Perm())
	// Only this test-owned Cwd, never shared TMPDIR or a production root.
	require.NoError(t, os.Chmod(root, 0700))
	info, statErr = os.Stat(root)
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	t.Logf("Cwd fixture path=%s after_mode=%#o", root, info.Mode().Perm())
	dbPath := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	db, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var calls, neutralCalls, processes, rpcs atomic.Int32
	var currentDB = db
	var binding store.EmbeddedBinding
	first := "Describe how the first turn changes the plan."
	second := "Now revise that plan with an explicit rollback step."
	third := "After restart, list the plan and rollback in order."
	prompts := []string{first, second, third}
	bound := make(chan struct{})
	step := func(history ai.TranscriptContext, options *ai.SimpleStreamOptions, state *providers.FauxState, model *ai.Model) *ai.AssistantMessage {
		<-bound
		return g2Response(history, options, state, model, &calls, &currentDB, &binding, dbPath, prompts)
	}
	neutralStep := func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		user, ok := h.Messages[len(h.Messages)-1].(ai.UserMessage)
		require.True(t, ok)
		require.Equal(t, ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}, user.Content)
		neutralCalls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "neutral recovery final"}}, ai.StopStop)
	}
	reg.SetResponses([]providers.FauxResponseStep{step, step, neutralStep, step})
	newManager := func(s *store.Store) *Manager {
		m, e := NewManager(ManagerOptions{Store: s, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessFactory: &rootRedProcessFactory{calls: &processes}, RPCFactory: rootRedRPCFactory{calls: &rpcs}, EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
		require.NoError(t, e)
		return m
	}
	manager := newManager(db)
	defer func() {
		if t.Failed() {
			manager.mu.Lock()
			_, published := manager.embeddedOwners["g2-root"]
			manager.mu.Unlock()
			t.Logf("failure snapshot owner_published=%t providers=%d", published, calls.Load())
			cwdPrivacyManagedSnapshot(t, dbPath)
		}
	}()
	request := LaunchRequest{Name: "g2-root", Cwd: root, InitialInstruction: first, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "33333333-3333-4333-8333-333333333333"}
	launched, err := manager.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	binding = store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	close(bound)
	require.Eventually(t, func() bool {
		saved, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && saved.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	binding = inspectManagedOwnerBinding(t, dbPath, binding)
	initial, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, []string{"system", "user", "assistant"}, g2Kinds(initial))
	require.Contains(t, string(initial.Entries[2].Body), "answer for turn 1")
	require.EqualValues(t, 1, calls.Load())
	replay, err := manager.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.EqualValues(t, 1, calls.Load(), "launch replay must not rerun provider")
	// A failed checked append leaves an ambiguous request that cannot replay.
	faultDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = faultDB.Exec(`CREATE TRIGGER g2_user_fault BEFORE INSERT ON embedded_entries WHEN NEW.kind='user' BEGIN SELECT RAISE(ABORT,'g2 user append fault'); END`)
	require.NoError(t, err)
	faultID := "55555555-5555-4555-8555-555555555555"
	require.Error(t, manager.AdmitPromptWithRequestID(ctx, binding.AgentID, second, faultID))
	require.EqualValues(t, 1, calls.Load(), "no provider before checked append")
	_, err = faultDB.Exec(`DROP TRIGGER g2_user_fault`)
	require.NoError(t, err)
	require.NoError(t, faultDB.Close())
	require.ErrorContains(t, manager.AdmitPromptWithRequestID(ctx, binding.AgentID, second, faultID), "outcome unknown")
	// The same request ID is durable, including across a distinct Manager/Store.
	turnID := "44444444-4444-4444-8444-444444444444"
	require.NoError(t, manager.AdmitPromptWithRequestID(ctx, binding.AgentID, second, turnID))
	require.Eventually(t, func() bool { snap, e := db.ReopenEmbedded(ctx, binding); return e == nil && len(snap.Entries) == 5 }, 3*time.Second, 10*time.Millisecond)
	secondTurn, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Contains(t, string(secondTurn.Entries[4].Body), "answer for turn 2")
	require.EqualValues(t, 2, calls.Load())
	require.NoError(t, manager.AdmitPromptWithRequestID(ctx, binding.AgentID, second, turnID))
	require.ErrorContains(t, manager.AdmitPromptWithRequestID(ctx, binding.AgentID, "different", turnID), "conflicting embedded prompt request")
	require.EqualValues(t, 2, calls.Load())
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, db.Close())
	db, err = store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	currentDB = db
	recovered := newManager(db)
	defer recovered.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, recovered)
	stale := binding
	stale.Epoch++
	_, err = db.AcquireEmbeddedOwner(ctx, stale, "g2-live-lease-probe", embeddedOwnerTTL)
	require.Error(t, err)
	require.ErrorContains(t, recovered.AdmitPromptWithRequestID(ctx, binding.AgentID, third, "66666666-6666-4666-8666-666666666666"), "managed agent is not ready")
	require.EqualValues(t, 2, calls.Load())
	ro, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()
	require.NoError(t, recovered.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool {
		saved, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && saved.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	binding = inspectManagedOwnerBinding(t, dbPath, binding)
	neutralBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var checked int
		e := ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots w JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation JOIN embedded_entries u ON u.agent_id=s.agent_id AND u.session_id=s.session_id AND u.epoch=s.epoch AND u.generation=s.generation AND u.seq=6 JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq WHERE s.agent_id=? AND s.session_id=? AND s.epoch=? AND s.generation=? AND s.current=1 AND s.owner_token=? AND w.claim_owner_token=s.owner_token AND w.slot='current' AND w.kind='root-idle' AND w.state='checked-complete' AND w.notice_id IS NULL AND w.process_generation=? AND u.kind='user' AND u.body=? AND f.seq=7 AND f.kind='assistant' AND json_extract(f.body,'$.stopReason')='stop'`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, binding.OwnerToken, recovered.embeddedStartupGeneration, neutralBody).Scan(&checked)
		return e == nil && checked == 1
	}, 3*time.Second, 10*time.Millisecond)
	cold, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Len(t, cold.Entries, len(secondTurn.Entries)+2, "only the proven neutral pair may follow")
	require.Equal(t, secondTurn.Entries, cold.Entries[:len(secondTurn.Entries)], "original prefix and digests must remain immutable")
	require.Equal(t, neutralBody, cold.Entries[5].Body)
	var neutralFinal ai.AssistantMessage
	require.NoError(t, json.Unmarshal(cold.Entries[6].Body, &neutralFinal))
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "neutral recovery final"}}, neutralFinal.Content)
	require.Equal(t, ai.StopStop, neutralFinal.StopReason)
	require.EqualValues(t, 1, neutralCalls.Load())
	require.EqualValues(t, 2, calls.Load(), "cold recovery must not replay prior turns")
	require.NoError(t, recovered.AdmitPromptWithRequestID(ctx, binding.AgentID, second, turnID))
	require.ErrorContains(t, recovered.AdmitPromptWithRequestID(ctx, binding.AgentID, "different", turnID), "conflicting embedded prompt request")
	require.EqualValues(t, 2, calls.Load())
	require.NoError(t, recovered.AdmitPrompt(ctx, binding.AgentID, third))
	require.Eventually(t, func() bool { snap, e := db.ReopenEmbedded(ctx, binding); return e == nil && len(snap.Entries) == 9 }, 3*time.Second, 10*time.Millisecond)
	snap, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, []string{"system", "user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant"}, g2Kinds(snap))
	require.Equal(t, cold.Entries, snap.Entries[:len(cold.Entries)])
	require.Contains(t, string(snap.Entries[8].Body), "answer for turn 3")
	require.EqualValues(t, 3, calls.Load())
	require.EqualValues(t, 1, neutralCalls.Load())
	for _, table := range []string{"embedded_effects", "embedded_acks", "embedded_reply_outbox", "embedded_interrupted_notices"} {
		var count int
		require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count))
		require.Zero(t, count, table)
	}
	require.Zero(t, processes.Load())
	require.Zero(t, rpcs.Load())
	files, fileErr := os.ReadDir(filepath.Join(root, "sessions"))
	if !os.IsNotExist(fileErr) {
		require.NoError(t, fileErr)
		require.Empty(t, files, "no stock Pi JSONL")
	}
}

func mustMessageJSON(message ai.Message) []byte {
	body, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	return body
}

func g2Kinds(snap store.EmbeddedSnapshot) []string {
	kinds := make([]string, 0, len(snap.Entries))
	for _, entry := range snap.Entries {
		kinds = append(kinds, entry.Kind)
	}
	return kinds
}

func g2Response(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model, calls *atomic.Int32, db **store.Store, binding *store.EmbeddedBinding, dbPath string, prompts []string) *ai.AssistantMessage {
	index := int(calls.Add(1)) - 1
	if index >= len(prompts) {
		panic("duplicate provider request")
	}
	reader, err := sql.Open("sqlite", dbPath)
	if err != nil {
		panic(err)
	}
	defer reader.Close()
	checked := *binding
	if err := reader.QueryRow(`SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, checked.AgentID, checked.SessionID, checked.Epoch, checked.Generation).Scan(&checked.OwnerToken); err != nil {
		panic(err)
	}
	snap, err := (*db).ReopenEmbedded(context.Background(), checked)
	if err != nil {
		panic(err)
	}
	users := 0
	for _, entry := range snap.Entries {
		if entry.Kind == "user" {
			if string(entry.Body) == string(mustMessageJSON(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}})) {
				continue // Separately proven checked neutral wake, never an original turn.
			}
			users++
			if users == index+1 && !strings.Contains(string(entry.Body), prompts[index]) {
				panic("wrong durable prompt")
			}
		}
	}
	if users != index+1 {
		panic("provider before checked prompt append")
	}
	for _, prompt := range prompts[:index+1] {
		found := false
		for _, message := range history.Messages {
			if message.MessageRole() == "user" && strings.Contains(string(mustMessageJSON(message)), prompt) {
				found = true
			}
		}
		if !found {
			panic("ordered context missing")
		}
	}
	return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "answer for turn " + string(rune('1'+index))}}, ai.StopStop)
}
