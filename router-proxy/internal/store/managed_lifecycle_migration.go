package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	managedRestartRetryMigration057ID = "057_managed_restart_retry_constraint"
	managedRestartRetryConstraintOld  = "CHECK (retry_of IS NULL OR kind IN ('reset','terminate'))"
	managedRestartRetryConstraintNew  = "CHECK (retry_of IS NULL OR kind IN ('restart','reset','terminate'))"
)

// applyManagedRestartRetryMigration057 rebuilds the existing lifecycle-action
// table because SQLite has no ALTER CHECK operation. The table definition is
// read from sqlite_master so columns added by later migrations stay intact.
func (s *Store) applyManagedRestartRetryMigration057(ctx context.Context, migration Migration) error {
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

	var tableSQL string
	if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_lifecycle_actions'`).Scan(&tableSQL); err != nil {
		return fmt.Errorf("read managed lifecycle action schema: %w", err)
	}
	if strings.Contains(tableSQL, managedRestartRetryConstraintNew) {
		_, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (id, applied_at) VALUES (?,?)`, migration.ID, formatTimestamp(s.clock.Now()))
		return err
	}
	if !strings.Contains(tableSQL, managedRestartRetryConstraintOld) {
		return errors.New("migration 057 could not find the historical retry constraint")
	}

	indexRows, err := s.db.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name='managed_lifecycle_actions' AND sql IS NOT NULL ORDER BY name`)
	if err != nil {
		return err
	}
	var indexSQL []string
	for indexRows.Next() {
		var statement string
		if err := indexRows.Scan(&statement); err != nil {
			_ = indexRows.Close()
			return err
		}
		indexSQL = append(indexSQL, statement)
	}
	if err := indexRows.Close(); err != nil {
		return err
	}
	columnRows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('managed_lifecycle_actions') ORDER BY cid`)
	if err != nil {
		return err
	}
	var columns []string
	for columnRows.Next() {
		var name string
		if err := columnRows.Scan(&name); err != nil {
			_ = columnRows.Close()
			return err
		}
		columns = append(columns, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	if err := columnRows.Close(); err != nil {
		return err
	}
	if len(columns) == 0 {
		return errors.New("migration 057 lifecycle action table has no columns")
	}
	columnList := strings.Join(columns, ",")
	var oldRows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions`).Scan(&oldRows); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS temp.managed_lifecycle_actions_057_snapshot`); err != nil {
		return err
	}

	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if foreignKeys != 1 {
		return errors.New("migration 057 requires foreign_keys=1")
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	foreignKeysDisabled := true
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
			return errors.New("migration 057 could not restore foreign keys")
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

	if _, err := s.db.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fail(err, false)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS managed_lifecycle_actions_057_new`); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TEMP TABLE managed_lifecycle_actions_057_snapshot AS SELECT * FROM managed_lifecycle_actions`); err != nil {
		return fail(err, true)
	}
	newTableSQL := strings.Replace(tableSQL, "CREATE TABLE managed_lifecycle_actions", "CREATE TABLE managed_lifecycle_actions_057_new", 1)
	newTableSQL = strings.Replace(newTableSQL, managedRestartRetryConstraintOld, managedRestartRetryConstraintNew, 1)
	if newTableSQL == tableSQL {
		return fail(errors.New("migration 057 did not change the lifecycle action schema"), true)
	}
	if _, err := s.db.ExecContext(ctx, newTableSQL); err != nil {
		return fail(fmt.Errorf("create migration 057 table: %w", err), true)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions_057_new SELECT * FROM managed_lifecycle_actions`); err != nil {
		return fail(fmt.Errorf("copy migration 057 rows: %w", err), true)
	}
	var newRows, leftOnly, rightOnly int
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM managed_lifecycle_actions_057_new`: &newRows,
		`SELECT COUNT(*) FROM (SELECT ` + columnList + ` FROM temp.managed_lifecycle_actions_057_snapshot EXCEPT SELECT ` + columnList + ` FROM managed_lifecycle_actions_057_new)`: &leftOnly,
		`SELECT COUNT(*) FROM (SELECT ` + columnList + ` FROM managed_lifecycle_actions_057_new EXCEPT SELECT ` + columnList + ` FROM temp.managed_lifecycle_actions_057_snapshot)`: &rightOnly,
	} {
		if err := s.db.QueryRowContext(ctx, query).Scan(destination); err != nil {
			return fail(err, true)
		}
	}
	if oldRows != newRows || leftOnly != 0 || rightOnly != 0 {
		return fail(fmt.Errorf("migration 057 row parity failed: before=%d after=%d left_only=%d right_only=%d", oldRows, newRows, leftOnly, rightOnly), true)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE managed_lifecycle_actions`); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE managed_lifecycle_actions_057_new RENAME TO managed_lifecycle_actions`); err != nil {
		return fail(err, true)
	}
	for _, statement := range indexSQL {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fail(fmt.Errorf("restore migration 057 index: %w", err), true)
		}
	}
	var foreignProblems int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems); err != nil {
		return fail(err, true)
	}
	if foreignProblems != 0 {
		return fail(errors.New("migration 057 foreign key check failed"), true)
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fail(err, true)
	}
	if integrity != "ok" {
		return fail(errors.New("migration 057 integrity check failed"), true)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE temp.managed_lifecycle_actions_057_snapshot`); err != nil {
		return fail(err, true)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (id, applied_at) VALUES (?,?)`, migration.ID, formatTimestamp(s.clock.Now())); err != nil {
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
		return errors.New("migration 057 post-commit foreign key check failed")
	}
	return chmodStateFiles(s.path)
}
