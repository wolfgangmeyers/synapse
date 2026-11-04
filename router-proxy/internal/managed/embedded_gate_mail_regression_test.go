package managed

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func waitGateStarts(t *testing.T, m *Manager) {
	t.Helper()
	done := make(chan struct{})
	go func() { m.startWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("manager starts did not drain")
	}
}

func TestResetReservationFailureReoffersGatedMail(t *testing.T) {
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("delivered")})
	owner := f.launch(t, "")
	waitGateStarts(t, f.m) // Launch followups must not consume the test message.
	id := "e1111111-2222-4222-8222-222222222222"
	_, err := f.raw.Exec(`CREATE TRIGGER reject_gate_reset BEFORE INSERT ON managed_http_operations BEGIN SELECT RAISE(ABORT,'reset fault'); END`)
	require.NoError(t, err)
	f.enqueue(t, id)
	f.m.embeddedResetReservationTestHook = func() {
		f.m.dispatchEmbeddedMail(owner.binding.AgentID)
		require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id))
		require.Zero(t, f.calls.Load(), "gated offer must not start a turn")
	}
	saved, err := f.db.ManagedAgent(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	_, err = f.m.reserveEmbeddedReset(context.Background(), ResetRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/reset", RequestID: "gate-reset", CanonicalBodyHash: "hash"}, saved)
	require.Error(t, err)
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, 2*time.Second, time.Millisecond, "mail offer lost after reset gate release")
	waitGateStarts(t, f.m)
	require.Equal(t, int32(1), f.calls.Load(), "exactly one turn")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id), "exactly one attempt")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_acks k JOIN embedded_attempts a ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation WHERE a.message_id=?`, id), "exactly one delivery")
}

func TestTerminationReservationFailureReoffersGatedMail(t *testing.T) {
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("delivered")})
	owner := f.launch(t, "")
	waitGateStarts(t, f.m) // Launch followups must not consume the test message.
	_, err := f.raw.Exec(`CREATE TRIGGER reject_gate_terminate BEFORE INSERT ON managed_http_operations BEGIN SELECT RAISE(ABORT,'termination fault'); END`)
	require.NoError(t, err)
	id := "e1111111-3333-4333-8333-333333333333"
	f.enqueue(t, id)
	// Pause after the termination gate is published and admission unlocked,
	// before termination acquires serial. Dispatch can then take the real locks.
	entered, proceed := make(chan struct{}), make(chan struct{})
	f.m.embeddedTerminationGateTestHook = func() { close(entered); <-proceed }
	defer func() {
		select {
		case <-proceed:
		default:
			close(proceed)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	defer func() {
		cancel()
		select {
		case <-proceed:
		default:
			close(proceed)
		}
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("termination cleanup did not finish")
		}
	}()
	go func() {
		defer close(finished)
		_, e := f.m.Terminate(ctx, TerminateRequest{AgentID: owner.binding.AgentID, Method: "DELETE", CanonicalPath: "/terminate", RequestID: "gate-terminate", CanonicalBodyHash: "hash"})
		done <- e
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("termination returned before gate: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("termination did not publish gate")
	}
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id), "gated offer must not reserve mail")
	require.Zero(t, f.calls.Load(), "gated offer must not start a turn")
	close(proceed)
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("termination did not return")
	}
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, 3*time.Second, time.Millisecond, "mail offer lost after termination gate release")
	waitGateStarts(t, f.m)
	require.Equal(t, int32(1), f.calls.Load(), "exactly one turn")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id), "exactly one attempt")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_acks k JOIN embedded_attempts a ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation WHERE a.message_id=?`, id), "exactly one delivery")
}

func TestCommittedToolResetDoesNotDuplicateMailWake(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	f.m.embeddedMailBackoff = time.Hour
	_, err := f.raw.Exec(`CREATE TRIGGER reject_gate_mail BEFORE INSERT ON embedded_entries WHEN NEW.kind='user' BEGIN SELECT RAISE(ABORT,'mail fault'); END`)
	require.NoError(t, err)
	id := "e1111111-6666-4666-8666-666666666666"
	f.enqueue(t, id)
	lane := f.m.laneFor(owner.binding.AgentID)
	req := &contextResetRequest{committed: true}
	lane.admission.Lock()
	lane.resetToolGates = map[*contextResetRequest]struct{}{req: {}}
	lane.admission.Unlock()
	// A cancelled reset fails before rotating the binding, but still releases the
	// committed coordinator's gate. Its single re-offer must respect backoff.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lane.serial.Lock()
	f.m.finishCommittedToolReset(ctx, owner.binding, req, true)
	lane.serial.Unlock()
	attempts := func() int {
		lane.serial.Lock()
		defer lane.serial.Unlock()
		return lane.mailAttempts[id]
	}
	require.Eventually(t, func() bool { return attempts() >= 1 }, 5*time.Second, time.Millisecond)
	require.Never(t, func() bool { return attempts() > 1 }, 500*time.Millisecond, 10*time.Millisecond, "duplicate wake bypassed retry backoff")
}

func TestResetReservationReplayReoffersGatedMail(t *testing.T) {
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("delivered")})
	owner := f.launch(t, "")
	waitGateStarts(t, f.m) // Launch followups must not consume the test message.
	id := "e1111111-4444-4444-8444-444444444444"
	f.enqueue(t, id)
	req := ResetRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/reset", RequestID: "gate-replay", CanonicalBodyHash: "hash"}
	f.m.embeddedResetReservationTestHook = func() {
		f.m.dispatchEmbeddedMail(owner.binding.AgentID)
		require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id))
		require.Zero(t, f.calls.Load(), "gated offer must not start a turn")
		_, _, err := f.db.ReserveEmbeddedReset(context.Background(), owner.binding.AgentID, store.ManagedOperation{OperationID: req.RequestID, Method: req.Method, CanonicalPath: req.CanonicalPath, RequestID: req.RequestID, CanonicalBodyHash: req.CanonicalBodyHash, ResponseStatus: 202})
		require.NoError(t, err)
	}
	saved, err := f.db.ManagedAgent(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	result, err := f.m.reserveEmbeddedReset(context.Background(), req, saved)
	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, 3*time.Second, time.Millisecond)
	waitGateStarts(t, f.m)
	require.Equal(t, int32(1), f.calls.Load(), "exactly one turn")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id), "exactly one attempt")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_acks k JOIN embedded_attempts a ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation WHERE a.message_id=?`, id), "exactly one delivery")
}

func TestResetReleaseRespectsOverlappingTerminationGate(t *testing.T) {
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("delivered")})
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	id := "e1111111-5555-4555-8555-555555555555"
	lane := f.m.laneFor(owner.binding.AgentID)
	_, ok := lane.beginEmbeddedReset()
	require.True(t, ok)
	lane.admission.Lock()
	lane.terminationPending++
	lane.admission.Unlock()
	f.enqueue(t, id)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	f.m.endEmbeddedReset(owner.binding.AgentID, lane)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id))
	require.True(t, lane.embeddedResetPending())
	require.Zero(t, f.calls.Load(), "overlapping gate must not start a turn")
	lane.admission.Lock()
	lane.terminationPending--
	lane.admission.Unlock()
	f.m.OfferDirect(owner.binding.AgentID)
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, 3*time.Second, time.Millisecond)
	waitGateStarts(t, f.m)
	require.Equal(t, int32(1), f.calls.Load(), "exactly one turn")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id), "exactly one attempt")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_acks k JOIN embedded_attempts a ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation WHERE a.message_id=?`, id), "exactly one delivery")
}

func TestResetStopFailureNeverReoffersMail(t *testing.T) {
	for _, mode := range []string{"external", "tool", "drain"} {
		t.Run(mode, func(t *testing.T) {
			f := newFailureFixture(t)
			f.reg.SetResponses([]providers.FauxResponseStep{f.response("unexpected")})
			owner := f.launch(t, "")
			waitGateStarts(t, f.m)
			done := make(chan struct{})
			close(done)
			proc := &ClaudeCodeProcess{stdin: poolTestStdin{}, done: done, exited: done, closeDone: make(chan struct{}), waitErr: errClaudeCodeGroupUnconfirmed}
			pool := &f.m.claudeCodeProcesses
			pool.mu.Lock()
			pool.entries = map[string]*claudeCodeProcessEntry{owner.binding.AgentID: {process: proc}}
			pool.mu.Unlock()
			t.Cleanup(func() { pool.mu.Lock(); delete(pool.entries, owner.binding.AgentID); pool.mu.Unlock() })
			_, err := f.raw.Exec(`CREATE TRIGGER reject_reset_failure BEFORE UPDATE ON managed_agents BEGIN SELECT RAISE(ABORT, 'failure recording rejected'); END`)
			require.NoError(t, err)
			id := "e1111111-7777-4777-8777-777777777777"
			f.enqueue(t, id)
			lane := f.m.laneFor(owner.binding.AgentID)
			switch mode {
			case "external":
				require.Error(t, f.m.ExecuteContextReset(context.Background(), owner.binding, "test"))
			case "drain":
				lane.serial.Lock()
				err = f.m.resetEmbeddedContextAfterDrain(context.Background(), owner.binding, "test")
				lane.serial.Unlock()
				require.Error(t, err)
			case "tool":
				req := &contextResetRequest{committed: true}
				lane.admission.Lock()
				lane.resetToolGates = map[*contextResetRequest]struct{}{req: {}}
				lane.admission.Unlock()
				lane.serial.Lock()
				f.m.finishCommittedToolReset(context.Background(), owner.binding, req, true)
				lane.serial.Unlock()
			}
			waitGateStarts(t, f.m)
			require.False(t, lane.embeddedResetRunning())
			require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id))
			require.Zero(t, f.calls.Load())
		})
	}
}

func TestResetGateReleaseDoesNotWakeIdleGoal(t *testing.T) {
	ctx := context.Background()
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	f.reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		_, _ = f.db.SetManagedGoalDesiredState(ctx, owner.binding.AgentID, false, "")
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "goal"}}, ai.StopStop)
	}})
	_, err := f.db.SetManagedGoalDesiredState(ctx, owner.binding.AgentID, true, "unfinished goal")
	require.NoError(t, err)
	lane := f.m.laneFor(owner.binding.AgentID)
	_, ok := lane.beginEmbeddedReset()
	require.True(t, ok)
	f.m.endEmbeddedReset(owner.binding.AgentID, lane)
	waitGateStarts(t, f.m)
	require.Zero(t, f.calls.Load(), "mail-only release must not wake goals")
}

func TestSuccessfulResetDoesNotDuplicateMailWake(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	f.m.embeddedMailBackoff = time.Hour
	f.reg.SetResponses([]providers.FauxResponseStep{func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		_, err := f.raw.Exec(`CREATE TRIGGER reject_reset_mail BEFORE INSERT ON embedded_entries WHEN NEW.kind='user' BEGIN SELECT RAISE(ABORT,'mail fault'); END`)
		if err != nil {
			panic(err)
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "reset wake"}}, ai.StopStop)
	}})
	id := "e1111111-8888-4888-8888-888888888888"
	f.enqueue(t, id)
	require.NoError(t, f.m.ExecuteContextReset(context.Background(), owner.binding, "test"))
	lane := f.m.laneFor(owner.binding.AgentID)
	attempts := func() int { lane.serial.Lock(); defer lane.serial.Unlock(); return lane.mailAttempts[id] }
	require.Eventually(t, func() bool { return attempts() >= 1 }, 5*time.Second, time.Millisecond)
	require.Never(t, func() bool { return attempts() > 1 }, 500*time.Millisecond, 10*time.Millisecond)
}

func TestTerminationGateActuallyStopsMailDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("unexpected")})
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
	require.NoError(t, err)
	active := &terminationReplacementRuntime{resetBarrierRuntime: &resetBarrierRuntime{}, entered: make(chan struct{}), proceed: make(chan struct{})}
	f.m.activeSessionsMu.Lock()
	f.m.activeEmbeddedSessions[owner.binding.AgentID] = active
	f.m.activeSessionsMu.Unlock()
	t.Cleanup(func() {
		f.m.activeSessionsMu.Lock()
		delete(f.m.activeEmbeddedSessions, owner.binding.AgentID)
		f.m.activeSessionsMu.Unlock()
	})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(active.proceed) }) }
	defer release()
	id := "e1111111-9999-4999-8999-999999999999"
	done := make(chan error, 1)
	finished := make(chan struct{})
	defer func() {
		cancel()
		release()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("termination cleanup did not finish")
		}
	}()
	go func() {
		defer close(finished)
		_, err := f.m.Terminate(ctx, TerminateRequest{AgentID: owner.binding.AgentID, Method: "DELETE", CanonicalPath: "/terminate", RequestID: "123e4567-e89b-42d3-a456-426614174294", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
		done <- err
	}()
	select {
	case <-active.entered:
	case err := <-done:
		t.Fatalf("termination returned before gate: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("termination did not reach gate")
	}
	f.enqueue(t, id)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id))
	require.Zero(t, f.calls.Load())
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("termination did not return")
	}
	waitGateStarts(t, f.m)
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id))
}
