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
)

func TestSubagentActiveAcrossRouterRestartResumesAndCompletes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	reg.SetResponses([]providers.FauxResponseStep{
		func(hist ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Subagent step before restart."},
			}, ai.StopStop)
		},
		func(hist ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Subagent completed work successfully after restart."},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)

	parentBinding := subagentTestParent(t, db, "parent-worker-resume")

	childID := "child-resume-1"
	childDesc := "investigate vertex model error"
	spawnRes, err := mgr.SpawnSubagent(ctx, parentBinding, SubagentSpawnRequest{
		AgentID:      childID,
		Description:  childDesc,
		SubagentType: "general-purpose",
		Prompt:       "Please investigate Vertex ADC",
	})
	require.NoError(t, err)
	require.Equal(t, "running", spawnRes.Status)

	// Close manager and store to simulate restart
	require.NoError(t, mgr.Close(ctx))
	require.NoError(t, db.Close())

	// Verify in SQLite that the subagent remains in status running (not prematurely aborted or stopped)
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	var status, errorMsg string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT status, error FROM managed_subagents WHERE agent_id=?`, childID).Scan(&status, &errorMsg))
	require.Equal(t, "running", status, "subagent must retain running status across restart: "+errorMsg)

	// Reopen store and manager (restarting router)
	reopenedDB, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer reopenedDB.Close()

	reopenedMgr := makeSubagentTestManager(t, reopenedDB, reg, 4)
	defer reopenedMgr.Close(ctx)

	// Recover embedded roots - marks in-flight subagents as failed / interrupted
	require.NoError(t, reopenedMgr.RecoverEmbeddedRoots(ctx))

	// Verify the subagent transitions to failed with error interrupted
	var lastSt, lastErr string
	require.Eventually(t, func() bool {
		_ = rawDB.QueryRowContext(ctx, `SELECT status, error FROM managed_subagents WHERE agent_id=?`, childID).Scan(&lastSt, &lastErr)
		t.Logf("poll: status=%s error=%s", lastSt, lastErr)
		return lastSt == "failed" && lastErr == "interrupted"
	}, 3*time.Second, 200*time.Millisecond)

	// Verify interrupted notification mailbox message was delivered to parent
	var msgCount int
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id LIKE ?`, "subagent-interrupted-"+childID+"-%").Scan(&msgCount))
	require.GreaterOrEqual(t, msgCount, 1, "interrupted mailbox message must be delivered to parent")
}
