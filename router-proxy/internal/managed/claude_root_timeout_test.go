package managed

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercises the real HTTP/grant boundary without a 30-second sleep. The old
// HTTP timeout is observable as an artificial deadline in ExecuteChecked.
func TestClaudeMCPCallUsesCallerContextAndRevokes(t *testing.T) {
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	handler := (&Manager{store: db}).ClaudeCodeMCP()
	observed := make(chan bool, 1)
	token, revoke, err := handler.Issue(context.Background(), ClaudeCodeMCPGrant{Binding: b, Owner: "wake-fixture", Incarnation: "test", Tools: []ClaudeCodeMCPTool{{Name: "slow", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ExecuteChecked: func(ctx context.Context, _ ClaudeCodeMCPCall) (json.RawMessage, error) {
		_, deadline := ctx.Deadline()
		observed <- deadline
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mcpRequest(t, server.URL, token, "tools/call", map[string]any{"name": "slow", "arguments": map[string]any{}, "_meta": map[string]any{"claudecode/toolUseId": "one"}})
	}()
	select {
	case deadline := <-observed:
		require.False(t, deadline, "HTTP must not truncate configured tool lifetime")
	case <-time.After(5 * time.Second):
		t.Fatal("tool not entered")
	}
	revoke()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("revocation did not cancel tool")
	}
}
