package managed_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/stretchr/testify/require"
)

// Real router, managed lane, SQLite and HTTP MCP; only the external CLI is fake.
func TestClaudeManagedHTTPMCPCheckedTurnFreshProcess(t *testing.T) {
	bin, err := filepath.Abs("../../../tests/fixtures/fake-claude/claude.mjs")
	require.NoError(t, err)
	root := t.TempDir()
	script := filepath.Join(root, "script.json")
	record := filepath.Join(root, "record.jsonl")
	require.NoError(t, os.WriteFile(script, []byte(`{"turns_by_input":[{"contains":"save the note","steps":[{"mcp_call":{"id":"scratch-1","name":"scratchpad","input":{"action":"append","content":"real HTTP checked note"}}},{"text":"first done"},{"barrier":"capture-grant"}]},{"contains":"next input","steps":[{"text":"second done"}]}]}`), 0600))
	t.Setenv("MECHA_CLAUDE_BIN", bin)
	t.Setenv("FAKE_CLAUDE_SCRIPT", script)
	t.Setenv("FAKE_CLAUDE_RECORD", record)
	h := newBehavioralHarness(t, nil)
	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{Name: "claude-http", Cwd: h.root, Runtime: "embedded", Model: &managed.Model{Provider: "claude-code", ID: "sonnet"}, InitialInstruction: "save the note", RequestID: "11111111-2222-4333-8444-555555555551"})
	require.NoError(t, err)
	id := launched.Agent.AgentID
	var barrierID string
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(record)
		for _, line := range strings.Split(string(data), "\n") {
			var row struct{ Kind, Name, Invocation string }
			if json.Unmarshal([]byte(line), &row) == nil && row.Kind == "barrier" && row.Name == "capture-grant" {
				barrierID = row.Invocation
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond)
	files, err := filepath.Glob(filepath.Join(h.root, "sessions", "claude-processes", "mcp-*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	configBytes, err := os.ReadFile(files[0])
	require.NoError(t, err)
	var config struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(configBytes, &config))
	require.NoError(t, os.WriteFile(filepath.Join(root, "barrier-"+barrierID), nil, 0600))
	raw := h.sqlDB(t)
	require.Eventually(t, func() bool {
		var n int
		_ = raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND message_content LIKE '%first done%'`, id).Scan(&n)
		return n > 0
	}, 15*time.Second, 20*time.Millisecond)
	client := h.registerWebSocketClient(t, "claude-user", "claude-inst")
	defer client.Close(websocket.StatusNormalClosure, "done")
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, id)
		return err == nil && agent.LifecycleState == "running"
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, 201, h.postMessage(t, "claude-mail-1", "claude-user", id, "next input"))
	require.Eventually(t, func() bool { data, _ := os.ReadFile(record); return strings.Contains(string(data), "next input") }, 10*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		var n int
		_ = raw.QueryRow(`SELECT count(*) FROM embedded_acks WHERE agent_id=?`, id).Scan(&n)
		return n == 1
	}, 10*time.Second, 20*time.Millisecond)

	entries, _, err := h.db.FetchScratchpadRecent(h.ctx, id, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "real HTTP checked note", entries[0].Content)
	var resultCount int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, id).Scan(&resultCount))
	require.Equal(t, 1, resultCount, "HTTP result plus stdout repeat append once")
	require.Eventually(t, func() bool {
		var n int
		_ = raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND message_content LIKE '%second done%'`, id).Scan(&n)
		return n > 0
	}, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, h.manager.Close(context.Background()))
	req, err := http.NewRequest("POST", config.Servers["mecha"].URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", config.Servers["mecha"].Headers["Authorization"])
	response, err := (&http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if response != nil {
		response.Body.Close()
	}
	require.Error(t, err, "private listener must be closed after manager shutdown")
	_, err = os.Stat(files[0])
	require.True(t, os.IsNotExist(err), "process secret file removed")

	data, err := os.ReadFile(record)
	require.NoError(t, err)
	processes := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r struct {
			Kind string   `json:"kind"`
			Argv []string `json:"argv"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		if r.Kind == "start" && len(r.Argv) > 0 && r.Argv[0] == "-p" {
			processes++
		}
	}
	require.Equal(t, 2, processes)
}

func TestClaudeManagedResetRevokesOldProcessAndPreservesContext(t *testing.T) {
	binary, err := filepath.Abs("../../../tests/fixtures/fake-claude/claude.mjs")
	require.NoError(t, err)
	t.Setenv("MECHA_CLAUDE_BIN", binary)
	record := filepath.Join(t.TempDir(), "record.jsonl")
	t.Setenv("FAKE_CLAUDE_RECORD", record)
	script := filepath.Join(t.TempDir(), "script.json")
	require.NoError(t, os.WriteFile(script, []byte(`{"turns":[[{"barrier":"capture-old"},{"text":"fake answer"}],[{"text":"fake answer"}]]}`), 0600))
	t.Setenv("FAKE_CLAUDE_SCRIPT", script)
	h := newBehavioralHarness(t, nil)
	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{Name: "claude-reset", Cwd: h.root, Runtime: "embedded", Model: &managed.Model{Provider: "claude-code", ID: "sonnet"}, InitialInstruction: "remember this initial work", RequestID: "11111111-2222-4333-8444-555555555552"})
	require.NoError(t, err)
	id := launched.Agent.AgentID
	// Hold an open procfs handle to the original process, not a reusable PID.
	// The completed turn may already close it before reset; this only checks
	// that the captured process is no longer live afterwards.
	var oldPID int
	var oldStat *os.File
	var invocation string
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(record)
		for _, line := range strings.Split(string(data), "\n") {
			var row struct {
				Kind       string `json:"kind"`
				Name       string `json:"name"`
				PID        int    `json:"pid"`
				Invocation string `json:"invocation"`
			}
			if json.Unmarshal([]byte(line), &row) == nil && row.Kind == "barrier" && row.Name == "capture-old" {
				oldPID, invocation = row.PID, row.Invocation
				return invocation != "" && oldPID > 0
			}
		}
		return false
	}, 10*time.Second, time.Millisecond)
	if runtime.GOOS == "linux" {
		oldStat, err = os.Open(fmt.Sprintf("/proc/%d/stat", oldPID))
		require.NoError(t, err)
		defer oldStat.Close()
	}
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(script), "barrier-"+invocation), nil, 0600))
	raw := h.sqlDB(t)
	require.Eventually(t, func() bool {
		var n int
		err := raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND message_content LIKE '%fake answer%'`, id).Scan(&n)
		return err == nil && n == 1
	}, 15*time.Second, 20*time.Millisecond, "initial answer must be durable before reset")
	require.Eventually(t, func() bool {
		a, e := h.db.ManagedAgent(h.ctx, id)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 10*time.Second, 20*time.Millisecond)

	req, err := http.NewRequest("POST", h.httpURL+"/v1/managed-agents/"+id+"/reset", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("X-Request-ID", "11111111-2222-4333-8444-555555555553")
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, 202, response.StatusCode)
	// The initial process also receives a seeded prompt. Wait for the reset
	// process itself to receive the preserved conversation, not merely any record.
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(record)
		var processIDs []int
		var inputs = make(map[int]string)
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var row struct {
				Kind       string   `json:"kind"`
				PID        int      `json:"pid"`
				Argv       []string `json:"argv"`
				Line       string   `json:"line"`
				Invocation string   `json:"invocation"`
			}
			if json.Unmarshal([]byte(line), &row) != nil {
				continue
			}
			if row.Kind == "start" && len(row.Argv) > 0 && row.Argv[0] == "-p" {
				processIDs = append(processIDs, row.PID)
			}
			if row.Kind == "stdin" {
				inputs[row.PID] += row.Line
			}
			if row.Kind == "barrier" && row.PID != oldPID && row.Invocation != "" {
				_ = os.WriteFile(filepath.Join(filepath.Dir(script), "barrier-"+row.Invocation), nil, 0600)
			}
		}
		return len(processIDs) == 2 && strings.Contains(inputs[processIDs[1]], "Recent conversation preserved by the router") && strings.Contains(inputs[processIDs[1]], "remember this initial work") && strings.Contains(inputs[processIDs[1]], "fake answer")
	}, 15*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		a, e := h.db.ManagedAgent(h.ctx, id)
		return e == nil && a.SessionEpoch == 2 && a.ActivityState == "idle"
	}, 15*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		var n int
		err := raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND message_content LIKE '%fake answer%'`, id).Scan(&n)
		return err == nil && n >= 2
	}, 15*time.Second, 20*time.Millisecond, "reset wake turn must finish before teardown")
	data, err := os.ReadFile(record)
	require.NoError(t, err)
	var processIDs []int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r struct {
			Kind string   `json:"kind"`
			PID  int      `json:"pid"`
			Argv []string `json:"argv"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		if r.Kind == "start" && len(r.Argv) > 0 && r.Argv[0] == "-p" {
			processIDs = append(processIDs, r.PID)
		}
	}
	require.Len(t, processIDs, 2)
	require.Positive(t, processIDs[0])
	require.Positive(t, processIDs[1])
	require.NotEqual(t, processIDs[0], processIDs[1], "reset must use a distinct process")
	if oldStat != nil {
		require.Equal(t, oldPID, processIDs[0])
		data, err := io.ReadAll(oldStat)
		if err == nil {
			// stat's comm field may contain spaces or parentheses; state follows its final ')'.
			end := strings.LastIndexByte(string(data), ')')
			require.GreaterOrEqual(t, end, 0)
			fields := strings.Fields(string(data[end+1:]))
			require.NotEmpty(t, fields)
			require.Equal(t, "Z", fields[0], "captured original process is still live")
		} else {
			require.True(t, errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrNotExist), "reading captured process: %v", err)
		}
	}
}

func TestClaudeManagedComposerWithoutInitialInstruction(t *testing.T) {
	binary, err := filepath.Abs("../../../tests/fixtures/fake-claude/claude.mjs")
	require.NoError(t, err)
	t.Setenv("MECHA_CLAUDE_BIN", binary)
	script := filepath.Join(t.TempDir(), "script.json")
	require.NoError(t, os.WriteFile(script, []byte(`{"turns":[[{"text":"composer answer"}]]}`), 0600))
	t.Setenv("FAKE_CLAUDE_SCRIPT", script)
	h := newBehavioralHarness(t, nil)
	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{Name: "claude-composer", Cwd: h.root, Runtime: "embedded", Model: &managed.Model{Provider: "claude-code", ID: "sonnet"}, RequestID: "11111111-2222-4333-8444-555555555565"})
	require.NoError(t, err)
	id := launched.Agent.AgentID
	require.Eventually(t, func() bool { a, e := h.db.ManagedAgent(h.ctx, id); return e == nil && a.LifecycleState == "running" }, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, 201, h.postMessage(t, "composer-message", "user", id, "Retry locally"))
	raw := h.sqlDB(t)
	defer func() {
		if t.Failed() {
			rows, _ := raw.Query(`SELECT body FROM embedded_entries WHERE agent_id=?`, id)
			if rows != nil {
				defer rows.Close()
				for rows.Next() {
					var body string
					rows.Scan(&body)
					t.Log(body)
				}
			}
		}
	}()
	require.Eventually(t, func() bool {
		var n int
		_ = raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND CAST(body AS TEXT) LIKE '%composer answer%'`, id).Scan(&n)
		return n == 1
	}, 8*time.Second, 20*time.Millisecond)
}

func TestClaudeManagedMCPMailboxUsesAdmittedOwner(t *testing.T) {
	binary, err := filepath.Abs("../../../tests/fixtures/fake-claude/claude.mjs")
	require.NoError(t, err)
	t.Setenv("MECHA_CLAUDE_BIN", binary)
	script := filepath.Join(t.TempDir(), "script.json")
	require.NoError(t, os.WriteFile(script, []byte(`{"turns":[[{"mcp_call":{"id":"mail-owner","name":"mecha_mailbox_send","input":{"recipient":"user","body":"checked HTTP mailbox"}}},{"text":"mail sent"}]]}`), 0600))
	t.Setenv("FAKE_CLAUDE_SCRIPT", script)
	h := newBehavioralHarness(t, nil)
	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{Name: "claude-mail-owner", Cwd: h.root, Runtime: "embedded", Model: &managed.Model{Provider: "claude-code", ID: "sonnet"}, InitialInstruction: "send mail", RequestID: "11111111-2222-4333-8444-555555555566"})
	require.NoError(t, err)
	raw := h.sqlDB(t)
	require.Eventually(t, func() bool {
		var n int
		_ = raw.QueryRow(`SELECT count(*) FROM messages WHERE sender=? AND body='checked HTTP mailbox'`, launched.Agent.AgentID).Scan(&n)
		return n == 1
	}, 10*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		var n int
		_ = raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult' AND json_extract(body,'$.toolName')='mecha_mailbox_send' AND COALESCE(json_extract(body,'$.isError'),0)=0`, launched.Agent.AgentID).Scan(&n)
		return n == 1
	}, 5*time.Second, 20*time.Millisecond)
}
