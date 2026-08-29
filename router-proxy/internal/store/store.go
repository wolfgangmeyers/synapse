// Package store owns the router's durable SQLite state.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	_ "modernc.org/sqlite"
)

const defaultBusyTimeout = 5 * time.Second

// Migration is one entry in the Node router's ordered migration plan.
type Migration struct {
	ID  string
	SQL string
}

// Options controls Store initialization. Nil Clock and IDSource values use the
// production wall clock and a cryptographically random UUID source.
type Options struct {
	Clock       deterministic.Clock
	IDSource    deterministic.IDSource
	BusyTimeout time.Duration
	Migrations  []Migration
}

// RetentionPolicy contains the bounded state cleanup rules implemented by the
// store foundation. Later router layers can add resource-specific cleanup.
type RetentionPolicy struct {
	QueuedMessageAge     time.Duration
	ConversationAge      time.Duration
	ConversationLimit    int
	LifecycleTerminalAge time.Duration
	LifecycleEventAge    time.Duration
	LifecycleProducerAge time.Duration
	LifecycleEventLimit  int
}

// DefaultRetentionPolicy matches the current Node router's durable bounds.
var DefaultRetentionPolicy = RetentionPolicy{
	QueuedMessageAge:     time.Hour,
	ConversationAge:      30 * 24 * time.Hour,
	ConversationLimit:    200,
	LifecycleTerminalAge: 24 * time.Hour,
	LifecycleEventAge:    7 * 24 * time.Hour,
	LifecycleProducerAge: 7 * 24 * time.Hour,
	LifecycleEventLimit:  100_000,
}

// SweepResult reports rows removed by one atomic sweep.
type SweepResult struct {
	QueuedMessages       int
	Leases               int
	GoalControlStates    int
	ConversationMessages int
	LifecycleStates      int
	LifecycleEvents      int
	LifecycleProducers   int
}

// Store is a single-connection SQLite store. The mutex protects the explicit
// BEGIN IMMEDIATE / COMMIT sequences from interleaving operations.
type Store struct {
	db     *sql.DB
	path   string
	clock  deterministic.Clock
	ids    deterministic.IDSource
	mu     sync.Mutex
	closed bool
}

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// DefaultMigrations returns the embedded copy of the current Node migration
// files, in numeric filename order.
func DefaultMigrations() []Migration {
	entries, err := fs.Glob(embeddedMigrations, "migrations/*.sql")
	if err != nil {
		panic(err)
	}
	sort.Strings(entries)
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		sqlText, err := fs.ReadFile(embeddedMigrations, entry)
		if err != nil {
			panic(err)
		}
		migrations = append(migrations, Migration{
			ID:  strings.TrimSuffix(filepath.Base(entry), filepath.Ext(entry)),
			SQL: string(sqlText),
		})
	}
	return migrations
}

// Open opens a persistent SQLite database, configures and verifies its
// durability settings, and applies all pending migrations.
func Open(ctx context.Context, databasePath string, options Options) (*Store, error) {
	if err := validateDatabasePath(databasePath); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	busyTimeout := options.BusyTimeout
	if busyTimeout == 0 {
		busyTimeout = defaultBusyTimeout
	}
	if busyTimeout < 0 {
		return nil, errors.New("busy timeout must not be negative")
	}
	if err := ensureDatabaseParent(databasePath); err != nil {
		return nil, err
	}

	clock := options.Clock
	if clock == nil {
		clock = wallClock{}
	}
	ids := options.IDSource
	if ids == nil {
		ids = randomIDSource{}
	}
	migrations := options.Migrations
	if migrations == nil {
		migrations = DefaultMigrations()
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	// SQLite connection-local PRAGMAs are safe because every operation uses
	// this one connection. This also prevents concurrent writers from creating
	// a second connection with different settings.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	store := &Store{db: db, path: databasePath, clock: clock, ids: ids}
	closeOnError := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(fmt.Errorf("ping SQLite database: %w", err))
	}
	if err := chmodStateFiles(databasePath); err != nil {
		return closeOnError(err)
	}
	if err := store.configureAndVerify(ctx, busyTimeout); err != nil {
		return closeOnError(err)
	}
	if err := store.applyMigrations(ctx, migrations); err != nil {
		return closeOnError(err)
	}
	if err := chmodStateFiles(databasePath); err != nil {
		return closeOnError(err)
	}
	return store, nil
}

// Close closes the database and reapplies state-file permissions first.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	permissionErr := chmodStateFiles(s.path)
	closeErr := s.db.Close()
	if permissionErr != nil {
		return permissionErr
	}
	return closeErr
}

func validateDatabasePath(databasePath string) error {
	if databasePath == "" {
		return errors.New("database path is required")
	}
	if databasePath == ":memory:" {
		return errors.New("database path must identify a persistent SQLite file")
	}
	info, err := os.Stat(databasePath)
	if err == nil && info.IsDir() {
		return errors.New("database path must identify a SQLite file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat database path: %w", err)
	}
	return nil
}

func ensureDatabaseParent(databasePath string) error {
	parent := filepath.Dir(databasePath)
	info, err := os.Stat(parent)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create database parent: %w", err)
		}
		if err := os.Chmod(parent, 0o700); err != nil {
			return fmt.Errorf("secure database parent: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat database parent: %w", err)
	}
	if !info.IsDir() {
		return errors.New("database parent is not a directory")
	}
	// Existing parents are not chmodded: opening a database must not change
	// permissions on an unrelated directory. Newly-created parents are 0700.
	return nil
}

func chmodStateFiles(databasePath string) error {
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("stat SQLite state file %s: %w", path, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("secure SQLite state file %s: %w", path, err)
		}
	}
	return nil
}

func (s *Store) configureAndVerify(ctx context.Context, busyTimeout time.Duration) error {
	busyMS := int64(busyTimeout / time.Millisecond)
	if busyTimeout%time.Millisecond != 0 || busyMS < 0 {
		return errors.New("busy timeout must be a whole number of milliseconds")
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		return fmt.Errorf("set SQLite journal mode: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA synchronous = FULL"); err != nil {
		return fmt.Errorf("set SQLite synchronous mode: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("set SQLite foreign keys: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA busy_timeout = "+strconv.FormatInt(busyMS, 10)); err != nil {
		return fmt.Errorf("set SQLite busy timeout: %w", err)
	}
	var journalMode string
	var synchronous, foreignKeys, configuredBusy int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("verify SQLite journal mode: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("verify SQLite synchronous mode: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("verify SQLite foreign keys: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&configuredBusy); err != nil {
		return fmt.Errorf("verify SQLite busy timeout: %w", err)
	}
	if strings.ToLower(journalMode) != "wal" || synchronous != 2 || foreignKeys != 1 || configuredBusy != busyMS {
		return fmt.Errorf("SQLite connection did not accept required durability settings: journal_mode=%s synchronous=%d foreign_keys=%d busy_timeout=%d", journalMode, synchronous, foreignKeys, configuredBusy)
	}
	return nil
}

func (s *Store) applyMigrations(ctx context.Context, migrations []Migration) error {
	for _, migration := range migrations {
		migration := migration
		if migration.ID == "" {
			return errors.New("migration ID is required")
		}
		if err := s.runImmediate(ctx, func() error {
			if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
				id TEXT PRIMARY KEY,
				applied_at TEXT NOT NULL
			)`); err != nil {
				return err
			}
			var present int
			err := s.db.QueryRowContext(ctx, "SELECT 1 FROM schema_migrations WHERE id = ?", migration.ID).Scan(&present)
			if err == nil {
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := s.db.ExecContext(ctx, migration.SQL); err != nil {
				return fmt.Errorf("apply migration %s: %w", migration.ID, err)
			}
			if migration.ID == "005_message_incarnation" {
				if err := s.backfillMessageIncarnations(ctx); err != nil {
					return err
				}
			}
			if _, err := s.db.ExecContext(ctx,
				"INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)",
				migration.ID, formatTimestamp(s.clock.Now())); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return fmt.Errorf("migration %s failed: %w", migration.ID, err)
		}
	}
	return nil
}

func (s *Store) backfillMessageIncarnations(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages
		WHERE message_incarnation_id IS NULL ORDER BY enqueue_sequence ASC`)
	if err != nil {
		return err
	}
	var messageIDs []string
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			_ = rows.Close()
			return err
		}
		messageIDs = append(messageIDs, messageID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, messageID := range messageIDs {
		incarnation, err := s.ids.Next()
		if err != nil {
			return fmt.Errorf("message %s incarnation: %w", messageID, err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE messages
			SET message_incarnation_id = ? WHERE id = ? AND message_incarnation_id IS NULL`, incarnation, messageID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) runImmediate(ctx context.Context, operation func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	if _, err := s.db.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := operation(); err != nil {
		_, _ = s.db.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	if _, err := s.db.ExecContext(ctx, "COMMIT"); err != nil {
		_, _ = s.db.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	return chmodStateFiles(s.path)
}

// Sweep removes the currently bounded, store-owned resources in one immediate
// transaction. It intentionally leaves HTTP, delivery, reminder, attachment,
// logging, and scheduler behavior to later layers.
func (s *Store) Sweep(ctx context.Context, policy RetentionPolicy) (SweepResult, error) {
	if err := policy.validate(); err != nil {
		return SweepResult{}, err
	}
	var result SweepResult
	if err := s.runImmediate(ctx, func() error {
		now := s.clock.Now()
		nowText := formatTimestamp(now)
		var err error
		result.QueuedMessages, err = s.pruneExpiredQueuedMessages(ctx, nowText)
		if err != nil {
			return err
		}
		result.Leases, err = s.pruneExpiredLeases(ctx, nowText)
		if err != nil {
			return err
		}
		result.GoalControlStates, err = s.pruneExpiredGoalControl(ctx, nowText)
		if err != nil {
			return err
		}
		result.ConversationMessages, err = s.pruneConversation(ctx, now, policy)
		if err != nil {
			return err
		}
		result.LifecycleStates, result.LifecycleEvents, result.LifecycleProducers, err = s.pruneLifecycle(ctx, now, policy)
		return err
	}); err != nil {
		return SweepResult{}, err
	}
	return result, nil
}

func (p RetentionPolicy) validate() error {
	for name, value := range map[string]time.Duration{
		"queued message age":     p.QueuedMessageAge,
		"conversation age":       p.ConversationAge,
		"lifecycle terminal age": p.LifecycleTerminalAge,
		"lifecycle event age":    p.LifecycleEventAge,
		"lifecycle producer age": p.LifecycleProducerAge,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if p.ConversationLimit <= 0 || p.LifecycleEventLimit <= 0 {
		return errors.New("retention limits must be positive")
	}
	return nil
}

func (s *Store) pruneExpiredQueuedMessages(ctx context.Context, now string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages
		WHERE state = 'queued' AND claimed_at IS NULL AND expires_at <= ?
		ORDER BY enqueue_sequence ASC`, now)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE id = ?", id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (s *Store) pruneExpiredLeases(ctx context.Context, now string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM agent_leases
		WHERE expires_at <= ? ORDER BY agent_id ASC`, now)
	if err != nil {
		return 0, err
	}
	var agents []string
	for rows.Next() {
		var agent string
		if err := rows.Scan(&agent); err != nil {
			_ = rows.Close()
			return 0, err
		}
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, agent := range agents {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM agent_leases WHERE agent_id = ?", agent); err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE agents SET disconnected_at = ? WHERE agent_id = ?", now, agent); err != nil {
			return 0, err
		}
	}
	return len(agents), nil
}

func (s *Store) pruneExpiredGoalControl(ctx context.Context, now string) (int, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM goal_control_desired_state WHERE expires_at <= ?", now)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func (s *Store) pruneConversation(ctx context.Context, now time.Time, policy RetentionPolicy) (int, error) {
	cutoff := formatTimestamp(now.Add(-policy.ConversationAge))
	rows, err := s.db.QueryContext(ctx, `SELECT conversation_sequence, participant_a, participant_b, recorded_at
		FROM conversation_messages ORDER BY participant_a ASC, participant_b ASC, conversation_sequence DESC`)
	if err != nil {
		return 0, err
	}
	type row struct {
		sequence       int64
		a, b, recorded string
	}
	var candidates []int64
	counts := make(map[string]int)
	for rows.Next() {
		var current row
		if err := rows.Scan(&current.sequence, &current.a, &current.b, &current.recorded); err != nil {
			_ = rows.Close()
			return 0, err
		}
		pair := current.a + "\x00" + current.b
		if current.recorded < cutoff || counts[pair] >= policy.ConversationLimit {
			candidates = append(candidates, current.sequence)
		} else {
			counts[pair]++
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, sequence := range candidates {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM conversation_messages WHERE conversation_sequence = ?", sequence); err != nil {
			return 0, err
		}
	}
	return len(candidates), nil
}

func (s *Store) pruneLifecycle(ctx context.Context, now time.Time, policy RetentionPolicy) (int, int, int, error) {
	nowText := formatTimestamp(now)
	terminalCutoff := formatTimestamp(now.Add(-policy.LifecycleTerminalAge))
	ledgerCutoff := formatTimestamp(now.Add(-policy.LifecycleEventAge))
	producerCutoff := formatTimestamp(now.Add(-policy.LifecycleProducerAge))
	stateResult, err := s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_state
		WHERE state = 'terminal' AND terminal_at <= ?`, terminalCutoff)
	if err != nil {
		return 0, 0, 0, err
	}
	stateCount, err := stateResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, err
	}
	var threshold sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT router_version FROM subagent_lifecycle_events
		ORDER BY router_version DESC LIMIT 1 OFFSET ?`, policy.LifecycleEventLimit-1).Scan(&threshold); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, err
	}
	var eventResult sql.Result
	if threshold.Valid {
		eventResult, err = s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_events
			WHERE received_at <= ? OR router_version < ?`, ledgerCutoff, threshold.Int64)
	} else {
		eventResult, err = s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_events WHERE received_at <= ?`, ledgerCutoff)
	}
	if err != nil {
		return 0, 0, 0, err
	}
	eventCount, err := eventResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, err
	}
	producerResult, err := s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_producers
		WHERE last_seen_at <= ?
		AND NOT EXISTS (SELECT 1 FROM agent_leases lease
			WHERE lease.agent_id = subagent_lifecycle_producers.parent_agent_id
			AND lease.expires_at > ?)
		AND NOT EXISTS (SELECT 1 FROM subagent_lifecycle_state state
			WHERE state.parent_agent_id = subagent_lifecycle_producers.parent_agent_id)
		AND NOT EXISTS (SELECT 1 FROM subagent_lifecycle_events event
			WHERE event.parent_agent_id = subagent_lifecycle_producers.parent_agent_id)`, producerCutoff, nowText)
	if err != nil {
		return 0, 0, 0, err
	}
	producerCount, err := producerResult.RowsAffected()
	return int(stateCount), int(eventCount), int(producerCount), err
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type randomIDSource struct{}

func (randomIDSource) Next() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}
