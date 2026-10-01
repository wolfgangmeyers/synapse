package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// T6: an ordinary checked final can reserve one due continuation. The due
// turn never evaluates itself. DONE below belongs to a second accepted mail,
// not to wake text. Off/revision/cold negatives already belong to
// TestEmbeddedGoalContinuationDoneAndOffDoNotStart and
// TestEmbeddedColdReservedNotDoneInvalidatedBeforeDue (and the Store
// atomic-authority matrix). The API currently exposes policy, not a dueState
// or embedded verdict: those assertions therefore read durable original keys.
// DONE is not an off command; no unsupported policy cancellation is invented.
func TestEmbeddedGoalAPIDueVisibilityOrdinaryDoneRED(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700)) // only this fixture's private Cwd
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const agentID, sender = "goal-api-due-worker", "goal-api-original-sender"
	const firstMail = "a5555555-5555-4555-8555-555555555681"
	const laterMail = "a5555555-5555-4555-8555-555555555682"
	const goalText = "finish the API due slice"
	results := make(chan ai.ToolResultMessage, 2)
	prompts := make(chan ai.UserMessage, 2)
	var providerCalls atomic.Int32
	result := func(h ai.TranscriptContext, id string) {
		for _, msg := range h.Messages {
			if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == id {
				results <- r
				return
			}
		}
		t.Logf("provider continuation missing correlated result call_id=%s provider_calls=%d", id, providerCalls.Load())
	}
	final := func(text string) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: text}}, ai.StopStop)
	}
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": goalText}, "api-due-set")}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			result(h, "api-due-set")
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal_snooze", map[string]any{"duration_ms": 1000}, "api-due-snooze")}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			result(h, "api-due-snooze")
			return final("first sender-bound final")
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			var latest ai.UserMessage
			for _, msg := range h.Messages {
				if u, ok := msg.(ai.UserMessage); ok {
					latest = u
				}
			}
			prompts <- latest
			return final("one internal due final")
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			var latest ai.UserMessage
			for _, msg := range h.Messages {
				if u, ok := msg.(ai.UserMessage); ok {
					latest = u
				}
			}
			prompts <- latest
			return final("later sender-bound final")
		},
	})
	evaluated := make(chan store.EmbeddedGoalEvaluationKey, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var evaluations atomic.Int32
	m, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Clock: clock,
		Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedGoalEvaluator: func(c context.Context, key store.EmbeddedGoalEvaluationKey) (string, string, error) {
			n := evaluations.Add(1)
			evaluated <- key
			if n == 1 {
				select {
				case <-release:
					return store.EmbeddedGoalEvaluationNotDone, "more work remains", nil
				case <-c.Done():
					t.Logf("goal evaluator callback error=%v key=%+v", c.Err(), key)
					return "", "", c.Err()
				}
			}
			return store.EmbeddedGoalEvaluationDone, "ordinary later turn completed the goal", nil
		},
	})
	require.NoError(t, err)
	r, err := New(Options{Store: db, Managed: m, Host: "127.0.0.1", Port: 0, Clock: clock})
	require.NoError(t, err)
	var subscriber *session
	t.Cleanup(func() {
		unblock()
		// This subscriber has no WebSocket. Remove only the fixture-installed
		// entry before Router.Close closes real subscribers.
		r.mu.Lock()
		if subscribers := r.convos[sender]; subscribers["goal-test"] == subscriber {
			delete(subscribers, "goal-test")
			if len(subscribers) == 0 {
				delete(r.convos, sender)
			}
		}
		r.mu.Unlock()
		_ = r.Close(context.Background())
		_ = m.Close(context.Background())
	})
	require.NoError(t, r.Listen(ctx))
	base, _ := r.URLs()
	// Observe the existing conversation publisher, not a replacement publisher
	// or a second WebSocket harness. Preserve full bodies and sender binding.
	var eventMu sync.Mutex
	var events []domain.ConversationMessage
	subscriber = &session{router: r, sendOverride: func(fields map[string]any) error {
		if fields["type"] == "conversation_message" {
			eventMu.Lock()
			events = append(events, fields["message"].(domain.ConversationMessage))
			eventMu.Unlock()
		}
		return nil
	}}
	r.mu.Lock()
	r.convos[sender] = map[string]*session{"goal-test": subscriber}
	r.mu.Unlock()
	launched, err := m.Launch(ctx, managed.LaunchRequest{Name: agentID, Cwd: root, Runtime: "embedded", RequestID: "a5555555-5555-4555-8555-555555555680"})
	require.NoError(t, err)
	b := store.EmbeddedBinding{AgentID: agentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer ro.Close()
	postMail := func(id, body string) {
		t.Helper()
		payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":%q,"created_at":"2030-01-02T03:04:05.000Z"}`, id, sender, agentID, body)
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", strings.NewReader(payload))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode)
	}
	goalAPI := func() {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/managed-agents/"+agentID+"/goal", nil)
		require.NoError(t, e)
		resp, e := http.DefaultClient.Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		var view map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&view))
		require.Equal(t, http.StatusOK, resp.StatusCode, "%v", view)
		state, ok := view["goal_state"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, state["enabled"])
		require.Equal(t, goalText, state["goal"])
		require.Equal(t, float64(1), state["desired_revision"])
		require.Equal(t, float64(1), state["applied_revision"])
		require.Equal(t, "applied", state["projection_state"])
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		require.NoError(t, ro.QueryRowContext(ctx, query, args...).Scan(&n))
		return n
	}
	checkedFinal := func(text string) (int64, string) {
		t.Helper()
		var seq int64
		var digest string
		require.Eventually(t, func() bool {
			return ro.QueryRowContext(ctx, `SELECT seq,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='assistant' AND json_extract(body,'$.stopReason')='stop' AND json_extract(body,'$.content[0].text')=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, text).Scan(&seq, &digest) == nil
		}, time.Second, 10*time.Millisecond, "checked SDK final missing: %s", text)
		require.NotEmpty(t, digest)
		return seq, digest
	}
	assertReply := func(mailID, text string) {
		t.Helper()
		var id string
		require.Eventually(t, func() bool {
			return ro.QueryRowContext(ctx, `SELECT o.reply_id FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=? AND o.sender=? AND o.recipient=? AND o.body=?`, mailID, agentID, sender, text).Scan(&id) == nil
		}, time.Second, 10*time.Millisecond)
		require.Eventually(t, func() bool {
			eventMu.Lock()
			defer eventMu.Unlock()
			for _, event := range events {
				if event.ID == id && event.From == agentID && event.To == sender && event.Body == text {
					return true
				}
			}
			return false
		}, time.Second, 10*time.Millisecond, "actual conversation event body/binding missing")
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/conversations?owner="+sender+"&peer="+agentID, nil)
		require.NoError(t, e)
		resp, e := http.DefaultClient.Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var view struct {
			Messages []domain.ConversationMessage `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&view))
		found := false
		for _, msg := range view.Messages {
			if msg.ID == id && msg.From == agentID && msg.To == sender && msg.Body == text {
				found = true
			}
		}
		require.True(t, found, "event must also be retrievable from real conversation API")
	}
	postMail(firstMail, "set and snooze your own goal")
	for _, id := range []string{"api-due-set", "api-due-snooze"} {
		select {
		case got := <-results:
			require.Equal(t, id, got.ToolCallID)
			require.Equal(t, map[string]string{"api-due-set": "goal", "api-due-snooze": "goal_snooze"}[id], got.ToolName)
			require.False(t, got.IsError)
			require.Len(t, got.Content, 1)
			text, ok := got.Content[0].(ai.TextContent)
			require.True(t, ok)
			table := map[string]string{"api-due-set": "embedded_goal_calls", "api-due-snooze": "embedded_goal_snooze_calls"}[id]
			var persisted string
			require.NoError(t, ro.QueryRowContext(ctx, `SELECT result_json FROM `+table+` WHERE call_id=?`, id).Scan(&persisted))
			require.JSONEq(t, persisted, text.Text, "SDK result must equal the committed original call result")
		case <-ctx.Done():
			// Read before cleanup, using a non-expired context for diagnostics only.
			diagnosticCtx := context.Background()
			policy, policyErr := db.ManagedGoalPolicyFor(diagnosticCtx, agentID)
			t.Logf("missing SDK result call_id=%s clock=%s provider_calls=%d evaluations=%d policy=%+v policy_error=%v router_process=%d incarnation=%s", id, clock.Now(), providerCalls.Load(), evaluations.Load(), policy, policyErr, r.embeddedProcessGeneration, "Store-owned incarnation")
			for _, query := range []string{
				`SELECT json_group_array(json_object('agent',agent_id,'lifecycle',lifecycle_state,'process_pid',process_pid,'runtime',runtime_generation,'launch',launch_operation_id,'error',last_error_code,'message',last_error_message)) FROM managed_agents`,
				`SELECT json_group_array(json_object('agent',agent_id,'session',session_id,'epoch',epoch,'runtime',generation,'owner',owner_id,'token',owner_token,'expires',owner_expires_at)) FROM embedded_sessions WHERE current=1`,
				`SELECT json_group_array(json_object('process',generation)) FROM embedded_process_generation_counter`,
				`SELECT json_group_array(json_object('process',generation,'incarnation',incarnation_key)) FROM embedded_process_generations`,
				`SELECT json_group_array(json_object('seq',seq,'kind',kind,'digest',digest,'body',CAST(body AS TEXT))) FROM embedded_entries`,
				`SELECT json_group_array(json_object('id',effect_id,'state',state,'intent',CAST(intent AS TEXT),'result',CAST(result AS TEXT))) FROM embedded_effects`,
				`SELECT json_group_array(json_object('id',call_id,'state',state,'args',args_json,'result',result_json)) FROM embedded_goal_calls`,
				`SELECT json_group_array(json_object('id',call_id,'user_seq',user_seq,'revision',goal_revision,'args',args_json,'result',result_json)) FROM embedded_goal_snooze_calls`,
			} {
				var snapshot string
				queryErr := ro.QueryRowContext(diagnosticCtx, query).Scan(&snapshot)
				t.Logf("missing SDK result query=%s result=%s query_error=%v", query, snapshot, queryErr)
			}
			t.Fatal("checked SDK tool result missing")
		}
	}
	var key store.EmbeddedGoalEvaluationKey
	select {
	case key = <-evaluated:
	case <-ctx.Done():
		t.Fatal("ordinary mail did not evaluate")
	}
	seq, digest := checkedFinal("first sender-bound final")
	require.Equal(t, seq, key.FinalSeq)
	require.Equal(t, digest, key.FinalDigest)
	require.EqualValues(t, 1, key.GoalRevision)
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_goal_applied_receipts WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND turn_entry_seq=? AND turn_entry_digest=? AND goal_revision=? AND goal_enabled=1`, agentID, b.SessionID, b.Epoch, b.Generation, key.UserSeq, key.UserDigest, key.GoalRevision))
	goalAPI()
	assertReply(firstMail, "first sender-bound final") // before evaluation release
	unblock()
	var due, state, disposition string
	readReservation := func() bool {
		return ro.QueryRowContext(ctx, `SELECT due_at,state,disposition FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=?`, agentID, b.SessionID, b.Epoch, b.Generation, key.UserSeq, key.UserDigest, key.GoalRevision).Scan(&due, &state, &disposition) == nil
	}
	require.Eventually(t, readReservation, time.Second, 10*time.Millisecond)
	require.Equal(t, clock.Now().Add(time.Second).Format("2006-01-02T15:04:05.000Z"), due)
	require.Equal(t, "reserved", state)
	require.Equal(t, "pending", disposition)
	require.NoError(t, clock.Advance(999*time.Millisecond))
	// Also gives the async verdict task time to install its real fixed timer.
	require.Never(t, func() bool { return providerCalls.Load() != 3 }, 100*time.Millisecond, 10*time.Millisecond)
	require.True(t, readReservation())
	require.Equal(t, "reserved", state)
	goalAPI()
	require.NoError(t, clock.Advance(time.Millisecond))
	select {
	case prompt := <-prompts:
		require.Equal(t, ai.ContentList{ai.TextContent{Text: "Continue working on the current goal."}}, prompt.Content)
	case <-ctx.Done():
		t.Fatal("original-key due scheduler did not deliver")
	}
	wakeSeq, _ := checkedFinal("one internal due final")
	require.Greater(t, wakeSeq, key.FinalSeq)
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_entries u JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest WHERE u.agent_id=? AND u.session_id=? AND u.epoch=? AND u.generation=? AND u.seq=? AND u.kind='user' AND json_extract(u.body,'$.content[0].text')='Continue working on the current goal.'`, agentID, b.SessionID, b.Epoch, b.Generation, wakeSeq-1), "due delivery must persist its exact checked SDK user/provider entry")
	require.True(t, readReservation())
	require.Equal(t, "started", state)
	require.Equal(t, "unknown", disposition)
	var started string
	require.NoError(t, ro.QueryRowContext(ctx, `SELECT started_at FROM embedded_goal_continuations WHERE agent_id=?`, agentID).Scan(&started))
	require.Equal(t, due, started)
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, agentID))
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND user_seq=? AND user_digest=? AND final_seq=? AND final_digest=? AND state='NOT_DONE'`, agentID, key.UserSeq, key.UserDigest, key.FinalSeq, key.FinalDigest))
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_reply_outbox WHERE sender=?`, agentID), "due control turn must not reply")
	goalAPI()
	eventMu.Lock()
	automaticReplies := 0
	for _, event := range events {
		if event.From == agentID {
			automaticReplies++
		}
	}
	eventMu.Unlock()
	require.Equal(t, 1, automaticReplies, "due control turn must publish no unsolicited conversation final")
	require.NoError(t, clock.Advance(2*time.Second))
	require.Never(t, func() bool { return providerCalls.Load() != 4 || evaluations.Load() != 1 }, 100*time.Millisecond, 10*time.Millisecond, "due wake must not reschedule or reevaluate")
	// Genuine boundary assertion: a new accepted ordinary mail must capture its
	// own applied receipt and evaluate DONE. If the current binding omits that
	// receipt, this remains RED here; no Store mutation or text-derived DONE.
	postMail(laterMail, "ordinary later work is complete")
	select {
	case prompt := <-prompts:
		encoded, e := json.Marshal(prompt.Content)
		require.NoError(t, e)
		require.Contains(t, string(encoded), laterMail)
		require.Contains(t, string(encoded), sender)
	case <-ctx.Done():
		t.Fatal("later accepted ordinary mail did not enter SDK")
	}
	laterSeq, laterDigest := checkedFinal("later sender-bound final")
	assertReply(laterMail, "later sender-bound final")
	var later store.EmbeddedGoalEvaluationKey
	select {
	case later = <-evaluated:
	case <-time.After(time.Second):
		t.Fatal("ordinary-later-DONE boundary unsupported: effect-free accepted mail lacks current-turn applied goal receipt/evaluation")
	}
	require.Greater(t, later.UserSeq, key.FinalSeq)
	require.Equal(t, laterSeq, later.FinalSeq)
	require.Equal(t, laterDigest, later.FinalDigest)
	require.Equal(t, key.GoalRevision, later.GoalRevision)
	require.NotEqual(t, key.UserDigest, later.UserDigest)
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_goal_applied_receipts WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND turn_entry_seq=? AND turn_entry_digest=? AND goal_revision=? AND goal_enabled=1`, agentID, b.SessionID, b.Epoch, b.Generation, later.UserSeq, later.UserDigest, later.GoalRevision), "effect-free ordinary work must bind its own applied policy receipt")
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND user_seq=? AND user_digest=? AND final_seq=? AND final_digest=? AND state='NOT_DONE'`, agentID, key.UserSeq, key.UserDigest, key.FinalSeq, key.FinalDigest), "later DONE cannot relabel original key1")
	require.Eventually(t, func() bool {
		return count(`SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND user_seq=? AND user_digest=? AND final_seq=? AND final_digest=? AND state='DONE'`, agentID, later.UserSeq, later.UserDigest, later.FinalSeq, later.FinalDigest) == 1
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, 1, count(`SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, agentID), "DONE must not allocate another continuation")
	goalAPI()
	require.NoError(t, clock.Advance(time.Second))
	require.Never(t, func() bool { return providerCalls.Load() != 5 || evaluations.Load() != 2 }, 100*time.Millisecond, 10*time.Millisecond)
}
