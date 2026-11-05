package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func mcpRequest(t *testing.T, url, token, method string, params any) (int, map[string]any) {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": "rpc-1", "method": method}
	if params != nil {
		request["params"] = params
	}
	body, err := json.Marshal(request)
	require.NoError(t, err)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	return resp.StatusCode, result
}
func TestClaudeCodeMCPHTTPGrantSQLiteFence(t *testing.T) {
	db, b, _, path := wakePersistenceFixtureWithClaim(t, false)
	seedMCPIdentity(t, path, b.AgentID)
	m := &Manager{store: db}
	handler := m.ClaudeCodeMCP()
	var calls atomic.Int32
	token, revoke, err := handler.Issue(context.Background(), ClaudeCodeMCPGrant{Binding: b, Owner: "wake-fixture", Incarnation: "process-1", Tools: []ClaudeCodeMCPTool{{Name: "scratchpad", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ExecuteChecked: func(ctx context.Context, call ClaudeCodeMCPCall) (json.RawMessage, error) {
		require.Equal(t, b, call.Binding)
		require.Equal(t, "process-1", call.Incarnation)
		require.Equal(t, "call-1", call.ToolUseID)
		require.Equal(t, "scratchpad", call.Name)
		calls.Add(1)
		return json.RawMessage(`{"content":[{"type":"text","text":"checked"}]}`), nil
	}})
	require.NoError(t, err)
	defer revoke()
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	url, err := handler.ListenPrivate(life)
	require.NoError(t, err)
	for _, method := range []string{"initialize", "tools/list"} {
		params := map[string]any{}
		if method == "initialize" {
			params = validMCPInitialize("2024-11-05")
		}
		status, out := mcpRequest(t, url, token, method, params)
		require.Equal(t, 200, status)
		require.Contains(t, out, "result")
	}
	params := map[string]any{"name": "scratchpad", "arguments": map[string]any{}, "_meta": map[string]any{"claudecode/toolUseId": "call-1"}}
	_, out := mcpRequest(t, url, token, "tools/call", params)
	require.Contains(t, out, "result")
	require.EqualValues(t, 1, calls.Load())
	delete(params, "_meta")
	_, out = mcpRequest(t, url, token, "tools/call", params)
	require.Contains(t, out, "error")
	require.EqualValues(t, 1, calls.Load())
	stale := b
	stale.OwnerToken++
	bad, _, err := handler.Issue(context.Background(), ClaudeCodeMCPGrant{Binding: stale, Owner: "wake-fixture", Incarnation: "stale", ExecuteChecked: func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error) { panic("stale") }})
	require.Error(t, err)
	require.Empty(t, bad)
	revoke()
	status, _ := mcpRequest(t, url, token, "tools/list", nil)
	require.Equal(t, 401, status)
	_ = store.EmbeddedBinding{} // Fixture above uses actual SQLite owner/session fencing.
}

func validMCPInitialize(version string) map[string]any {
	return map[string]any{"protocolVersion": version, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}
}

func TestClaudeCodeMCPInitializeAndPing(t *testing.T) {
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	handler := (&Manager{store: db}).ClaudeCodeMCP()
	token, revoke, err := handler.Issue(context.Background(), ClaudeCodeMCPGrant{Binding: b, Owner: "wake-fixture", Incarnation: "test", ExecuteChecked: func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error) { panic("unexpected tool call") }})
	require.NoError(t, err)
	defer revoke()
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, version := range []string{"2024-11-05", "2099-01-01"} {
		status, out := mcpRequest(t, server.URL, token, "initialize", validMCPInitialize(version))
		require.Equal(t, 200, status)
		require.Equal(t, "rpc-1", out["id"])
		require.Equal(t, "2025-03-26", out["result"].(map[string]any)["protocolVersion"])
	}
	for _, params := range []any{nil, map[string]any{}, "bad", map[string]any{"protocolVersion": "2024-11-05"}, map[string]any{"protocolVersion": 1, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "a", "version": "1"}}, map[string]any{"protocolVersion": "x", "capabilities": nil, "clientInfo": map[string]any{"name": "a", "version": "1"}}, map[string]any{"protocolVersion": "x", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "a"}}} {
		_, out := mcpRequest(t, server.URL, token, "initialize", params)
		require.Equal(t, "rpc-1", out["id"])
		require.EqualValues(t, -32602, out["error"].(map[string]any)["code"])
	}
	for _, params := range []any{nil, map[string]any{}} {
		_, out := mcpRequest(t, server.URL, token, "ping", params)
		require.Equal(t, "rpc-1", out["id"])
		require.Equal(t, map[string]any{}, out["result"])
	}
	_, out := mcpRequest(t, server.URL, token, "ping", "bad")
	require.EqualValues(t, -32602, out["error"].(map[string]any)["code"])
	status, _ := mcpRequest(t, server.URL, "invalid", "ping", nil)
	require.Equal(t, 401, status)
}

func TestClaudeCodeMCPNotificationsHaveNoResponse(t *testing.T) {
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	handler := (&Manager{store: db}).ClaudeCodeMCP()
	token, revoke, err := handler.Issue(context.Background(), ClaudeCodeMCPGrant{Binding: b, Owner: "wake-fixture", Incarnation: "test", ExecuteChecked: func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error) { panic("notification executed tool") }})
	require.NoError(t, err)
	defer revoke()
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, body := range []string{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`, `{"jsonrpc":"2.0","method":"unknown"}`, `{"jsonrpc":"2.0","method":"tools/call","params":{}}`} {
		req, err := http.NewRequest("POST", server.URL, bytes.NewBufferString(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, resp.StatusCode, body)
		content, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Empty(t, content, body)
	}
}

func TestClaudeMCPStatelessVersionAndBatch(t *testing.T) {
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	h := (&Manager{store: db}).ClaudeCodeMCP()
	token, revoke, err := h.Issue(context.Background(), ClaudeCodeMCPGrant{Binding: b, Owner: "wake-fixture", Incarnation: "version", ExecuteChecked: func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error) { panic("unexpected") }})
	require.NoError(t, err)
	defer revoke()
	request := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`)
	require.Contains(t, w.Body.String(), `"protocolVersion":"2025-03-26"`)
	w = request(`[{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"ping"},{"jsonrpc":"2.0","id":3,"result":{}}]`)
	require.Equal(t, 200, w.Code)
	require.JSONEq(t, `[{"jsonrpc":"2.0","id":2,"result":{}}]`, w.Body.String())
	w = request(`[{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":3,"result":{}}]`)
	require.Equal(t, 202, w.Code)
	require.Empty(t, w.Body.String())
}

func TestClaudeMCPListTracksWorkerPolicy(t *testing.T) {
	db, b, _, path := wakePersistenceFixtureWithClaim(t, false)
	seedMCPIdentity(t, path, b.AgentID)
	handler := (&Manager{store: db}).ClaudeCodeMCP()
	token, revoke, err := handler.Issue(context.Background(), ClaudeCodeMCPGrant{Binding: b, Owner: "wake-fixture", Incarnation: "policy", Tools: []ClaudeCodeMCPTool{{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ExecuteChecked: func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error) { return nil, nil }})
	require.NoError(t, err)
	defer revoke()
	server := httptest.NewServer(handler)
	defer server.Close()
	list := func() []any {
		_, out := mcpRequest(t, server.URL, token, "tools/list", nil)
		require.Contains(t, out, "result")
		return out["result"].(map[string]any)["tools"].([]any)
	}
	require.Len(t, list(), 2)
	require.NoError(t, db.SetAgentTool(context.Background(), b.AgentID, "bash", false))
	got := list()
	require.Len(t, got, 1)
	require.Equal(t, "read", got[0].(map[string]any)["name"])
	require.NoError(t, db.SetAgentTool(context.Background(), b.AgentID, "bash", true))
	require.Len(t, list(), 2)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`DROP TABLE managed_agent_tools`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	_, out := mcpRequest(t, server.URL, token, "tools/list", nil)
	require.Contains(t, out, "error")
	require.NotContains(t, out, "result")
}

func seedMCPIdentity(t *testing.T, path, id string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, id, "2026-01-01T00:00:00Z")
	require.NoError(t, err)
}
