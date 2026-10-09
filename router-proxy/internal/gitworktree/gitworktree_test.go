package gitworktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s failed: %v: %s", strings.Join(args, " "), dir, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")

	// Commit initial file
	testFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial commit")
	return dir
}

func TestCheckWorktree_NotRepo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	res, err := CheckWorktree(ctx, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsGitRepo {
		t.Errorf("expected IsGitRepo false, got true")
	}
	if res.IsMainWorktree {
		t.Errorf("expected IsMainWorktree false, got true")
	}
}

func TestCheckWorktree_NonExistent(t *testing.T) {
	ctx := context.Background()
	res, err := CheckWorktree(ctx, filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsGitRepo {
		t.Errorf("expected IsGitRepo false")
	}
}

func TestCheckWorktree_MainWorktree(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)

	res, err := CheckWorktree(ctx, repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsGitRepo {
		t.Errorf("expected IsGitRepo true")
	}
	if !res.IsMainWorktree {
		t.Errorf("expected IsMainWorktree true")
	}
	if res.HasUncommittedChanges {
		t.Errorf("expected HasUncommittedChanges false")
	}

	// Make an uncommitted change
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Modified\n"), 0644); err != nil {
		t.Fatal(err)
	}
	res, err = CheckWorktree(ctx, repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.HasUncommittedChanges {
		t.Errorf("expected HasUncommittedChanges true")
	}
}

func TestCreateAndRemoveWorktree(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)

	wtPath, err := GenerateWorktreePath(repo, "feat/my-feature")
	if err != nil {
		t.Fatalf("GenerateWorktreePath error: %v", err)
	}
	if !strings.Contains(wtPath, "feat-my-feature") {
		t.Errorf("expected slugified branch in wtPath, got: %s", wtPath)
	}

	err = CreateWorktree(ctx, repo, "feat/my-feature", wtPath)
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}
	defer os.RemoveAll(wtPath)

	// Check the created worktree
	res, err := CheckWorktree(ctx, wtPath)
	if err != nil {
		t.Fatalf("CheckWorktree failed: %v", err)
	}
	if !res.IsGitRepo {
		t.Errorf("expected IsGitRepo true for linked worktree")
	}
	if res.IsMainWorktree {
		t.Errorf("expected IsMainWorktree false for linked worktree")
	}
	if res.HasUncommittedChanges {
		t.Errorf("expected HasUncommittedChanges false")
	}

	// Remove worktree
	err = RemoveWorktree(ctx, repo, wtPath, true)
	if err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}

	// Verify worktree is gone
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("expected worktree path to be removed")
	}
}
