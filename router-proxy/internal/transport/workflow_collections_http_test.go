package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestWorkflowCollectionsHTTP(t *testing.T) {
	s, _ := storetest.OpenTestStore(t)
	r, err := transport.New(transport.Options{Store: s})
	require.NoError(t, err)

	server := httptest.NewServer(r)
	defer server.Close()
	client := server.Client()

	// 1. GET /v1/workflow-collections initially contains "default"
	res, err := client.Get(server.URL + "/v1/workflow-collections")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var collections []map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&collections))
	res.Body.Close()
	require.Len(t, collections, 1)
	require.Equal(t, "default", collections[0]["id"])
	require.Equal(t, "Default", collections[0]["name"])
	require.Equal(t, float64(0), collections[0]["workflow_count"])

	// 2. GET /v1/workflow-collections/default -> 200
	res, err = client.Get(server.URL + "/v1/workflow-collections/default")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var defaultCol map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&defaultCol))
	res.Body.Close()
	require.Equal(t, "default", defaultCol["id"])
	require.Equal(t, "Default", defaultCol["name"])

	// 3. GET /v1/workflow-collections/non-existent -> 404
	res, err = client.Get(server.URL + "/v1/workflow-collections/does-not-exist")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()

	// 4. PUT /v1/workflow-collections/default -> 400 Bad Request
	putBody, _ := json.Marshal(map[string]any{"name": "Renamed Default"})
	putReq, err := http.NewRequest(http.MethodPut, server.URL+"/v1/workflow-collections/default", bytes.NewReader(putBody))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/json")
	res, err = client.Do(putReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	res.Body.Close()

	// 5. DELETE /v1/workflow-collections/default -> 400 Bad Request
	delReq, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/workflow-collections/default", nil)
	require.NoError(t, err)
	res, err = client.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	res.Body.Close()

	// 6. POST /v1/workflow-collections with invalid payload -> 400
	invalidBody, _ := json.Marshal(map[string]any{"name": ""})
	res, err = client.Post(server.URL+"/v1/workflow-collections", "application/json", bytes.NewReader(invalidBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	res.Body.Close()

	// 7. POST /v1/workflow-collections -> 201 Created
	createBody, _ := json.Marshal(map[string]any{"name": "DevOps", "description": "DevOps workflows"})
	res, err = client.Post(server.URL+"/v1/workflow-collections", "application/json", bytes.NewReader(createBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	var createdCol map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&createdCol))
	res.Body.Close()
	colID := createdCol["id"].(string)
	require.NotEmpty(t, colID)
	require.Equal(t, "DevOps", createdCol["name"])
	require.Equal(t, "DevOps workflows", createdCol["description"])
	require.Equal(t, float64(0), createdCol["workflow_count"])

	// 8. POST duplicate name -> 409 Conflict
	res, err = client.Post(server.URL+"/v1/workflow-collections", "application/json", bytes.NewReader(createBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, res.StatusCode)
	res.Body.Close()

	// 9. PUT /v1/workflow-collections/{id} -> 200
	updateBody, _ := json.Marshal(map[string]any{"name": "DevOps Automation", "description": "Updated desc"})
	putReq, err = http.NewRequest(http.MethodPut, server.URL+"/v1/workflow-collections/"+colID, bytes.NewReader(updateBody))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/json")
	res, err = client.Do(putReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var updatedCol map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&updatedCol))
	res.Body.Close()
	require.Equal(t, "DevOps Automation", updatedCol["name"])

	// 10. Create a workflow in that collection
	wfBody, _ := json.Marshal(map[string]any{
		"name":          "Deploy App",
		"collection_id": colID,
	})
	res, err = client.Post(server.URL+"/v1/workflows", "application/json", bytes.NewReader(wfBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	var createdWf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&createdWf))
	res.Body.Close()
	require.Equal(t, colID, createdWf["collection_id"])

	// Verify workflow count in collection
	res, err = client.Get(server.URL + "/v1/workflow-collections/" + colID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getCol map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getCol))
	res.Body.Close()
	require.Equal(t, float64(1), getCol["workflow_count"])

	// 11. DELETE /v1/workflow-collections/{id}?cascade=false on non-empty -> 409 Conflict
	delReq, err = http.NewRequest(http.MethodDelete, server.URL+"/v1/workflow-collections/"+colID+"?cascade=false", nil)
	require.NoError(t, err)
	res, err = client.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, res.StatusCode)
	res.Body.Close()

	// 12. DELETE /v1/workflow-collections/{id}?cascade=true -> 204 No Content
	delReq, err = http.NewRequest(http.MethodDelete, server.URL+"/v1/workflow-collections/"+colID+"?cascade=true", nil)
	require.NoError(t, err)
	res, err = client.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, res.StatusCode)
	res.Body.Close()

	// Verify collection deleted
	res, err = client.Get(server.URL + "/v1/workflow-collections/" + colID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()

	// Verify workflows inside were cascaded
	res, err = client.Get(server.URL + "/v1/workflows/" + createdWf["id"].(string))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()
}

func TestWorkflowsCollectionMoveAndDuplicateHTTP(t *testing.T) {
	ctx := context.Background()
	s, _ := storetest.OpenTestStore(t)
	r, err := transport.New(transport.Options{Store: s})
	require.NoError(t, err)

	server := httptest.NewServer(r)
	defer server.Close()
	client := server.Client()

	// Create two collections: colA and colB
	colABody, _ := json.Marshal(map[string]any{"name": "Collection A"})
	res, err := client.Post(server.URL+"/v1/workflow-collections", "application/json", bytes.NewReader(colABody))
	require.NoError(t, err)
	var colA map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&colA))
	res.Body.Close()
	colAID := colA["id"].(string)

	colBBody, _ := json.Marshal(map[string]any{"name": "Collection B"})
	res, err = client.Post(server.URL+"/v1/workflow-collections", "application/json", bytes.NewReader(colBBody))
	require.NoError(t, err)
	var colB map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&colB))
	res.Body.Close()
	colBID := colB["id"].(string)

	// Create workflow in colA
	wfBody, _ := json.Marshal(map[string]any{
		"name":          "My Workflow",
		"collection_id": colAID,
	})
	res, err = client.Post(server.URL+"/v1/workflows", "application/json", bytes.NewReader(wfBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	var wf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&wf))
	res.Body.Close()
	wfID := wf["id"].(string)

	// Test GET /v1/workflows?collection_id=...
	res, err = client.Get(server.URL + "/v1/workflows?collection_id=" + colAID)
	require.NoError(t, err)
	var wfsA []map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&wfsA))
	res.Body.Close()
	require.Len(t, wfsA, 1)
	require.Equal(t, wfID, wfsA[0]["id"])

	res, err = client.Get(server.URL + "/v1/workflows?collection_id=" + colBID)
	require.NoError(t, err)
	var wfsB []map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&wfsB))
	res.Body.Close()
	require.Len(t, wfsB, 0)

	// Test Duplicate to colB
	dupBody, _ := json.Marshal(map[string]any{
		"destination_collection_id": colBID,
		"name":                      "My Workflow Copied",
	})
	res, err = client.Post(server.URL+"/v1/workflows/"+wfID+"/duplicate", "application/json", bytes.NewReader(dupBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	var dupWf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&dupWf))
	res.Body.Close()
	require.Equal(t, colBID, dupWf["collection_id"])
	require.Equal(t, "My Workflow Copied", dupWf["name"])

	// Test Move to colB (create conflict first: create another workflow named "My Workflow" in colB)
	wfBConflictBody, _ := json.Marshal(map[string]any{
		"name":          "My Workflow",
		"collection_id": colBID,
	})
	res, err = client.Post(server.URL+"/v1/workflows", "application/json", bytes.NewReader(wfBConflictBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, res.StatusCode)
	res.Body.Close()

	// Move wfID ("My Workflow" in colA) to colB -> 409 Conflict because "My Workflow" already in colB
	moveBody, _ := json.Marshal(map[string]any{
		"destination_collection_id": colBID,
	})
	res, err = client.Post(server.URL+"/v1/workflows/"+wfID+"/move", "application/json", bytes.NewReader(moveBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, res.StatusCode)
	res.Body.Close()

	// Move to default collection -> 200 OK
	moveDefaultBody, _ := json.Marshal(map[string]any{
		"destination_collection_id": "default",
	})
	res, err = client.Post(server.URL+"/v1/workflows/"+wfID+"/move", "application/json", bytes.NewReader(moveDefaultBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var movedWf map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&movedWf))
	res.Body.Close()
	require.Equal(t, "default", movedWf["collection_id"])
	require.Equal(t, wfID, movedWf["id"])

	// Test error cases: POST /v1/workflows/nonexistent-id/move returns HTTP 404 (workflow_not_found)
	res, err = client.Post(server.URL+"/v1/workflows/nonexistent-id/move", "application/json", bytes.NewReader(moveDefaultBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	var moveErrResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&moveErrResp))
	res.Body.Close()
	errObj, ok := moveErrResp["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "workflow_not_found", errObj["code"])

	// Test error cases: POST /v1/workflows/nonexistent-id/duplicate returns HTTP 404 (workflow_not_found)
	res, err = client.Post(server.URL+"/v1/workflows/nonexistent-id/duplicate", "application/json", bytes.NewReader(dupBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	var dupErrResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&dupErrResp))
	res.Body.Close()
	errObj, ok = dupErrResp["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "workflow_not_found", errObj["code"])

	_ = ctx
}

func TestWorkflowCollectionsGitSyncBackwardsCompatibilityHTTP(t *testing.T) {
	ctx := context.Background()
	s, _ := storetest.OpenTestStore(t)
	mock := &mockGitSyncer{}
	r, err := transport.New(transport.Options{
		Store:     s,
		GitSyncer: mock,
	})
	require.NoError(t, err)

	server := httptest.NewServer(r)
	defer server.Close()
	client := server.Client()

	// 1. PUT /v1/git-sync/workflows sets repo URL
	setBody, _ := json.Marshal(map[string]any{
		"repo_url": "https://github.com/example/default-workflows.git",
	})
	putReq, err := http.NewRequest(http.MethodPut, server.URL+"/v1/git-sync/workflows", bytes.NewReader(setBody))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/json")
	res, err := client.Do(putReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var setResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&setResp))
	res.Body.Close()
	require.Equal(t, "workflows", setResp["target"])
	require.Equal(t, "https://github.com/example/default-workflows.git", setResp["repo_url"])

	// Verify target row in store
	targetRow, err := s.GetGitSyncTarget(ctx, "workflows")
	require.NoError(t, err)
	require.NotNil(t, targetRow)
	require.Equal(t, "https://github.com/example/default-workflows.git", targetRow.RepoURL)

	// Create a workflow in the default collection
	wfDefault, err := s.CreateWorkflow(ctx, store.WorkflowRequest{
		Name:        "Backwards Compat Workflow",
		Description: "Lives in default collection",
	})
	require.NoError(t, err)
	require.Equal(t, store.DefaultWorkflowCollectionID, wfDefault.CollectionID)

	// Create a custom collection with a workflow that should NOT be pushed when acting on default
	customCol, err := s.CreateWorkflowCollection(ctx, store.WorkflowCollectionRequest{
		Name: "custom-collection",
	})
	require.NoError(t, err)
	_, err = s.CreateWorkflow(ctx, store.WorkflowRequest{
		CollectionID: customCol.ID,
		Name:         "Custom Workflow",
	})
	require.NoError(t, err)

	// 2. POST /v1/git-sync/workflows/push acts on the default collection
	var pushedFile string
	var pushedBytes []byte
	var pushedRepo string
	mock.pushFn = func(ctx context.Context, repoURL, filename string, content []byte, commitMessage string) error {
		pushedRepo = repoURL
		pushedFile = filename
		pushedBytes = content
		return nil
	}

	res, err = client.Post(server.URL+"/v1/git-sync/workflows/push", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var pushResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&pushResp))
	res.Body.Close()
	require.Equal(t, "workflows", pushResp["target"])
	require.Equal(t, true, pushResp["success"])
	require.Equal(t, float64(1), pushResp["count"]) // Only 1 workflow in default collection!

	require.Equal(t, "https://github.com/example/default-workflows.git", pushedRepo)
	require.Equal(t, "workflows.json", pushedFile)

	var exportedPayload store.WorkflowsExportPayload
	require.NoError(t, json.Unmarshal(pushedBytes, &exportedPayload))
	require.Len(t, exportedPayload.Workflows, 1)
	require.Equal(t, "Backwards Compat Workflow", exportedPayload.Workflows[0].Workflow.Name)

	// 3. POST /v1/git-sync/workflows/pull acts on the default collection
	replacementPayload := store.WorkflowsExportPayload{
		Format:     "mecha-workflows",
		Version:    1,
		ExportedAt: "2026-11-16T12:00:00Z",
		Workflows: []store.WorkflowExportBundle{
			{
				SchemaVersion: 1,
				Workflow: store.WorkflowInfoExport{
					Name:        "Pulled Default Workflow",
					Description: "Imported via pull into default collection",
				},
				EntryStateKey: "entry",
				States: []store.WorkflowStateExport{
					{
						Key:          "entry",
						Name:         "Start",
						EntryText:    "Hello Start",
						IsEntryState: true,
					},
				},
			},
		},
	}
	replacementBytes, err := json.Marshal(replacementPayload)
	require.NoError(t, err)

	var pulledRepo string
	var pulledFile string
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		pulledRepo = repoURL
		pulledFile = filename
		return replacementBytes, true, nil
	}

	res, err = client.Post(server.URL+"/v1/git-sync/workflows/pull", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var pullResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&pullResp))
	res.Body.Close()
	require.Equal(t, "workflows", pullResp["target"])
	require.Equal(t, true, pullResp["exists"])
	require.Equal(t, float64(1), pullResp["count"])

	require.Equal(t, "https://github.com/example/default-workflows.git", pulledRepo)
	require.Equal(t, "workflows.json", pulledFile)

	// Verify default collection now contains "Pulled Default Workflow"
	defaultWfs, err := s.ListWorkflowsByCollection(ctx, store.DefaultWorkflowCollectionID)
	require.NoError(t, err)
	require.Len(t, defaultWfs, 1)
	require.Equal(t, "Pulled Default Workflow", defaultWfs[0].Name)
	require.Equal(t, store.DefaultWorkflowCollectionID, defaultWfs[0].CollectionID)

	// Verify custom collection workflows are completely untouched
	customWfs, err := s.ListWorkflowsByCollection(ctx, customCol.ID)
	require.NoError(t, err)
	require.Len(t, customWfs, 1)
	require.Equal(t, "Custom Workflow", customWfs[0].Name)
	require.Equal(t, customCol.ID, customWfs[0].CollectionID)
}
