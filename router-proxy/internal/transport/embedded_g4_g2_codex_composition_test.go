package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

type compositionCodexDoer struct {
	mu       sync.Mutex
	seen     map[string]int
	problems []string
}

func (d *compositionCodexDoer) Do(r *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	body := string(raw)
	role, phase := "", ""
	for _, name := range []string{"composition-root", "composition-worker"} {
		if strings.Contains(body, "mail for "+name) {
			role, phase = name, "mail"
			break
		}
		if strings.Contains(body, "initial for "+name) {
			role, phase = name, "initial"
		}
	}
	key := role + ":" + phase
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.URL.String() != "https://chatgpt.com/backend-api/codex/responses" || r.Header.Get("Authorization") != "Bearer private-composition-secret-sentinel" || r.Header.Get("ChatGPT-Account-ID") != "composition-account" || role == "" || phase == "" || d.seen[key] != 0 {
		d.problems = append(d.problems, fmt.Sprintf("unexpected SDK request host=%s path=%s role=%s phase=%s auth=%t account=%q duplicate=%d", r.URL.Host, r.URL.Path, role, phase, r.Header.Get("Authorization") == "Bearer private-composition-secret-sentinel", r.Header.Get("ChatGPT-Account-ID"), d.seen[key]))
		return nil, errors.New("closed offline Codex doer rejected request")
	}
	d.seen[key]++
	answer := "ready " + role
	if phase == "mail" {
		answer = "reply " + role
	}
	sse := fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_composition\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"item_composition\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":%q}\n\ndata: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_composition\",\"status\":\"completed\"}}\n\n", answer, answer)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse)), Request: r}, nil
}
func (d *compositionCodexDoer) snapshot() (map[string]int, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	copySeen := make(map[string]int, len(d.seen))
	for k, v := range d.seen {
		copySeen[k] = v
	}
	return copySeen, append([]string(nil), d.problems...)
}

// Disk-only real SDK Responses turns through a single Listen: root and an independent worker.
func TestEmbeddedG4G2CodexRootWorkerMailComposition(t *testing.T) {
	ctx := context.Background()
	base, err := os.MkdirTemp("/srv/disk-tmp", "codex-composition-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	require.NoError(t, os.Chmod(base, 0700))
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	credentials, err := store.OpenLocalCodexCredentialStore(filepath.Join(base, "private", "credential.json"))
	require.NoError(t, err)
	require.NoError(t, credentials.Save(ctx, store.CodexCredential{AccessToken: "private-composition-secret-sentinel", RefreshToken: "private-refresh-composition-secret-sentinel", ExpiresAt: time.Now().Add(time.Hour), AccountID: "composition-account"}))
	info, err := os.Stat(filepath.Join(base, "private", "credential.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	manifest := mustEffectManifest(t)
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	doer := &compositionCodexDoer{seen: map[string]int{}}
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model }, CodexCredentialStore: credentials, CodexHTTPTransport: fixtureCodexTransport{doer}})
	require.NoError(t, err)
	defer manager.Close(ctx)
	router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true, StartupManaged: func(c context.Context, m *managed.Manager) error {
		_, e := m.LaunchEmbeddedRoot(c, managed.LaunchRequest{Name: "composition-root", Cwd: project, Runtime: "embedded", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-6-sol"}, InitialInstruction: "initial for composition-root", RequestID: "c3111111-1111-4111-8111-111111111111"})
		return e
	}})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer router.Close(ctx)
	_, err = manager.Launch(ctx, managed.LaunchRequest{Name: "composition-worker", Cwd: project, Runtime: "embedded", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-6-sol"}, InitialInstruction: "initial for composition-worker", RequestID: "c3222222-2222-4222-8222-222222222222"})
	require.NoError(t, err)
	url, _ := router.URLs()
	client := &http.Client{Timeout: 3 * time.Second}
	call := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, method, url+path, strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		require.NoError(t, e)
		require.NotContains(t, string(raw), "composition-secret-sentinel", path)
		return resp.StatusCode, raw
	}
	names := []string{"composition-root", "composition-worker"}
	senders := []string{"composition-sender-root", "composition-sender-worker"}
	ids := []string{"c3333333-3333-4333-8333-333333333333", "c3444444-4444-4444-8444-444444444444"}
	check, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer check.Close()
	for i, name := range names {
		require.Eventually(t, func() bool {
			row, e := db.ManagedAgent(ctx, name)
			seen, problems := doer.snapshot()
			return e == nil && row.LifecycleState == "running" && seen[name+":initial"] == 1 && len(problems) == 0
		}, 5*time.Second, 10*time.Millisecond, "initial SDK completion %s", name)
		row, e := db.ManagedAgent(ctx, name)
		require.NoError(t, e)
		require.Equal(t, "embedded", row.Runtime)
		var role, session, generation string
		var epoch, token int64
		require.NoError(t, check.QueryRowContext(ctx, `SELECT role,session_id,epoch,generation,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&role, &session, &epoch, &generation, &token))
		expected := "worker"
		if i == 0 {
			expected = "root"
		}
		require.Equal(t, expected, role)
		require.Equal(t, row.PiSessionID, session)
		require.Positive(t, token)
		var initialSeq int64
		require.NoError(t, check.QueryRowContext(ctx, `SELECT max(seq) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=?`, name, session, epoch).Scan(&initialSeq))
		body := "mail for " + name
		payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":%q,"created_at":"2026-09-27T00:00:00.000Z"}`, ids[i], senders[i], name, body)
		status, raw := call(http.MethodPost, "/v1/messages", payload)
		require.Equal(t, http.StatusCreated, status, string(raw))
		require.Eventually(t, func() bool {
			var entryBody []byte
			var entrySeq int64
			e := check.QueryRowContext(ctx, `SELECT ent.seq,ent.body FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries ent ON ent.agent_id=k.agent_id AND ent.session_id=k.session_id AND ent.epoch=k.epoch AND ent.seq=k.entry_seq WHERE a.message_id=? AND a.sender=? AND a.agent_id=? AND a.accepted_body=? AND a.session_id=? AND a.epoch=? AND a.generation=? AND ent.kind='user' AND instr(ent.body,a.attempt_id)>0`, ids[i], senders[i], name, body, session, epoch, generation).Scan(&entrySeq, &entryBody)
			if e != nil || entrySeq <= initialSeq || !strings.Contains(string(entryBody), senders[i]) || !strings.Contains(string(entryBody), body) {
				return false
			}
			msg, e := db.MessageFor(ctx, ids[i])
			if e != nil || msg.State != domain.MessageAcknowledged {
				return false
			}
			history, e := db.ConversationHistory(ctx, senders[i], name)
			seen, problems := doer.snapshot()
			return e == nil && len(history) == 2 && history[1].From == name && history[1].To == senders[i] && history[1].Body == "reply "+name && seen[name+":mail"] == 1 && len(problems) == 0
		}, 5*time.Second, 10*time.Millisecond, "SDK addressed ACK and sender-bound reply %s", name)
		status, raw = call(http.MethodGet, "/v1/conversations?owner="+senders[i]+"&peer="+name, "")
		require.Equal(t, http.StatusOK, status, string(raw))
		require.Contains(t, string(raw), "reply "+name)
		status, raw = call(http.MethodGet, "/v1/conversations?owner="+name+"&peer="+senders[i], "")
		require.Equal(t, http.StatusOK, status, string(raw))
		require.Contains(t, string(raw), "reply "+name)
		other := names[1-i]
		history, e := db.ConversationHistory(ctx, senders[i], other)
		require.NoError(t, e)
		require.Empty(t, history)
		status, raw = call(http.MethodGet, "/v1/managed-agents/"+name, "")
		require.Equal(t, http.StatusOK, status, string(raw))
		status, raw = call(http.MethodGet, "/v1/managed-agents", "")
		require.Equal(t, http.StatusOK, status, string(raw))
		var projection map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &projection))
	}
	seen, problems := doer.snapshot()
	require.Empty(t, problems)
	require.Equal(t, map[string]int{"composition-root:initial": 1, "composition-root:mail": 1, "composition-worker:initial": 1, "composition-worker:mail": 1}, seen)
	for _, id := range ids {
		var attempts, outbox int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id).Scan(&attempts))
		require.Equal(t, 1, attempts)
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=?`, id).Scan(&outbox))
		require.Equal(t, 1, outbox)
	}
	raw, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "composition-secret-sentinel", "persisted SQLite")
}
