package managed

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedBrowserResetOverlapAdmission(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reset.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	m := makeSubagentTestManager(t, db, reg, 4)
	defer m.Close(ctx)
	binding := subagentTestParent(t, db, "overlap-reset")
	registerParentOwner(m, binding)
	saved, err := db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	req := ResetRequest{AgentID: binding.AgentID, Method: "POST", CanonicalPath: "/reset", RequestID: "overlap-key", CanonicalBodyHash: "hash"}
	lane := m.laneFor(binding.AgentID)
	// A live coordinator is the barrier; admission is reopened during wake but ownership remains.
	_, ok := lane.beginEmbeddedReset()
	require.True(t, ok)
	lane.reopenEmbeddedResetAdmissions()
	_, err = m.reserveEmbeddedReset(ctx, req, saved)
	var notReady *store.ManagedAgentNotReadyError
	require.ErrorAs(t, err, &notReady)
	_, err = db.FindManagedOperation(ctx, req.Method, req.RequestID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.True(t, lane.embeddedResetRunning(), "busy request must not release owner")
	// Store an unrelated accepted operation while the coordinator is held to test
	// the in-selection recheck rather than the public method's early replay.
	op := store.ManagedOperation{OperationID: "replay-key", Method: req.Method, CanonicalPath: req.CanonicalPath, RequestID: "replay-key", CanonicalBodyHash: req.CanonicalBodyHash, ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	_, _, err = db.ReserveEmbeddedReset(ctx, binding.AgentID, op)
	require.NoError(t, err)
	replay := req
	replay.RequestID = op.RequestID
	result, err := m.reserveEmbeddedReset(ctx, replay, saved)
	require.NoError(t, err)
	require.True(t, result.Replayed)
	replay.CanonicalBodyHash = "different"
	_, err = m.reserveEmbeddedReset(ctx, replay, saved)
	var conflict *store.ManagedIdempotencyConflictError
	require.True(t, errors.As(err, &conflict))
	require.True(t, lane.embeddedResetRunning())
	lane.endEmbeddedReset()
	// Force a reservation failure after acquiring the coordinator.
	_, err = raw.Exec(`CREATE TRIGGER reject_overlap BEFORE INSERT ON managed_http_operations BEGIN SELECT RAISE(ABORT,'reservation fault'); END`)
	require.NoError(t, err)
	_, err = m.reserveEmbeddedReset(ctx, req, saved)
	require.Error(t, err)
	require.False(t, lane.embeddedResetRunning())
	_, err = db.FindManagedOperation(ctx, req.Method, req.RequestID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = raw.Exec(`DROP TRIGGER reject_overlap`)
	require.NoError(t, err)
	// Hold serial so the successful retry cannot finish before its ownership is checked.
	lane.serial.Lock()
	result, err = m.reserveEmbeddedReset(ctx, req, saved)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.True(t, lane.embeddedResetRunning())
	lane.serial.Unlock()
}
