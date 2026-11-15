package store

import (
	"context"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestWorkflowLiveEvaluationsRetainSharedDependencies(t *testing.T) {
	s, _, b, turn, clock := snoozeFixture(t)
	defer s.Close()
	ctx := context.Background()
	_, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "call", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "call-2", []byte(`{"duration_ms":2000}`))
	require.NoError(t, err)
	k := EmbeddedGoalEvaluationKey{UserSeq: turn.UserSeq, UserDigest: turn.UserDigest, GoalRevision: turn.GoalRevision}
	receiptForEvaluation(t, s, b, &k)
	require.NoError(t, clock.Advance(30*24*time.Hour))
	final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Zero(t, embeddedRedCount(t, s, "embedded_execution_closures"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_continuations"))
	require.NoError(t, s.runImmediate(ctx, func() error { return s.pruneEmbeddedEvidenceTx(ctx) }))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_applied_receipts"))
	require.Equal(t, 2, embeddedRedCount(t, s, "embedded_goal_snooze_calls"))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_snooze_preferences"))
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationDone, "done")
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, clock.Advance(30*24*time.Hour))
	require.NoError(t, s.runImmediate(ctx, func() error { return s.pruneEmbeddedEvidenceTx(ctx) }))
	for _, table := range []string{"embedded_goal_evaluations", "embedded_goal_applied_receipts", "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences"} {
		require.Zero(t, embeddedRedCount(t, s, table), table)
	}
}
