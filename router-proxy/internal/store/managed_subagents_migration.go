package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func embeddedMigrationForeignKeyProblems(ctx context.Context, db *sql.DB) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
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
		identity := fmt.Sprintf("%s:%v:%s:%d", table, rowID, parent, key)
		problems[identity]++
	}
	return problems, rows.Err()
}

// Foreign keys cannot be disabled inside SQLite's migration transaction. Do
// the narrow 065 session-table rebuild with this connection exclusively held,
// then verify the entire dependent FK graph before committing any schema row.
func (s *Store) applyEmbeddedSubagentRoleMigration085(ctx context.Context, migration Migration) error {
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
		return errors.New("migration 085 requires foreign keys enabled")
	}
	before, err := embeddedMigrationForeignKeyProblems(ctx, s.db)
	if err != nil {
		return err
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
			return errors.New("migration 085 failed to restore foreign keys")
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
	var old, upgraded int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_sessions`).Scan(&upgraded); err != nil {
		return fail(err, true)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_sessions WHERE role IN ('root','worker')`).Scan(&old); err != nil {
		return fail(err, true)
	}
	if old != upgraded {
		return fail(errors.New("migration 085 session copy mismatch"), true)
	}
	after, err := embeddedMigrationForeignKeyProblems(ctx, s.db)
	if err != nil {
		return fail(err, true)
	}
	for problem, count := range after {
		if count > before[problem] {
			return fail(fmt.Errorf("migration 085 introduced foreign key violation: %s", problem), true)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations(id,applied_at) VALUES(?,?)`, migration.ID, formatTimestamp(s.clock.Now())); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `COMMIT`); err != nil {
		return fail(err, true)
	}
	return restore()
}
