package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedGoalContinuationDueFromSavedTurn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		preference bool
		mismatch   string
	}{
		{name: "no preference"}, {name: "matching", preference: true},
		{name: "wrong turn", preference: true, mismatch: "turn"},
		{name: "wrong revision", preference: true, mismatch: "revision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			clock := deterministic.NewFixedClock(time.Now().UTC().Truncate(time.Millisecond))
			s.clock = clock
			if tc.preference {
				turn := EmbeddedGoalSnoozeTurn{UserSeq: k.UserSeq, UserDigest: k.UserDigest, GoalRevision: k.GoalRevision}
				if tc.mismatch == "turn" {
					turn.UserDigest = "wrong"
				}
				if tc.mismatch == "revision" {
					turn.GoalRevision++
				}
				if tc.mismatch == "" {
					_, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "snooze", []byte(`{"duration_ms":1000}`))
					require.NoError(t, err)
				} else {
					// A foreign preference row cannot be authority for this evaluation.
					_, err := s.db.Exec(`INSERT INTO embedded_goal_snooze_calls(call_id,agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,args_json,result_json,policy_version) VALUES(?,?,?,?,?,'root',?,?,?,'{"duration_ms":1000}','{}',1)`, "foreign", b.AgentID, b.SessionID, b.Epoch, b.Generation, turn.UserSeq, turn.UserDigest, turn.GoalRevision)
					if tc.mismatch == "turn" {
						require.ErrorContains(t, err, "retention-expired")
					} else {
						require.NoError(t, err)
					}
					if tc.mismatch != "turn" {
						_, err = s.db.Exec(`INSERT INTO embedded_goal_snooze_preferences(agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,policy_version,call_id,due_at) VALUES(?,?,?,?,? ,?,?,?,1,'foreign',?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, "root", turn.UserSeq, turn.UserDigest, turn.GoalRevision, "2030-01-02T03:04:06.000Z")
						require.NoError(t, err)
					}
				}
			}
			receiptForEvaluation(t, s, b, &k)
			_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			require.NoError(t, clock.Advance(250*time.Millisecond))
			s.embeddedGoalContinuationFault = func(string) error { return errors.New("rollback") }
			_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.ErrorContains(t, err, "rollback")
			require.False(t, changed)
			_, err = s.EmbeddedGoalContinuationFor(ctx, b, k)
			require.ErrorIs(t, err, sql.ErrNoRows)
			s.embeddedGoalContinuationFault = nil
			_, changed, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.NoError(t, err)
			require.True(t, changed)
			c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
			require.NoError(t, err)
			if tc.mismatch == "" && tc.preference {
				require.Equal(t, formatTimestamp(clock.Now().Add(750*time.Millisecond)), c.DueAt.String)
				require.True(t, c.DueAt.Valid)
				c, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
				require.NoError(t, err)
				require.False(t, started)
				require.Equal(t, "reserved", c.State)
				require.NoError(t, clock.Advance(749*time.Millisecond))
				_, started, err = s.StartEmbeddedGoalContinuation(ctx, b, k)
				require.NoError(t, err)
				require.False(t, started)
				require.NoError(t, clock.Advance(time.Millisecond))
			} else {
				require.False(t, c.DueAt.Valid)
			}
			_, changed, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.NoError(t, err)
			require.False(t, changed)
			if tc.preference && tc.mismatch == "" {
				_, savedReplay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, EmbeddedGoalSnoozeTurn{UserSeq: k.UserSeq, UserDigest: k.UserDigest, GoalRevision: k.GoalRevision}, "snooze", []byte(`{"duration_ms":1000}`))
				require.NoError(t, err)
				require.True(t, savedReplay)
			}
			replay, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
			require.NoError(t, err)
			require.Equal(t, c.DueAt, replay.DueAt)
			var count int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, b.AgentID).Scan(&count))
			require.Equal(t, 1, count)
			c, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, started)
			require.Equal(t, "unknown", c.Disposition)
			_, started, err = s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.False(t, started)
		})
	}
}

func TestEmbeddedGoalContinuationDueReopenAndInvalidation(t *testing.T) {
	for _, change := range []string{"reopen", "off", "revision", "epoch"} {
		t.Run(change, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			ctx := context.Background()
			clock := deterministic.NewFixedClock(time.Now().UTC().Truncate(time.Millisecond))
			s.clock = clock
			turn := EmbeddedGoalSnoozeTurn{UserSeq: k.UserSeq, UserDigest: k.UserDigest, GoalRevision: k.GoalRevision}
			_, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "due", []byte(`{"duration_ms":1000}`))
			require.NoError(t, err)
			receiptForEvaluation(t, s, b, &k)
			_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.NoError(t, err)
			path := s.path
			if change == "reopen" {
				require.NoError(t, s.Close())
				s, err = Open(ctx, path, Options{Clock: clock})
				require.NoError(t, err)
			}
			defer s.Close()
			if change == "off" {
				_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "off", EmbeddedGoalCommand{Action: "off"})
				require.NoError(t, err)
			}
			if change == "revision" {
				_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "new", EmbeddedGoalCommand{Action: "set", Goal: "new", Settings: DefaultManagedGoalSettings()})
				require.NoError(t, err)
			}
			if change == "epoch" {
				_, err = s.db.Exec(`UPDATE managed_agents SET session_epoch=session_epoch+1 WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			}
			c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
			require.NoError(t, err)
			require.True(t, c.DueAt.Valid)
			if change != "reopen" {
				require.Equal(t, "invalidated", c.State)
				return
			}
			_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.False(t, started)
			require.NoError(t, clock.Advance(time.Second))
			_, started, err = s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, started)
		})
	}
}

func TestEmbeddedGoalContinuation_HonorsPolicySnoozedUntil(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Now().UTC().Truncate(time.Millisecond))
	s.clock = clock

	// Set snoozed_until on managed_agent_policies 10 minutes into the future
	futureTime := clock.Now().Add(10 * time.Minute)
	futureStr := formatTimestamp(futureTime)
	err := s.SetManagedGoalSnoozeUntil(ctx, b.AgentID, futureStr)
	require.NoError(t, err)

	receiptForEvaluation(t, s, b, &k)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)

	// Complete evaluation as NOT_DONE without any turn-specific snooze preference
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	require.True(t, changed)

	c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
	require.NoError(t, err)
	require.True(t, c.DueAt.Valid)
	require.Equal(t, futureStr, c.DueAt.String)

	// Start continuation should not start before snoozed_until
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.False(t, started)

	// Advance clock past snooze
	require.NoError(t, clock.Advance(10*time.Minute))
	c, started, err = s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	require.Equal(t, "unknown", c.Disposition)
}

func TestEmbeddedGoalEvaluation_ClearsSnoozedUntilOnCompletion(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()

	futureStr := formatTimestamp(s.clock.Now().Add(10 * time.Minute))
	err := s.SetManagedGoalSnoozeUntil(ctx, b.AgentID, futureStr)
	require.NoError(t, err)

	receiptForEvaluation(t, s, b, &k)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)

	// Complete evaluation as DONE/COMPLETED
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationDone, "all finished")
	require.NoError(t, err)
	require.True(t, changed)

	var snoozedUntil sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&snoozedUntil)
	require.NoError(t, err)
	require.False(t, snoozedUntil.Valid)
}
