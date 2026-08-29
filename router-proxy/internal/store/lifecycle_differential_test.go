package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

type lifecycleTraceSummary struct {
	Dispositions []string             `json:"dispositions"`
	Hashes       []lifecycleHashRow   `json:"hashes"`
	Replay       lifecycleReplayTrace `json:"replay"`
	Parent       lifecycleParentTrace `json:"parent"`
}

type lifecycleHashRow struct {
	EventID string `json:"event_id"`
	Hash    string `json:"payload_hash"`
}

type lifecycleReplayTrace struct {
	Mode          string `json:"mode"`
	RouterVersion int64  `json:"router_version"`
	UpdateCount   int    `json:"update_count"`
	ProjectionLen int    `json:"projection_len"`
}

type lifecycleParentTrace struct {
	Version int64 `json:"version"`
	Count   int   `json:"count"`
}

func TestNodeAndGoLifecycleTraceMatchesAcrossDuplicatesWhitespaceIncludingU0085AndRestart(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))

	nodePath := filepath.Join(t.TempDir(), "node.db")
	node := runNodeJSON[lifecycleTraceSummary](t, nodePath, `
const { DatabaseSync } = await import("node:sqlite");
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const now = "2026-08-29T12:00:00.000Z";
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date(now) });
const base = {
  agent_id: "parent-a", instance_id: "instance-a", parent_session_id: "session-a",
  lease_id: "lease-a", producer_epoch: "123e4567-e89b-42d3-a456-426614174000",
};
const snapshot = {
  version: "v1", frame_id: "frame-snapshot", type: "subagent_lifecycle_snapshot",
  ...base, client_sequence: 1, event_id: "snapshot-1",
  subagents: [{ id: " \u0085child-a\u0085 ", type: " worker ", description: " child <&> ", status: "running" }],
};
const started = {
  version: "v1", frame_id: "frame-started", type: "subagent_lifecycle_event",
  ...base, client_sequence: 2, event_id: "started-1", lifecycle_event: "started",
  terminal_reason: "ignored",
  subagent: { id: " \u0085child-a\u0085 ", type: " worker ", description: " child <&> ", status: "running" },
};
const terminal = {
  version: "v1", frame_id: "frame-terminal", type: "subagent_lifecycle_event",
  ...base, client_sequence: 3, event_id: "terminal-1", lifecycle_event: "terminal",
  terminal_reason: "completed", subagent: { id: " \u0085child-a\u0085 " },
};
const dispositions = [
  repository.applySubagentLifecycleSnapshot(snapshot).disposition,
  repository.applySubagentLifecycleEvent(started).disposition,
  repository.applySubagentLifecycleEvent(started).disposition,
  repository.applySubagentLifecycleEvent(terminal).disposition,
];
const replay = repository.replaySubagentLifecycle({ afterVersion: 0 });
const parent = repository.subagentLifecycleSnapshot({ parentAgentId: "parent-a" }).agents[0];
repository.close();
const database = new DatabaseSync(process.env.MECHA_NODE_STORE_DATABASE, { enableForeignKeyConstraints: true });
const hashes = database.prepare("SELECT event_id, payload_hash FROM subagent_lifecycle_events ORDER BY router_version ASC").all();
database.close();
console.log(JSON.stringify({
  dispositions,
  hashes,
  replay: { mode: replay.mode, router_version: replay.router_version, update_count: replay.updates.length, projection_len: replay.agents?.length ?? 0 },
  parent: { version: parent?.subagent_version ?? 0, count: parent?.subagent_count ?? 0 },
}));
`)

	goPath := filepath.Join(t.TempDir(), "go.db")
	goStore, err := Open(ctx, goPath, Options{Clock: clock})
	require.NoError(t, err)
	goTrace := applyLifecycleTrace(t, goStore)
	require.Equal(t, node, goTrace)
	require.NoError(t, goStore.Close())

	// The Go store must read Node's durable producer and recognize the same
	// terminal retry after a process restart.
	crossStore, err := Open(ctx, nodePath, Options{Clock: clock})
	require.NoError(t, err)
	terminal := lifecycleCandidate(3, "terminal-1", "terminal", LifecycleChild{ID: " \u0085child-a\u0085 "})
	terminal.TerminalReason = "completed"
	result, err := crossStore.ApplySubagentLifecycle(ctx, terminal, false)
	require.NoError(t, err)
	require.Equal(t, "duplicate", result.Disposition)
	require.Equal(t, int64(2), result.AppliedVersion)
	require.NoError(t, crossStore.Close())

	crossStore, err = Open(ctx, nodePath, Options{Clock: clock})
	require.NoError(t, err)
	result, err = crossStore.ApplySubagentLifecycle(ctx, terminal, false)
	require.NoError(t, err)
	require.Equal(t, "duplicate", result.Disposition)
	require.NoError(t, crossStore.Close())
}

func TestNodeAndGoLifecycleHashRetriesTreatU2028AndU2029AsDuplicates(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	const nodeSnapshots = `
const descriptions = ["before\u2028after", "before\u2029after"];
const base = { instance_id: "instance-a", parent_session_id: "session-a", lease_id: "lease-a", producer_epoch: "123e4567-e89b-42d3-a456-426614174000" };
const results = descriptions.map((description, index) => repository.applySubagentLifecycleSnapshot({
  version: "v1", frame_id: "frame-" + index, type: "subagent_lifecycle_snapshot",
  agent_id: "unicode-parent-" + index, ...base, client_sequence: 1, event_id: "unicode-snapshot-" + index,
  subagents: [{ id: "unicode-child-" + index, type: "worker", description, status: "running" }],
}).disposition);
console.log(JSON.stringify(results));
repository.close();
`

	nodePath := filepath.Join(t.TempDir(), "node.db")
	nodeResults := runNodeJSON[[]string](t, nodePath, `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date("2026-08-29T12:00:00.000Z") });
`+nodeSnapshots)
	require.Equal(t, []string{"applied", "applied"}, nodeResults)

	goStore, err := Open(ctx, nodePath, Options{Clock: clock})
	require.NoError(t, err)
	for index, description := range []string{"before\u2028after", "before\u2029after"} {
		candidate := lifecycleSnapshotCandidate(1, "unicode-snapshot-"+fmt.Sprint(index), LifecycleChild{
			ID: "unicode-child-" + fmt.Sprint(index), Type: "worker", Description: description, Status: "running",
		})
		candidate.ParentAgentID = "unicode-parent-" + fmt.Sprint(index)
		result, applyErr := goStore.ApplySubagentLifecycle(ctx, candidate, false)
		require.NoError(t, applyErr)
		require.Equal(t, "duplicate", result.Disposition)
	}
	require.NoError(t, goStore.Close())

	goPath := filepath.Join(t.TempDir(), "go.db")
	goStore, err = Open(ctx, goPath, Options{Clock: clock})
	require.NoError(t, err)
	for index, description := range []string{"before\u2028after", "before\u2029after"} {
		candidate := lifecycleSnapshotCandidate(1, "unicode-snapshot-"+fmt.Sprint(index), LifecycleChild{
			ID: "unicode-child-" + fmt.Sprint(index), Type: "worker", Description: description, Status: "running",
		})
		candidate.ParentAgentID = "unicode-parent-" + fmt.Sprint(index)
		_, err = goStore.ApplySubagentLifecycle(ctx, candidate, false)
		require.NoError(t, err)
	}
	require.NoError(t, goStore.Close())

	nodeResults = runNodeJSON[[]string](t, goPath, `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date("2026-08-29T12:00:00.000Z") });
`+nodeSnapshots)
	require.Equal(t, []string{"duplicate", "duplicate"}, nodeResults)
}

func TestNodeAndGoLifecycleHashRetriesPreserveLiteralUnicodeEscapes(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	const nodeSnapshots = `
const descriptions = ["before\\u2028after", "before\\u2029after"];
const base = { instance_id: "instance-a", parent_session_id: "session-a", lease_id: "lease-a", producer_epoch: "123e4567-e89b-42d3-a456-426614174000" };
const results = descriptions.map((description, index) => repository.applySubagentLifecycleSnapshot({
  version: "v1", frame_id: "frame-" + index, type: "subagent_lifecycle_snapshot",
  agent_id: "literal-parent-" + index, ...base, client_sequence: 1, event_id: "literal-snapshot-" + index,
  subagents: [{ id: "literal-child-" + index, type: "worker", description, status: "running" }],
}).disposition);
console.log(JSON.stringify(results));
repository.close();
`

	nodePath := filepath.Join(t.TempDir(), "node.db")
	nodeResults := runNodeJSON[[]string](t, nodePath, `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date("2026-08-29T12:00:00.000Z") });
`+nodeSnapshots)
	require.Equal(t, []string{"applied", "applied"}, nodeResults)

	goStore, err := Open(ctx, nodePath, Options{Clock: clock})
	require.NoError(t, err)
	for index, description := range []string{"before\\u2028after", "before\\u2029after"} {
		candidate := lifecycleSnapshotCandidate(1, "literal-snapshot-"+fmt.Sprint(index), LifecycleChild{
			ID: "literal-child-" + fmt.Sprint(index), Type: "worker", Description: description, Status: "running",
		})
		candidate.ParentAgentID = "literal-parent-" + fmt.Sprint(index)
		result, applyErr := goStore.ApplySubagentLifecycle(ctx, candidate, false)
		require.NoError(t, applyErr)
		require.Equal(t, "duplicate", result.Disposition)
	}
	require.NoError(t, goStore.Close())

	goPath := filepath.Join(t.TempDir(), "go.db")
	goStore, err = Open(ctx, goPath, Options{Clock: clock})
	require.NoError(t, err)
	for index, description := range []string{"before\\u2028after", "before\\u2029after"} {
		candidate := lifecycleSnapshotCandidate(1, "literal-snapshot-"+fmt.Sprint(index), LifecycleChild{
			ID: "literal-child-" + fmt.Sprint(index), Type: "worker", Description: description, Status: "running",
		})
		candidate.ParentAgentID = "literal-parent-" + fmt.Sprint(index)
		_, err = goStore.ApplySubagentLifecycle(ctx, candidate, false)
		require.NoError(t, err)
	}
	require.NoError(t, goStore.Close())

	nodeResults = runNodeJSON[[]string](t, goPath, `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date("2026-08-29T12:00:00.000Z") });
`+nodeSnapshots)
	require.Equal(t, []string{"duplicate", "duplicate"}, nodeResults)
}

func applyLifecycleTrace(t *testing.T, lifecycleStore *Store) lifecycleTraceSummary {
	t.Helper()
	ctx := context.Background()
	snapshot := lifecycleSnapshotCandidate(1, "snapshot-1", LifecycleChild{
		ID: " \u0085child-a\u0085 ", Type: " worker ", Description: " child <&> ", Status: "running",
	})
	started := lifecycleCandidate(2, "started-1", "started", snapshot.Subagents[0])
	started.TerminalReason = "ignored"
	terminal := lifecycleCandidate(3, "terminal-1", "terminal", LifecycleChild{ID: " \u0085child-a\u0085 "})
	terminal.TerminalReason = "completed"

	var result lifecycleTraceSummary
	for _, candidate := range []LifecycleCandidate{snapshot, started, started, terminal} {
		applied, err := lifecycleStore.ApplySubagentLifecycle(ctx, candidate, false)
		require.NoError(t, err)
		result.Dispositions = append(result.Dispositions, applied.Disposition)
	}
	rows, err := lifecycleStore.db.QueryContext(ctx, `SELECT event_id, payload_hash FROM subagent_lifecycle_events ORDER BY router_version ASC`)
	require.NoError(t, err)
	for rows.Next() {
		var row lifecycleHashRow
		require.NoError(t, rows.Scan(&row.EventID, &row.Hash))
		result.Hashes = append(result.Hashes, row)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	replay, err := lifecycleStore.ReplaySubagentLifecycle(ctx, 0)
	require.NoError(t, err)
	result.Replay = lifecycleReplayTrace{
		Mode: replay.Mode, RouterVersion: replay.RouterVersion,
		UpdateCount: len(replay.Updates), ProjectionLen: len(replay.Projection.Agents),
	}
	parent, err := lifecycleStore.LifecycleAgentProjection(ctx, "parent-a")
	require.NoError(t, err)
	result.Parent = lifecycleParentTrace{Version: parent.SubagentVersion, Count: len(parent.Subagents)}

	var terminalReason sql.NullString
	require.NoError(t, lifecycleStore.db.QueryRowContext(ctx, `SELECT terminal_reason FROM subagent_lifecycle_state WHERE parent_agent_id = ? AND subagent_id = ?`, "parent-a", "\u0085child-a\u0085").Scan(&terminalReason))
	require.True(t, terminalReason.Valid)
	require.Equal(t, "completed", terminalReason.String)
	return result
}

func TestLifecycleHashUsesNodePropertyOrder(t *testing.T) {
	candidate := lifecycleSnapshotCandidate(1, "snapshot-1", LifecycleChild{ID: "child", Type: "worker", Description: "description", Status: "running"})
	want := "463d1a0479d999c793028bb9248da8aba6b7565f1f53d1bec101cd83f8f296d7"
	require.Equal(t, want, PayloadHash(candidate))
}
