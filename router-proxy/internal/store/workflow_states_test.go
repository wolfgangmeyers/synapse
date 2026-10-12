package store

import (
	"context"
	"strings"
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

	// 8. Test Reminders on workflow state
	interval := int64(60)
	subj := "Plan Reminder"
	stateWithRemindersReq := WorkflowStateRequest{
		Name:        "reviewing",
		Description: "Reviewing state with reminders",
		EntryText:   "Review checklist",
		Reminders: []WorkflowReminderConfig{
			{
				ID:           "rem-1",
				Type:         WorkflowReminderOneShot,
				Subject:      &subj,
				Body:         "Remember to check tests",
				DelaySeconds: 120,
			},
			{
				ID:              "rem-2",
				Type:            WorkflowReminderRecurring,
				Body:            "Recurring check status",
				DelaySeconds:    30,
				IntervalSeconds: &interval,
			},
		},
	}
	stateWithRem, err := s.CreateWorkflowState(ctx, stateWithRemindersReq)
	require.NoError(t, err)
	require.Len(t, stateWithRem.Reminders, 2)
	require.Equal(t, WorkflowReminderOneShot, stateWithRem.Reminders[0].Type)
	require.Equal(t, &subj, stateWithRem.Reminders[0].Subject)
	require.Equal(t, int64(120), stateWithRem.Reminders[0].DelaySeconds)
	require.Equal(t, WorkflowReminderRecurring, stateWithRem.Reminders[1].Type)
	require.Equal(t, &interval, stateWithRem.Reminders[1].IntervalSeconds)

	// Fetch and verify reminders persisted
	fetchedWithRem, err := s.GetWorkflowState(ctx, stateWithRem.ID)
	require.NoError(t, err)
	require.NotNil(t, fetchedWithRem)
	require.Len(t, fetchedWithRem.Reminders, 2)
	require.Equal(t, "rem-1", fetchedWithRem.Reminders[0].ID)
	require.Equal(t, "rem-2", fetchedWithRem.Reminders[1].ID)

	// 9. Delete state
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

	// Only 2 states left (state2 and stateWithRem)
	states, err = s.ListWorkflowStates(ctx)
	require.NoError(t, err)
	require.Len(t, states, 2)
}

func TestWorkflowStateValidationReminders(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Invalid ID
	_, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "test-state",
		Reminders: []WorkflowReminderConfig{
			{ID: "", Type: WorkflowReminderOneShot, Body: "test", DelaySeconds: 10},
		},
	})
	require.Error(t, err)

	// Invalid Type
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "test-state",
		Reminders: []WorkflowReminderConfig{
			{ID: "rem1", Type: "unknown", Body: "test", DelaySeconds: 10},
		},
	})
	require.Error(t, err)

	// Empty Body
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "test-state",
		Reminders: []WorkflowReminderConfig{
			{ID: "rem1", Type: WorkflowReminderOneShot, Body: "", DelaySeconds: 10},
		},
	})
	require.Error(t, err)

	// Invalid DelaySeconds (< 1)
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "test-state",
		Reminders: []WorkflowReminderConfig{
			{ID: "rem1", Type: WorkflowReminderOneShot, Body: "test", DelaySeconds: 0},
		},
	})
	require.Error(t, err)

	// Recurring without interval
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "test-state",
		Reminders: []WorkflowReminderConfig{
			{ID: "rem1", Type: WorkflowReminderRecurring, Body: "test", DelaySeconds: 10},
		},
	})
	require.Error(t, err)

	// Recurring with interval < 1
	badInterval := int64(0)
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "test-state",
		Reminders: []WorkflowReminderConfig{
			{ID: "rem1", Type: WorkflowReminderRecurring, Body: "test", DelaySeconds: 10, IntervalSeconds: &badInterval},
		},
	})
	require.Error(t, err)
}

func TestManagedAgentWorkflowRemindersLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Register a managed agent
	agentID := "test-agent-rem"
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,?,'openai-codex','gpt-5','running','running','idle','embedded',?,?,?,1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		agentID, agentID, t.TempDir(), agentID+"-sess", int64(1), "gen-1", "hash-1")
	require.NoError(t, err)

	interval := int64(120)
	subj := "Lifecycle Rem Subj"
	wfState, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "state-with-reminders",
		Reminders: []WorkflowReminderConfig{
			{
				ID:           "one-shot",
				Type:         WorkflowReminderOneShot,
				Subject:      &subj,
				Body:         "One shot body",
				DelaySeconds: 60,
			},
			{
				ID:              "recurring",
				Type:            WorkflowReminderRecurring,
				Body:            "Recurring body",
				DelaySeconds:    30,
				IntervalSeconds: &interval,
			},
		},
	})
	require.NoError(t, err)

	// Spawn reminders
	err = s.SpawnWorkflowStateReminders(ctx, agentID, wfState)
	require.NoError(t, err)

	// Reminders should now exist in store
	reminders, err := s.Reminders(ctx)
	require.NoError(t, err)
	require.Len(t, reminders, 2)

	// Check reminder properties
	for _, rem := range reminders {
		require.Equal(t, agentID, rem.Target)
		if rem.IntervalMS != nil {
			require.Equal(t, int64(120000), *rem.IntervalMS)
			require.Equal(t, "Recurring body", rem.Body)
		} else {
			require.Equal(t, "Lifecycle Rem Subj", *rem.Subject)
			require.Equal(t, "One shot body", rem.Body)
		}
	}

	// Clean up
	err = s.CleanUpManagedAgentWorkflowReminders(ctx, agentID)
	require.NoError(t, err)

	// List reminders should now be empty (canceled)
	reminders, err = s.Reminders(ctx)
	require.NoError(t, err)
	require.Empty(t, reminders)
}

func TestWorkflowStateValidationGoal(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Goal exceeding 4000 bytes rejected
	tooLongGoal := strings.Repeat("g", 4001)
	_, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "test-goal-too-long",
		Goal: tooLongGoal,
	})
	require.Error(t, err)
	var valErr *WorkflowStateValidationError
	require.ErrorAs(t, err, &valErr)
	require.Equal(t, "goal", valErr.Field)

	// Goal of exactly 4000 bytes accepted
	validGoal := strings.Repeat("g", 4000)
	st, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "test-goal-max",
		Goal: validGoal,
	})
	require.NoError(t, err)
	require.Equal(t, validGoal, st.Goal)

	// Update exceeding 4000 bytes rejected
	_, err = s.UpdateWorkflowState(ctx, st.ID, WorkflowStateRequest{
		Name: "test-goal-max",
		Goal: tooLongGoal,
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &valErr)
	require.Equal(t, "goal", valErr.Field)
}

func TestWorkflowStateGoalCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Create with non-empty goal and verify persistence and retrieval
	goal1 := "Ship Phase 1 of Workflow State Goals"
	st1, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "implementing-goals",
		Goal: goal1,
	})
	require.NoError(t, err)
	require.Equal(t, goal1, st1.Goal)

	fetched, err := s.GetWorkflowState(ctx, st1.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	require.Equal(t, goal1, fetched.Goal)

	// 2. UpdateWorkflowState updating goal from non-empty to empty
	st1Updated, err := s.UpdateWorkflowState(ctx, st1.ID, WorkflowStateRequest{
		Name: "implementing-goals",
		Goal: "",
	})
	require.NoError(t, err)
	require.Equal(t, "", st1Updated.Goal)

	fetched, err = s.GetWorkflowState(ctx, st1.ID)
	require.NoError(t, err)
	require.Equal(t, "", fetched.Goal)

	// UpdateWorkflowState updating goal from empty to non-empty
	goal2 := "Refactored goal for state"
	st1Updated2, err := s.UpdateWorkflowState(ctx, st1.ID, WorkflowStateRequest{
		Name: "implementing-goals",
		Goal: goal2,
	})
	require.NoError(t, err)
	require.Equal(t, goal2, st1Updated2.Goal)

	fetched, err = s.GetWorkflowState(ctx, st1.ID)
	require.NoError(t, err)
	require.Equal(t, goal2, fetched.Goal)

	// 3. ListWorkflowStates returns Goal
	allStates, err := s.ListWorkflowStates(ctx)
	require.NoError(t, err)
	require.Len(t, allStates, 1)
	require.Equal(t, goal2, allStates[0].Goal)

	// 4. ListWorkflowStatesByWorkflow returns Goal
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{
		Name: "goal-workflow",
	})
	require.NoError(t, err)

	goal3 := "Constituent state goal"
	stInWf, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		WorkflowID: &wf.ID,
		Name:       "wf-state-goal",
		Goal:       goal3,
	})
	require.NoError(t, err)
	require.Equal(t, goal3, stInWf.Goal)

	wfStates, err := s.ListWorkflowStatesByWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	require.Len(t, wfStates, 1)
	require.Equal(t, goal3, wfStates[0].Goal)
}

func TestWorkflowStateAllowedTools(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Create with empty/omitted allowed_tools defaults to []string{}
	stateDefault, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name:        "default-tools",
		Description: "testing default tools",
	})
	require.NoError(t, err)
	require.NotNil(t, stateDefault)
	require.Equal(t, []string{}, stateDefault.AllowedTools)

	// Verify readback of default state
	fetchedDefault, err := s.GetWorkflowState(ctx, stateDefault.ID)
	require.NoError(t, err)
	require.NotNil(t, fetchedDefault)
	require.Equal(t, []string{}, fetchedDefault.AllowedTools)

	// 2. Create workflow state with allowed_tools: ["read", "bash"] -> verify readback
	stateCustom, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name:         "custom-tools",
		Description:  "testing custom tools",
		AllowedTools: []string{"read", "bash"},
	})
	require.NoError(t, err)
	require.NotNil(t, stateCustom)
	require.Equal(t, []string{"read", "bash"}, stateCustom.AllowedTools)

	fetchedCustom, err := s.GetWorkflowState(ctx, stateCustom.ID)
	require.NoError(t, err)
	require.NotNil(t, fetchedCustom)
	require.Equal(t, []string{"read", "bash"}, fetchedCustom.AllowedTools)

	// Verify in ListWorkflowStates
	allStates, err := s.ListWorkflowStates(ctx)
	require.NoError(t, err)
	require.Len(t, allStates, 2)
	for _, st := range allStates {
		if st.ID == stateCustom.ID {
			require.Equal(t, []string{"read", "bash"}, st.AllowedTools)
		} else if st.ID == stateDefault.ID {
			require.Equal(t, []string{}, st.AllowedTools)
		}
	}

	// 3. Validation rejects invalid tool name: ["invalid_tool"] -> returns error
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name:         "invalid-tools",
		AllowedTools: []string{"invalid_tool"},
	})
	require.Error(t, err)
	var valErr *WorkflowStateValidationError
	require.ErrorAs(t, err, &valErr)
	require.Equal(t, "allowed_tools", valErr.Field)

	// Test trimming and deduplication
	stateDedupe, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name:         "dedupe-tools",
		AllowedTools: []string{" read ", "bash", "read", " write "},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"read", "bash", "write"}, stateDedupe.AllowedTools)

	// 4. Test updating allowed_tools
	updated, err := s.UpdateWorkflowState(ctx, stateCustom.ID, WorkflowStateRequest{
		Name:         "custom-tools",
		Description:  "updated description",
		AllowedTools: []string{"mecha_mailbox_send", "scratchpad"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"mecha_mailbox_send", "scratchpad"}, updated.AllowedTools)

	fetchedUpdated, err := s.GetWorkflowState(ctx, stateCustom.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"mecha_mailbox_send", "scratchpad"}, fetchedUpdated.AllowedTools)

	// Reject invalid tool on update
	_, err = s.UpdateWorkflowState(ctx, stateCustom.ID, WorkflowStateRequest{
		Name:         "custom-tools",
		AllowedTools: []string{"not_a_valid_tool"},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &valErr)
	require.Equal(t, "allowed_tools", valErr.Field)
}

func TestWorkflowStateTransitionsAndExit(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Create target state A
	stateA, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name:        "State A",
		Description: "Target state A",
	})
	require.NoError(t, err)
	require.NotNil(t, stateA)
	require.Equal(t, []WorkflowTransitionConfig{}, stateA.Transitions)
	require.True(t, stateA.AllowExit) // default allow_exit is true

	// 2. Create state B with transition to State A and allow_exit = false (test empty description is allowed)
	allowExitFalse := false
	stateB, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name:        "State B",
		Description: "State B with transition",
		Transitions: []WorkflowTransitionConfig{
			{
				TargetStateID: stateA.ID,
				Description:   "",
			},
		},
		AllowExit: &allowExitFalse,
	})
	require.NoError(t, err)
	require.NotNil(t, stateB)
	require.False(t, stateB.AllowExit)
	require.Len(t, stateB.Transitions, 1)
	require.Equal(t, stateA.ID, stateB.Transitions[0].TargetStateID)
	require.Equal(t, "", stateB.Transitions[0].Description)

	// Fetch State B from store and verify
	fetchedB, err := s.GetWorkflowState(ctx, stateB.ID)
	require.NoError(t, err)
	require.NotNil(t, fetchedB)
	require.False(t, fetchedB.AllowExit)
	require.Len(t, fetchedB.Transitions, 1)
	require.Equal(t, stateA.ID, fetchedB.Transitions[0].TargetStateID)

	// 3. Validation: Self-transition rejected
	_, err = s.UpdateWorkflowState(ctx, stateA.ID, WorkflowStateRequest{
		Name:        "State A",
		Description: "Self-transition attempt",
		Transitions: []WorkflowTransitionConfig{
			{
				TargetStateID: stateA.ID,
				Description:   "Self loop",
			},
		},
	})
	require.Error(t, err)
	var valErr *WorkflowStateValidationError
	require.ErrorAs(t, err, &valErr)

	// Validation: Target state does not exist rejected
	nonExistentUUID := "12345678-1234-4123-8123-123456789abc"
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "State C",
		Transitions: []WorkflowTransitionConfig{
			{
				TargetStateID: nonExistentUUID,
				Description:   "Points to void",
			},
		},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &valErr)

	// Validation: Duplicate target rejected
	_, err = s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name: "State D",
		Transitions: []WorkflowTransitionConfig{
			{
				TargetStateID: stateA.ID,
				Description:   "First link",
			},
			{
				TargetStateID: stateA.ID,
				Description:   "Duplicate link",
			},
		},
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &valErr)

	// 4. In-use deletion protection: Attempting to delete stateA while stateB references it returns WorkflowStateInUseError
	_, err = s.DeleteWorkflowState(ctx, stateA.ID)
	require.Error(t, err)
	var inUseErr *WorkflowStateInUseError
	require.ErrorAs(t, err, &inUseErr)
	require.Equal(t, stateA.ID, inUseErr.ID)
	require.Equal(t, "State B", inUseErr.ReferrerState)

	// Verify stateA still exists
	stillA, err := s.GetWorkflowState(ctx, stateA.ID)
	require.NoError(t, err)
	require.NotNil(t, stillA)

	// 5. Update stateB to remove transitions, then delete stateA successfully
	_, err = s.UpdateWorkflowState(ctx, stateB.ID, WorkflowStateRequest{
		Name:        "State B",
		Transitions: []WorkflowTransitionConfig{},
	})
	require.NoError(t, err)

	deleted, err := s.DeleteWorkflowState(ctx, stateA.ID)
	require.NoError(t, err)
	require.True(t, deleted)

	vanishedA, err := s.GetWorkflowState(ctx, stateA.ID)
	require.NoError(t, err)
	require.Nil(t, vanishedA)
}

