package transport_test

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWorkflowActivationHTTPReceiptReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	s, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer s.Close()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	id := "activation-replay-agent"
	_, err = db.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','test','test','running','running','idle','rpc','session',1,'gen',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id, id)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, id, "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	state, err := s.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "original"})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: s, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := transport.New(transport.Options{Store: s, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()
	send := func(key, body string) (int, []byte) {
		req, e := http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+id+"/workflow-state", bytes.NewBufferString(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", uuid.NewString())
		req.Header.Set("Idempotency-Key", key)
		res, e := server.Client().Do(req)
		require.NoError(t, e)
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		require.NoError(t, e)
		return res.StatusCode, b
	}
	body := `{"action":"activate","workflow_state_id":"` + state.ID + `"}`
	status, original := send("activation-one", body)
	require.Equal(t, http.StatusOK, status, string(original))
	status, stopped := send("deactivation-one", `{"action":"deactivate"}`)
	require.Equal(t, http.StatusOK, status)
	status, deactivationReplay := send("deactivation-one", `{"action":"deactivate"}`)
	require.Equal(t, http.StatusOK, status, string(deactivationReplay))
	require.JSONEq(t, string(stopped), string(deactivationReplay))
	status, replay := send("activation-one", body)
	require.Equal(t, http.StatusOK, status, string(replay))
	require.JSONEq(t, string(original), string(replay))
	agent, err := s.ManagedAgent(ctx, id)
	require.NoError(t, err)
	require.Empty(t, agent.ActiveWorkflowStateID, "replay must not reactivate")
	status, _ = send("activation-one", `{"action":"deactivate"}`)
	require.Equal(t, http.StatusConflict, status)
}

func TestWorkflowActivationHTTPReplayAfterAgentDeletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	s, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer s.Close()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	id := "deleted-activation-agent"
	_, err = db.Exec(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','test','test','running','running','idle','rpc','session',1,'gen',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id, id)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, id, "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	state, err := s.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "original"})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: s, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := transport.New(transport.Options{Store: s, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()
	send := func(key, body string) (int, string) {
		req, e := http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+id+"/workflow-state", bytes.NewBufferString(body))
		require.NoError(t, e)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", uuid.NewString())
		req.Header.Set("Idempotency-Key", key)
		res, e := server.Client().Do(req)
		require.NoError(t, e)
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		require.NoError(t, e)
		return res.StatusCode, string(b)
	}
	on := `{"action":"activate","workflow_state_id":"` + state.ID + `"}`
	off := `{"action":"deactivate"}`
	status, first := send("on", on)
	require.Equal(t, 200, status, first)
	status, stopped := send("off", off)
	require.Equal(t, 200, status, stopped)
	_, err = db.Exec(`DELETE FROM managed_agents WHERE agent_id=?`, id)
	require.NoError(t, err)
	status, replay := send("on", on)
	require.Equal(t, 200, status, replay)
	require.JSONEq(t, first, replay)
	status, replay = send("off", off)
	require.Equal(t, 200, status, replay)
	require.JSONEq(t, stopped, replay)
	status, replay = send("on", off)
	require.Equal(t, 409, status, replay)
	status, replay = send("new", on)
	require.Equal(t, 404, status, replay)
}
