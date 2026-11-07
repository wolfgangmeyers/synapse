package managed

import (
	"context"
	"errors"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeWorkerExitSettlesWithVisibleActionableError(t *testing.T) {
	f := newFailureFixture(t)
	bin := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = auth ]; then echo '{\"loggedIn\":true}'; exit 0; fi\nexit 73\n"), 0700))
	t.Setenv("PATH", filepath.Dir(bin)+":"+os.Getenv("PATH"))
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	f.m.embeddedMailBackoff = time.Hour
	id := "e1111111-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	_, err = f.raw.Exec(`UPDATE messages SET sender='user' WHERE id=?`, id)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	}
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM messages WHERE id=? AND state='dead_letter'`, id))
	history, err := f.db.GetManagedCompleteHistory(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	var found bool
	for _, entry := range history {
		if entry.MessageContent != nil && strings.Contains(*entry.MessageContent, "Claude") && strings.Contains(*entry.MessageContent, "retry") {
			found = true
		}
	}
	require.True(t, found, "settled failure must have actionable user-visible history: %+v", history)
}

func TestClaudeWorkerStartFailureSettlesWithVisibleError(t *testing.T) {
	f := newFailureFixture(t)
	bin := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/missing-fixture-interpreter\n"), 0700))
	t.Setenv("PATH", filepath.Dir(bin)+":"+os.Getenv("PATH"))
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	f.m.embeddedMailBackoff = time.Hour
	id := "e2222222-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	_, err = f.raw.Exec(`UPDATE messages SET sender='user' WHERE id=?`, id)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	}
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM messages WHERE id=? AND state='dead_letter'`, id))
	history, err := f.db.GetManagedCompleteHistory(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	require.NotEmpty(t, history, "start failure must be visible")
}

func TestClaudeSettlementTransactionFailureDoesNotRerunWorker(t *testing.T) {
	f := newFailureFixture(t)
	bin := filepath.Join(t.TempDir(), "claude")
	count := filepath.Join(t.TempDir(), "starts")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho start >> '"+count+"'\nexit 73\n"), 0700))
	t.Setenv("PATH", filepath.Dir(bin)+":"+os.Getenv("PATH"))
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	f.m.embeddedMailBackoff = time.Hour
	id := "e3333333-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	_, err = f.raw.Exec(`CREATE TRIGGER fail_claude_settlement BEFORE UPDATE OF state ON messages WHEN NEW.state='dead_letter' BEGIN SELECT RAISE(ABORT,'injected settlement failure'); END`)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	}
	before, err := os.ReadFile(count)
	require.NoError(t, err)
	_, err = f.raw.Exec(`DROP TRIGGER fail_claude_settlement`)
	require.NoError(t, err)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	after, err := os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "retry must settle the original attempt without executing Claude")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM messages WHERE id=? AND state='dead_letter'`, id))
}

func TestClaudeAcquisitionGrantAndClosedPoolAreDefinite(t *testing.T) {
	pool := &claudeCodeProcessPool{}
	_, err := pool.acquire(context.Background(), store.EmbeddedBinding{AgentID: "agent"}, ClaudeCodeProcessConfig{}, func() (claudeCodeProcessGrant, error) { return claudeCodeProcessGrant{}, errors.New("grant failed") })
	require.ErrorIs(t, err, errClaudeDefiniteStart)
	pool.closed = true
	_, err = pool.acquire(context.Background(), store.EmbeddedBinding{AgentID: "agent"}, ClaudeCodeProcessConfig{}, nil)
	require.ErrorIs(t, err, errClaudeDefiniteStart)
}

func TestReviewerSettlementStaleAttemptClearsPending(t *testing.T) {
	f := newFailureFixture(t)
	bin := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 73\n"), 0700))
	t.Setenv("PATH", filepath.Dir(bin)+":"+os.Getenv("PATH"))
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	f.m.embeddedMailBackoff = time.Hour
	id := "e6666666-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	_, err = f.raw.Exec(`CREATE TRIGGER reviewer_fail_settlement BEFORE UPDATE OF state ON messages WHEN NEW.state='dead_letter' BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	}
	lane := f.m.laneFor(owner.binding.AgentID)
	require.NotNil(t, lane.claudePendingSettlement)
	_, err = f.raw.Exec(`DROP TRIGGER reviewer_fail_settlement`)
	require.NoError(t, err)
	lane.claudePendingSettlement.AttemptID = "stale-attempt"
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	require.Nil(t, lane.claudePendingSettlement, "stale settlement must not permanently block the lane")
}

func TestReviewerSettlementContinuesQueuedMail(t *testing.T) {
	f := newFailureFixture(t)
	bin := filepath.Join(t.TempDir(), "claude")
	count := filepath.Join(t.TempDir(), "starts")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho start >> '"+count+"'\nexit 73\n"), 0700))
	t.Setenv("PATH", filepath.Dir(bin)+":"+os.Getenv("PATH"))
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	f.m.embeddedMailBackoff = time.Hour
	id := "e7777777-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	next := "e8888888-7777-4777-8777-777777777777"
	// Identical live envelopes deduplicate even when their IDs differ.
	_, err = f.db.Enqueue(context.Background(), protocol.Envelope{ID: next, From: "sender", To: "failure-root", Body: "different queued mail", CreatedAt: f.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = f.db.ResolveMessageTarget(context.Background(), next)
	require.NoError(t, err)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM messages WHERE id=?`, next))
	for i := 0; i < 3; i++ {
		f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	}
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(count)
		return len(strings.Split(strings.TrimSpace(string(b)), "\n")) >= 4
	}, 2*time.Second, 10*time.Millisecond, "next queued message must be offered without another external event")
}

func TestReviewerUnclassifiedClaudeFailureIsNotInventedAsStdin(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	f.m.embeddedMailBackoff = time.Hour
	token, err := f.db.EnterEmbeddedSDKExecution(context.Background(), owner.binding, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.db.ClearEmbeddedExecution(context.Background(), owner.binding, token) })
	id := "e9999999-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	for i := 0; i < 3; i++ {
		f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	}
	held, err := f.db.ClaudeMailHeld(context.Background(), id)
	require.NoError(t, err)
	require.False(t, held, "unclassified failure must not invent an stdin marker")
	require.Zero(t, f.m.laneFor(owner.binding.AgentID).mailAttempts[id], "unclassified errors are not definite start attempts")
}

func TestClaudeRetryTimerDeduplicatesAndAutomaticallySettles(t *testing.T) {
	f := newFailureFixture(t)
	bin := filepath.Join(t.TempDir(), "claude")
	count := filepath.Join(t.TempDir(), "starts")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho start >> '"+count+"'\nexit 73\n"), 0700))
	t.Setenv("PATH", filepath.Dir(bin)+":"+os.Getenv("PATH"))
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)
	f.m.embeddedMailBackoff = time.Hour
	id := "eaaaaaaa-7777-4777-8777-777777777777"
	f.enqueue(t, id)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.serial.Lock()
	scheduled := lane.claudeRetryScheduled
	previousDeadline := lane.claudeRetryDeadline
	// A second schedule advances the deadline without creating duplicate executions.
	f.m.scheduleClaudeMailRetry(owner.binding.AgentID, time.Millisecond)
	advancedDeadline := lane.claudeRetryDeadline
	// Keep subsequent retries fast while the original deadline remains far away.
	f.m.embeddedMailBackoff = 100 * time.Millisecond
	lane.serial.Unlock()
	require.True(t, scheduled, "initial retry must be scheduled")
	require.True(t, advancedDeadline.Before(previousDeadline), "retry deadline must advance")
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(count)
		return err == nil && len(strings.Fields(string(b))) >= 2
	}, 5*time.Second, 10*time.Millisecond, "retry must start automatically")
	require.Eventually(t, func() bool {
		return f.count(t, `SELECT count(*) FROM messages WHERE id=? AND state='dead_letter'`, id) == 1
	}, 5*time.Second, 20*time.Millisecond)
	b, err := os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, 3, len(strings.Fields(string(b))))
}
