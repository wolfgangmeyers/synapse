package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetEmbeddedActivityStateTogglesAndBumpsProjection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	b := redBind(t, s, "activity-worker", "activity-session", 1, "activity-generation", "worker")

	initialVersion, err := s.bumpManagedProjectionVersion(ctx)
	require.NoError(t, err)

	// Transition from idle to active
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b, "active"))
	agent, err := s.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)

	versionAfterActive, err := s.bumpManagedProjectionVersion(ctx)
	require.NoError(t, err)
	// One bump from SetEmbeddedActivityState + one bump from our query
	require.Equal(t, initialVersion+2, versionAfterActive)

	// Setting to active again should be a no-op (no extra projection bump)
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b, "active"))
	versionNoChange, err := s.bumpManagedProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, versionAfterActive+1, versionNoChange)

	// Transition from active to idle
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b, "idle"))
	agent, err = s.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)

	// Invalid state returns error
	require.Error(t, s.SetEmbeddedActivityState(ctx, b, "unknown_state"))
}

func TestResetActiveEmbeddedAgentsStartupRecovery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	b1 := redBind(t, s, "active-agent-1", "session-1", 1, "gen-1", "worker")
	b2 := redBind(t, s, "active-agent-2", "session-2", 1, "gen-1", "root")

	// Set both to active
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b1, "active"))
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b2, "active"))

	a1, err := s.ManagedAgent(ctx, b1.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", a1.ActivityState)

	a2, err := s.ManagedAgent(ctx, b2.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", a2.ActivityState)

	// Insert dangling active history entry and completed entry
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at, presentation_json)
		VALUES ('active-agent-1', 'managed_tool', 'info', 'Running bash', '2026-10-08T14:40:34.372Z', '{"kind":"tool","tool_name":"bash","outcome":"active","summary":"Running"}')`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at, presentation_json)
		VALUES ('active-agent-1', 'managed_tool', 'info', 'Ran echo', '2026-10-08T14:40:30.000Z', '{"kind":"tool","tool_name":"bash","outcome":"completed","summary":"Succeeded"}')`)
	require.NoError(t, err)

	// Simulate router startup resetting active agents to idle
	require.NoError(t, s.ResetActiveEmbeddedAgents(ctx))

	a1, err = s.ManagedAgent(ctx, b1.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", a1.ActivityState)

	a2, err = s.ManagedAgent(ctx, b2.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", a2.ActivityState)

	var activeOutcome, activeSummary string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Running bash'`).Scan(&activeOutcome, &activeSummary))
	require.Equal(t, "failed", activeOutcome)
	require.Equal(t, "Interrupted by restart", activeSummary)

	var completedOutcome, completedSummary string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Ran echo'`).Scan(&completedOutcome, &completedSummary))
	require.Equal(t, "completed", completedOutcome)
	require.Equal(t, "Succeeded", completedSummary)
}

