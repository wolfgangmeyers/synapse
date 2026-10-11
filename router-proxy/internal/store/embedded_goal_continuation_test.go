package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedGoalContinuationAtomicAndReplay(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = s.EmbeddedGoalContinuationFor(ctx, b, k)
	require.ErrorIs(t, err, sql.ErrNoRows)
	s.embeddedGoalContinuationFault = func(string) error { return errors.New("injected reserve failure") }
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "more")
	require.ErrorContains(t, err, "injected reserve failure")
	require.False(t, changed)
	e, err := s.embeddedEvaluationRow(ctx, b, k)
	require.NoError(t, err)
	require.Equal(t, EmbeddedGoalEvaluationUnknown, e.State)
	_, err = s.EmbeddedGoalContinuationFor(ctx, b, k)
	require.ErrorIs(t, err, sql.ErrNoRows)
	s.embeddedGoalContinuationFault = nil
	_, changed, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "more")
	require.NoError(t, err)
	require.True(t, changed)
	c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
	require.NoError(t, err)
	require.Equal(t, "goal_reevaluation", c.Origin)
	require.Equal(t, "reserved", c.State)
	require.Equal(t, "pending", c.Disposition)
	_, changed, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationDone, "contradiction")
	require.NoError(t, err)
	require.False(t, changed)
	// A missing reservation for an already committed NOT_DONE is reconciled,
	// never a second key. Simulate an older committed row via direct SQL.
	_, err = s.db.Exec(`DROP TRIGGER embedded_goal_continuation_no_delete`)
	require.NoError(t, err)
	_, err = s.db.Exec(`DELETE FROM embedded_goal_continuations WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, changed, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "more")
	require.NoError(t, err)
	require.False(t, changed)
	c, err = s.EmbeddedGoalContinuationFor(ctx, b, k)
	require.NoError(t, err)
	require.Equal(t, "reserved", c.State)
	_, changed, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "more")
	require.NoError(t, err)
	require.False(t, changed)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, b.AgentID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestEmbeddedGoalContinuationDoneAndStartOnce(t *testing.T) {
	for _, outcome := range []string{EmbeddedGoalEvaluationDone, EmbeddedGoalEvaluationNotDone} {
		t.Run(outcome, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			receiptForEvaluation(t, s, b, &k)
			_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, outcome, "")
			require.NoError(t, err)
			if outcome == EmbeddedGoalEvaluationDone {
				_, err = s.EmbeddedGoalContinuationFor(ctx, b, k)
				require.ErrorIs(t, err, sql.ErrNoRows)
				_, _, err = s.StartEmbeddedGoalContinuation(ctx, b, k)
				require.ErrorIs(t, err, sql.ErrNoRows)
				return
			}
			stale := b
			stale.Epoch++
			_, started, err := s.StartEmbeddedGoalContinuation(ctx, stale, k)
			require.Error(t, err)
			require.False(t, started)
			c, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, started)
			require.Equal(t, "started", c.State)
			require.Equal(t, "unknown", c.Disposition)
			require.NotEmpty(t, c.StartedAt)
			c, started, err = s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.False(t, started)
			require.Equal(t, "unknown", c.Disposition)
		})
	}
}

func TestEmbeddedGoalContinuationReopenUnknownNeverReplays(t *testing.T) {
	s, b, k := evaluationFixture(t)
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	path := s.path
	clock := s.clock
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()
	c, err := s.EmbeddedGoalContinuationFor(ctx, b, k)
	require.NoError(t, err)
	require.Equal(t, "reserved", c.State)
	c, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	c, started, err = s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.False(t, started)
	require.Equal(t, "unknown", c.Disposition)
	stale := b
	stale.Epoch++
	_, started, err = s.StartEmbeddedGoalContinuation(ctx, stale, k)
	require.Error(t, err)
	require.False(t, started)
}

func TestEmbeddedGoalContinuationStartInvalidation(t *testing.T) {
	for _, change := range []string{"off", "revision", "epoch"} {
		t.Run(change, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			receiptForEvaluation(t, s, b, &k)
			_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.NoError(t, err)
			switch change {
			case "off":
				_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "off", EmbeddedGoalCommand{Action: "off"})
				require.NoError(t, err)
			case "revision":
				_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "set2", EmbeddedGoalCommand{Action: "set", Goal: "next", Settings: DefaultManagedGoalSettings()})
				require.NoError(t, err)
			case "epoch":
				_, err = s.db.Exec(`UPDATE managed_agents SET session_epoch=session_epoch+1 WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
				b.Epoch++
			}
			c, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.False(t, started)
			if change == "epoch" {
				require.Error(t, err)
				require.Empty(t, c.State)
				old := b
				old.Epoch--
				c, err = s.EmbeddedGoalContinuationFor(ctx, old, k)
				require.NoError(t, err)
				require.Equal(t, "invalidated", c.State)
			} else {
				require.NoError(t, err)
				require.Equal(t, "invalidated", c.State)
				require.Equal(t, "invalidated", c.Disposition)
			}
		})
	}
}
