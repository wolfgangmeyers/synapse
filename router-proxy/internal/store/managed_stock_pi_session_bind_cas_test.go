package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// BindManagedPiSession arguments are reservationID, launchOperationID,
// piSessionID, exact rootRelativePath, and the full current runtime fence.
type managedStockPiSessionCASBinder interface {
	BindManagedPiSession(context.Context, string, string, string, string, ManagedRuntimeFence) error
}

func TestManagedStockPiSessionBindRequiresLaunchAndProcessCASCapability(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	binder, supported := any(durable).(managedStockPiSessionCASBinder)
	require.True(t, supported, "stock Pi session binding must expose a launch-operation and process-fenced CAS")

	const (
		agentID         = "migration-061-bind-cas"
		reservationID   = "migration-061-bind-reservation"
		launchOperation = "migration-061-bind-launch"
		relativePath    = "sessions/migration-061-bind.jsonl"
	)
	priorFence := ManagedRuntimeFence{
		AgentID: agentID, PiSessionID: "provisional-session", SessionEpoch: 1,
		ProcessPID: 971, ProcessStartMarker: "prior-start", ProcessIncarnation: "prior-incarnation",
		RuntimeGeneration: "prior-generation", ManifestRevision: 1, ManifestHash: testManifestHash,
	}
	orderlyStopMarkerTestAgent(t, durable, agentID, launchOperation, priorFence)
	require.NoError(t, migration061InsertReservation(durable, reservationID, agentID, priorFence.SessionEpoch, launchOperation, relativePath, migration061Hash(71), nil, "reserved", nil))

	currentFence := priorFence
	currentFence.ProcessPID = 972
	currentFence.ProcessStartMarker = "current-start"
	currentFence.ProcessIncarnation = "current-incarnation"
	currentFence.RuntimeGeneration = "current-generation"
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_agents SET process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=?`, currentFence.ProcessPID, currentFence.ProcessStartMarker, currentFence.ProcessIncarnation, currentFence.RuntimeGeneration, agentID)
	require.NoError(t, err)
	_, err = durable.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=?`, currentFence.ProcessPID, currentFence.ProcessStartMarker, currentFence.ProcessIncarnation, currentFence.RuntimeGeneration, agentID)
	require.NoError(t, err)
	currentSource, err := durable.ManagedReadinessSource(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, &currentFence, currentSource.Fence)

	assertStaleManagedPiSessionBind(t, binder.BindManagedPiSession(ctx, reservationID, launchOperation, "stale-session", relativePath, priorFence))
	assertMigration061ReservationBinding(t, durable, reservationID, "reserved", "")

	assertStaleManagedPiSessionBind(t, binder.BindManagedPiSession(ctx, reservationID, "changed-operation", "wrong-operation-session", relativePath, currentFence))
	assertStaleManagedPiSessionBind(t, binder.BindManagedPiSession(ctx, reservationID, launchOperation, "wrong-path-session", "sessions/changed.jsonl", currentFence))
	changedFence := currentFence
	changedFence.ManifestRevision++
	assertStaleManagedPiSessionBind(t, binder.BindManagedPiSession(ctx, reservationID, launchOperation, "wrong-fence-session", relativePath, changedFence))
	assertMigration061ReservationBinding(t, durable, reservationID, "reserved", "")

	require.NoError(t, binder.BindManagedPiSession(ctx, reservationID, launchOperation, "winning-session", relativePath, currentFence))
	assertMigration061ReservationBinding(t, durable, reservationID, "bound", "winning-session")
	generatedFence := currentFence
	generatedFence.PiSessionID = "winning-session"
	source, err := durable.ManagedReadinessSource(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, &generatedFence, source.Fence, "generated Pi session ID must be immediately usable by exact readiness checks")

	assertStaleManagedPiSessionBind(t, binder.BindManagedPiSession(ctx, reservationID, launchOperation, "different-session", relativePath, currentFence))
	assertMigration061ReservationBinding(t, durable, reservationID, "bound", "winning-session")
}

func TestBindManagedPiSessionRollsBackWhenExactReadinessSourceIsStale(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)

	const (
		agentID         = "migration-061-readiness-cas"
		reservationID   = "migration-061-readiness-reservation"
		launchOperation = "migration-061-readiness-launch"
		relativePath    = "sessions/migration-061-readiness.jsonl"
	)
	fence := ManagedRuntimeFence{
		AgentID: agentID, PiSessionID: "provisional-session", SessionEpoch: 1,
		ProcessPID: 981, ProcessStartMarker: "readiness-start", ProcessIncarnation: "readiness-incarnation",
		RuntimeGeneration: "readiness-generation", ManifestRevision: 1, ManifestHash: testManifestHash,
	}
	orderlyStopMarkerTestAgent(t, durable, agentID, launchOperation, fence)
	require.NoError(t, migration061InsertReservation(durable, reservationID, agentID, fence.SessionEpoch, launchOperation, relativePath, migration061Hash(72), nil, "reserved", nil))
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET runtime_generation='stale-readiness-generation' WHERE agent_id=?`, agentID)
	require.NoError(t, err)

	assertStaleManagedPiSessionBind(t, durable.BindManagedPiSession(ctx, reservationID, launchOperation, "generated-session", relativePath, fence))
	assertMigration061ReservationBinding(t, durable, reservationID, "reserved", "")

	var agentSessionID, sourceSessionID string
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT pi_session_id FROM managed_agents WHERE agent_id=?`, agentID).Scan(&agentSessionID))
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT pi_session_id FROM managed_readiness_sources WHERE agent_id=?`, agentID).Scan(&sourceSessionID))
	require.Equal(t, fence.PiSessionID, agentSessionID)
	require.Equal(t, fence.PiSessionID, sourceSessionID)
}

func assertStaleManagedPiSessionBind(t *testing.T, err error) {
	t.Helper()
	var stale *ManagedPiSessionBindStaleError
	require.Error(t, err)
	require.True(t, errors.As(err, &stale), "expected typed stale bind error, got %T: %v", err, err)
}
