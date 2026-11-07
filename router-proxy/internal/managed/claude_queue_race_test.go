package managed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueueSimultaneouslyReady(t *testing.T) {
	// Both cases are ready before select. Repetition exercises Go's selection of
	// either branch; every outcome must reject and leave the semaphore empty.
	for i := 0; i < 256; i++ {
		expired := make(chan time.Time, 1)
		expired <- time.Now()
		serial := make(chan struct{}, 1)
		err := claudeMCPAcquireSerial(context.Background(), serial, time.Now().Add(-time.Second), expired)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired admission accepted at iteration %d: %v", i, err)
		}
		if len(serial) != 0 {
			t.Fatal("semaphore leaked")
		}
	}
}

func TestQueueCanceledReadySemaphore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 256; i++ {
		serial := make(chan struct{}, 1)
		err := claudeMCPAcquireSerial(ctx, serial, time.Now().Add(time.Hour), make(chan time.Time))
		if !errors.Is(err, context.Canceled) || len(serial) != 0 {
			t.Fatalf("canceled admission: %v, held=%d", err, len(serial))
		}
	}
}

func TestQueueSuccessfulAdmission(t *testing.T) {
	serial := make(chan struct{}, 1)
	if err := claudeMCPAcquireSerial(context.Background(), serial, time.Now().Add(time.Hour), make(chan time.Time)); err != nil {
		t.Fatal(err)
	}
	if len(serial) != 1 {
		t.Fatal("not acquired")
	}
	<-serial
}

func TestQueueExpiredAfterSemaphoreSelected(t *testing.T) {
	// The timer delivery can lag its deadline; this forces the semaphore branch.
	serial := make(chan struct{}, 1)
	err := claudeMCPAcquireSerial(context.Background(), serial, time.Now().Add(-time.Second), nil)
	if !errors.Is(err, context.DeadlineExceeded) || len(serial) != 0 {
		t.Fatalf("expired admission: %v, held=%d", err, len(serial))
	}
}

func TestQueueRequestAndGrantCanceled(t *testing.T) {
	for _, kind := range []string{"request", "grant"} {
		t.Run(kind, func(t *testing.T) {
			db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
			h := (&Manager{store: db}).ClaudeCodeMCP()
			calls := make(chan struct{}, 1)
			queued := make(chan struct{}, 1)
			h.queueDeadline = func() (time.Time, *time.Timer) {
				deadline := time.Now().Add(time.Hour)
				queued <- struct{}{}
				return deadline, time.NewTimer(time.Until(deadline))
			}
			grantCtx, cancelGrant := context.WithCancel(context.Background())
			defer cancelGrant()
			token, revoke, err := h.Issue(grantCtx, ClaudeCodeMCPGrant{Binding: b, Owner: "wake-fixture", Incarnation: "queue-test", Tools: []ClaudeCodeMCPTool{{Name: "scratchpad", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ExecuteChecked: func(context.Context, ClaudeCodeMCPCall) (json.RawMessage, error) {
				calls <- struct{}{}
				return json.RawMessage(`{"content":[]}`), nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer revoke()
			g := h.grants[token]
			g.serial <- struct{}{}
			defer func() { <-g.serial }()
			requestCtx, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			req := httptest.NewRequest("POST", "/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scratchpad","arguments":{},"_meta":{"claudecode/toolUseId":"q"}}}`)).WithContext(requestCtx)
			req.Header.Set("Authorization", "Bearer "+token)
			done := make(chan struct{})
			recorder := httptest.NewRecorder()
			go func() { defer close(done); h.ServeHTTP(recorder, req) }()
			select {
			case <-queued:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not reach queue")
			}
			if kind == "request" {
				cancelRequest()
			} else {
				cancelGrant()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation blocked")
			}
			var response struct {
				JSONRPC string `json:"jsonrpc"`
				ID      int    `json:"id"`
				Error   *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if recorder.Code != 200 || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response.JSONRPC != "2.0" || response.ID != 1 || response.Error == nil || response.Error.Code != -32000 || response.Error.Message != "tool request cancelled" {
				t.Fatalf("unexpected cancellation response: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			select {
			case <-calls:
				t.Fatal("canceled call executed")
			default:
			}
		})
	}
}
