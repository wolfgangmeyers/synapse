package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestEmbeddedSubagentLeaseRenewalFailureClassifiedAsError(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_lease.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-lease-test")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var running atomic.Bool
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			running.Store(true)
			// Block stream until aborted
			time.Sleep(10 * time.Second)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "unreachable"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	childID := "subagent-lease-fail-1"
	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     childID,
		Description: "lease test child",
		Prompt:      "run until lease fails",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return running.Load()
	}, 3*time.Second, 20*time.Millisecond)

	// Read actual child lease details from SQLite
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	var ownerID string
	var ownerToken int64
	require.Eventually(t, func() bool {
		err := rawDB.QueryRowContext(ctx, `SELECT owner_id, owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, childID).Scan(&ownerID, &ownerToken)
		return err == nil && ownerID != "" && ownerToken > 0
	}, 3*time.Second, 20*time.Millisecond)

	childBinding := store.EmbeddedBinding{
		AgentID:    childID,
		SessionID:  childID + "-session",
		Epoch:      1,
		Generation: parent.Generation,
		OwnerToken: ownerToken,
	}

	// Release owner lease via Store API so next renewal fails
	require.NoError(t, db.ReleaseEmbeddedOwner(ctx, childBinding, ownerID))

	// Acquire competing lease to advance token
	_, err = db.AcquireEmbeddedOwner(ctx, childBinding, "stealer-owner", time.Minute)
	require.NoError(t, err)

	// Wait for runner to fail renewal and classify status as error
	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, childID)
		if err != nil {
			return false
		}
		return rec.Status == "error"
	}, 15*time.Second, 100*time.Millisecond)

	rec, err := db.ManagedSubagent(ctx, parent, childID)
	require.NoError(t, err)
	require.Equal(t, "error", rec.Status)
	require.Contains(t, rec.Error, "owner lease renewal failed")

	// Verify registry and active slots cleaned up
	mgr.subagentsMu.Lock()
	_, inRegistry := mgr.subagentsRegistry[childID]
	_, inActive := mgr.subagentsActive[childID]
	mgr.subagentsMu.Unlock()
	require.False(t, inRegistry)
	require.False(t, inActive)

	// Assert old child owner append/renew is denied
	require.Error(t, db.RenewEmbeddedOwner(ctx, childBinding, ownerID, time.Minute))

	// Assert parent owner is alive and root epoch unchanged
	require.NoError(t, db.RenewEmbeddedOwner(ctx, parent, "fixture:"+parent.AgentID, time.Minute))
	parentAgent, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	require.Equal(t, parent.Epoch, parentAgent.SessionEpoch)
}
