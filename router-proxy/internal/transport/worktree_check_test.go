package transport

import (
	"encoding/json"
	"fmt"
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

	// Create an uncommitted file
	if err := os.WriteFile(filepath.Join(gitDir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// Default (without include_status param) defaults to include_status=true and checks uncommitted changes
	reqDefault := httptest.NewRequest(http.MethodGet, "/v1/git/worktree-check?path="+gitDir, nil)
	rrDefault := httptest.NewRecorder()
	r.handleGitWorktreeCheck(rrDefault, reqDefault)
	if rrDefault.Code != http.StatusOK {
		t.Fatalf("expected 200 for default include_status, got %d", rrDefault.Code)
	}
	var resDefault gitworktree.WorktreeCheckResult
	if err := json.Unmarshal(rrDefault.Body.Bytes(), &resDefault); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resDefault.HasUncommittedChanges {
		t.Errorf("expected HasUncommittedChanges=true when include_status is omitted")
	}

	// With include_status=false -> skips status check and returns has_uncommitted_changes=false
	reqNoStatus := httptest.NewRequest(http.MethodGet, "/v1/git/worktree-check?path="+gitDir+"&include_status=false", nil)
	rrNoStatus := httptest.NewRecorder()
	r.handleGitWorktreeCheck(rrNoStatus, reqNoStatus)
	if rrNoStatus.Code != http.StatusOK {
		t.Fatalf("expected 200 for include_status=false, got %d", rrNoStatus.Code)
	}
	var resNoStatus gitworktree.WorktreeCheckResult
	if err := json.Unmarshal(rrNoStatus.Body.Bytes(), &resNoStatus); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resNoStatus.IsGitRepo || !resNoStatus.IsMainWorktree {
		t.Errorf("expected IsGitRepo=true and IsMainWorktree=true for include_status=false, got %+v", resNoStatus)
	}
	if resNoStatus.HasUncommittedChanges {
		t.Errorf("expected HasUncommittedChanges=false when include_status=false, got %+v", resNoStatus)
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

func TestManagedLaunchGitWorktreeErrorHTTPResponse(t *testing.T) {
	r := &Router{}
	req := httptest.NewRequest(http.MethodPost, "/v1/managed-agents", nil)
	req.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174000")
	rr := httptest.NewRecorder()

	wtErr := fmt.Errorf("failed to create git worktree: git worktree add failed: exit status 128: fatal: 'main' is already used by worktree at '/foo/bar'")
	r.writeManagedError(rr, req, managedLaunchError(wtErr))

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}

	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if payload.Error.Code != "VALIDATION_FAILED" {
		t.Errorf("expected code VALIDATION_FAILED, got %s", payload.Error.Code)
	}
	expected := "Git worktree error: 'main' is already used by worktree at '/foo/bar'"
	if payload.Error.Message != expected {
		t.Errorf("expected clean message %q, got %q", expected, payload.Error.Message)
	}
}
