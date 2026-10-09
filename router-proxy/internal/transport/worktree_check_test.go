package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
