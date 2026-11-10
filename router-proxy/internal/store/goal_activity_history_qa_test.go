package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoalActivityQARawDetailDedup(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "remaining")
	require.NoError(t, err)
	require.True(t, changed)
	p := &ManagedHistoryPresentation{Kind: "goal_evaluation", Outcome: "not_done", Summary: "remaining"}
	record := func(key string) (ManagedHistoryEntry, error) {
		return s.RecordEmbeddedGoalEvaluationActivity(ctx, b, k, EmbeddedGoalEvaluationNotDone, "remaining", "Goal Evaluation • NOT_DONE", key, p, "NOT_DONE\nremaining")
	}
	first, err := record("qa-first")
	require.NoError(t, err)
	require.NotEmpty(t, first.ID)
	second, err := record("qa-second")
	require.NoError(t, err)
	require.Empty(t, second.ID, "same parsed reason must deduplicate despite raw detail")
}

func TestGoalActivityQARetentionDoesNotResurrect(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "remaining")
	require.NoError(t, err)
	require.True(t, changed)
	p := &ManagedHistoryPresentation{Kind: "goal_evaluation", Outcome: "not_done", Summary: "remaining"}
	_, err = s.RecordEmbeddedGoalEvaluationActivity(ctx, b, k, EmbeddedGoalEvaluationNotDone, "remaining", "Goal Evaluation • NOT_DONE", fmt.Sprintf("goal-evaluation:%s:%d:%d:NOT_DONE", b.AgentID, k.UserSeq, k.FinalSeq), p, "raw")
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<5000) INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at) SELECT ?, 'qa', 'info', 'newer', '2026-01-01T00:00:00Z' FROM n`, b.AgentID)
	require.NoError(t, err)
	require.NoError(t, s.pruneManagedCompleteHistoryTx(ctx, b.AgentID))
	require.NoError(t, s.BackfillEmbeddedGoalEvaluations(ctx))
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, b.AgentID).Scan(&count))
	require.Zero(t, count, "retention-evicted activity must not reappear")
}
