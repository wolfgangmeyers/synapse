package managed

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

type restartMailBarrier struct {
	embeddedRuntime
	entered chan struct{}
	release chan struct{}
}

func (r *restartMailBarrier) Interrupt() { close(r.entered); <-r.release }

func TestClaudeRestartReoffersMail(t *testing.T) {
	ctx := context.Background()
	cfg := claudeTestConfig(t, `{"turns":[[{"text":"mail complete"}]]}`)
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	t.Setenv("FAKE_CLAUDE_SCRIPT", filepath.Join(cfg.Cwd, "script.json"))
	t.Setenv("FAKE_CLAUDE_RECORD", filepath.Join(cfg.Cwd, "record.jsonl"))
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
	require.NoError(t, err)
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	lane := f.m.laneFor(owner.binding.AgentID)
	barrier := &restartMailBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	lane.activeMu.Lock()
	lane.activeSession = barrier
	lane.activeMu.Unlock()
	req := RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-mail-regression", CanonicalBodyHash: "hash"}
	_, err = f.m.reserveClaudeCodeRestart(ctx, req, saved)
	require.NoError(t, err)
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not reach barrier")
	}
	id := "e8888888-8888-4888-8888-888888888888"
	f.enqueue(t, id)
	// This offer is consumed while the restart gate is closed.
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id))
	lane.activeMu.Lock()
	lane.activeSession = nil
	lane.activeMu.Unlock()
	close(barrier.release)
	require.Eventually(t, func() bool { return f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id) == 1 }, 3*time.Second, 10*time.Millisecond, "gate release must reoffer queued mail")
}

func TestClaudeRestartReofferSkipsUncertainMail(t *testing.T) {
	ctx := context.Background()
	cfg := claudeTestConfig(t, `{"turns":[[{"text":"mail complete"}]]}`)
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	t.Setenv("FAKE_CLAUDE_SCRIPT", filepath.Join(cfg.Cwd, "script.json"))
	t.Setenv("FAKE_CLAUDE_RECORD", filepath.Join(cfg.Cwd, "record.jsonl"))
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
	require.NoError(t, err)
	heldID := "e8888888-8888-4888-8888-888888888888"
	f.enqueue(t, heldID)
	attempt, err := f.db.ReserveEmbeddedMail(ctx, owner.binding, heldID)
	require.NoError(t, err)
	require.NoError(t, f.db.MarkClaudeMailWriteStarted(ctx, owner.binding, attempt.AttemptID))
	require.NoError(t, f.db.ReleaseEmbeddedUnconsumedAttempt(ctx, owner.binding, attempt.AttemptID))
	// Different sender and body avoid enqueue deduplication.
	nextID := "e9999999-9999-4999-8999-999999999999"
	_, err = f.db.Enqueue(ctx, protocol.Envelope{ID: nextID, From: "other", To: "failure-root", Body: "eligible later mail", CreatedAt: f.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = f.db.ResolveMessageTarget(ctx, nextID)
	require.NoError(t, err)
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	req := RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-held", CanonicalBodyHash: "hash"}
	_, err = f.m.reserveClaudeCodeRestart(ctx, req, saved)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, nextID) == 1 }, 5*time.Second, 10*time.Millisecond)
	held, err := f.db.ClaudeMailHeld(ctx, heldID)
	require.NoError(t, err)
	require.True(t, held)
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, heldID))
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, heldID))
}

// A failed stop must finish the gate but must not start a new mail offer, even
// when recording the failed owner is rejected. A Pi-selected fixture makes any
// accidental reoffer observable without trying to use the closed Claude pool.
func TestClaudeRestartStopFailureDoesNotReoffer(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("unexpected delivery")})
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	done := make(chan struct{})
	close(done)
	proc := &ClaudeCodeProcess{stdin: poolTestStdin{}, done: done, exited: done, closeDone: make(chan struct{}), waitErr: errClaudeCodeGroupUnconfirmed}
	pool := &f.m.claudeCodeProcesses
	pool.mu.Lock()
	pool.entries = map[string]*claudeCodeProcessEntry{owner.binding.AgentID: {process: proc}}
	pool.mu.Unlock()
	t.Cleanup(func() { pool.mu.Lock(); delete(pool.entries, owner.binding.AgentID); pool.mu.Unlock() })
	_, err = f.raw.Exec(`CREATE TRIGGER reject_owner_failure BEFORE UPDATE ON managed_agents BEGIN SELECT RAISE(ABORT, 'failure recording rejected'); END`)
	require.NoError(t, err)
	id := "e8888888-8888-4888-8888-888888888888"
	f.enqueue(t, id)
	req := RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-stop-failed", CanonicalBodyHash: "hash"}
	_, err = f.m.reserveClaudeCodeRestart(ctx, req, saved)
	require.NoError(t, err)
	f.m.startWG.Wait()
	lane := f.m.laneFor(owner.binding.AgentID)
	require.False(t, lane.embeddedResetPending())
	require.False(t, lane.embeddedResetRunning())
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id))
	message, err := f.db.MessageFor(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "queued", string(message.State))
}

// An idle goal must not be awakened by the restart's mail-only reoffer.
func TestClaudeRestartReofferDoesNotWakeIdleGoal(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	f.reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		_, _ = f.db.SetManagedGoalDesiredState(ctx, owner.binding.AgentID, false, "")
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "goal continuation"}}, ai.StopStop)
	}})
	_, err := f.db.SetManagedGoalDesiredState(ctx, owner.binding.AgentID, true, "unfinished goal")
	require.NoError(t, err)
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", saved.ActivityState)
	req := RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "restart-idle-goal", CanonicalBodyHash: "hash"}
	_, err = f.m.reserveClaudeCodeRestart(ctx, req, saved)
	require.NoError(t, err)
	f.m.startWG.Wait()
	require.Zero(t, f.calls.Load(), "restart mail reoffer must not start idle goal work")
	// Positive control: the same idle goal is eligible for an explicit goal wake.
	f.m.MaybeResumeEmbeddedGoal(owner.binding.AgentID)
	f.m.startWG.Wait()
	require.Positive(t, f.calls.Load(), "explicit goal wake must start the eligible goal")
}
