package transport_test

import (
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

func TestManagedSubagentStopHTTP(t *testing.T) {
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

	parentAgentID := "parent-" + uuid.NewString()
	childID := "subagent-stop-test-1"

	// Insert parent agent
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','google','gemini-3.8-flash','running','running','active','embedded','session-1',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, parentAgentID, "parent-worker")
	require.NoError(t, err)

	// Insert running subagent
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_subagents(agent_id,parent_agent_id,parent_session_id,parent_epoch,parent_generation,description,subagent_type,status,max_turns,created_at,updated_at) VALUES(?,?,'session-1',1,'gen-1','Workflow subagent','general-purpose','running',10,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, childID, parentAgentID)
	require.NoError(t, err)

	client := server.Client()

	// 1. Verify subagent is running in GET /children
	res, err := client.Get(server.URL + "/v1/managed-agents/" + parentAgentID + "/children")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var childrenResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&childrenResp))
	res.Body.Close()
	childrenList, ok := childrenResp["children"].([]any)
	require.True(t, ok)
	require.Len(t, childrenList, 1)
	first := childrenList[0].(map[string]any)
	require.Equal(t, "running", first["status"])

	// 2. POST /v1/managed-agents/:agent_id/children/:child_id/stop
	stopRes, err := client.Post(server.URL+"/v1/managed-agents/"+parentAgentID+"/children/"+childID+"/stop", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, stopRes.StatusCode)
	var stopBody map[string]any
	require.NoError(t, json.NewDecoder(stopRes.Body).Decode(&stopBody))
	stopRes.Body.Close()
	require.Equal(t, true, stopBody["ok"])

	// 3. Verify subagent status is now "stopped" in GET /children
	resAfter, err := client.Get(server.URL + "/v1/managed-agents/" + parentAgentID + "/children")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resAfter.StatusCode)
	var childrenAfterResp map[string]any
	require.NoError(t, json.NewDecoder(resAfter.Body).Decode(&childrenAfterResp))
	resAfter.Body.Close()
	childrenAfterList, ok := childrenAfterResp["children"].([]any)
	require.True(t, ok)
	require.Len(t, childrenAfterList, 1)
	firstAfter := childrenAfterList[0].(map[string]any)
	require.Equal(t, "stopped", firstAfter["status"])

	// 4. Test legacy/alternative /subagents/:child_id/stop path
	childID2 := "subagent-stop-test-2"
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_subagents(agent_id,parent_agent_id,parent_session_id,parent_epoch,parent_generation,description,subagent_type,status,max_turns,created_at,updated_at) VALUES(?,?,'session-1',1,'gen-1','Workflow subagent 2','general-purpose','running',10,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, childID2, parentAgentID)
	require.NoError(t, err)

	stopRes2, err := client.Post(server.URL+"/v1/managed-agents/"+parentAgentID+"/subagents/"+childID2+"/stop", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, stopRes2.StatusCode)
	var stopBody2 map[string]any
	require.NoError(t, json.NewDecoder(stopRes2.Body).Decode(&stopBody2))
	stopRes2.Body.Close()
	require.Equal(t, true, stopBody2["ok"])
}
