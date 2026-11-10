package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestGoalActivityAgePruningCascadesPublicationMarkers(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	clock := deterministic.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s.clock = clock

	publish := func(agent string) (EmbeddedBinding, EmbeddedGoalEvaluationKey) {
		t.Helper()
		b := goalBind(t, s, agent, "session", 1, "generation", "root")
		user := receiptProviderUser(t, s, b, "do work")
		goalAPIPolicy(t, s, b, true, "finish")
		k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
		_, replay, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, EmbeddedGoalAppliedReceiptRequest{TurnKey: "turn-" + agent, TurnEntrySeq: k.UserSeq, TurnEntryDigest: k.UserDigest, GoalRevision: k.GoalRevision})
		require.NoError(t, err)
		require.False(t, replay)
		body, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop})
		require.NoError(t, err)
		final, err := s.AppendEmbeddedEntry(ctx, b, "assistant", body)
		require.NoError(t, err)
		k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
		_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
		require.NoError(t, err)
		require.True(t, claimed)
		_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationCompleted, "finished")
		require.NoError(t, err)
		require.True(t, changed)
		presentation := &ManagedHistoryPresentation{Kind: "goal_evaluation", Outcome: "completed", Summary: "finished"}
		_, err = s.RecordEmbeddedGoalEvaluationActivity(ctx, b, k, EmbeddedGoalEvaluationCompleted, "finished", "completed", "event-"+agent, presentation, "detail")
		require.NoError(t, err)
		return b, k
	}
	old, oldKey := publish("age-cascade-old")
	require.NoError(t, clock.Advance(29*24*time.Hour))
	recent, recentKey := publish("age-cascade-recent")

	check := func(b EmbeddedBinding, k EmbeddedGoalEvaluationKey, want int) {
		t.Helper()
		for _, table := range []string{"embedded_goal_evaluations", "embedded_goal_activity_publications"} {
			var count int
			err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, k.UserSeq, k.UserDigest, k.GoalRevision).Scan(&count)
			require.NoError(t, err)
			require.Equal(t, want, count, "%s for %s", table, b.AgentID)
		}
	}
	check(old, oldKey, 1)
	check(recent, recentKey, 1)
	require.NoError(t, clock.Advance(24*time.Hour))
	_, err := s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	check(old, oldKey, 0)
	check(recent, recentKey, 1)
	var violations int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations))
	require.Zero(t, violations)
}
