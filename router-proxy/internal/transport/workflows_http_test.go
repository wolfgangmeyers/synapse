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
)

func TestWorkflowHTTP(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	router, err := transport.New(transport.Options{Store: durable})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	client := server.Client()

	// 1. GET /v1/workflows initially empty
	res, err := client.Get(server.URL + "/v1/workflows")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var listRes []map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&listRes))
	res.Body.Close()
	require.Empty(t, listRes)

	// 2. POST /v1/workflows with invalid payload -> 400
	badPayload := map[string]any{"name": ""}
	body, _ := json.Marshal(badPayload)
	res, err = client.Post(server.URL+"/v1/workflows", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	res.Body.Close()

	// 3. POST /v1/workflows -> 201 Created
	createPayload := map[string]any{
		"name":        "Test Workflow",
		"description": "Workflow for transport tests",
		"variables": []map[string]any{
			{
				"name":          "target_env",
				"label":         "Target Environment",
				"description":   "Environment to target",
				"type":          "text",
				"required":      true,
				"default_value": "production",
			},
		},
	}
	body, _ = json.Marshal(createPayload)
	res, err = client.Post(server.URL+"/v1/workflows", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	var createdWf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&createdWf))
	res.Body.Close()
	wfID := createdWf["id"].(string)
	require.NotEmpty(t, wfID)
	require.Equal(t, "Test Workflow", createdWf["name"])
	require.Equal(t, "Workflow for transport tests", createdWf["description"])
	varsList, ok := createdWf["variables"].([]any)
	require.True(t, ok)
	require.Len(t, varsList, 1)
	firstVar := varsList[0].(map[string]any)
	require.Equal(t, "target_env", firstVar["name"])
	require.Equal(t, "production", firstVar["default_value"])

	// 4. POST /v1/workflows conflict -> 409
	res, err = client.Post(server.URL+"/v1/workflows", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, res.StatusCode)
	var conflictErr map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&conflictErr))
	res.Body.Close()
	errObj := conflictErr["error"].(map[string]any)
	require.Equal(t, "workflow_name_conflict", errObj["code"])

	// 5. GET /v1/workflows/{id} -> 200 with states array
	res, err = client.Get(server.URL + "/v1/workflows/" + wfID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getRes))
	res.Body.Close()
	wfObj := getRes["workflow"].(map[string]any)
	require.Equal(t, wfID, wfObj["id"])
	require.Equal(t, "Test Workflow", wfObj["name"])
	statesArr := getRes["states"].([]any)
	require.Empty(t, statesArr)

	// 6. GET /v1/workflows/{id} non-existent -> 404
	res, err = client.Get(server.URL + "/v1/workflows/00000000-0000-0000-0000-000000000000")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()

	// 7. Create a workflow-state linked to this workflow
	stateCreatePayload := map[string]any{
		"name":        "State 1",
		"description": "Initial state",
		"entry_text":  "Welcome",
		"goal":        "Workflow goal 1",
		"workflow_id": wfID,
		"ui_pos_x":    100.5,
		"ui_pos_y":    200.5,
	}
	body, _ = json.Marshal(stateCreatePayload)
	res, err = client.Post(server.URL+"/v1/workflow-states", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	var stateCreated map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&stateCreated))
	res.Body.Close()
	stateID := stateCreated["workflow_state"].(map[string]any)["id"].(string)
	require.NotEmpty(t, stateID)
	require.Equal(t, wfID, stateCreated["workflow_state"].(map[string]any)["workflow_id"])
	require.Equal(t, "Workflow goal 1", stateCreated["workflow_state"].(map[string]any)["goal"])
	require.Equal(t, 100.5, stateCreated["workflow_state"].(map[string]any)["ui_pos_x"])
	require.Equal(t, 200.5, stateCreated["workflow_state"].(map[string]any)["ui_pos_y"])

	// Verify GET /v1/workflows/{id} includes state with goal
	res, err = client.Get(server.URL + "/v1/workflows/" + wfID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResWithStates map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResWithStates))
	res.Body.Close()
	statesWithGoal := getResWithStates["states"].([]any)
	require.Len(t, statesWithGoal, 1)
	require.Equal(t, "Workflow goal 1", statesWithGoal[0].(map[string]any)["goal"])

	// 8. PUT /v1/workflows/{id} update entry_state_id and name
	updatePayload := map[string]any{
		"name":           "Renamed Workflow",
		"description":    "Updated desc",
		"entry_state_id": stateID,
	}
	body, _ = json.Marshal(updatePayload)
	updateReq, err := http.NewRequest(http.MethodPut, server.URL+"/v1/workflows/"+wfID, bytes.NewReader(body))
	require.NoError(t, err)
	updateReq.Header.Set("Content-Type", "application/json")
	res, err = client.Do(updateReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var updatedWf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&updatedWf))
	res.Body.Close()
	require.Equal(t, "Renamed Workflow", updatedWf["name"])
	require.Equal(t, stateID, updatedWf["entry_state_id"])

	// 8b. PUT /v1/workflows/{id} partial update with only entry_state_id
	partialPayload := map[string]any{
		"entry_state_id": nil,
	}
	body, _ = json.Marshal(partialPayload)
	partialReq, err := http.NewRequest(http.MethodPut, server.URL+"/v1/workflows/"+wfID, bytes.NewReader(body))
	require.NoError(t, err)
	partialReq.Header.Set("Content-Type", "application/json")
	res, err = client.Do(partialReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var clearedWf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&clearedWf))
	res.Body.Close()
	require.Equal(t, "Renamed Workflow", clearedWf["name"])
	require.Nil(t, clearedWf["entry_state_id"])

	// Restore entry_state_id with only entry_state_id in payload
	partialPayload2 := map[string]any{
		"entry_state_id": stateID,
	}
	body, _ = json.Marshal(partialPayload2)
	partialReq2, err := http.NewRequest(http.MethodPut, server.URL+"/v1/workflows/"+wfID, bytes.NewReader(body))
	require.NoError(t, err)
	partialReq2.Header.Set("Content-Type", "application/json")
	res, err = client.Do(partialReq2)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var restoredWf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&restoredWf))
	res.Body.Close()
	require.Equal(t, "Renamed Workflow", restoredWf["name"])
	require.Equal(t, stateID, restoredWf["entry_state_id"])

	// 9. PUT /v1/workflows/{id}/layout -> batch update state node positions
	layoutPayload := map[string]any{
		"positions": []map[string]any{
			{
				"state_id": stateID,
				"ui_pos_x": 350.0,
				"ui_pos_y": 450.0,
			},
		},
	}
	body, _ = json.Marshal(layoutPayload)
	layoutReq, err := http.NewRequest(http.MethodPut, server.URL+"/v1/workflows/"+wfID+"/layout", bytes.NewReader(body))
	require.NoError(t, err)
	layoutReq.Header.Set("Content-Type", "application/json")
	res, err = client.Do(layoutReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	// Verify updated positions via GET /v1/workflows/{id}
	res, err = client.Get(server.URL + "/v1/workflows/" + wfID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getRes))
	res.Body.Close()
	statesArr = getRes["states"].([]any)
	require.Len(t, statesArr, 1)
	stObj := statesArr[0].(map[string]any)
	require.Equal(t, 350.0, stObj["ui_pos_x"])
	require.Equal(t, 450.0, stObj["ui_pos_y"])

	// 10. PUT /v1/workflows/{id}/layout with invalid payload -> 400
	badLayoutPayload := map[string]any{
		"positions": []map[string]any{
			{
				"state_id": stateID,
				// missing ui_pos_x / ui_pos_y
			},
		},
	}
	body, _ = json.Marshal(badLayoutPayload)
	layoutReq, err = http.NewRequest(http.MethodPut, server.URL+"/v1/workflows/"+wfID+"/layout", bytes.NewReader(body))
	require.NoError(t, err)
	layoutReq.Header.Set("Content-Type", "application/json")
	res, err = client.Do(layoutReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	res.Body.Close()

	// 11. POST /v1/workflows/{id}/duplicate -> 201 Created
	dupPayload := map[string]any{
		"name": "Cloned Workflow",
	}
	body, _ = json.Marshal(dupPayload)
	res, err = client.Post(server.URL+"/v1/workflows/"+wfID+"/duplicate", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	var duplicatedWf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&duplicatedWf))
	res.Body.Close()
	dupID := duplicatedWf["id"].(string)
	require.NotEmpty(t, dupID)
	require.NotEqual(t, wfID, dupID)
	require.Equal(t, "Cloned Workflow", duplicatedWf["name"])

	// Verify duplicated workflow has its own cloned states
	res, err = client.Get(server.URL + "/v1/workflows/" + dupID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var dupGetRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&dupGetRes))
	res.Body.Close()
	dupStatesArr := dupGetRes["states"].([]any)
	require.Len(t, dupStatesArr, 1)
	dupStateObj := dupStatesArr[0].(map[string]any)
	require.NotEqual(t, stateID, dupStateObj["id"])
	require.Equal(t, dupID, dupStateObj["workflow_id"])

	// 12. Duplicate with duplicate name -> 409 Conflict
	res, err = client.Post(server.URL+"/v1/workflows/"+wfID+"/duplicate", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, res.StatusCode)
	res.Body.Close()

	// 12b. Export Workflow: GET /v1/workflows/{id}/export -> 200 with Content-Disposition
	res, err = client.Get(server.URL + "/v1/workflows/" + dupID + "/export")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	cdHeader := res.Header.Get("Content-Disposition")
	require.Contains(t, cdHeader, "cloned-workflow-workflow.json")
	var exportBundle store.WorkflowExportBundle
	require.NoError(t, json.NewDecoder(res.Body).Decode(&exportBundle))
	res.Body.Close()
	require.Equal(t, 1, exportBundle.SchemaVersion)
	require.Equal(t, "Cloned Workflow", exportBundle.Workflow.Name)
	require.Len(t, exportBundle.States, 1)

	// 12c. Import Workflow: POST /v1/workflows/import -> 201 Created
	exportBundle.Workflow.Name = "Imported From Test"
	impBody, _ := json.Marshal(exportBundle)
	res, err = client.Post(server.URL+"/v1/workflows/import", "application/json", bytes.NewReader(impBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	var importedWf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&importedWf))
	res.Body.Close()
	require.Equal(t, "Imported From Test", importedWf["name"])
	impID := importedWf["id"].(string)
	require.NotEmpty(t, impID)

	// Verify imported workflow via GET
	res, err = client.Get(server.URL + "/v1/workflows/" + impID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var impGetRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&impGetRes))
	res.Body.Close()
	require.Len(t, impGetRes["states"].([]any), 1)

	// 13. DELETE /v1/workflows/{id} -> 204 No Content
	delReq, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/workflows/"+wfID, nil)
	require.NoError(t, err)
	res, err = client.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, res.StatusCode)
	res.Body.Close()

	// 14. Verify deleted -> 404
	res, err = client.Get(server.URL + "/v1/workflows/" + wfID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()

	// 15. DELETE non-existent -> 404
	delReq, err = http.NewRequest(http.MethodDelete, server.URL+"/v1/workflows/"+wfID, nil)
	require.NoError(t, err)
	res, err = client.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()
}
