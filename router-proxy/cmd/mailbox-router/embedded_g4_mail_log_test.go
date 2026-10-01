package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

type g4MailDoer struct{ calls atomic.Int32 }

func (d *g4MailDoer) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://chatgpt.com/backend-api/codex/responses" {
		return nil, errors.New("Codex fixture destination denied")
	}
	if d.calls.Add(1) != 1 {
		return nil, errors.New("mail provider request forbidden")
	}
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fake\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"item_fake\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ready\"}\n\ndata: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":\"ready\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fake\",\"status\":\"completed\"}}\n\n"
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestEmbeddedG4IssuerErrorMailLog(t *testing.T) {
	ctx := context.Background()
	base, err := os.MkdirTemp("/srv/disk-tmp", "g4-mail-log-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(root, 0700))
	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	credentialPath := filepath.Join(base, "private", "credential.json")
	credentials, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	old := store.CodexCredential{AccessToken: "valid-old-fake-access", RefreshToken: "valid-old-fake-refresh", ExpiresAt: time.Now().Add(time.Hour), AccountID: "fixture-account"}
	require.NoError(t, credentials.Save(ctx, old))
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	provider := &g4MailDoer{}
	writer, err := newLogWriter(filepath.Join(base, "events.jsonl"))
	require.NoError(t, err)
	var stderr bytes.Buffer
	events := make(chan managed.Event, 128)
	sink := managedEventLogger(writer, &stderr)
	var refreshes atomic.Int32
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest,
		Config:                managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model },
		CodexCredentialStore:  credentials, CodexHTTPTransport: provider,
		CodexRefresh: func(context.Context, store.CodexCredential) (store.CodexCredential, error) {
			refreshes.Add(1)
			return store.CodexCredential{}, errors.New("issuer failed fake-secret-marker")
		},
		EventSink: func(e managed.Event) { events <- e; sink(e) },
	})
	require.NoError(t, err)
	router, err := transport.New(transport.Options{Store: db, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(ctx); _ = db.Close(); _ = writer.Close() })
	const recipient = "g4-mail-worker"
	launched, err := manager.Launch(ctx, managed.LaunchRequest{Name: recipient, Cwd: root, Runtime: "embedded", InitialInstruction: "say ready", RequestID: "a5555555-5555-4555-8555-555555555581"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, recipient)
		return e == nil && row.LifecycleState == "running" && provider.calls.Load() == 1
	}, 5*time.Second, 10*time.Millisecond, "first provider turn must succeed")
	require.EqualValues(t, 0, refreshes.Load())
	binding := store.EmbeddedBinding{AgentID: recipient, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1 AND owner_id IS NOT NULL`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation).Scan(&binding.OwnerToken))
	require.Positive(t, binding.OwnerToken)
	before, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	var initialAssistant int
	for _, entry := range before.Entries {
		if entry.Kind == "assistant" {
			initialAssistant++
		}
	}
	require.Positive(t, initialAssistant, "initial provider completion required")
	initialSequence := before.Entries[len(before.Entries)-1].Sequence
	expired := old
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	require.NoError(t, credentials.Save(ctx, expired))
	const mailID, sender = "a5555555-5555-4555-8555-555555555582", "g4-other-worker"
	payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":"mail turn after expiry","created_at":"2026-09-27T00:00:00.000Z"}`, mailID, sender, recipient)
	resp, err := http.Post(server.URL+"/v1/messages", "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	response, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(response))
	var attempt, attributedSender, acceptedBody, kind string
	var sdkBody []byte
	require.Eventually(t, func() bool {
		return raw.QueryRowContext(ctx, `SELECT a.attempt_id,a.sender,a.accepted_body,e.kind,e.body FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq WHERE a.message_id=?`, mailID).Scan(&attempt, &attributedSender, &acceptedBody, &kind, &sdkBody) == nil
	}, 5*time.Second, 10*time.Millisecond, "mail must receive checked SDK append and ACK")
	require.NotEmpty(t, attempt)
	require.Equal(t, sender, attributedSender)
	require.Equal(t, "mail turn after expiry", acceptedBody)
	require.Equal(t, "user", kind)
	require.Contains(t, string(sdkBody), sender)
	require.Contains(t, string(sdkBody), attempt)
	require.Eventually(t, func() bool {
		var state string
		if raw.QueryRowContext(ctx, `SELECT state FROM messages WHERE id=?`, mailID).Scan(&state) != nil || state != "acknowledged" || refreshes.Load() != 1 {
			return false
		}
		failed, e := db.ManagedAgent(ctx, recipient)
		return e == nil && failed.LifecycleState == "failed" && !failed.AcceptingPrompts && !failed.SlotHeld &&
			failed.PiSessionID == binding.SessionID && failed.SessionEpoch == binding.Epoch && failed.RuntimeGeneration == binding.Generation
	}, 5*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, provider.calls.Load(), "failed refresh must not send mail provider turn")
	var replies int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=?`, attempt).Scan(&replies))
	require.Zero(t, replies)
	// A failed managed runtime cannot reopen an active owner-bound session.
	// Inspect its persisted terminal entries through the existing read-only SQL
	// handle, retaining the exact saved session/epoch/runtime generation.
	_, err = db.ReopenEmbedded(ctx, binding)
	require.ErrorContains(t, err, "stale managed embedded binding")
	entries, err := raw.QueryContext(ctx, `SELECT seq,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq>? AND kind='assistant' ORDER BY seq`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, initialSequence)
	require.NoError(t, err)
	var failedAssistants int
	for entries.Next() {
		var sequence int64
		var body []byte
		require.NoError(t, entries.Scan(&sequence, &body))
		var message ai.AssistantMessage
		require.NoError(t, json.Unmarshal(body, &message), "decode new SDK assistant entry at sequence %d", sequence)
		require.NotEqual(t, ai.StopStop, message.StopReason, "mail failure must not complete an assistant turn at sequence %d", sequence)
		require.Equal(t, ai.StopError, message.StopReason, "issuer failure should persist an error assistant at sequence %d", sequence)
		failedAssistants++
	}
	require.NoError(t, entries.Err())
	require.NoError(t, entries.Close())
	require.Positive(t, failedAssistants, "issuer failure should persist an SDK error assistant")
	stored, err := credentials.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, expired.AccessToken, stored.AccessToken)
	require.Equal(t, expired.RefreshToken, stored.RefreshToken)
	require.Equal(t, expired.AccountID, stored.AccountID)
	require.True(t, expired.ExpiresAt.Equal(stored.ExpiresAt), "stored expiry must match expired fixture")
	var diagnostic []byte
	for len(events) > 0 {
		e := <-events
		rawEvent, marshalErr := json.Marshal(e)
		require.NoError(t, marshalErr)
		diagnostic = append(diagnostic, rawEvent...)
	}
	for _, path := range []string{"/v1/managed-agents/" + recipient, "/v1/managed-agents"} {
		res, e := http.Get(server.URL + path)
		require.NoError(t, e)
		body, e := io.ReadAll(res.Body)
		require.NoError(t, e)
		require.NoError(t, res.Body.Close())
		require.Equal(t, http.StatusOK, res.StatusCode)
		diagnostic = append(diagnostic, body...)
	}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/ui-live"
	conn, wsResponse, e := websocket.Dial(ctx, wsURL, nil)
	if wsResponse != nil && wsResponse.Body != nil {
		_ = wsResponse.Body.Close()
	}
	require.NoError(t, e)
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		_, frame, readErr := conn.Read(readCtx)
		require.NoError(t, readErr)
		diagnostic = append(diagnostic, frame...)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "done")
	require.NoError(t, writer.Close())
	jsonl, err := os.ReadFile(filepath.Join(base, "events.jsonl"))
	require.NoError(t, err)
	dbBytes, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	for label, value := range map[string][]byte{"HTTP/WS/events": diagnostic, "JSONL": jsonl, "stderr": stderr.Bytes(), "DB": dbBytes, "mail response": response} {
		require.NotContains(t, string(value), "fake-secret-marker", label)
	}
}
