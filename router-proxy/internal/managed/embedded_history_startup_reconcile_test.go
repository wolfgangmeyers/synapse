package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestStartupReconciliationDanglingHistoryOutcome(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	mgr, err := NewManager(ManagerOptions{
		Store:    db,
		Manifest: manifest,
		Config:   Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
	})
	require.NoError(t, err)
	defer mgr.Close(ctx)

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	// Insert dangling active history entry
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at, presentation_json)
		VALUES ('workflows', 'managed_tool', 'info', 'Running bash', '2026-10-08T14:40:34.372Z', '{"kind":"tool","tool_name":"bash","outcome":"active","summary":"Running"}')`)
	require.NoError(t, err)

	// Insert completed history entry
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at, presentation_json)
		VALUES ('workflows', 'managed_tool', 'info', 'Ran echo', '2026-10-08T14:40:30.000Z', '{"kind":"tool","tool_name":"bash","outcome":"completed","summary":"Succeeded"}')`)
	require.NoError(t, err)

	// Run startup reconciliation via Manager
	require.NoError(t, mgr.ResetActiveEmbeddedAgents(ctx))

	var activeOutcome, activeSummary string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Running bash'`).Scan(&activeOutcome, &activeSummary))
	require.Equal(t, "failed", activeOutcome)
	require.Equal(t, "Interrupted by restart", activeSummary)

	var completedOutcome, completedSummary string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Ran echo'`).Scan(&completedOutcome, &completedSummary))
	require.Equal(t, "completed", completedOutcome)
	require.Equal(t, "Succeeded", completedSummary)

	// Idempotency: running again should not change anything
	require.NoError(t, mgr.ResetActiveEmbeddedAgents(ctx))
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Running bash'`).Scan(&activeOutcome, &activeSummary))
	require.Equal(t, "failed", activeOutcome)
	require.Equal(t, "Interrupted by restart", activeSummary)
}
