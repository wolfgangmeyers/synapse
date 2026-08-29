package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedMigrationsMatchNodeMigrationFixtures(t *testing.T) {
	migrations := DefaultMigrations()
	require.Len(t, migrations, 20)
	repoRoot := filepath.Join("..", "..", "..")
	for _, migration := range migrations {
		contents, err := os.ReadFile(filepath.Join(repoRoot, "harness", "migrations", migration.ID+".sql"))
		require.NoError(t, err, migration.ID)
		require.Equal(t, string(contents), migration.SQL, migration.ID)
	}
}

func TestMigrationPlanUsesNodeIDsInOrder(t *testing.T) {
	want := []string{
		"001_initial", "002_delivery_attempt_deadline", "003_reminders", "004_server_jobs",
		"005_message_incarnation", "006_sentinel", "007_sentinel_occurrences", "008_sentinel_deadline",
		"009_sentinel_tombstones", "010_sentinel_exit_outcome", "011_sentinel_relay_capabilities",
		"012_sentinel_native_cleanup", "013_conversation_history", "014_reminder_batch_links",
		"015_conversation_source", "016_delivery_batches", "017_subagent_lifecycle",
		"018_subagent_lifecycle_status", "019_goal_control_desired_state", "020_prompt_templates",
	}
	got := make([]string, 0, len(DefaultMigrations()))
	for _, migration := range DefaultMigrations() {
		got = append(got, migration.ID)
	}
	require.Equal(t, want, got)
}
