package managed

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type diagnosticDiscard struct{}

func (diagnosticDiscard) Write(p []byte) (int, error) { return len(p), nil }
func (diagnosticDiscard) Close() error                { return nil }

func diagnosticTestProcess(q *claudeDiagnosticQueue) *ClaudeCodeProcess {
	return &ClaudeCodeProcess{inputs: make(map[string]claudeCodePendingInput), notify: make(chan struct{}, 1), done: make(chan struct{}), stdin: diagnosticDiscard{}, protocolDiagnostics: q}
}

func TestClaudeProtocolDiagnosticsBoundedAndSanitized(t *testing.T) {
	q := &claudeDiagnosticQueue{records: make(chan ClaudeProtocolDiagnostic, 64)}
	p := diagnosticTestProcess(q)
	p.inputs["secret-id"] = claudeCodePendingInput{content: []byte(`"secret-payload"`)}
	for i := 0; i < 20; i++ {
		p.recordProtocolEvent(ClaudeCodeEvent{Type: "secret-type", Subtype: "secret-subtype", UUID: "secret-id"})
	}
	p.recordProtocolEvent(ClaudeCodeEvent{Type: "user", IsReplay: true})
	require.Len(t, p.protocolRecent, 8)
	p.logProtocolDiagnostic(4, "replay_content_mismatch")
	p.logProtocolDiagnostic(4, "replay_content_mismatch")
	require.Len(t, q.records, 1)
	record := <-q.records
	require.Equal(t, ClaudeProtocolDiagnostic{Reason: "replay_content_mismatch", Pending: 1, Recent: "other,other,other,other,other,other,other,user_replay"}, record)
	encoded, err := json.Marshal(record)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
}

func TestClaudeDiagnosticBlockedSinkDoesNotBlockFailure(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	q := newClaudeDiagnosticQueue(func(ClaudeProtocolDiagnostic) { once.Do(func() { close(entered); <-release }) })
	q.records <- ClaudeProtocolDiagnostic{}
	<-entered
	for i := 0; i < cap(q.records); i++ {
		q.records <- ClaudeProtocolDiagnostic{}
	}
	p := diagnosticTestProcess(q)
	p.inputs["expected"] = claudeCodePendingInput{content: []byte(`"private"`)}
	done := make(chan struct{})
	go func() {
		p.read(strings.NewReader(`{"type":"user","isReplay":true,"uuid":"unexpected","message":{"role":"user","content":"secret"}}` + "\n"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("protocol failure blocked on diagnostic sink")
	}
	require.ErrorContains(t, p.Err(), "identity/content mismatch")
	require.Len(t, p.inputs, 1)
	require.Empty(t, p.events)
	// Release the test sink and drain without a goroutine stuck in the fixture.
	close(release)
	close(q.records)
}

func TestClaudeDiagnosticReservedBeforeWrite(t *testing.T) {
	q := &claudeDiagnosticQueue{records: make(chan ClaudeProtocolDiagnostic, 64)}
	p := diagnosticTestProcess(q)
	p.writeMu.Lock()
	done := make(chan error, 1)
	go func() { done <- p.SendUser(context.Background(), "private-id", json.RawMessage(`"private-input"`)) }()
	require.Eventually(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.inputs) == 1 }, time.Second, time.Millisecond)
	p.read(strings.NewReader(`{"type":"result","subtype":"success"}` + "\n"))
	select {
	case record := <-q.records:
		require.Equal(t, "result_with_pending_input", record.Reason)
		require.Equal(t, 1, record.Pending)
		require.Equal(t, "result", record.Recent)
	case <-time.After(time.Second):
		t.Fatal("missing diagnostic")
	}
	require.Len(t, p.inputs, 1)
	e, err := p.Next(context.Background())
	require.NoError(t, err)
	require.Equal(t, "result", e.Type)
	p.writeMu.Unlock()
	require.NoError(t, <-done)
	require.Len(t, p.inputs, 1)
}
