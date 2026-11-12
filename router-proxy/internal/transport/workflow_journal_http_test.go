package transport_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWorkflowJournalHTTPAgentScopeAndInvalidCursor(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := transport.New(transport.Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES('journal-agent','Agent','/tmp','openai-codex','gpt-5','running','running','idle','embedded','session',1,'gen',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/v1/managed-agents/journal-agent/workflow-journal", http.StatusOK},
		{"/v1/managed-agents/journal-agent/workflow-journal?cursor=not-a-cursor", http.StatusBadRequest},
		{"/v1/managed-agents/missing/workflow-journal", http.StatusNotFound},
		{"/v1/managed-agents/journal-agent/workflow-journal/missing", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			res, err := server.Client().Get(server.URL + tc.path)
			require.NoError(t, err)
			defer res.Body.Close()
			require.Equal(t, tc.status, res.StatusCode)
		})
	}
}

func TestWorkflowJournalHTTPRejectsInvalidMethodsAndLimits(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := transport.New(transport.Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/v1/managed-agents/a/workflow-journal?limit=-1", http.StatusBadRequest},
		{http.MethodGet, "/v1/managed-agents/a/workflow-journal?status=invalid", http.StatusBadRequest},
		{http.MethodPost, "/v1/managed-agents/a/workflow-journal", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/v1/managed-agents/a/workflow-journal/missing", http.StatusNotFound},
	} {
		req, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		require.NoError(t, err)
		res, err := server.Client().Do(req)
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, tc.status, res.StatusCode, tc.path)
	}
}

// WJ-04: HTTP deletion is agent scoped and only terminal runs may be removed.
func TestWorkflowJournalHTTPDeleteTerminalOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := transport.New(transport.Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES('a','Agent','/tmp','p','m','running','running','idle','embedded','session',1,'gen',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO workflow_journal_runs(id,agent_id,agent_name,initial_state_id,initial_state_name,started_at,status,terminal_at) VALUES ('active','a','Agent','','','2026-01-01T00:00:00Z','active',NULL),('done','a','Agent','','','2099-01-01T00:00:00Z','terminal','2099-01-02T00:00:00Z')`)
	require.NoError(t, err)
	for _, tc := range []struct {
		agent, run string
		want       int
	}{{"a", "active", 409}, {"other", "done", 404}, {"a", "done", 204}, {"a", "done", 404}, {"a", "active", 409}} {
		req, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/managed-agents/"+tc.agent+"/workflow-journal/"+tc.run, nil)
		require.NoError(t, err)
		res, err := server.Client().Do(req)
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, tc.want, res.StatusCode)
	}
}
