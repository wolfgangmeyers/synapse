package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// A new settled final is independently admissible even after the original
// turn's admission window has closed. Earlier finals must not regain authority.
func TestWorkflowFreshFinalAfterUserExpiry(t *testing.T) {
	s, b, old := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &old)
	var admitted int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, old.UserSeq).Scan(&admitted))
	clock := deterministic.NewFixedClock(time.UnixMilli(admitted).Add(30 * 24 * time.Hour))
	s.clock = clock
	fresh := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	key := old
	key.FinalSeq, key.FinalDigest = fresh.Sequence, fresh.Digest
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, key)
	require.NoError(t, err)
	require.True(t, claimed)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, old)
	require.Error(t, err)
}

func TestWorkflowFreshFinalReservationAndPruning(t *testing.T) {
	s, b, old := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &old)
	var a int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, old.UserSeq).Scan(&a))
	clock := deterministic.NewFixedClock(time.UnixMilli(a).Add(30 * 24 * time.Hour))
	s.clock = clock
	final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	k := old
	k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
	_, claimed, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	_, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	require.True(t, changed)
	var basis, expiry, f int64
	require.NoError(t, s.db.QueryRow(`SELECT v.retention_basis_ms,c.retention_expires_at_ms,e.retention_admitted_at_ms FROM embedded_goal_evaluations v JOIN embedded_goal_continuations c USING(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,final_seq,final_digest) JOIN embedded_entries e ON e.agent_id=v.agent_id AND e.session_id=v.session_id AND e.epoch=v.epoch AND e.generation=v.generation AND e.seq=v.final_seq WHERE v.agent_id=?`, b.AgentID).Scan(&basis, &expiry, &f))
	require.Equal(t, f, basis)
	require.Equal(t, f+2*embeddedEvidenceAgeMS, expiry)
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, &k)
	require.NoError(t, err)
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_applied_receipts"))
	require.NoError(t, clock.Advance(60*24*time.Hour))
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_evaluations"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_applied_receipts"))
	s.clock = deterministic.NewFixedClock(time.UnixMilli(f))
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.Error(t, err)
}

func TestWorkflowFreshFinalMissingReceiptFailsClosed(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	var a int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&a))
	s.clock = deterministic.NewFixedClock(time.UnixMilli(a).Add(30 * 24 * time.Hour))
	_, err := s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.Error(t, err)
}

func TestWorkflowFreshFinalSnoozeCallRemainsExpired(t *testing.T) {
	s, _, b, turn, clock := snoozeFixture(t)
	defer s.Close()
	ctx := context.Background()
	_, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "old-call", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	k := EmbeddedGoalEvaluationKey{UserSeq: turn.UserSeq, UserDigest: turn.UserDigest, GoalRevision: turn.GoalRevision}
	receiptForEvaluation(t, s, b, &k)
	// Make a fresh final at the cutoff.
	require.NoError(t, clock.Advance(30*24*time.Hour))
	final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	k.FinalSeq, k.FinalDigest = final.Sequence, final.Digest
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "old-call", []byte(`{"duration_ms":1000}`))
	require.ErrorContains(t, err, "retention-expired")
}

func TestWorkflowMigratedFinalCannotReviveAfterPruning(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/legacy.db"
	clock := deterministic.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, "134_distinct_settled_final_identity")
	s, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:idx+1]})
	require.NoError(t, err)
	b := goalBind(t, s, "migrated-no-revive", "session", 1, "gen", "root")
	user := receiptProviderUser(t, s, b, "go")
	goalAPIPolicy(t, s, b, true, "finish")
	k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
	receiptForEvaluation(t, s, b, &k)
	a := clock.Now()
	require.NoError(t, clock.Advance(15*24*time.Hour))
	fresh := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
	k.FinalSeq, k.FinalDigest = fresh.Sequence, fresh.Digest
	_, err = s.db.Exec(`INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,claimed_at) VALUES(?,?,?,?,?,?,?,?,?,?,'UNKNOWN',?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, b.OwnerToken, k.FinalSeq, k.FinalDigest, formatTimestamp(clock.Now()))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	var basis int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_basis_ms FROM embedded_goal_evaluations WHERE agent_id=?`, b.AgentID).Scan(&basis))
	require.Equal(t, a.UnixMilli(), basis)
	_, err = s.db.Exec(`INSERT OR REPLACE INTO embedded_evaluation_expired_keys SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,final_admitted_ms+1 FROM embedded_evaluation_expired_keys WHERE agent_id=?`, b.AgentID)
	require.ErrorContains(t, err, "invalid expired evaluation key")
	_, err = s.db.Exec(`INSERT INTO embedded_evaluation_expired_keys SELECT v.agent_id,v.session_id,v.epoch,v.generation,v.user_seq,v.user_digest,v.goal_revision,v.final_seq,v.final_digest,f.retention_admitted_at_ms+1 FROM embedded_goal_evaluations v JOIN embedded_entries f ON f.agent_id=v.agent_id AND f.session_id=v.session_id AND f.epoch=v.epoch AND f.generation=v.generation AND f.seq=v.final_seq WHERE v.agent_id=?`, b.AgentID)
	require.ErrorContains(t, err, "invalid expired evaluation key")
	require.NoError(t, clock.Advance(15*24*time.Hour))
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_evaluations"))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_evaluation_expired_keys"))
	var recursive int
	require.NoError(t, s.db.QueryRow(`PRAGMA recursive_triggers`).Scan(&recursive))
	require.Zero(t, recursive)
	_, err = s.db.Exec(`INSERT OR REPLACE INTO embedded_evaluation_expired_keys SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,final_admitted_ms+100000000000 FROM embedded_evaluation_expired_keys WHERE agent_id=?`, b.AgentID)
	require.ErrorContains(t, err, "invalid expired evaluation key")
	_, err = s.db.Exec(`INSERT OR REPLACE INTO embedded_evaluation_expired_keys SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,final_admitted_ms FROM embedded_evaluation_expired_keys WHERE agent_id=?`, b.AgentID)
	require.ErrorContains(t, err, "invalid expired evaluation key")
	_, err = s.db.Exec(`INSERT INTO embedded_evaluation_expired_keys SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision+1,final_seq,final_digest,final_admitted_ms FROM embedded_evaluation_expired_keys WHERE agent_id=?`, b.AgentID)
	require.ErrorContains(t, err, "invalid expired evaluation key")
	_, err = s.db.Exec(`DELETE FROM embedded_evaluation_expired_keys WHERE agent_id=?`, b.AgentID)
	require.Error(t, err)
	_, err = s.db.Exec(`INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,claimed_at,retention_basis_ms) SELECT ?,?,?,?,?,?,?,?,?,?,'UNKNOWN',?,retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, b.OwnerToken, k.FinalSeq, k.FinalDigest, formatTimestamp(clock.Now()), b.AgentID, b.SessionID, b.Epoch, b.Generation, k.FinalSeq)
	require.ErrorContains(t, err, "retention-expired")
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.ErrorContains(t, err, "retention-expired")
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{Clock: deterministic.NewFixedClock(a)})
	require.NoError(t, err)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.ErrorContains(t, err, "retention-expired")
	s.clock = clock
	require.NoError(t, clock.Advance(15*24*time.Hour))
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_evaluation_expired_keys"))
	s.clock = deterministic.NewFixedClock(a)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.ErrorContains(t, err, "retention-expired")
	require.NoError(t, s.Close())
}
