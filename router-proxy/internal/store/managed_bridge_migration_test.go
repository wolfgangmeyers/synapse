package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedBridgeMigration049IsRegisteredAfterReservedGapAndCreatesSchema(t *testing.T) {
	migrations := DefaultMigrations()
	ids := make([]string, 0, len(migrations))
	for _, migration := range migrations {
		ids = append(ids, migration.ID)
	}
	index := make(map[string]int, len(ids))
	for position, id := range ids {
		index[id] = position
	}
	require.Contains(t, index, "047_managed_crash_policy")
	require.Contains(t, index, "049_managed_bridge_store")
	require.Less(t, index["047_managed_crash_policy"], index["049_managed_bridge_store"])
	durable, err := Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	var applied int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='049_managed_bridge_store'`).Scan(&applied))
	require.Equal(t, 1, applied)
	for _, table := range []string{"managed_bridges", "managed_bridge_commands", "managed_bridge_events"} {
		var present int
		require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&present))
		require.Equal(t, 1, present, table)
	}
}
