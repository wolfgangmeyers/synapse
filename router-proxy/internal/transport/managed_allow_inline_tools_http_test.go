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

func TestManagedAgentAllowInlineToolsHTTP(t *testing.T) {
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

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	agentID := "agent-" + uuid.NewString()
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','google','gemini-3.8-flash','running','running','idle','embedded','session-1',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, "inline-test-agent")
	require.NoError(t, err)

	client := server.Client()

	// 1. GET /v1/managed-agents/:id: allow_inline_tools must be false by default
	res, err := client.Get(server.URL + "/v1/managed-agents/" + agentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var singleResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&singleResp))
	res.Body.Close()
	agentData, ok := singleResp["agent"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, agentData["allow_inline_tools"])

	// 2. GET /v1/managed-agents: list must include allow_inline_tools
	listRes, err := client.Get(server.URL + "/v1/managed-agents")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listRes.StatusCode)
	var listResp map[string]any
	require.NoError(t, json.NewDecoder(listRes.Body).Decode(&listResp))
	listRes.Body.Close()
	agentsList, ok := listResp["agents"].([]any)
	require.True(t, ok)
	var foundAgent map[string]any
	for _, item := range agentsList {
		if m, isMap := item.(map[string]any); isMap && m["agent_id"] == agentID {
			foundAgent = m
			break
		}
	}
	require.NotNil(t, foundAgent)
	require.Equal(t, false, foundAgent["allow_inline_tools"])

	// 3. POST /v1/managed-agents/:id/allow-inline-tools with {"allow_inline_tools": true}
	postBody, _ := json.Marshal(map[string]any{"allow_inline_tools": true})
	postRes, err := client.Post(server.URL+"/v1/managed-agents/"+agentID+"/allow-inline-tools", "application/json", bytes.NewReader(postBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, postRes.StatusCode)
	var postRespData map[string]any
	require.NoError(t, json.NewDecoder(postRes.Body).Decode(&postRespData))
	postRes.Body.Close()
	require.Equal(t, agentID, postRespData["agent_id"])
	require.Equal(t, true, postRespData["allow_inline_tools"])

	// 4. Verify updated state via GET /v1/managed-agents/:id
	resAfterPost, err := client.Get(server.URL + "/v1/managed-agents/" + agentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resAfterPost.StatusCode)
	var singleRespAfter map[string]any
	require.NoError(t, json.NewDecoder(resAfterPost.Body).Decode(&singleRespAfter))
	resAfterPost.Body.Close()
	agentDataAfter := singleRespAfter["agent"].(map[string]any)
	require.Equal(t, true, agentDataAfter["allow_inline_tools"])

	// 5. PATCH /v1/managed-agents/:id with {"allow_inline_tools": false}
	patchBody, _ := json.Marshal(map[string]any{"allow_inline_tools": false})
	patchReq, err := http.NewRequest(http.MethodPatch, server.URL+"/v1/managed-agents/"+agentID, bytes.NewReader(patchBody))
	require.NoError(t, err)
	patchReq.Header.Set("Content-Type", "application/json")
	patchRes, err := client.Do(patchReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, patchRes.StatusCode)
	var patchRespData map[string]any
	require.NoError(t, json.NewDecoder(patchRes.Body).Decode(&patchRespData))
	patchRes.Body.Close()
	require.Equal(t, agentID, patchRespData["agent_id"])
	require.Equal(t, false, patchRespData["allow_inline_tools"])

	// 6. Verify updated state via GET /v1/managed-agents/:id
	resAfterPatch, err := client.Get(server.URL + "/v1/managed-agents/" + agentID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resAfterPatch.StatusCode)
	var singleRespAfterPatch map[string]any
	require.NoError(t, json.NewDecoder(resAfterPatch.Body).Decode(&singleRespAfterPatch))
	resAfterPatch.Body.Close()
	agentDataAfterPatch := singleRespAfterPatch["agent"].(map[string]any)
	require.Equal(t, false, agentDataAfterPatch["allow_inline_tools"])

	// 7. Error cases
	// Missing field
	badReq, _ := http.NewRequest(http.MethodPatch, server.URL+"/v1/managed-agents/"+agentID, bytes.NewReader([]byte(`{}`)))
	badReq.Header.Set("Content-Type", "application/json")
	badRes, err := client.Do(badReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, badRes.StatusCode)
	badRes.Body.Close()

	// Non-existent agent
	noAgentReq, _ := http.NewRequest(http.MethodPatch, server.URL+"/v1/managed-agents/non-existent-agent", bytes.NewReader(patchBody))
	noAgentReq.Header.Set("Content-Type", "application/json")
	noAgentRes, err := client.Do(noAgentReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, noAgentRes.StatusCode)
	noAgentRes.Body.Close()
}
