package transport_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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

func TestManagedAgentWorkflowStateHTTP(t *testing.T) {
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

	// Create a managed agent directly via raw DB
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	agentID := "agent-" + uuid.NewString()
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','openai-codex','gpt-5','running','running','idle','embedded','session-1',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, "workflow-agent")
	require.NoError(t, err)

	// Create a workflow state
	ws, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "review-mode",
		Description: "Code review mode",
		EntryText:   "Review the code thoroughly.",
	})
	require.NoError(t, err)

	client := server.Client()

	// 1. GET workflow state initially empty
	res, err := client.Get(server.URL + "/v1/managed-agents/" + agentID + "/workflow-state")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResp))
	res.Body.Close()
	require.Equal(t, agentID, getResp["agent_id"])
	require.Equal(t, "", getResp["active_workflow_state_id"])
	require.Nil(t, getResp["active_workflow_state"])

	// 2. Activate workflow state
	activateBody, err := json.Marshal(map[string]any{
		"action":            "activate",
		"workflow_state_id": ws.ID,
	})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(activateBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())

	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var activateResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&activateResp))
	res.Body.Close()
	require.Equal(t, ws.ID, activateResp["active_workflow_state_id"])
	activeMap, ok := activateResp["active_workflow_state"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "review-mode", activeMap["name"])

	// Verify persistence via GET
	res, err = client.Get(server.URL + "/v1/managed-agents/" + agentID + "/workflow-state")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResp2 map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResp2))
	res.Body.Close()
	require.Equal(t, ws.ID, getResp2["active_workflow_state_id"])

	// 3. Deactivate workflow state
	deactivateBody, err := json.Marshal(map[string]any{
		"action": "deactivate",
	})
	require.NoError(t, err)
	req, err = http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(deactivateBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())

	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var deactivateResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&deactivateResp))
	res.Body.Close()
	require.Equal(t, "", deactivateResp["active_workflow_state_id"])
	require.Nil(t, deactivateResp["active_workflow_state"])

	// Verify persistence after deactivation via GET
	res, err = client.Get(server.URL + "/v1/managed-agents/" + agentID + "/workflow-state")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResp3 map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResp3))
	res.Body.Close()
	require.Equal(t, "", getResp3["active_workflow_state_id"])

	// 4. Test state with reminders activation and cleanup upon deactivation
	subj := "Review Mode Subject"
	wsWithRem, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "review-mode-with-reminders",
		Description: "Review with automated reminders",
		EntryText:   "Review checklist entry",
		Reminders: []store.WorkflowReminderConfig{
			{
				ID:           "test-rem-oneshot",
				Type:         store.WorkflowReminderOneShot,
				Subject:      &subj,
				Body:         "Please complete the code review checklist.",
				DelaySeconds: 100,
			},
		},
	})
	require.NoError(t, err)

	activateWithRemBody, err := json.Marshal(map[string]any{
		"action":            "activate",
		"workflow_state_id": wsWithRem.ID,
	})
	require.NoError(t, err)
	req, err = http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(activateWithRemBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())

	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	// Verify reminder was created
	rems, err := durable.Reminders(ctx)
	require.NoError(t, err)
	require.Len(t, rems, 1)
	require.Equal(t, agentID, rems[0].Target)
	require.Equal(t, "Please complete the code review checklist.", rems[0].Body)

	// Deactivate and verify reminder was cleaned up
	deactReq, err := http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(deactivateBody))
	require.NoError(t, err)
	deactReq.Header.Set("Content-Type", "application/json")
	deactReq.Header.Set("X-Request-ID", uuid.NewString())

	res, err = client.Do(deactReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	// Verify reminder was cleaned up
	rems, err = durable.Reminders(ctx)
	require.NoError(t, err)
	require.Empty(t, rems)
}
