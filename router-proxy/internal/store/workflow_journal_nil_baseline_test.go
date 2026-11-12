package store

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWorkflowJournalNilStateClearsBaselineBinding(t *testing.T) {
	ctx := context.Background()
	for _, stateOnly := range []bool{true, false} {
		t.Run(map[bool]string{true: "state setter", false: "activation setter"}[stateOnly], func(t *testing.T) {
			s, a := readinessSourceFixture(t)
			wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "nil baseline"})
			require.NoError(t, err)
			st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "entry", WorkflowID: &wf.ID})
			require.NoError(t, err)
			require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, a.AgentID, &wf.ID, &st.ID, map[string]string{"key": "value"}))
			if stateOnly {
				err = s.SetManagedAgentActiveWorkflowState(ctx, a.AgentID, nil)
			} else {
				err = s.SetManagedAgentActiveWorkflowWithVariables(ctx, a.AgentID, &wf.ID, nil, map[string]string{"key": "value"})
			}
			require.NoError(t, err)
			var state, workflow, vars *string
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT active_workflow_state_id,active_workflow_id,workflow_variables_json FROM managed_agents WHERE agent_id=?`, a.AgentID).Scan(&state, &workflow, &vars))
			require.Nil(t, state)
			require.Nil(t, workflow)
			require.Nil(t, vars)
			var active int
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, a.AgentID).Scan(&active))
			require.Zero(t, active)
		})
	}
}
