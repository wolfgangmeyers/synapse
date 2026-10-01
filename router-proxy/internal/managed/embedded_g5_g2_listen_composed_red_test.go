package managed_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// One Listen, one real gated Bash, and an independent addressed SDK mail turn.
func TestEmbeddedG5G2ListenBashSiblingMailCompositionRED(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	root := t.TempDir()
	for _, name := range []string{"compose-a", "compose-b"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "compose-b", "one.txt"), []byte("private-b\n"), 0600))
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	aReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	bReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer aReg.Unregister()
	defer bReg.Unregister()
	const mailID = "a5555555-5555-4555-8555-5555555556a1"
	const sender = "compose-sender"
	const body = "mail while B real bash waits"
	const answer = "A answered gated mail"
	type observation struct {
		transcript bool
		durable    bool
		err        error
	}
	aSeen := make(chan observation, 2)
	bSeen := make(chan *ai.ToolResultMessage, 2)
	aReg.SetResponses([]providers.FauxResponseStep{func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		seen := observation{}
		for _, msg := range h.Messages {
			if msg.MessageRole() == "user" && strings.Contains(fmt.Sprint(msg), body) {
				seen.transcript = true
			}
		}
		reader, e := sql.Open("sqlite", path)
		if e != nil {
			seen.err = e
		} else {
			var n int
			seen.err = reader.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id='compose-a' AND kind='user' AND instr(body, ?) > 0`, body).Scan(&n)
			seen.durable = n == 1
			_ = reader.Close()
		}
		aSeen <- seen
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: answer}}, ai.StopStop)
	}})
	bReg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": "grep -F -n -- private-b one.txt", "timeout": float64(10)}, "b-bash-1")}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var result *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if v, ok := msg.(ai.ToolResultMessage); ok && v.ToolCallID == "b-bash-1" {
					copy := v
					result = &copy
				}
			}
			bSeen <- result
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "B finished"}}, ai.StopStop)
		},
	})
	m, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, CodexCredentialStore: testfixture.CodexAdmission(t), Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
		if row.AgentID == "compose-b" {
			return bReg.GetModel()
		}
		return aReg.GetModel()
	}})
	require.NoError(t, err)
	type gateState struct {
		pid   int
		write *os.File
	}
	started := make(chan gateState, 1)
	var gateMu sync.Mutex
	var gate gateState
	closing := false
	restore := managed.SetEmbeddedBashAfterStartForTest(m, func(pid int, w *os.File) {
		gateMu.Lock()
		if closing {
			gateMu.Unlock()
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = w.Close()
			return
		}
		gate = gateState{pid, w} // Own the process even before the test receives started.
		gateMu.Unlock()
		started <- gateState{pid, w}
	})
	var once sync.Once
	released := func() {
		once.Do(func() {
			gateMu.Lock()
			w := gate.write
			gateMu.Unlock()
			if w != nil {
				_, _ = w.Write([]byte("\n"))
				_ = w.Close()
			}
		})
	}
	finished := false
	defer func() {
		gateMu.Lock()
		closing = true
		g := gate
		gateMu.Unlock()
		released()
		if g.pid > 0 && !finished {
			_ = syscall.Kill(-g.pid, syscall.SIGKILL)
		}
		_ = m.Close(context.Background())
		restore()
	}()
	router, err := transport.New(transport.Options{Store: db, Managed: m, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer router.Close(context.Background())
	url, _ := router.URLs()
	call := func(method, endpoint, id, payload string) (int, []byte) {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, method, url+endpoint, strings.NewReader(payload))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		response, e := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		require.NoError(t, e)
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		require.NoError(t, e)
		return response.StatusCode, raw
	}
	for i, name := range []string{"compose-a", "compose-b"} {
		instruction := ""
		if i == 1 {
			instruction = "check own file"
		}
		payload := fmt.Sprintf(`{"name":%q,"cwd":%q,"model":{"provider":"openai-codex","id":"gpt-5.6-terra"},"initial_instruction":%q}`, name, filepath.Join(root, name), instruction)
		status, raw := call(http.MethodPost, "/v1/managed-agents", fmt.Sprintf("a5555555-5555-4555-8555-5555555556b%d", i), payload)
		require.Equal(t, http.StatusAccepted, status, string(raw))
	}
	for _, name := range []string{"compose-a", "compose-b"} {
		row, e := db.ManagedAgent(ctx, name)
		require.NoError(t, e)
		require.Equal(t, "embedded", row.Runtime)
		role, e := db.EmbeddedSessionRole(ctx, store.EmbeddedBinding{AgentID: row.AgentID, SessionID: row.PiSessionID, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration})
		require.NoError(t, e)
		require.Equal(t, "worker", role)
	}
	select {
	case observed := <-started:
		require.Positive(t, observed.pid)
		require.NotNil(t, observed.write)
	case <-time.After(4 * time.Second):
		row, rowErr := db.ManagedAgent(ctx, "compose-b")
		t.Logf("B row: %+v err=%v", row, rowErr)
		var entries []struct{ Kind, Body string }
		check, openErr := sql.Open("sqlite", path)
		if openErr == nil {
			rows, queryErr := check.QueryContext(ctx, `SELECT kind,body FROM embedded_entries WHERE agent_id='compose-b' ORDER BY seq`)
			if queryErr == nil {
				for rows.Next() {
					var e struct{ Kind, Body string }
					_ = rows.Scan(&e.Kind, &e.Body)
					entries = append(entries, e)
				}
				_ = rows.Close()
			}
			_ = check.Close()
		}
		t.Logf("B entries: %+v", entries)
		t.Fatal("fixture: B Bash did not start")
	}
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, "compose-a")
		return e == nil && a.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	status, raw := call(http.MethodGet, "/v1/health", "", "")
	require.Equal(t, http.StatusOK, status, string(raw))
	var health struct {
		Status    string `json:"status"`
		Readiness string `json:"readiness"`
		Version   string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(raw, &health))
	require.Equal(t, "ok", health.Status)
	require.Equal(t, "ok", health.Readiness)
	require.Equal(t, "v1", health.Version)
	probe, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var premature int
	probeErr := probe.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id='compose-b' AND kind='toolResult' AND instr(body,'b-bash-1')>0`).Scan(&premature)
	require.NoError(t, probe.Close())
	require.NoError(t, probeErr)
	require.Zero(t, premature, "B result must not exist at health probe")
	select {
	case <-bSeen:
		t.Fatal("B continued before release")
	default:
	}
	payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":"compose-a","body":%q,"created_at":"2026-09-27T00:00:00.000Z"}`, mailID, sender, body)
	status, raw = call(http.MethodPost, "/v1/messages", "", payload)
	require.Equal(t, http.StatusCreated, status, string(raw))
	select {
	case seen := <-aSeen:
		require.NoError(t, seen.err)
		require.True(t, seen.transcript, "A SDK missed user mail")
		require.True(t, seen.durable, "user entry not committed before SDK")
	case <-time.After(4 * time.Second):
		t.Fatal("A SDK blocked behind B")
	}
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	require.Eventually(t, func() bool {
		var n int
		e := check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries u ON u.agent_id=k.agent_id AND u.session_id=k.session_id AND u.epoch=k.epoch AND u.seq=k.entry_seq WHERE a.message_id=? AND a.sender=? AND a.agent_id='compose-a' AND a.accepted_body=? AND u.kind='user' AND instr(u.body,a.attempt_id)>0`, mailID, sender, body).Scan(&n)
		if e != nil || n != 1 {
			return false
		}
		message, e := db.MessageFor(ctx, mailID)
		if e != nil || message.State != domain.MessageAcknowledged {
			return false
		}
		history, e := db.ConversationHistory(ctx, sender, "compose-a")
		if e != nil || len(history) != 2 || history[1].From != "compose-a" || history[1].To != sender || history[1].Body != answer {
			return false
		}
		// Publication commits the conversation before the separate outbox
		// settlement. Wait for both without releasing B or replaying A.
		e = check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=? AND a.sender=? AND o.state='delivered'`, mailID, sender).Scan(&n)
		return e == nil && n == 1
	}, 4*time.Second, 10*time.Millisecond, "A exact attempted/acked durable sender-bound reply and delivered outbox while B held")
	var n int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=? AND a.sender=? AND o.state='delivered'`, mailID, sender).Scan(&n))
	require.Equal(t, 1, n)
	for _, table := range []string{"embedded_attempts", "embedded_acks"} {
		require.NoError(t, check.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE message_id=?", mailID).Scan(&n))
		require.Equal(t, 1, n, "exactly one %s for A mail", table)
	}
	status, raw = call(http.MethodGet, "/v1/conversations?owner="+sender+"&peer=compose-a", "", "")
	require.Equal(t, http.StatusOK, status, string(raw))
	var projection struct {
		Messages []json.RawMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(raw, &projection))
	require.Len(t, projection.Messages, 2)
	for _, name := range []string{"compose-b"} {
		history, e := db.ConversationHistory(ctx, sender, name)
		require.NoError(t, e)
		require.Empty(t, history)
	}
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id='compose-b' AND kind='toolResult' AND instr(body,'b-bash-1')>0`).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id='compose-b'`).Scan(&n))
	require.Zero(t, n, "B must not publish while gated")
	select {
	case <-bSeen:
		t.Fatal("B SDK continued while gated")
	default:
	}
	released()
	select {
	case result := <-bSeen:
		require.NotNil(t, result)
		require.Equal(t, "b-bash-1", result.ToolCallID)
		require.Equal(t, "bash", result.ToolName)
		require.False(t, result.IsError, "%v", result.Content)
		require.Contains(t, fmt.Sprint(result.Content), "exit=0")
		require.Contains(t, fmt.Sprint(result.Content), "1:private-b")
	case <-time.After(4 * time.Second):
		t.Fatal("B SDK did not continue")
	}
	require.Eventually(t, func() bool {
		var count int
		e := check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id='compose-b' AND kind='toolResult' AND instr(body,'b-bash-1')>0`).Scan(&count)
		return e == nil && count == 1
	}, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return syscall.Kill(-gate.pid, 0) == syscall.ESRCH }, 3*time.Second, 10*time.Millisecond)
	finished = true
}
