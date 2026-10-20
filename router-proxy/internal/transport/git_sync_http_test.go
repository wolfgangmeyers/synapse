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
	pullFn func(ctx context.Context, repoURL, filename string) ([]byte, bool, error)
	pushFn func(ctx context.Context, repoURL, filename string, content []byte, commitMessage string) error
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
