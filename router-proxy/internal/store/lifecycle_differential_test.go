package store

import (
	"context"
	"database/sql"
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

func TestNodeAndGoLifecycleTraceMatchesAcrossDuplicatesWhitespaceAndRestart(t *testing.T) {
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
  subagents: [{ id: " child-a ", type: " worker ", description: " child <&> ", status: "running" }],
};
const started = {
  version: "v1", frame_id: "frame-started", type: "subagent_lifecycle_event",
  ...base, client_sequence: 2, event_id: "started-1", lifecycle_event: "started",
  terminal_reason: "ignored",
  subagent: { id: " child-a ", type: " worker ", description: " child <&> ", status: "running" },
};
const terminal = {
  version: "v1", frame_id: "frame-terminal", type: "subagent_lifecycle_event",
  ...base, client_sequence: 3, event_id: "terminal-1", lifecycle_event: "terminal",
  terminal_reason: "completed", subagent: { id: " child-a " },
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
	terminal := lifecycleCandidate(3, "terminal-1", "terminal", LifecycleChild{ID: " child-a "})
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

func applyLifecycleTrace(t *testing.T, lifecycleStore *Store) lifecycleTraceSummary {
	t.Helper()
	ctx := context.Background()
	snapshot := lifecycleSnapshotCandidate(1, "snapshot-1", LifecycleChild{
		ID: " child-a ", Type: " worker ", Description: " child <&> ", Status: "running",
	})
	started := lifecycleCandidate(2, "started-1", "started", snapshot.Subagents[0])
	started.TerminalReason = "ignored"
	terminal := lifecycleCandidate(3, "terminal-1", "terminal", LifecycleChild{ID: " child-a "})
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
	require.NoError(t, lifecycleStore.db.QueryRowContext(ctx, `SELECT terminal_reason FROM subagent_lifecycle_state WHERE parent_agent_id = ? AND subagent_id = ?`, "parent-a", "child-a").Scan(&terminalReason))
	require.True(t, terminalReason.Valid)
	require.Equal(t, "completed", terminalReason.String)
	return result
}

func TestLifecycleHashUsesNodePropertyOrder(t *testing.T) {
	candidate := lifecycleSnapshotCandidate(1, "snapshot-1", LifecycleChild{ID: "child", Type: "worker", Description: "description", Status: "running"})
	want := "463d1a0479d999c793028bb9248da8aba6b7565f1f53d1bec101cd83f8f296d7"
	require.Equal(t, want, PayloadHash(candidate))
}
