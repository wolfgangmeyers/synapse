package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func lifecycleCandidate(sequence int64, eventID, kind string, child LifecycleChild) LifecycleCandidate {
	return LifecycleCandidate{
		ParentAgentID:   "parent-a",
		InstanceID:      "instance-a",
		ParentSessionID: "session-a",
		LeaseID:         "lease-a",
		ProducerEpoch:   "123e4567-e89b-42d3-a456-426614174000",
		ClientSequence:  sequence,
		EventID:         eventID,
		EventKind:       kind,
		Subagent:        child,
	}
}

func lifecycleSnapshotCandidate(sequence int64, eventID string, children ...LifecycleChild) LifecycleCandidate {
	candidate := lifecycleCandidate(sequence, eventID, "", LifecycleChild{})
	candidate.Snapshot = true
	candidate.Subagents = children
	return candidate
}

func TestApplySubagentLifecycleSuppressesDuplicateUpdatesAndReplaysPersistedVersions(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	path := t.TempDir() + "/router.db"
	store, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)

	first := lifecycleSnapshotCandidate(1, "snapshot-1",
		LifecycleChild{ID: "child-a", Type: "worker", Description: "first", Status: "running"},
		LifecycleChild{ID: "child-b", Type: "worker", Description: "second", Status: "queued"},
	)
	result, err := store.ApplySubagentLifecycle(ctx, first, false)
	require.NoError(t, err)
	require.Equal(t, "applied", result.Disposition)
	require.Equal(t, int64(2), result.RouterVersion)
	require.Len(t, result.Snapshot.Subagents, 2)

	duplicate, err := store.ApplySubagentLifecycle(ctx, first, false)
	require.NoError(t, err)
	require.Equal(t, "duplicate", duplicate.Disposition)
	require.Equal(t, int64(1), duplicate.AppliedVersion)
	require.Equal(t, int64(2), duplicate.RouterVersion)

	changedSequence, err := store.ApplySubagentLifecycle(ctx, lifecycleSnapshotCandidate(1, "snapshot-2", first.Subagents...), false)
	require.NoError(t, err)
	require.Equal(t, "conflict", changedSequence.Disposition)
	require.Equal(t, int64(2), changedSequence.RouterVersion)

	terminal := lifecycleCandidate(2, "terminal-1", "terminal", LifecycleChild{ID: "child-a"})
	terminal.TerminalReason = "completed"
	result, err = store.ApplySubagentLifecycle(ctx, terminal, false)
	require.NoError(t, err)
	require.Equal(t, "applied", result.Disposition)
	require.Equal(t, int64(3), result.RouterVersion)
	require.Len(t, result.Snapshot.Subagents, 1)
	require.Equal(t, "child-b", result.Snapshot.Subagents[0]["id"])

	replay, err := store.ReplaySubagentLifecycle(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "replay", replay.Mode)
	require.Equal(t, int64(3), replay.RouterVersion)
	require.Len(t, replay.Projection.Agents, 1)
	require.Equal(t, "parent-a", replay.Projection.Agents[0].AgentID)
	require.Equal(t, int64(2), replay.Projection.Agents[0].SubagentVersion)
	require.Equal(t, []LifecycleChild{{ID: "child-b", Type: "worker", Description: "second", Status: "queued"}}, replay.Projection.Agents[0].Subagents)
	require.Len(t, replay.Updates, 2)
	require.Equal(t, int64(2), replay.Updates[0].RouterVersion)
	require.Equal(t, "running", replay.Updates[0].State)
	require.Equal(t, int64(3), replay.Updates[1].RouterVersion)
	require.Equal(t, "terminal", replay.Updates[1].State)
	require.True(t, replay.Updates[1].HasReason)
	require.Equal(t, "completed", replay.Updates[1].TerminalReason)

	require.NoError(t, store.Close())
	store, err = Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	persisted, err := store.ReplaySubagentLifecycle(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, replay, persisted)
}

func TestStartedTerminalReasonIsDiscardedAndDoesNotChangeDuplicateHash(t *testing.T) {
	store := openTestStoreWithOptions(t, Options{})
	ctx := context.Background()
	started := lifecycleCandidate(2, "started-1", "started", LifecycleChild{ID: "child-a", Type: "worker"})
	started.TerminalReason = "ignored"
	result, err := store.ApplySubagentLifecycle(ctx, started, false)
	require.NoError(t, err)
	require.Equal(t, "conflict", result.Disposition)

	// A snapshot establishes the producer before the event, matching Node's
	// lifecycle ordering. The started event then supplies the first child row.
	_, err = store.ApplySubagentLifecycle(ctx, lifecycleSnapshotCandidate(1, "snapshot-1"), false)
	require.NoError(t, err)
	result, err = store.ApplySubagentLifecycle(ctx, started, false)
	require.NoError(t, err)
	require.Equal(t, "applied", result.Disposition)

	started.TerminalReason = ""
	result, err = store.ApplySubagentLifecycle(ctx, started, false)
	require.NoError(t, err)
	require.Equal(t, "duplicate", result.Disposition)

	var terminalReason sql.NullString
	require.NoError(t, store.db.QueryRow(`SELECT terminal_reason FROM subagent_lifecycle_events WHERE event_id = ?`, "started-1").Scan(&terminalReason))
	require.False(t, terminalReason.Valid)
}

func TestReplaySubagentLifecycleClosesEventRowsBeforeReadingProjection(t *testing.T) {
	store := openTestStoreWithOptions(t, Options{})
	ctx := context.Background()
	_, err := store.ApplySubagentLifecycle(ctx, lifecycleSnapshotCandidate(1, "snapshot-1", LifecycleChild{ID: "child-a"}), false)
	require.NoError(t, err)

	replayContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	replay, err := store.ReplaySubagentLifecycle(replayContext, 0)
	require.NoError(t, err)
	require.Equal(t, "replay", replay.Mode)
	require.Len(t, replay.Updates, 1)
	require.Len(t, replay.Projection.Agents, 1)
}

func TestApplySubagentLifecycleUsesNoopForRepeatedStartedMetadata(t *testing.T) {
	store := openTestStoreWithOptions(t, Options{})
	ctx := context.Background()
	started := lifecycleCandidate(2, "started-1", "started", LifecycleChild{ID: "child-a", Type: "worker", Description: "same", Status: "running"})

	result, err := store.ApplySubagentLifecycle(ctx, lifecycleSnapshotCandidate(1, "snapshot-1", started.Subagent), false)
	require.NoError(t, err)
	require.Equal(t, "applied", result.Disposition)

	result, err = store.ApplySubagentLifecycle(ctx, started, false)
	require.NoError(t, err)
	require.Equal(t, "accepted_noop", result.Disposition)
	require.Equal(t, int64(1), result.RouterVersion)

	var count int
	require.NoError(t, store.db.QueryRow("SELECT count FROM subagent_lifecycle_diagnostics WHERE parent_agent_id = ? AND reason_code = ?", "parent-a", "accepted_noop").Scan(&count))
	require.Equal(t, 1, count)
}
