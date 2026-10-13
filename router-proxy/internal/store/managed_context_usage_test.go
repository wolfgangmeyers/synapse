package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestManagedAgentContextUsageStore(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(ctx, dbPath, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	// Verify migration 100_managed_agent_context_usage was applied
	var count int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='100_managed_agent_context_usage'`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "migration 100_managed_agent_context_usage must be applied")

	// Launch new agent with context usage initially nil/0
	agentID := "worker-context-test"
	launchRecord := ManagedAgentRecord{
		AgentID:          agentID,
		Name:             "Worker Context Test",
		Cwd:              t.TempDir(),
		SelectedModel:    ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     "manifest-sha",
		CreatedAt:        "2026-09-01T12:00:00Z",
		UpdatedAt:        "2026-09-01T12:00:00Z",
	}
	op := ManagedOperation{
		OperationID:       "op-launch-context",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-launch-context",
		CanonicalBodyHash: "hash-launch-context",
		State:             "terminal",
		ResponseStatus:    200,
	}
	err = s.ReserveManagedLaunch(ctx, launchRecord, op)
	require.NoError(t, err)

	// 1. By default, newly launched agent has nil tokens, 0 window, nil percent
	loaded, err := s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Nil(t, loaded.ContextTokens)
	require.Equal(t, 0, loaded.ContextWindow)
	require.Nil(t, loaded.ContextPercent)

	// 2. Update context usage
	tokens := 42000
	window := 1000000
	percent := 4.2
	err = s.UpdateManagedAgentContextUsage(ctx, agentID, &tokens, window, &percent)
	require.NoError(t, err)

	// Verify ManagedAgent loads updated context usage
	loadedAfter, err := s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, loadedAfter.ContextTokens)
	require.Equal(t, 42000, *loadedAfter.ContextTokens)
	require.Equal(t, 1000000, loadedAfter.ContextWindow)
	require.NotNil(t, loadedAfter.ContextPercent)
	require.InDelta(t, 4.2, *loadedAfter.ContextPercent, 0.001)

	// Verify ManagedAgents listing includes context usage
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
	require.NotNil(t, found.ContextTokens)
	require.Equal(t, 42000, *found.ContextTokens)
	require.Equal(t, 1000000, found.ContextWindow)
	require.NotNil(t, found.ContextPercent)
	require.InDelta(t, 4.2, *found.ContextPercent, 0.001)

	// Verify ManagedAgentsProjectionSnapshot includes context usage
	snapshot, err := s.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	var snapshotFound *ManagedAgentRecord
	for _, a := range snapshot.Agents {
		if a.AgentID == agentID {
			snapshotFound = a
			break
		}
	}
	require.NotNil(t, snapshotFound)
	require.NotNil(t, snapshotFound.ContextTokens)
	require.Equal(t, 42000, *snapshotFound.ContextTokens)
	require.Equal(t, 1000000, snapshotFound.ContextWindow)
	require.NotNil(t, snapshotFound.ContextPercent)
	require.InDelta(t, 4.2, *snapshotFound.ContextPercent, 0.001)

	// Verify projection version was bumped
	require.Positive(t, snapshot.ProjectionVersion)
}
