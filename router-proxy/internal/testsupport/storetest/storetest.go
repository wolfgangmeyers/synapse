package storetest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var (
	templateDBOnce sync.Once
	templateDBData []byte
	templateDBErr  error
)

// StoreOption is a functional option for configuring store.Options in tests.
type StoreOption func(*store.Options)

// WithClock configures a deterministic clock for the store.
func WithClock(clock deterministic.Clock) StoreOption {
	return func(o *store.Options) {
		o.Clock = clock
	}
}

// WithIDSource configures an ID source for the store.
func WithIDSource(ids deterministic.IDSource) StoreOption {
	return func(o *store.Options) {
		o.IDSource = ids
	}
}

// WithBusyTimeout configures SQLite busy timeout.
func WithBusyTimeout(d time.Duration) StoreOption {
	return func(o *store.Options) {
		o.BusyTimeout = d
	}
}

// WithMigrations configures explicit migrations.
func WithMigrations(migrations []store.Migration) StoreOption {
	return func(o *store.Options) {
		o.Migrations = migrations
	}
}

// WithStoreOptions sets raw store.Options.
func WithStoreOptions(opts store.Options) StoreOption {
	return func(o *store.Options) {
		*o = opts
	}
}

func initTemplateDB() {
	tempDir, err := os.MkdirTemp("", "router-test-template-*")
	if err != nil {
		templateDBErr = fmt.Errorf("create temp dir for template db: %w", err)
		return
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "template.db")
	s, err := store.Open(context.Background(), dbPath, store.Options{})
	if err != nil {
		templateDBErr = fmt.Errorf("open template db: %w", err)
		return
	}

	// The template is private and quiescent. Checkpoint before closing the store;
	// the store-local test fixture uses its private connection to avoid an import cycle.
	checkpoint, err := sql.Open("sqlite", dbPath)
	if err != nil {
		_ = s.Close()
		templateDBErr = fmt.Errorf("open template checkpoint: %w", err)
		return
	}
	err = checkpointTemplate(checkpoint)
	closeErr := checkpoint.Close()
	if err != nil || closeErr != nil {
		_ = s.Close()
		if err != nil {
			templateDBErr = fmt.Errorf("wal checkpoint template db: %w", err)
		} else {
			templateDBErr = fmt.Errorf("close template checkpoint: %w", closeErr)
		}
		return
	}
	if err := s.Close(); err != nil {
		templateDBErr = fmt.Errorf("close template db: %w", err)
		return
	}

	data, err := os.ReadFile(dbPath)
	if err != nil {
		templateDBErr = fmt.Errorf("read template db file: %w", err)
		return
	}
	templateDBData = data
}

// SeedTemplateDatabase initializes the template database if not yet initialized,
// and returns the raw database bytes.
func SeedTemplateDatabase() ([]byte, error) {
	templateDBOnce.Do(initTemplateDB)
	if templateDBErr != nil {
		return nil, templateDBErr
	}
	return templateDBData, nil
}

// CloneTemplateDB copies the initialized template database with all migrations applied
// into destPath with 0600 permissions. If destination directory does not exist, it is created.
func CloneTemplateDB(t testing.TB, destPath string) error {
	if t != nil {
		t.Helper()
	}
	data, err := SeedTemplateDatabase()
	if err != nil {
		if t != nil {
			t.Fatalf("CloneTemplateDB: failed to seed template db: %v", err)
		}
		return err
	}

	parent := filepath.Dir(destPath)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		if t != nil {
			t.Fatalf("CloneTemplateDB: failed to mkdir parent %s: %v", parent, err)
		}
		return err
	}

	if err := os.WriteFile(destPath, data, 0o600); err != nil {
		if t != nil {
			t.Fatalf("CloneTemplateDB: failed to write db file %s: %v", destPath, err)
		}
		return err
	}
	return nil
}

// OpenTestStore initializes a new test database for testing, seeded quickly
// from the template database snapshot with all migrations applied.
// The store is registered for cleanup via t.Cleanup.
func OpenTestStore(t testing.TB, options ...StoreOption) (*store.Store, string) {
	if t != nil {
		t.Helper()
	}
	var dir string
	if t != nil {
		dir = t.TempDir()
	} else {
		var err error
		dir, err = os.MkdirTemp("", "router-test-store-*")
		if err != nil {
			panic(fmt.Sprintf("OpenTestStore: failed to create temp dir: %v", err))
		}
	}
	dbPath := filepath.Join(dir, "router.db")

	opts := store.Options{}
	for _, opt := range options {
		opt(&opts)
	}

	if opts.Migrations == nil {
		if err := CloneTemplateDB(t, dbPath); err == nil {
			// Since schema migrations are already in place, avoid repeating them
			opts.Migrations = []store.Migration{}
		}
	}

	s, err := store.Open(context.Background(), dbPath, opts)
	if err != nil {
		if t != nil {
			t.Fatalf("OpenTestStore: failed to open store: %v", err)
		}
		panic(fmt.Sprintf("OpenTestStore: failed to open store: %v", err))
	}

	if t != nil {
		t.Cleanup(func() {
			_ = s.Close()
		})
	}
	return s, dbPath
}

// checkpointTemplate rejects a successful PRAGMA that could not finish.
func checkpointTemplate(db *sql.DB) error {
	var busy, logFrames, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 || logFrames != checkpointed {
		return fmt.Errorf("incomplete checkpoint: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointed)
	}
	return nil
}
