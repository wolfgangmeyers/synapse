package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

type mockGitSyncer struct {
	pullFn          func(ctx context.Context, repoURL, filename string) ([]byte, bool, error)
	pushFn          func(ctx context.Context, repoURL, filename string, content []byte, commitMessage string) error
	pullDirectoryFn func(ctx context.Context, repoURL, targetDir string) error
	pushDirectoryFn func(ctx context.Context, repoURL, sourceDir, commitMessage string) error
}

func (m *mockGitSyncer) Pull(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
	if m.pullFn != nil {
		return m.pullFn(ctx, repoURL, filename)
	}
	return nil, false, nil
}

func (m *mockGitSyncer) Push(ctx context.Context, repoURL, filename string, content []byte, commitMessage string) error {
	if m.pushFn != nil {
		return m.pushFn(ctx, repoURL, filename, content, commitMessage)
	}
	return nil
}

func (m *mockGitSyncer) PullDirectory(ctx context.Context, repoURL, targetDir string) error {
	if m.pullDirectoryFn != nil {
		return m.pullDirectoryFn(ctx, repoURL, targetDir)
	}
	return nil
}

func (m *mockGitSyncer) PushDirectory(ctx context.Context, repoURL, sourceDir, commitMessage string) error {
	if m.pushDirectoryFn != nil {
		return m.pushDirectoryFn(ctx, repoURL, sourceDir, commitMessage)
	}
	return nil
}

func TestGitSyncHTTP_TargetCRUD(t *testing.T) {
	s, _ := store.OpenTestStore(t)
	r, err := transport.New(transport.Options{
		Store: s,
	})
	require.NoError(t, err)

	// GET initially 404
	req := httptest.NewRequest(http.MethodGet, "/v1/git-sync/prompt_templates", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	// PUT to set target
	putBody := `{"repo_url": "git@github.com:user/prompts.git"}`
	req = httptest.NewRequest(http.MethodPut, "/v1/git-sync/prompt_templates", bytes.NewBufferString(putBody))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var putResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &putResp))
	require.Equal(t, "prompt_templates", putResp["target"])
	require.Equal(t, "git@github.com:user/prompts.git", putResp["repo_url"])

	// GET returns updated target
	req = httptest.NewRequest(http.MethodGet, "/v1/git-sync/prompt_templates", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var getResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &getResp))
	require.Equal(t, "prompt_templates", getResp["target"])
	require.Equal(t, "git@github.com:user/prompts.git", getResp["repo_url"])

	// PUT with empty repo_url clears target
	clearBody := `{"repo_url": ""}`
	req = httptest.NewRequest(http.MethodPut, "/v1/git-sync/prompt_templates", bytes.NewBufferString(clearBody))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// GET now returns 404
	req = httptest.NewRequest(http.MethodGet, "/v1/git-sync/prompt_templates", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	// PUT again to restore
	req = httptest.NewRequest(http.MethodPut, "/v1/git-sync/prompt_templates", bytes.NewBufferString(putBody))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// DELETE method also removes target
	req = httptest.NewRequest(http.MethodDelete, "/v1/git-sync/prompt_templates", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// GET now returns 404
	req = httptest.NewRequest(http.MethodGet, "/v1/git-sync/prompt_templates", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestGitSyncHTTP_Pull(t *testing.T) {
	ctx := context.Background()
	s, _ := store.OpenTestStore(t)
	mock := &mockGitSyncer{}
	r, err := transport.New(transport.Options{
		Store:     s,
		GitSyncer: mock,
	})
	require.NoError(t, err)

	// Configure target first
	_, err = s.SetGitSyncTarget(ctx, "prompt_templates", "git@github.com:user/prompts.git")
	require.NoError(t, err)

	// Test 1: File does not exist on remote
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		require.Equal(t, "git@github.com:user/prompts.git", repoURL)
		require.Equal(t, "prompt-templates.json", filename)
		return nil, false, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/git-sync/prompt_templates/pull", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var pullResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pullResp))
	require.Equal(t, false, pullResp["exists"])
	require.Equal(t, "File not found", pullResp["message"])

	// Test 2: File exists and contains prompt templates
	fileContent := `{
		"version": 1,
		"templates": [
			{
				"id": "tpl-1",
				"name": "Template One",
				"body": "Body One",
				"created_at": "2026-01-01T00:00:00.000Z",
				"updated_at": "2026-01-01T00:00:00.000Z"
			},
			{
				"id": "tpl-2",
				"name": "Template Two",
				"body": "Body Two",
				"created_at": "2026-01-02T00:00:00.000Z",
				"updated_at": "2026-01-02T00:00:00.000Z"
			}
		]
	}`
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		return []byte(fileContent), true, nil
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/prompt_templates/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pullResp))
	require.Equal(t, true, pullResp["exists"])
	require.Equal(t, float64(2), pullResp["count"])

	// Verify store has the imported templates
	templates, err := s.PromptTemplates(ctx)
	require.NoError(t, err)
	require.Len(t, templates, 2)
	require.Equal(t, "tpl-1", templates[0].ID)
	require.Equal(t, "Template One", templates[0].Name)
}

func TestGitSyncHTTP_Push(t *testing.T) {
	ctx := context.Background()
	s, _ := store.OpenTestStore(t)
	mock := &mockGitSyncer{}
	r, err := transport.New(transport.Options{
		Store:     s,
		GitSyncer: mock,
	})
	require.NoError(t, err)

	// Populate templates in store
	_, err = s.CreatePromptTemplate(ctx, store.PromptTemplateRequest{
		Name: "Template Alpha",
		Body: "Alpha Body",
	})
	require.NoError(t, err)

	// Configure target
	_, err = s.SetGitSyncTarget(ctx, "prompt_templates", "git@github.com:user/prompts.git")
	require.NoError(t, err)

	var pushedBytes []byte
	var pushedCommitMsg string
	mock.pushFn = func(ctx context.Context, repoURL, filename string, content []byte, commitMessage string) error {
		require.Equal(t, "git@github.com:user/prompts.git", repoURL)
		require.Equal(t, "prompt-templates.json", filename)
		pushedBytes = content
		pushedCommitMsg = commitMessage
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/git-sync/prompt_templates/push", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var pushResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pushResp))
	require.Equal(t, true, pushResp["success"])
	require.Equal(t, float64(1), pushResp["count"])

	require.Contains(t, pushedCommitMsg, "Update prompt-templates.json")
	var exported map[string]any
	require.NoError(t, json.Unmarshal(pushedBytes, &exported))
	require.Equal(t, "mecha-prompt-templates", exported["format"])
	require.Equal(t, float64(1), exported["version"])
	templatesList, ok := exported["templates"].([]any)
	require.True(t, ok)
	require.Len(t, templatesList, 1)
}

func TestGitSyncHTTP_Validation(t *testing.T) {
	ctx := context.Background()
	s, _ := store.OpenTestStore(t)
	mock := &mockGitSyncer{}
	r, err := transport.New(transport.Options{
		Store:     s,
		GitSyncer: mock,
	})
	require.NoError(t, err)

	// Test 1: Invalid repo URL on PUT target
	putBody := `{"repo_url": "--upload-pack=exploit"}`
	req := httptest.NewRequest(http.MethodPut, "/v1/git-sync/prompt_templates", bytes.NewBufferString(putBody))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Set valid target
	_, err = s.SetGitSyncTarget(ctx, "prompt_templates", "https://github.com/user/prompts.git")
	require.NoError(t, err)

	// Test 2: Invalid pull payloads
	// 2a. Empty name
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		return []byte(`{"version":1,"templates":[{"name":"","body":"valid"}]}`), true, nil
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/prompt_templates/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 2b. Empty body
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		return []byte(`{"version":1,"templates":[{"name":"test","body":""}]}`), true, nil
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/prompt_templates/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 2c. Name too long (> 128)
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		longName := strings.Repeat("a", 129)
		return []byte(`{"version":1,"templates":[{"name":"` + longName + `","body":"valid"}]}`), true, nil
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/prompt_templates/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 2d. Duplicate names
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		return []byte(`{"version":1,"templates":[{"name":"dup","body":"1"},{"name":"dup","body":"2"}]}`), true, nil
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/prompt_templates/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGitSyncHTTP_Workflows_PushAndPull(t *testing.T) {
	ctx := context.Background()
	s, _ := store.OpenTestStore(t)
	mock := &mockGitSyncer{}
	r, err := transport.New(transport.Options{
		Store:     s,
		GitSyncer: mock,
	})
	require.NoError(t, err)

	// Configure target for workflows
	_, err = s.SetGitSyncTarget(ctx, "workflows", "git@github.com:user/workflows-repo.git")
	require.NoError(t, err)

	// 1. Create a workflow in store
	wf, err := s.CreateWorkflow(ctx, store.WorkflowRequest{
		Name:        "Test Workflow",
		Description: "A workflow for testing push and pull",
		Variables: &[]store.WorkflowVariableDefinition{
			{
				Name:         "VAR1",
				Label:        "Var 1",
				Type:         store.WorkflowVariableTypeText,
				DefaultValue: "val1",
			},
		},
	})
	require.NoError(t, err)

	x1, y1 := 10.0, 20.0
	st1, err := s.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		WorkflowID:   &wf.ID,
		Name:         "State One",
		Description:  "First state",
		EntryText:    "Entering one",
		Goal:         "Achieve one",
		AllowedTools: []string{"bash"},
		UIPosX:       &x1,
		UIPosY:       &y1,
	})
	require.NoError(t, err)

	x2, y2 := 100.0, 200.0
	st2, err := s.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		WorkflowID:   &wf.ID,
		Name:         "State Two",
		Description:  "Second state",
		EntryText:    "Entering two",
		Goal:         "Achieve two",
		AllowedTools: []string{"read"},
		UIPosX:       &x2,
		UIPosY:       &y2,
	})
	require.NoError(t, err)

	// Add transition from st1 -> st2
	_, err = s.UpdateWorkflowState(ctx, st1.ID, store.WorkflowStateRequest{
		Name:        st1.Name,
		Transitions: []store.WorkflowTransitionConfig{{TargetStateID: st2.ID, Description: "to state two"}},
	})
	require.NoError(t, err)

	// Set entry state
	_, err = s.UpdateWorkflow(ctx, wf.ID, store.WorkflowRequest{
		Name:         wf.Name,
		EntryStateID: &st1.ID,
	})
	require.NoError(t, err)

	// 2. Test Push
	var pushedFile string
	var pushedBytes []byte
	var pushedMsg string
	mock.pushFn = func(ctx context.Context, repoURL, filename string, content []byte, commitMessage string) error {
		require.Equal(t, "git@github.com:user/workflows-repo.git", repoURL)
		pushedFile = filename
		pushedBytes = content
		pushedMsg = commitMessage
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/git-sync/workflows/push", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var pushResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pushResp))
	require.Equal(t, "workflows", pushResp["target"])
	require.Equal(t, true, pushResp["success"])
	require.Equal(t, float64(1), pushResp["count"])

	require.Equal(t, "workflows.json", pushedFile)
	require.Equal(t, "Update workflows.json from app", pushedMsg)

	var exportedPayload store.WorkflowsExportPayload
	require.NoError(t, json.Unmarshal(pushedBytes, &exportedPayload))
	require.Equal(t, "mecha-workflows", exportedPayload.Format)
	require.Equal(t, 1, exportedPayload.Version)
	require.Len(t, exportedPayload.Workflows, 1)
	require.Equal(t, "Test Workflow", exportedPayload.Workflows[0].Workflow.Name)
	require.Len(t, exportedPayload.Workflows[0].States, 2)

	// 3. Test Pull - Not Found
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		require.Equal(t, "git@github.com:user/workflows-repo.git", repoURL)
		require.Equal(t, "workflows.json", filename)
		return nil, false, nil
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/workflows/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var notFoundResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &notFoundResp))
	require.Equal(t, "workflows", notFoundResp["target"])
	require.Equal(t, false, notFoundResp["exists"])
	require.Equal(t, "No workflows.json found in repository", notFoundResp["message"])

	// 4. Test Pull - Invalid JSON
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		return []byte("{invalid-json"), true, nil
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/workflows/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var errResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	require.Equal(t, false, errResp["success"])
	require.Contains(t, errResp["error"], "Invalid JSON")

	// 5. Test Pull - Unsupported Format / Version / Invalid Bundle
	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		return []byte(`{"format":"unknown-format","version":1,"workflows":[]}`), true, nil
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/workflows/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		return []byte(`{"format":"mecha-workflows","version":0,"workflows":[]}`), true, nil
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/workflows/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		return []byte(`{"format":"mecha-workflows","version":1,"workflows":[{"workflow":{"name":""}}]}`), true, nil
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/workflows/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// 6. Test Pull - Successful Replace
	newFileContent := `{
		"format": "mecha-workflows",
		"version": 1,
		"exported_at": "2026-06-01T00:00:00Z",
		"workflows": [
			{
				"schema_version": 1,
				"exported_at": "2026-06-01T00:00:00Z",
				"workflow": {
					"name": "Pulled Workflow",
					"description": "Pulled from remote git",
					"variables": []
				},
				"entry_state_key": "step-alpha",
				"states": [
					{
						"key": "step-alpha",
						"name": "Alpha Step",
						"description": "First step in pulled wf",
						"entry_text": "Run alpha",
						"goal": "Finish alpha",
						"is_entry_state": true,
						"allow_exit": true,
						"allowed_tools": ["bash"],
						"transitions": [
							{
								"target_state_key": "step-beta",
								"description": "Next is beta"
							}
						]
					},
					{
						"key": "step-beta",
						"name": "Beta Step",
						"description": "Second step in pulled wf",
						"entry_text": "Run beta",
						"goal": "Finish beta",
						"is_entry_state": false,
						"allow_exit": true,
						"allowed_tools": ["read"]
					}
				]
			}
		]
	}`

	mock.pullFn = func(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
		return []byte(newFileContent), true, nil
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/workflows/pull", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var pullSuccessResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pullSuccessResp))
	require.Equal(t, "workflows", pullSuccessResp["target"])
	require.Equal(t, true, pullSuccessResp["exists"])
	require.Equal(t, float64(1), pullSuccessResp["count"])

	// Verify store state has only the pulled workflow
	workflows, err := s.ListWorkflows(ctx)
	require.NoError(t, err)
	require.Len(t, workflows, 1)
	require.Equal(t, "Pulled Workflow", workflows[0].Name)
	require.Equal(t, "Pulled from remote git", workflows[0].Description)
	require.Equal(t, 2, workflows[0].StateCount)
	require.NotNil(t, workflows[0].EntryStateID)

	pulledStates, err := s.ListWorkflowStatesByWorkflow(ctx, workflows[0].ID)
	require.NoError(t, err)
	require.Len(t, pulledStates, 2)
}

func TestGitSyncHTTP_KB_PushAndPull(t *testing.T) {
	ctx := context.Background()
	s, _ := store.OpenTestStore(t)
	mock := &mockGitSyncer{}
	r, err := transport.New(transport.Options{
		Store:     s,
		GitSyncer: mock,
	})
	require.NoError(t, err)

	tempKB := t.TempDir()
	t.Setenv("MECHA_KB_DIR", tempKB)

	_, err = s.SetGitSyncTarget(ctx, "kb", "git@github.com:user/kb-repo.git")
	require.NoError(t, err)

	// Test Pull
	var pulledRepo, pulledTarget string
	mock.pullDirectoryFn = func(ctx context.Context, repoURL, targetDir string) error {
		pulledRepo = repoURL
		pulledTarget = targetDir
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/git-sync/kb/pull", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var pullResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pullResp))
	require.Equal(t, "kb", pullResp["target"])
	require.Equal(t, true, pullResp["success"])
	require.Equal(t, "git@github.com:user/kb-repo.git", pulledRepo)
	require.Equal(t, tempKB, pulledTarget)

	// Test Push
	var pushedRepo, pushedSource, pushedMsg string
	mock.pushDirectoryFn = func(ctx context.Context, repoURL, sourceDir, commitMessage string) error {
		pushedRepo = repoURL
		pushedSource = sourceDir
		pushedMsg = commitMessage
		return nil
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/git-sync/kb/push", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var pushResp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pushResp))
	require.Equal(t, "kb", pushResp["target"])
	require.Equal(t, true, pushResp["success"])
	require.Equal(t, "git@github.com:user/kb-repo.git", pushedRepo)
	require.Equal(t, tempKB, pushedSource)
	require.Equal(t, "Update kb files from app", pushedMsg)
}

