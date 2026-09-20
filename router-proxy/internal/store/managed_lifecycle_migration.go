package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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
	baselineForeignKeys, err := captureManagedForeignKeyViolations(ctx, s.db)
	if err != nil {
		return err
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
	afterForeignKeys, err := captureManagedForeignKeyViolations(ctx, s.db)
	if err != nil {
		return fail(err, true)
	}
	if err := validateManagedForeignKeyDelta057(baselineForeignKeys, afterForeignKeys); err != nil {
		return fail(err, true)
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
	afterCommitForeignKeys, err := captureManagedForeignKeyViolations(ctx, s.db)
	if err != nil {
		s.closed = true
		_ = s.db.Close()
		return err
	}
	if err := validateManagedForeignKeyDelta057(baselineForeignKeys, afterCommitForeignKeys); err != nil {
		s.closed = true
		_ = s.db.Close()
		return fmt.Errorf("migration 057 post-commit foreign key check failed: %w", err)
	}
	return chmodStateFiles(s.path)
}

type managedForeignKeyViolation struct {
	table, parent string
	rowID         int64
	hasRowID      bool
	foreignKeyID  int64
}

func captureManagedForeignKeyViolations(ctx context.Context, db *sql.DB) ([]managedForeignKeyViolation, error) {
	rows, err := db.QueryContext(ctx, `SELECT "table",rowid,parent,fkid FROM pragma_foreign_key_check ORDER BY "table",rowid,parent,fkid`)
	if err != nil {
		return nil, err
	}
	var violations []managedForeignKeyViolation
	for rows.Next() {
		var violation managedForeignKeyViolation
		var rowID sql.NullInt64
		var parent sql.NullString
		if err := rows.Scan(&violation.table, &rowID, &parent, &violation.foreignKeyID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if rowID.Valid {
			violation.rowID, violation.hasRowID = rowID.Int64, true
		}
		if parent.Valid {
			violation.parent = parent.String
		}
		violations = append(violations, violation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return violations, nil
}

func managedForeignKeyViolationKey(violation managedForeignKeyViolation) string {
	return fmt.Sprintf("%s|%t|%d|%s|%d", violation.table, violation.hasRowID, violation.rowID, violation.parent, violation.foreignKeyID)
}

func managedLifecycleForeignKeyViolation057(violation managedForeignKeyViolation) bool {
	switch violation.table {
	case "managed_lifecycle_actions", "managed_lifecycle_batch_items", "managed_session_artifacts", "managed_termination_diagnostics":
		return true
	default:
		return false
	}
}

func validateManagedForeignKeyDelta057(baseline, after []managedForeignKeyViolation) error {
	baselineUnrelated := make(map[string]struct{}, len(baseline))
	for _, violation := range baseline {
		if !managedLifecycleForeignKeyViolation057(violation) {
			baselineUnrelated[managedForeignKeyViolationKey(violation)] = struct{}{}
		}
	}
	afterUnrelated := make(map[string]struct{}, len(after))
	for _, violation := range after {
		if managedLifecycleForeignKeyViolation057(violation) {
			return fmt.Errorf("migration 057 left lifecycle foreign-key violation in %s", violation.table)
		}
		key := managedForeignKeyViolationKey(violation)
		if _, exists := baselineUnrelated[key]; !exists {
			return fmt.Errorf("migration 057 introduced foreign-key violation in %s", violation.table)
		}
		afterUnrelated[key] = struct{}{}
	}
	for key := range baselineUnrelated {
		if _, exists := afterUnrelated[key]; !exists {
			return errors.New("migration 057 removed a pre-existing unrelated foreign-key violation")
		}
	}
	return nil
}

const managedLifecycleOrphanRepairMigration059ID = "059_managed_lifecycle_orphan_repair"

// applyManagedLifecycleOrphanRepairMigration059 repairs foreign-key debt left
// by older router versions. It is deliberately separate from migration 057 so
// databases that already recorded 057 still receive the repair.
func (s *Store) applyManagedLifecycleOrphanRepairMigration059(ctx context.Context, migration Migration) error {
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
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if foreignKeys != 1 {
		return errors.New("migration 059 requires foreign_keys=1")
	}
	baselineForeignKeys, err := captureManagedForeignKeyViolations(ctx, s.db)
	if err != nil {
		return err
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
			return errors.New("migration 059 could not restore foreign keys")
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
	if err := s.repairManagedLifecycleForeignKeys059(ctx); err != nil {
		return fail(fmt.Errorf("migration 059 orphan repair: %w", err), true)
	}
	afterForeignKeys, err := captureManagedForeignKeyViolations(ctx, s.db)
	if err != nil {
		return fail(err, true)
	}
	if err := validateManagedForeignKeyDelta057(baselineForeignKeys, afterForeignKeys); err != nil {
		return fail(fmt.Errorf("migration 059 foreign key check failed: %w", err), true)
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fail(err, true)
	}
	if integrity != "ok" {
		return fail(errors.New("migration 059 integrity check failed"), true)
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
	afterCommitForeignKeys, err := captureManagedForeignKeyViolations(ctx, s.db)
	if err != nil {
		s.closed = true
		_ = s.db.Close()
		return err
	}
	if err := validateManagedForeignKeyDelta057(baselineForeignKeys, afterCommitForeignKeys); err != nil {
		s.closed = true
		_ = s.db.Close()
		return fmt.Errorf("migration 059 post-commit foreign key check failed: %w", err)
	}
	return chmodStateFiles(s.path)
}

// repairManagedLifecycleForeignKeys059 repairs only the known historical
// lifecycle graph damage. It keeps action and diagnostic rows, clears an
// unusable artifact identity, and gives retained rows synthetic parents whose
// normal operation retention still applies.
func (s *Store) repairManagedLifecycleForeignKeys059(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_http_operations (
		operation_id,session_identity_hash,method,canonical_path,request_id,canonical_body_hash,state,response_status,response_headers_json,response_body,created_at,terminal_at,expires_at,terminal_code,terminal_message
	) SELECT action.operation_id,'migration-057-orphan-repair-v1','INTERNAL','/internal/migrations/057/orphan-repair','migration-057-' || action.operation_id,action.operation_id,
		CASE WHEN action.state IN ('succeeded','failed','cancelled','superseded') THEN 'terminal' ELSE 'pending' END,
		CASE WHEN action.state IN ('succeeded','failed','cancelled','superseded') THEN 500 ELSE 202 END,'{}','{}',action.created_at,
		CASE WHEN action.state IN ('succeeded','failed','cancelled','superseded') THEN COALESCE(action.completed_at,action.updated_at,action.created_at) END,
		CASE WHEN action.state IN ('succeeded','failed','cancelled','superseded') THEN COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ',COALESCE(action.completed_at,action.updated_at,action.created_at),'+24 hours'),'1970-01-02T00:00:00.000Z') END,
		CASE WHEN action.state IN ('succeeded','failed','cancelled','superseded') THEN 'MIGRATION_057_ORPHAN_OPERATION' END,
		CASE WHEN action.state IN ('succeeded','failed','cancelled','superseded') THEN 'lifecycle operation parent reconstructed during migration' END
	FROM managed_lifecycle_actions AS action
	WHERE NOT EXISTS (SELECT 1 FROM managed_http_operations AS operation WHERE operation.operation_id=action.operation_id)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions
		SET target_artifact_id=NULL
		WHERE target_artifact_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM managed_session_artifacts AS artifact WHERE artifact.artifact_id=managed_lifecycle_actions.target_artifact_id)`); err != nil {
		return err
	}

	diagnosticRows, err := s.db.QueryContext(ctx, `SELECT action_id,MIN(recorded_at)
		FROM managed_termination_diagnostics AS diagnostic
		WHERE NOT EXISTS (SELECT 1 FROM managed_lifecycle_actions AS action WHERE action.action_id=diagnostic.action_id)
		GROUP BY action_id ORDER BY action_id`)
	if err != nil {
		return err
	}
	type orphanDiagnostic struct{ actionID, recordedAt string }
	var orphanDiagnostics []orphanDiagnostic
	for diagnosticRows.Next() {
		var diagnostic orphanDiagnostic
		if err := diagnosticRows.Scan(&diagnostic.actionID, &diagnostic.recordedAt); err != nil {
			_ = diagnosticRows.Close()
			return err
		}
		orphanDiagnostics = append(orphanDiagnostics, diagnostic)
	}
	if err := diagnosticRows.Err(); err != nil {
		_ = diagnosticRows.Close()
		return err
	}
	if err := diagnosticRows.Close(); err != nil {
		return err
	}
	for _, diagnostic := range orphanDiagnostics {
		operationID := managed057RepairOperationID(diagnostic.actionID)
		if err := s.insertManaged057RepairOperation(ctx, operationID, "failed", diagnostic.recordedAt, diagnostic.recordedAt, diagnostic.recordedAt); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions (
			action_id,operation_id,agent_id,kind,source,parent_action_id,retry_of,execution_owner,state,stage,
			planned_target_runtime_generation,target_artifact_id,terminal_code,terminal_message,retryable,completed_at,created_at,updated_at
		) VALUES (?,?,?,'terminate','direct',NULL,NULL,'lifecycle','failed','complete',NULL,NULL,?,?,0,?,?,?)`,
			diagnostic.actionID, operationID, managed057RepairAgentID(diagnostic.actionID), "MIGRATION_057_ORPHAN_DIAGNOSTIC", "termination diagnostic retained after its lifecycle action was lost", diagnostic.recordedAt, diagnostic.recordedAt, diagnostic.recordedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) insertManaged057RepairOperation(ctx context.Context, operationID, state, completedAt, createdAt, updatedAt string) error {
	terminal := state == "succeeded" || state == "failed" || state == "cancelled" || state == "superseded"
	var terminalAt, expiresAt, terminalCode, terminalMessage any
	responseStatus := 202
	if terminal {
		terminalAtValue := completedAt
		if terminalAtValue == "" {
			terminalAtValue = updatedAt
		}
		if terminalAtValue == "" {
			terminalAtValue = createdAt
		}
		parsed := parseTimestamp(terminalAtValue)
		if parsed.IsZero() {
			terminalAtValue = "1970-01-01T00:00:00.000Z"
			parsed = parseTimestamp(terminalAtValue)
		}
		terminalAt = terminalAtValue
		expiresAt = formatTimestamp(parsed.Add(24 * time.Hour))
		terminalCode, terminalMessage = "MIGRATION_057_ORPHAN_OPERATION", "lifecycle operation parent reconstructed during migration"
		responseStatus = 500
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(operationID)))
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_http_operations (
		operation_id,session_identity_hash,method,canonical_path,request_id,canonical_body_hash,state,response_status,response_headers_json,response_body,created_at,terminal_at,expires_at,terminal_code,terminal_message
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationID, "migration-057-orphan-repair-v1", "INTERNAL", "/internal/migrations/057/orphan-repair", "migration-057-"+hash, hash, map[bool]string{true: "terminal", false: "pending"}[terminal], responseStatus, "{}", `{}`, createdAt, terminalAt, expiresAt, terminalCode, terminalMessage)
	return err
}

func managed057RepairOperationID(actionID string) string {
	return "migration-057-orphan-diagnostic-" + fmt.Sprintf("%x", sha256.Sum256([]byte(actionID)))
}

func managed057RepairAgentID(actionID string) string {
	return "migration-057-orphan-agent-" + fmt.Sprintf("%x", sha256.Sum256([]byte(actionID)))
}
