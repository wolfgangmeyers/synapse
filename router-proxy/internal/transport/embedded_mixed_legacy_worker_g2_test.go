package transport

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// A protocol-speaking legacy window (not tmux) and two independently launched
// parentless embedded workers share one listening router and private SQLite DB.
func TestEmbeddedMixedLegacyToParentlessWorkerG2(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	manifest := mustEffectManifest(t)
	const legacyID, targetID, otherID = "mixed-legacy", "mixed-worker", "mixed-other-worker"
	var targetTurns, otherTurns atomic.Int32
	models := map[string]*ai.Model{}
	for _, name := range []string{targetID, otherID} {
		name := name
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		t.Cleanup(reg.Unregister)
		step := func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			if name == targetID {
				targetTurns.Add(1)
			} else {
				otherTurns.Add(1)
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "reply from " + name}}, ai.StopStop)
		}
		steps := make([]providers.FauxResponseStep, 8)
		for i := range steps {
			steps[i] = step
		}
		reg.SetResponses(steps)
		models[name] = reg.GetModel()
	}
	manager, err := managed.NewManager(managed.ManagerOptions{CodexCredentialStore: testfixture.CodexAdmission(t),
		Store: db, Manifest: manifest,
		Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model { return models[row.AgentID] },
	})
	require.NoError(t, err)
	router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	t.Cleanup(func() { _ = router.Close(ctx); _ = manager.Close(ctx); _ = db.Close() })
	base, wsURL := router.URLs()
	legacy, registered := mixedDialLegacy(t, wsURL, legacyID)
	defer legacy.Close(websocket.StatusNormalClosure, "test done")

	for i, name := range []string{targetID, otherID} {
		body := fmt.Sprintf(`{"name":%q,"cwd":%q,"model":{"provider":"openai-codex","id":"gpt-5.6-terra"},"initial_instruction":%q}`, name, root, "initial instruction for "+name)
		request, err := http.NewRequest(http.MethodPost, base+"/v1/managed-agents", strings.NewReader(body))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", fmt.Sprintf("e%07d-1111-4111-8111-111111111111", i+1))
		response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
		require.NoError(t, err)
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, response.StatusCode, string(raw))
	}
	var workers [2]*store.ManagedAgentRecord
	for i, name := range []string{targetID, otherID} {
		require.Eventually(t, func() bool {
			row, e := db.ManagedAgent(ctx, name)
			if e == nil && row.LifecycleState == "running" {
				workers[i] = row
				return true
			}
			return false
		}, 4*time.Second, 10*time.Millisecond, "launch fixture not ready: %s", name)
		require.Equal(t, "embedded", workers[i].Runtime)
		binding := store.EmbeddedBinding{AgentID: name, SessionID: workers[i].PiSessionID, Epoch: workers[i].SessionEpoch, Generation: workers[i].RuntimeGeneration}
		role, err := db.EmbeddedSessionRole(ctx, binding)
		require.NoError(t, err, "missing durable session is fixture failure")
		require.Equal(t, "worker", role, "parentless HTTP launch must bind a worker")
	}
	require.NotEqual(t, workers[0].PiSessionID, workers[1].PiSessionID)
	require.Eventually(t, func() bool { return targetTurns.Load() == 1 && otherTurns.Load() == 1 }, 4*time.Second, 10*time.Millisecond, "initial prompt fixture not settled")

	const mailID, body = "e3000000-1111-4111-8111-111111111111", "legacy addressed payload"
	status, raw := mixedPostMessage(t, base, mailID, legacyID, targetID, body, nil)
	require.Equal(t, http.StatusCreated, status, string(raw), "direct legacy-to-worker acceptance")
	check, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer check.Close()
	var attempt, sender, agent, session, generation, accepted, sdkKind, sdkBody string
	var epoch int64
	require.Eventually(t, func() bool {
		return check.QueryRowContext(ctx, `SELECT a.attempt_id,a.sender,a.agent_id,a.session_id,a.epoch,a.generation,a.accepted_body,e.kind,e.body
			FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.message_id=a.message_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation
			JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq WHERE a.message_id=?`, mailID).
			Scan(&attempt, &sender, &agent, &session, &epoch, &generation, &accepted, &sdkKind, &sdkBody) == nil
	}, 4*time.Second, 10*time.Millisecond, "accepted mail must produce worker SDK entry and processing ACK")
	require.NotEmpty(t, attempt)
	require.Equal(t, legacyID, sender)
	require.Equal(t, targetID, agent)
	require.Equal(t, workers[0].PiSessionID, session)
	require.Equal(t, workers[0].SessionEpoch, epoch)
	require.Equal(t, workers[0].RuntimeGeneration, generation)
	require.Equal(t, body, accepted)
	require.Equal(t, "user", sdkKind)
	require.Contains(t, sdkBody, attempt)
	require.Contains(t, sdkBody, body)

	frame := mixedReadJSON(t, legacy)
	require.Equal(t, "message", frame["type"], "actual worker reply must reach ordinary legacy WS route")
	delivered := frame["message"].(map[string]any)
	require.Equal(t, targetID, delivered["from"])
	require.Equal(t, legacyID, delivered["to"])
	require.Equal(t, "reply from "+targetID, delivered["body"])
	incarnation := frame["message_incarnation_id"]
	attemptNumber := frame["attempt_number"]
	require.NotEmpty(t, incarnation)
	require.EqualValues(t, 1, attemptNumber)
	replyID := delivered["id"].(string)
	fields := map[string]any{"version": "v1", "agent_id": registered["agent_id"], "instance_id": registered["instance_id"], "lease_id": registered["lease_id"], "message_id": replyID, "attempt_number": attemptNumber}
	fields["frame_id"], fields["type"] = "mixed-inject-"+replyID, "injection_requested"
	mixedWriteJSON(t, legacy, fields)
	fields["frame_id"], fields["type"] = "mixed-ack-"+replyID, "ack"
	mixedWriteJSON(t, legacy, fields)
	ack := mixedReadJSON(t, legacy)
	require.Equal(t, "delivery_acknowledged", ack["type"])
	require.Equal(t, replyID, ack["message_id"])
	require.Equal(t, incarnation, ack["message_incarnation_id"])
	require.Equal(t, attemptNumber, ack["attempt_number"])

	require.Eventually(t, func() bool {
		message, e := db.MessageFor(ctx, mailID)
		if e != nil || message.State != domain.MessageAcknowledged {
			return false
		}
		history, e := db.ConversationHistory(ctx, legacyID, targetID)
		return e == nil && len(history) == 2 && history[1].ID == replyID && history[1].From == targetID && history[1].To == legacyID && history[1].Body == "reply from "+targetID
	}, 4*time.Second, 10*time.Millisecond, "worker generated settled sender-bound reply")
	var frozenReplyID, frozenSender, frozenRecipient, frozenBody, outboxState string
	require.NoError(t, check.QueryRowContext(ctx, `SELECT reply_id,sender,recipient,body,state FROM embedded_reply_outbox WHERE attempt_id=?`, attempt).
		Scan(&frozenReplyID, &frozenSender, &frozenRecipient, &frozenBody, &outboxState))
	require.Equal(t, replyID, frozenReplyID)
	require.Equal(t, targetID, frozenSender)
	require.Equal(t, legacyID, frozenRecipient)
	require.Equal(t, "reply from "+targetID, frozenBody)
	require.Equal(t, "delivered", outboxState, "settled worker reply must be published")
	status, raw = mixedPostMessage(t, base, mailID, legacyID, targetID, body, nil)
	require.Equal(t, http.StatusOK, status, string(raw), "duplicate post must not prompt again")
	for _, query := range []string{
		`SELECT count(*) FROM embedded_attempts WHERE message_id=?`,
		`SELECT count(*) FROM embedded_acks WHERE message_id=?`,
		`SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id IN (SELECT attempt_id FROM embedded_attempts WHERE message_id=?)`,
	} {
		var count int
		require.NoError(t, check.QueryRowContext(ctx, query, mailID).Scan(&count))
		require.Equal(t, 1, count, query)
	}
	var foreign int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks WHERE message_id=? AND (agent_id<>? OR session_id<>?)`, mailID, targetID, workers[0].PiSessionID).Scan(&foreign))
	require.Zero(t, foreign, "other worker cannot ACK this attempt")
	history, err := db.ConversationHistory(ctx, legacyID, otherID)
	require.NoError(t, err)
	require.Empty(t, history)
	require.EqualValues(t, 2, targetTurns.Load(), "one initial turn and one mail turn")
	require.EqualValues(t, 1, otherTurns.Load(), "other worker must not receive mail")
}
