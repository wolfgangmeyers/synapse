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

func TestCreateWorktree_NewBranch(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)

	wtPath, err := GenerateWorktreePath(repo, "feat/brand-new-branch")
	if err != nil {
		t.Fatalf("GenerateWorktreePath error: %v", err)
	}
	defer os.RemoveAll(wtPath)

	err = CreateWorktree(ctx, repo, "feat/brand-new-branch", wtPath)
	if err != nil {
		t.Fatalf("CreateWorktree failed for new branch: %v", err)
	}

	// Verify the worktree is on the new branch
	branch := runGit(t, wtPath, "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "feat/brand-new-branch" {
		t.Errorf("expected branch feat/brand-new-branch, got %s", branch)
	}

	// Clean up
	if err := RemoveWorktree(ctx, repo, wtPath, true); err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}
}

func TestCreateWorktree_ExistingBranch(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)

	// Create the branch in the repo beforehand
	runGit(t, repo, "branch", "feat/existing-branch")

	wtPath, err := GenerateWorktreePath(repo, "feat/existing-branch")
	if err != nil {
		t.Fatalf("GenerateWorktreePath error: %v", err)
	}
	defer os.RemoveAll(wtPath)

	// CreateWorktree should detect the existing branch and checkout without -b
	err = CreateWorktree(ctx, repo, "feat/existing-branch", wtPath)
	if err != nil {
		t.Fatalf("CreateWorktree failed for existing branch: %v", err)
	}

	// Verify the worktree is on the existing branch
	branch := runGit(t, wtPath, "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "feat/existing-branch" {
		t.Errorf("expected branch feat/existing-branch, got %s", branch)
	}

	// Clean up
	if err := RemoveWorktree(ctx, repo, wtPath, true); err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}
}

func TestCreateWorktree_ExistingRemoteTrackingBranch(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)

	// Configure remote origin and create remote-tracking ref
	runGit(t, repo, "config", "remote.origin.url", "https://example.com/repo.git")
	runGit(t, repo, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	runGit(t, repo, "update-ref", "refs/remotes/origin/feat/remote-branch", "HEAD")

	wtPath, err := GenerateWorktreePath(repo, "feat/remote-branch")
	if err != nil {
		t.Fatalf("GenerateWorktreePath error: %v", err)
	}
	defer os.RemoveAll(wtPath)

	// CreateWorktree should detect remote-tracking branch and succeed
	err = CreateWorktree(ctx, repo, "feat/remote-branch", wtPath)
	if err != nil {
		t.Fatalf("CreateWorktree failed for remote tracking branch: %v", err)
	}

	branch := runGit(t, wtPath, "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "feat/remote-branch" {
		t.Errorf("expected branch feat/remote-branch, got %s", branch)
	}

	// Clean up
	if err := RemoveWorktree(ctx, repo, wtPath, true); err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}
}

func TestCreateWorktree_ExistingBranch_AlreadyCheckedOut(t *testing.T) {
	ctx := context.Background()
	repo := initGitRepo(t)

	// "main" is already checked out in repo
	wtPath, err := GenerateWorktreePath(repo, "main")
	if err != nil {
		t.Fatalf("GenerateWorktreePath error: %v", err)
	}
	defer os.RemoveAll(wtPath)

	err = CreateWorktree(ctx, repo, "main", wtPath)
	if err == nil {
		t.Fatalf("expected error when checking out already checked-out branch 'main'")
	}
	if !strings.Contains(err.Error(), "already used by worktree") && !strings.Contains(err.Error(), "already checked out") {
		t.Errorf("unexpected error message: %v", err)
	}
}
