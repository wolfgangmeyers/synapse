package store

import (
	"context"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// A later settled assistant under the same incoming turn is new work, while
// observing either already-claimed final again must remain idempotent.
func TestEmbeddedEvaluationDistinctSettledFinals(t *testing.T) {
	s, b, first := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &first)
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, first)
	require.NoError(t, err)
	require.True(t, claimed)
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, first, EmbeddedGoalEvaluationNotDone, "first")
	require.NoError(t, err)
	require.True(t, changed)

	final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	second := first
	second.FinalSeq, second.FinalDigest = final.Sequence, final.Digest
	_, claimed, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, second)
	require.NoError(t, err)
	require.True(t, claimed)
	_, claimed, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, second)
	require.NoError(t, err)
	require.False(t, claimed)
	_, changed, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, second, EmbeddedGoalEvaluationNotDone, "second")
	require.NoError(t, err)
	require.True(t, changed)
	old, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, first)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Equal(t, "first", old.Reason)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, b.AgentID).Scan(&count))
	require.Equal(t, 2, count)
}

// Upgrades must retain the original claim, reservation, and publication as one
// exact final identity; the old schema stores final only on the evaluation.
func TestEmbeddedDistinctFinalMigrationPreservesEvidence(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/legacy.db"
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, "133_workflow_journal_receipt_agent_delete")
	before, err := Open(ctx, path, Options{Migrations: migrations[:idx+1]})
	require.NoError(t, err)
	b := goalBind(t, before, "migration-final", "session", 1, "gen", "root")
	user := receiptProviderUser(t, before, b, "go")
	goalAPIPolicy(t, before, b, true, "finish")
	k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
	receiptForEvaluation(t, before, b, &k)
	_, err = before.db.Exec(`INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,claimed_at) VALUES(?,?,?,?,?,?,?,?,?,?,'UNKNOWN','2026-01-01T00:00:00.000Z')`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, b.OwnerToken, k.FinalSeq, k.FinalDigest)
	require.NoError(t, err)
	_, err = before.db.Exec(`UPDATE embedded_goal_evaluations SET state='NOT_DONE',reason='saved',completed_at='2026-01-01T00:00:00.000Z' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, err = before.db.Exec(`INSERT INTO embedded_goal_continuations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,state,disposition,reserved_at,retention_expires_at_ms) SELECT ?,?,?,?,?,?,?,'reserved','pending','2026-01-01T00:00:00.000Z',retention_admitted_at_ms+5184000000 FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, b.AgentID, k.UserSeq)
	require.NoError(t, err)
	_, err = before.db.Exec(`INSERT INTO embedded_goal_activity_publications(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,event_key,reason) VALUES(?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, k.UserSeq, k.UserDigest, k.GoalRevision, "saved-event", "saved")
	require.NoError(t, err)
	var prior string
	require.NoError(t, before.db.QueryRow(`SELECT json_array(state,disposition,reserved_at,due_at,started_at,retention_expires_at_ms) FROM embedded_goal_continuations WHERE agent_id=?`, b.AgentID).Scan(&prior))
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()
	var got string
	var seq int64
	var digest string
	require.NoError(t, after.db.QueryRow(`SELECT json_array(state,disposition,reserved_at,due_at,started_at,retention_expires_at_ms),final_seq,final_digest FROM embedded_goal_continuations WHERE agent_id=?`, b.AgentID).Scan(&got, &seq, &digest))
	require.Equal(t, prior, got)
	require.Equal(t, k.FinalSeq, seq)
	require.Equal(t, k.FinalDigest, digest)
	require.NoError(t, after.db.QueryRow(`SELECT final_seq,final_digest FROM embedded_goal_activity_publications WHERE agent_id=?`, b.AgentID).Scan(&seq, &digest))
	require.Equal(t, k.FinalSeq, seq)
	require.Equal(t, k.FinalDigest, digest)
	rows, err := after.db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
}

func TestEmbeddedDistinctFinalContinuationExactStart(t *testing.T) {
	s, b, first := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &first)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, first)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, first, EmbeddedGoalEvaluationNotDone, "first")
	require.NoError(t, err)
	final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	second := first
	second.FinalSeq, second.FinalDigest = final.Sequence, final.Digest
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, second)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, second, EmbeddedGoalEvaluationNotDone, "second")
	require.NoError(t, err)
	latest, found, err := s.LatestReservedEmbeddedGoalContinuation(ctx, b)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, second, latest)

	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, second)
	require.NoError(t, err)
	require.True(t, started)
	old, err := s.EmbeddedGoalContinuationFor(ctx, b, first)
	require.NoError(t, err)
	require.Equal(t, "reserved", old.State)
	newer, err := s.EmbeddedGoalContinuationFor(ctx, b, second)
	require.NoError(t, err)
	require.Equal(t, "started", newer.State)
	var executionToken int64
	err = s.db.QueryRowContext(ctx, `SELECT execution_token FROM embedded_sessions WHERE agent_id = ?`, b.AgentID).Scan(&executionToken)
	require.NoError(t, err)
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, executionToken))
}
