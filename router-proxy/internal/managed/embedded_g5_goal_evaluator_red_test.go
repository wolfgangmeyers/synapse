package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Test-only evaluator contract. The Manager invokes the fake only after a
// durable claim at the checked final boundary; the test never invokes it.
type g5NotDoneEvaluator struct {
	mu    sync.Mutex
	calls []g5EvaluationKey
}

type g5EvaluationKey struct {
	agent, session, digest   string
	epoch, userSeq, revision int64
}

func (f *g5NotDoneEvaluator) Evaluate(key g5EvaluationKey) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	return "NOT_DONE", "more work remains"
}

func (f *g5NotDoneEvaluator) Calls() []g5EvaluationKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]g5EvaluationKey(nil), f.calls...)
}

// RED at the first absent evaluator claim/invocation, after proving the applied
// receipt and final assistant belong to the initiating saved SDK user turn.
// No external LLM, network, sender, outbox or mail ACK is involved.
func TestEmbeddedG5RootFinalEvaluatesOnceAndNotDoneContinuesOnceRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const goal = "finish this root goal"
	const callID = "g5-t6-evaluator-goal"
	requested := make(chan int, 4)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requested <- 1
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": goal}, callID)}, ai.StopToolUse)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requested <- 2
			<-release
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial root final"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requested <- 3
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "internal continuation final"}}, ai.StopStop)
		},
	})
	fake := &g5NotDoneEvaluator{}
	var binding store.EmbeddedBinding
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedGoalEvaluator: func(_ context.Context, k store.EmbeddedGoalEvaluationKey) (string, string, error) {
			outcome, reason := fake.Evaluate(g5EvaluationKey{agent: binding.AgentID, session: binding.SessionID, epoch: binding.Epoch, userSeq: k.UserSeq, digest: k.UserDigest, revision: k.GoalRevision})
			return outcome, reason, nil
		},
	})
	require.NoError(t, err)
	defer func() { unblock(); _ = m.Close(ctx) }()
	startupProcess := setEmbeddedRecoveryGeneration(t, ctx, db, m)
	readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer readOnly.Close()
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-t6-evaluator-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set your own goal", RequestID: "a5555555-5555-4555-8555-555555555582"})
	require.NoError(t, err)
	binding = store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	for _, want := range []int{1, 2} {
		select {
		case got := <-requested:
			require.Equal(t, want, got)
		case <-time.After(4 * time.Second):
			t.Fatalf("SDK provider request %d did not arrive", want)
		}
	}
	// This is an independent SQLite connection while the next provider response
	// remains held. Desired policy state alone does not satisfy this gate.
	var key g5EvaluationKey
	var enabled int
	var receiptGoal string
	var receiptCount int
	err = readOnly.QueryRowContext(ctx, `SELECT turn_entry_seq,turn_entry_digest,goal_revision,goal_enabled,goal_text
		FROM embedded_goal_applied_receipts WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`,
		binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation,
	).Scan(&key.userSeq, &key.digest, &key.revision, &enabled, &receiptGoal)
	require.NoError(t, err, "receipt gate failed before evaluator assertions")
	key.agent, key.session, key.epoch = binding.AgentID, binding.SessionID, binding.Epoch
	require.EqualValues(t, 2, key.userSeq)
	require.EqualValues(t, 1, key.revision)
	require.Equal(t, 1, enabled)
	require.Equal(t, goal, receiptGoal)
	binding = inspectManagedOwnerBinding(t, path, binding)
	snapshot, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, "user", snapshot.Entries[1].Kind)
	require.Equal(t, snapshot.Entries[1].Sequence, key.userSeq)
	require.Equal(t, snapshot.Entries[1].Digest, key.digest)
	require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_applied_receipts WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND turn_entry_seq=? AND turn_entry_digest=? AND goal_revision=?`, key.agent, key.session, key.epoch, binding.Generation, key.userSeq, key.digest, key.revision).Scan(&receiptCount))
	require.Equal(t, 1, receiptCount)
	defer func() {
		if !t.Failed() {
			return
		}
		m.mu.Lock()
		owner := m.embeddedOwners[binding.AgentID]
		m.mu.Unlock()
		t.Logf("precleanup evaluator snapshot captured_startup_process=%d owner=%+v calls=%+v", startupProcess, owner.binding, fake.Calls())
		for _, query := range []struct{ label, sql string }{
			{"owner/process/lifecycle", `SELECT json_object('owner_token',s.owner_token,'owner',s.owner_id,'session',s.session_id,'epoch',s.epoch,'generation',s.generation,'process',(SELECT generation FROM embedded_process_generation_counter WHERE singleton=1),'lifecycle',a.lifecycle_state,'error_code',a.last_error_code,'error_message',a.last_error_message) FROM embedded_sessions s JOIN managed_agents a ON a.agent_id=s.agent_id WHERE s.agent_id=? AND s.current=1`},
			{"policy", `SELECT json_object('desired',desired_revision,'applied',applied_revision,'enabled',goal_enabled,'projection',projection_state) FROM managed_agent_policies WHERE agent_id=?`},
			{"entries/SDK stamps", `SELECT json_group_array(json_object('seq',seq,'kind',kind,'digest',digest,'parent',parent_digest,'body',CAST(body AS TEXT))) FROM (SELECT * FROM embedded_entries WHERE agent_id=? ORDER BY seq)`},
			{"goal DOMAIN", `SELECT json_group_array(json_object('call',call_id,'state',state,'args',args_json,'result',result_json)) FROM embedded_goal_calls WHERE agent_id=?`},
			{"effects", `SELECT json_group_array(json_object('id',effect_id,'state',state,'intent',CAST(intent AS TEXT),'result',CAST(result AS TEXT))) FROM embedded_effects WHERE agent_id=?`},
		} {
			var state string
			readErr := readOnly.QueryRowContext(ctx, query.sql, binding.AgentID).Scan(&state)
			t.Logf("precleanup evaluator snapshot=%s state=%s read_error=%v", query.label, state, readErr)
		}
	}()
	unblock()
	require.Eventually(t, func() bool {
		saved, reopenErr := db.ReopenEmbedded(ctx, binding)
		if reopenErr != nil || len(saved.Entries) == 0 {
			return false
		}
		// A fast continuation may already be the head. The original checked
		// final remains durable evidence in this exact saved binding.
		for _, entry := range saved.Entries {
			if entry.Kind != "assistant" || entry.Sequence <= key.userSeq {
				continue
			}
			message, decodeErr := ai.UnmarshalMessage(entry.Body)
			final, ok := message.(ai.AssistantMessage)
			if decodeErr != nil || !ok || final.StopReason != ai.StopStop {
				continue
			}
			content, encodeErr := json.Marshal(final.Content)
			expected, expectedErr := json.Marshal(ai.ContentList{ai.TextContent{Text: "initial root final"}})
			if encodeErr == nil && expectedErr == nil && string(content) == string(expected) {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "initial assistant must be checked before evaluator")

	// The injected fake must be invoked by the Manager, not by this test.
	require.Eventually(t, func() bool { return len(fake.Calls()) == 1 }, 300*time.Millisecond, 10*time.Millisecond,
		"G5/T6 RED: checked final and applied receipt reached; embedded evaluator not invoked")
	// Assert its exact key, then use request 3 as the durable settlement gate.
	// No direct Prompt, mail sender, or synthetic ACK.
	require.Equal(t, []g5EvaluationKey{key}, fake.Calls())
	// Invocation is not settlement: the fake records its call before the
	// Manager commits the verdict. Request 3 follows that commit.
	select {
	case got := <-requested:
		require.Equal(t, 3, got, "exactly one internal provider continuation")
	case <-time.After(3 * time.Second):
		t.Fatal("NOT_DONE did not initiate internal SDK continuation")
	}
	var verdict string
	require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT state FROM embedded_goal_evaluations WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=? AND owner_token=?`, key.agent, key.session, key.epoch, key.userSeq, key.digest, key.revision, binding.OwnerToken).Scan(&verdict))
	require.Equal(t, store.EmbeddedGoalEvaluationNotDone, verdict)
	require.Eventually(t, func() bool {
		saved, reopenErr := db.ReopenEmbedded(ctx, binding)
		if reopenErr != nil || len(saved.Entries) == 0 {
			return false
		}
		last := saved.Entries[len(saved.Entries)-1]
		return last.Kind == "assistant" && strings.Contains(string(last.Body), "internal continuation final")
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, []g5EvaluationKey{key}, fake.Calls(), "continuation final must not start another evaluation")
	select {
	case <-requested:
		t.Fatal("chained provider continuation")
	case <-time.After(100 * time.Millisecond):
	}
}
