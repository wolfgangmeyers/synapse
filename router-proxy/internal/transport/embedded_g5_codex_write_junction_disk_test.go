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

type codexWriteFixture struct {
	mu                                            sync.Mutex
	seen                                          map[string]int
	problems                                      []string
	names, paths, contents, calls, items, replies [2]string
}

func (d *codexWriteFixture) snapshot() (map[string]int, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := map[string]int{}
	for k, v := range d.seen {
		m[k] = v
	}
	return m, append([]string(nil), d.problems...)
}
func (d *codexWriteFixture) Do(r *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Model  string            `json:"model"`
		Stream bool              `json:"stream"`
		Store  bool              `json:"store"`
		Input  []json.RawMessage `json:"input"`
		Tools  []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	reject := func(reason string) (*http.Response, error) {
		d.problems = append(d.problems, reason)
		return nil, errors.New("closed offline Codex doer rejected request")
	}
	if r.Method != "POST" || r.URL.String() != "https://chatgpt.com/backend-api/codex/responses" || r.Header.Get("Authorization") != "Bearer private-write-junction-secret" || r.Header.Get("ChatGPT-Account-ID") != "write-junction-account" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "text/event-stream" || payload.Model != "gpt-6-sol" || !payload.Stream || payload.Store || len(payload.Input) == 0 {
		return reject("invalid Codex request envelope")
	}
	writeRegistered := false
	for _, tool := range payload.Tools {
		if tool.Name == "write" {
			writeRegistered = true
		}
	}
	if !writeRegistered {
		return reject("write not registered")
	}
	// Continuations carry earlier mail: classify by function_call_output before mail before initial.
	index, phase := -1, ""
	for i := range d.names {
		for _, item := range payload.Input {
			var obj map[string]json.RawMessage
			if json.Unmarshal(item, &obj) != nil {
				continue
			}
			var kind, call, output string
			_ = json.Unmarshal(obj["type"], &kind)
			_ = json.Unmarshal(obj["call_id"], &call)
			_ = json.Unmarshal(obj["output"], &output)
			if kind == "function_call_output" && call == d.calls[i] && strings.Contains(output, "Successfully wrote") {
				index, phase = i, "continuation"
			}
		}
	}
	if phase == "" {
		for i := range d.names {
			for _, item := range payload.Input {
				if strings.Contains(string(item), "mail for "+d.names[i]) {
					index, phase = i, "mail"
				}
			}
		}
	}
	if phase == "" {
		for i := range d.names {
			for _, item := range payload.Input {
				if strings.Contains(string(item), "initial for "+d.names[i]) {
					index, phase = i, "initial"
				}
			}
		}
	}
	if index < 0 {
		return reject("unknown request phase or agent")
	}
	key := d.names[index] + ":" + phase
	if d.seen[key] != 0 {
		return reject("duplicate " + key)
	}
	if phase == "continuation" {
		foundCall, foundResult := 0, 0
		for _, item := range payload.Input {
			var obj map[string]json.RawMessage
			_ = json.Unmarshal(item, &obj)
			var kind, call, id, name, args, output string
			_ = json.Unmarshal(obj["type"], &kind)
			_ = json.Unmarshal(obj["call_id"], &call)
			_ = json.Unmarshal(obj["id"], &id)
			_ = json.Unmarshal(obj["name"], &name)
			_ = json.Unmarshal(obj["arguments"], &args)
			_ = json.Unmarshal(obj["output"], &output)
			if kind == "function_call" && call == d.calls[index] && id == d.items[index] && name == "write" {
				var a struct{ Path, Content string }
				if json.Unmarshal([]byte(args), &a) == nil && a.Path == d.paths[index] && a.Content == d.contents[index] {
					foundCall++
				}
			}
			if kind == "function_call_output" && call == d.calls[index] && strings.Contains(output, "Successfully wrote") {
				foundResult++
			}
		}
		if foundCall != 1 || foundResult != 1 {
			return reject(fmt.Sprintf("missing/multiple matching call or result: %d %d", foundCall, foundResult))
		}
	}
	d.seen[key]++
	var events []string
	id := fmt.Sprintf("resp_write_%s_%s", d.names[index], phase)
	events = append(events, fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id))
	if phase == "mail" {
		args, _ := json.Marshal(map[string]string{"path": d.paths[index], "content": d.contents[index]})
		events = append(events, fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":%q,"call_id":%q,"name":"write"}}`, d.items[index], d.calls[index]), fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":%q,"call_id":%q,"name":"write","arguments":%q}}`, d.items[index], d.calls[index], string(args)))
	} else {
		text := "ready " + d.names[index]
		if phase == "continuation" {
			text = d.replies[index]
		}
		events = append(events, fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_%s_%s"}}`, d.names[index], phase), fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_%s_%s","content":[{"type":"output_text","text":%q}]}}`, d.names[index], phase, text))
	}
	events = append(events, fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed"}}`, id))
	var s strings.Builder
	for _, e := range events {
		fmt.Fprintf(&s, "data: %s\n\n", e)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(s.String())), Request: r}, nil
}

func TestEmbeddedG5CodexResponsesRealWriteJunctionDisk(t *testing.T) {
	ctx := context.Background()
	base, err := os.MkdirTemp("/srv/disk-tmp", "codex-write-junction-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	require.NoError(t, os.Chmod(base, 0700))
	d := &codexWriteFixture{seen: map[string]int{}, names: [2]string{"junction-root", "junction-worker"}, paths: [2]string{"root-created.txt", "worker-created.txt"}, contents: [2]string{"root SDK bytes\n", "worker SDK bytes\n"}, calls: [2]string{"call_root_junction", "call_worker_junction"}, items: [2]string{"fc_root_junction", "fc_worker_junction"}, replies: [2]string{"root write finished", "worker write finished"}}
	projects := [2]string{filepath.Join(base, "root-project"), filepath.Join(base, "worker-project")}
	senders := [2]string{"junction-sender-root", "junction-sender-worker"}
	ids := [2]string{"e5111111-1111-4111-8111-111111111111", "e5222222-2222-4222-8222-222222222222"}
	for _, p := range projects {
		require.NoError(t, os.Mkdir(p, 0700))
		entries, e := os.ReadDir(p)
		require.NoError(t, e)
		require.Empty(t, entries, "fixture project starts empty")
	}
	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	credentialPath := filepath.Join(base, "private", "credential.json")
	cred, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	require.NoError(t, cred.Save(ctx, store.CodexCredential{AccessToken: "private-write-junction-secret", RefreshToken: "private-write-junction-refresh", ExpiresAt: time.Now().Add(time.Hour), AccountID: "write-junction-account"}))
	info, err := os.Stat(credentialPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: mustEffectManifest(t), Config: managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: projects[:]}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return &model }, CodexCredentialStore: cred, CodexHTTPTransport: fixtureCodexTransport{d}})
	require.NoError(t, err)
	defer manager.Close(ctx)
	router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true, StartupManaged: func(c context.Context, m *managed.Manager) error {
		_, e := m.LaunchEmbeddedRoot(c, managed.LaunchRequest{Name: d.names[0], Cwd: projects[0], Runtime: "embedded", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-6-sol"}, InitialInstruction: "initial for " + d.names[0], RequestID: "e5000000-0000-4000-8000-000000000000"})
		return e
	}})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer router.Close(ctx)
	_, err = manager.Launch(ctx, managed.LaunchRequest{Name: d.names[1], Cwd: projects[1], Runtime: "embedded", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-6-sol"}, InitialInstruction: "initial for " + d.names[1], RequestID: "e5333333-3333-4333-8333-333333333333"})
	require.NoError(t, err)
	check, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer check.Close()
	url, _ := router.URLs()
	client := &http.Client{Timeout: 3 * time.Second}
	httpCall := func(method, path, body string) (int, string) {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, method, url+path, strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		bytes, e := io.ReadAll(resp.Body)
		require.NoError(t, e)
		require.NotContains(t, string(bytes), "private-write-junction-secret")
		return resp.StatusCode, string(bytes)
	}
	for i, name := range d.names {
		require.Eventually(t, func() bool {
			row, e := db.ManagedAgent(ctx, name)
			seen, problems := d.snapshot()
			return e == nil && row.LifecycleState == "running" && seen[name+":initial"] == 1 && len(problems) == 0
		}, 5*time.Second, 10*time.Millisecond, "initial SDK ready %s", name)
		row, e := db.ManagedAgent(ctx, name)
		require.NoError(t, e)
		require.Equal(t, name, row.AgentID)
		require.Equal(t, projects[i], row.Cwd)
		require.Equal(t, "embedded", row.Runtime)
		var role, session, generation string
		var epoch, token int64
		require.NoError(t, check.QueryRowContext(ctx, `SELECT role,session_id,epoch,generation,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&role, &session, &epoch, &generation, &token))
		expected := "root"
		if i == 1 {
			expected = "worker"
		}
		require.Equal(t, expected, role)
		require.Equal(t, row.PiSessionID, session)
		require.Positive(t, token)
		var before int64
		require.NoError(t, check.QueryRowContext(ctx, `SELECT max(seq) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=?`, name, session, epoch).Scan(&before))
		body := "mail for " + name
		payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":%q,"created_at":"2026-09-27T00:00:00.000Z"}`, ids[i], senders[i], name, body)
		status, raw := httpCall("POST", "/v1/messages", payload)
		require.Equal(t, http.StatusCreated, status, raw)
		require.Eventually(t, func() bool {
			msg, e := db.MessageFor(ctx, ids[i])
			if e != nil || msg.State != domain.MessageAcknowledged {
				return false
			}
			history, e := db.ConversationHistory(ctx, senders[i], name)
			seen, problems := d.snapshot()
			return e == nil && len(history) == 2 && history[1].From == name && history[1].To == senders[i] && history[1].Body == d.replies[i] && seen[name+":mail"] == 1 && seen[name+":continuation"] == 1 && len(problems) == 0
		}, 5*time.Second, 10*time.Millisecond, "actual SDK write continuation %s", name)
		bytes, e := os.ReadFile(filepath.Join(projects[i], d.paths[i]))
		require.NoError(t, e)
		require.Equal(t, d.contents[i], string(bytes))
		require.NoFileExists(t, filepath.Join(projects[1-i], d.paths[i]))
		entries, e := os.ReadDir(projects[i])
		require.NoError(t, e)
		require.Len(t, entries, 1, "only the addressed SDK write may populate this private project")
		require.Equal(t, d.paths[i], entries[0].Name())
		require.True(t, entries[0].Type().IsRegular(), "SDK output must be a regular file")
		var n int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries ent ON ent.agent_id=k.agent_id AND ent.session_id=k.session_id AND ent.epoch=k.epoch AND ent.seq=k.entry_seq WHERE a.message_id=? AND a.sender=? AND a.agent_id=? AND a.accepted_body=? AND a.session_id=? AND a.epoch=? AND a.generation=? AND ent.kind='user' AND ent.seq>? AND instr(ent.body,a.attempt_id)>0`, ids[i], senders[i], name, body, session, epoch, generation, before).Scan(&n))
		require.Equal(t, 1, n, "checked persisted SDK user ACK")
		for _, entry := range []struct{ kind, needle string }{{"assistant", d.calls[i] + "|" + d.items[i]}, {"toolResult", d.calls[i] + "|" + d.items[i]}, {"toolResult", "Successfully wrote"}, {"assistant", d.replies[i]}} {
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind=? AND seq>? AND instr(body,?)>0`, name, session, epoch, generation, entry.kind, before, entry.needle).Scan(&n))
			require.Equal(t, 1, n, entry.kind+" "+entry.needle)
		}
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, ids[i]).Scan(&n))
		require.Equal(t, 1, n)
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=?`, ids[i]).Scan(&n))
		require.Equal(t, 1, n)
		for _, owner := range []string{senders[i], name} {
			status, raw = httpCall("GET", "/v1/conversations?owner="+owner+"&peer="+map[bool]string{true: name, false: senders[i]}[owner == senders[i]], "")
			require.Equal(t, http.StatusOK, status, raw)
			require.Contains(t, raw, d.replies[i])
		}
		history, e := db.ConversationHistory(ctx, senders[i], d.names[1-i])
		require.NoError(t, e)
		require.Empty(t, history)
	}
	root, err := db.ManagedAgent(ctx, d.names[0])
	require.NoError(t, err)
	worker, err := db.ManagedAgent(ctx, d.names[1])
	require.NoError(t, err)
	require.NotEqual(t, root.AgentID, worker.AgentID, "independent worker identity")
	require.NotEqual(t, root.PiSessionID, worker.PiSessionID, "independent session binding")
	seen, problems := d.snapshot()
	require.Empty(t, problems)
	require.Equal(t, map[string]int{"junction-root:initial": 1, "junction-worker:initial": 1, "junction-root:mail": 1, "junction-worker:mail": 1, "junction-root:continuation": 1, "junction-worker:continuation": 1}, seen)
	raw, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-write-junction-secret")
}
