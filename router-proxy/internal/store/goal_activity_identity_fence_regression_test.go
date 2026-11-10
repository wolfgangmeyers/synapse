package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoalActivityRecoveryFences(t *testing.T) {
	for _, mutation := range []string{"session", "generation", "epoch", "managed"} {
		t.Run(mutation, func(t *testing.T) {
			s, b, _ := evaluationFixture(t)
			defer s.Close()
			_, err := s.db.Exec(`UPDATE managed_agent_policies SET applied_revision=desired_revision,projection_state='applied' WHERE agent_id=?`, b.AgentID)
			require.NoError(t, err)
			restartEvidence(t, s, b, 1, false)
			switch mutation {
			case "generation":
				_, err = s.db.Exec(`UPDATE managed_agents SET runtime_generation=? WHERE agent_id=?`, "superseded", b.AgentID)
			case "epoch":
				_, err = s.db.Exec(`UPDATE managed_agents SET session_epoch=session_epoch+1 WHERE agent_id=?`, b.AgentID)
			case "session":
				_, err = s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, b.AgentID)
			case "managed":
				_, err = s.db.Exec(`UPDATE managed_agents SET desired_state='terminated' WHERE agent_id=?`, b.AgentID)
			}
			require.NoError(t, err)
			require.NoError(t, s.BackfillEmbeddedGoalEvaluations(context.Background()))
			var n int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE category='managed_goal_evaluation'`).Scan(&n))
			require.Zero(t, n)
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_activity_publications`).Scan(&n))
			require.Zero(t, n)
		})
	}
}

func TestGoalActivityIdentityAndLegacyCollision(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	_, err := s.db.Exec(`UPDATE managed_agent_policies SET applied_revision=desired_revision,projection_state='applied' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	restartEvidence(t, s, b, 2, true)
	var k EmbeddedGoalEvaluationKey
	require.NoError(t, s.db.QueryRow(`SELECT user_seq,user_digest,goal_revision,final_seq,final_digest FROM embedded_goal_evaluations WHERE agent_id=? ORDER BY user_seq LIMIT 1`, b.AgentID).Scan(&k.UserSeq, &k.UserDigest, &k.GoalRevision, &k.FinalSeq, &k.FinalDigest))
	other := b
	other.SessionID = "other"
	require.NotEqual(t, GoalEvaluationActivityKey(b, k), GoalEvaluationActivityKey(other, k))
	other = b
	other.Epoch++
	require.NotEqual(t, GoalEvaluationActivityKey(b, k), GoalEvaluationActivityKey(other, k))
	// A migrated legacy marker already associates the old ambiguous key with another evaluation.
	legacy := fmt.Sprintf("goal-evaluation:%s:%d:%d:NOT_DONE", b.AgentID, k.UserSeq, k.FinalSeq)
	_, err = s.db.Exec(`INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key) VALUES(?, 'managed_goal_evaluation','info','Goal Evaluation • NOT_DONE','2026-01-01T00:00:00Z',?)`, b.AgentID, legacy)
	require.NoError(t, err)
	// Seed a prior publication using a separate durable claim with the same old key.
	_, err = s.db.Exec(`INSERT INTO embedded_goal_activity_publications SELECT agent_id,session_id,epoch,user_seq,user_digest,goal_revision,?,'old' FROM embedded_goal_evaluations WHERE agent_id=? AND user_seq<>?`, legacy, b.AgentID, k.UserSeq)
	require.NoError(t, err)
	require.NoError(t, s.BackfillEmbeddedGoalEvaluations(context.Background()))
	var event string
	require.NoError(t, s.db.QueryRow(`SELECT event_key FROM embedded_goal_activity_publications WHERE agent_id=? AND user_seq=?`, b.AgentID, k.UserSeq).Scan(&event))
	require.Equal(t, GoalEvaluationActivityKey(b, k), event)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE category='managed_goal_evaluation'`).Scan(&n))
	require.Equal(t, 2, n)
}

func TestGoalActivityRecoveryOwnerRotation(t *testing.T) {
	for _, outcome := range []string{EmbeddedGoalEvaluationNotDone, EmbeddedGoalEvaluationCompleted} {
		t.Run(outcome, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			receiptForEvaluation(t, s, b, &k)
			_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, claimed)
			_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, outcome, "finished")
			require.NoError(t, err)
			require.True(t, changed)
			_, err = s.db.Exec(`UPDATE embedded_sessions SET owner_token=owner_token+1 WHERE agent_id=?`, b.AgentID)
			require.NoError(t, err)
			p := &ManagedHistoryPresentation{Kind: "goal_evaluation", Outcome: "completed", Summary: "finished"}
			_, err = s.RecordEmbeddedGoalEvaluationActivity(ctx, b, k, outcome, "finished", "completed", "stale-owner", p, "finished")
			require.Error(t, err)
			for i := 0; i < 2; i++ {
				require.NoError(t, s.BackfillEmbeddedGoalEvaluations(ctx))
			}
			var n int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE category='managed_goal_evaluation'`).Scan(&n))
			require.Equal(t, 1, n)
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_activity_publications`).Scan(&n))
			require.Equal(t, 1, n)
		})
	}
}

func TestGoalActivityStaleOwnerCannotComplete(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET owner_token=owner_token+1 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationCompleted, "stale")
	require.Error(t, err)
	require.False(t, changed)
	current := b
	current.OwnerToken++
	_, changed, err = s.CompleteEmbeddedGoalEvaluation(ctx, current, k, EmbeddedGoalEvaluationCompleted, "stale")
	require.Error(t, err)
	require.False(t, changed)
	require.NoError(t, s.BackfillEmbeddedGoalEvaluations(ctx))
	var state string
	require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, b.AgentID).Scan(&state))
	require.Equal(t, EmbeddedGoalEvaluationUnknown, state)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_activity_publications`).Scan(&n))
	require.Zero(t, n)
}

func TestGoalActivityUnclaimedLegacyKeyAndAmbiguousReasons(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	_, err := s.db.Exec(`UPDATE managed_agent_policies SET applied_revision=desired_revision,projection_state='applied' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	restartEvidence(t, s, b, 2, true)
	var k EmbeddedGoalEvaluationKey
	require.NoError(t, s.db.QueryRow(`SELECT user_seq,user_digest,goal_revision,final_seq,final_digest FROM embedded_goal_evaluations WHERE agent_id=? ORDER BY user_seq LIMIT 1`, b.AgentID).Scan(&k.UserSeq, &k.UserDigest, &k.GoalRevision, &k.FinalSeq, &k.FinalDigest))
	legacy := fmt.Sprintf("goal-evaluation:%s:%d:%d:NOT_DONE", b.AgentID, k.UserSeq, k.FinalSeq)
	_, err = s.db.Exec(`INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,message_content) VALUES(?, 'managed_goal_evaluation','info','Goal Evaluation • NOT_DONE','2026-01-01T00:00:00Z',?,'historic content')`, b.AgentID, legacy)
	require.NoError(t, err)
	require.NoError(t, s.BackfillEmbeddedGoalEvaluations(context.Background()))
	var event string
	require.NoError(t, s.db.QueryRow(`SELECT event_key FROM embedded_goal_activity_publications WHERE agent_id=? AND user_seq=?`, b.AgentID, k.UserSeq).Scan(&event))
	require.Equal(t, GoalEvaluationActivityKey(b, k), event)
	// Model migration 128's multiple claims for the same ambiguous legacy key.
	_, err = s.db.Exec(`UPDATE embedded_goal_activity_publications SET event_key=?,reason=CASE WHEN user_seq=? THEN 'wrong one' ELSE 'wrong two' END WHERE agent_id=?`, legacy, k.UserSeq, b.AgentID)
	require.NoError(t, err)
	_, err = s.db.Exec(`DELETE FROM managed_complete_history WHERE event_key<>? AND category='managed_goal_evaluation'`, legacy)
	require.NoError(t, err)
	match, err := s.lastGoalActivityMatchesReasonTx(context.Background(), b.AgentID, "wrong one")
	require.NoError(t, err)
	require.False(t, match)
	match, err = s.lastGoalActivityMatchesReasonTx(context.Background(), b.AgentID, "historic content")
	require.NoError(t, err)
	require.True(t, match)
}
