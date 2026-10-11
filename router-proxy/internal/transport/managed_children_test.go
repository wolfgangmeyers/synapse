package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func currentChildProjectionFixture() (*store.ManagedAgentRecord, *store.ManagedReadinessSource) {
	agent := &store.ManagedAgentRecord{AgentID: "alpha", PiSessionID: "session", SessionEpoch: 2, ProcessPID: 99, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
	source := &store.ManagedReadinessSource{AgentID: "alpha", Fence: &store.ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}, Snapshot: &store.ManagedChildSnapshot{
		SnapshotID: "snapshot", OwnerSessionID: "session", Sequence: 7,
		Canonical: []byte(`{"runs":[{"task_id":"task","run_id":"run","parent_entry_id":"entry","description":"use token top-secret-value for process 123","model":{"provider":"openai-codex","id":"gpt-5.6-luna","name":"GPT-5.6 Luna"},"lifecycle_state":"running","started_at":"2030-01-01T00:00:00Z","finished_at":null,"duration_ms":1200,"compaction":{"state":"idle","count":2,"started_at":null,"last_outcome":"completed"},"sequence":3}]}`),
	}, SnapshotConfirmedAt: "2030-01-01T00:00:00Z"}
	return agent, source
}

func TestManagedChildrenProjectionCarriesStructuredSafeChildState(t *testing.T) {
	agent, source := currentChildProjectionFixture()
	projection := managedChildrenProjectionForAgent(agent, source)
	require.Equal(t, "alpha", projection["agent_id"])
	require.Equal(t, "router-managed", projection["source"])
	require.Equal(t, int64(7), projection["lifecycle_version"])
	require.Equal(t, "current", projection["recovery_state"])
	children := projection["children"].([]map[string]any)
	require.Len(t, children, 1)
	child := children[0]
	require.Equal(t, "entry", child["parent_entry_id"])
	require.Equal(t, "task", child["task_id"])
	require.Equal(t, "run", child["run_id"])
	require.Equal(t, int64(3), child["sequence"])
	require.Equal(t, int64(1200), child["duration_ms"])
	require.Equal(t, map[string]any{"state": "confirmed", "cause": nil, "observed_at": "2030-01-01T00:00:00Z"}, child["recovery"])
	require.Equal(t, map[string]any{"provider": "openai-codex", "id": "gpt-5.6-luna", "name": "GPT-5.6 Luna"}, child["model"])
	require.Equal(t, map[string]any{"state": "idle", "count": int64(2), "last_outcome": "completed"}, child["compaction"])
	require.NotContains(t, child["description"], "top-secret-value")
	for _, field := range []string{"owner_session_id", "snapshot_id", "process_pid", "runtime_generation", "pi_session_id"} {
		_, present := child[field]
		require.False(t, present, field)
	}
}

func TestManagedChildrenProjectionRejectsStaleParentFenceWithoutInventingTerminalState(t *testing.T) {
	agent, source := currentChildProjectionFixture()
	source.Fence.ProcessIncarnation = "stale-incarnation"
	projection := managedChildrenProjectionForAgent(agent, source)
	require.Equal(t, "reconstructing", projection["recovery_state"])
	require.Empty(t, projection["children"])
	require.Equal(t, int64(0), projection["lifecycle_version"])
}

func TestManagedChildrenProjectionRejectsUnreadableSnapshotWithoutLeakingIt(t *testing.T) {
	agent, source := currentChildProjectionFixture()
	source.Snapshot.Canonical = []byte(`not-json`)
	projection := managedChildrenProjectionForAgent(agent, source)
	require.Equal(t, "reconstructing", projection["recovery_state"])
	require.Empty(t, projection["children"])
}

func TestManagedChildrenProjectionRendersInterruptedUnknownRecovery(t *testing.T) {
	agent, source := currentChildProjectionFixture()
	source.Fence.ProcessIncarnation = "replacement"
	source.Recoveries = []store.ManagedChildRecovery{{
		PriorSessionID: "session", PriorSessionEpoch: 2, PriorProcessIncarnation: "incarnation", PriorRuntimeGeneration: "generation",
		ParentEntryID: "entry", TaskID: "task", RunID: "run", Sequence: 3, Cause: "parent_process_lost", ObservedAt: "2030-01-01T00:00:03Z",
		RunCanonical: []byte(`{"task_id":"task","run_id":"run","parent_entry_id":"entry","description":"Recovery child","model":null,"lifecycle_state":"running","started_at":"2030-01-01T00:00:00Z","finished_at":null,"duration_ms":null,"compaction":{"state":"compacting","count":2,"started_at":"2030-01-01T00:00:02Z","last_outcome":null},"sequence":3}`),
	}}
	projection := managedChildrenProjectionForAgent(agent, source)
	require.Equal(t, "reconstructing", projection["recovery_state"])
	children := projection["children"].([]map[string]any)
	require.Len(t, children, 1)
	require.Equal(t, "running", children[0]["status"])
	require.Equal(t, map[string]any{"state": "interrupted_unknown", "cause": "parent_process_lost", "observed_at": "2030-01-01T00:00:03Z"}, children[0]["recovery"])
	require.Equal(t, "reconciled_unknown", children[0]["compaction"].(map[string]any)["last_outcome"])
}

func TestManagedChildrenGETReturnsEmptyForMissingOrArchivedAgent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	manager, err := managed.NewManager(managed.ManagerOptions{Store: s, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)

	router, err := New(Options{Store: s, Managed: manager, Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = router.Close(ctx) })

	// Query children for a non-existent / terminating agent returns 200 OK with empty children
	req := httptest.NewRequest(http.MethodGet, "/v1/managed-agents/deleted-agent/children", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "deleted-agent", body["agent_id"])
	require.Equal(t, "router-managed", body["source"])
	require.Equal(t, float64(0), body["lifecycle_version"])
	require.Equal(t, "current", body["recovery_state"])
	children, ok := body["children"].([]any)
	require.True(t, ok)
	require.Empty(t, children)

	// Query children for an archived agent returns 200 OK with empty children
	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agent_archives (
		archive_key,agent_id,name,terminal_outcome,completed_at,disposition,total,
		queued_never_attempted,in_flight_or_unknown,completed,released_or_failed)
		VALUES ('123e4567-e89b-42d3-a456-426614174099','archived-agent','archived-agent','terminated','2026-10-11T00:00:00Z','completed',1,0,0,1,0)`)
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodGet, "/v1/managed-agents/archived-agent/children", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var archivedBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &archivedBody))
	require.Equal(t, "archived-agent", archivedBody["agent_id"])
	require.Equal(t, "router-managed", archivedBody["source"])
	require.Equal(t, float64(0), archivedBody["lifecycle_version"])
	require.Equal(t, "current", archivedBody["recovery_state"])
	children, ok = archivedBody["children"].([]any)
	require.True(t, ok)
	require.Empty(t, children)
}
