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
	"github.com/stretchr/testify/require"
)

// Even with no command bootstrap flag, an already-saved embedded root must be
// dispatched from its durable role by Listen before readiness becomes "ok".
func TestHTTPLaunchNeverGrantsCommandRootAuthorityRED(t *testing.T) {
	plain := newEmbeddedHTTPG2FixtureWithDefault(t, false)
	status, raw := plain.call(http.MethodPost, "/v1/managed-agents", "a0444444-4444-4444-8444-444444444444",
		fmt.Sprintf(`{"name":"api-root","cwd":%q,"initial_instruction":"ordinary HTTP launch"}`, plain.root))
	require.Equal(t, http.StatusAccepted, status, string(raw))
	row, err := plain.db.ManagedAgent(context.Background(), "api-root")
	require.NoError(t, err)
	require.Equal(t, "rpc", row.Runtime, "bare HTTP stays RPC by default")

	opted := newParentlessHTTPG3Fixture(t)
	status, raw = opted.call(http.MethodPost, "/v1/managed-agents", "a0555555-5555-4555-8555-555555555555",
		fmt.Sprintf(`{"name":"command-looking-root","cwd":%q,"runtime":"embedded","initial_instruction":"root-looking body"}`, opted.root))
	require.Equal(t, http.StatusAccepted, status, string(raw))
	row, err = opted.db.ManagedAgent(context.Background(), "command-looking-root")
	require.NoError(t, err)
	require.Equal(t, "embedded", row.Runtime)
	check, err := sql.Open("sqlite", opted.dbPath)
	require.NoError(t, err)
	defer check.Close()
	var role string
	require.NoError(t, check.QueryRowContext(context.Background(), `SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1`, row.AgentID).Scan(&role))
	require.Equal(t, "worker", role, "parentless HTTP body cannot select command root role")
}

func TestSavedEmbeddedRoleRecoveredDuringStartupWithoutBootstrapRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	config := managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}
	newManager := func(s *store.Store, started chan<- struct{}) *managed.Manager {
		t.Helper()
		m, e := managed.NewManager(managed.ManagerOptions{Store: s, Manifest: manifest, Config: config, CodexCredentialStore: testfixture.CodexAdmission(t),
			CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			EmbeddedRunnerFactory: func(_ context.Context, _ store.ManagedAgentRecord) error {
				if started != nil {
					started <- struct{}{}
				}
				return nil
			},
		})
		require.NoError(t, e)
		return m
	}
	first := newManager(db, nil)
	request := managed.LaunchRequest{Name: "saved-root", Cwd: root, Runtime: "embedded", RequestID: "a0333333-3333-4333-8333-333333333333"}
	launched, err := first.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && row.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	original := *launched.Agent
	require.NoError(t, first.Close(ctx))
	require.NoError(t, db.Close())
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var firstToken int64
	require.NoError(t, check.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, original.AgentID).Scan(&firstToken))
	_, err = check.ExecContext(ctx, `UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z' WHERE agent_id=?`, original.AgentID)
	require.NoError(t, err)
	require.NoError(t, check.Close())

	db, err = store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	started := make(chan struct{}, 1)
	recovered := newManager(db, started)
	router, err := New(Options{Store: db, Managed: recovered}) // no embedded HTTP default
	require.NoError(t, err)
	defer router.Close(ctx)
	// Keep readiness "starting" until runStartup has returned; its dispatch
	// must already have reached the runner without a fresh bootstrap request.
	router.startupWork = func(startupCtx context.Context) error {
		if err := router.runStartup(startupCtx); err != nil {
			return err
		}
		select {
		case <-started:
			return nil
		case <-time.After(3 * time.Second):
			return fmt.Errorf("saved embedded role was not dispatched before readiness")
		}
	}
	require.NoError(t, router.Listen(ctx))
	url, _ := router.URLs()
	response, err := http.Get(url + "/v1/health")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Eventually(t, func() bool {
		read, e := sql.Open("sqlite", path)
		if e != nil {
			return false
		}
		defer read.Close()
		var role, session, generation string
		var token int64
		e = read.QueryRowContext(ctx, `SELECT role,session_id,generation,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, original.AgentID).Scan(&role, &session, &generation, &token)
		return e == nil && role == "root" && session == original.PiSessionID && generation == original.RuntimeGeneration && token > firstToken
	}, 3*time.Second, 10*time.Millisecond)
}
