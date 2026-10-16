package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupGoalTestServer(t *testing.T) (*httptest.Server, *store.Store, *managed.Manager, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "router.db")
	opts := store.Options{}
	if opts.Migrations == nil {
		if err := store.CloneTemplateDB(t, dbPath); err == nil {
			opts.Migrations = []store.Migration{}
		}
	}
	durable, err := store.Open(ctx, dbPath, opts)
	require.NoError(t, err)

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	mgr, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)

	router, err := New(Options{Store: durable, Managed: mgr})
	require.NoError(t, err)
	server := httptest.NewServer(router)

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)

	t.Cleanup(func() {
		server.Close()
		_ = router.Close(ctx)
		_ = rawDB.Close()
		_ = durable.Close()
	})

	return server, durable, mgr, rawDB
}

func callGoalEndpoint(t *testing.T, client *http.Client, serverURL, method, agentID string, body map[string]any) (int, map[string]any) {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(raw)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, serverURL+"/v1/managed-agents/"+agentID+"/goal", bodyReader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())

	res, err := client.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()

	var payload map[string]any
	_ = json.NewDecoder(res.Body).Decode(&payload)
	return res.StatusCode, payload
}

func TestManagedGoalUpdateAndDisableWhenAgentIsIdle(t *testing.T) {
	server, _, _, rawDB := setupGoalTestServer(t)
	ctx := context.Background()
	client := server.Client()

	agentID := "idle-agent"
	_, err := rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','google','gemini-3.8-flash','running','running','idle','embedded',1,1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, agentID)
	require.NoError(t, err)

	// 1. Set goal on idle agent
	code, resp := callGoalEndpoint(t, client, server.URL, http.MethodPut, agentID, map[string]any{
		"action": "set",
		"goal":   "Write unit tests",
	})
	require.Equal(t, http.StatusOK, code, resp)
	goalState, ok := resp["goal_state"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, goalState["enabled"])
	require.Equal(t, "Write unit tests", goalState["goal"])

	// 2. GET goal on idle agent
	code, getResp := callGoalEndpoint(t, client, server.URL, http.MethodGet, agentID, nil)
	require.Equal(t, http.StatusOK, code, getResp)
	getGoalState := getResp["goal_state"].(map[string]any)
	require.Equal(t, true, getGoalState["enabled"])
	require.Equal(t, "Write unit tests", getGoalState["goal"])

	// 3. Disable goal on idle agent
	code, offResp := callGoalEndpoint(t, client, server.URL, http.MethodPut, agentID, map[string]any{
		"action": "off",
	})
	require.Equal(t, http.StatusOK, code, offResp)
	offGoalState := offResp["goal_state"].(map[string]any)
	require.Equal(t, false, offGoalState["enabled"])
	require.Equal(t, "", offGoalState["goal"])
}

func TestManagedGoalUpdateAndDisableWhenAgentIsStopped(t *testing.T) {
	server, _, _, rawDB := setupGoalTestServer(t)
	ctx := context.Background()
	client := server.Client()

	agentID := "stopped-agent"
	_, err := rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','google','gemini-3.8-flash','terminated','terminated','stopped','embedded',1,1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, agentID)
	require.NoError(t, err)

	// 1. Set goal on stopped agent
	code, resp := callGoalEndpoint(t, client, server.URL, http.MethodPut, agentID, map[string]any{
		"action": "set",
		"goal":   "Configure before start",
	})
	require.Equal(t, http.StatusOK, code, resp)
	goalState := resp["goal_state"].(map[string]any)
	require.Equal(t, true, goalState["enabled"])
	require.Equal(t, "Configure before start", goalState["goal"])

	// 2. Disable goal on stopped agent
	code, offResp := callGoalEndpoint(t, client, server.URL, http.MethodPut, agentID, map[string]any{
		"action": "off",
	})
	require.Equal(t, http.StatusOK, code, offResp)
	offGoalState := offResp["goal_state"].(map[string]any)
	require.Equal(t, false, offGoalState["enabled"])
	require.Equal(t, "", offGoalState["goal"])
}

func TestManagedGoalUpdateAndDisableWhenReadinessSourceIsNotReady(t *testing.T) {
	server, _, _, rawDB := setupGoalTestServer(t)
	ctx := context.Background()
	client := server.Client()

	agentID := "unready-source-agent"
	_, err := rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','google','gemini-3.8-flash','running','running','idle','embedded',1,1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, agentID)
	require.NoError(t, err)

	// Explicitly insert an unready readiness source (no applied_policy_canonical, no fence)
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_readiness_sources (agent_id, desired_policy_canonical, desired_policy_sha256) VALUES (?, ?, ?)`, agentID, []byte("{}"), strings.Repeat("0", 64))
	require.NoError(t, err)

	// 1. Set goal when readiness source is not ready
	code, resp := callGoalEndpoint(t, client, server.URL, http.MethodPut, agentID, map[string]any{
		"action": "set",
		"goal":   "Goal despite unready readiness source",
	})
	require.Equal(t, http.StatusOK, code, resp)
	goalState := resp["goal_state"].(map[string]any)
	require.Equal(t, true, goalState["enabled"])
	require.Equal(t, "Goal despite unready readiness source", goalState["goal"])

	// 2. Disable goal when readiness source is not ready
	code, offResp := callGoalEndpoint(t, client, server.URL, http.MethodPut, agentID, map[string]any{
		"action": "off",
	})
	require.Equal(t, http.StatusOK, code, offResp)
	offGoalState := offResp["goal_state"].(map[string]any)
	require.Equal(t, false, offGoalState["enabled"])
	require.Equal(t, "", offGoalState["goal"])
}

func TestManagedGoalUpdateAndDisableWhenAgentHasActiveWorkflowState(t *testing.T) {
	server, durable, _, rawDB := setupGoalTestServer(t)
	ctx := context.Background()
	client := server.Client()

	// Create a workflow state with a locked goal
	state, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "LockedWorkflowState",
		Description: "State with mandatory goal",
		Goal:        "State mandated goal",
	})
	require.NoError(t, err)

	agentID := "workflow-bound-agent"
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,session_epoch,active_workflow_state_id,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','google','gemini-3.8-flash','running','running','idle','embedded',1,?,1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, agentID, state.ID)
	require.NoError(t, err)

	// Initial goal set to state's goal
	_, err = durable.UpdateManagedGoalConfig(ctx, agentID, store.ManagedGoalConfig{
		Enabled:  true,
		Goal:     state.Goal,
		Settings: store.DefaultManagedGoalSettings(),
	})
	require.NoError(t, err)

	// Operator disables goal via HTTP PUT - must succeed with 200 OK!
	code, offResp := callGoalEndpoint(t, client, server.URL, http.MethodPut, agentID, map[string]any{
		"action": "off",
	})
	require.Equal(t, http.StatusOK, code, offResp)
	offGoalState := offResp["goal_state"].(map[string]any)
	require.Equal(t, false, offGoalState["enabled"])
	require.Equal(t, "", offGoalState["goal"])

	// Operator updates goal via HTTP PUT - must succeed with 200 OK!
	code, setResp := callGoalEndpoint(t, client, server.URL, http.MethodPut, agentID, map[string]any{
		"action": "set",
		"goal":   "Operator updated goal",
	})
	require.Equal(t, http.StatusOK, code, setResp)
	setGoalState := setResp["goal_state"].(map[string]any)
	require.Equal(t, true, setGoalState["enabled"])
	require.Equal(t, "Operator updated goal", setGoalState["goal"])
}
