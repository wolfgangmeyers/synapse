package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestEmbeddedSubagentRunsWithoutOwnerLease(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_no_lease.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-no-lease-test")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "child completed without lease"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	childID := "subagent-no-lease-1"
	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     childID,
		Description: "no lease child",
		Prompt:      "run and complete",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, childID)
		if err != nil {
			return false
		}
		return rec.Status == "completed"
	}, 5*time.Second, 50*time.Millisecond)

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	// Verify child session in embedded_sessions has no owner lease (owner_id is NULL or empty, owner_token is 0)
	var ownerID sql.NullString
	var ownerToken int64
	var role string
	err = rawDB.QueryRowContext(ctx, `SELECT owner_id, owner_token, role FROM embedded_sessions WHERE agent_id=? AND current=1`, childID).Scan(&ownerID, &ownerToken, &role)
	require.NoError(t, err)
	require.Equal(t, "subagent", role)
	require.False(t, ownerID.Valid && ownerID.String != "", "child subagent must not hold an owner identity")
	require.Zero(t, ownerToken, "child subagent must not have acquired an owner lease token")

	// Verify child record completed
	rec, err := db.ManagedSubagent(ctx, parent, childID)
	require.NoError(t, err)
	require.Equal(t, "completed", rec.Status)
	require.Equal(t, "child completed without lease", rec.ResultText)

	// Verify registry and active slots cleaned up
	mgr.subagentsMu.Lock()
	_, inRegistry := mgr.subagentsRegistry[childID]
	_, inActive := mgr.subagentsActive[childID]
	mgr.subagentsMu.Unlock()
	require.False(t, inRegistry)
	require.False(t, inActive)
}
