package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// IR-012: even an identical state and predecessor after real transitions is
// not the binding observed by the delayed attempt. Check durable effects too.
func TestWorkflowStaleSnapshotAfterTransitionABA(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "transition ABA"})
	require.NoError(t, err)
	yes := true
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "A", WorkflowID: &wf.ID, AllowImplicitReturn: &yes, Reminders: []WorkflowReminderConfig{{ID: "a", Type: WorkflowReminderOneShot, Body: "A", DelaySeconds: 3600}}, Monitors: []WorkflowMonitorConfig{{ID: "a-monitor", Name: "A monitor", QuestionInstructions: "check", NudgeTemplate: "nudge", QuestionType: "noul", ConditionOperator: "gte", ConditionThreshold: "0.5"}}})
	require.NoError(t, err)
	b, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "B", WorkflowID: &wf.ID, Transitions: []WorkflowTransitionConfig{{TargetStateID: a.ID}}, Reminders: []WorkflowReminderConfig{{ID: "b", Type: WorkflowReminderOneShot, Body: "B", DelaySeconds: 3600}}})
	require.NoError(t, err)
	err = s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, &b.ID, nil)
	require.NoError(t, err)
	move := func(from, to string) {
		v, e := s.WorkflowBindingVersion(ctx, agent.AgentID)
		require.NoError(t, e)
		_, e = s.AuthorizeWorkflowTransition(ctx, agent.AgentID, v.Generation, from, to)
		require.NoError(t, e)
	}
	move(b.ID, a.ID)
	stale, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, b.ID, stale.PredecessorID)
	move(a.ID, b.ID)
	move(b.ID, a.ID)
	before, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, stale.PredecessorID, before.PredecessorID)
	require.Greater(t, before.Generation, stale.Generation)
	require.NoError(t, s.SpawnWorkflowStateReminders(ctx, agent.AgentID, a))
	reminders, err := s.Reminders(ctx)
	require.NoError(t, err)
	require.Len(t, reminders, 1)
	require.Equal(t, "A", reminders[0].Body)
	monitors, err := s.ListManagedAgentMonitors(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Empty(t, monitors)
	_, err = s.AuthorizeWorkflowTransition(ctx, agent.AgentID, stale.Generation, a.ID, b.ID)
	require.ErrorContains(t, err, "workflow binding changed")
	after, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	current, err := s.GetManagedAgentActiveWorkflowState(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, a.ID, current.ID)
	afterReminders, err := s.Reminders(ctx)
	require.NoError(t, err)
	require.Equal(t, reminders, afterReminders)
	afterMonitors, err := s.ListManagedAgentMonitors(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, monitors, afterMonitors)
}

// IR-012: a captured binding does not capture permission. Edits ordered before
// authorization control the result, in both enable and disable directions.
func TestWorkflowSnapshotUsesCurrentReturnPermission(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled before commit", true: "enabled before commit"}[enabled], func(t *testing.T) {
			ctx := context.Background()
			s, agent := readinessSourceFixture(t)
			wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "permission"})
			require.NoError(t, err)
			initial := !enabled
			b, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "B", WorkflowID: &wf.ID, AllowImplicitReturn: &initial})
			require.NoError(t, err)
			a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "A", WorkflowID: &wf.ID, Transitions: []WorkflowTransitionConfig{{TargetStateID: b.ID}}})
			require.NoError(t, err)
			err = s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, &a.ID, nil)
			require.NoError(t, err)
			v, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
			require.NoError(t, err)
			_, err = s.AuthorizeWorkflowTransition(ctx, agent.AgentID, v.Generation, a.ID, b.ID)
			require.NoError(t, err)
			captured, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
			require.NoError(t, err)
			_, err = s.UpdateWorkflowState(ctx, b.ID, WorkflowStateRequest{Name: "B", WorkflowID: &wf.ID, AllowImplicitReturn: &enabled})
			require.NoError(t, err)
			_, err = s.AuthorizeWorkflowTransition(ctx, agent.AgentID, captured.Generation, b.ID, a.ID)
			if enabled {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "not permitted")
			}
			current, err := s.GetManagedAgentActiveWorkflowState(ctx, agent.AgentID)
			require.NoError(t, err)
			if enabled {
				require.Equal(t, a.ID, current.ID)
			} else {
				require.Equal(t, b.ID, current.ID)
				after, e := s.WorkflowBindingVersion(ctx, agent.AgentID)
				require.NoError(t, e)
				require.Equal(t, captured, after)
			}
		})
	}
}
