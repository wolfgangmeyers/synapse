package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type managedPiSessionBinder interface {
	BindManagedPiSession(context.Context, string, string, string, string, ManagedRuntimeFence) error
}

func TestBindManagedPiSessionRejectsStaleProcessAndAllowsOneWinner(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	const (
		agentID      = "migration-061-bind-contention"
		launchID     = "migration-061-bind-launch"
		reservation  = "migration-061-bind-reservation"
		relativePath = "sessions/migration-061-bind.jsonl"
	)

	oldFence := ManagedRuntimeFence{
		AgentID: agentID, PiSessionID: "provisional", SessionEpoch: 1,
		ProcessPID: 961, ProcessStartMarker: "old-start", ProcessIncarnation: "old-incarnation",
		RuntimeGeneration: "old-generation", ManifestRevision: 1, ManifestHash: testManifestHash,
	}
	orderlyStopMarkerTestAgent(t, durable, agentID, launchID, oldFence)
	require.NoError(t, migration061InsertReservation(durable, reservation, agentID, 1, launchID, relativePath, migration061Hash(61), nil, "reserved", nil))

	currentFence := oldFence
	currentFence.ProcessPID = 962
	currentFence.ProcessStartMarker = "replacement-start"
	currentFence.ProcessIncarnation = "replacement-incarnation"
	currentFence.RuntimeGeneration = "replacement-generation"
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_agents SET process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=?`, currentFence.ProcessPID, currentFence.ProcessStartMarker, currentFence.ProcessIncarnation, currentFence.RuntimeGeneration, agentID)
	require.NoError(t, err)
	_, err = durable.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=?`, currentFence.ProcessPID, currentFence.ProcessStartMarker, currentFence.ProcessIncarnation, currentFence.RuntimeGeneration, agentID)
	require.NoError(t, err)

	binder, ok := any(durable).(managedPiSessionBinder)
	require.True(t, ok, "Store must provide fenced BindManagedPiSession capability")

	err = binder.BindManagedPiSession(ctx, reservation, launchID, "stale-session", relativePath, oldFence)
	require.Error(t, err, "stale get_state callback from the prior process must fail closed")
	assertMigration061ReservationBinding(t, durable, reservation, "reserved", "")

	require.NoError(t, binder.BindManagedPiSession(ctx, reservation, launchID, "winning-session", relativePath, currentFence))
	require.Error(t, binder.BindManagedPiSession(ctx, reservation, launchID, "losing-session", relativePath, currentFence), "two competing binders must not both win")
	assertMigration061ReservationBinding(t, durable, reservation, "bound", "winning-session")
}

func assertMigration061ReservationBinding(t *testing.T, durable *Store, reservationID, wantState, wantSessionID string) {
	t.Helper()
	var state string
	var sessionID *string
	require.NoError(t, durable.db.QueryRow(`SELECT state,pi_session_id FROM managed_stock_pi_session_reservations WHERE reservation_id=?`, reservationID).Scan(&state, &sessionID))
	require.Equal(t, wantState, state)
	if wantSessionID == "" {
		require.Nil(t, sessionID)
		return
	}
	require.NotNil(t, sessionID)
	require.Equal(t, wantSessionID, *sessionID)
}
