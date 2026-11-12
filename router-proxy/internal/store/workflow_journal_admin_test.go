package store

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestJournalPartialBindingAdministrativeLifecycle(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Admin workflow"})
	require.NoError(t, err)
	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Admin state", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, &st.ID, nil))
	var run string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent.AgentID).Scan(&run))
	deleted, err := s.DeleteWorkflowState(ctx, st.ID)
	require.NoError(t, err)
	require.True(t, deleted)
	var status, visit string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT status,current_visit_id FROM workflow_journal_runs WHERE id=?`, run).Scan(&status, &visit))
	require.Equal(t, "active", status)
	require.Empty(t, visit)
	var binding string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT COALESCE(active_workflow_id,'') FROM managed_agents WHERE agent_id=?`, agent.AgentID).Scan(&binding))
	require.Equal(t, wf.ID, binding)
	replacement, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Replacement", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowState(ctx, agent.AgentID, &replacement.ID))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT status,current_visit_id FROM workflow_journal_runs WHERE id=?`, run).Scan(&status, &visit))
	require.Equal(t, "active", status)
	require.NotEmpty(t, visit)
	deleted, err = s.DeleteWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.True(t, deleted)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT status FROM workflow_journal_runs WHERE id=?`, run).Scan(&status))
	require.Equal(t, "terminal", status)
}

func TestJournalNilStateWithWorkflowDoesNotActivate(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Only workflow"})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, nil, nil))
	var runs int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE agent_id=?`, agent.AgentID).Scan(&runs))
	require.Zero(t, runs)
	var workflow, state *string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT active_workflow_id,active_workflow_state_id FROM managed_agents WHERE agent_id=?`, agent.AgentID).Scan(&workflow, &state))
	require.Nil(t, workflow)
	require.Nil(t, state)
}

func TestJournalDeleteWorkflowPreservesCrossWorkflowVisit(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	a, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Binding workflow"})
	require.NoError(t, err)
	b, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "State workflow"})
	require.NoError(t, err)
	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Cross state", WorkflowID: &b.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &a.ID, &st.ID, nil))
	var run, visit string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT id,current_visit_id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent.AgentID).Scan(&run, &visit))
	deleted, err := s.DeleteWorkflow(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, deleted)
	var status, after string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT status,current_visit_id FROM workflow_journal_runs WHERE id=?`, run).Scan(&status, &after))
	require.Equal(t, "active", status)
	require.Equal(t, visit, after)
	var kind string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT kind FROM workflow_journal_events WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, run).Scan(&kind))
	require.Equal(t, "binding_changed", kind)
	var snapshotWorkflow, snapshotName, snapshotState, snapshotStateName string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT current_workflow_id,current_workflow_name,current_state_id,current_state_name FROM workflow_journal_runs WHERE id=?`, run).Scan(&snapshotWorkflow, &snapshotName, &snapshotState, &snapshotStateName))
	require.Equal(t, b.ID, snapshotWorkflow, "workflow-only FK clearing preserves the actual surviving state identity")
	require.Equal(t, "State workflow", snapshotName)
	require.Equal(t, st.ID, snapshotState)
	require.Equal(t, "Cross state", snapshotStateName)
	var visits int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=? AND kind IN ('start','visit','admin_visit')`, run).Scan(&visits))
	require.Equal(t, 1, visits, "clearing only the workflow binding must not append a visit")
}

func TestJournalStateMoveAndRename(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	a, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Move A"})
	require.NoError(t, err)
	b, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Move B"})
	require.NoError(t, err)
	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Move state", WorkflowID: &a.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &a.ID, &st.ID, nil))
	var run string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent.AgentID).Scan(&run))
	_, err = s.UpdateWorkflowState(ctx, st.ID, WorkflowStateRequest{Name: "Renamed", WorkflowID: &a.ID})
	require.NoError(t, err)
	_, err = s.UpdateWorkflowState(ctx, st.ID, WorkflowStateRequest{Name: "Renamed", WorkflowID: &b.ID})
	require.NoError(t, err)
	rows, err := s.db.QueryContext(ctx, `SELECT e.workflow_id,e.workflow_name,e.state_name FROM workflow_journal_events e JOIN workflow_journal_runs r ON r.id=e.run_id WHERE r.agent_id=? ORDER BY e.sequence`, agent.AgentID)
	require.NoError(t, err)
	defer rows.Close()
	var ids, workflowNames, names []string
	for rows.Next() {
		var id, workflowName, name string
		require.NoError(t, rows.Scan(&id, &workflowName, &name))
		ids = append(ids, id)
		workflowNames = append(workflowNames, workflowName)
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{a.ID, b.ID}, ids)
	require.Equal(t, []string{"Move A", "Move B"}, workflowNames)
	require.Equal(t, []string{"Move state", "Renamed"}, names)
	var snapshotID, snapshotName, snapshotState string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT current_workflow_id,current_workflow_name,current_state_name FROM workflow_journal_runs WHERE agent_id=?`, agent.AgentID).Scan(&snapshotID, &snapshotName, &snapshotState))
	require.Equal(t, b.ID, snapshotID)
	require.Equal(t, "Move B", snapshotName)
	require.Equal(t, "Renamed", snapshotState)
	var binding string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT active_workflow_id FROM managed_agents WHERE agent_id=?`, agent.AgentID).Scan(&binding))
	require.Equal(t, a.ID, binding)

	// Moving to an unassigned state must snapshot the empty workflow ID rather
	// than attempt to resolve a workflow name for the empty destination ID.
	emptyWorkflowID := ""
	_, err = s.UpdateWorkflowState(ctx, st.ID, WorkflowStateRequest{Name: "Unassigned", WorkflowID: &emptyWorkflowID})
	require.NoError(t, err)
	var movedWorkflow any
	var movedName string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT workflow_id,workflow_name FROM workflow_journal_events WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, run).Scan(&movedWorkflow, &movedName))
	require.Equal(t, "", movedWorkflow)
	require.Empty(t, movedName)
	var movedStateName string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT state_name FROM workflow_journal_events WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, run).Scan(&movedStateName))
	require.Equal(t, "Unassigned", movedStateName, "new visit records the destination state name even without a workflow")
	var currentWorkflow any
	var currentName string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT current_workflow_id,current_workflow_name FROM workflow_journal_runs WHERE id=?`, run).Scan(&currentWorkflow, &currentName))
	require.Equal(t, "", currentWorkflow)
	require.Empty(t, currentName)
	var currentStateName string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT current_state_name FROM workflow_journal_runs WHERE id=?`, run).Scan(&currentStateName))
	require.Equal(t, "Unassigned", currentStateName)
	var runAfter string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent.AgentID).Scan(&runAfter))
	require.Equal(t, run, runAfter, "state moves preserve journal identity")
	var eventCount int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=?`, run).Scan(&eventCount))
	require.Equal(t, 3, eventCount, "each move splits the visit without replacing the run")
}

func TestJournalStateMoveUsesCurrentVisitForPartialBinding(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	a, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Partial move A"})
	require.NoError(t, err)
	b, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Partial move B"})
	require.NoError(t, err)
	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Partial move state", WorkflowID: &a.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &a.ID, &st.ID, nil))
	var run string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent.AgentID).Scan(&run))
	// Simulate FK workflow-only clearing: state visit remains current although
	// the managed-agent binding no longer carries its state ID/workflow pair.
	_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET active_workflow_id=NULL WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	_, err = s.UpdateWorkflowState(ctx, st.ID, WorkflowStateRequest{Name: "Partial move state", WorkflowID: &b.ID})
	require.NoError(t, err)
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=? AND kind='admin_visit'`, run).Scan(&count))
	require.Equal(t, 1, count)
	var workflowID string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT current_workflow_id FROM workflow_journal_runs WHERE id=?`, run).Scan(&workflowID))
	require.Equal(t, b.ID, workflowID)
}

func TestJournalReplaceAllAndRejectedStateDeletion(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Replace"})
	require.NoError(t, err)
	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Replace state", WorkflowID: &wf.ID})
	require.NoError(t, err)
	other, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Referrer"})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET transitions_json=? WHERE id=?`, `[{"target_state_id":"`+st.ID+`"}]`, other.ID)
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, &st.ID, nil))
	deleted, err := s.DeleteWorkflowState(ctx, st.ID)
	require.Error(t, err)
	require.False(t, deleted)
	var run string
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent.AgentID).Scan(&run))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=?`, run).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, s.ReplaceAllWorkflows(ctx, nil))
	var status string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT status FROM workflow_journal_runs WHERE id=?`, run).Scan(&status))
	require.Equal(t, "terminal", status)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=?`, run).Scan(&count))
	require.Equal(t, 2, count)
}
