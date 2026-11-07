package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInvestigationQueueDeadline(t *testing.T) {
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	h := (&Manager{store: db}).ClaudeCodeMCP()
	called := make(chan struct{}, 1)
	token, revoke, err := h.Issue(context.Background(), ClaudeCodeMCPGrant{Binding: b, Owner: "wake-fixture", Incarnation: "queue-test", Tools: []ClaudeCodeMCPTool{{Name: "scratchpad", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ExecuteChecked: func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error) {
		called <- struct{}{}
		return json.RawMessage(`{"content":[]}`), nil
	}})
	require.NoError(t, err)
	defer revoke()
	g := h.grants[token]
	g.serial <- struct{}{}
	defer func() { <-g.serial }()
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scratchpad","arguments":{},"_meta":{"claudecode/toolUseId":"q"}}}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	done := make(chan *http.Response, 1)
	errs := make(chan error, 1)
	go func() {
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			errs <- e
			return
		}
		done <- resp
	}()
	select {
	case resp := <-done:
		defer resp.Body.Close()
		var body map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Contains(t, body, "error")
		if os.Getenv("EXPECT_WAIT") == "1" {
			t.Fatal("unexpected bounded response")
		}
		t.Log("queue rejected before 32s")
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(32 * time.Second):
		if os.Getenv("EXPECT_WAIT") != "1" {
			t.Fatal("queue did not reject within 32s")
		}
		t.Log("queue still waiting after 32s")
		cancel()
	}
	select {
	case <-called:
		t.Fatal("queued call executed")
	default:
	}
}
