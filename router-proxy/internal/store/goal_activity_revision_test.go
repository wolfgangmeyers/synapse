package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoalActivityClaimAndRevisionFence(t *testing.T) {
	for _, outcome := range []string{EmbeddedGoalEvaluationNotDone, EmbeddedGoalEvaluationCompleted} {
		t.Run(outcome, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			receiptForEvaluation(t, s, b, &k)
			_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, claimed)
			p := &ManagedHistoryPresentation{Kind: "goal_evaluation", Outcome: "completed", Summary: "reason"}
			record := func() (ManagedHistoryEntry, error) {
				return s.RecordEmbeddedGoalEvaluationActivity(ctx, b, k, outcome, "reason", "Goal Evaluation • "+outcome, "test-event", p, "reason")
			}
			_, err = record()
			require.Error(t, err)
			_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, outcome, "reason")
			require.NoError(t, err)
			require.True(t, changed)
			entry, err := record()
			require.NoError(t, err)
			require.NotEmpty(t, entry.ID)
			_, err = s.UpdateManagedGoalConfig(ctx, b.AgentID, ManagedGoalConfig{Enabled: true, Goal: "new goal", Settings: DefaultManagedGoalSettings()})
			require.NoError(t, err)
			_, err = record()
			require.Error(t, err)
			_, err = s.db.ExecContext(ctx, `DELETE FROM managed_complete_history WHERE agent_id=?`, b.AgentID)
			require.NoError(t, err)
			require.NoError(t, s.BackfillEmbeddedGoalEvaluations(ctx))
			var count int
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, b.AgentID).Scan(&count))
			require.Zero(t, count)
		})
	}
}
