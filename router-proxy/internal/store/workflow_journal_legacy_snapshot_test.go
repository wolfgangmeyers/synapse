package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// WJ-07/WJ-11: a legacy workflow-only binding keeps its snapshot without inventing a visit.
func TestWorkflowJournalLegacyWorkflowOnlySnapshot(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "Legacy workflow"})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET active_workflow_id=?, active_workflow_state_id=NULL WHERE agent_id=?`, wf.ID, agent.AgentID)
	require.NoError(t, err)
	require.NoError(t, s.migrateWorkflowJournalTx(ctx))
	var id, name, state, visit, status string
	var synthetic, incomplete, events int
	err = s.db.QueryRowContext(ctx, `SELECT workflow_id,workflow_name,initial_state_id,current_visit_id,status,synthetic,incomplete,(SELECT count(*) FROM workflow_journal_events WHERE run_id=workflow_journal_runs.id) FROM workflow_journal_runs WHERE agent_id=?`, agent.AgentID).Scan(&id, &name, &state, &visit, &status, &synthetic, &incomplete, &events)
	require.NoError(t, err)
	require.Equal(t, wf.ID, id)
	require.Equal(t, wf.Name, name)
	require.Empty(t, state)
	require.Empty(t, visit)
	require.Equal(t, "active", status)
	require.Equal(t, 1, synthetic)
	require.Equal(t, 1, incomplete)
	require.Zero(t, events)
}
