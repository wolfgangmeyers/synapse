package store

import (
	"context"
	"fmt"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestWorkflowRetentionAdmissionImmutable(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	s.clock = clock
	entry, err := s.AppendEmbeddedEntry(context.Background(), b, "user", []byte(`{"role":"user","content":"new","timestamp":9999999999999}`))
	require.NoError(t, err)
	var admitted int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, entry.Sequence).Scan(&admitted))
	require.Equal(t, s.clock.Now().UnixMilli(), admitted)
	_, err = s.db.Exec(`UPDATE embedded_entries SET retention_admitted_at_ms=retention_admitted_at_ms+1 WHERE agent_id=?`, b.AgentID)
	require.ErrorContains(t, err, "immutable")
}

func TestWorkflowRetentionCutoffMonotonic(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	_, err := s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=123 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=122 WHERE agent_id=?`, b.AgentID)
	require.ErrorContains(t, err, "monotonic")
	_, err = s.db.Exec(`UPDATE embedded_sessions SET owner_token=owner_token+1,current=0 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	var cutoff int64
	require.NoError(t, s.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&cutoff))
	require.EqualValues(t, 123, cutoff)
}

func TestWorkflowRetentionImportedAdmission(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	old := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	s.clock = old
	source, err := s.AppendEmbeddedEntry(ctx, b, "user", []byte(`{"role":"user","content":"old"}`))
	require.NoError(t, err)
	s.clock = deterministic.NewFixedClock(old.Now().Add(60 * 24 * time.Hour))
	copied, err := s.AppendImportedEmbeddedEntry(ctx, b, b, source.Sequence, source.Digest)
	require.NoError(t, err)
	var admitted int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, copied.Sequence).Scan(&admitted))
	require.Equal(t, old.Now().UnixMilli(), admitted)
	_, err = s.AppendImportedEmbeddedEntry(ctx, b, b, source.Sequence, "forged")
	require.Error(t, err)
}

func TestWorkflowRetentionReplacementAndReopen(t *testing.T) {
	s, b, k := evaluationFixture(t)
	var path string
	require.NoError(t, s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
	_, err := s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=123 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT OR REPLACE INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,'root',1)`, b.AgentID, b.SessionID, b.Epoch, b.Generation)
	require.ErrorContains(t, err, "replacement")
	_, err = s.db.Exec(`INSERT OR REPLACE INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest) SELECT agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq)
	require.Error(t, err)
	var admitted int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&admitted))
	require.NoError(t, s.Close())
	reopened, err := Open(context.Background(), path, Options{Clock: deterministic.NewFixedClock(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))})
	require.NoError(t, err)
	defer reopened.Close()
	var after, cutoff int64
	require.NoError(t, reopened.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&after))
	require.Equal(t, admitted, after)
	require.NoError(t, reopened.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=?`, b.AgentID).Scan(&cutoff))
	require.GreaterOrEqual(t, cutoff, int64(123))
	require.NoError(t, reopened.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&after))
	require.Zero(t, after)
}

func TestWorkflowRetentionLegacyBackfillOnce(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/legacy.db"
	migrations := DefaultMigrations()
	tip := migrationIndex(t, migrations, "121_workflow_evidence_retention")
	legacy, err := Open(ctx, path, Options{Migrations: migrations[:tip]})
	require.NoError(t, err)
	b := goalBind(t, legacy, "legacy-retention", "session", 1, "generation", "root")
	_, err = legacy.db.Exec(`INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest) VALUES(?,?,?,?,1,'genesis','user',?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, []byte(`{"timestamp":9999999999999}`), "unchanged-digest")
	require.NoError(t, err)
	require.NoError(t, legacy.Close())
	before := time.Now().Add(-time.Second).UnixMilli()
	upgraded, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	var stamp int64
	var body []byte
	var digest string
	require.NoError(t, upgraded.db.QueryRow(`SELECT retention_admitted_at_ms,body,digest FROM embedded_entries WHERE agent_id=?`, b.AgentID).Scan(&stamp, &body, &digest))
	require.GreaterOrEqual(t, stamp, before)
	require.LessOrEqual(t, stamp, time.Now().Add(time.Second).UnixMilli())
	require.Equal(t, `{"timestamp":9999999999999}`, string(body))
	require.Equal(t, "unchanged-digest", digest)
	require.NoError(t, upgraded.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	var second int64
	require.NoError(t, reopened.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=?`, b.AgentID).Scan(&second))
	require.Equal(t, stamp, second)
}

func TestWorkflowRetentionImportCarriesSourceCutoff(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	_, err := s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=123456 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	target := redBind(t, s, b.AgentID, "reset-target", 2, "new-generation", "root")
	s.clock = deterministic.NewFixedClock(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	_, err = s.AppendImportedEmbeddedEntry(ctx, target, b, k.UserSeq, k.UserDigest)
	require.NoError(t, err)
	var cutoff int64
	require.NoError(t, s.db.QueryRow(`SELECT evidence_expired_through_ms FROM embedded_sessions WHERE agent_id=? AND session_id=?`, target.AgentID, target.SessionID).Scan(&cutoff))
	require.EqualValues(t, 123456, cutoff)
}

func TestWorkflowRetentionInclusiveAdmissionAndCallChurn(t *testing.T) {
	s, _, b, turn, clock := snoozeFixture(t)
	ctx := context.Background()
	_, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "first", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	require.NoError(t, clock.Advance(30*24*time.Hour))
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "first", []byte(`{"duration_ms":1000}`))
	require.ErrorContains(t, err, "retention-expired")
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "new-call", []byte(`{"duration_ms":1000}`))
	require.ErrorContains(t, err, "retention-expired")
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_snooze_calls"))
}

func TestWorkflowRetentionPruneRollbackAndPending(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	require.NoError(t, clock.Advance(30*24*time.Hour))
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_continuations"))
	require.NoError(t, clock.Advance(30*24*time.Hour))
	_, err = s.db.Exec(`CREATE TEMP TRIGGER retention_fault BEFORE DELETE ON embedded_goal_evaluations BEGIN SELECT RAISE(ABORT,'retention fault'); END`)
	require.NoError(t, err)
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.ErrorContains(t, err, "retention fault")
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_continuations"))
	_, err = s.db.Exec(`DROP TRIGGER retention_fault`)
	require.NoError(t, err)
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_evaluations"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_continuations"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_applied_receipts"))
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.ErrorContains(t, err, "retention-expired")
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n))
	require.Zero(t, n)
}

func TestWorkflowRetentionExecutingUnknownBeyondExpiry(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	token, err := s.EnterEmbeddedGoalEvaluationExecution(ctx, b, k)
	require.NoError(t, err)
	require.NoError(t, clock.Advance(90*24*time.Hour))
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_evaluations"))
	e, changed, err := s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "late")
	require.ErrorContains(t, err, "retention-expired")
	_ = e
	_ = changed
	var state string
	require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, b.AgentID).Scan(&state))
	require.Equal(t, "NOT_DONE", state)
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_continuations"))
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_evaluations"))
}

func TestWorkflowRetentionDeniedAdmissionPersistsCutoff(t *testing.T) {
	s, _, b, turn, clock := snoozeFixture(t)
	ctx := context.Background()
	original := clock.Now()
	require.NoError(t, clock.Advance(30*24*time.Hour))
	_, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "expired", []byte(`{"duration_ms":1000}`))
	require.ErrorContains(t, err, "retention-expired")
	s.clock = deterministic.NewFixedClock(original)
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "rollback", []byte(`{"duration_ms":1000}`))
	require.ErrorContains(t, err, "retention-expired")
}

func TestWorkflowRetentionFutureDeadlineAllReservations(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(s.clock.Now())
	s.clock = clock
	var keys []EmbeddedGoalEvaluationKey
	for i := 0; i < 8; i++ {
		user := receiptProviderUser(t, s, b, "next")
		_, _, err := s.CommitEmbeddedGoalAppliedReceipt(ctx, b, EmbeddedGoalAppliedReceiptRequest{TurnKey: fmt.Sprintf("turn-%d", i), TurnEntrySeq: user.Sequence, TurnEntryDigest: user.Digest, GoalRevision: 1})
		require.NoError(t, err)
		final := receiptSDKAppend(t, s, b, ai.AssistantMessage{StopReason: ai.StopStop})
		k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1, FinalSeq: final.Sequence, FinalDigest: final.Digest}
		_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
		require.NoError(t, err)
		_, err = s.db.Exec(`UPDATE managed_agent_policies SET snoozed_until=? WHERE agent_id=?`, formatTimestamp(clock.Now().Add(31*24*time.Hour)), b.AgentID)
		require.NoError(t, err)
		_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
		require.NoError(t, err)
		keys = append(keys, k)
	}
	require.NoError(t, clock.Advance(30*24*time.Hour))
	_, err := s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 8, embeddedRedCount(t, s, "embedded_goal_continuations"))
	require.Equal(t, 8, embeddedRedCount(t, s, "embedded_goal_evaluations"))
	c, started, err := s.StartEmbeddedGoalContinuation(ctx, b, keys[0])
	require.NoError(t, err)
	require.False(t, started)
	require.True(t, c.DueAt.Valid)
	require.NoError(t, clock.Advance(24*time.Hour))
	_, started, err = s.StartEmbeddedGoalContinuation(ctx, b, keys[0])
	require.NoError(t, err)
	require.True(t, started)
	_, started, err = s.StartEmbeddedGoalContinuation(ctx, b, keys[0])
	require.NoError(t, err)
	require.False(t, started)
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, &keys[0])
	require.NoError(t, err)
	require.NoError(t, clock.Advance(31*24*time.Hour))
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_continuations"))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_evaluations"))
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_continuations"))
	_, _, err = s.StartEmbeddedGoalContinuation(ctx, b, keys[0])
	require.ErrorContains(t, err, "retention-expired")
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, keys[0], EmbeddedGoalEvaluationNotDone, "")
	require.ErrorContains(t, err, "retention-expired")
}

func TestWorkflowRetentionImportedExpiredCannotRevive(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	var admitted int64
	require.NoError(t, s.db.QueryRow(`SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND seq=?`, b.AgentID, k.UserSeq).Scan(&admitted))
	_, err := s.db.Exec(`UPDATE embedded_sessions SET evidence_expired_through_ms=? WHERE agent_id=?`, admitted, b.AgentID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	target := redBind(t, s, b.AgentID, "new-target", 2, "new-generation", "root")
	s.clock = deterministic.NewFixedClock(time.UnixMilli(admitted).Add(-24 * time.Hour))
	entry, err := s.AppendImportedEmbeddedEntry(ctx, target, b, k.UserSeq, k.UserDigest)
	require.NoError(t, err)
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, target, EmbeddedGoalSnoozeTurn{UserSeq: entry.Sequence, UserDigest: entry.Digest, GoalRevision: 1}, "revive", []byte(`{"duration_ms":1000}`))
	require.ErrorContains(t, err, "retention-expired")
}

func TestWorkflowRetentionSnoozeGraphAutomaticStartup(t *testing.T) {
	s, path, b, turn, clock := snoozeFixture(t)
	ctx := context.Background()
	_, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "old-a", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "old-b", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, clock.Advance(30*24*time.Hour))
	reopened, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer reopened.Close()
	require.Zero(t, embeddedRedCount(t, reopened, "embedded_goal_snooze_calls"))
	require.Zero(t, embeddedRedCount(t, reopened, "embedded_goal_snooze_preferences"))
	require.Equal(t, 1, embeddedRedCount(t, reopened, "embedded_entries"))
	_, _, err = reopened.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "old-b", []byte(`{"duration_ms":1000}`))
	require.ErrorContains(t, err, "retention-expired")
	var n int
	require.NoError(t, reopened.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n))
	require.Zero(t, n)
}

func TestWorkflowRetentionPhase121ImportWithoutExecutionSchema(t *testing.T) {
	ctx := context.Background()
	migrations := DefaultMigrations()
	tip := migrationIndex(t, migrations, "121_workflow_evidence_retention")
	s, err := Open(ctx, t.TempDir()+"/phase121.db", Options{Migrations: migrations[:tip+1]})
	require.NoError(t, err)
	defer s.Close()
	b := goalBind(t, s, "phase121-import", "session", 1, "generation", "root")
	source, err := s.AppendEmbeddedEntry(ctx, b, "user", []byte(`{"role":"user","content":"saved"}`))
	require.NoError(t, err)
	copied, err := s.AppendImportedEmbeddedEntry(ctx, b, b, source.Sequence, source.Digest)
	require.NoError(t, err)
	require.Equal(t, source.Body, copied.Body)
	var tables int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('embedded_sessions') WHERE name='execution_json'`).Scan(&tables))
	require.Zero(t, tables, "phase121 import must not require migration122")
}
