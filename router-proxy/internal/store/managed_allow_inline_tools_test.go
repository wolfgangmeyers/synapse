package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestManagedAgentAllowInlineToolsStore(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(ctx, dbPath, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	// Verify migration 090_allow_inline_tools was applied
	var count int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='090_allow_inline_tools'`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "migration 090_allow_inline_tools must be applied")

	// Launch new agent
	agentID := "worker-inline-test"
	launchRecord := ManagedAgentRecord{
		AgentID:          agentID,
		Name:             "Worker Inline Test",
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
		OperationID:       "op-launch-inline",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-launch-inline",
		CanonicalBodyHash: "hash-launch-inline",
		State:             "terminal",
		ResponseStatus:    200,
	}
	err = s.ReserveManagedLaunch(ctx, launchRecord, op)
	require.NoError(t, err)

	// 1. By default, newly launched agent must have AllowInlineTools == false
	loaded, err := s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.False(t, loaded.AllowInlineTools, "AllowInlineTools must be false by default")

	// Verify in ManagedAgents listing
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
	require.False(t, found.AllowInlineTools)

	// Verify in projection snapshot
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
	require.False(t, snapshotFound.AllowInlineTools)

	initialVersion := snapshot.ProjectionVersion

	// 2. Set AllowInlineTools to true
	err = s.SetManagedAgentAllowInlineTools(ctx, agentID, true)
	require.NoError(t, err)

	loaded, err = s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.True(t, loaded.AllowInlineTools, "AllowInlineTools must be true after update")

	// Verify projection version was bumped
	singleSnapshot, err := s.ManagedAgentSnapshot(ctx, agentID)
	require.NoError(t, err)
	require.Greater(t, singleSnapshot.ProjectionVersion, initialVersion)
	require.NotNil(t, singleSnapshot.Agent)
	require.True(t, singleSnapshot.Agent.AllowInlineTools)

	// 3. Set AllowInlineTools back to false
	err = s.SetManagedAgentAllowInlineTools(ctx, agentID, false)
	require.NoError(t, err)

	loaded, err = s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.False(t, loaded.AllowInlineTools, "AllowInlineTools must be false after toggle")

	// 4. Non-existent agent returns error
	err = s.SetManagedAgentAllowInlineTools(ctx, "non-existent-agent", true)
	require.Error(t, err)
}
