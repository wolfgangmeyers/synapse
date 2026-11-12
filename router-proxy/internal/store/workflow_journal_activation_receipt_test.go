package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowJournalActivationReceiptAtomicReplay(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "receipt state"})
	require.NoError(t, err)
	input := WorkflowJournalActivationInput{StateID: &state.ID, Variables: map[string]string{"x": "y"}}
	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER receipt_fail BEFORE INSERT ON workflow_journal_receipts BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	_, _, err = s.SetWorkflowJournalActivation(ctx, agent.AgentID, "activate-1", input)
	require.Error(t, err)
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE agent_id=?`, agent.AgentID).Scan(&count))
	require.Zero(t, count)
	_, err = s.db.ExecContext(ctx, `DROP TRIGGER receipt_fail`)
	require.NoError(t, err)
	first, replay, err := s.SetWorkflowJournalActivation(ctx, agent.AgentID, "activate-1", input)
	require.NoError(t, err)
	require.False(t, replay)
	require.NotEmpty(t, first.RunID)
	require.NotEmpty(t, first.EventID)
	ended, replay, err := s.SetWorkflowJournalActivation(ctx, agent.AgentID, "deactivate-1", WorkflowJournalActivationInput{})
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, first.RunID, ended.RunID)
	require.NotEmpty(t, ended.EventID)
	endedAgain, replay, err := s.SetWorkflowJournalActivation(ctx, agent.AgentID, "deactivate-1", WorkflowJournalActivationInput{})
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, ended, endedAgain)
	again, replay, err := s.SetWorkflowJournalActivation(ctx, agent.AgentID, "activate-1", input)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, again)
	input.Variables["x"] = "changed"
	_, _, err = s.SetWorkflowJournalActivation(ctx, agent.AgentID, "activate-1", input)
	require.ErrorIs(t, err, ErrWorkflowJournalOperationConflict)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE agent_id=?`, agent.AgentID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestActivationCallerIntentReplaysSnapshotAfterDefinitionChange(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "before"})
	require.NoError(t, err)
	intent := WorkflowActivationRequest{Action: "activate", StateID: state.ID}
	first, replay, err := s.ActivateWorkflow(ctx, agent.AgentID, "op", intent, intent)
	require.NoError(t, err)
	require.False(t, replay)
	_, _, err = s.ActivateWorkflow(ctx, agent.AgentID, "off", WorkflowActivationRequest{Action: "deactivate"}, WorkflowActivationRequest{Action: "deactivate"})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET name='after' WHERE id=?`, state.ID)
	require.NoError(t, err)
	again, replay, err := s.ActivateWorkflow(ctx, agent.AgentID, "op", intent, intent)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, again)
	require.Equal(t, "before", again.State.Name)
	changed := intent
	changed.StateID = "missing"
	_, _, err = s.ActivateWorkflow(ctx, agent.AgentID, "op", changed, changed)
	require.ErrorIs(t, err, ErrWorkflowJournalOperationConflict)
	current, err := s.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Empty(t, current.ActiveWorkflowStateID)
}

func TestActivationCallerIntentReceiptFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "atomic"})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_activation_receipt BEFORE INSERT ON workflow_journal_receipts BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	intent := WorkflowActivationRequest{Action: "activate", StateID: state.ID}
	_, _, err = s.ActivateWorkflow(ctx, agent.AgentID, "op", intent, intent)
	require.Error(t, err)
	current, err := s.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Empty(t, current.ActiveWorkflowStateID)
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE agent_id=?`, agent.AgentID).Scan(&count))
	require.Zero(t, count)
}

func TestActivationReceiptBeforeDeletedAgentAdmission(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "original"})
	require.NoError(t, err)
	on := WorkflowActivationRequest{Action: "activate", StateID: state.ID}
	off := WorkflowActivationRequest{Action: "deactivate"}
	first, replay, err := s.ActivateWorkflow(ctx, agent.AgentID, "on", on, on)
	require.NoError(t, err)
	require.False(t, replay)
	stopped, replay, err := s.ActivateWorkflow(ctx, agent.AgentID, "off", off, off)
	require.NoError(t, err)
	require.False(t, replay)
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_readiness_sources WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	again, replay, err := s.ActivateWorkflow(ctx, agent.AgentID, "on", on, on)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, again)
	again, replay, err = s.ActivateWorkflow(ctx, agent.AgentID, "off", off, off)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, stopped, again)
	_, _, err = s.ActivateWorkflow(ctx, agent.AgentID, "on", off, off)
	require.ErrorIs(t, err, ErrWorkflowJournalOperationConflict)
	for _, request := range []WorkflowActivationRequest{on, off, {Action: "activate", StateID: "missing"}} {
		_, _, err = s.ActivateWorkflow(ctx, agent.AgentID, "new", request, request)
		var missing *ManagedAgentNotFoundError
		require.ErrorAs(t, err, &missing)
	}
}

func TestWorkflowJournalActivationReceiptNilStateWithWorkflow(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "deactivation"})
	require.NoError(t, err)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "entry", WorkflowID: &wf.ID})
	require.NoError(t, err)
	first, _, err := s.SetWorkflowJournalActivation(ctx, agent.AgentID, "on", WorkflowJournalActivationInput{WorkflowID: &wf.ID, StateID: &state.ID})
	require.NoError(t, err)
	input := WorkflowJournalActivationInput{WorkflowID: &wf.ID}
	ended, replay, err := s.SetWorkflowJournalActivation(ctx, agent.AgentID, "off", input)
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, first.RunID, ended.RunID)
	require.NotEmpty(t, ended.EventID)
	var status, kind string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT r.status,e.kind FROM workflow_journal_runs r JOIN workflow_journal_events e ON e.run_id=r.id WHERE r.id=? AND e.id=?`, ended.RunID, ended.EventID).Scan(&status, &kind))
	require.Equal(t, "terminal", status)
	require.Equal(t, "end", kind)
	var workflow, current *string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT active_workflow_id,active_workflow_state_id FROM managed_agents WHERE agent_id=?`, agent.AgentID).Scan(&workflow, &current))
	require.Nil(t, workflow)
	require.Nil(t, current)
	again, replay, err := s.SetWorkflowJournalActivation(ctx, agent.AgentID, "off", input)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, ended, again)
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=?`, first.RunID).Scan(&count))
	require.Equal(t, 2, count)
}
