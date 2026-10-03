package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration086PreservesExistingRowsAndEnforcesForeignKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()

	// 1. Run migrations up to 085 (pre-086)
	idx086 := migrationIndex(t, migrations, "086_google_auth_methods")
	before, err := Open(ctx, path, Options{Migrations: migrations[:idx086]})
	require.NoError(t, err)

	// Update pre-existing 3 providers with full 7 columns in managed_provider_status
	_, err = before.db.Exec(`UPDATE managed_provider_status SET authenticated=1, status='authenticated', account_label='user@example.com', updated_at='2026-09-01T00:00:00.000Z' WHERE provider='openai-codex'`)
	require.NoError(t, err)
	_, err = before.db.Exec(`UPDATE managed_provider_status SET authenticated=1, status='authenticated', account_label='api-key-12345', updated_at='2026-09-01T00:00:00.000Z' WHERE provider='google'`)
	require.NoError(t, err)
	_, err = before.db.Exec(`UPDATE managed_provider_status SET authenticated=0, status='signed_out', error='token expired', updated_at='2026-09-01T00:00:00.000Z' WHERE provider='cursor'`)
	require.NoError(t, err)

	// Seed child rows in managed_provider_models referencing pre-existing providers
	_, err = before.db.Exec(`INSERT INTO managed_provider_models (provider, model_id, name, available, updated_at) VALUES
		('openai-codex', 'gpt-5.6-terra', 'GPT-5.6 Terra', 1, '2026-09-01T00:00:00.000Z'),
		('cursor', 'cursor-fast', 'Cursor Fast', 1, '2026-09-01T00:00:00.000Z')`)
	require.NoError(t, err)

	// Seed managed_agents and a legacy recipe without antigravity_extension
	agentID := "test-agent-legacy"
	agent := ManagedAgentRecord{
		AgentID:          agentID,
		Name:             agentID,
		SlotHeld:         true,
		Cwd:              "/tmp",
		SelectedModel:    ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:     "running",
		LifecycleState:   "starting",
		ActivityState:    "starting",
		PiSessionID:      "session-1",
		SessionEpoch:     1,
		ManifestRevision: 1,
		ManifestHash:     testManifestHash,
	}
	op := ManagedOperation{
		OperationID:       "cold-launch-legacy",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "cold-launch-legacy",
		CanonicalBodyHash: "body",
		State:             "pending",
		ResponseStatus:    202,
		ResponseHeaders:   map[string]string{},
		ResponseBody:      []byte(`{}`),
	}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, op))

	legacyRecipe := managedRootRecoveryRecipeFixture()
	testJSON, err := json.Marshal(legacyRecipe.TestOnlyExtensions)
	require.NoError(t, err)
	envJSON, err := json.Marshal(legacyRecipe.Environment)
	require.NoError(t, err)
	_, err = before.db.Exec(`INSERT INTO managed_launch_recipes(agent_id,pi_executable,session_root,cwd,provider,model_id,mecha_rpc_extension,pi_subagents_extension,cursor_extension,test_extensions_json,environment_json,recipe_sha256,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		agentID, legacyRecipe.PiExecutable, legacyRecipe.SessionRoot, legacyRecipe.Cwd, legacyRecipe.Provider, legacyRecipe.ModelID, legacyRecipe.MechaRPCExtension, legacyRecipe.PiSubagentsExtension, legacyRecipe.CursorExtension, string(testJSON), string(envJSON), legacyRecipe.SHA256, "2026-09-01T00:00:00.000Z", "2026-09-01T00:00:00.000Z")
	require.NoError(t, err)

	require.NoError(t, before.Close())

	// 2. Open with all migrations including 086
	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, after.Close()) })

	// Verify PRAGMA foreign_key_check passes with 0 violations
	rows, err := after.db.Query(`PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer rows.Close()
	var fkViolations []string
	for rows.Next() {
		var table, parent string
		var rowid, fkid int
		require.NoError(t, rows.Scan(&table, &rowid, &parent, &fkid))
		fkViolations = append(fkViolations, table+"->"+parent)
	}
	require.NoError(t, rows.Err())
	require.Empty(t, fkViolations, "foreign key check must pass with zero violations")

	// Verify all 5 providers exist in managed_provider_status
	var statusCount int
	require.NoError(t, after.db.QueryRow(`SELECT count(*) FROM managed_provider_status`).Scan(&statusCount))
	require.Equal(t, 5, statusCount)

	// Verify legacy status rows survived with all 7 columns intact
	var accountLabel, errorVal string
	var authenticated int
	var status string
	require.NoError(t, after.db.QueryRow(`SELECT authenticated, status, account_label FROM managed_provider_status WHERE provider='openai-codex'`).Scan(&authenticated, &status, &accountLabel))
	require.Equal(t, 1, authenticated)
	require.Equal(t, "authenticated", status)
	require.Equal(t, "user@example.com", accountLabel)

	require.NoError(t, after.db.QueryRow(`SELECT authenticated, status, error FROM managed_provider_status WHERE provider='cursor'`).Scan(&authenticated, &status, &errorVal))
	require.Equal(t, 0, authenticated)
	require.Equal(t, "signed_out", status)
	require.Equal(t, "token expired", errorVal)

	// Verify default rows for google-vertex and antigravity exist with signed_out status
	for _, expected := range []struct {
		provider string
		label    string
	}{
		{"google-vertex", "Google Vertex AI"},
		{"antigravity", "Google Antigravity"},
	} {
		var label, st string
		var auth int
		require.NoError(t, after.db.QueryRow(`SELECT label, authenticated, status FROM managed_provider_status WHERE provider=?`, expected.provider).Scan(&label, &auth, &st))
		require.Equal(t, expected.label, label)
		require.Equal(t, 0, auth)
		require.Equal(t, "signed_out", st)
	}

	// Verify child model rows survived
	var modelCount int
	require.NoError(t, after.db.QueryRow(`SELECT count(*) FROM managed_provider_models`).Scan(&modelCount))
	require.Equal(t, 2, modelCount)

	// Verify index managed_provider_models_available_idx exists and works
	var indexCount int
	require.NoError(t, after.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='managed_provider_models_available_idx'`).Scan(&indexCount))
	require.Equal(t, 1, indexCount)

	// Verify FK CASCADE: deleting cursor from managed_provider_status cascades to managed_provider_models
	_, err = after.db.Exec(`DELETE FROM managed_provider_status WHERE provider='cursor'`)
	require.NoError(t, err)
	var cursorModels int
	require.NoError(t, after.db.QueryRow(`SELECT count(*) FROM managed_provider_models WHERE provider='cursor'`).Scan(&cursorModels))
	require.Zero(t, cursorModels, "FK CASCADE must delete child model rows")

	// Verify managed_launch_recipes table has antigravity_extension column
	var agExt string
	require.NoError(t, after.db.QueryRow(`SELECT antigravity_extension FROM managed_launch_recipes WHERE agent_id=?`, agentID).Scan(&agExt))
	require.Equal(t, "", agExt, "legacy recipe should have default empty antigravity_extension")
}

func TestMigration086CHECKConstraintsEnforced(t *testing.T) {
	durable := openTestStore(t)
	// Invalid provider in managed_provider_status
	_, err := durable.db.Exec(`INSERT INTO managed_provider_status (provider, label, authenticated, status, updated_at) VALUES ('unknown', 'Unknown', 0, 'signed_out', '2026-09-01T00:00:00.000Z')`)
	require.Error(t, err, "CHECK constraint must reject unknown provider")

	// Invalid provider in managed_provider_models
	_, err = durable.db.Exec(`INSERT INTO managed_provider_models (provider, model_id, name, available, updated_at) VALUES ('unknown', 'm1', 'Model 1', 1, '2026-09-01T00:00:00.000Z')`)
	require.Error(t, err, "CHECK constraint must reject unknown provider in models")

	// Valid expanded providers accepted in managed_provider_status
	for _, p := range []string{"google-vertex", "antigravity"} {
		var exists int
		require.NoError(t, durable.db.QueryRow(`SELECT count(*) FROM managed_provider_status WHERE provider=?`, p).Scan(&exists))
		require.Equal(t, 1, exists)
	}

	// antigravity_extension length constraint (> 4096 bytes rejected)
	tooLong := strings.Repeat("a", 4097)
	_, err = durable.db.Exec(`INSERT INTO managed_launch_recipes (agent_id, pi_executable, session_root, cwd, provider, model_id, mecha_rpc_extension, pi_subagents_extension, cursor_extension, antigravity_extension, recipe_sha256, created_at, updated_at) VALUES
		('overflow-agent', '/bin/pi', '/sessions', '/cwd', 'openai-codex', 'gpt-5.6', '/rpc.ts', '/sub.ts', '', ?, '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', '2026-09-01', '2026-09-01')`, tooLong)
	require.Error(t, err, "CHECK constraint must reject antigravity_extension > 4096 bytes")
}
