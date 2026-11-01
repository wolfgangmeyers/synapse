package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration099WorkflowStateGoalConstraint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()

	// 1. Run migrations up to 098 (pre-099)
	idx099 := migrationIndex(t, migrations, "099_workflow_state_goal")
	before, err := Open(ctx, path, Options{Migrations: migrations[:idx099]})
	require.NoError(t, err)

	// Seed existing workflow states before migration 099
	_, err = before.db.ExecContext(ctx, `INSERT INTO workflow_states (id, name, description, entry_text, created_at, updated_at) VALUES ('state-pre-099', 'pre-migration', 'desc', 'entry', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`)
	require.NoError(t, err)

	require.NoError(t, before.Close())

	// 2. Open with all migrations including 099
	after, err := Open(ctx, path, Options{Migrations: migrations[:idx099+1]})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, after.Close()) })

	// Verify PRAGMA foreign_key_check passes
	rows, err := after.db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next(), "foreign key check must pass with zero violations")

	// Verify existing row survived and has default empty goal
	var goal string
	err = after.db.QueryRowContext(ctx, `SELECT goal FROM workflow_states WHERE id='state-pre-099'`).Scan(&goal)
	require.NoError(t, err)
	require.Equal(t, "", goal)

	// Verify CHECK constraint rejects goal > 4000 chars
	tooLongGoal := strings.Repeat("x", 4001)
	_, err = after.db.ExecContext(ctx, `INSERT INTO workflow_states (id, name, description, entry_text, goal, created_at, updated_at) VALUES ('state-too-long', 'too-long', 'desc', 'entry', ?, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`, tooLongGoal)
	require.Error(t, err, "CHECK constraint must reject length(goal) > 4000")

	// Verify CHECK constraint accepts goal == 4000 chars
	validGoal := strings.Repeat("x", 4000)
	_, err = after.db.ExecContext(ctx, `INSERT INTO workflow_states (id, name, description, entry_text, goal, created_at, updated_at) VALUES ('state-max', 'max-goal', 'desc', 'entry', ?, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`, validGoal)
	require.NoError(t, err, "CHECK constraint must accept length(goal) <= 4000")
}

func TestMigration117WorkflowImplicitGoals(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()

	// 1. Run migrations up to 116 (pre-117)
	idx117 := migrationIndex(t, migrations, "117_workflow_implicit_goals")
	before, err := Open(ctx, path, Options{Migrations: migrations[:idx117]})
	require.NoError(t, err)

	// Seed pre-117 workflow and workflow_state (with goal column)
	_, err = before.db.ExecContext(ctx, `INSERT INTO workflows (id, name, description, created_at, updated_at) VALUES ('wf-pre-117', 'legacy-wf', 'desc', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`)
	require.NoError(t, err)

	_, err = before.db.ExecContext(ctx, `INSERT INTO workflow_states (id, workflow_id, name, description, entry_text, goal, created_at, updated_at) VALUES ('state-pre-117', 'wf-pre-117', 'legacy-state', 'desc', 'entry', 'legacy-goal', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`)
	require.NoError(t, err)

	require.NoError(t, before.Close())

	// 2. Open with all migrations including 117
	after, err := Open(ctx, path, Options{Migrations: migrations[:idx117+1]})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, after.Close()) })

	// Verify PRAGMA foreign_key_check passes
	rows, err := after.db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next(), "foreign key check must pass with zero violations")

	// Verify existing workflow has default snooze_duration_ms == 600000
	var snoozeDurationMS int
	err = after.db.QueryRowContext(ctx, `SELECT snooze_duration_ms FROM workflows WHERE id='wf-pre-117'`).Scan(&snoozeDurationMS)
	require.NoError(t, err)
	require.Equal(t, 600000, snoozeDurationMS)

	// Verify goal column was dropped from workflow_states
	var dummy string
	err = after.db.QueryRowContext(ctx, `SELECT goal FROM workflow_states WHERE id='state-pre-117'`).Scan(&dummy)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no such column: goal")

	// Verify CHECK constraint on snooze_duration_ms rejects <= 0
	_, err = after.db.ExecContext(ctx, `INSERT INTO workflows (id, name, description, snooze_duration_ms, created_at, updated_at) VALUES ('wf-bad-snooze', 'bad-wf', 'desc', 0, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`)
	require.Error(t, err)
}

func TestMigration118CodexSearchTool(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()

	idx118 := migrationIndex(t, migrations, "118_codex_search_tool")
	s, err := Open(ctx, path, Options{Migrations: migrations[:idx118+1]})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	var category string
	var enabled int
	err = s.db.QueryRowContext(ctx, `SELECT category, enabled FROM managed_tool_defaults WHERE tool_name='codex_search'`).Scan(&category, &enabled)
	require.NoError(t, err)
	require.Equal(t, "Knowledge Base & Search", category)
	require.Equal(t, 1, enabled)
}

func TestMigration119StructuredWorkflowSchedules(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()

	// 1. Run up to migration 118 (pre-119)
	idx119 := migrationIndex(t, migrations, "119_structured_workflow_schedules")
	before, err := Open(ctx, path, Options{Migrations: migrations[:idx119]})
	require.NoError(t, err)

	// Seed workflow and legacy workflow_schedule
	_, err = before.db.ExecContext(ctx, `INSERT INTO workflows (id, name, description, created_at, updated_at) VALUES ('wf-mig-119', 'mig-wf', 'desc', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`)
	require.NoError(t, err)

	_, err = before.db.ExecContext(ctx, `
		INSERT INTO workflow_schedules (
			id, workflow_id, name, enabled, schedule_type, interval_ms, cron_expression,
			variables_json, target_cwd, model_provider, model_id, next_run_at, created_at, updated_at
		) VALUES (
			'sched-legacy-1', 'wf-mig-119', 'legacy-sched', 1, 'interval', 300000, NULL,
			'{}', '', '', '', '2026-11-02T10:00:00Z', '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z'
		)
	`)
	require.NoError(t, err)
	require.NoError(t, before.Close())

	// 2. Open with all migrations including 119
	after, err := Open(ctx, path, Options{Migrations: migrations[:idx119+1]})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, after.Close()) })

	// Verify PRAGMA foreign_key_check passes
	rows, err := after.db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	require.False(t, rows.Next(), "foreign key check must pass with zero violations")

	// Verify legacy schedule was migrated to structured JSON format
	var schedJSON string
	err = after.db.QueryRowContext(ctx, `SELECT schedule_json FROM workflow_schedules WHERE id='sched-legacy-1'`).Scan(&schedJSON)
	require.NoError(t, err)
	require.Contains(t, schedJSON, `"time_mode":"repeating_interval"`)
	require.Contains(t, schedJSON, `"value":5`)

	// Verify old columns are gone
	var dummy string
	err = after.db.QueryRowContext(ctx, `SELECT cron_expression FROM workflow_schedules WHERE id='sched-legacy-1'`).Scan(&dummy)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no such column: cron_expression")
}

