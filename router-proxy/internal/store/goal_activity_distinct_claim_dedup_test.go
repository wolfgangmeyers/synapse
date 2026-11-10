package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestGoalActivityDeduplicatesDistinctClaimsByLatestReason(t *testing.T) {
	s, b, first := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	revision := first.GoalRevision

	publish := func(i int, outcome, reason, raw string) ManagedHistoryEntry {
		t.Helper()
		k := first
		if i == 0 {
			receiptForEvaluation(t, s, b, &k)
		} else {
			user := receiptProviderUser(t, s, b, fmt.Sprintf("turn %d", i))
			k = EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: revision}
			_, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, EmbeddedGoalAppliedReceiptRequest{TurnKey: fmt.Sprintf("turn-%d", i), TurnEntrySeq: k.UserSeq, TurnEntryDigest: k.UserDigest, GoalRevision: k.GoalRevision})
			require.NoError(t, err)
			require.False(t, replay)
			body, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop})
			require.NoError(t, err)
			final, err := s.AppendEmbeddedEntry(ctx, b, "assistant", body)
			require.NoError(t, err)
			k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
		}
		_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
		require.NoError(t, err)
		require.True(t, claimed, "each turn must create a distinct claim")
		_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, outcome, reason)
		require.NoError(t, err)
		require.True(t, changed)
		presentationOutcome := "not_done"
		if outcome == EmbeddedGoalEvaluationCompleted {
			presentationOutcome = "completed"
		}
		p := &ManagedHistoryPresentation{Kind: "goal_evaluation", Outcome: presentationOutcome, Summary: reason}
		entry, err := s.RecordEmbeddedGoalEvaluationActivity(ctx, b, k, outcome, reason, "Goal Evaluation • "+outcome, fmt.Sprintf("distinct-claim-%d", i), p, raw)
		require.NoError(t, err)
		return entry
	}

	firstEntry := publish(0, EmbeddedGoalEvaluationNotDone, "work remains", "first raw detail")
	require.NotEmpty(t, firstEntry.ID)
	duplicate := publish(1, EmbeddedGoalEvaluationNotDone, "work remains", "different raw detail")
	require.Empty(t, duplicate.ID, "same reason across distinct claims must deduplicate despite different raw details")
	intervening := publish(2, EmbeddedGoalEvaluationNotDone, "different reason", "third raw detail")
	require.NotEmpty(t, intervening.ID)
	afterFence := publish(3, EmbeddedGoalEvaluationNotDone, "work remains", "fourth raw detail")
	require.NotEmpty(t, afterFence.ID, "deduplication must compare the latest goal activity, not any older matching reason")

	completed := publish(4, EmbeddedGoalEvaluationCompleted, "finished", "completion raw detail")
	require.NotEmpty(t, completed.ID)
	policy := goalAPIPolicy(t, s, b, true, "finish")
	revision = policy.DesiredRevision
	require.Greater(t, revision, first.GoalRevision)
	afterCompletion := publish(5, EmbeddedGoalEvaluationNotDone, "work remains", "after completion raw detail")
	require.NotEmpty(t, afterCompletion.ID, "completed activity must break the NOT_DONE deduplication fence")

	var history, publications int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, b.AgentID).Scan(&history))
	require.Equal(t, 5, history)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_activity_publications WHERE agent_id=?`, b.AgentID).Scan(&publications))
	require.Equal(t, 6, publications, "suppressed claims still record their publication decision")
}
