package transport

import (
	"strings"
	"testing"

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
