package managed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueueHTTPTimeoutAndLongExecution(t *testing.T) {
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	h := (&Manager{store: db}).ClaudeCodeMCP()
	const wait = 150 * time.Millisecond
	queued := make(chan struct{}, 8)
	h.queueDeadline = func() (time.Time, *time.Timer) {
		d := time.Now().Add(wait)
		queued <- struct{}{}
		return d, time.NewTimer(time.Until(d))
	}
	entered := make(chan string, 8)
	release := make(chan struct{})
	var calls atomic.Int32
	token, revoke, err := h.Issue(context.Background(), ClaudeCodeMCPGrant{Binding: binding, Owner: "wake-fixture", Incarnation: "http-queue", Tools: []ClaudeCodeMCPTool{{Name: "scratchpad", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ExecuteChecked: func(ctx context.Context, call ClaudeCodeMCPCall) (json.RawMessage, error) {
		calls.Add(1)
		entered <- call.ToolUseID
		if call.ToolUseID == "long" {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return json.RawMessage(`{"content":[]}`), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	server := httptest.NewServer(h)
	defer server.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(id string) string {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scratchpad","arguments":{},"_meta":{"claudecode/toolUseId":"` + id + `"}}}`
		req, err := http.NewRequest("POST", server.URL, strings.NewReader(body))
		if err != nil {
			return err.Error()
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return err.Error()
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return err.Error()
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("HTTP status = %d, body %s", resp.StatusCode, b)
		}
		var envelope struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(b, &envelope); err != nil {
			t.Errorf("invalid JSON-RPC: %v", err)
			return string(b)
		}
		if envelope.JSONRPC != "2.0" || string(envelope.ID) != "1" {
			t.Errorf("bad JSON-RPC envelope: %s", b)
		}
		if id == "blocked" {
			if envelope.Error == nil || envelope.Error.Code != -32000 || envelope.Error.Message != "tool queue wait timed out" || len(envelope.Result) != 0 {
				t.Errorf("bad timeout response: %s", b)
			}
		} else if envelope.Error != nil || string(envelope.Result) != `{"content":[]}` {
			t.Errorf("bad success response: %s", b)
		}
		return string(b)
	}
	longDone := make(chan string, 1)
	go func() { longDone <- request("long") }()
	select {
	case id := <-entered:
		if id != "long" {
			t.Fatal(id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long execution did not start")
	}
	<-queued
	blockedDone := make(chan string, 1)
	go func() { blockedDone <- request("blocked") }()
	select {
	case <-queued:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked call did not reach queue")
	}
	select {
	case result := <-blockedDone:
		if !strings.Contains(result, "tool queue wait timed out") {
			t.Fatalf("unexpected response: %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked request did not time out")
	}
	if calls.Load() != 1 {
		t.Fatalf("blocked request executed: %d", calls.Load())
	}
	select {
	case result := <-longDone:
		t.Fatalf("admitted execution ended at queue deadline: %s", result)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case result := <-longDone:
		if !strings.Contains(result, `"result"`) {
			t.Fatalf("long request: %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long request did not finish")
	}
	result := request("later")
	if !strings.Contains(result, `"result"`) {
		t.Fatalf("grant unusable: %s", result)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}
