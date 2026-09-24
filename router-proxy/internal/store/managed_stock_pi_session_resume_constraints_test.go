package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration061CreatesCompleteStockSessionReservationSchema(t *testing.T) {
	durable := openTestStore(t)

	rows, err := durable.db.Query(`PRAGMA table_info('managed_stock_pi_session_reservations')`)
	require.NoError(t, err)
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		columns = append(columns, name)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{
		"reservation_id",
		"agent_id",
		"session_epoch",
		"launch_operation_id",
		"root_relative_path",
		"path_sha256",
		"pi_session_id",
		"state",
		"session_artifact_id",
		"created_at",
		"updated_at",
	}, columns)
}

func TestMigration061ConstrainsReservationStates(t *testing.T) {
	durable := openTestStore(t)
	agentID := "migration-061-states"
	migration061TestAgent(t, durable, agentID)

	for index, state := range []string{"reserved", "bound", "materialized", "retired"} {
		err := migration061InsertReservation(durable, fmt.Sprintf("reservation-%d", index), agentID, int64(index+2), fmt.Sprintf("launch-%d", index), fmt.Sprintf("sessions/%d.json", index), migration061Hash(index+1), fmt.Sprintf("pi-session-%d", index), state, nil)
		require.NoErrorf(t, err, "state %q must be accepted", state)
	}

	err := migration061InsertReservation(durable, "reservation-invalid", agentID, 20, "launch-invalid", "sessions/invalid.json", migration061Hash(20), "pi-session-invalid", "invalid", nil)
	require.Error(t, err)
}

func TestMigration061EnforcesReservationUniqueness(t *testing.T) {
	durable := openTestStore(t)
	agentID := "migration-061-unique"
	migration061TestAgent(t, durable, agentID)

	require.NoError(t, migration061InsertReservation(durable, "reservation-base", agentID, 2, "launch-base", "sessions/base.json", migration061Hash(1), "pi-session-base", "reserved", nil))

	tests := []struct {
		name       string
		epoch      int64
		launchID   string
		pathSHA256 string
		piSession  any
	}{
		{name: "launch_operation_id", epoch: 3, launchID: "launch-base", pathSHA256: migration061Hash(2), piSession: "pi-session-2"},
		{name: "agent_and_session_epoch", epoch: 2, launchID: "launch-3", pathSHA256: migration061Hash(3), piSession: "pi-session-3"},
		{name: "path_sha256", epoch: 4, launchID: "launch-4", pathSHA256: migration061Hash(1), piSession: "pi-session-4"},
		{name: "non_null_pi_session_id", epoch: 5, launchID: "launch-5", pathSHA256: migration061Hash(5), piSession: "pi-session-base"},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := migration061InsertReservation(durable, fmt.Sprintf("reservation-duplicate-%d", index), agentID, testCase.epoch, testCase.launchID, fmt.Sprintf("sessions/duplicate-%d.json", index), testCase.pathSHA256, testCase.piSession, "reserved", nil)
			require.Error(t, err)
		})
	}

	require.NoError(t, migration061InsertReservation(durable, "reservation-null-1", agentID, 10, "launch-null-1", "sessions/null-1.json", migration061Hash(10), nil, "reserved", nil))
	require.NoError(t, migration061InsertReservation(durable, "reservation-null-2", agentID, 11, "launch-null-2", "sessions/null-2.json", migration061Hash(11), nil, "reserved", nil))
}

func TestMigration061EnforcesSessionArtifactForeignKeys(t *testing.T) {
	durable := openTestStore(t)
	agentID := "migration-061-foreign-keys"
	migration061TestAgent(t, durable, agentID)

	err := migration061InsertReservation(durable, "reservation-missing-artifact", agentID, 2, "launch-missing-artifact", "sessions/missing.json", migration061Hash(1), nil, "reserved", "missing-artifact")
	require.Error(t, err)

	artifact := migration061TestArtifact(agentID, 2, "migration-061-artifact")
	require.NoError(t, durable.RecordManagedSessionArtifact(context.Background(), artifact))
	require.NoError(t, migration061InsertReservation(durable, "reservation-valid-artifact", agentID, 3, "launch-valid-artifact", "sessions/valid.json", migration061Hash(2), nil, "reserved", artifact.ArtifactID))
}

func TestMigration061PreservesLegacyRowsAndLinksResumeArtifacts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	migration061 := migrationIndex(t, migrations, "061_managed_stock_pi_session_resume")

	before, err := Open(ctx, path, Options{Migrations: migrations[:migration061]})
	require.NoError(t, err)
	fence := ManagedRuntimeFence{
		AgentID: "migration-061-legacy", PiSessionID: "legacy-session", SessionEpoch: 1,
		ProcessPID: 861, ProcessStartMarker: "legacy-start", ProcessIncarnation: "legacy-incarnation",
		RuntimeGeneration: "legacy-generation", ManifestRevision: 1, ManifestHash: testManifestHash,
	}
	orderlyStopMarkerTestAgent(t, before, fence.AgentID, "migration-061-legacy-launch", fence)
	require.NoError(t, before.MarkManagedOrderlyStopped(ctx, fence))
	action, _, err := before.ClaimManagedRootRecovery(ctx, fence.AgentID, fence, testManifestHash, "replacement-generation")
	require.NoError(t, err)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()

	for table, key := range map[string]struct {
		column string
		value  string
	}{
		"managed_orderly_stop_markers":  {column: "agent_id", value: fence.AgentID},
		"managed_root_recovery_actions": {column: "action_id", value: action.ActionID},
	} {
		var resumeArtifactID sql.NullString
		query := fmt.Sprintf("SELECT resume_artifact_id FROM %s WHERE %s=?", table, key.column)
		require.NoError(t, after.db.QueryRowContext(ctx, query, key.value).Scan(&resumeArtifactID))
		require.Falsef(t, resumeArtifactID.Valid, "%s legacy row must have a NULL resume_artifact_id", table)

		update := fmt.Sprintf("UPDATE %s SET resume_artifact_id=? WHERE %s=?", table, key.column)
		_, err = after.db.ExecContext(ctx, update, "missing-artifact", key.value)
		require.Error(t, err)
	}

	artifact := migration061TestArtifact(fence.AgentID, 2, "migration-061-resume-artifact")
	require.NoError(t, after.RecordManagedSessionArtifact(ctx, artifact))
	_, err = after.db.ExecContext(ctx, `UPDATE managed_orderly_stop_markers SET resume_artifact_id=? WHERE agent_id=?`, artifact.ArtifactID, fence.AgentID)
	require.NoError(t, err)
	_, err = after.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET resume_artifact_id=? WHERE action_id=?`, artifact.ArtifactID, action.ActionID)
	require.NoError(t, err)
}

func migration061TestAgent(t *testing.T, durable *Store, agentID string) {
	t.Helper()
	fence := ManagedRuntimeFence{
		AgentID: agentID, PiSessionID: "session-" + agentID, SessionEpoch: 1,
		ProcessPID: 862, ProcessStartMarker: "start-" + agentID, ProcessIncarnation: "incarnation-" + agentID,
		RuntimeGeneration: "generation-" + agentID, ManifestRevision: 1, ManifestHash: testManifestHash,
	}
	orderlyStopMarkerTestAgent(t, durable, agentID, "launch-"+agentID, fence)
}

func migration061TestArtifact(agentID string, epoch int64, artifactID string) ManagedSessionArtifact {
	return ManagedSessionArtifact{
		ArtifactID: lifecycleUUID(961), AgentID: agentID, SessionEpoch: epoch,
		PiSessionID: "artifact-session-" + artifactID, RootRelativePath: "sessions/" + artifactID + ".json",
		PathSHA256: migration061Hash(int(epoch) + 30), DiscoveryState: "not_materialized", CleanupState: "retained",
		DiscoveredAt: "2026-09-14T00:00:00.000Z", CleanupUpdatedAt: "2026-09-14T00:00:00.000Z",
	}
}

func migration061InsertReservation(durable *Store, reservationID, agentID string, epoch int64, launchID, relativePath, pathSHA256 string, piSessionID any, state string, artifactID any) error {
	_, err := durable.db.Exec(`INSERT INTO managed_stock_pi_session_reservations (
		reservation_id,agent_id,session_epoch,launch_operation_id,root_relative_path,path_sha256,
		pi_session_id,state,session_artifact_id,created_at,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, reservationID, agentID, epoch, launchID, relativePath, pathSHA256, piSessionID, state, artifactID, "2026-09-14T00:00:00.000Z", "2026-09-14T00:00:00.000Z")
	return err
}

func migration061Hash(value int) string {
	return fmt.Sprintf("%064x", value)
}
