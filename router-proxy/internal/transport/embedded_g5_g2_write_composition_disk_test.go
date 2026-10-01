package transport

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Disk-only SDK write through two addressed HTTP mail turns on one Listen.
// The faux provider proposes tool calls; the real embedded tool writes the files.
func TestEmbeddedG5G2RootParentlessWorkerWriteMailDisk(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	names := []string{"disk-write-root", "disk-write-worker"}
	senders := []string{"disk-write-sender-root", "disk-write-sender-worker"}
	ids := []string{"d5111111-1111-4111-8111-111111111111", "d5222222-2222-4222-8222-222222222222"}
	projects := []string{filepath.Join(base, "root-project"), filepath.Join(base, "worker-project")}
	paths := []string{"root-created.txt", "worker-created.txt"}
	contents := []string{"root bytes\n", "worker bytes\n"}
	calls := []string{"root-write-call", "worker-write-call"}
	replies := []string{"root write complete", "worker write complete"}
	for _, p := range projects {
		require.NoError(t, os.Mkdir(p, 0700))
	}
	dbPath := filepath.Join(base, "router.db")
	manifest := mustEffectManifest(t)
	type observation struct {
		result *ai.ToolResultMessage
		tools  []ai.Tool
		mail   bool
	}
	observed := [2]chan observation{make(chan observation, 3), make(chan observation, 3)}
	models := [2]*ai.Model{}
	for i := range names {
		i := i
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		t.Cleanup(reg.Unregister)
		reg.SetResponses([]providers.FauxResponseStep{
			func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial ready " + names[i]}}, ai.StopStop)
			},
			func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				var tools []ai.Tool
				mail := false
				for _, message := range h.Messages {
					if sys, ok := message.(ai.SystemMessage); ok {
						tools = append(tools, sys.ToolsAdded...)
					}
					if u, ok := message.(ai.UserMessage); ok && strings.Contains(fmt.Sprint(u), "mail for "+names[i]) {
						mail = true
					}
				}
				observed[i] <- observation{tools: tools, mail: mail}
				return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("write", map[string]any{"path": paths[i], "content": contents[i]}, calls[i])}, ai.StopToolUse)
			},
			func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				var result *ai.ToolResultMessage
				for _, message := range h.Messages {
					if r, ok := message.(ai.ToolResultMessage); ok && r.ToolCallID == calls[i] {
						copy := r
						result = &copy
					}
				}
				observed[i] <- observation{result: result}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: replies[i]}}, ai.StopStop)
			},
		})
		models[i] = reg.GetModel()
	}
	open := func(bootstrap bool) (*store.Store, *managed.Manager, *Router, error) {
		db, err := store.Open(ctx, dbPath, store.Options{})
		if err != nil {
			return nil, nil, nil, err
		}
		manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, CodexCredentialStore: testfixture.CodexAdmission(t),
			Config:        managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: projects},
			CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
				for i, n := range names {
					if row.AgentID == n {
						return models[i]
					}
				}
				return nil
			},
		})
		if err != nil {
			_ = db.Close()
			return nil, nil, nil, err
		}
		router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true,
			StartupManaged: func(c context.Context, m *managed.Manager) error {
				if !bootstrap {
					return nil
				}
				_, e := m.LaunchEmbeddedRoot(c, managed.LaunchRequest{Name: names[0], Cwd: projects[0], Runtime: "embedded", InitialInstruction: "initial root only", RequestID: "d5000000-0000-4000-8000-000000000000", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}})
				return e
			},
		})
		if err != nil {
			_ = manager.Close(ctx)
			_ = db.Close()
			return nil, nil, nil, err
		}
		if err = router.Listen(ctx); err != nil {
			_ = router.Close(ctx)
			_ = manager.Close(ctx)
			_ = db.Close()
			return nil, nil, nil, err
		}
		return db, manager, router, nil
	}
	closeAll := func(db *store.Store, m *managed.Manager, r *Router) {
		require.NoError(t, r.Close(ctx))
		require.NoError(t, m.Close(ctx))
		require.NoError(t, db.Close())
	}
	db, m, r, err := open(true)
	require.NoError(t, err)
	defer func() {
		if r != nil {
			closeAll(db, m, r)
		}
	}()
	check, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer check.Close()
	url, _ := r.URLs()
	call := func(method, endpoint, body, requestID string) (int, string) {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, method, url+endpoint, strings.NewReader(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		if requestID != "" {
			req.Header.Set("X-Request-ID", requestID)
		}
		resp, e := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		require.NoError(t, e)
		return resp.StatusCode, string(raw)
	}
	payload := fmt.Sprintf(`{"name":%q,"cwd":%q,"model":{"provider":"openai-codex","id":"gpt-5.6-terra"},"initial_instruction":"initial worker only"}`, names[1], projects[1])
	status, raw := call(http.MethodPost, "/v1/managed-agents", payload, "d5333333-3333-4333-8333-333333333333")
	require.Equal(t, http.StatusAccepted, status, raw)
	type binding struct {
		role, session, generation string
		epoch                     int64
	}
	var saved [2]binding
	for i, name := range names {
		require.Eventually(t, func() bool { row, e := db.ManagedAgent(ctx, name); return e == nil && row.LifecycleState == "running" }, 4*time.Second, 10*time.Millisecond, name)
		row, e := db.ManagedAgent(ctx, name)
		require.NoError(t, e)
		require.NoError(t, check.QueryRowContext(ctx, `SELECT role,session_id,generation,epoch FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&saved[i].role, &saved[i].session, &saved[i].generation, &saved[i].epoch))
		role := "root"
		if i == 1 {
			role = "worker"
		}
		require.Equal(t, role, saved[i].role)
		require.Equal(t, row.PiSessionID, saved[i].session)
	}
	require.NotEqual(t, saved[0].session, saved[1].session)
	for i, name := range names {
		body := "mail for " + name
		payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":%q,"created_at":"2026-09-27T00:00:00.000Z"}`, ids[i], senders[i], name, body)
		status, raw = call(http.MethodPost, "/v1/messages", payload, "")
		require.Equal(t, http.StatusCreated, status, raw)
		select {
		case got := <-observed[i]:
			require.True(t, got.mail, "write must follow addressed mail")
			hasWrite := false
			for _, tool := range got.tools {
				if tool.Name == "write" {
					hasWrite = true
				}
			}
			require.True(t, hasWrite, "actual registered SDK write")
		case <-time.After(4 * time.Second):
			t.Fatal("mail SDK proposal timed out")
		}
		select {
		case got := <-observed[i]:
			require.NotNil(t, got.result)
			require.Equal(t, calls[i], got.result.ToolCallID)
			require.Equal(t, "write", got.result.ToolName)
			require.False(t, got.result.IsError)
			require.Contains(t, got.result.Content[0].(ai.TextContent).Text, "Successfully wrote")
		case <-time.After(4 * time.Second):
			t.Fatal("checked SDK continuation timed out")
		}
		require.Eventually(t, func() bool {
			message, e := db.MessageFor(ctx, ids[i])
			if e != nil || message.State != domain.MessageAcknowledged {
				return false
			}
			history, e := db.ConversationHistory(ctx, senders[i], name)
			return e == nil && len(history) == 2 && history[1].From == name && history[1].To == senders[i] && history[1].Body == replies[i]
		}, 4*time.Second, 10*time.Millisecond, "sender-bound reply")
		bytes, e := os.ReadFile(filepath.Join(projects[i], paths[i]))
		require.NoError(t, e)
		require.Equal(t, contents[i], string(bytes))
		var n int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq WHERE a.message_id=? AND a.sender=? AND a.agent_id=? AND a.accepted_body=? AND a.session_id=? AND a.epoch=? AND a.generation=? AND e.kind='user' AND instr(e.body,a.attempt_id)>0`, ids[i], senders[i], name, body, saved[i].session, saved[i].epoch, saved[i].generation).Scan(&n))
		require.Equal(t, 1, n, "exact SDK user ACK")
		for _, kind := range []string{"toolResult", "assistant"} {
			require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind=? AND instr(body,?)>0`, name, saved[i].session, saved[i].epoch, saved[i].generation, kind, map[string]string{"toolResult": calls[i], "assistant": replies[i]}[kind]).Scan(&n))
			require.Equal(t, 1, n, kind)
		}
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=?`, ids[i]).Scan(&n))
		require.Equal(t, 1, n)
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, ids[i]).Scan(&n))
		require.Equal(t, 1, n)
		history, e := db.ConversationHistory(ctx, senders[i], names[1-i])
		require.NoError(t, e)
		require.Empty(t, history)
		status, raw = call(http.MethodGet, "/v1/conversations?owner="+senders[i]+"&peer="+name, "", "")
		require.Equal(t, http.StatusOK, status, raw)
		require.Contains(t, raw, replies[i])
	}
	require.NoFileExists(t, filepath.Join(projects[0], paths[1]))
	require.NoFileExists(t, filepath.Join(projects[1], paths[0]))
	closeAll(db, m, r)
	r = nil
	db, m, r, err = open(false)
	require.NoError(t, err, "cold reopen")
	for i, name := range names {
		row, e := db.ManagedAgent(ctx, name)
		require.NoError(t, e)
		require.Equal(t, saved[i].session, row.PiSessionID)
		var role, session string
		require.NoError(t, check.QueryRowContext(ctx, `SELECT role,session_id FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&role, &session))
		require.Equal(t, saved[i].role, role)
		require.Equal(t, saved[i].session, session)
		bytes, e := os.ReadFile(filepath.Join(projects[i], paths[i]))
		require.NoError(t, e)
		require.Equal(t, contents[i], string(bytes))
		history, e := db.ConversationHistory(ctx, senders[i], name)
		require.NoError(t, e)
		require.Len(t, history, 2)
		require.Equal(t, replies[i], history[1].Body)
		for _, query := range []string{`SELECT count(*) FROM embedded_attempts WHERE message_id=?`, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=?`} {
			var n int
			require.NoError(t, check.QueryRowContext(ctx, query, ids[i]).Scan(&n))
			require.Equal(t, 1, n)
		}
	}
	t.Log("cold reopen assertions reached for root and parentless worker")
}
