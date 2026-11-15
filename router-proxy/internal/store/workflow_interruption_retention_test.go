package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestWorkflowInterruptionRetention(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	now := s.clock.Now().UnixMilli()
	for i := 0; i < 102; i++ {
		_, err := s.db.Exec(`INSERT INTO workflow_interruption_markers(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,owner_token,execution_token,recorded_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, i+1, fmt.Sprint(i), 1, i+2, "digest", 1, i+1, now-int64(i))
		require.NoError(t, err)
	}
	markers, err := s.WorkflowInterruptionMarkers(ctx, b.AgentID)
	require.NoError(t, err)
	require.Len(t, markers, 100)
	_, err = s.db.Exec(`UPDATE workflow_interruption_markers SET recorded_at_ms=?`, now-int64(30*24*time.Hour/time.Millisecond))
	require.NoError(t, err)
	markers, err = s.WorkflowInterruptionMarkers(ctx, b.AgentID)
	require.NoError(t, err)
	require.Empty(t, markers)
}

func TestWorkflowInterruptionSweepPrunesExpired(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	_, err := s.db.Exec(`INSERT INTO workflow_interruption_markers(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,owner_token,execution_token,recorded_at_ms,workflow_id,workflow_state_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, 1, "u", 1, 2, "f", 1, 1, s.clock.Now().Add(-30*24*time.Hour).UnixMilli(), "wf", "state")
	require.NoError(t, err)
	_, err = s.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM workflow_interruption_markers`).Scan(&count))
	require.Zero(t, count)
}

func TestWorkflowInterruptionWitnessFollowsEvidenceRetention(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	markerWorkflow(t, s, b, "retained-witness")
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM workflow_continuation_start_identity`).Scan(&count))
	require.Equal(t, 1, count)
	next, err := s.AcquireEmbeddedOwner(ctx, b, "retention-successor", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = next
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	s.clock = deterministic.NewFixedClock(s.clock.Now().Add(31 * 24 * time.Hour))
	require.NoError(t, s.runImmediate(ctx, func() error { return s.pruneEmbeddedEvidenceTx(ctx) }))
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM workflow_continuation_start_identity`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_continuation_prompt_identity`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM workflow_interruption_markers`).Scan(&count))
	require.Zero(t, count)
}

func TestLegacyAmbiguityPrunedByEvidenceSweep(t *testing.T) {
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	_, err := s.db.Exec(`INSERT INTO legacy_continuation_ambiguity(agent_id,recorded_at_ms) VALUES(?,?)`, b.AgentID, s.clock.Now().Add(-30*24*time.Hour).UnixMilli())
	require.NoError(t, err)
	require.NoError(t, s.runImmediate(ctx, func() error { return s.pruneEmbeddedEvidenceTx(ctx) }))
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM legacy_continuation_ambiguity`).Scan(&n))
	require.Zero(t, n)
}
