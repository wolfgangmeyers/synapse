package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// checkLegacyRPCBeforeOpen uses a read-only SQLite connection, including WAL,
// before migrations or startup reconciliation can touch the existing database.
func checkLegacyRPCBeforeOpen(ctx context.Context, path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err = os.Stat(absolute); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute}).String() + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	var exists int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='managed_agents'`).Scan(&exists); err != nil {
		return fmt.Errorf("legacy RPC preflight: %w", err)
	}
	if exists == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(managed_agents)`)
	if err != nil {
		return fmt.Errorf("legacy RPC preflight: %w", err)
	}
	hasRuntime := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err = rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "runtime" {
			hasRuntime = true
		}
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	query := `SELECT agent_id FROM managed_agents ORDER BY agent_id LIMIT 1`
	if hasRuntime {
		query = `SELECT agent_id FROM managed_agents WHERE runtime IS NULL OR runtime <> 'embedded' ORDER BY agent_id LIMIT 1`
	}
	var id string
	err = db.QueryRowContext(ctx, query).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("legacy RPC preflight: %w", err)
	}
	return fmt.Errorf("unsupported legacy RPC agent %s: recovery requires a compatible router", id)
}
