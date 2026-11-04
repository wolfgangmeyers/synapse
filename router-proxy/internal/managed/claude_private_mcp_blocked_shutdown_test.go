package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Shutdown closes the transport and revokes authority; it does not promise to
// undo an effect already performed by a checked callback or wait for it to exit.
func TestClaudePrivateMCPShutdownDuringBlockedCheckedCall(t *testing.T) {
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &Manager{store: db, runCtx: ctx, cancel: cancel}
	url, err := m.ClaudeCodeMCPURL()
	require.NoError(t, err)
	s := m.ClaudeCodeMCP()
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var effects atomic.Int32
	defer func() {
		close(release)
		select {
		case <-entered:
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Error("checked callback did not finish after release")
			}
		default:
		}
	}()
	token, revoke, err := s.Issue(context.Background(), ClaudeCodeMCPGrant{
		Binding: binding, Owner: "wake-fixture", Incarnation: "blocked-shutdown",
		Tools: []ClaudeCodeMCPTool{{Name: "scratchpad", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ExecuteChecked: func(_ context.Context, _ ClaudeCodeMCPCall) (json.RawMessage, error) {
			defer close(finished)
			effects.Add(1) // An effect may already have happened before cancellation.
			close(entered)
			<-release // Deliberately ignore cancellation to test non-waiting shutdown.
			return json.RawMessage(`{"content":[]}`), nil
		},
	})
	require.NoError(t, err)
	defer revoke()
	s.mu.Lock()
	grantCtx := s.grants[token].ctx
	s.mu.Unlock()

	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scratchpad","arguments":{},"_meta":{"claudecode/toolUseId":"blocked"}}}`
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	clientDone := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		clientDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("checked callback did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- m.Close(context.Background()) }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("manager shutdown waited for blocked callback")
	}
	select {
	case <-grantCtx.Done():
	default:
		t.Fatal("grant was not canceled")
	}
	select {
	case err := <-clientDone:
		require.Error(t, err, "client must not receive a successful response")
	case <-time.After(5 * time.Second):
		t.Fatal("client connection remained open")
	}
	select {
	case <-finished:
		t.Fatal("callback unexpectedly finished before release")
	default:
	}
	require.EqualValues(t, 1, effects.Load(), "shutdown must not claim to roll back an already performed effect")
}
