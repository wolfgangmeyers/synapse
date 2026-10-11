package transport

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func setupTestStoreWithParent(t *testing.T, parentID string) (*store.Store, string, store.EmbeddedBinding) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	agent := store.ManagedAgentRecord{
		AgentID:          parentID,
		Name:             parentID,
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "anthropic", ID: "claude-sonnet-4-6"},
		Runtime:          "embedded",
		PiSessionID:      parentID + "-session",
		SessionEpoch:     1,
		ManifestRevision: 1,
		ManifestHash:     strings.Repeat("a", 64),
	}
	op := store.ManagedOperation{
		OperationID:       parentID + "-launch",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         parentID + "-launch",
		CanonicalBodyHash: "hash",
		ResponseStatus:    202,
		ResponseHeaders:   map[string]string{},
		ResponseBody:      []byte(`{}`),
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, s.UpdateManagedAgentStarting(ctx, parentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: 1234, StartMarker: "start", Incarnation: "inc"}, "gen-1"))
	require.NoError(t, s.MarkManagedReady(ctx, parentID))

	binding := store.EmbeddedBinding{
		AgentID:    parentID,
		SessionID:  agent.PiSessionID,
		Epoch:      1,
		Generation: "gen-1",
	}
	require.NoError(t, s.BindEmbeddedSession(ctx, binding, "worker"))
	token, err := s.AcquireEmbeddedOwner(ctx, binding, "fixture:"+parentID, time.Minute)
	require.NoError(t, err)
	binding.OwnerToken = token
	return s, path, binding
}

// simulateUIActiveSubagents mimics the exact filtering in web/router-ui-react:
// activeChildCount = children.filter(child => child.recovery.state === "confirmed" && (child.status === "queued" || child.status === "running")).length
func simulateUIActiveSubagents(children []map[string]any) []map[string]any {
	active := make([]map[string]any, 0)
	for _, child := range children {
		rec, _ := child["recovery"].(map[string]any)
		if rec == nil || rec["state"] != "confirmed" {
			continue
		}
		status, _ := child["status"].(string)
		if status == "queued" || status == "running" {
			active = append(active, child)
		}
	}
	return active
}

func TestNativeSubagentExcludedFromTopLevelProjection(t *testing.T) {
	ctx := context.Background()
	s, _, parent := setupTestStoreWithParent(t, "top-parent")
	router, err := New(Options{Store: s, Port: 0})
	require.NoError(t, err)

	child := store.ManagedSubagentRecord{
		AgentID:        "native-child-hidden",
		ParentAgentID:  parent.AgentID,
		Description:    "explore directory",
		SubagentType:   "general-purpose",
		Status:         "queued",
		MaxTurns:       5,
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child))

	// 1. Store ManagedAgents excludes native subagent
	agents, err := s.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	require.Equal(t, parent.AgentID, agents[0].AgentID)

	// 2. Store ManagedAgentsProjectionSnapshot excludes native subagent
	snapshot, err := s.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Agents, 1)
	require.Equal(t, parent.AgentID, snapshot.Agents[0].AgentID)

	// 3. UI live snapshot frame excludes native subagent from top-level agents
	hubSnapshot, err := router.uiLive.snapshot(ctx)
	require.NoError(t, err)
	frame := hubSnapshot.frame()
	topLevelAgents, ok := frame["agents"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, topLevelAgents, 1)
	require.Equal(t, parent.AgentID, topLevelAgents[0]["agent_id"])

	// 4. Requesting the native subagent directly as a top-level parent fails
	_, err = router.managedChildrenProjection(ctx, child.AgentID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestNativeSubagentBadgeCountAndDisclosureQueuedAndRunning(t *testing.T) {
	ctx := context.Background()
	s, _, parent := setupTestStoreWithParent(t, "active-parent")
	router, err := New(Options{Store: s, Port: 0})
	require.NoError(t, err)

	child1 := store.ManagedSubagentRecord{
		AgentID:        "native-child-q",
		ParentAgentID:  parent.AgentID,
		Description:    "queued task description",
		SubagentType:   "general-purpose",
		Status:         "queued",
		MaxTurns:       3,
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child1))

	child2 := store.ManagedSubagentRecord{
		AgentID:        "native-child-r",
		ParentAgentID:  parent.AgentID,
		Description:    "running task description",
		SubagentType:   "general-purpose",
		Status:         "running",
		MaxTurns:       5,
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child2))

	projection, err := router.managedChildrenProjection(ctx, parent.AgentID)
	require.NoError(t, err)
	require.Equal(t, parent.AgentID, projection["agent_id"])
	require.Equal(t, "router-managed", projection["source"])
	require.Equal(t, "current", projection["recovery_state"])

	children, ok := projection["children"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, children, 2)

	// Verify both children have structured projection matching UI protocol
	for _, c := range children {
		require.NotEmpty(t, c["id"])
		require.NotEmpty(t, c["parent_entry_id"])
		require.NotEmpty(t, c["task_id"])
		require.NotEmpty(t, c["run_id"])
		require.Equal(t, "none", c["compaction_state"])
		comp, _ := c["compaction"].(map[string]any)
		require.NotNil(t, comp)
		require.Equal(t, "idle", comp["state"])
	}

	// Verify UI badge disclosure: count is 2, descriptions match
	active := simulateUIActiveSubagents(children)
	require.Len(t, active, 2)
	descriptions := []string{active[0]["description"].(string), active[1]["description"].(string)}
	require.Contains(t, descriptions, "queued task description")
	require.Contains(t, descriptions, "running task description")
}

func TestNativeSubagentTerminalHidesBadge(t *testing.T) {
	ctx := context.Background()
	s, _, parent := setupTestStoreWithParent(t, "term-parent")
	router, err := New(Options{Store: s, Port: 0})
	require.NoError(t, err)

	child := store.ManagedSubagentRecord{
		AgentID:        "native-child-term",
		ParentAgentID:  parent.AgentID,
		Description:    "short task",
		SubagentType:   "general-purpose",
		Status:         "running",
		MaxTurns:       2,
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child))

	// Transition to completed
	require.NoError(t, s.UpdateManagedSubagent(ctx, parent, child.AgentID, "running", store.ManagedSubagentResult{
		Status:     "completed",
		TurnCount:  2,
		ResultText: "done",
	}))

	projection, err := router.managedChildrenProjection(ctx, parent.AgentID)
	require.NoError(t, err)

	children, ok := projection["children"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, children, 1)
	require.Equal(t, "completed", children[0]["status"])

	// UI filtering: completed child is NOT queued or running, so active count is 0 -> badge hides
	active := simulateUIActiveSubagents(children)
	require.Empty(t, active, "completed child must not appear as active in UI badge")
}

func TestNativeSubagentRemainsVisibleAcrossParentEpoch(t *testing.T) {
	ctx := context.Background()
	s, dbPath, parent := setupTestStoreWithParent(t, "fenced-parent")
	router, err := New(Options{Store: s, Port: 0})
	require.NoError(t, err)

	child := store.ManagedSubagentRecord{
		AgentID:        "native-child-epoch1",
		ParentAgentID:  parent.AgentID,
		Description:    "child from epoch 1",
		SubagentType:   "general-purpose",
		Status:         "running",
		MaxTurns:       5,
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, child))

	// Before epoch advance, projection sees the child
	projection, err := router.managedChildrenProjection(ctx, parent.AgentID)
	require.NoError(t, err)
	children, _ := projection["children"].([]map[string]any)
	require.Len(t, children, 1)

	// Advance parent epoch in managed_agents (simulating parent reset/restart epoch increment)
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec("UPDATE managed_agents SET session_epoch = session_epoch + 1 WHERE agent_id = ?", parent.AgentID)
	require.NoError(t, err)

	// Query projection for parent under new epoch: child must remain visible
	projectionNew, err := router.managedChildrenProjection(ctx, parent.AgentID)
	require.NoError(t, err)
	childrenNew, _ := projectionNew["children"].([]map[string]any)
	require.Len(t, childrenNew, 1, "epoch 1 child must remain visible under epoch 2")
	require.Equal(t, "native-child-epoch1", childrenNew[0]["id"])
}

func TestNativeSubagentErrorPathDistinguishesStaleVsDBFailure(t *testing.T) {
	ctx := context.Background()
	agent := &store.ManagedAgentRecord{
		AgentID:           "test-agent",
		Runtime:           "embedded",
		PiSessionID:       "session-1",
		SessionEpoch:      1,
		RuntimeGeneration: "gen-1",
	}

	// 1. Benign stale parent race: returns empty children without error
	staleStore := &mockNativeStore{err: errors.New("stale subagent parent binding")}
	children, err := loadNativeChildrenForAgent(ctx, staleStore, agent)
	require.NoError(t, err)
	require.Nil(t, children)

	base := map[string]any{"agent_id": agent.AgentID, "source": "router-managed", "lifecycle_version": int64(0), "recovery_state": "reconstructing", "children": []map[string]any{}}
	merged, err := mergeNativeSubagentsForAgent(ctx, staleStore, agent, base)
	require.NoError(t, err)
	require.Equal(t, "current", merged["recovery_state"])
	require.Empty(t, merged["children"])

	// 2. Real DB/read failure: fails loudly and propagates error
	dbFailStore := &mockNativeStore{err: errors.New("database disk image is malformed")}
	children, err = loadNativeChildrenForAgent(ctx, dbFailStore, agent)
	require.Error(t, err)
	require.Contains(t, err.Error(), "malformed")
	require.Nil(t, children)

	_, err = mergeNativeSubagentsForAgent(ctx, dbFailStore, agent, base)
	require.Error(t, err)
	require.Contains(t, err.Error(), "malformed")
}

type mockNativeStore struct {
	records []*store.ManagedSubagentRecord
	err     error
}

func (m *mockNativeStore) ManagedSubagents(ctx context.Context, parent store.EmbeddedBinding) ([]*store.ManagedSubagentRecord, error) {
	return m.records, m.err
}

func TestExistingRPCChildrenRemainWithNativeSubagents(t *testing.T) {
	ctx := context.Background()
	s, _, parent := setupTestStoreWithParent(t, "mixed-parent")
	router, err := New(Options{Store: s, Port: 0})
	require.NoError(t, err)

	// 1. Add RPC child via ConfirmManagedChildSnapshot
	rpcFence := store.ManagedRuntimeFence{
		AgentID:            parent.AgentID,
		PiSessionID:        parent.SessionID,
		SessionEpoch:       parent.Epoch,
		ProcessPID:         1234,
		ProcessStartMarker: "start",
		ProcessIncarnation: "inc",
		RuntimeGeneration:  parent.Generation,
		ManifestRevision:   1,
		ManifestHash:       strings.Repeat("a", 64),
	}
	policy := store.DefaultManagedReadinessPolicy()
	require.NoError(t, s.ConfirmManagedPolicyApplied(ctx, rpcFence, policy))
	rpcSnap := store.ManagedChildSnapshot{
		SnapshotID:     "snap-1",
		OwnerSessionID: parent.SessionID,
		Sequence:       1,
		Canonical:      []byte(`{"runs":[{"task_id":"rpc-task","run_id":"rpc-run","parent_entry_id":"rpc-entry","description":"rpc child work","lifecycle_state":"running","compaction":{"state":"idle","count":1},"sequence":1}]}`),
		SHA256:         strings.Repeat("a", 64),
	}
	require.NoError(t, s.ConfirmManagedChildSnapshot(ctx, rpcFence, rpcSnap))

	// 2. Add native subagent
	nativeChild := store.ManagedSubagentRecord{
		AgentID:        "native-sub-1",
		ParentAgentID:  parent.AgentID,
		Description:    "native child work",
		SubagentType:   "general-purpose",
		Status:         "running",
		MaxTurns:       3,
	}
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, nativeChild))

	// 3. Query projection: both RPC child and native child must be present
	projection, err := router.managedChildrenProjection(ctx, parent.AgentID)
	require.NoError(t, err)
	children, ok := projection["children"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, children, 2, "both RPC child and native subagent should be present")

	// Verify both are present and active
	active := simulateUIActiveSubagents(children)
	require.Len(t, active, 2)
}
