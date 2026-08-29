package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestOpenRejectsNonPersistentPaths(t *testing.T) {
	ctx := context.Background()
	_, err := Open(ctx, "", Options{})
	require.EqualError(t, err, "database path is required")
	_, err = Open(ctx, ":memory:", Options{})
	require.EqualError(t, err, "database path must identify a persistent SQLite file")

	_, err = Open(ctx, t.TempDir(), Options{})
	require.EqualError(t, err, "database path must identify a SQLite file")
}

func TestOpenConfiguresOneDurableConnectionAndAppliesCurrentMigrations(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "state", "router.db")
	store, err := Open(context.Background(), databasePath, Options{
		BusyTimeout: 2300 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	require.Equal(t, 1, store.db.Stats().MaxOpenConnections)
	var journal string
	var synchronous, foreignKeys, busyTimeout int64
	require.NoError(t, store.db.QueryRow("PRAGMA journal_mode").Scan(&journal))
	require.NoError(t, store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous))
	require.NoError(t, store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys))
	require.NoError(t, store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout))
	require.Equal(t, "wal", journal)
	require.Equal(t, int64(2), synchronous)
	require.Equal(t, int64(1), foreignKeys)
	require.Equal(t, int64(2300), busyTimeout)

	var migrationCount int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount))
	require.Equal(t, len(DefaultMigrations()), migrationCount)
	var promptTemplates, sentinelOccurrences int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'prompt_templates'").Scan(&promptTemplates))
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sentinel_occurrences'").Scan(&sentinelOccurrences))
	require.Equal(t, 1, promptTemplates)
	require.Equal(t, 0, sentinelOccurrences, "migration 012 must match the Node schema cleanup")

	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), path)
	}
	parentInfo, err := os.Stat(filepath.Dir(databasePath))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), parentInfo.Mode().Perm())
}

func TestMigration005BackfillsInSequenceBeforeAppliedAtClock(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	first, err := Open(ctx, databasePath, Options{
		Clock:      clock,
		IDSource:   deterministic.NewSequenceIDSource(),
		Migrations: DefaultMigrations()[:4],
	})
	require.NoError(t, err)
	require.NoError(t, first.Close())

	db, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agents (agent_id, first_seen_at) VALUES ('agent-a', ?);
		INSERT INTO messages (id, sender, recipient, body, created_at, expires_at, state)
		VALUES ('message-a', 'sender', 'agent-a', 'body', ?, ?, 'queued');`,
		formatTimestamp(start), formatTimestamp(start), formatTimestamp(start.Add(time.Hour)))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	clock.ResetCalls()
	ids := deterministic.NewSequenceIDSource("incarnation-a")
	second, err := Open(ctx, databasePath, Options{Clock: clock, IDSource: ids})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	var incarnation, appliedAt string
	require.NoError(t, second.db.QueryRow("SELECT message_incarnation_id FROM messages WHERE id = 'message-a'").Scan(&incarnation))
	require.NoError(t, second.db.QueryRow("SELECT applied_at FROM schema_migrations WHERE id = '005_message_incarnation'").Scan(&appliedAt))
	require.Equal(t, "incarnation-a", incarnation)
	require.Equal(t, formatTimestamp(start), appliedAt)
	require.Equal(t, 16, len(clock.Calls()), "only unapplied migrations consume startup clock values")
	require.Equal(t, []string{"incarnation-a"}, ids.Calls())
}

func TestMigration005RollsBackWhenIDSourceIsExhausted(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	first, err := Open(ctx, databasePath, Options{Clock: clock, Migrations: DefaultMigrations()[:4]})
	require.NoError(t, err)
	require.NoError(t, first.Close())

	db, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agents (agent_id, first_seen_at) VALUES ('agent-a', '2026-08-29T12:00:00.000Z');
		INSERT INTO messages (id, sender, recipient, body, created_at, expires_at, state)
		VALUES ('message-a', 'sender', 'agent-a', 'body', '2026-08-29T12:00:00.000Z', '2026-08-29T13:00:00.000Z', 'queued');`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = Open(ctx, databasePath, Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource()})
	require.Error(t, err)
	db, err = sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer db.Close()
	var migrations int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrations))
	require.Equal(t, 4, migrations)
	var columns int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'message_incarnation_id'").Scan(&columns))
	require.Equal(t, 0, columns, "the failed migration's ALTER TABLE must roll back")
}

func TestRunImmediateRollsBackTheWholeOperation(t *testing.T) {
	store := openTestStore(t)
	err := store.runImmediate(context.Background(), func() error {
		_, err := store.db.Exec("INSERT INTO agents (agent_id, first_seen_at) VALUES ('agent-a', '2026-08-29T12:00:00.000Z')")
		require.NoError(t, err)
		return errors.New("abort")
	})
	require.EqualError(t, err, "abort")
	var count int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM agents").Scan(&count))
	require.Zero(t, count)
}

func TestSweepPrunesExpiredQueueLeasesGoalStateAndConversationHistory(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	past := formatTimestamp(start.Add(-2 * time.Hour))
	oldConversation := formatTimestamp(start.Add(-31 * 24 * time.Hour))
	require.NoError(t, execTestData(store, `
		INSERT INTO agents (agent_id, first_seen_at) VALUES ('agent-a', ?);
		INSERT INTO agent_leases (lease_id, agent_id, instance_id, acquired_at, expires_at) VALUES ('lease-a', 'agent-a', 'instance-a', ?, ?);
		INSERT INTO messages (id, sender, recipient, body, created_at, expires_at, state) VALUES ('message-a', 'sender', 'agent-a', 'body', ?, ?, 'queued');
		INSERT INTO goal_control_desired_state (agent_id, instance_id, revision, config_json, updated_at, expires_at) VALUES ('agent-a', 'instance-a', 1, '{}', ?, ?);
		INSERT INTO conversation_messages (message_id, message_incarnation_id, delivery_message_id, sender, recipient, participant_a, participant_b, body, created_at, recorded_at) VALUES ('c-1', 'i-1', 'message-a', 'a', 'b', 'a', 'b', 'old', ?, ?);
		INSERT INTO conversation_messages (message_id, message_incarnation_id, delivery_message_id, sender, recipient, participant_a, participant_b, body, created_at, recorded_at) VALUES ('c-2', 'i-2', 'message-a', 'a', 'b', 'a', 'b', 'new-1', ?, ?);
		INSERT INTO conversation_messages (message_id, message_incarnation_id, delivery_message_id, sender, recipient, participant_a, participant_b, body, created_at, recorded_at) VALUES ('c-3', 'i-3', 'message-a', 'a', 'b', 'a', 'b', 'new-2', ?, ?);`,
		formatTimestamp(start), past, past, past, past, past, past, past, oldConversation, formatTimestamp(start), formatTimestamp(start), formatTimestamp(start)))

	result, err := store.Sweep(context.Background(), RetentionPolicy{
		QueuedMessageAge:     time.Hour,
		ConversationAge:      30 * 24 * time.Hour,
		ConversationLimit:    2,
		LifecycleTerminalAge: 24 * time.Hour,
		LifecycleEventAge:    7 * 24 * time.Hour,
		LifecycleProducerAge: 7 * 24 * time.Hour,
		LifecycleEventLimit:  100_000,
	})
	require.NoError(t, err)
	require.Equal(t, SweepResult{QueuedMessages: 1, Leases: 1, GoalControlStates: 1, ConversationMessages: 1}, result)
	var count int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM agent_leases").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM goal_control_desired_state").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM conversation_messages").Scan(&count))
	require.Equal(t, 2, count)
	var disconnected string
	require.NoError(t, store.db.QueryRow("SELECT disconnected_at FROM agents WHERE agent_id = 'agent-a'").Scan(&disconnected))
	require.Equal(t, formatTimestamp(start), disconnected)
}

func openTestStore(t *testing.T) *Store {
	return openTestStoreWithOptions(t, Options{})
}

func openTestStoreWithOptions(t *testing.T, options Options) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "router.db")
	store, err := Open(context.Background(), path, options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func execTestData(store *Store, query string, args ...interface{}) error {
	_, err := store.db.Exec(query, args...)
	return err
}
