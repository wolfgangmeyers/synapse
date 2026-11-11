package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
)

// Older databases can contain launch recipes whose agents are already gone.
// Rebuilding this leaf table must preserve that history, not delete it or make
// it a new startup blocker. Like the other table-rebuild migrations, hold the
// single connection exclusively and change enforcement outside the transaction.
func (s *Store) applyOptionalRetiredRPCRecipeMigration131(ctx context.Context, migration Migration) (result error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var present int
	if err := conn.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE id=?`, migration.ID).Scan(&present); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var enabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return errors.New("migration 131 requires foreign keys enabled")
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	transactionOpen := false
	defer func() {
		// Cancellation must not prevent rollback or leave normal writes with
		// foreign-key enforcement disabled. An uncertain connection is closed.
		cleanup := context.Background()
		var cleanupErr error
		if transactionOpen {
			if _, err := conn.ExecContext(cleanup, `ROLLBACK`); err != nil {
				cleanupErr = fmt.Errorf("migration 131 rollback: %w", err)
			}
		}
		if _, err := conn.ExecContext(cleanup, `PRAGMA foreign_keys=ON`); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("migration 131 restore foreign keys: %w", err))
		} else if err := conn.QueryRowContext(cleanup, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		} else if enabled != 1 {
			cleanupErr = errors.Join(cleanupErr, errors.New("migration 131 failed to restore foreign keys"))
		}
		if cleanupErr != nil {
			s.closed = true
			_ = conn.Close()
			_ = s.db.Close()
			result = errors.Join(result, cleanupErr)
		}
	}()
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 0 {
		return errors.New("migration 131 could not suspend foreign keys")
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	transactionOpen = true
	before, err := recipeMigrationForeignKeyProblems131(ctx, conn)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf("apply migration %s: %w", migration.ID, err)
	}
	after, err := recipeMigrationForeignKeyProblems131(ctx, conn)
	if err != nil {
		return err
	}
	// A clean database must stay clean; existing debt must survive exactly.
	// Preserved rowids keep these diagnostics stable across the table copy.
	if !maps.Equal(before, after) {
		return errors.New("migration 131 changed foreign key violations")
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(id,applied_at) VALUES(?,?)`, migration.ID, formatTimestamp(s.clock.Now())); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	transactionOpen = false
	return chmodStateFiles(s.path)
}

func recipeMigrationForeignKeyProblems131(ctx context.Context, conn *sql.Conn) (map[string]int, error) {
	// This is a leaf-table rebuild. Checking only its relationships avoids
	// scanning years of unrelated conversation history on router startup.
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check(managed_launch_recipes)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	problems := make(map[string]int)
	for rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var key int
		if err := rows.Scan(&table, &rowID, &parent, &key); err != nil {
			return nil, err
		}
		problems[fmt.Sprintf("%s:%v:%s:%d", table, rowID, parent, key)]++
	}
	return problems, rows.Err()
}
