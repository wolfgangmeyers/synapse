package store

import (
	"context"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func (s *Store) embeddedExecutionProtectsTx(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey, callID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_execution_closures WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=? AND (?=0 OR (final_seq=? AND final_digest=?)) AND (?='' OR call_id=?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, k.FinalSeq, k.FinalSeq, k.FinalDigest, callID, callID).Scan(&n)
	return n > 0, err
}

func TestWorkflowExecutionExactEvaluator(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	ok, err := s.embeddedExecutionProtectsTx(ctx, b, k, "")
	require.NoError(t, err)
	require.False(t, ok)
	token, err := s.EnterEmbeddedGoalEvaluationExecution(ctx, b, k)
	require.NoError(t, err)
	ok, err = s.embeddedExecutionProtectsTx(ctx, b, k, "")
	require.NoError(t, err)
	require.True(t, ok)
	other := k
	other.FinalDigest = "other"
	ok, err = s.embeddedExecutionProtectsTx(ctx, b, other, "")
	require.NoError(t, err)
	require.False(t, ok)
	require.Error(t, s.ClearEmbeddedExecution(ctx, b, token+1))
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	_, err = s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=9223372036854775806 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, err = s.EnterEmbeddedGoalEvaluationExecution(ctx, b, k)
	require.Error(t, err)
}

func TestWorkflowExecutionSourceCurrentRollback(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	err = s.runImmediate(ctx, func() error {
		_, err := s.db.Exec(`UPDATE embedded_goal_continuations SET state='started',disposition='unknown',started_at=? WHERE agent_id=?`, formatTimestamp(s.clock.Now()), b.AgentID)
		if err != nil {
			return err
		}
		if err := s.installEmbeddedGoalContinuationExecutionTx(ctx, b, k); err != nil {
			return err
		}
		return context.Canceled
	})
	require.ErrorIs(t, err, context.Canceled)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_execution_closures WHERE agent_id=?`, b.AgentID).Scan(&n))
	require.Zero(t, n)
	_, started, startErr := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, startErr)
	require.True(t, started)
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, &k)
	require.NoError(t, err)
	user, err := s.AppendEmbeddedEntry(ctx, b, "user", []byte(`{"role":"user","content":"continuation"}`))
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, user.Sequence, user.Digest))
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_execution_closures WHERE agent_id=?`, b.AgentID).Scan(&n))
	require.Equal(t, 2, n)
	for _, key := range []EmbeddedGoalEvaluationKey{k, {UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: k.GoalRevision}} {
		ok, err := s.embeddedExecutionProtectsTx(ctx, b, key, "")
		require.NoError(t, err)
		require.True(t, ok)
	}
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	_, err = s.EnterEmbeddedSDKExecution(ctx, b, &k)
	require.Error(t, err)
}

func TestWorkflowExecutionSQLBoundedOwnerFence(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
	require.Error(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, "forged"))
	for _, q := range []string{`UPDATE embedded_sessions SET execution_json='{}' WHERE agent_id=?`, `UPDATE embedded_sessions SET execution_json='not json' WHERE agent_id=?`, `UPDATE embedded_sessions SET execution_token=execution_token-1 WHERE agent_id=?`, `UPDATE embedded_sessions SET execution_json=json_set(execution_json,'$.owner',9999) WHERE agent_id=?`} {
		_, err = s.db.Exec(q, b.AgentID)
		require.Error(t, err, q)
	}
	_, err = s.db.Exec(`UPDATE embedded_sessions SET owner_token=owner_token+1 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_execution_closures WHERE agent_id=?`, b.AgentID).Scan(&n))
	require.Zero(t, n)
	require.Error(t, s.ClearEmbeddedExecution(ctx, b, token))
	b.OwnerToken++
	next, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.Greater(t, next, token)
	require.Error(t, s.ClearEmbeddedExecution(ctx, b, token))
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, next))
}

func TestWorkflowExecutionCutoffMeaningAndStaleEntry(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	var a int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&a))
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
	var cutoff int64
	require.NoError(t, s.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&cutoff))
	require.Less(t, cutoff, a)
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	_, err = s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=? WHERE agent_id=?`, a, b.AgentID)
	require.NoError(t, err)
	token, err = s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.Error(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	stale := b
	stale.OwnerToken++
	_, err = s.EnterEmbeddedSDKExecution(ctx, stale, nil)
	require.Error(t, err)
}

func TestWorkflowExecutionSteeringAndRawResurrection(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
	user, err := s.AppendEmbeddedEntry(ctx, b, "user", []byte(`{"role":"user","content":"steer"}`))
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, user.Sequence, user.Digest))
	require.Error(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
	var raw string
	require.NoError(t, s.db.QueryRow(`SELECT execution_json FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&raw))
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	var a int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, user.Sequence).Scan(&a))
	_, err = s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=? WHERE agent_id=?`, a, b.AgentID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET execution_token=execution_token+1,execution_json=json_set(?,'$.token',execution_token+1) WHERE agent_id=?`, raw, b.AgentID)
	require.Error(t, err)
	token, err = s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.Error(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, user.Sequence, user.Digest))
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
}

func TestWorkflowExecutionCallFreezeReopenBackclock(t *testing.T) {
	s, path, b, turn, clock := snoozeFixture(t)
	ctx := context.Background()
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, turn.UserSeq, turn.UserDigest))
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "first", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "latest", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	require.NoError(t, clock.Advance(30*24*time.Hour))
	require.NoError(t, s.runImmediate(ctx, func() error {
		if err := s.refreshEmbeddedExecutionCallsTx(ctx); err != nil {
			return err
		}
		_, err := s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=? WHERE agent_id=?`, clock.Now().Add(-30*24*time.Hour).UnixMilli(), b.AgentID)
		return err
	}))
	key := EmbeddedGoalEvaluationKey{UserSeq: turn.UserSeq, UserDigest: turn.UserDigest, GoalRevision: turn.GoalRevision}
	ok, err := s.embeddedExecutionProtectsTx(ctx, b, key, "latest")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.embeddedExecutionProtectsTx(ctx, b, key, "first")
	require.NoError(t, err)
	require.False(t, ok)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET execution_json=json_set(execution_json,'$.current.call_id','first') WHERE agent_id=?`, b.AgentID)
	require.Error(t, err)
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{Clock: deterministic.NewFixedClock(clock.Now().Add(-100 * 24 * time.Hour))})
	require.NoError(t, err)
	defer reopened.Close()
	ok, err = reopened.embeddedExecutionProtectsTx(ctx, b, key, "latest")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, reopened.ClearEmbeddedExecution(ctx, b, token))
	next, err := reopened.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.Greater(t, next, token)
	require.Error(t, reopened.SetEmbeddedSDKExecutionUser(ctx, b, next, turn.UserSeq, turn.UserDigest))
	var n int
	require.NoError(t, reopened.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n))
	require.Zero(t, n)
}

func TestWorkflowExecutionActualOwnerReplacement(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	owner, err := s.AcquireEmbeddedOwner(ctx, b, "execution-owner-one", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = owner
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
	replacement, err := s.AcquireEmbeddedOwner(ctx, b, "execution-owner-two", time.Minute)
	require.NoError(t, err)
	require.Greater(t, replacement, owner, "actual owner change must fence old execution")
	require.Error(t, s.ClearEmbeddedExecution(ctx, b, token))
	b.OwnerToken = replacement
	next, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.Greater(t, next, token)
}

func TestWorkflowExecutionRawStartedSourceCannotResurrect(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, &k)
	require.NoError(t, err)
	var raw string
	require.NoError(t, s.db.QueryRow(`SELECT execution_json FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&raw))
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	_, err = s.db.Exec(`UPDATE embedded_sessions SET execution_token=execution_token+1,execution_json=json_set(?,'$.token',execution_token+1) WHERE agent_id=?`, raw, b.AgentID)
	require.Error(t, err, "historical started is not execution authority")
}

func TestWorkflowExecutionReplacedOwnerCannotAppendEffect(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	first, err := s.AcquireEmbeddedOwner(ctx, b, "old-runtime", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = first
	_, err = s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	next, err := s.AcquireEmbeddedOwner(ctx, b, "new-runtime", time.Minute)
	require.NoError(t, err)
	require.Greater(t, next, first)
	_, err = s.AppendEmbeddedEntry(ctx, b, "assistant", []byte(`{"role":"assistant","content":[]}`))
	require.Error(t, err, "old runtime effect must be fenced before successor slot replacement")
}

func TestWorkflowExecutionReplacedOwnerCannotToolEffect(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	first, err := s.AcquireEmbeddedOwner(ctx, b, "old-tool-runtime", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = first
	require.NoError(t, s.RecordEmbeddedEffectIntent(ctx, b, "old-effect", []byte(`{"name":"bash"}`)))
	next, err := s.AcquireEmbeddedOwner(ctx, b, "new-tool-runtime", time.Minute)
	require.NoError(t, err)
	require.Greater(t, next, first)
	require.Error(t, s.RecordEmbeddedEffectIntent(ctx, b, "stale-effect", []byte(`{"name":"bash"}`)))
	require.Error(t, s.ObserveEmbeddedEffect(ctx, b, "old-effect", []byte(`{"ok":true}`)))
	_, _, err = s.ApplyEmbeddedGoalCall(ctx, b, "stale-goal", EmbeddedGoalCommand{Action: "off"})
	require.Error(t, err)
}

func TestWorkflowExecutionSameUserKeepsRevisionAndValidatesToken(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
	goalAPIPolicy(t, s, b, true, "changed")
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, k.UserDigest))
	require.Error(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token+1, k.UserSeq, k.UserDigest))
	require.Error(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, k.UserSeq, "forged"))
	var rev int64
	require.NoError(t, s.db.QueryRow(`SELECT goal_revision FROM embedded_execution_closures WHERE agent_id=?`, b.AgentID).Scan(&rev))
	require.Equal(t, k.GoalRevision, rev)
}
