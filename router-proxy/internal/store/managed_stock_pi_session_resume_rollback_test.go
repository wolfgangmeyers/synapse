package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration061RollbackOnPartialSchemaFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	migration061 := migrationIndex(t, migrations, "061_managed_stock_pi_session_resume")

	before, err := Open(ctx, path, Options{Migrations: migrations[:migration061]})
	require.NoError(t, err)
	fence := ManagedRuntimeFence{
		AgentID: "migration-061-rollback", PiSessionID: "legacy-session", SessionEpoch: 1,
		ProcessPID: 861, ProcessStartMarker: "legacy-start", ProcessIncarnation: "legacy-incarnation",
		RuntimeGeneration: "legacy-generation", ManifestRevision: 1, ManifestHash: testManifestHash,
	}
	orderlyStopMarkerTestAgent(t, before, fence.AgentID, "migration-061-rollback-launch", fence)
	require.NoError(t, before.MarkManagedOrderlyStopped(ctx, fence))
	require.NoError(t, before.Close())

	failingMigrations := append([]Migration(nil), migrations[:migration061+1]...)
	const secondAlter = "ALTER TABLE managed_root_recovery_actions"
	failingMigrations[migration061].SQL = strings.Replace(
		failingMigrations[migration061].SQL,
		secondAlter,
		"SELECT * FROM migration_061_forced_failure;\n"+secondAlter,
		1,
	)
	require.Contains(t, failingMigrations[migration061].SQL, "migration_061_forced_failure")

	_, err = Open(ctx, path, Options{Migrations: failingMigrations})
	require.Error(t, err)

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()

	for _, object := range []struct {
		objectType string
		name       string
	}{
		{objectType: "table", name: "managed_stock_pi_session_reservations"},
		{objectType: "index", name: "managed_stock_pi_session_id_idx"},
	} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, object.objectType, object.name).Scan(&count))
		require.Zero(t, count, "%s %s must roll back", object.objectType, object.name)
	}

	for _, table := range []string{"managed_orderly_stop_markers", "managed_root_recovery_actions"} {
		var columns int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name='resume_artifact_id'`, table).Scan(&columns))
		require.Zero(t, columns, "%s resume_artifact_id must roll back", table)
	}

	var migrationRows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='061_managed_stock_pi_session_resume'`).Scan(&migrationRows))
	require.Zero(t, migrationRows, "failed migration must not be recorded")

	var legacyRows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM managed_orderly_stop_markers WHERE agent_id=?`, fence.AgentID).Scan(&legacyRows))
	require.Equal(t, 1, legacyRows, "schema-060 legacy row must survive rollback")
}
