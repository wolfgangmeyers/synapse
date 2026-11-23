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

// inspectManagedOwnerBinding reads the Manager's actual Store claim without
// acquiring, renewing, or releasing the lease. It grants no production authority.
func inspectManagedOwnerBinding(t *testing.T, path string, binding store.EmbeddedBinding) store.EmbeddedBinding {
	t.Helper()
	read, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer read.Close()
	require.NoError(t, read.QueryRowContext(context.Background(), `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1 AND owner_id IS NOT NULL`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&binding.OwnerToken))
	require.Positive(t, binding.OwnerToken)
	return binding
}

// cwdPrivacyManagedSnapshot is read-only failure evidence for this scoped fixture batch.
func cwdPrivacyManagedSnapshot(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Logf("failure snapshot open_error=%v", err)
		return
	}
	defer raw.Close()
	for _, query := range []string{
		`SELECT json_group_array(json_object('agent',agent_id,'lifecycle',lifecycle_state,'error_code',last_error_code,'error_message',last_error_message)) FROM managed_agents`,
		`SELECT json_group_array(json_object('operation',o.operation_id,'state',o.state,'status',o.response_status)) FROM managed_http_operations o JOIN managed_agents a ON a.launch_operation_id=o.operation_id`,
		`SELECT json_group_array(json_object('agent',s.agent_id,'session',s.session_id,'head',(SELECT max(e.seq) FROM embedded_entries e WHERE e.agent_id=s.agent_id AND e.session_id=s.session_id AND e.epoch=s.epoch),'owner',s.owner_id,'token',s.owner_token,'lease',s.owner_expires_at)) FROM embedded_sessions s WHERE s.current=1`,
		`SELECT json_group_array(json_object('process',generation)) FROM embedded_process_generation_counter`,
		`SELECT json_group_array(json_object('agent',agent_id,'slot',slot,'kind',kind,'state',state,'process',process_generation,'token',claim_owner_token)) FROM embedded_restart_wake_slots`,
	} {
		var snapshot string
		err := raw.QueryRow(query).Scan(&snapshot)
		t.Logf("failure snapshot query=%s result=%s read_error=%v", query, snapshot, err)
	}
}

// Exercises the production Session runner, not the legacy callback fixture.
func TestEmbeddedProductionSessionColdReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	info, statErr := os.Stat(root)
	require.NoError(t, statErr)
	t.Logf("Cwd fixture source=embedded_session_test.go:t.TempDir path=%s before_mode=%#o", root, info.Mode().Perm())
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
	var calls, neutralCalls atomic.Int32
	var sawResponsivenessPrompt atomic.Bool
	reg.SetResponses([]providers.FauxResponseStep{func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		sys := ai.GetCurrentSystemPrompt(h.Messages)
		if strings.Contains(sys, "### Responsiveness & Communication") &&
			strings.Contains(sys, "always acknowledge the user with a brief conversational response first") {
			sawResponsivenessPrompt.Store(true)
		}
		user, ok := h.Messages[len(h.Messages)-1].(ai.UserMessage)
		require.True(t, ok)
		require.Equal(t, ai.ContentList{ai.TextContent{Text: "durable prompt"}}, user.Content)
		calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "persisted answer"}}, ai.StopStop)
	}, func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		user, ok := h.Messages[len(h.Messages)-1].(ai.UserMessage)
		require.True(t, ok)
		require.Equal(t, ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}, user.Content)
		neutralCalls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "neutral recovery final"}}, ai.StopStop)
	}})
	var processes, rpcs atomic.Int32
	newManager := func(db *store.Store) *Manager {
		m, e := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessFactory: &rootRedProcessFactory{calls: &processes}, RPCFactory: rootRedRPCFactory{calls: &rpcs}, EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
		require.NoError(t, e)
		return m
	}
	manager := newManager(db)
	defer func() {
		if t.Failed() {
			manager.mu.Lock()
			_, published := manager.embeddedOwners["production-root"]
			manager.mu.Unlock()
			t.Logf("failure snapshot owner_published=%t providers=%d", published, calls.Load())
			cwdPrivacyManagedSnapshot(t, dbPath)
		}
	}()
	request := LaunchRequest{Name: "production-root", Cwd: root, InitialInstruction: "durable prompt", Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "22222222-2222-4222-8222-222222222222"}
	launched, err := manager.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	require.Eventually(t, func() bool {
		saved, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && saved.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	binding = inspectManagedOwnerBinding(t, dbPath, binding)
	snap, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 3)
	require.Equal(t, "user", snap.Entries[1].Kind)
	require.Equal(t, "assistant", snap.Entries[2].Kind)
	require.EqualValues(t, 1, calls.Load())
	require.True(t, sawResponsivenessPrompt.Load(), "embedded worker must receive responsiveness instruction in system prompt")
	require.NoError(t, manager.Close(ctx))
	leaseRead, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	require.NoError(t, err)
	var expiryText string
	require.NoError(t, leaseRead.QueryRowContext(ctx, `SELECT owner_expires_at FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1 AND owner_token=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, binding.OwnerToken).Scan(&expiryText))
	require.NoError(t, leaseRead.Close())
	expiry, err := time.Parse(time.RFC3339Nano, expiryText)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, clock.Advance(expiry.Add(time.Millisecond).Sub(clock.Now())))
	db, err = store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	recovered := newManager(db)
	defer recovered.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, recovered)
	require.NoError(t, recovered.RecoverEmbeddedRoots(ctx))
	require.Eventually(t, func() bool {
		replay, e := recovered.LaunchEmbeddedRoot(ctx, request)
		return e == nil && replay.Replayed
	}, 3*time.Second, 10*time.Millisecond)
	// Owner publication is not a checked final. Wait for the exact current
	// process/owner wake, its checked origin, and the persisted neutral pair.
	ro, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()
	neutralBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var checked int
		e := ro.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots w JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation JOIN embedded_entries u ON u.agent_id=s.agent_id AND u.session_id=s.session_id AND u.epoch=s.epoch AND u.generation=s.generation AND u.seq=4 JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq WHERE s.agent_id=? AND s.session_id=? AND s.epoch=? AND s.generation=? AND s.current=1 AND s.owner_token=? AND w.claim_owner_token=s.owner_token AND w.slot='current' AND w.kind='root-idle' AND w.state='checked-complete' AND w.notice_id IS NULL AND w.process_generation=? AND u.kind='user' AND u.body=? AND f.seq=5 AND f.kind='assistant' AND json_extract(f.body,'$.stopReason')='stop'`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, binding.OwnerToken, recovered.embeddedStartupGeneration, neutralBody).Scan(&checked)
		return e == nil && checked == 1
	}, 3*time.Second, 10*time.Millisecond, "current neutral wake must have a checked stop final")
	reopened, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Len(t, reopened.Entries, len(snap.Entries)+2, "only the proven neutral pair may follow the immutable original")
	require.Equal(t, snap.Entries, reopened.Entries[:len(snap.Entries)], "original sequence, IDs, bodies and digests are immutable")
	require.Equal(t, neutralBody, reopened.Entries[3].Body)
	var final ai.AssistantMessage
	require.NoError(t, json.Unmarshal(reopened.Entries[4].Body, &final))
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "neutral recovery final"}}, final.Content)
	require.Equal(t, ai.StopStop, final.StopReason)
	for _, table := range []string{"embedded_effects", "embedded_acks", "embedded_reply_outbox", "embedded_interrupted_notices"} {
		var count int
		require.NoError(t, ro.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count))
		require.Zero(t, count, table)
	}
	require.EqualValues(t, 1, calls.Load(), "original provider turn must not replay")
	require.EqualValues(t, 1, neutralCalls.Load())
	require.Zero(t, processes.Load())
	require.Zero(t, rpcs.Load())
}

func TestThinkingOnlyTurnTreatedAsEmptyTurn(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()

	_, err := f.raw.Exec(`UPDATE managed_agent_policies SET goal_enabled=1,goal_text='test goal',desired_revision=1,applied_revision=1,projection_state='applied' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)

	// Case 1: Assistant message with only ai.ThinkingContent
	userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work on goal"}}})
	require.NoError(t, err)
	user, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", userBody)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user.Sequence, user.Digest))
	receipt, _, err := f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "thinking-empty-1", TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: 1})
	require.NoError(t, err)

	asstBody, err := json.Marshal(ai.AssistantMessage{
		Content:    ai.ContentList{ai.ThinkingContent{Thinking: "I am only thinking, emitting no action or text."}},
		StopReason: ai.StopStop,
	})
	require.NoError(t, err)
	final, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", asstBody)
	require.NoError(t, err)

	lane := f.m.laneFor(owner.binding.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	lane.consecutiveEmptyTurns.Store(0)

	p := &embeddedPersistence{
		turnSeq:    user.Sequence,
		turnDigest: user.Digest,
		seq:        final.Sequence,
		digest:     final.Digest,
	}

	err = f.m.evaluateEmbeddedTurn(ctx, owner.binding, p, &receipt, nil, nil)
	require.NoError(t, err)

	// Thinking-only turn must be treated as an empty turn: consecutiveEmptyTurns incremented to 1
	require.EqualValues(t, 1, lane.consecutiveEmptyTurns.Load(), "thinking-only turn must increment consecutiveEmptyTurns")

	// Case 2: Pointer variant *ai.ThinkingContent
	userPtrBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "pointer thinking turn"}}})
	require.NoError(t, err)
	userPtr, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", userPtrBody)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, userPtr.Sequence, userPtr.Digest))
	receiptPtr, _, err := f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "thinking-ptr-turn", TurnEntrySeq: userPtr.Sequence, TurnEntryDigest: userPtr.Digest, GoalRevision: 1})
	require.NoError(t, err)

	asstPtrBody, err := json.Marshal(ai.AssistantMessage{
		Content:    ai.ContentList{&ai.ThinkingContent{Thinking: "I am only thinking via pointer."}},
		StopReason: ai.StopStop,
	})
	require.NoError(t, err)
	finalPtr, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", asstPtrBody)
	require.NoError(t, err)

	lane.consecutiveEmptyTurns.Store(0)
	pPtr := &embeddedPersistence{
		turnSeq:    userPtr.Sequence,
		turnDigest: userPtr.Digest,
		seq:        finalPtr.Sequence,
		digest:     finalPtr.Digest,
	}
	err = f.m.evaluateEmbeddedTurn(ctx, owner.binding, pPtr, &receiptPtr, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, lane.consecutiveEmptyTurns.Load(), "pointer *ai.ThinkingContent must increment consecutiveEmptyTurns")

	// Case 3: Turn with actual text resets consecutiveEmptyTurns to 0
	user2Body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "next step"}}})
	require.NoError(t, err)
	user2, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", user2Body)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user2.Sequence, user2.Digest))
	receipt2, _, err := f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "text-turn-2", TurnEntrySeq: user2.Sequence, TurnEntryDigest: user2.Digest, GoalRevision: 1})
	require.NoError(t, err)

	asst2Body, err := json.Marshal(ai.AssistantMessage{
		Content:    ai.ContentList{ai.ThinkingContent{Thinking: "pondering"}, ai.TextContent{Text: "actual response"}},
		StopReason: ai.StopStop,
	})
	require.NoError(t, err)
	final2, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", asst2Body)
	require.NoError(t, err)

	p2 := &embeddedPersistence{
		turnSeq:    user2.Sequence,
		turnDigest: user2.Digest,
		seq:        final2.Sequence,
		digest:     final2.Digest,
	}

	err = f.m.evaluateEmbeddedTurn(ctx, owner.binding, p2, &receipt2, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 0, lane.consecutiveEmptyTurns.Load(), "turn with real text must reset consecutiveEmptyTurns to 0")

	// Case 4: Strike 3 (consecutiveEmptyTurns = 2 + thinking-only turn) trips circuit breaker, increments to 3, and returns without launching continuation
	user3Body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "strike 3 attempt"}}})
	require.NoError(t, err)
	user3, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "user", user3Body)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, owner.binding, user3.Sequence, user3.Digest))
	receipt3, _, err := f.db.CommitEmbeddedGoalAppliedReceipt(ctx, owner.binding, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "strike-3-turn", TurnEntrySeq: user3.Sequence, TurnEntryDigest: user3.Digest, GoalRevision: 1})
	require.NoError(t, err)

	asst3Body, err := json.Marshal(ai.AssistantMessage{
		Content:    ai.ContentList{&ai.ThinkingContent{Thinking: "thinking on strike 3"}},
		StopReason: ai.StopStop,
	})
	require.NoError(t, err)
	final3, err := f.db.AppendEmbeddedEntry(ctx, owner.binding, "assistant", asst3Body)
	require.NoError(t, err)

	lane.consecutiveEmptyTurns.Store(2)
	p3 := &embeddedPersistence{
		turnSeq:    user3.Sequence,
		turnDigest: user3.Digest,
		seq:        final3.Sequence,
		digest:     final3.Digest,
	}

	f.m.retireEmbeddedGoalTimer(owner.binding.AgentID)
	err = f.m.evaluateEmbeddedTurn(ctx, owner.binding, p3, &receipt3, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 3, lane.consecutiveEmptyTurns.Load(), "strike 3 must increment consecutiveEmptyTurns to 3")

	f.m.mu.Lock()
	timer := f.m.embeddedGoalTimers[owner.binding.AgentID]
	f.m.mu.Unlock()
	require.Nil(t, timer, "strike 3 circuit breaker must return without launching or scheduling continuation")
}

