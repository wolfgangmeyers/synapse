package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func markerWorkflow(t *testing.T, s *Store, b EmbeddedBinding, name string) (string, string) {
	t.Helper()
	ctx := context.Background()
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: name})
	require.NoError(t, err)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "working", WorkflowID: &wf.ID})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET active_workflow_id=?,active_workflow_state_id=? WHERE agent_id=?`, wf.ID, state.ID, b.AgentID)
	require.NoError(t, err)
	return wf.ID, state.ID
}

func TestWorkflowInterruptionMarkerTakeover(t *testing.T) {
	for _, settled := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted", true: "settled"}[settled], func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			receiptForEvaluation(t, s, b, &k)
			_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.NoError(t, err)
			wf, state := markerWorkflow(t, s, b, "marker-workflow")
			_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, started)
			execution, err := s.EnterEmbeddedSDKExecution(ctx, b, &k)
			require.NoError(t, err)
			if settled {
				require.NoError(t, s.ClearEmbeddedExecution(ctx, b, execution))
			}
			next, err := s.AcquireEmbeddedOwner(ctx, b, "replacement-marker-owner", time.Minute)
			require.NoError(t, err)
			require.Greater(t, next, b.OwnerToken)
			markers, err := s.WorkflowInterruptionMarkers(ctx, b.AgentID)
			require.NoError(t, err)
			if settled {
				require.Empty(t, markers)
				return
			}
			require.Len(t, markers, 1)
			require.Equal(t, k, markers[0].Key)
			require.Equal(t, wf, markers[0].WorkflowID)
			require.Equal(t, state, markers[0].WorkflowStateID)
			require.Equal(t, b.OwnerToken, markers[0].OwnerToken)
			b.OwnerToken = next
			token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
			require.NoError(t, err)
			require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
			markers, err = s.WorkflowInterruptionMarkers(ctx, b.AgentID)
			require.NoError(t, err)
			require.Len(t, markers, 1)
		})
	}
}

func TestWorkflowInterruptionMarkerFailureRollsBackFence(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	markerWorkflow(t, s, b, "rollback-workflow")
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	_, err = s.db.Exec(`CREATE TRIGGER fail_marker BEFORE INSERT ON workflow_interruption_markers BEGIN SELECT RAISE(ABORT,'marker fault'); END`)
	require.NoError(t, err)
	_, err = s.AcquireEmbeddedOwner(ctx, b, "failed-marker-owner", time.Minute)
	require.ErrorContains(t, err, "marker fault")
	var token int64
	require.NoError(t, s.db.QueryRow(`SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, b.AgentID).Scan(&token))
	require.Equal(t, b.OwnerToken, token)
}

func TestWorkflowInterruptionStartIdentity(t *testing.T) {
	for _, tc := range []struct {
		name            string
		workflowAtStart bool
		change          string
		want            bool
	}{
		{"ordinary goal", false, "", false}, {"goal becomes workflow", false, "activate", false},
		{"workflow exits", true, "exit", true}, {"workflow replaced", true, "replace", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			receiptForEvaluation(t, s, b, &k)
			_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.NoError(t, err)
			var wf, state string
			if tc.workflowAtStart {
				wf, state = markerWorkflow(t, s, b, "initial")
			}
			_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, started)
			switch tc.change {
			case "exit":
				require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, nil, nil, nil))
			case "activate", "replace":
				newWF, newState := markerWorkflow(t, s, b, "replacement")
				require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &newWF, &newState, nil))
			}
			next, err := s.AcquireEmbeddedOwner(ctx, b, "identity-successor", time.Minute)
			require.NoError(t, err)
			require.Error(t, s.ClearEmbeddedExecution(ctx, b, 1))
			_, _, err = s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.Error(t, err)
			markers, err := s.WorkflowInterruptionMarkers(ctx, b.AgentID)
			require.NoError(t, err)
			if !tc.want {
				require.Empty(t, markers)
				return
			}
			require.Len(t, markers, 1)
			require.Equal(t, wf, markers[0].WorkflowID)
			require.Equal(t, state, markers[0].WorkflowStateID)
			b.OwnerToken = next
			_, err = s.AcquireEmbeddedOwner(ctx, b, "third-owner", time.Minute)
			require.NoError(t, err)
			markers, err = s.WorkflowInterruptionMarkers(ctx, b.AgentID)
			require.NoError(t, err)
			require.Len(t, markers, 1)
		})
	}
}

func TestWorkflowInterruptionRestartAndMigration(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/router.db"
	s, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	b := goalBind(t, s, "marker-restart", "session", 1, "gen", "root")
	user := receiptProviderUser(t, s, b, "go")
	goalAPIPolicy(t, s, b, true, "finish")
	k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
	receiptForEvaluation(t, s, b, &k)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	wf, state := markerWorkflow(t, s, b, "restart")
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	next, err := s.AcquireEmbeddedOwner(ctx, b, "restarted-owner", time.Minute)
	require.NoError(t, err)
	markers, err := s.WorkflowInterruptionMarkers(ctx, b.AgentID)
	require.NoError(t, err)
	require.Len(t, markers, 1)
	require.Equal(t, wf, markers[0].WorkflowID)
	require.Equal(t, state, markers[0].WorkflowStateID)
	b.OwnerToken = next
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.ClearEmbeddedExecution(ctx, b, token))
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	markers, err = s.WorkflowInterruptionMarkers(ctx, b.AgentID)
	require.NoError(t, err)
	require.Len(t, markers, 1)
}

func TestWorkflowInterruptionMigrationDoesNotInventIdentity(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/legacy.db"
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, "136_workflow_interruption_markers")
	s, err := Open(ctx, path, Options{Migrations: migrations[:idx+1]})
	require.NoError(t, err)
	b := goalBind(t, s, "legacy-marker", "session", 1, "gen", "root")
	user := receiptProviderUser(t, s, b, "go")
	goalAPIPolicy(t, s, b, true, "finish")
	k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
	receiptForEvaluation(t, s, b, &k)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	markerWorkflow(t, s, b, "legacy-current")
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	_, err = s.db.Exec(`INSERT INTO workflow_interruption_markers(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,owner_token,execution_token,recorded_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, k.FinalSeq, k.FinalDigest, b.OwnerToken, 1, s.clock.Now().UnixMilli())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	_, err = s.AcquireEmbeddedOwner(ctx, b, "post-upgrade", time.Minute)
	require.NoError(t, err)
	markers, err := s.WorkflowInterruptionMarkers(ctx, b.AgentID)
	require.NoError(t, err)
	require.Empty(t, markers)
	legacyAt, err := s.LegacyContinuationAmbiguity(ctx, b.AgentID)
	require.NoError(t, err)
	require.Positive(t, legacyAt)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_continuation_prompt_identity`).Scan(&count))
	require.Zero(t, count)
}

func TestContinuationPromptIdentityFencesBindingAndOwner(t *testing.T) {
	for _, workflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "goal", true: "workflow"}[workflow], func(t *testing.T) {
			s, b, k := evaluationFixture(t)
			defer s.Close()
			ctx := context.Background()
			receiptForEvaluation(t, s, b, &k)
			_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
			require.NoError(t, err)
			_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
			require.NoError(t, err)
			var wf, state string
			if workflow {
				wf, state = markerWorkflow(t, s, b, "original")
			}
			_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
			require.NoError(t, err)
			require.True(t, started)
			before, err := s.EmbeddedContinuationPromptIdentity(ctx, b, k)
			require.NoError(t, err)
			require.Equal(t, workflow, before.Workflow)
			require.Equal(t, "finish", before.GoalText)
			if workflow {
				require.Equal(t, wf, before.WorkflowID)
				require.Equal(t, state, before.WorkflowStateID)
				require.Equal(t, "original", before.WorkflowName)
				require.Equal(t, "working", before.WorkflowStateName)
			}
			markerWorkflow(t, s, b, "replacement")
			_, err = s.db.Exec(`UPDATE managed_agent_policies SET goal_text='replacement goal' WHERE agent_id=?`, b.AgentID)
			require.NoError(t, err)
			if workflow {
				_, err = s.db.Exec(`UPDATE workflows SET name='renamed' WHERE id=?`, wf)
				require.NoError(t, err)
				_, err = s.db.Exec(`UPDATE workflow_states SET name='renamed-state' WHERE id=?`, state)
				require.NoError(t, err)
			}
			after, err := s.EmbeddedContinuationPromptIdentity(ctx, b, k)
			require.NoError(t, err)
			require.Equal(t, before, after)
			_, err = s.AcquireEmbeddedOwner(ctx, b, "stale-prompt", time.Minute)
			require.NoError(t, err)
			_, err = s.EmbeddedContinuationPromptIdentity(ctx, b, k)
			require.Error(t, err)
		})
	}
}

func TestContinuationPromptIdentityStartRollback(t *testing.T) {
	s, b, k := evaluationFixture(t)
	defer s.Close()
	ctx := context.Background()
	receiptForEvaluation(t, s, b, &k)
	_, _, err := s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER prompt_identity_fault BEFORE INSERT ON embedded_continuation_prompt_identity BEGIN SELECT RAISE(ABORT,'prompt identity fault'); END`)
	require.NoError(t, err)
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.ErrorContains(t, err, "prompt identity fault")
	require.False(t, started)
	var state string
	var execution sql.NullString
	require.NoError(t, s.db.QueryRow(`SELECT c.state,s.execution_json FROM embedded_goal_continuations c JOIN embedded_sessions s USING(agent_id,session_id,epoch,generation) WHERE c.agent_id=?`, b.AgentID).Scan(&state, &execution))
	require.Equal(t, "reserved", state)
	require.False(t, execution.Valid)
}

func TestLegacyAmbiguityMigrationExcludesClassifiedOrdinaryGoal(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/legacy.db"
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, "139_legacy_continuation_ambiguity")
	s, err := Open(ctx, path, Options{Migrations: migrations[:idx]})
	require.NoError(t, err)
	b := goalBind(t, s, "ordinary-classified", "session", 1, "gen", "root")
	user := receiptProviderUser(t, s, b, "go")
	goalAPIPolicy(t, s, b, true, "finish")
	k := EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}
	receiptForEvaluation(t, s, b, &k)
	_, _, err = s.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	_, _, err = s.CompleteEmbeddedGoalEvaluation(ctx, b, k, EmbeddedGoalEvaluationNotDone, "")
	require.NoError(t, err)
	_, started, err := s.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_continuation_prompt_identity WHERE agent_id=? AND workflow_id=''`, b.AgentID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	recorded, err := s.LegacyContinuationAmbiguity(ctx, b.AgentID)
	require.NoError(t, err)
	require.Zero(t, recorded)
}
