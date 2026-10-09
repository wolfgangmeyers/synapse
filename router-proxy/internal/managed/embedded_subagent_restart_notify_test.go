package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestReconcileInterruptedSubagentsNotifiesParent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}),
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
	})
	require.NoError(t, err)

	// Create parent embedded worker using standard fixture helper
	parentBinding := subagentTestParent(t, db, "parent-worker-restart")

	childID := "child-interrupted-1"
	childDesc := "scrape external documentation"
	err = db.CreateManagedSubagent(ctx, parentBinding, store.ManagedSubagentRecord{
		AgentID:       childID,
		ParentAgentID: parentBinding.AgentID,
		Description:   childDesc,
		Status:        "running",
	})
	require.NoError(t, err)

	// Close store and manager to simulate router shutdown/crash
	require.NoError(t, mgr.Close(ctx))
	require.NoError(t, db.Close())

	// Set status to interrupted in raw DB to simulate interrupted subagent
	rawInitDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = rawInitDB.ExecContext(ctx, `UPDATE managed_subagents SET status='interrupted', error='router process terminated abruptly' WHERE agent_id=?`, childID)
	require.NoError(t, err)
	require.NoError(t, rawInitDB.Close())

	// Reopen store
	reopenedDB, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer reopenedDB.Close()

	// Verify the subagent remains interrupted
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	var status, errorText string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT status, error FROM managed_subagents WHERE agent_id=?`, childID).Scan(&status, &errorText))
	require.Equal(t, "interrupted", status)
	require.Equal(t, "router process terminated abruptly", errorText)

	// Create new manager on reopened store
	reopenedMgr, err := NewManager(ManagerOptions{
		Store:                 reopenedDB,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}),
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
	})
	require.NoError(t, err)
	defer reopenedMgr.Close(ctx)

	// Run ReconcileInterruptedSubagents
	err = reopenedMgr.ReconcileInterruptedSubagents(ctx)
	require.NoError(t, err)

	// Verify notification message in store
	var msgID string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT id FROM messages WHERE id LIKE ?`, "subagent-interrupted-"+childID+"-%").Scan(&msgID))
	msg, err := reopenedDB.MessageFor(ctx, msgID)
	require.NoError(t, err)
	require.NotNil(t, msg, "mailbox message must be enqueued for interrupted subagent")
	require.Equal(t, "system", msg.From)
	require.Equal(t, parentBinding.AgentID, msg.To)
	require.NotNil(t, msg.Subject)
	require.Equal(t, "[System Notice] Subagent Interrupted: "+childID, *msg.Subject)
	expectedBody := "Subagent " + childID + " (\"" + childDesc + "\") was interrupted due to a router process restart. Partial progress and logs have been saved. You can inspect its results using get_subagent_result(agent_id=\"" + childID + "\").\n\nAutomated system notification. No mailbox reply is needed or expected. Proceed directly with your next step."
	require.Equal(t, expectedBody, msg.Body)

	// Running ReconcileInterruptedSubagents again must be idempotent
	err = reopenedMgr.ReconcileInterruptedSubagents(ctx)
	require.NoError(t, err)

	var count int
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE id LIKE ?`, "subagent-interrupted-"+childID+"-%").Scan(&count))
	require.Equal(t, 1, count, "exactly one notification message must exist")
}
