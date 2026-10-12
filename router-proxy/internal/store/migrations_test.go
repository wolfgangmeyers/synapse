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
