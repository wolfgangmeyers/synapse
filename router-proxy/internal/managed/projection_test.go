package managed

import (
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestPublicProjectionOmitsPrivateProcessAndSessionFields(t *testing.T) {
	agent := &store.ManagedAgentRecord{
		AgentID: "alpha", Name: "alpha", Cwd: "/work", SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"},
		DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true,
		PiSessionID: "pi-session-secret", SessionEpoch: 3, ProcessPID: 1234, ProcessStartMarker: "start-secret", ProcessIncarnation: "incarnation-secret",
		RuntimeGeneration: "runtime-secret", ManifestRevision: 1, ManifestHash: "manifest-hash", PendingInitialInstruction: "private prompt", InitialInstructionSent: true,
		LastErrorCode: "", LastErrorMessage: "auth secret", LastErrorAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z", StartedAt: "2026-01-01T00:00:01Z", UpdatedAt: "2026-01-01T00:00:01Z",
	}
	snapshot := store.ManagedAgentSnapshot{GeneratedAt: time.Unix(1, 0), ProjectionVersion: 7, Agent: agent}
	projection := ProjectionEnvelope(snapshot)
	raw, err := MarshalProjectionEnvelope(snapshot)
	require.NoError(t, err)
	encoded := string(raw)
	for _, secret := range []string{"pi-session-secret", "start-secret", "incarnation-secret", "runtime-secret", "private prompt", "auth secret", "1234"} {
		require.False(t, strings.Contains(encoded, secret), secret)
	}
	require.Equal(t, "router-managed", projection["agent"].(map[string]any)["source"])
}

func TestPublicArchiveProjectionUsesOnlyTheClosedShape(t *testing.T) {
	archive := &store.ManagedAgentArchive{
		RecordKind: "archived", LifecycleState: "archived", AgentID: "alpha", Name: "alpha",
		ArchiveKey: "123e4567-e89b-42d3-a456-426614174081", TerminalOutcome: "cleanup_failed",
		CompletedAt: "2026-09-01T12:00:00.000Z", Delivery: store.ManagedDeliveryAggregate{
			Disposition: "unknown", Total: 2, InFlightOrUnknown: 1, ReleasedOrFailed: 1,
			TotalOverflow: true, InFlightOrUnknownOverflow: true,
		},
	}
	projection := PublicArchive(archive)
	require.Equal(t, map[string]any{
		"record_kind": "archived", "lifecycle_state": "archived", "agent_id": "alpha", "name": "alpha",
		"archive_key": archive.ArchiveKey, "terminal_outcome": "cleanup_failed", "completed_at": archive.CompletedAt,
		"delivery": map[string]any{
			"disposition": "unknown", "total": 2, "queued_never_attempted": 0, "in_flight_or_unknown": 1,
			"completed": 0, "released_or_failed": 1,
			"overflow": map[string]any{"total": true, "queued_never_attempted": false, "in_flight_or_unknown": true, "completed": false, "released_or_failed": false},
		},
	}, projection)
}

func TestProjectionListContainsEveryLiveRecordAndNoArchives(t *testing.T) {
	liveAlpha := &store.ManagedAgentRecord{AgentID: "alpha", Name: "alpha", Cwd: "/work/alpha", SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
	liveBeta := &store.ManagedAgentRecord{AgentID: "beta", Name: "beta", Cwd: "/work/beta", SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: strings.Repeat("b", 64)}
	archive := &store.ManagedAgentArchive{RecordKind: "archived", LifecycleState: "archived", AgentID: "alpha", Name: "alpha", ArchiveKey: "123e4567-e89b-42d3-a456-426614174081", TerminalOutcome: "terminated", CompletedAt: "2026-09-01T12:00:00.000Z"}
	result := ProjectionListEnvelope(store.ManagedAgentsSnapshot{GeneratedAt: time.Unix(1, 0), ProjectionVersion: 8, Agents: []*store.ManagedAgentRecord{liveAlpha, liveBeta}, Archives: []*store.ManagedAgentArchive{archive}})
	agents := result["agents"].([]map[string]any)
	require.Len(t, agents, 2)
	require.Equal(t, []string{"alpha", "beta"}, []string{agents[0]["agent_id"].(string), agents[1]["agent_id"].(string)})
	for _, agent := range agents {
		require.NotContains(t, agent, "record_kind")
		require.NotContains(t, agent, "archive_key")
	}
}

func TestProjectionListKeepsEmptyLiveProjectionEmpty(t *testing.T) {
	result := ProjectionListEnvelope(store.ManagedAgentsSnapshot{GeneratedAt: time.Unix(1, 0), ProjectionVersion: 9, Archives: []*store.ManagedAgentArchive{{RecordKind: "archived", AgentID: "alpha", ArchiveKey: "123e4567-e89b-42d3-a456-426614174081"}}})
	require.Equal(t, int64(9), result["projection_version"])
	require.Empty(t, result["agents"])
}

func TestPublicAgentWorktreeBasePath(t *testing.T) {
	agent := &store.ManagedAgentRecord{
		AgentID:          "alpha",
		Cwd:              "/tmp/repo-feat",
		Worktree:         true,
		WorktreeBasePath: "/tmp/repo",
		SelectedModel:    store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"},
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     strings.Repeat("a", 64),
		CreatedAt:        "2026-01-01T00:00:00Z",
		UpdatedAt:        "2026-01-01T00:00:00Z",
	}
	projection := PublicAgent(agent)
	require.Equal(t, true, projection["worktree"])
	require.Equal(t, "/tmp/repo", projection["worktree_base_path"])
}

func TestPublicProjectionKeepsAUsefulRedactedDurableCause(t *testing.T) {
	agent := &store.ManagedAgentRecord{AgentID: "alpha", Cwd: "/work", SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "failed", ActivityState: "failed", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64), LastErrorCode: "PROCESS_FAILED", LastErrorMessage: `/private/router/raw.log request failed token super-secret-value Bearer another-secret https://account:fourth-secret@host.invalid/path?access_token=third-secret /private/router/session.json cwd="/Users/wolfgang/project/output.log" cwd=&quot;/Users/wolfgang/project/output.log&quot; cwd="/Users/wolfgang/project/output log" file:///Users/wolfgang/project/output.log cwd="C:\Users\wolfgang\project\output log" permission denied`, LastErrorAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	projection := PublicAgent(agent)
	errorView := projection["last_error"].(map[string]any)
	message := errorView["message"].(string)
	for _, secret := range []string{"super-secret-value", "another-secret", "third-secret", "fourth-secret", "/private/router/raw.log", "/private/router/session.json", "/tmp/managed", "/Users/wolfgang/project/output.log", "/Users/wolfgang/project/output log", "file:///Users/wolfgang/project/output.log", `C:\Users\wolfgang\project\output log`} {
		require.NotContains(t, message, secret)
	}
	require.Contains(t, message, "request failed")
	require.Contains(t, message, "permission denied")
	require.Contains(t, message, "https://host.invalid/path")
	require.LessOrEqual(t, len(message), 512)
}
