package transport

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Behavioral RED runs separately from the missing internal-root API compile RED.
// Bare WebUI payloads cannot be promoted to roots by their names or body.
func TestEmbeddedHTTPWorkerRoleReplayAndColdRestoreRED(t *testing.T) {
	f := newParentlessHTTPG3Fixture(t)
	defer func() {
		if t.Failed() {
			t.Logf("failure snapshot providers=%d owner_published=unmeasured(private Manager map)", f.calls.Load())
			cwdPrivacyTransportSnapshot(t, f.dbPath)
		}
	}()
	ctx := context.Background()
	names := []string{"role-one", "role-two"}
	ids := []string{"d8111111-1111-4111-8111-111111111111", "d8222222-2222-4222-8222-222222222222"}
	type savedRole struct {
		agent store.ManagedAgentRecord
		token int64
	}
	rows := make([]savedRole, len(names))
	readRole := func(db *sql.DB, agent store.ManagedAgentRecord) (string, int64, error) {
		var role string
		var token int64
		err := db.QueryRowContext(ctx, `SELECT role,owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, agent.AgentID, agent.PiSessionID, agent.SessionEpoch, agent.RuntimeGeneration).Scan(&role, &token)
		return role, token, err
	}
	check, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer check.Close()
	for i, name := range names {
		body := fmt.Sprintf(`{"name":%q,"cwd":%q,"initial_instruction":%q}`, name, f.root, "instruction for "+name)
		status, response := f.call(http.MethodPost, "/v1/managed-agents", ids[i], body)
		require.Equal(t, http.StatusAccepted, status, string(response))
		// Same request ID and body must return the original durable response.
		replayStatus, replay := f.call(http.MethodPost, "/v1/managed-agents", ids[i], body)
		require.Equal(t, status, replayStatus)
		require.Equal(t, response, replay)
		conflictStatus, _ := f.call(http.MethodPost, "/v1/managed-agents", ids[i], fmt.Sprintf(`{"name":%q,"cwd":%q,"initial_instruction":"changed"}`, name, f.root))
		require.Equal(t, http.StatusConflict, conflictStatus)
		require.Eventually(t, func() bool {
			row, e := f.db.ManagedAgent(ctx, name)
			return e == nil && row.LifecycleState == "running"
		}, 3*time.Second, 10*time.Millisecond)
		row, e := f.db.ManagedAgent(ctx, name)
		require.NoError(t, e)
		require.Equal(t, "embedded", row.Runtime)
		role, token, e := readRole(check, *row)
		require.NoError(t, e, "role must be reserved before runner readiness")
		require.Equal(t, "worker", role)
		binding := store.EmbeddedBinding{AgentID: row.AgentID, SessionID: row.PiSessionID, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration, OwnerToken: token}
		snapshot, e := f.db.ReopenEmbedded(ctx, binding)
		require.NoError(t, e)
		require.NotEmpty(t, snapshot.Entries)
		rows[i] = savedRole{agent: *row, token: token}
	}
	require.NotEqual(t, rows[0].agent.AgentID, rows[1].agent.AgentID)
	require.NotEqual(t, rows[0].agent.PiSessionID, rows[1].agent.PiSessionID)
	require.NoError(t, f.router.Close(ctx))
	require.NoError(t, f.db.Close())
	// Simulate lease expiry after process death. Never mutate saved role/runtime.
	_, err = check.ExecContext(ctx, `UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z' WHERE agent_id IN (?,?)`, names[0], names[1])
	require.NoError(t, err)
	reopened, err := store.Open(ctx, f.dbPath, store.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	next, err := managed.NewManager(managed.ManagerOptions{Store: reopened, Manifest: manifest,
		Config:                managed.Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return f.model },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer next.Close(ctx)
	setTransportEmbeddedRecoveryGeneration(t, ctx, reopened, next)
	require.NoError(t, next.RecoverEmbeddedRoots(ctx))
	for _, prior := range rows {
		require.Eventually(t, func() bool {
			role, token, e := readRole(check, prior.agent)
			return e == nil && role == "worker" && token >= prior.token
		}, 3*time.Second, 10*time.Millisecond, "cold recovery must claim the saved worker role")
		row, e := reopened.ManagedAgent(ctx, prior.agent.AgentID)
		require.NoError(t, e)
		require.Equal(t, prior.agent.PiSessionID, row.PiSessionID)
	}
}
