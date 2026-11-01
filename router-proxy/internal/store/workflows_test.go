package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Initially empty
	wfs, err := s.ListWorkflows(ctx)
	require.NoError(t, err)
	require.Empty(t, wfs)

	// 2. Create first workflow
	req1 := WorkflowRequest{
		Name:             "code-review-flow",
		Description:      "A workflow for code reviews",
		SnoozeDurationMS: 300000,
	}
	wf1, err := s.CreateWorkflow(ctx, req1)
	require.NoError(t, err)
	require.NotEmpty(t, wf1.ID)
	require.Equal(t, req1.Name, wf1.Name)
	require.Equal(t, req1.Description, wf1.Description)
	require.Equal(t, 300000, wf1.SnoozeDurationMS)
	require.Nil(t, wf1.EntryStateID)
	require.Equal(t, 0, wf1.StateCount)
	require.NotEmpty(t, wf1.CreatedAt)
	require.NotEmpty(t, wf1.UpdatedAt)

	// 3. Name conflict
	_, err = s.CreateWorkflow(ctx, req1)
	require.Error(t, err)
	var nameConflict *WorkflowNameConflictError
	require.ErrorAs(t, err, &nameConflict)

	// 4. Create second workflow (with 0 snooze duration to test default 600000)
	req2 := WorkflowRequest{
		Name:        "bug-fix-flow",
		Description: "A workflow for bug fixes",
	}
	wf2, err := s.CreateWorkflow(ctx, req2)
	require.NoError(t, err)
	require.NotEqual(t, wf1.ID, wf2.ID)
	require.Equal(t, 600000, wf2.SnoozeDurationMS)

	// 5. List workflows (ordered by name: bug-fix-flow, code-review-flow)
	wfs, err = s.ListWorkflows(ctx)
	require.NoError(t, err)
	require.Len(t, wfs, 2)
	require.Equal(t, "bug-fix-flow", wfs[0].Name)
	require.Equal(t, "code-review-flow", wfs[1].Name)

	// 6. Get workflow
	fetched, err := s.GetWorkflow(ctx, wf1.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	require.Equal(t, wf1.ID, fetched.ID)
	require.Equal(t, wf1.Name, fetched.Name)
	require.Equal(t, 300000, fetched.SnoozeDurationMS)
	require.Equal(t, 0, fetched.StateCount)

	// Get non-existent
	missing, err := s.GetWorkflow(ctx, "non-existent")
	require.NoError(t, err)
	require.Nil(t, missing)

	// 7. Update workflow
	updateReq := WorkflowRequest{
		Name:             "code-review-flow-v2",
		Description:      "Updated code review workflow",
		SnoozeDurationMS: 120000,
	}
	updated, err := s.UpdateWorkflow(ctx, wf1.ID, updateReq)
	require.NoError(t, err)
	require.Equal(t, updateReq.Name, updated.Name)
	require.Equal(t, updateReq.Description, updated.Description)
	require.Equal(t, 120000, updated.SnoozeDurationMS)

	// Conflict update
	conflictReq := WorkflowRequest{
		Name: "bug-fix-flow",
	}
	_, err = s.UpdateWorkflow(ctx, wf1.ID, conflictReq)
	require.Error(t, err)
	require.ErrorAs(t, err, &nameConflict)

	// Update non-existent
	_, err = s.UpdateWorkflow(ctx, "non-existent", updateReq)
	require.Error(t, err)
	var notFound *WorkflowNotFoundError
	require.ErrorAs(t, err, &notFound)

	// 8. Delete workflow
	deleted, err := s.DeleteWorkflow(ctx, wf2.ID)
	require.NoError(t, err)
	require.True(t, deleted)

	fetched2, err := s.GetWorkflow(ctx, wf2.ID)
	require.NoError(t, err)
	require.Nil(t, fetched2)

	deletedAgain, err := s.DeleteWorkflow(ctx, wf2.ID)
	require.NoError(t, err)
	require.False(t, deletedAgain)
}

func TestWorkflowWithStatesAndPositions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Create workflow
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{
		Name:        "dev-flow",
		Description: "Standard dev flow",
	})
	require.NoError(t, err)

	// Create states associated with workflow
	x1, y1 := 100.0, 150.0
	state1, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID:  &wf.ID,
		Name:        "planning",
		Description: "Planning state",
		UIPosX:      &x1,
		UIPosY:      &y1,
	})
	require.NoError(t, err)
	require.NotNil(t, state1.WorkflowID)
	require.Equal(t, wf.ID, *state1.WorkflowID)
	require.Equal(t, 100.0, state1.UIPosX)
	require.Equal(t, 150.0, state1.UIPosY)

	x2, y2 := 300.0, 350.0
	state2, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID:  &wf.ID,
		Name:        "coding",
		Description: "Coding state",
		UIPosX:      &x2,
		UIPosY:      &y2,
		Transitions: []WorkflowTransitionConfig{
			{
				TargetStateID: state1.ID,
				Description:   "Back to planning",
			},
		},
	})
	require.NoError(t, err)

	// Verify StateCount in GetWorkflow and ListWorkflows
	fetchedWf, err := s.GetWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.Equal(t, 2, fetchedWf.StateCount)

	// Set entry state on workflow
	updatedWf, err := s.UpdateWorkflow(ctx, wf.ID, WorkflowRequest{
		Name:         wf.Name,
		Description:  wf.Description,
		EntryStateID: &state1.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, updatedWf.EntryStateID)
	require.Equal(t, state1.ID, *updatedWf.EntryStateID)

	// List states by workflow
	states, err := s.ListWorkflowStatesByWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.Len(t, states, 2)
	require.Equal(t, "coding", states[0].Name)
	require.Equal(t, "planning", states[1].Name)

	// Update positions
	newPositions := []WorkflowStatePosition{
		{
			StateID: state1.ID,
			UIPosX:  120.5,
			UIPosY:  160.5,
		},
		{
			StateID: state2.ID,
			UIPosX:  320.5,
			UIPosY:  360.5,
		},
	}
	err = s.UpdateWorkflowStatePositions(ctx, wf.ID, newPositions)
	require.NoError(t, err)

	fetchedState1, err := s.GetWorkflowState(ctx, state1.ID)
	require.NoError(t, err)
	require.Equal(t, 120.5, fetchedState1.UIPosX)
	require.Equal(t, 160.5, fetchedState1.UIPosY)

	fetchedState2, err := s.GetWorkflowState(ctx, state2.ID)
	require.NoError(t, err)
	require.Equal(t, 320.5, fetchedState2.UIPosX)
	require.Equal(t, 360.5, fetchedState2.UIPosY)

	// Update position with non-existent workflow
	err = s.UpdateWorkflowStatePositions(ctx, "non-existent-wf", newPositions)
	require.Error(t, err)
	var wfNotFound *WorkflowNotFoundError
	require.ErrorAs(t, err, &wfNotFound)

	// Update position with non-matching state
	err = s.UpdateWorkflowStatePositions(ctx, wf.ID, []WorkflowStatePosition{
		{
			StateID: "00000000-0000-4000-8000-000000000000",
			UIPosX:  0,
			UIPosY:  0,
		},
	})
	require.Error(t, err)
	var stateNotFound *WorkflowStateNotFoundError
	require.ErrorAs(t, err, &stateNotFound)

	// Delete workflow cascades/deletes states
	deleted, err := s.DeleteWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.True(t, deleted)

	// Check states were deleted
	afterStates, err := s.ListWorkflowStatesByWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.Empty(t, afterStates)

	missingState1, err := s.GetWorkflowState(ctx, state1.ID)
	require.NoError(t, err)
	require.Nil(t, missingState1)
}

func TestWorkflowDuplicate(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Create workflow
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{
		Name:        "original-wf",
		Description: "Original workflow description",
	})
	require.NoError(t, err)

	// Create 2 states with positions and transitions
	x1, y1 := 50.0, 75.0
	state1, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID:   &wf.ID,
		Name:         "stage-one",
		Description:  "First stage",
		EntryText:    "Entering stage one",
		AllowedTools: []string{"read", "edit"},
		UIPosX:       &x1,
		UIPosY:       &y1,
	})
	require.NoError(t, err)

	x2, y2 := 200.0, 250.0
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID:  &wf.ID,
		Name:        "stage-two",
		Description: "Second stage",
		UIPosX:      &x2,
		UIPosY:      &y2,
		Transitions: []WorkflowTransitionConfig{
			{
				TargetStateID: state1.ID,
				Description:   "Go back to stage one",
			},
		},
	})
	require.NoError(t, err)

	// Set entry state on original workflow
	_, err = s.UpdateWorkflow(ctx, wf.ID, WorkflowRequest{
		Name:         wf.Name,
		Description:  wf.Description,
		EntryStateID: &state1.ID,
	})
	require.NoError(t, err)

	// Duplicate workflow
	dup, err := s.DuplicateWorkflow(ctx, wf.ID, "duplicated-wf")
	require.NoError(t, err)
	require.NotNil(t, dup)
	require.NotEqual(t, wf.ID, dup.ID)
	require.Equal(t, "duplicated-wf", dup.Name)
	require.Equal(t, wf.Description, dup.Description)
	require.Equal(t, 2, dup.StateCount)
	require.NotNil(t, dup.EntryStateID)
	require.NotEqual(t, state1.ID, *dup.EntryStateID) // Must be remapped to the new state ID

	// Verify child states of duplicated workflow
	dupStates, err := s.ListWorkflowStatesByWorkflow(ctx, dup.ID)
	require.NoError(t, err)
	require.Len(t, dupStates, 2)

	// Find cloned state1 and cloned state2
	var clonedState1, clonedState2 *WorkflowState
	for i := range dupStates {
		st := &dupStates[i]
		if st.ID == *dup.EntryStateID {
			clonedState1 = st
		} else {
			clonedState2 = st
		}
	}
	require.NotNil(t, clonedState1)
	require.NotNil(t, clonedState2)

	// Check UI Pos offset by +20
	require.Equal(t, 50.0+20.0, clonedState1.UIPosX)
	require.Equal(t, 75.0+20.0, clonedState1.UIPosY)
	require.Equal(t, 200.0+20.0, clonedState2.UIPosX)
	require.Equal(t, 250.0+20.0, clonedState2.UIPosY)

	// Check copied entry text and allowed tools
	require.Equal(t, "Entering stage one", clonedState1.EntryText)
	require.Equal(t, []string{"read", "edit"}, clonedState1.AllowedTools)

	// Check remapped transitions in cloned state 2
	require.Len(t, clonedState2.Transitions, 1)
	require.Equal(t, clonedState1.ID, clonedState2.Transitions[0].TargetStateID)
	require.Equal(t, "Go back to stage one", clonedState2.Transitions[0].Description)

	// Duplicate with name conflict
	_, err = s.DuplicateWorkflow(ctx, wf.ID, "duplicated-wf")
	require.Error(t, err)
	var nameConflict *WorkflowNameConflictError
	require.ErrorAs(t, err, &nameConflict)

	// Verify states in duplicated workflow retain their original names (scoped uniqueness)
	require.Equal(t, "stage-one", clonedState1.Name)
	require.Equal(t, "stage-two", clonedState2.Name)

	// Duplicate non-existent workflow

	_, err = s.DuplicateWorkflow(ctx, "non-existent", "another-wf")
	require.Error(t, err)
	var notFound *WorkflowNotFoundError
	require.ErrorAs(t, err, &notFound)
}
