package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// WJ-01/WJ-09: stale or failed transitions cannot create journal events.
func TestWorkflowJournalBindingAndRollback(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Journal A"})
	require.NoError(t, err)
	b, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Journal B"})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET transitions_json=? WHERE id=?`, `[{"target_state_id":"`+b.ID+`"}]`, a.ID)
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, nil, &a.ID, nil))
	var run string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent.AgentID).Scan(&run))
	binding, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	_, err = s.AuthorizeWorkflowTransition(ctx, agent.AgentID, binding.Generation, a.ID, b.ID)
	require.NoError(t, err)
	_, err = s.AuthorizeWorkflowTransition(ctx, agent.AgentID, binding.Generation, a.ID, b.ID)
	require.Error(t, err)
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=?`, run).Scan(&count))
	require.Equal(t, 2, count)
	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER journal_test_failure BEFORE INSERT ON workflow_journal_events BEGIN SELECT RAISE(ABORT, 'injected journal failure'); END`)
	require.NoError(t, err)
	require.Error(t, s.SetManagedAgentActiveWorkflowState(ctx, agent.AgentID, &a.ID))
	current, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, b.ID, current.StateID)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=?`, run).Scan(&count))
	require.Equal(t, 2, count)
}

// WJ-05/WJ-08: reactivation closes the old run and agent deletion cascades.
func TestWorkflowJournalReplacementAndAgentDeletion(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Journal state"})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, nil, &a.ID, nil))
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, nil, &a.ID, nil))
	var active, terminal int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent.AgentID).Scan(&active))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE agent_id=? AND status='terminal'`, agent.AgentID).Scan(&terminal))
	require.Equal(t, 1, active)
	require.Equal(t, 1, terminal)
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_readiness_sources WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE agent_id=?`, agent.AgentID).Scan(&active))
	require.Zero(t, active)
}
