package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// IR-010, IR-012: a binding replacement fences stale attempts, even if its
// visible state is unchanged. Ordinary transitions retain one predecessor.
func TestWorkflowPredecessorBindingFence(t *testing.T) {
	ctx := context.Background()
	s, fence := readinessSourceFixture(t)
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "A"})
	require.NoError(t, err)
	b, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "B", Transitions: []WorkflowTransitionConfig{{TargetStateID: a.ID}}})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET transitions_json=? WHERE id=?`, `[{"target_state_id":"`+b.ID+`"}]`, a.ID)
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowState(ctx, fence.AgentID, &a.ID))
	first, err := s.WorkflowBindingVersion(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Empty(t, first.PredecessorID)
	_, err = s.AuthorizeWorkflowTransition(ctx, fence.AgentID, first.Generation, a.ID, b.ID)
	require.NoError(t, err)
	second, err := s.WorkflowBindingVersion(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, a.ID, second.PredecessorID)
	_, err = s.AuthorizeWorkflowTransition(ctx, fence.AgentID, first.Generation, a.ID, b.ID)
	require.Error(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowState(ctx, fence.AgentID, &b.ID))
	third, err := s.WorkflowBindingVersion(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Empty(t, third.PredecessorID)
	require.Greater(t, third.Generation, second.Generation)
	_, err = s.AuthorizeWorkflowTransition(ctx, fence.AgentID, second.Generation, b.ID, a.ID)
	require.Error(t, err)
}

// IR-012: a failed write leaves both the state and predecessor intact.
func TestWorkflowPredecessorRollbackAndABA(t *testing.T) {
	ctx := context.Background()
	s, fence := readinessSourceFixture(t)
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "A"})
	require.NoError(t, err)
	b, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "B", Transitions: []WorkflowTransitionConfig{{TargetStateID: a.ID}}})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET transitions_json=? WHERE id=?`, `[{"target_state_id":"`+b.ID+`"}]`, a.ID)
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowState(ctx, fence.AgentID, &a.ID))
	original, err := s.WorkflowBindingVersion(ctx, fence.AgentID)
	require.NoError(t, err)
	_, err = s.AuthorizeWorkflowTransition(ctx, fence.AgentID, original.Generation, a.ID, b.ID)
	require.NoError(t, err)
	middle, err := s.WorkflowBindingVersion(ctx, fence.AgentID)
	require.NoError(t, err)
	_, err = s.AuthorizeWorkflowTransition(ctx, fence.AgentID, middle.Generation, b.ID, "missing")
	require.Error(t, err)
	unchanged, err := s.WorkflowBindingVersion(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, middle, unchanged)
	_, err = s.AuthorizeWorkflowTransition(ctx, fence.AgentID, middle.Generation, b.ID, a.ID)
	require.NoError(t, err)
	_, err = s.AuthorizeWorkflowTransition(ctx, fence.AgentID, original.Generation, a.ID, b.ID)
	require.Error(t, err)
	last, err := s.WorkflowBindingVersion(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, b.ID, last.PredecessorID)
}
