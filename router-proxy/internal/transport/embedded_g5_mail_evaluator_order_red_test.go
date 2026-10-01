package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The evaluator is held after the checked mail final. The router must have
// published that final to the original sender before NOT_DONE can continue.
func TestEmbeddedG5MailReplyBeforeBlockedEvaluationRED(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const agent, sender, other = "g5-mail-goal-worker", "g5-original-sender", "g5-other-sender"
	const mailID, callID = "a5555555-5555-4555-8555-555555555592", "g5-mail-goal-call"
	type observation struct {
		step   int
		result *ai.ToolResultMessage
	}
	provider := make(chan observation, 4)
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			t.Logf("mail SDK request step=1 messages=%v output=toolUse", h.Messages)
			provider <- observation{step: 1}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "finish own mail goal"}, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var result *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == callID {
					copy := r
					result = &copy
				}
			}
			t.Logf("mail SDK continuation step=2 result=%+v output=stop", result)
			provider <- observation{step: 2, result: result}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "first addressed final"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			provider <- observation{step: 3}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "internal continuation final"}}, ai.StopStop)
		},
	})
	type evaluation struct {
		key store.EmbeddedGoalEvaluationKey
	}
	entered := make(chan evaluation, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	m, err := managed.NewManager(managed.ManagerOptions{CodexCredentialStore: testfixture.CodexAdmission(t),
		Store: db, Manifest: manifest,
		Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		EmbeddedGoalEvaluator: func(c context.Context, k store.EmbeddedGoalEvaluationKey) (string, string, error) {
			entered <- evaluation{key: k}
			select {
			case <-release:
				return store.EmbeddedGoalEvaluationNotDone, "more work", nil
			case <-c.Done():
				return "", "", c.Err()
			}
		},
	})
	require.NoError(t, err)
	router, err := New(Options{Store: db, Managed: m})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	require.Positive(t, router.embeddedProcessGeneration)
	server := httptest.NewServer(router)
	defer func() {
		if !t.Failed() {
			return
		}
		diagnostic := context.Background()
		row, rowErr := db.ManagedAgent(diagnostic, agent)
		policy, policyErr := db.ManagedGoalPolicyFor(diagnostic, agent)
		mail, mailErr := db.MessageFor(diagnostic, mailID)
		t.Logf("mail evaluation process=%d incarnation=%s row=%+v row_error=%v policy=%+v policy_error=%v DOMAIN=%+v mail_error=%v", router.embeddedProcessGeneration, "Store-owned incarnation", row, rowErr, policy, policyErr, mail, mailErr)
		read, openErr := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if openErr != nil {
			t.Logf("mail evaluation open_error=%v", openErr)
			return
		}
		defer read.Close()
		for _, table := range []string{"embedded_sessions", "embedded_entries", "embedded_effects", "embedded_goal_calls", "embedded_attempts", "embedded_acks", "embedded_nonmail_provider_entries", "embedded_goal_applied_receipts", "embedded_reply_outbox"} {
			rows, queryErr := read.QueryContext(diagnostic, "SELECT * FROM "+table)
			if queryErr != nil {
				t.Logf("mail evaluation table=%s query_error=%v", table, queryErr)
				continue
			}
			columns, columnErr := rows.Columns()
			t.Logf("mail evaluation table=%s columns=%v error=%v", table, columns, columnErr)
			for rows.Next() {
				values := make([]any, len(columns))
				targets := make([]any, len(columns))
				for i := range values {
					targets[i] = &values[i]
				}
				scanErr := rows.Scan(targets...)
				for i, value := range values {
					if body, ok := value.([]byte); ok {
						values[i] = string(body)
					}
				}
				t.Logf("mail evaluation table=%s values=%v error=%v", table, values, scanErr)
			}
			t.Logf("mail evaluation table=%s rows_error=%v", table, rows.Err())
			_ = rows.Close()
		}
	}()
	t.Cleanup(func() { unblock(); server.Close(); _ = router.Close(context.Background()); _ = db.Close() })
	launched, err := m.Launch(ctx, managed.LaunchRequest{Name: agent, Cwd: root, Runtime: "embedded", RequestID: "a5555555-5555-4555-8555-555555555591"})
	require.NoError(t, err)
	b := store.EmbeddedBinding{AgentID: agent, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	role, err := db.EmbeddedSessionRole(ctx, b)
	require.NoError(t, err)
	require.Equal(t, "worker", role)
	require.Eventually(t, func() bool { a, e := db.ManagedAgent(ctx, agent); return e == nil && a.LifecycleState == "running" }, 3*time.Second, 10*time.Millisecond)
	payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":"set your own goal and reply","created_at":"2026-09-27T00:00:00.000Z"}`, mailID, sender, agent)
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/messages", strings.NewReader(payload))
	require.NoError(t, err)
	post.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(post)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	for _, step := range []int{1, 2} {
		select {
		case got := <-provider:
			require.Equal(t, step, got.step)
			if step == 2 {
				require.NotNil(t, got.result)
				require.Equal(t, callID, got.result.ToolCallID)
				require.False(t, got.result.IsError)
			}
		case <-ctx.Done():
			t.Fatal("mail SDK provider request missing")
		}
	}
	var evaluated evaluation
	select {
	case evaluated = <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("checked mail final did not invoke injected evaluator")
	}
	check, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer check.Close()
	var attempt, attemptSender, boundAgent, boundSession, boundGeneration string
	var boundEpoch int64
	require.NoError(t, check.QueryRowContext(ctx, `SELECT attempt_id,sender,agent_id,session_id,epoch,generation FROM embedded_attempts WHERE message_id=?`, mailID).Scan(&attempt, &attemptSender, &boundAgent, &boundSession, &boundEpoch, &boundGeneration))
	require.NotEmpty(t, attempt)
	require.Equal(t, sender, attemptSender)
	require.Equal(t, agent, boundAgent)
	require.Equal(t, b.SessionID, boundSession)
	require.Equal(t, b.Epoch, boundEpoch)
	require.Equal(t, b.Generation, boundGeneration)
	var ackSeq int64
	require.NoError(t, check.QueryRowContext(ctx, `SELECT entry_seq FROM embedded_acks WHERE attempt_id=? AND message_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, attempt, mailID, agent, b.SessionID, b.Epoch, b.Generation).Scan(&ackSeq))
	mail, err := db.MessageFor(ctx, mailID)
	require.NoError(t, err)
	require.Equal(t, domain.MessageAcknowledged, mail.State)
	var receiptSeq, revision int64
	var digest, goal, receiptRole string
	var enabled int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT turn_entry_seq,turn_entry_digest,goal_revision,goal_enabled,goal_text,role FROM embedded_goal_applied_receipts WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, agent, b.SessionID, b.Epoch, b.Generation).Scan(&receiptSeq, &digest, &revision, &enabled, &goal, &receiptRole))
	require.Equal(t, ackSeq, receiptSeq)
	require.Equal(t, "worker", receiptRole)
	require.EqualValues(t, 1, revision)
	require.Equal(t, 1, enabled)
	require.Equal(t, "finish own mail goal", goal)
	require.Equal(t, receiptSeq, evaluated.key.UserSeq)
	require.Equal(t, digest, evaluated.key.UserDigest)
	require.Equal(t, revision, evaluated.key.GoalRevision)
	var finalSeq int64
	var finalBody []byte
	require.NoError(t, check.QueryRowContext(ctx, `SELECT seq,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='assistant' ORDER BY seq DESC LIMIT 1`, agent, b.SessionID, b.Epoch, b.Generation).Scan(&finalSeq, &finalBody))
	require.Greater(t, finalSeq, receiptSeq)
	require.Contains(t, string(finalBody), "first addressed final")
	var outCount int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=?`, attempt).Scan(&outCount))
	require.Equal(t, 1, outCount, "checked first final must freeze exactly one sender-bound intent before evaluation returns")
	var replyID, replyFrom, replyTo, replyBody, replyCreated string
	var replySeq int64
	require.NoError(t, check.QueryRowContext(ctx, `SELECT reply_id,sender,recipient,body,created_at,entry_seq FROM embedded_reply_outbox WHERE attempt_id=?`, attempt).Scan(&replyID, &replyFrom, &replyTo, &replyBody, &replyCreated, &replySeq))
	require.Equal(t, finalSeq, replySeq)
	require.Equal(t, agent, replyFrom)
	require.Equal(t, sender, replyTo)
	require.Equal(t, "first addressed final", replyBody)
	// Outbox intent alone is not a reply. Read through the router while the evaluator cannot return.
	type replyView struct {
		ID        string `json:"id"`
		From      string `json:"from"`
		To        string `json:"to"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}
	fetch := func(owner string) []replyView {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/conversations?owner="+owner+"&peer="+agent, nil)
		if e != nil {
			return nil
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		var view struct {
			Messages []replyView `json:"messages"`
		}
		if json.NewDecoder(resp.Body).Decode(&view) != nil {
			return nil
		}
		return view.Messages
	}
	published := func() bool {
		for _, r := range fetch(sender) {
			if r.ID == replyID && r.From == agent && r.To == sender && r.Body == replyBody && r.CreatedAt == replyCreated {
				return true
			}
		}
		return false
	}
	require.Eventually(t, published, 1200*time.Millisecond, 10*time.Millisecond, "G5/T6 RED: checked final and outbox reached, but first reply is not retrievable while evaluator is blocked")
	for _, r := range fetch(other) {
		require.NotEqual(t, replyID, r.ID, "cross-sender reply leak")
	}
	select {
	case <-provider:
		t.Fatal("provider continued before NOT_DONE released")
	default:
	}
	select {
	case <-entered:
		t.Fatal("duplicate evaluation while first evaluator blocked")
	default:
	}
	unblock()
	select {
	case got := <-provider:
		require.Equal(t, 3, got.step, "NOT_DONE must start one non-mail continuation")
	case <-time.After(3 * time.Second):
		t.Fatal("NOT_DONE did not continue")
	}
	require.Eventually(t, func() bool {
		var body []byte
		e := check.QueryRowContext(ctx, `SELECT body FROM embedded_entries WHERE agent_id=? AND kind='assistant' ORDER BY seq DESC LIMIT 1`, agent).Scan(&body)
		return e == nil && strings.Contains(string(body), "internal continuation final")
	}, 3*time.Second, 10*time.Millisecond)
	require.Never(t, func() bool {
		select {
		case <-provider:
			return true
		case <-entered:
			return true
		default:
			return false
		}
	}, 150*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=?`, attempt).Scan(&outCount))
	require.Equal(t, 1, outCount)
	require.True(t, published())
	for _, r := range fetch(other) {
		require.NotEqual(t, replyID, r.ID)
	}
}
