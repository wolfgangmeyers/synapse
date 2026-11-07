package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// The producer is checked and the same call succeeds after the timeout. This
// distinguishes queue cancellation from a request that could never execute.
func TestInvestigationWitnessedQueueTimeout(t *testing.T) {
	ctx := context.Background()
	db, binding, _, path := wakePersistenceFixtureWithClaim(t, false)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, binding.AgentID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	m := &Manager{store: db}
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: binding, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
	require.NoError(t, err)
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "remember"}}}))
	call := ai.ToolCall{ID: "queued", Name: "scratchpad", Arguments: map[string]any{"action": "append", "content": "witnessed"}}
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
	bridge := newClaudeCodeTurnBridge(a)
	bridge.admitted = true
	require.NoError(t, bridge.producer(ctx, call))
	h := m.ClaudeCodeMCP()
	h.queueDeadline = func() (time.Time, *time.Timer) {
		d := time.Now().Add(150 * time.Millisecond)
		return d, time.NewTimer(time.Until(d))
	}
	token, revoke, err := h.Issue(ctx, ClaudeCodeMCPGrant{Binding: binding, Owner: "wake-fixture", Incarnation: "witnessed", Tools: []ClaudeCodeMCPTool{{Name: "scratchpad", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ExecuteChecked: bridge.execute})
	require.NoError(t, err)
	defer revoke()
	g := h.grants[token]
	g.serial <- struct{}{}
	held := true
	defer func() {
		if held {
			<-g.serial
		}
	}()
	server := httptest.NewServer(h)
	defer server.Close()
	request := func() map[string]json.RawMessage {
		req, e := http.NewRequest("POST", server.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scratchpad","arguments":{"action":"append","content":"witnessed"},"_meta":{"claudecode/toolUseId":"queued"}}}`))
		require.NoError(t, e)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, e := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		var body map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body
	}
	result := request()
	require.JSONEq(t, `{"code":-32000,"message":"tool queue wait timed out"}`, string(result["error"]))
	count, err := db.CountScratchpadEntries(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Zero(t, count)
	<-g.serial
	held = false
	// The timeout response completed the handler before release. Verify no effect
	// exists, then prove this exact checked call is executable on a fresh request.
	count, err = db.CountScratchpadEntries(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Zero(t, count)
	result = request()
	require.NotContains(t, result, "error")
	require.NotContains(t, string(result["result"]), `"isError":true`)
	count, err = db.CountScratchpadEntries(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
