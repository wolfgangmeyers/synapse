package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func insertManagedArchiveForReadTest(t *testing.T, store *Store, key, agentID, completedAt string) {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO managed_agent_archives (
		archive_key,agent_id,name,terminal_outcome,completed_at,disposition,total,
		queued_never_attempted,in_flight_or_unknown,completed,released_or_failed)
		VALUES (?,?,?,?,?,'completed',1,0,0,1,0)`, key, agentID, agentID, "terminated", completedAt)
	require.NoError(t, err)
}

func TestManagedArchiveReadsAreClosedAndOrdered(t *testing.T) {
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	ctx := context.Background()
	older := testUUID("0001")
	newerA := testUUID("0002")
	newerB := testUUID("0003")
	insertManagedArchiveForReadTest(t, store, older, "old-agent", formatTimestamp(start.Add(time.Hour)))
	insertManagedArchiveForReadTest(t, store, newerA, "new-agent-a", formatTimestamp(start.Add(2*time.Hour)))
	insertManagedArchiveForReadTest(t, store, newerB, "new-agent-b", formatTimestamp(start.Add(2*time.Hour)))

	archives, err := store.ManagedAgentArchives(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{newerB, newerA, older}, []string{archives[0].ArchiveKey, archives[1].ArchiveKey, archives[2].ArchiveKey})
	require.Equal(t, "archived", archives[0].RecordKind)
	require.Equal(t, "completed", archives[0].Delivery.Disposition)

	archive, err := store.ManagedAgentArchive(ctx, newerA)
	require.NoError(t, err)
	require.Equal(t, newerA, archive.ArchiveKey)
	require.Equal(t, "new-agent-a", archive.Name)

	snapshot, err := store.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Archives, 3)
	require.Empty(t, snapshot.Agents)
}

func TestManagedArchiveLookupReturnsNotFoundAfterStrictExpiry(t *testing.T) {
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	ctx := context.Background()
	key := testUUID("0004")
	insertManagedArchiveForReadTest(t, store, key, "expired-agent", formatTimestamp(start))

	require.NoError(t, clock.Advance(managedArchiveRetention))
	archives, err := store.ManagedAgentArchives(ctx)
	require.NoError(t, err)
	require.Empty(t, archives)
	_, err = store.ManagedAgentArchive(ctx, key)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
