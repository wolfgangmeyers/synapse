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
