package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWorkflowStatesHTTPCRUD(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	router, err := transport.New(transport.Options{Store: durable})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	client := server.Client()

	// 1. GET /v1/workflow-states initially empty
	res, err := client.Get(server.URL + "/v1/workflow-states")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var listRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&listRes))
	res.Body.Close()
	require.Empty(t, listRes["workflow_states"])

	// 2. POST /v1/workflow-states
	createPayload := map[string]any{
		"name":        "initial-triage",
		"description": "Initial task triage state",
		"entry_text":  "Agent has entered initial triage. Please analyze task requirements.",
	}
	bodyBytes, _ := json.Marshal(createPayload)
	res, err = client.Post(server.URL+"/v1/workflow-states", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	var createRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&createRes))
	res.Body.Close()
	createdState := createRes["workflow_state"].(map[string]any)
	stateID := createdState["id"].(string)
	require.Equal(t, "initial-triage", createdState["name"])
	require.Equal(t, "Initial task triage state", createdState["description"])
	require.Equal(t, "Agent has entered initial triage. Please analyze task requirements.", createdState["entry_text"])

	// 3. GET /v1/workflow-states/{id}
	res, err = client.Get(server.URL + "/v1/workflow-states/" + stateID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getRes))
	res.Body.Close()
	gotState := getRes["workflow_state"].(map[string]any)
	require.Equal(t, stateID, gotState["id"])

	// 4. PUT /v1/workflow-states/{id}
	updatePayload := map[string]any{
		"name":        "triage-updated",
		"description": "Updated triage description",
		"entry_text":  "Updated entry text",
	}
	updateBytes, _ := json.Marshal(updatePayload)
	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/workflow-states/"+stateID, bytes.NewReader(updateBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var updateRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&updateRes))
	res.Body.Close()
	updatedState := updateRes["workflow_state"].(map[string]any)
	require.Equal(t, "triage-updated", updatedState["name"])
	require.Equal(t, "Updated triage description", updatedState["description"])

	// 5. DELETE /v1/workflow-states/{id}
	deleteReq, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/workflow-states/"+stateID, nil)
	require.NoError(t, err)
	res, err = client.Do(deleteReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, res.StatusCode)
	res.Body.Close()

	// 6. Verify deleted
	res, err = client.Get(server.URL + "/v1/workflow-states/" + stateID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()
}
