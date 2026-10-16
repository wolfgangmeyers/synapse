package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func createTestManagedAgent(t *testing.T, ctx context.Context, s *Store, agentID string) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,?,'openai-codex','gpt-5','running','running','idle','embedded',?,?,?,1,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		agentID, agentID, t.TempDir(), agentID+"-sess", int64(1), "gen-1", "hash-1")
	require.NoError(t, err)
}

func TestManagedAgentMonitorsCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	agentID := "test-agent-monitors-1"
	createTestManagedAgent(t, ctx, s, agentID)

	// 1. Initially empty
	list, err := s.ListManagedAgentMonitors(ctx, agentID)
	require.NoError(t, err)
	require.Empty(t, list)

	// 2. Create monitor
	criteriaJSON := json.RawMessage(`{"stuck_threshold": 3}`)
	optionsJSON := json.RawMessage(`["continue", "pivot"]`)
	m := &ManagedAgentMonitor{
		ID:                    "mon-1",
		AgentID:               agentID,
		Name:                  "Stuck Loop Detector",
		Description:           "Detects if agent loops on the same tool call",
		Enabled:               true,
		IntervalSeconds:       45,
		NextEvalAt:            "2026-03-01T12:00:00Z",
		CooldownSeconds:       180,
		TrailingMessagesCount: 8,
		QuestionType:          "noul",
		QuestionInstructions:  "Determine if the agent is stuck in an unproductive loop.",
		QuestionCriteria:      criteriaJSON,
		QuestionOptions:       optionsJSON,
		ConditionOperator:     "gte",
		ConditionThreshold:    "0.85",
		NudgeTemplate:         "System 1 notice: You appear to be repeating actions. Consider alternative approach.",
		FallbackMode:          "skip",
	}

	err = s.CreateManagedAgentMonitor(ctx, m)
	require.NoError(t, err)

	// 3. Get monitor
	fetched, err := s.GetManagedAgentMonitor(ctx, agentID, "mon-1")
	require.NoError(t, err)
	require.NotNil(t, fetched)
	require.Equal(t, m.ID, fetched.ID)
	require.Equal(t, m.AgentID, fetched.AgentID)
	require.Equal(t, m.Name, fetched.Name)
	require.Equal(t, m.Description, fetched.Description)
	require.True(t, fetched.Enabled)
	require.Equal(t, 45, fetched.IntervalSeconds)
	require.Equal(t, 180, fetched.CooldownSeconds)
	require.Equal(t, 8, fetched.TrailingMessagesCount)
	require.Equal(t, "noul", fetched.QuestionType)
	require.Equal(t, "Determine if the agent is stuck in an unproductive loop.", fetched.QuestionInstructions)
	require.JSONEq(t, `{"stuck_threshold": 3}`, string(fetched.QuestionCriteria))
	require.JSONEq(t, `["continue", "pivot"]`, string(fetched.QuestionOptions))
	require.Equal(t, "gte", fetched.ConditionOperator)
	require.Equal(t, "0.85", fetched.ConditionThreshold)
	require.Equal(t, m.NudgeTemplate, fetched.NudgeTemplate)
	require.Equal(t, "skip", fetched.FallbackMode)
	require.Nil(t, fetched.WorkflowStateID)
	require.Nil(t, fetched.LastEvaluatedAt)
	require.Nil(t, fetched.LastFiredAt)
	require.Equal(t, 0, fetched.ConsecutiveAlerts)
	require.Equal(t, 0, fetched.MaxAlerts)
	require.NotEmpty(t, fetched.CreatedAt)
	require.NotEmpty(t, fetched.UpdatedAt)

	// 4. Update monitor
	fetched.Description = "Updated description"
	fetched.Enabled = false
	fetched.ConditionThreshold = "0.90"
	err = s.UpdateManagedAgentMonitor(ctx, fetched)
	require.NoError(t, err)

	updated, err := s.GetManagedAgentMonitor(ctx, agentID, "mon-1")
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, "Updated description", updated.Description)
	require.False(t, updated.Enabled)
	require.Equal(t, "0.90", updated.ConditionThreshold)

	// Update non-existent returns error
	ghost := &ManagedAgentMonitor{
		ID:      "ghost-mon",
		AgentID: agentID,
		Name:    "Ghost",
	}
	err = s.UpdateManagedAgentMonitor(ctx, ghost)
	require.ErrorIs(t, err, ErrManagedAgentMonitorNotFound)

	// 5. List monitors
	m2 := &ManagedAgentMonitor{
		ID:                   "mon-2",
		AgentID:              agentID,
		Name:                 "Off-topic detector",
		QuestionInstructions: "Is agent wandering off-topic?",
		NudgeTemplate:        "Please refocus on the main goal.",
	}
	err = s.CreateManagedAgentMonitor(ctx, m2)
	require.NoError(t, err)

	list, err = s.ListManagedAgentMonitors(ctx, agentID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "mon-1", list[0].ID)
	require.Equal(t, "mon-2", list[1].ID)

	// 6. Delete monitor
	err = s.DeleteManagedAgentMonitor(ctx, agentID, "mon-1")
	require.NoError(t, err)

	missing, err := s.GetManagedAgentMonitor(ctx, agentID, "mon-1")
	require.NoError(t, err)
	require.Nil(t, missing)

	// Delete again returns not found
	err = s.DeleteManagedAgentMonitor(ctx, agentID, "mon-1")
	require.ErrorIs(t, err, ErrManagedAgentMonitorNotFound)

	list, err = s.ListManagedAgentMonitors(ctx, agentID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "mon-2", list[0].ID)
}

func TestManagedAgentMonitorsDueAndEvaluation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	agentID := "test-agent-monitors-due"
	createTestManagedAgent(t, ctx, s, agentID)

	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	tPast := t0.Add(-10 * time.Minute)
	tFuture := t0.Add(10 * time.Minute)

	// Monitor 1: Enabled, past due
	m1 := &ManagedAgentMonitor{
		ID:                   "mon-due-1",
		AgentID:              agentID,
		Name:                 "Due Monitor 1",
		Enabled:              true,
		NextEvalAt:           formatTimestamp(tPast),
		QuestionInstructions: "Q1",
		NudgeTemplate:        "N1",
	}
	require.NoError(t, s.CreateManagedAgentMonitor(ctx, m1))

	// Monitor 2: Enabled, future due
	m2 := &ManagedAgentMonitor{
		ID:                   "mon-due-2",
		AgentID:              agentID,
		Name:                 "Future Monitor",
		Enabled:              true,
		NextEvalAt:           formatTimestamp(tFuture),
		QuestionInstructions: "Q2",
		NudgeTemplate:        "N2",
	}
	require.NoError(t, s.CreateManagedAgentMonitor(ctx, m2))

	// Monitor 3: Disabled, past due
	m3 := &ManagedAgentMonitor{
		ID:                   "mon-due-3",
		AgentID:              agentID,
		Name:                 "Disabled Past Monitor",
		Enabled:              false,
		NextEvalAt:           formatTimestamp(tPast),
		QuestionInstructions: "Q3",
		NudgeTemplate:        "N3",
	}
	require.NoError(t, s.CreateManagedAgentMonitor(ctx, m3))

	// Query due monitors at t0
	due, err := s.QueryDueManagedAgentMonitors(ctx, t0, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, "mon-due-1", due[0].ID)

	// Record evaluated (no alert)
	evalTime := t0
	nextEval := t0.Add(30 * time.Second)
	err = s.RecordManagedAgentMonitorEvaluated(ctx, "mon-due-1", evalTime, nextEval)
	require.NoError(t, err)

	m1Fetched, err := s.GetManagedAgentMonitor(ctx, agentID, "mon-due-1")
	require.NoError(t, err)
	require.NotNil(t, m1Fetched.LastEvaluatedAt)
	require.Equal(t, formatTimestamp(evalTime), *m1Fetched.LastEvaluatedAt)
	require.Equal(t, formatTimestamp(nextEval), m1Fetched.NextEvalAt)
	require.Equal(t, 0, m1Fetched.ConsecutiveAlerts)
	require.Nil(t, m1Fetched.LastFiredAt)

	// Now record fired
	fireTime := nextEval
	nextAfterFire := fireTime.Add(120 * time.Second)
	err = s.RecordManagedAgentMonitorFired(ctx, "mon-due-1", fireTime, nextAfterFire)
	require.NoError(t, err)

	m1Fetched, err = s.GetManagedAgentMonitor(ctx, agentID, "mon-due-1")
	require.NoError(t, err)
	require.NotNil(t, m1Fetched.LastEvaluatedAt)
	require.Equal(t, formatTimestamp(fireTime), *m1Fetched.LastEvaluatedAt)
	require.NotNil(t, m1Fetched.LastFiredAt)
	require.Equal(t, formatTimestamp(fireTime), *m1Fetched.LastFiredAt)
	require.Equal(t, formatTimestamp(nextAfterFire), m1Fetched.NextEvalAt)
	require.Equal(t, 1, m1Fetched.ConsecutiveAlerts)

	// Fire again -> consecutive_alerts should be 2
	fireTime2 := nextAfterFire
	nextAfterFire2 := fireTime2.Add(120 * time.Second)
	err = s.RecordManagedAgentMonitorFired(ctx, "mon-due-1", fireTime2, nextAfterFire2)
	require.NoError(t, err)

	m1Fetched, err = s.GetManagedAgentMonitor(ctx, agentID, "mon-due-1")
	require.NoError(t, err)
	require.Equal(t, 2, m1Fetched.ConsecutiveAlerts)

	// Evaluated again without firing -> consecutive_alerts resets to 0
	err = s.RecordManagedAgentMonitorEvaluated(ctx, "mon-due-1", nextAfterFire2, nextAfterFire2.Add(30*time.Second))
	require.NoError(t, err)

	m1Fetched, err = s.GetManagedAgentMonitor(ctx, agentID, "mon-due-1")
	require.NoError(t, err)
	require.Equal(t, 0, m1Fetched.ConsecutiveAlerts)
}

func TestManagedAgentMonitorsWorkflowCleanupAndCascade(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	agentID := "test-agent-monitors-cascade"
	createTestManagedAgent(t, ctx, s, agentID)

	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name:        "monitor-test-state",
		Description: "State for testing monitors",
	})
	require.NoError(t, err)

	// Create standalone monitor (workflow_state_id is nil)
	mStandalone := &ManagedAgentMonitor{
		ID:                   "mon-standalone",
		AgentID:              agentID,
		Name:                 "Standalone",
		QuestionInstructions: "Check overall progress",
		NudgeTemplate:        "Keep going",
	}
	require.NoError(t, s.CreateManagedAgentMonitor(ctx, mStandalone))

	// Create workflow-state-spawned monitor
	mStateSpawned := &ManagedAgentMonitor{
		ID:                   "mon-state-spawned",
		AgentID:              agentID,
		WorkflowStateID:      &state.ID,
		Name:                 "State Monitor",
		QuestionInstructions: "Check state specific condition",
		NudgeTemplate:        "Please complete state goals",
	}
	require.NoError(t, s.CreateManagedAgentMonitor(ctx, mStateSpawned))

	// Clean up workflow state monitors for agent
	err = s.CleanUpManagedAgentWorkflowMonitors(ctx, agentID)
	require.NoError(t, err)

	// Workflow monitor should be gone, standalone should remain
	ms, err := s.GetManagedAgentMonitor(ctx, agentID, "mon-state-spawned")
	require.NoError(t, err)
	require.Nil(t, ms)

	mStand, err := s.GetManagedAgentMonitor(ctx, agentID, "mon-standalone")
	require.NoError(t, err)
	require.NotNil(t, mStand)

	// Create another state-spawned monitor
	mStateSpawned2 := &ManagedAgentMonitor{
		ID:                   "mon-state-spawned-2",
		AgentID:              agentID,
		WorkflowStateID:      &state.ID,
		Name:                 "State Monitor 2",
		QuestionInstructions: "Check 2",
		NudgeTemplate:        "Nudge 2",
	}
	require.NoError(t, s.CreateManagedAgentMonitor(ctx, mStateSpawned2))

	// Delete workflow state -> monitor should cascade delete via foreign key
	deleted, err := s.DeleteWorkflowState(ctx, state.ID)
	require.NoError(t, err)
	require.True(t, deleted)

	ms2, err := s.GetManagedAgentMonitor(ctx, agentID, "mon-state-spawned-2")
	require.NoError(t, err)
	require.Nil(t, ms2)

	// Standalone monitor should still be there
	mStand, err = s.GetManagedAgentMonitor(ctx, agentID, "mon-standalone")
	require.NoError(t, err)
	require.NotNil(t, mStand)

	// Delete agent -> cascade deletes remaining monitors
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id = ?`, agentID)
	require.NoError(t, err)

	mStandAfterAgentDelete, err := s.GetManagedAgentMonitor(ctx, agentID, "mon-standalone")
	require.NoError(t, err)
	require.Nil(t, mStandAfterAgentDelete)
}
