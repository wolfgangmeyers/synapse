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
