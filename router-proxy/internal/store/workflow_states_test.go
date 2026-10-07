package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowStateCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Initially empty
	states, err := s.ListWorkflowStates(ctx)
	require.NoError(t, err)
	require.Empty(t, states)

	// 2. Create first state
	req1 := WorkflowStateRequest{
		Name:        "planning",
		Description: "State for drafting plans",
		EntryText:   "Agent has entered the planning state.",
	}
	state1, err := s.CreateWorkflowState(ctx, req1)
	require.NoError(t, err)
	require.NotEmpty(t, state1.ID)
	require.Equal(t, req1.Name, state1.Name)
	require.Equal(t, req1.Description, state1.Description)
	require.Equal(t, req1.EntryText, state1.EntryText)
	require.NotEmpty(t, state1.CreatedAt)
	require.NotEmpty(t, state1.UpdatedAt)

	// 3. Duplicate name conflict
	_, err = s.CreateWorkflowState(ctx, req1)
	require.Error(t, err)
	var nameConflict *WorkflowStateNameConflictError
	require.ErrorAs(t, err, &nameConflict)

	// 4. Create second state
	req2 := WorkflowStateRequest{
		Name:        "implementing",
		Description: "State for implementing changes",
		EntryText:   "Agent has entered the implementing state.",
	}
	state2, err := s.CreateWorkflowState(ctx, req2)
	require.NoError(t, err)
	require.NotEqual(t, state1.ID, state2.ID)

	// 5. List states (should be ordered by name: implementing, planning)
	states, err = s.ListWorkflowStates(ctx)
	require.NoError(t, err)
	require.Len(t, states, 2)
	require.Equal(t, "implementing", states[0].Name)
	require.Equal(t, "planning", states[1].Name)

	// 6. Get state by ID
	fetched, err := s.GetWorkflowState(ctx, state1.ID)
	require.NoError(t, err)
	require.Equal(t, state1.ID, fetched.ID)
	require.Equal(t, state1.Name, fetched.Name)

	// Non-existent state
	missing, err := s.GetWorkflowState(ctx, "non-existent")
	require.NoError(t, err)
	require.Nil(t, missing)

	// 7. Update state
	updateReq := WorkflowStateRequest{
		Name:        "planning-revised",
		Description: "Revised description",
		EntryText:   "Revised entry text",
	}
	updated, err := s.UpdateWorkflowState(ctx, state1.ID, updateReq)
	require.NoError(t, err)
	require.Equal(t, updateReq.Name, updated.Name)
	require.Equal(t, updateReq.Description, updated.Description)
	require.Equal(t, updateReq.EntryText, updated.EntryText)

	// Update to conflict with state2 name
	conflictReq := WorkflowStateRequest{
		Name:        "implementing",
		Description: "Should conflict",
		EntryText:   "Conflict text",
	}
	_, err = s.UpdateWorkflowState(ctx, state1.ID, conflictReq)
	require.Error(t, err)
	require.ErrorAs(t, err, &nameConflict)

	// 8. Delete state
	deleted, err := s.DeleteWorkflowState(ctx, state1.ID)
	require.NoError(t, err)
	require.True(t, deleted)

	// Verify deleted
	fetched, err = s.GetWorkflowState(ctx, state1.ID)
	require.NoError(t, err)
	require.Nil(t, fetched)

	// Delete already deleted
	deleted, err = s.DeleteWorkflowState(ctx, state1.ID)
	require.NoError(t, err)
	require.False(t, deleted)

	// Only 1 state left
	states, err = s.ListWorkflowStates(ctx)
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.Equal(t, state2.ID, states[0].ID)
}
