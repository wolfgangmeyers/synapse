package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedAgentBlockedState(t *testing.T) {
	ctx := context.Background()
	s, _ := OpenTestStore(t)

	// Check that migration 113 is applied
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='113_managed_agent_blocked_state'`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "migration 113 must be applied")

	// Create test agent
	agentID := "test-agent-blocked"
	launchRecord := ManagedAgentRecord{
		AgentID:          agentID,
		Name:             agentID,
		Runtime:          "embedded",
		Cwd:              t.TempDir(),
		SelectedModel:    ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     testManifestHash,
	}
	op := ManagedOperation{
		OperationID:       "113e4567-e89b-42d3-a456-426614174001",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "113e4567-e89b-42d3-a456-426614174002",
		CanonicalBodyHash: "test-body-hash",
		ResponseStatus:    202,
		ResponseBody:      []byte(`{}`),
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, launchRecord, op))

	// Initial state: not blocked
	blocked, reason, blockedAt, err := s.GetAgentBlocked(ctx, agentID)
	require.NoError(t, err)
	require.False(t, blocked)
	require.Empty(t, reason)
	require.Empty(t, blockedAt)

	agent, err := s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.False(t, agent.Blocked)
	require.Empty(t, agent.BlockedReason)
	require.Empty(t, agent.BlockedAt)

	// Validation: cannot set blocked=true without reason
	err = s.SetAgentBlocked(ctx, agentID, true, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "reason is required")

	err = s.SetAgentBlocked(ctx, agentID, true, "   ")
	require.Error(t, err)
	require.Contains(t, err.Error(), "reason is required")

	// Set blocked
	blockedReason := "Missing API key for third-party integration"
	require.NoError(t, s.SetAgentBlocked(ctx, agentID, true, blockedReason))

	// Verify via GetAgentBlocked
	blocked, reason, blockedAt, err = s.GetAgentBlocked(ctx, agentID)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Equal(t, blockedReason, reason)
	require.NotEmpty(t, blockedAt)

	// Verify via ManagedAgent
	agent, err = s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.True(t, agent.Blocked)
	require.Equal(t, blockedReason, agent.BlockedReason)
	require.Equal(t, blockedAt, agent.BlockedAt)

	// Verify via ManagedAgents
	agents, err := s.ManagedAgents(ctx)
	require.NoError(t, err)
	var found *ManagedAgentRecord
	for _, a := range agents {
		if a.AgentID == agentID {
			found = a
			break
		}
	}
	require.NotNil(t, found)
	require.True(t, found.Blocked)
	require.Equal(t, blockedReason, found.BlockedReason)
	require.Equal(t, blockedAt, found.BlockedAt)

	// Verify via ManagedAgentsProjectionSnapshot
	snap, err := s.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	var snapFound *ManagedAgentRecord
	for _, a := range snap.Agents {
		if a.AgentID == agentID {
			snapFound = a
			break
		}
	}
	require.NotNil(t, snapFound)
	require.True(t, snapFound.Blocked)
	require.Equal(t, blockedReason, snapFound.BlockedReason)
	require.Equal(t, blockedAt, snapFound.BlockedAt)

	// Clear blocked
	require.NoError(t, s.SetAgentBlocked(ctx, agentID, false, ""))

	blocked, reason, blockedAt, err = s.GetAgentBlocked(ctx, agentID)
	require.NoError(t, err)
	require.False(t, blocked)
	require.Empty(t, reason)
	require.Empty(t, blockedAt)

	agent, err = s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.False(t, agent.Blocked)
	require.Empty(t, agent.BlockedReason)
	require.Empty(t, agent.BlockedAt)

	// Non-existent agent error handling
	err = s.SetAgentBlocked(ctx, "non-existent-agent", true, "some reason")
	require.Error(t, err)
	var notFoundErr *ManagedAgentNotFoundError
	require.ErrorAs(t, err, &notFoundErr)

	_, _, _, err = s.GetAgentBlocked(ctx, "non-existent-agent")
	require.Error(t, err)
	require.ErrorAs(t, err, &notFoundErr)
}
