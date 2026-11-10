package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// fakeBarrierInvocation waits until the fake CLI reports that it is parked at
// the named barrier, and returns the invocation token that releases it.
func fakeBarrierInvocation(t *testing.T, dir, name string) string {
	t.Helper()
	var invocation string
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "record.jsonl"))
		for _, line := range strings.Split(string(b), "\n") {
			var rec struct {
				Kind       string `json:"kind"`
				Name       string `json:"name"`
				Invocation string `json:"invocation"`
			}
			if json.Unmarshal([]byte(line), &rec) == nil && rec.Kind == "barrier" && rec.Name == name {
				invocation = rec.Invocation
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond)
	return invocation
}

func fakeUserWrites(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "record.jsonl"))
	require.NoError(t, err)
	return strings.Count(string(b), `\"type\":\"user\"`)
}

// A steer echoed after the first result is checked and answered by the next
// result on the same process; it must not remain held or be sent twice.
func TestClaudeRuntimeResultBeforeSteeredEchoConsumesMail(t *testing.T) {
	ctx := context.Background()
	r, p, pool, cfg := claudeRuntimeFixture(t, `{"turns_by_input":[{"contains":"steer-window-first","steps":[{"text":"partial"},{"barrier":"steer-window"},{"text":"final answer"}]},{"contains":"late echo","steps":[{"text":"steer answer"}]}]}`)
	require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
	_, err := p.db.Enqueue(ctx, protocol.Envelope{ID: "late-echo", From: "sender", To: p.binding.AgentID, Body: "late echo", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = p.db.ResolveMessageTarget(ctx, "late-echo")
	require.NoError(t, err)
	attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "late-echo")
	require.NoError(t, err)

	echoed := make(chan struct{})
	var echoOnce sync.Once
	p.afterUserAppend = func(context.Context) { echoOnce.Do(func() { close(echoed) }) }

	type runResult struct {
		outcome *embeddedOutcome
		err     error
	}
	done := make(chan runResult, 1)
	go func() {
		out, e := r.Run(ctx, "steer-window-first")
		done <- runResult{out, e}
	}()
	select {
	case <-echoed:
	case <-time.After(10 * time.Second):
		t.Fatal("first input echo not checked")
	}
	require.Eventually(t, r.canSteer, 10*time.Second, time.Millisecond)
	invocation := fakeBarrierInvocation(t, cfg.Cwd, "steer-window")
	r.mu.Lock()
	proc := r.process
	r.mu.Unlock()
	require.NotNil(t, proc)

	outcome, err := r.steerInput(ctx, buildSteerUserMessage(attempt, time.Now().UnixMilli()), attempt, "")
	require.NoError(t, err)
	require.Equal(t, claudeSteerAccepted, outcome, "the steer write must start and be awaiting its echo")
	require.Eventually(t, func() bool { return fakeUserWrites(t, cfg.Cwd) == 2 }, 10*time.Second, 10*time.Millisecond)

	// Release the barrier: the fake emits the answer, then the result, then the
	// queued steered echo. The runtime reads the result first.
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "barrier-"+invocation), nil, 0600))

	var first runResult
	select {
	case first = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("turn did not finish after result")
	}
	require.NoError(t, first.err, "a result before the steered echo must not fail the turn")
	require.NotNil(t, first.outcome)
	require.Equal(t, ai.StopStop, first.outcome.StopReason)
	require.Equal(t, "steer answer", r.Snapshot().LastAssistantText)

	held, err := p.db.ClaudeMailHeld(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.False(t, held, "checked late echo must consume the reservation")
	msg, err := p.db.MessageFor(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.Equal(t, "acknowledged", string(msg.State))
	id, err := p.db.NextEmbeddedMail(ctx, p.binding.AgentID)
	require.NoError(t, err)
	require.Empty(t, id)
	require.Equal(t, 2, fakeUserWrites(t, cfg.Cwd), "steer must not be resent")
	select {
	case <-proc.Done():
		t.Fatal("successful late echo must retain its process")
	default:
	}
	pool.mu.Lock()
	_, live := pool.entries[p.binding.AgentID]
	pool.mu.Unlock()
	require.True(t, live)

}

// A usage limit reported while a steered write is unechoed must not start the
// retry path: retry would re-send the last echoed input while the steered write
// is still unresolved. The turn fails closed and the steer stays held.
func TestClaudeRuntimeUsageLimitWithUnechoedSteerDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	past := time.Now().Add(-time.Minute).Unix()
	r, p, pool, cfg := claudeRuntimeFixture(t, fmt.Sprintf(`{"turns_by_input":[{"contains":"steer-window-limit","steps":[{"text":"partial"},{"barrier":"limit-window"},{"rate_limit":{"resetsAt":%d}}]}]}`, past))
	require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
	_, err := p.db.Enqueue(ctx, protocol.Envelope{ID: "limit-steer", From: "sender", To: p.binding.AgentID, Body: "limit steer", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = p.db.ResolveMessageTarget(ctx, "limit-steer")
	require.NoError(t, err)
	attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "limit-steer")
	require.NoError(t, err)

	echoed := make(chan struct{})
	var echoOnce sync.Once
	p.afterUserAppend = func(context.Context) { echoOnce.Do(func() { close(echoed) }) }
	done := make(chan error, 1)
	go func() {
		_, e := r.Run(ctx, "steer-window-limit")
		done <- e
	}()
	select {
	case <-echoed:
	case <-time.After(10 * time.Second):
		t.Fatal("first input echo not checked")
	}
	require.Eventually(t, r.canSteer, 10*time.Second, time.Millisecond)
	invocation := fakeBarrierInvocation(t, cfg.Cwd, "limit-window")
	outcome, err := r.steerInput(ctx, buildSteerUserMessage(attempt, time.Now().UnixMilli()), attempt, "")
	require.NoError(t, err)
	require.Equal(t, claudeSteerAccepted, outcome)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "barrier-"+invocation), nil, 0600))

	select {
	case err = <-done:
		require.Error(t, err, "usage limit with an unechoed steer must fail closed")
		require.ErrorContains(t, err, "usage limit")
	case <-time.After(15 * time.Second):
		t.Fatal("turn did not finish")
	}
	require.Equal(t, 2, fakeUserWrites(t, cfg.Cwd), "no retry write may follow a limit with an unresolved steer")
	held, err := p.db.ClaudeMailHeld(ctx, attempt.MessageID)
	require.NoError(t, err)
	require.True(t, held)
	pool.mu.Lock()
	_, live := pool.entries[p.binding.AgentID]
	pool.mu.Unlock()
	require.False(t, live)
}

// These cases hold the fake at an explicit boundary until the steer is written.
func TestClaudeLateSteerFailureRetiresProcess(t *testing.T) {
	for _, tc := range []struct {
		name, steps string
		interrupt   bool
	}{
		{"error", `[{"barrier":"failure-window"},{"emit":{"type":"result","subtype":"error_during_execution","result":"failed"}},{"hang":true}]`, false},
		{"invalid_echo", `[{"barrier":"failure-window"},{"emit":{"type":"result","subtype":"success"}},{"emit":{"type":"user","isReplay":true,"uuid":"wrong-id","message":{"role":"user","content":"wrong"}}},{"hang":true}]`, false},
		{"cancel", `[{"barrier":"failure-window"},{"wait":"interrupt"}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, p, pool, cfg := claudeRuntimeFixture(t, fmt.Sprintf(`{"turns_by_input":[{"contains":"first","steps":%s}]}`, tc.steps))
			require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
			_, err := p.db.Enqueue(ctx, protocol.Envelope{ID: "pending", From: "sender", To: p.binding.AgentID, Body: "mail", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
			require.NoError(t, err)
			_, err = p.db.ResolveMessageTarget(ctx, "pending")
			require.NoError(t, err)
			attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "pending")
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { _, e := r.Run(ctx, "first"); done <- e }()
			invocation := fakeBarrierInvocation(t, cfg.Cwd, "failure-window")
			require.Eventually(t, r.canSteer, 5*time.Second, time.Millisecond)
			r.mu.Lock()
			proc := r.process
			r.mu.Unlock()
			outcome, err := r.steerInput(ctx, buildSteerUserMessage(attempt, time.Now().UnixMilli()), attempt, "")
			require.NoError(t, err)
			require.Equal(t, claudeSteerAccepted, outcome)
			require.Eventually(t, func() bool { return fakeUserWrites(t, cfg.Cwd) == 2 }, 5*time.Second, 10*time.Millisecond)
			require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "barrier-"+invocation), nil, 0600))
			if tc.interrupt {
				r.Interrupt()
			}
			select {
			case err = <-done:
				require.Error(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("turn did not finish")
			}
			select {
			case <-proc.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("process not retired")
			}
			held, err := p.db.ClaudeMailHeld(ctx, attempt.MessageID)
			require.NoError(t, err)
			require.True(t, held)
			msg, err := p.db.MessageFor(ctx, attempt.MessageID)
			require.NoError(t, err)
			require.NotEqual(t, "acknowledged", string(msg.State))
			pool.mu.Lock()
			_, live := pool.entries[p.binding.AgentID]
			pool.mu.Unlock()
			require.False(t, live)
		})
	}
}

func TestClaudeLateDeadlineRejectsBufferedEvents(t *testing.T) {
	p := &ClaudeCodeProcess{events: []ClaudeCodeEvent{{Type: "result"}}}
	_, err := claudeNextBeforeDeadline(p, context.Background(), time.Now().Add(-time.Second))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Len(t, p.events, 1, "expired deadline must not consume buffered output")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	p = &ClaudeCodeProcess{notify: make(chan struct{}), done: make(chan struct{})}
	_, err = claudeNextBeforeDeadline(p, ctx, time.Now().Add(20*time.Millisecond))
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
