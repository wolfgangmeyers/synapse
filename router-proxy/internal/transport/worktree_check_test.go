package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/gitworktree"
)

func TestHandleGitWorktreeCheck(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "worktree-check-http-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gitDir := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = gitDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v, out: %s", err, string(out))
	}

	r := &Router{}

	// Test missing path param -> 400
	reqMissing := httptest.NewRequest(http.MethodGet, "/v1/git/worktree-check", nil)
	rrMissing := httptest.NewRecorder()
	r.handleGitWorktreeCheck(rrMissing, reqMissing)
	if rrMissing.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing path, got %d", rrMissing.Code)
	}

	// Test valid repo path -> 200 with JSON WorktreeCheckResult
	reqValid := httptest.NewRequest(http.MethodGet, "/v1/git/worktree-check?path="+gitDir, nil)
	rrValid := httptest.NewRecorder()
	r.handleGitWorktreeCheck(rrValid, reqValid)
	if rrValid.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid git repo, got %d (body: %s)", rrValid.Code, rrValid.Body.String())
	}

	var result gitworktree.WorktreeCheckResult
	if err := json.Unmarshal(rrValid.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !result.IsGitRepo || !result.IsMainWorktree {
		t.Fatalf("expected IsGitRepo=true and IsMainWorktree=true, got %+v", result)
	}

	if !managedPath("/v1/git/worktree-check") {
		t.Fatalf("expected managedPath to match /v1/git/worktree-check")
	}
}

func TestHandleManagedLaunchWorktreeFieldsAccepted(t *testing.T) {
	r := &Router{}
	// Unknown field should yield UNKNOWN_FIELD (400)
	badBody := `{"name":"","invalid_field":true}`
	badReq := httptest.NewRequest(http.MethodPost, "/v1/managed-agents", strings.NewReader(badBody))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174000")
	badRR := httptest.NewRecorder()
	r.handleManagedLaunch(badRR, badReq)
	if badRR.Code != http.StatusBadRequest || !strings.Contains(badRR.Body.String(), "UNKNOWN_FIELD") {
		t.Fatalf("expected UNKNOWN_FIELD 400 for invalid_field, got %d: %s", badRR.Code, badRR.Body.String())
	}

	// Worktree fields must be accepted by unknown field validator and proceed to name validation (422)
	goodBody := `{"name":"","worktree":true,"worktree_branch":"feature-branch"}`
	goodReq := httptest.NewRequest(http.MethodPost, "/v1/managed-agents", strings.NewReader(goodBody))
	goodReq.Header.Set("Content-Type", "application/json")
	goodReq.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174000")
	goodRR := httptest.NewRecorder()
	r.handleManagedLaunch(goodRR, goodReq)
	if goodRR.Code == http.StatusBadRequest && strings.Contains(goodRR.Body.String(), "UNKNOWN_FIELD") {
		t.Fatalf("worktree and worktree_branch were rejected as UNKNOWN_FIELD: %s", goodRR.Body.String())
	}
	if goodRR.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 VALIDATION_FAILED, got %d: %s", goodRR.Code, goodRR.Body.String())
	}
}

