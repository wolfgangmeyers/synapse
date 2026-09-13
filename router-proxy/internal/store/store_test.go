package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
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

func TestOpenSecuresAnExistingDatabaseParentDirectory(t *testing.T) {
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o755))
	store, err := Open(context.Background(), filepath.Join(parent, "router.db"), Options{})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	info, err := os.Stat(parent)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
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
	// Migrations 026 through 043 each record schema_migrations applied_at like
	// every other unapplied migration. The specialized runners consume one
	// migration clock value apiece.
	require.Equal(t, 39, len(clock.Calls()), "only unapplied migrations consume startup clock values")
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

func TestSweepCompletesLinkedRemindersBeforeDeletingExpiredMessages(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	store := openTestStoreWithOptions(t, Options{Clock: deterministic.NewFixedClock(start)})
	past := formatTimestamp(start.Add(-time.Minute))
	require.NoError(t, execTestData(store, `
		INSERT INTO agents (agent_id, first_seen_at, last_connected_at) VALUES ('agent-a', ?, ?);
		INSERT INTO messages (id, sender, recipient, body, created_at, expires_at, state) VALUES ('message-a', 'sender', 'agent-a', 'body', ?, ?, 'queued');
		INSERT INTO reminders (id, target, creator, body, created_at, next_due_at, interval_ms, state, linked_message_id, occurrence_number, attempt_count)
		VALUES ('one-shot', 'agent-a', 'creator', 'one-shot', ?, ?, NULL, 'message_queued', 'message-a', 1, 0);
		INSERT INTO reminders (id, target, creator, body, created_at, next_due_at, interval_ms, state, linked_message_id, occurrence_number, attempt_count)
		VALUES ('recurring', 'agent-a', 'creator', 'recurring', ?, ?, 60000, 'message_queued', 'message-a', 1, 0);`,
		formatTimestamp(start), formatTimestamp(start), formatTimestamp(start), past,
		formatTimestamp(start), formatTimestamp(start), formatTimestamp(start), formatTimestamp(start)))

	_, err := store.Sweep(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
	var count int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM messages WHERE id = 'message-a'").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM reminders WHERE id = 'one-shot'").Scan(&count))
	require.Zero(t, count)
	var state string
	var linkedMessage sql.NullString
	require.NoError(t, store.db.QueryRow("SELECT state, linked_message_id FROM reminders WHERE id = 'recurring'").Scan(&state, &linkedMessage))
	require.Equal(t, "scheduled", state)
	require.False(t, linkedMessage.Valid)
}

func TestSweepExpiresLifecycleChildrenAsProducerExpiredTombstones(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	old := formatTimestamp(start.Add(-8 * 24 * time.Hour))
	epoch := "123e4567-e89b-12d3-a456-426614174000"
	require.NoError(t, execTestData(store, `
		INSERT INTO subagent_lifecycle_producers (
			parent_agent_id, instance_id, parent_session_id, producer_epoch, lease_id,
			highest_client_sequence, last_seen_at, created_at, updated_at)
		VALUES ('parent-a', 'instance-a', 'session-a', ?, 'lease-a', 1, ?, ?, ?);
		INSERT INTO subagent_lifecycle_state (
			parent_agent_id, subagent_id, instance_id, parent_session_id, producer_epoch,
			type, description, state, started_at, updated_at, router_version)
		VALUES ('parent-a', 'child-a', 'instance-a', 'session-a', ?, 'worker', 'description', 'running', ?, ?, 0);`,
		epoch, old, old, old, epoch, old, old))

	result, err := store.Sweep(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 1, result.LifecycleExpired)
	var state, reason, terminalAt string
	require.NoError(t, store.db.QueryRow(`SELECT state, terminal_reason, terminal_at
		FROM subagent_lifecycle_state WHERE parent_agent_id = 'parent-a' AND subagent_id = 'child-a'`).Scan(&state, &reason, &terminalAt))
	require.Equal(t, "terminal", state)
	require.Equal(t, "producer_expired", reason)
	require.Equal(t, formatTimestamp(start), terminalAt)

	var eventID, eventKind, eventState, eventReason, payloadHash, receivedAt string
	var clientSequence, routerVersion int64
	require.NoError(t, store.db.QueryRow(`SELECT event_id, client_sequence, event_kind, state,
		terminal_reason, payload_hash, received_at, router_version
		FROM subagent_lifecycle_events WHERE parent_agent_id = 'parent-a'`).Scan(
		&eventID, &clientSequence, &eventKind, &eventState, &eventReason, &payloadHash, &receivedAt, &routerVersion))
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte("producer_expired:parent-a:child-a:"+formatTimestamp(start)))), eventID)
	require.Equal(t, int64(1000), clientSequence)
	require.Equal(t, "terminal", eventKind)
	require.Equal(t, "terminal", eventState)
	require.Equal(t, "producer_expired", eventReason)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte("producer_expired"))), payloadHash)
	require.Equal(t, formatTimestamp(start), receivedAt)
	require.Equal(t, int64(1), routerVersion)

	require.NoError(t, clock.Advance(25*time.Hour))
	result, err = store.Sweep(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 1, result.LifecycleStates)
	require.NoError(t, clock.Advance(7*24*time.Hour))
	result, err = store.Sweep(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 1, result.LifecycleEvents)
	require.Equal(t, 1, result.LifecycleProducers)
}

func TestSweepRemovesStaleAgentsOnlyUnderNodeReminderConditions(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	store := openTestStoreWithOptions(t, Options{Clock: deterministic.NewFixedClock(start)})
	old := formatTimestamp(start.Add(-2 * time.Hour))
	require.NoError(t, execTestData(store, "INSERT INTO agents (agent_id, first_seen_at) VALUES ('never-delete', ?)", old))
	require.NoError(t, execTestData(store, "INSERT INTO agents (agent_id, first_seen_at) VALUES ('never-scheduled', ?)", old))
	require.NoError(t, execTestData(store, "INSERT INTO agents (agent_id, first_seen_at) VALUES ('never-linked', ?)", old))
	require.NoError(t, execTestData(store, "INSERT INTO agents (agent_id, first_seen_at, disconnected_at) VALUES ('disconnected-delete', ?, ?)", old, old))
	require.NoError(t, execTestData(store, "INSERT INTO agents (agent_id, first_seen_at, last_connected_at, disconnected_at) VALUES ('disconnected-new', ?, ?, ?)", old, formatTimestamp(start), formatTimestamp(start)))
	require.NoError(t, execTestData(store, `INSERT INTO reminders (id, target, creator, body, created_at, next_due_at, interval_ms, state, occurrence_number, attempt_count)
		VALUES ('scheduled', 'never-scheduled', 'creator', 'body', ?, ?, 60000, 'scheduled', 0, 0)`, formatTimestamp(start), formatTimestamp(start)))
	require.NoError(t, execTestData(store, `INSERT INTO reminders (id, target, creator, body, created_at, next_due_at, interval_ms, state, linked_message_id, occurrence_number, attempt_count)
		VALUES ('linked', 'never-linked', 'creator', 'body', ?, ?, NULL, 'message_queued', 'message-a', 0, 0)`, formatTimestamp(start), formatTimestamp(start)))

	result, err := store.Sweep(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 2, result.StaleAgents)
	for _, agent := range []string{"never-delete", "disconnected-delete"} {
		var count int
		require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM agents WHERE agent_id = ?", agent).Scan(&count))
		require.Zero(t, count, agent)
	}
	for _, agent := range []string{"never-scheduled", "never-linked", "disconnected-new"} {
		var count int
		require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM agents WHERE agent_id = ?", agent).Scan(&count))
		require.Equal(t, 1, count, agent)
	}
}

func TestSweepPrunesExpiredQueueLeasesGoalStateAndConversationHistory(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	past := formatTimestamp(start.Add(-2 * time.Hour))
	oldConversation := formatTimestamp(start.Add(-31 * 24 * time.Hour))
	require.NoError(t, execTestData(store, `
		INSERT INTO agents (agent_id, first_seen_at, last_connected_at) VALUES ('agent-a', ?, ?);
		INSERT INTO agent_leases (lease_id, agent_id, instance_id, acquired_at, expires_at) VALUES ('lease-a', 'agent-a', 'instance-a', ?, ?);
		INSERT INTO messages (id, sender, recipient, body, created_at, expires_at, state) VALUES ('message-a', 'sender', 'agent-a', 'body', ?, ?, 'queued');
		INSERT INTO goal_control_desired_state (agent_id, instance_id, revision, config_json, updated_at, expires_at) VALUES ('agent-a', 'instance-a', 1, '{}', ?, ?);
		INSERT INTO conversation_messages (message_id, message_incarnation_id, delivery_message_id, sender, recipient, participant_a, participant_b, body, created_at, recorded_at) VALUES ('c-1', 'i-1', 'message-a', 'a', 'b', 'a', 'b', 'old', ?, ?);
		INSERT INTO conversation_messages (message_id, message_incarnation_id, delivery_message_id, sender, recipient, participant_a, participant_b, body, created_at, recorded_at) VALUES ('c-2', 'i-2', 'message-a', 'a', 'b', 'a', 'b', 'new-1', ?, ?);
		INSERT INTO conversation_messages (message_id, message_incarnation_id, delivery_message_id, sender, recipient, participant_a, participant_b, body, created_at, recorded_at) VALUES ('c-3', 'i-3', 'message-a', 'a', 'b', 'a', 'b', 'new-2', ?, ?);`,
		formatTimestamp(start), formatTimestamp(start), past, past, past, past, past, past, oldConversation, formatTimestamp(start), formatTimestamp(start), formatTimestamp(start)))

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
