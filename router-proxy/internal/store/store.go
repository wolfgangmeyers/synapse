// Package store owns the router's durable SQLite state.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	Clock             deterministic.Clock
	IDSource          deterministic.IDSource
	BusyTimeout       time.Duration
	Migrations        []Migration
	MaxBodyBytes      int
	MaxQueuedMessages int
	MaxQueuedBytes    int
	MaxBatchBodyChars int
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
type ExpiredLease struct {
	AgentID    string
	InstanceID string
	LeaseID    string
}

type LifecycleUpdate struct {
	ParentAgentID string
	RouterVersion int64
}

type SweepResult struct {
	StaleAgents          int
	LifecycleExpired     int
	QueuedMessages       int
	Leases               int
	ExpiredLeases        []ExpiredLease
	GoalControlStates    int
	ConversationMessages int
	ReceiptsPruned       int
	LifecycleStates      int
	LifecycleEvents      int
	LifecycleProducers   int
	ManagedOperations    int
	ManagedControls      int
	ManagedAgents        int
	LifecycleUpdates     []LifecycleUpdate
}

type LifecycleSnapshot struct {
	Subagents []map[string]any
}

// Store is a single-connection SQLite store. The mutex protects the explicit
// BEGIN IMMEDIATE / COMMIT sequences from interleaving operations.
type Store struct {
	db                   *sql.DB
	path                 string
	clock                deterministic.Clock
	ids                  deterministic.IDSource
	maxBodyBytes         int
	maxQueuedMessages    int
	maxQueuedBytes       int
	maxBatchBodyChars    int
	reminderIncarnations map[string]string
	migrations           []Migration
	receiptLifecycle     bool
	// managed027Fault, managedRestartAllFault, managedGraphFault, and
	// managedTerminationFault are test-only failure seams. They keep rollback
	// tests on the real single SQLite connection.
	managed027Fault               func(string) error
	managedRestartAllFault        func(string) error
	managedGraphFault             func(string) error
	managedTerminationFault       func(string) error
	managedBridgeFault            func(string) error
	embeddedGoalContinuationFault func(string) error // test-only rollback seam
	// These process-local bounds reset on restart; reconciliation is idempotent.
	managedParentActivityReconcileMu        sync.Mutex
	managedParentActivityReconcileCursor    string
	managedParentActivityReconcileHighWater string
	mu                                      sync.Mutex
	closed                                  bool
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
	closeOnError := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	maxBodyBytes := options.MaxBodyBytes
	if maxBodyBytes == 0 {
		maxBodyBytes = 4 * 1024
	}
	if maxBodyBytes <= 0 || maxBodyBytes > 64*1024 {
		return closeOnError(errors.New("maxBodyBytes must be a positive safe integer no greater than 65536"))
	}
	maxQueuedMessages := options.MaxQueuedMessages
	if maxQueuedMessages == 0 {
		maxQueuedMessages = 15
	}
	maxQueuedBytes := options.MaxQueuedBytes
	if maxQueuedBytes == 0 {
		maxQueuedBytes = 256 * 1024
	}
	maxBatchBodyChars := options.MaxBatchBodyChars
	if maxBatchBodyChars == 0 {
		maxBatchBodyChars = 4096
	}
	if maxQueuedMessages < 1 || maxQueuedBytes < 1 || maxBatchBodyChars < 0 {
		return closeOnError(errors.New("queue limits must be positive"))
	}
	store := &Store{db: db, path: databasePath, clock: clock, ids: ids,
		maxBodyBytes: maxBodyBytes, maxQueuedMessages: maxQueuedMessages,
		maxQueuedBytes: maxQueuedBytes, maxBatchBodyChars: maxBatchBodyChars,
		reminderIncarnations: make(map[string]string), migrations: append([]Migration(nil), migrations...)}
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
	if err := store.SyncNativeToolDefaults(ctx); err != nil {
		return closeOnError(err)
	}
	if err := store.backfillPromptTemplateSlugs(ctx); err != nil {
		return closeOnError(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE id='082_conversation_send_receipt_lifecycle')`).Scan(&store.receiptLifecycle); err != nil {
		return closeOnError(err)
	}
	if present, err := store.managedSubagentSchemaPresent(ctx); err != nil {
		return closeOnError(err)
	} else if present {
		if err := store.reconcileManagedSubagents(ctx); err != nil {
			return closeOnError(err)
		}
	}
	if err := store.backfillClosedManagedHistoryIncarnations(ctx); err != nil {
		return closeOnError(err)
	}
	if err := store.pruneManagedArchives(ctx); err != nil {
		return closeOnError(err)
	}
	if err := chmodStateFiles(databasePath); err != nil {
		return closeOnError(err)
	}
	return store, nil
}

// backfillClosedManagedHistoryIncarnations runs after all migrations because
// focused old-schema migration fixtures intentionally omit the Complete table.
// It marks already-removed agent names audit-only without changing their rows.
func (s *Store) backfillClosedManagedHistoryIncarnations(ctx context.Context) error {
	var historyExists, incarnationsExist int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_complete_history'`).Scan(&historyExists); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_history_incarnations'`).Scan(&incarnationsExist); err != nil {
		return err
	}
	if historyExists == 0 || incarnationsExist == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_history_incarnations(incarnation_id,agent_id,start_sequence,created_at,closed_at)
		SELECT 'legacy-closed:' || history.agent_id, history.agent_id, 0, MIN(history.occurred_at), MIN(history.occurred_at)
		FROM managed_complete_history AS history
		WHERE history.agent_id IS NOT NULL
		  AND history.agent_id NOT IN (SELECT agent_id FROM managed_agents)
		  AND NOT EXISTS (SELECT 1 FROM managed_history_incarnations AS incarnation WHERE incarnation.agent_id=history.agent_id)
		GROUP BY history.agent_id`)
	return err
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
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("secure database parent: %w", err)
	}
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

func (s *Store) migrationPending(ctx context.Context, id string) (bool, error) {
	var present int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE id=?`, id).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

// preflightManagedLifecycleOrphansBefore057 runs immediately before migration
// 057 in Store.Open's migration pass. At that point all 057 prerequisites,
// including termination diagnostics, exist, and repair still precedes 057's
// foreign-key check.
func (s *Store) preflightManagedLifecycleOrphansBefore057(ctx context.Context) error {
	var lifecycleTable int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_lifecycle_actions'`).Scan(&lifecycleTable); err != nil {
		return err
	}
	if lifecycleTable == 0 {
		return nil
	}
	pending, err := s.migrationPending(ctx, managedRestartRetryMigration057ID)
	if err != nil {
		return err
	}
	if !pending {
		return nil
	}
	return s.runImmediate(ctx, func() error {
		return s.repairManagedLifecycleForeignKeys059(ctx)
	})
}

func (s *Store) applyMigrations(ctx context.Context, migrations []Migration) error {
	for _, migration := range migrations {
		migration := migration
		if migration.ID == "" {
			return errors.New("migration ID is required")
		}
		if migration.ID == "088_subagent_cascade" {
			pending, err := s.migrationPending(ctx, migration.ID)
			if err != nil {
				return fmt.Errorf("migration %s preflight check failed: %w", migration.ID, err)
			}
			if !pending {
				continue
			}
			hasParent, err := managedAgentColumnTx(ctx, s.db, "parent_agent_id")
			if err == nil && hasParent {
				if err := s.runImmediate(ctx, func() error {
					_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(id, applied_at) VALUES (?, ?)`, migration.ID, formatTimestamp(s.clock.Now()))
					return err
				}); err != nil {
					return fmt.Errorf("migration %s mark applied failed: %w", migration.ID, err)
				}
				continue
			}
		}
		if migration.ID == "104_widen_managed_agents_lifecycle_state" {
			if err := s.applyWidenManagedAgentsLifecycleState104(ctx, migration); err != nil {
				return fmt.Errorf("migration %s failed: %w", migration.ID, err)
			}
			continue
		}
		if migration.ID == "085_embedded_subagent_role" {
			if err := s.applyEmbeddedSubagentRoleMigration085(ctx, migration); err != nil {
				return fmt.Errorf("migration %s failed: %w", migration.ID, err)
			}
			continue
		}
		if migration.ID == "027_managed_control_requests" {
			if err := s.applyManagedControlMigration027(ctx, migration); err != nil {
				return fmt.Errorf("migration %s failed: %w", migration.ID, err)
			}
			continue
		}
		if migration.ID == "028_managed_restart_all_batch_references" {
			if err := s.applyManagedRestartAllBatchReferencesMigration028(ctx, migration); err != nil {
				return fmt.Errorf("migration %s failed: %w", migration.ID, err)
			}
			continue
		}
		if migration.ID == managedRestartRetryMigration057ID {
			// This is the only point where every 057 prerequisite table exists
			// and the repair still precedes 057's foreign-key check.
			pending, err := s.migrationPending(ctx, migration.ID)
			if err != nil {
				return fmt.Errorf("migration %s preflight check failed: %w", migration.ID, err)
			}
			if pending {
				if err := s.preflightManagedLifecycleOrphansBefore057(ctx); err != nil {
					return fmt.Errorf("migration %s orphan preflight failed: %w", migration.ID, err)
				}
			}
			if err := s.applyManagedRestartRetryMigration057(ctx, migration); err != nil {
				return fmt.Errorf("migration %s failed: %w", migration.ID, err)
			}
			continue
		}
		if migration.ID == managedLifecycleOrphanRepairMigration059ID {
			if err := s.applyManagedLifecycleOrphanRepairMigration059(ctx, migration); err != nil {
				return fmt.Errorf("migration %s failed: %w", migration.ID, err)
			}
			continue
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
			if migration.ID == "082_conversation_send_receipt_lifecycle" {
				accepted := s.clock.Now().UTC().Truncate(time.Millisecond)
				if _, err := s.db.ExecContext(ctx, `UPDATE conversation_send_receipts SET accepted_at=?, deadline_at=? WHERE accepted_at IS NULL`, formatTimestamp(accepted), formatTimestamp(accepted.Add(conversationReceiptHorizon))); err != nil {
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

// applyManagedControlMigration027 is intentionally separate from runImmediate.
// The managed parent has four populated foreign-key children after migration
// 026, and SQLite only permits its FK enforcement switch outside a transaction.
// Store.Open has one connection and this mutex keeps every statement below on it.
func (s *Store) applyManagedControlMigration027(ctx context.Context, migration Migration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		id TEXT PRIMARY KEY, applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	var present int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE id=?`, migration.ID).Scan(&present)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var foreignKeys, legacyAlter int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		if err != nil {
			return err
		}
		return errors.New("migration 027 requires foreign_keys=1")
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA legacy_alter_table`).Scan(&legacyAlter); err != nil || legacyAlter != 0 {
		if err != nil {
			return err
		}
		return errors.New("migration 027 requires legacy_alter_table=0")
	}
	for _, id := range []string{"024_managed_readiness_sources", "025_managed_restart_execution", "026_managed_direct_delivery"} {
		var found int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE id=?`, id).Scan(&found); err != nil {
			return fmt.Errorf("migration 027 prerequisite %s: %w", id, err)
		}
	}
	children := []string{"managed_readiness_sources", "managed_restart_transitions", "managed_direct_delivery_attempts", "managed_direct_delivery_diagnostics"}
	before := make(map[string]managed027ChildSnapshot, len(children))
	for _, child := range children {
		snapshot, err := captureManaged027Child(ctx, s.db, child)
		if err != nil {
			return err
		}
		before[child] = snapshot
	}
	foreignKeysDisabled := false
	restoreForeignKeys := func() error {
		if !foreignKeysDisabled {
			return nil
		}
		if _, err := s.db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
			return err
		}
		var value int
		if err := s.db.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&value); err != nil {
			return err
		}
		if value != 1 {
			return errors.New("migration 027 could not restore foreign keys")
		}
		foreignKeysDisabled = false
		return nil
	}
	fail := func(cause error, transactionOpen bool) error {
		if transactionOpen {
			_, _ = s.db.ExecContext(context.Background(), `ROLLBACK`)
		}
		if restoreErr := restoreForeignKeys(); restoreErr != nil {
			return fmt.Errorf("%w; restore foreign keys: %v", cause, restoreErr)
		}
		return cause
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	foreignKeysDisabled = true
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		if err != nil {
			return fail(err, false)
		}
		return fail(errors.New("migration 027 could not disable foreign keys"), false)
	}
	if err := s.injectManaged027Fault("after_foreign_keys_off"); err != nil {
		return fail(err, false)
	}
	var unmatchedStarting int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE lifecycle_state='starting'`).Scan(&unmatchedStarting); err != nil {
		return fail(err, false)
	}
	if unmatchedStarting != 0 {
		return fail(errors.New("migration 027 refuses unmatched pre-027 starting launches"), false)
	}
	if _, err := s.db.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fail(err, false)
	}
	if _, err := s.db.ExecContext(ctx, migration.SQL); err != nil {
		return fail(fmt.Errorf("apply migration DDL: %w", err), true)
	}
	for _, statement := range []string{
		`ALTER TABLE managed_http_operations ADD COLUMN terminal_code TEXT`,
		`ALTER TABLE managed_http_operations ADD COLUMN terminal_message TEXT`,
		managedAgents027DDL,
		`CREATE TEMP TABLE managed_agents_027_snapshot AS SELECT ` + managedAgents027Columns + ` FROM managed_agents`,
		`INSERT INTO managed_agents_027_new (` + managedAgents027Columns + `,launch_operation_id) SELECT ` + managedAgents027Columns + `,NULL FROM managed_agents`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fail(err, true)
		}
	}
	var oldCount, newCount, leftOnly, rightOnly, nonNullLinks int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents_027_snapshot`).Scan(&oldCount); err != nil {
		return fail(err, true)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents_027_new`).Scan(&newCount); err != nil {
		return fail(err, true)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT `+managedAgents027Columns+` FROM managed_agents_027_snapshot EXCEPT SELECT `+managedAgents027Columns+` FROM managed_agents_027_new)`).Scan(&leftOnly); err != nil {
		return fail(err, true)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT `+managedAgents027Columns+` FROM managed_agents_027_new EXCEPT SELECT `+managedAgents027Columns+` FROM managed_agents_027_snapshot)`).Scan(&rightOnly); err != nil {
		return fail(err, true)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents_027_new WHERE launch_operation_id IS NOT NULL`).Scan(&nonNullLinks); err != nil {
		return fail(err, true)
	}
	if oldCount != newCount || leftOnly != 0 || rightOnly != 0 || nonNullLinks != 0 {
		return fail(errors.New("migration 027 parent copy parity failed"), true)
	}
	if err := s.injectManaged027Fault("after_parent_copy"); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE managed_agents`); err != nil {
		return fail(err, true)
	}
	if err := s.injectManaged027Fault("after_parent_drop"); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE managed_agents_027_new RENAME TO managed_agents`); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX managed_agents_slot_idx ON managed_agents (slot_held, lifecycle_state)`); err != nil {
		return fail(err, true)
	}
	if err := backfillManaged027LegacyActions(ctx, s.db); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE UNIQUE INDEX managed_lifecycle_one_active_lane ON managed_lifecycle_actions(agent_id) WHERE agent_id IS NOT NULL AND state IN ('admitted','awaiting_settle','stopping_source','starting_target','awaiting_target_ready','old_artifact_cleanup','terminating','cleanup')`); err != nil {
		return fail(err, true)
	}
	var foreignProblems int
	if err := s.injectManaged027Fault("pre_commit_foreign_key_check"); err != nil {
		return fail(err, true)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems); err != nil || foreignProblems != 0 {
		if err != nil {
			return fail(err, true)
		}
		return fail(errors.New("migration 027 foreign key check failed"), true)
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		if err != nil {
			return fail(err, true)
		}
		return fail(errors.New("migration 027 integrity check failed"), true)
	}
	for _, child := range children {
		after, err := captureManaged027Child(ctx, s.db, child)
		if err != nil {
			return fail(err, true)
		}
		if !before[child].same(after) {
			return fail(fmt.Errorf("migration 027 changed child %s", child), true)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (id, applied_at) VALUES (?,?)`, migration.ID, formatTimestamp(s.clock.Now())); err != nil {
		return fail(err, true)
	}
	if err := s.injectManaged027Fault("after_migration_record"); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `COMMIT`); err != nil {
		return fail(err, true)
	}
	if err := restoreForeignKeys(); err != nil {
		s.closed = true
		_ = s.db.Close()
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems); err != nil || foreignProblems != 0 {
		s.closed = true
		_ = s.db.Close()
		if err != nil {
			return err
		}
		return errors.New("migration 027 post-commit foreign key check failed")
	}
	return chmodStateFiles(s.path)
}

// applyManagedRestartAllBatchReferencesMigration028 rebuilds only the batch
// reference table. The transaction verifies the copied rows before recording
// the migration, so a failed upgrade leaves the historical 027 table intact.
func (s *Store) applyManagedRestartAllBatchReferencesMigration028(ctx context.Context, migration Migration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		id TEXT PRIMARY KEY, applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	var present int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE id=?`, migration.ID).Scan(&present); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// TEMP tables survive only this single SQLite connection. Clear an abandoned
	// test fixture before creating the snapshot used for two-way parity checks.
	if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS temp.managed_lifecycle_batch_items_028_snapshot`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	rollback := func(cause error) error {
		_, _ = s.db.ExecContext(context.Background(), `ROLLBACK`)
		return cause
	}
	if _, err := s.db.ExecContext(ctx, migration.SQL); err != nil {
		return rollback(err)
	}
	columns := `parent_action_id,snapshot_position,agent_id,status,child_action_id,child_operation_id,error_code,error_message,created_at`
	var oldCount, newCount, leftOnly, rightOnly, foreignProblems int
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM temp.managed_lifecycle_batch_items_028_snapshot`: &oldCount,
		`SELECT COUNT(*) FROM managed_lifecycle_batch_items`:                   &newCount,
		`SELECT COUNT(*) FROM (SELECT ` + columns + ` FROM temp.managed_lifecycle_batch_items_028_snapshot EXCEPT SELECT ` + columns + ` FROM managed_lifecycle_batch_items)`: &leftOnly,
		`SELECT COUNT(*) FROM (SELECT ` + columns + ` FROM managed_lifecycle_batch_items EXCEPT SELECT ` + columns + ` FROM temp.managed_lifecycle_batch_items_028_snapshot)`: &rightOnly,
		`SELECT COUNT(*) FROM pragma_foreign_key_check`: &foreignProblems,
	} {
		if err := s.db.QueryRowContext(ctx, query).Scan(destination); err != nil {
			return rollback(err)
		}
	}
	if oldCount != newCount || leftOnly != 0 || rightOnly != 0 || foreignProblems != 0 {
		return rollback(errors.New("migration 028 batch reference parity failed"))
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return rollback(err)
	}
	if integrity != "ok" {
		return rollback(errors.New("migration 028 integrity check failed"))
	}
	if s.managedRestartAllFault != nil {
		if err := s.managedRestartAllFault("migration_028_before_record"); err != nil {
			return rollback(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (id, applied_at) VALUES (?,?)`, migration.ID, formatTimestamp(s.clock.Now())); err != nil {
		return rollback(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE temp.managed_lifecycle_batch_items_028_snapshot`); err != nil {
		return rollback(err)
	}
	if _, err := s.db.ExecContext(ctx, `COMMIT`); err != nil {
		return rollback(err)
	}
	return chmodStateFiles(s.path)
}

func (s *Store) injectManaged027Fault(stage string) error {
	if s.managed027Fault == nil {
		return nil
	}
	return s.managed027Fault(stage)
}

type managed027ChildSnapshot struct {
	tableSQL string
	indexes  string
	count    int
	foreigns string
}

func (s managed027ChildSnapshot) same(other managed027ChildSnapshot) bool {
	return s.tableSQL == other.tableSQL && s.indexes == other.indexes && s.count == other.count && s.foreigns == other.foreigns
}

func captureManaged027Child(ctx context.Context, db *sql.DB, table string) (managed027ChildSnapshot, error) {
	allowed := map[string]bool{"managed_readiness_sources": true, "managed_restart_transitions": true, "managed_direct_delivery_attempts": true, "managed_direct_delivery_diagnostics": true}
	if !allowed[table] {
		return managed027ChildSnapshot{}, errors.New("unknown migration 027 child")
	}
	var result managed027ChildSnapshot
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&result.tableSQL); err != nil {
		return result, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&result.count); err != nil {
		return result, err
	}
	rows, err := db.QueryContext(ctx, `SELECT name,COALESCE(sql,'') FROM sqlite_master WHERE type='index' AND tbl_name=? ORDER BY name`, table)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var name, sqlText string
		if err := rows.Scan(&name, &sqlText); err != nil {
			_ = rows.Close()
			return result, err
		}
		result.indexes += name + "\x00" + sqlText + "\x00"
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	fkRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(`+table+`)`)
	if err != nil {
		return result, err
	}
	for fkRows.Next() {
		var id, seq int
		var target, from, to, update, deleteRule, match string
		if err := fkRows.Scan(&id, &seq, &target, &from, &to, &update, &deleteRule, &match); err != nil {
			_ = fkRows.Close()
			return result, err
		}
		result.foreigns += fmt.Sprintf("%d:%d:%s:%s:%s:%s:%s:%s\x00", id, seq, target, from, to, update, deleteRule, match)
	}
	if err := fkRows.Close(); err != nil {
		return result, err
	}
	return result, nil
}

const managedAgents027Columns = `agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,pending_initial_instruction,initial_instruction_sent,last_error_code,last_error_message,last_error_at,created_at,started_at,updated_at`

const managedAgents027DDL = `CREATE TABLE managed_agents_027_new (
  agent_id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  slot_held INTEGER NOT NULL DEFAULT 0 CHECK (slot_held IN (0,1)),
  cwd TEXT NOT NULL,
  selected_model_provider TEXT NOT NULL,
  selected_model_id TEXT NOT NULL,
  desired_state TEXT NOT NULL CHECK (desired_state IN ('running','terminated')),
  lifecycle_state TEXT NOT NULL CHECK (lifecycle_state IN ('starting','running','restarting','resetting','terminating','failed','terminated')),
  activity_state TEXT NOT NULL CHECK (activity_state IN ('starting','waiting','active','idle','compacting','stopped','failed')),
  accepting_prompts INTEGER NOT NULL DEFAULT 0 CHECK (accepting_prompts IN (0,1)),
  pi_session_id TEXT,
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  process_pid INTEGER,
  process_start_marker TEXT,
  process_incarnation TEXT,
  runtime_generation TEXT,
  manifest_revision INTEGER NOT NULL CHECK (manifest_revision >= 1),
  manifest_hash TEXT NOT NULL,
  pending_initial_instruction TEXT,
  initial_instruction_sent INTEGER NOT NULL DEFAULT 0 CHECK (initial_instruction_sent IN (0,1)),
  last_error_code TEXT,
  last_error_message TEXT,
  last_error_at TEXT,
  created_at TEXT NOT NULL,
  started_at TEXT,
  updated_at TEXT NOT NULL,
  launch_operation_id TEXT REFERENCES managed_http_operations(operation_id) ON DELETE RESTRICT
)`

func backfillManaged027LegacyActions(ctx context.Context, db *sql.DB) error {
	var total, pending, uniqueIDs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_restart_transitions`).Scan(&total); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_restart_transitions t JOIN managed_http_operations o ON o.operation_id=t.operation_id WHERE o.state='pending' AND length(t.operation_id)>0`).Scan(&pending); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT operation_id) FROM managed_restart_transitions WHERE length(operation_id)>0`).Scan(&uniqueIDs); err != nil {
		return err
	}
	if total != pending || total != uniqueIDs {
		return errors.New("migration 027 legacy transition preflight failed")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions (
		action_id,operation_id,agent_id,kind,source,parent_action_id,retry_of,execution_owner,state,stage,
		source_pi_session_id,source_session_epoch,source_process_pid,source_process_start_marker,source_process_incarnation,source_runtime_generation,source_manifest_revision,source_manifest_hash,
		target_pi_session_id,target_session_epoch,target_process_pid,target_process_start_marker,target_process_incarnation,target_runtime_generation,target_manifest_revision,target_manifest_hash,
		planned_target_runtime_generation,target_artifact_id,terminal_code,terminal_message,retryable,completed_at,recovery_owner,recovery_claimed_at,created_at,updated_at
	) SELECT
		t.operation_id,t.operation_id,t.agent_id,'restart','direct',NULL,NULL,'legacy025',
		CASE t.stage WHEN 'reserved' THEN 'admitted' WHEN 'prepared' THEN 'admitted' WHEN 'settling' THEN 'awaiting_settle' WHEN 'stopping' THEN 'stopping_source' WHEN 'stopped' THEN 'starting_target' WHEN 'starting' THEN 'awaiting_target_ready' END,
		CASE t.stage WHEN 'reserved' THEN 'admission' WHEN 'prepared' THEN 'admission' WHEN 'settling' THEN 'source_settle' WHEN 'stopping' THEN 'source_stop' WHEN 'stopped' THEN 'target_start' WHEN 'starting' THEN 'target_ready' END,
		t.old_pi_session_id,t.old_session_epoch,t.old_process_pid,t.old_process_start_marker,t.old_process_incarnation,t.old_runtime_generation,t.old_manifest_revision,t.old_manifest_hash,
		t.target_pi_session_id,t.target_session_epoch,t.target_process_pid,t.target_process_start_marker,t.target_process_incarnation,
		CASE WHEN t.target_pi_session_id IS NULL THEN NULL ELSE t.target_runtime_generation END,t.target_manifest_revision,t.target_manifest_hash,
		t.target_runtime_generation,NULL,NULL,NULL,NULL,NULL,NULL,NULL,t.created_at,t.updated_at
	FROM managed_restart_transitions t
	JOIN managed_http_operations o ON o.operation_id=t.operation_id
	WHERE o.state='pending'`)
	return err
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

func (s *Store) applyWidenManagedAgentsLifecycleState104(ctx context.Context, migration Migration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE id=?`, migration.ID).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var enabled int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return errors.New("migration 104 requires foreign keys enabled")
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		_, _ = s.db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		return err
	}
	restore := func() error {
		if _, err := s.db.ExecContext(context.Background(), `PRAGMA legacy_alter_table=OFF`); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
			return err
		}
		var value int
		if err := s.db.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&value); err != nil {
			return err
		}
		if value != 1 {
			return errors.New("migration 104 failed to restore foreign keys")
		}
		return nil
	}
	fail := func(cause error, begun bool) error {
		if begun {
			_, _ = s.db.ExecContext(context.Background(), `ROLLBACK`)
		}
		if err := restore(); err != nil {
			return fmt.Errorf("%w; restore foreign keys: %v", cause, err)
		}
		return cause
	}
	if _, err := s.db.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fail(err, false)
	}
	if _, err := s.db.ExecContext(ctx, migration.SQL); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations(id,applied_at) VALUES(?,?)`, migration.ID, formatTimestamp(s.clock.Now())); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `COMMIT`); err != nil {
		return fail(err, true)
	}
	if err := restore(); err != nil {
		return err
	}
	return chmodStateFiles(s.path)
}

func (s *Store) runImmediate(ctx context.Context, operation func() error) error {
	_, _, err := s.runImmediateWithCommitOutcome(ctx, operation)
	return err
}

// runImmediateWithCommitOutcome distinguishes transaction failure from errors
// after COMMIT (notably state-file permissions). A caller with an in-memory
// admission reservation must settle it using committed, not err == nil.
func (s *Store) runImmediateWithCommitOutcome(ctx context.Context, operation func() error) (committed, conclusive bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, true, errors.New("store is closed")
	}
	if _, err := s.db.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return false, true, err
	}
	rollback := func(cause error) (bool, bool, error) {
		if _, rollbackErr := s.db.ExecContext(context.Background(), "ROLLBACK"); rollbackErr != nil {
			// An unsuccessful rollback cannot prove the write was undone. Do
			// not refund an admission whose final state is unknown.
			return false, false, fmt.Errorf("%w; rollback failed: %v", cause, rollbackErr)
		}
		return false, true, cause
	}
	if err := operation(); err != nil {
		return rollback(err)
	}
	if _, err := s.db.ExecContext(ctx, "COMMIT"); err != nil {
		return rollback(err)
	}
	return true, true, chmodStateFiles(s.path)
}

// Sweep removes the currently bounded, store-owned resources in one immediate
// transaction. It retains the historical queue cleanup behavior for callers
// that use the store directly.
func (s *Store) Sweep(ctx context.Context, policy RetentionPolicy) (SweepResult, error) {
	return s.sweep(ctx, policy, true)
}

// SweepLifecycle runs the store portion of the router lifecycle sweep. Queued
// messages are recovered by the router first so it can publish the Node
// message_expired records for every message it removes.
func (s *Store) SweepLifecycle(ctx context.Context, policy RetentionPolicy) (SweepResult, error) {
	return s.sweep(ctx, policy, false)
}

// SubagentLifecycleSnapshot returns the currently running children for one
// parent. Terminal rows are intentionally omitted from the observer projection,
// matching the Node lifecycle snapshot contract.
func (s *Store) SubagentLifecycleSnapshot(ctx context.Context, parentAgentID string) (LifecycleSnapshot, error) {
	if ctx == nil {
		return LifecycleSnapshot{}, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return LifecycleSnapshot{}, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT subagent_id, type, description, status
		FROM subagent_lifecycle_state
		WHERE parent_agent_id = ? AND state = 'running'
		ORDER BY subagent_id ASC`, parentAgentID)
	if err != nil {
		return LifecycleSnapshot{}, err
	}
	snapshot := LifecycleSnapshot{Subagents: []map[string]any{}}
	for rows.Next() {
		var id, childType, description, status sql.NullString
		if err := rows.Scan(&id, &childType, &description, &status); err != nil {
			_ = rows.Close()
			return LifecycleSnapshot{}, err
		}
		child := map[string]any{"id": id.String}
		if childType.Valid {
			child["type"] = childType.String
		}
		if description.Valid {
			child["description"] = description.String
		}
		if status.Valid {
			child["status"] = status.String
		}
		snapshot.Subagents = append(snapshot.Subagents, child)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return LifecycleSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return LifecycleSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) sweep(ctx context.Context, policy RetentionPolicy, pruneQueued bool) (SweepResult, error) {
	if err := policy.validate(); err != nil {
		return SweepResult{}, err
	}
	var result SweepResult
	if err := s.runImmediate(ctx, func() error {
		now := s.clock.Now()
		nowText := formatTimestamp(now)
		var err error
		result.StaleAgents, err = s.pruneStaleAgents(ctx, nowText)
		if err != nil {
			return err
		}
		if pruneQueued {
			result.QueuedMessages, err = s.pruneExpiredQueuedMessages(ctx, nowText)
			if err != nil {
				return err
			}
		}
		// Embedded ACK keeps ordinary state visible until accepted expiry.
		// Both sweep paths must prune it; the router recovers only queued mail.
		// The attempt/evidence cascades; the SDK entry stays with its session.
		if _, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE state='acknowledged' AND expires_at<=? AND id IN (SELECT message_id FROM embedded_attempts) AND NOT EXISTS (SELECT 1 FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.message_id=messages.id AND o.state='pending')`, nowText); err != nil {
			return err
		}
		var expiredLeases []ExpiredLease
		result.Leases, expiredLeases, err = s.pruneExpiredLeases(ctx, nowText)
		if !pruneQueued {
			result.ExpiredLeases = expiredLeases
		}
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
		result.ReceiptsPruned, err = s.pruneConversationReceipts(ctx, nowText)
		if err != nil {
			return err
		}
		result.LifecycleExpired, result.LifecycleStates, result.LifecycleEvents, result.LifecycleProducers, result.LifecycleUpdates, err = s.pruneLifecycle(ctx, now, policy)
		if err != nil {
			return err
		}
		result.ManagedOperations, err = s.sweepManagedOperations(ctx, nowText)
		if err != nil {
			return err
		}
		result.ManagedControls, err = s.pruneManagedControlTerminalTx(ctx, "", now)
		if err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `DELETE FROM managed_direct_delivery_diagnostics WHERE observed_at < ?`, formatTimestamp(now.Add(-30*24*time.Hour))); err != nil {
			return err
		}
		if err = s.pruneManagedParentWorkTx(ctx, nowText); err != nil {
			return err
		}
		archiveRemoved, archiveErr := pruneManagedArchivesTx(ctx, s.db, now)
		if archiveErr != nil {
			return archiveErr
		}
		if archiveRemoved > 0 {
			if _, archiveErr = s.bumpManagedProjectionVersion(ctx); archiveErr != nil {
				return archiveErr
			}
		}
		// Failed managed rows are durable audit/name reservations. They are
		// intentionally not part of generic retention; a later terminate or
		// restart must not make a failed name silently reusable.
		result.ManagedAgents = 0
		if present, err := s.managedSubagentSchemaPresent(ctx); err != nil {
			return err
		} else if present {
			result.ManagedAgents, err = s.pruneManagedSubagentsTx(ctx, now, policy.LifecycleTerminalAge)
			if err != nil {
				return err
			}
		}
		return s.pruneEmbeddedProcessGenerationsTx(ctx)
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
		frozen, err := managedTerminationSettlementFrozenForMessageTx(ctx, s.db, id)
		if err != nil {
			return 0, err
		}
		if frozen {
			continue
		}
		if err := s.expireManagedDirectForMessageTx(ctx, id, now); err != nil {
			return 0, err
		}
		if err := s.completeLinkedReminder(ctx, id); err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE id = ?", id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (s *Store) completeLinkedReminder(ctx context.Context, messageID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM reminders WHERE linked_message_id = ? AND interval_ms IS NULL AND state = 'message_queued'`, messageID)
	if err != nil {
		return err
	}
	var completed []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		completed = append(completed, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM reminders
		WHERE linked_message_id = ? AND interval_ms IS NULL AND state = 'message_queued'`, messageID); err != nil {
		return err
	}
	for _, id := range completed {
		delete(s.reminderIncarnations, id)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE reminders
		SET state = 'scheduled', linked_message_id = NULL
		WHERE linked_message_id = ? AND interval_ms IS NOT NULL
		AND state IN ('scheduled', 'message_queued')`, messageID)
	return err
}

func (s *Store) pruneStaleAgents(ctx context.Context, now string) (int, error) {
	threshold := formatTimestamp(parseTimestamp(now).Add(-time.Hour))
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM agents
		WHERE (
			(last_connected_at IS NULL AND first_seen_at <= ? AND NOT EXISTS (
				SELECT 1 FROM reminders
				WHERE reminders.target = agents.agent_id
				AND (reminders.state = 'scheduled'
					OR (reminders.interval_ms IS NULL AND reminders.linked_message_id IS NOT NULL))
			) AND NOT EXISTS (SELECT 1 FROM messages WHERE recipient = agents.agent_id))
			OR (disconnected_at IS NOT NULL AND disconnected_at <= ?
				AND NOT EXISTS (SELECT 1 FROM messages WHERE recipient = agents.agent_id))
		)
		ORDER BY agent_id ASC`, threshold, threshold)
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
		if _, err := s.db.ExecContext(ctx, "DELETE FROM agents WHERE agent_id = ?", agent); err != nil {
			return 0, err
		}
	}
	return len(agents), nil
}

func (s *Store) pruneExpiredLeases(ctx context.Context, now string) (int, []ExpiredLease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id, instance_id, lease_id FROM agent_leases
		WHERE expires_at <= ? ORDER BY agent_id ASC`, now)
	if err != nil {
		return 0, nil, err
	}
	var expired []ExpiredLease
	for rows.Next() {
		var lease ExpiredLease
		if err := rows.Scan(&lease.AgentID, &lease.InstanceID, &lease.LeaseID); err != nil {
			_ = rows.Close()
			return 0, nil, err
		}
		expired = append(expired, lease)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, nil, err
	}
	if err := rows.Close(); err != nil {
		return 0, nil, err
	}
	for _, lease := range expired {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM agent_leases WHERE agent_id = ?", lease.AgentID); err != nil {
			return 0, nil, err
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE agents SET disconnected_at = ? WHERE agent_id = ?", now, lease.AgentID); err != nil {
			return 0, nil, err
		}
	}
	return len(expired), expired, nil
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

func (s *Store) pruneLifecycle(ctx context.Context, now time.Time, policy RetentionPolicy) (int, int, int, int, []LifecycleUpdate, error) {
	nowText := formatTimestamp(now)
	terminalCutoff := formatTimestamp(now.Add(-policy.LifecycleTerminalAge))
	ledgerCutoff := formatTimestamp(now.Add(-policy.LifecycleEventAge))
	producerCutoff := formatTimestamp(now.Add(-policy.LifecycleProducerAge))
	expiredRunning, updates, err := s.expireRunningLifecycleChildren(ctx, nowText, producerCutoff)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	stateResult, err := s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_state
		WHERE state = 'terminal' AND terminal_at <= ?`, terminalCutoff)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	stateCount, err := stateResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	var threshold sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT router_version FROM subagent_lifecycle_events
		ORDER BY router_version DESC LIMIT 1 OFFSET ?`, policy.LifecycleEventLimit-1).Scan(&threshold); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, 0, nil, err
	}
	var eventResult sql.Result
	if threshold.Valid {
		eventResult, err = s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_events
			WHERE received_at <= ? OR router_version < ?`, ledgerCutoff, threshold.Int64)
	} else {
		eventResult, err = s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_events WHERE received_at <= ?`, ledgerCutoff)
	}
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	eventCount, err := eventResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, 0, nil, err
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
		return 0, 0, 0, 0, nil, err
	}
	producerCount, err := producerResult.RowsAffected()
	return expiredRunning, int(stateCount), int(eventCount), int(producerCount), updates, err
}

func (s *Store) expireRunningLifecycleChildren(ctx context.Context, now, producerCutoff string) (int, []LifecycleUpdate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state.parent_agent_id, state.subagent_id,
		producer.instance_id, producer.parent_session_id, producer.producer_epoch
		FROM subagent_lifecycle_state state
		JOIN subagent_lifecycle_producers producer ON producer.parent_agent_id = state.parent_agent_id
		WHERE state.state = 'running' AND producer.last_seen_at <= ?
		AND NOT EXISTS (SELECT 1 FROM agent_leases lease
			WHERE lease.agent_id = producer.parent_agent_id AND lease.expires_at > ?)
		ORDER BY state.parent_agent_id ASC, state.subagent_id ASC`, producerCutoff, now)
	if err != nil {
		return 0, nil, err
	}
	type child struct{ parent, id, instance, session, epoch string }
	var children []child
	for rows.Next() {
		var current child
		if err := rows.Scan(&current.parent, &current.id, &current.instance, &current.session, &current.epoch); err != nil {
			_ = rows.Close()
			return 0, nil, err
		}
		children = append(children, current)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, nil, err
	}
	if err := rows.Close(); err != nil {
		return 0, nil, err
	}
	payloadHash := fmt.Sprintf("%x", sha256.Sum256([]byte("producer_expired")))
	versions := make(map[string]int64, len(children))
	for _, current := range children {
		var latest sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT MAX(client_sequence) FROM subagent_lifecycle_events
			WHERE parent_agent_id = ? AND producer_epoch = ?`, current.parent, current.epoch).Scan(&latest); err != nil {
			return 0, nil, err
		}
		sequence := int64(1)
		if latest.Valid {
			sequence = latest.Int64/1000 + 1
		}
		eventID := fmt.Sprintf("%x", sha256.Sum256([]byte("producer_expired:"+current.parent+":"+current.id+":"+now)))
		result, err := s.db.ExecContext(ctx, `INSERT INTO subagent_lifecycle_events (
			parent_agent_id, instance_id, parent_session_id, producer_epoch, subagent_id,
			event_id, client_sequence, event_kind, state, type, description, status,
			terminal_reason, payload_hash, received_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'terminal', 'terminal', NULL, NULL, NULL, 'producer_expired', ?, ?)`,
			current.parent, current.instance, current.session, current.epoch, current.id,
			eventID, sequence*1000, payloadHash, now)
		if err != nil {
			return 0, nil, err
		}
		version, err := result.LastInsertId()
		if err != nil {
			return 0, nil, err
		}
		versions[current.parent] = version
		if _, err := s.db.ExecContext(ctx, `INSERT INTO subagent_lifecycle_state (
			parent_agent_id, subagent_id, instance_id, parent_session_id, producer_epoch,
			type, description, status, state, started_at, updated_at, terminal_at,
			terminal_reason, router_version)
			VALUES (?, ?, ?, ?, ?, NULL, NULL, NULL, 'terminal', NULL, ?, ?, 'producer_expired', ?)
			ON CONFLICT (parent_agent_id, subagent_id) DO UPDATE SET
			instance_id = excluded.instance_id, parent_session_id = excluded.parent_session_id,
			producer_epoch = excluded.producer_epoch, type = excluded.type, description = excluded.description,
			status = excluded.status, state = excluded.state,
			started_at = CASE WHEN excluded.state = 'running' THEN excluded.started_at ELSE subagent_lifecycle_state.started_at END,
			updated_at = excluded.updated_at, terminal_at = excluded.terminal_at,
			terminal_reason = excluded.terminal_reason, router_version = excluded.router_version`,
			current.parent, current.id, current.instance, current.session, current.epoch,
			now, now, version); err != nil {
			return 0, nil, err
		}
	}
	var updates []LifecycleUpdate
	for parent, version := range versions {
		updates = append(updates, LifecycleUpdate{ParentAgentID: parent, RouterVersion: version})
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].ParentAgentID < updates[j].ParentAgentID })
	return len(children), updates, nil
}

func parseTimestamp(value string) time.Time {
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	if err != nil {
		return time.Time{}
	}
	return parsed
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
