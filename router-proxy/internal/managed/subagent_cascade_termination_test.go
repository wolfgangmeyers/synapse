package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestTerminateCascadesSubagents(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cascade_term.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	parent := subagentTestParent(t, db, "parent-cascade")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	// Create a subagent in store
	childID := "subagent-parent-cascade-child1"
	childRecord := store.ManagedSubagentRecord{
		AgentID:       childID,
		ParentAgentID: parent.AgentID,
		Description:   "task child",
		SubagentType:  "general-purpose",
		Status:        "running",
		MaxTurns:      10,
	}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, childRecord))

	// Register in manager's active subagents
	childTask := &subagentTask{
		parent:  parent,
		childID: childID,
		status:  "running",
	}
	mgr.subagentsMu.Lock()
	mgr.subagentsActive[childID] = childTask
	mgr.subagentsRegistry[childID] = childTask
	mgr.subagentsMu.Unlock()

	// Call CascadeTerminateSubagents to test store transition
	require.NoError(t, db.CascadeTerminateSubagents(ctx, parent.AgentID))

	// SQLite store: subagent record in managed_agents must be terminated
	var childDesired, childLifecycle string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT desired_state, lifecycle_state FROM managed_agents WHERE agent_id=?`, childID).Scan(&childDesired, &childLifecycle))
	require.Equal(t, "terminated", childDesired)
	require.Equal(t, "terminated", childLifecycle)

	// SQLite store: subagent record in managed_subagents must be cancelled
	var childSubStatus string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT status FROM managed_subagents WHERE agent_id=?`, childID).Scan(&childSubStatus))
	require.Equal(t, "cancelled", childSubStatus)

	// Terminate parent agent
	termReq := TerminateRequest{
		AgentID:           parent.AgentID,
		Method:            "DELETE",
		CanonicalPath:     "/v1/managed-agents/" + parent.AgentID,
		RequestID:         "123e4567-e89b-42d3-a456-426614174888",
		CanonicalBodyHash: strings.Repeat("b", 64),
	}
	res, err := mgr.TerminateManagedAgent(ctx, termReq)
	require.NoError(t, err)
	require.NotNil(t, res.Operation)

	// In-memory task must be marked stopped
	childTask.mu.Lock()
	require.True(t, childTask.stopped)
	childTask.mu.Unlock()
}

func TestTopLevelSnapshotExcludesSubagents(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "proj_exclusion.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	parent := subagentTestParent(t, db, "parent-top-level")

	// Create subagent properly via store
	childID := "subagent-parent-top-level-child"
	childRecord := store.ManagedSubagentRecord{
		AgentID:       childID,
		ParentAgentID: parent.AgentID,
		Description:   "sub task",
		SubagentType:  "general-purpose",
		Status:        "running",
		MaxTurns:      5,
	}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, childRecord))
	_, err = rawDB.ExecContext(ctx, `UPDATE managed_subagents SET status='completed', terminal_at='2026-01-01T00:00:00Z' WHERE agent_id=?`, childID)
	require.NoError(t, err)

	// Also insert an orphaned subagent row in managed_agents directly (no entry in managed_subagents)
	orphanedID := "subagent-parent-top-level-orphan"
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at,runtime,parent_agent_id) VALUES(?,?,0,?, 'openai-codex', 'gpt-5', 'running', 'running', 'idle', 0, ?, 1, 'gen', 1, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'embedded', ?)`, orphanedID, orphanedID, t.TempDir(), orphanedID+"-session", strings.Repeat("c", 64), parent.AgentID)
	require.NoError(t, err)

	// Query ManagedAgents
	agents, err := db.ManagedAgents(ctx)
	require.NoError(t, err)
	for _, a := range agents {
		require.False(t, strings.HasPrefix(a.AgentID, "subagent-"), "ManagedAgents must never return subagents: got %s", a.AgentID)
		require.Empty(t, a.ParentAgentID, "ManagedAgents must never return agents with parent_agent_id")
	}

	// Query projection snapshot
	snapshot, err := db.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	for _, a := range snapshot.Agents {
		require.False(t, strings.HasPrefix(a.AgentID, "subagent-"), "Snapshot must never return subagents: got %s", a.AgentID)
		require.Empty(t, a.ParentAgentID)
	}

	// Envelope projection
	envelope := ProjectionListEnvelope(snapshot)
	envAgents, ok := envelope["agents"].([]map[string]any)
	require.True(t, ok)
	for _, ea := range envAgents {
		id, _ := ea["agent_id"].(string)
		require.False(t, strings.HasPrefix(id, "subagent-"), "Projection envelope must never contain subagents: got %s", id)
	}
}

func TestPruneExpiredSubagentsAndTimeout(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_prune.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	parent := subagentTestParent(t, db, "parent-prune")

	now := time.Now().UTC()
	oldTime := now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	recentTime := now.Add(-10 * time.Minute).Format("2006-01-02T15:04:05.000Z")

	// 1. Old terminal subagent (> 1 hour ago)
	oldID := "subagent-parent-prune-old"
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{
		AgentID: oldID, ParentAgentID: parent.AgentID, Description: "old", Status: "running", MaxTurns: 5,
	}))
	_, err = rawDB.ExecContext(ctx, `UPDATE managed_subagents SET status='completed', terminal_at=?, updated_at=? WHERE agent_id=?`, oldTime, oldTime, oldID)
	require.NoError(t, err)

	// 2. Recent terminal subagent (10 mins ago)
	recentID := "subagent-parent-prune-recent"
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{
		AgentID: recentID, ParentAgentID: parent.AgentID, Description: "recent", Status: "running", MaxTurns: 5,
	}))
	_, err = rawDB.ExecContext(ctx, `UPDATE managed_subagents SET status='completed', terminal_at=?, updated_at=? WHERE agent_id=?`, recentTime, recentTime, recentID)
	require.NoError(t, err)

	// 3. Stale running subagent created > 1 hour ago
	runningID := "subagent-parent-prune-running"
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{
		AgentID: runningID, ParentAgentID: parent.AgentID, Description: "stale running", Status: "running", MaxTurns: 5,
	}))
	_, err = rawDB.ExecContext(ctx, `UPDATE managed_subagents SET created_at=? WHERE agent_id=?`, oldTime, runningID)
	require.NoError(t, err)

	// Run timeout
	timedOutCount, err := db.TimeoutRunningSubagents(ctx, 1*time.Hour)
	require.NoError(t, err)
	require.Equal(t, 1, timedOutCount)

	// Verify runningID is now failed
	var runningStatus string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT status FROM managed_subagents WHERE agent_id=?`, runningID).Scan(&runningStatus))
	require.Equal(t, "failed", runningStatus)

	// Run PruneExpiredSubagents
	require.NoError(t, db.PruneExpiredSubagents(ctx, 1*time.Hour))

	// oldID must be pruned (deleted)
	var countOld int
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_subagents WHERE agent_id=?`, oldID).Scan(&countOld))
	require.Equal(t, 0, countOld, "expired subagent must be pruned")

	// recentID must still exist
	var countRecent int
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_subagents WHERE agent_id=?`, recentID).Scan(&countRecent))
	require.Equal(t, 1, countRecent, "recent subagent must not be pruned")
}
