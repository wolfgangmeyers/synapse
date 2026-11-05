package store

// This local fixture avoids a store -> storetest -> store test import cycle.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
)

var (
	templateDBOnce sync.Once
	templateDBData []byte
	templateDBErr  error
)

// localStoreOption is a functional option for configuring store Options in tests.
type localStoreOption func(*Options)

// withLocalClock configures a deterministic clock for the store.
func withLocalClock(clock deterministic.Clock) localStoreOption {
	return func(o *Options) {
		o.Clock = clock
	}
}

// withLocalIDSource configures an ID source for the store.
func withLocalIDSource(ids deterministic.IDSource) localStoreOption {
	return func(o *Options) {
		o.IDSource = ids
	}
}

// WithBusyTimeout configures SQLite busy timeout.
func WithBusyTimeout(d time.Duration) localStoreOption {
	return func(o *Options) {
		o.BusyTimeout = d
	}
}

// withLocalMigrations configures explicit migrations.
func withLocalMigrations(migrations []Migration) localStoreOption {
	return func(o *Options) {
		o.Migrations = migrations
	}
}

// WithlocalStoreOptions sets raw store Options.
func WithlocalStoreOptions(opts Options) localStoreOption {
	return func(o *Options) {
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
	s, err := Open(context.Background(), dbPath, Options{})
	if err != nil {
		templateDBErr = fmt.Errorf("open template db: %w", err)
		return
	}

	// Checkpoint WAL completely into the main database file
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = s.Close()
		templateDBErr = fmt.Errorf("wal checkpoint template db: %w", err)
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

// seedLocalTemplateDatabase initializes the template database if not yet initialized,
// and returns the raw database bytes.
func seedLocalTemplateDatabase() ([]byte, error) {
	templateDBOnce.Do(initTemplateDB)
	if templateDBErr != nil {
		return nil, templateDBErr
	}
	return templateDBData, nil
}

// cloneLocalTemplateDB copies the initialized template database with all migrations applied
// into destPath with 0600 permissions. If destination directory does not exist, it is created.
func cloneLocalTemplateDB(t testing.TB, destPath string) error {
	if t != nil {
		t.Helper()
	}
	data, err := seedLocalTemplateDatabase()
	if err != nil {
		if t != nil {
			t.Fatalf("cloneLocalTemplateDB: failed to seed template db: %v", err)
		}
		return err
	}

	parent := filepath.Dir(destPath)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		if t != nil {
			t.Fatalf("cloneLocalTemplateDB: failed to mkdir parent %s: %v", parent, err)
		}
		return err
	}

	if err := os.WriteFile(destPath, data, 0o600); err != nil {
		if t != nil {
			t.Fatalf("cloneLocalTemplateDB: failed to write db file %s: %v", destPath, err)
		}
		return err
	}
	return nil
}

// openLocalTestStore initializes a new test database for testing, seeded quickly
// from the template database snapshot with all migrations applied.
// The store is registered for cleanup via t.Cleanup.
func openLocalTestStore(t testing.TB, options ...localStoreOption) (*Store, string) {
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
			panic(fmt.Sprintf("openLocalTestStore: failed to create temp dir: %v", err))
		}
	}
	dbPath := filepath.Join(dir, "router.db")

	opts := Options{}
	for _, opt := range options {
		opt(&opts)
	}

	if opts.Migrations == nil {
		if err := cloneLocalTemplateDB(t, dbPath); err == nil {
			// Since schema migrations are already in place, avoid repeating them
			opts.Migrations = []Migration{}
		}
	}

	s, err := Open(context.Background(), dbPath, opts)
	if err != nil {
		if t != nil {
			t.Fatalf("openLocalTestStore: failed to open store: %v", err)
		}
		panic(fmt.Sprintf("openLocalTestStore: failed to open store: %v", err))
	}

	if t != nil {
		t.Cleanup(func() {
			_ = s.Close()
		})
	}
	return s, dbPath
}
