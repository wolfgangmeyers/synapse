package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestClaudeRetryDurableAdmissionAndFences(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	a := resetReservationMail(t, s, b, "retry-store")
	now := s.clock.Now()
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	state, err := s.RecordClaudeStartFailure(ctx, b, a, "private-secret")
	require.NoError(t, err)
	require.Equal(t, 1, state.Executions)
	require.Equal(t, "unknown", state.Category)
	require.WithinDuration(t, now.Add(6*time.Minute+35*time.Second), state.Deadline, time.Second)
	_, err = s.AdmitClaudeStartRetry(ctx, b, a, now)
	require.ErrorIs(t, err, ErrClaudeRetryNotDue)
	stale := *a
	stale.SnapshotID = "wrong"
	_, err = s.ClaudeStartRetry(ctx, b, &stale)
	require.ErrorIs(t, err, ErrStaleEmbeddedFence)
	require.NoError(t, clock.Advance(state.NextEligible.Sub(clock.Now())))
	state, err = s.AdmitClaudeStartRetry(ctx, b, a, state.NextEligible)
	require.NoError(t, err)
	require.Equal(t, 2, state.Executions)
	require.True(t, state.InFlight)
	_, err = s.AdmitClaudeStartRetry(ctx, b, a, state.NextEligible)
	require.Error(t, err)
	_, err = s.RecordClaudeStartFailure(ctx, b, a, "spawn")
	require.NoError(t, err)
	state, err = s.ClaudeStartRetry(ctx, b, a)
	require.NoError(t, err)
	require.Equal(t, "spawn", state.Category)
	require.False(t, state.InFlight)
	_, err = s.db.Exec(`DELETE FROM messages WHERE id=?`, a.MessageID)
	require.NoError(t, err)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM claude_start_retries`).Scan(&n))
	require.Zero(t, n)
}

func TestClaudeRetryPersistenceFailureAndLifecycle(t *testing.T) {
	ctx := context.Background()
	for _, operation := range []string{"rollback", "replacement", "settlement"} {
		t.Run(operation, func(t *testing.T) {
			s, b, _ := embeddedMailFixture(t)
			a := resetReservationMail(t, s, b, "retry-"+operation)
			_, err := s.AdmitClaudeStartInitial(ctx, b, a)
			require.NoError(t, err)
			if operation == "rollback" {
				_, err = s.db.Exec(`CREATE TRIGGER retry_fail BEFORE UPDATE ON claude_start_retries BEGIN SELECT RAISE(ABORT,'failure'); END`)
				require.NoError(t, err)
				_, err = s.RecordClaudeStartFailure(ctx, b, a, "auth")
				require.Error(t, err)
				state, err := s.ClaudeStartRetry(ctx, b, a)
				require.NoError(t, err)
				require.True(t, state.InFlight)
				return
			}
			_, err = s.RecordClaudeStartFailure(ctx, b, a, map[bool]string{true: "auth", false: "unknown"}[operation == "settlement"], operation == "settlement")
			require.NoError(t, err)
			if operation == "replacement" {
				_, err = s.db.Exec(`UPDATE messages SET message_incarnation_id='replacement' WHERE id=?`, a.MessageID)
			} else {
				_, err = s.SettleClaudeStartRetry(ctx, b, a, true)
			}
			require.NoError(t, err)
			var count int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM claude_start_retries`).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestClaudeRetrySettlementDeadlineAndInflight(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	a := resetReservationMail(t, s, b, "retry-deadline")
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	state, err := s.RecordClaudeStartFailure(ctx, b, a, "probe-died")
	require.NoError(t, err)
	_, err = s.SettleClaudeUnsubmittedMail(ctx, b, a, "auth")
	require.ErrorIs(t, err, ErrClaudeRetryNotDue)
	require.NoError(t, clock.Advance(state.NextEligible.Sub(clock.Now())))
	_, err = s.AdmitClaudeStartRetry(ctx, b, a, state.NextEligible)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE claude_start_retries SET deadline=? WHERE message_id=?`, formatTimestamp(s.clock.Now().Add(-time.Second)), a.MessageID)
	require.NoError(t, err)
	_, err = s.SettleClaudeStartRetry(ctx, b, a, true, "auth")
	require.ErrorIs(t, err, ErrClaudeRetryInFlight)
	_, err = s.RecordClaudeStartFailure(ctx, b, a, "probe-died")
	require.NoError(t, err)
	_, err = s.SettleClaudeUnsubmittedMail(ctx, b, a, "auth")
	require.NoError(t, err)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM claude_start_retries WHERE message_id=?`, a.MessageID).Scan(&n))
	require.Zero(t, n)
}

func TestClaudeRetryRebindInflightFailsClosed(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	a := resetReservationMail(t, s, b, "retry-rebind")
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	state, err := s.RecordClaudeStartFailure(ctx, b, a, "unknown")
	require.NoError(t, err)
	require.NoError(t, s.claudeRetryRebindSafe(ctx, a.MessageID, a.MessageIncarnationID, b.AgentID))
	require.NoError(t, clock.Advance(state.NextEligible.Sub(clock.Now())))
	_, err = s.AdmitClaudeStartRetry(ctx, b, a, state.NextEligible)
	require.NoError(t, err)
	require.ErrorIs(t, s.claudeRetryRebindSafe(ctx, a.MessageID, a.MessageIncarnationID, b.AgentID), ErrClaudeRetryNotDue)
}

func TestClaudeRetryAbsentDistinctFromMissingTarget(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	a := resetReservationMail(t, s, b, "retry-absent")
	_, err := s.ClaudeStartRetry(ctx, b, a)
	require.ErrorIs(t, err, ErrClaudeRetryAbsent)
	_, err = s.db.Exec(`DELETE FROM message_target_snapshots WHERE message_id=?`, a.MessageID)
	require.NoError(t, err)
	_, err = s.ClaudeStartRetry(ctx, b, a)
	require.ErrorIs(t, err, ErrStaleEmbeddedFence)
	require.NotErrorIs(t, err, ErrClaudeRetryAbsent)
}

func TestClaudeInitialAdmissionSurvivesUnresolvedFailure(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	a := resetReservationMail(t, s, b, "initial-crash")
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	_, err = s.AdmitClaudeStartInitial(ctx, b, a)
	require.Error(t, err)
	state, err := s.ClaudeStartRetry(ctx, b, a)
	require.NoError(t, err)
	require.True(t, state.InFlight)
	require.ErrorIs(t, s.claudeRetryRebindSafe(ctx, a.MessageID, a.MessageIncarnationID, b.AgentID), ErrClaudeRetryNotDue)
	_, err = s.SettleClaudeStartRetry(ctx, b, a, true)
	require.ErrorIs(t, err, ErrClaudeRetryInFlight)
}

func TestClaudeRejectedInputReleaseClearsAdmissionAtomically(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	a := resetReservationMail(t, s, b, "retry-input-rejected")
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	require.NoError(t, s.ReleaseClaudeRejectedInput(ctx, b, a))
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM claude_start_retries WHERE message_id=?`, a.MessageID).Scan(&count))
	require.Zero(t, count)
}

func TestClaudePromptDispositionSurvivesRecovery(t *testing.T) {
	ctx := context.Background()
	for _, category := range []string{"auth", "spawn"} {
		t.Run(category, func(t *testing.T) {
			s, b, _ := embeddedMailFixture(t)
			a := resetReservationMail(t, s, b, "retry-prompt-"+category)
			_, err := s.AdmitClaudeStartInitial(ctx, b, a)
			require.NoError(t, err)
			_, err = s.RecordClaudeStartFailure(ctx, b, a, category, true)
			require.NoError(t, err)
			state, err := s.ClaudeStartRetry(ctx, b, a)
			require.NoError(t, err)
			require.True(t, state.PromptSettlement)
			_, err = s.SettleClaudeStartRetry(ctx, b, a, false)
			require.NoError(t, err)
		})
	}
}

func TestClaudeRejectedInputRetainsReservationWhenAdmissionNotCleared(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	a := resetReservationMail(t, s, b, "retry-rejected-held")
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE messages SET claude_stdin_started=1 WHERE id=?`, a.MessageID)
	require.NoError(t, err)
	require.ErrorIs(t, s.ReleaseClaudeRejectedInput(ctx, b, a), ErrStaleEmbeddedFence)
	var attempts, retries int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_attempts WHERE attempt_id=?`, a.AttemptID).Scan(&attempts))
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM claude_start_retries WHERE message_id=?`, a.MessageID).Scan(&retries))
	require.Equal(t, 1, attempts)
	require.Equal(t, 1, retries)
}

func TestClaudeRetryAdmissionUsesClockAfterLock(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	a := resetReservationMail(t, s, b, "retry-lock-clock")
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	state, err := s.RecordClaudeStartFailure(ctx, b, a, "spawn")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(state.NextEligible.Sub(clock.Now())))
	stale := clock.Now()
	s.mu.Lock()
	done := make(chan error, 1)
	go func() { _, e := s.AdmitClaudeStartRetry(ctx, b, a, stale); done <- e }()
	require.NoError(t, clock.Advance(state.Deadline.Sub(clock.Now())))
	s.mu.Unlock()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrClaudeRetryNotDue)
	case <-time.After(time.Second):
		t.Fatal("admission remained blocked")
	}
	state, err = s.ClaudeStartRetry(ctx, b, a)
	require.NoError(t, err)
	require.Equal(t, 1, state.Executions)
	require.False(t, state.InFlight)
}

func TestClaudeRejectedInputDoesNotReleaseChangedEnvelope(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	a := resetReservationMail(t, s, b, "rejected-envelope-fence")
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	_, err = s.db.Exec(`DELETE FROM message_target_snapshots WHERE message_id=?`, a.MessageID)
	require.NoError(t, err)
	require.ErrorIs(t, s.ReleaseClaudeRejectedInput(ctx, b, a), ErrStaleEmbeddedFence)
	var attempts, retries int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_attempts WHERE attempt_id=?`, a.AttemptID).Scan(&attempts))
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM claude_start_retries WHERE message_id=?`, a.MessageID).Scan(&retries))
	require.Equal(t, 1, attempts)
	require.Equal(t, 1, retries)
}

func TestClaudeRejectedInputRejectsStaleAcceptedIdentity(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{"incarnation", "snapshot", "digest"} {
		t.Run(change, func(t *testing.T) {
			s, b, _ := embeddedMailFixture(t)
			a := resetReservationMail(t, s, b, "rejected-stale-"+change)
			_, err := s.AdmitClaudeStartInitial(ctx, b, a)
			require.NoError(t, err)
			stale := *a
			switch change {
			case "incarnation":
				stale.MessageIncarnationID += "stale"
			case "snapshot":
				stale.SnapshotID += "stale"
			case "digest":
				stale.AcceptedDigest += "stale"
			}
			require.ErrorIs(t, s.ReleaseClaudeRejectedInput(ctx, b, &stale), ErrStaleEmbeddedFence)
			state, err := s.ClaudeStartRetry(ctx, b, a)
			require.NoError(t, err)
			require.True(t, state.InFlight)
		})
	}
}

func TestClaudeRetryAdmissionRejectsPromptSettlement(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	a := resetReservationMail(t, s, b, "retry-prompt-admission")
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	state, err := s.RecordClaudeStartFailure(ctx, b, a, "auth", true)
	require.NoError(t, err)
	require.NoError(t, clock.Advance(state.NextEligible.Sub(clock.Now())))
	_, err = s.AdmitClaudeStartRetry(ctx, b, a, clock.Now())
	require.ErrorIs(t, err, ErrClaudeRetryNotDue)
	got, err := s.ClaudeStartRetry(ctx, b, a)
	require.NoError(t, err)
	require.Equal(t, state.Executions, got.Executions)
	require.False(t, got.InFlight)
	require.True(t, got.PromptSettlement)
	require.WithinDuration(t, state.Deadline, got.Deadline, time.Millisecond)
}

func TestClaudeRejectedRetryOfferAndReloadStaySettlementOnly(t *testing.T) {
	ctx := context.Background()
	s, b, path := embeddedMailFixture(t)
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	a := resetReservationMail(t, s, b, "rejected-retry-offer")
	_, err := s.AdmitClaudeStartInitial(ctx, b, a)
	require.NoError(t, err)
	original, err := s.RecordClaudeStartFailure(ctx, b, a, "spawn")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(original.NextEligible.Sub(clock.Now())))
	_, err = s.AdmitClaudeStartRetry(ctx, b, a, clock.Now())
	require.NoError(t, err)
	require.NoError(t, s.ReleaseClaudeRejectedInput(ctx, b, a))
	// A fresh offer reserves the same envelope and reloads durable state, just as recovery does.
	a, err = s.ReserveEmbeddedMail(ctx, b, a.MessageID)
	require.NoError(t, err)
	state, err := s.ClaudeStartRetry(ctx, b, a)
	require.NoError(t, err)
	require.WithinDuration(t, original.Deadline, state.Deadline, time.Millisecond)
	require.WithinDuration(t, original.Deadline, state.NextEligible, time.Millisecond)
	require.Equal(t, 1, state.Executions)
	require.Equal(t, "spawn", state.Category)
	_, err = s.AdmitClaudeStartRetry(ctx, b, a, clock.Now())
	require.ErrorIs(t, err, ErrClaudeRetryNotDue)

	// Reopen the database to exercise the durable fence after a router restart.
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer reopened.Close()
	state, err = reopened.ClaudeStartRetry(ctx, b, a)
	require.NoError(t, err)
	require.WithinDuration(t, original.Deadline, state.NextEligible, time.Millisecond)
	_, err = reopened.AdmitClaudeStartRetry(ctx, b, a, clock.Now())
	require.ErrorIs(t, err, ErrClaudeRetryNotDue)
}
