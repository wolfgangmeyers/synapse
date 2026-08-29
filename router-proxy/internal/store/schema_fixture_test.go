package store

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

type nodeStoreSnapshot struct {
	Migrations []string `json:"migrations"`
	Settings   struct {
		JournalMode   string `json:"journalMode"`
		Synchronous   string `json:"synchronous"`
		ForeignKeys   bool   `json:"foreignKeys"`
		BusyTimeoutMS int    `json:"busyTimeoutMs"`
	} `json:"settings"`
}

func TestNodeCreatedDatabaseOpensInGo(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "node-created.db")
	node := runNodeStore(t, databasePath)
	require.Equal(t, migrationIDs(), node.Migrations)
	require.Equal(t, "wal", node.Settings.JournalMode)
	require.Equal(t, "full", node.Settings.Synchronous)
	require.True(t, node.Settings.ForeignKeys)
	require.Equal(t, 5000, node.Settings.BusyTimeoutMS)

	store, err := Open(context.Background(), databasePath, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	var count int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count))
	require.Equal(t, len(DefaultMigrations()), count)
}

func TestGoCreatedDatabaseOpensInNode(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "go-created.db")
	store, err := Open(context.Background(), databasePath, Options{})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	node := runNodeStore(t, databasePath)
	require.Equal(t, migrationIDs(), node.Migrations)
	require.Equal(t, "wal", node.Settings.JournalMode)
	require.Equal(t, "full", node.Settings.Synchronous)
	require.True(t, node.Settings.ForeignKeys)
	require.Equal(t, 5000, node.Settings.BusyTimeoutMS)
}

func runNodeStore(t *testing.T, databasePath string) nodeStoreSnapshot {
	t.Helper()
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	moduleURL := (&url.URL{Scheme: "file", Path: filepath.Join(repoRoot, "harness", "db.js")}).String()
	const script = `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE });
console.log(JSON.stringify({ migrations: repository.migrationIds(), settings: repository.settings() }));
repository.close();
`
	command := exec.Command(node, "--input-type=module", "-e", script)
	command.Env = append(os.Environ(),
		"MECHA_NODE_STORE_MODULE="+moduleURL,
		"MECHA_NODE_STORE_DATABASE="+databasePath,
	)
	output, err := command.Output()
	require.NoError(t, err)
	var snapshot nodeStoreSnapshot
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(output))), &snapshot))
	return snapshot
}

func migrationIDs() []string {
	migrations := DefaultMigrations()
	ids := make([]string, 0, len(migrations))
	for _, migration := range migrations {
		ids = append(ids, migration.ID)
	}
	return ids
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
