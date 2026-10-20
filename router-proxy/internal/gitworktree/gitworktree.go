package gitworktree

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// WorktreeCheckResult represents the inspection status of a path for git worktree operations.
type WorktreeCheckResult struct {
	IsGitRepo             bool `json:"is_git_repo"`
	IsMainWorktree        bool `json:"is_main_worktree"`
	HasUncommittedChanges bool `json:"has_uncommitted_changes"`
}

var nonSlugRegex = regexp.MustCompile(`[^a-zA-Z0-9_\-]+`)

func slug(s string) string {
	cleaned := nonSlugRegex.ReplaceAllString(s, "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "branch"
	}
	return cleaned
}

func randomHex(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CheckWorktree inspects path to determine if it is a git repo, whether it is the main worktree,
// and whether it contains uncommitted changes.
func CheckWorktree(ctx context.Context, path string) (WorktreeCheckResult, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return WorktreeCheckResult{IsGitRepo: false}, nil
		}
		return WorktreeCheckResult{}, err
	}
	if !fi.IsDir() {
		return WorktreeCheckResult{IsGitRepo: false}, nil
	}

	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return WorktreeCheckResult{IsGitRepo: false}, nil
	}

	result := WorktreeCheckResult{
		IsGitRepo: true,
	}

	// Determine if path is the main worktree:
	// In the main worktree, `git rev-parse --git-dir` equals `--git-common-dir` or `.git`,
	// and `.git` in the worktree root is a directory rather than a file.
	// We can also verify via `git worktree list --porcelain`, where the first "worktree <path>" line is the main worktree.
	cmdWorktreeList := exec.CommandContext(ctx, "git", "-C", path, "worktree", "list", "--porcelain")
	wtOut, err := cmdWorktreeList.Output()
	if err == nil {
		lines := strings.Split(string(wtOut), "\n")
		var firstWorktreePath string
		for _, line := range lines {
			if strings.HasPrefix(line, "worktree ") {
				firstWorktreePath = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
				break
			}
		}

		if firstWorktreePath != "" {
			resolvedTarget, err1 := filepath.EvalSymlinks(path)
			if err1 != nil {
				resolvedTarget = filepath.Clean(path)
			}
			resolvedTargetAbs, errAbs := filepath.Abs(resolvedTarget)
			if errAbs == nil {
				resolvedTarget = resolvedTargetAbs
			}

			resolvedFirst, err2 := filepath.EvalSymlinks(firstWorktreePath)
			if err2 != nil {
				resolvedFirst = filepath.Clean(firstWorktreePath)
			}
			resolvedFirstAbs, errAbs2 := filepath.Abs(resolvedFirst)
			if errAbs2 == nil {
				resolvedFirst = resolvedFirstAbs
			}

			if resolvedTarget == resolvedFirst {
				result.IsMainWorktree = true
			}
		}
	} else {
		// Fallback to git-dir vs git-common-dir
		gitDirCmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--git-dir")
		gitDirOut, errDir := gitDirCmd.Output()
		commonDirCmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--git-common-dir")
		commonDirOut, errCommon := commonDirCmd.Output()
		if errDir == nil && errCommon == nil {
			gd := strings.TrimSpace(string(gitDirOut))
			cd := strings.TrimSpace(string(commonDirOut))
			if gd == cd || gd == ".git" {
				dotGitStat, statErr := os.Stat(filepath.Join(path, ".git"))
				if statErr == nil && dotGitStat.IsDir() {
					result.IsMainWorktree = true
				}
			}
		}
	}

	// Check for uncommitted changes
	statusCmd := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain")
	statusOut, err := statusCmd.Output()
	if err == nil {
		if strings.TrimSpace(string(statusOut)) != "" {
			result.HasUncommittedChanges = true
		}
	}

	return result, nil
}

// refExists reports whether a git ref exists in the repository.
func refExists(ctx context.Context, baseRepoPath, ref string) bool {
	if baseRepoPath == "" || ref == "" {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "-C", baseRepoPath, "show-ref", "--verify", "--quiet", ref)
	return cmd.Run() == nil
}

// branchExists reports whether branchName exists locally (refs/heads/<branchName>)
// or in remote tracking (refs/remotes/origin/<branchName>).
func branchExists(ctx context.Context, baseRepoPath, branchName string) bool {
	if baseRepoPath == "" || branchName == "" {
		return false
	}
	if refExists(ctx, baseRepoPath, "refs/heads/"+branchName) {
		return true
	}
	return refExists(ctx, baseRepoPath, "refs/remotes/origin/"+branchName)
}

// CreateWorktree runs `git -C <baseRepoPath> worktree add <worktreePath> <branchName>` if branchName
// exists locally or in remote tracking.
// For a new branch, it runs best-effort `git -C <baseRepoPath> fetch origin main` (ignoring errors).
// If `refs/remotes/origin/main` exists, it creates the branch based on origin/main:
// `git -C <baseRepoPath> worktree add -b <branchName> <worktreePath> origin/main`.
// Otherwise, it falls back to:
// `git -C <baseRepoPath> worktree add -b <branchName> <worktreePath>`.
func CreateWorktree(ctx context.Context, baseRepoPath, branchName, worktreePath string) error {
	if baseRepoPath == "" {
		return errors.New("baseRepoPath cannot be empty")
	}
	if branchName == "" {
		return errors.New("branchName cannot be empty")
	}
	if worktreePath == "" {
		return errors.New("worktreePath cannot be empty")
	}

	var cmd *exec.Cmd
	if branchExists(ctx, baseRepoPath, branchName) {
		cmd = exec.CommandContext(ctx, "git", "-C", baseRepoPath, "worktree", "add", worktreePath, branchName)
	} else {
		// Best-effort fetch origin main before creating new branch
		fetchCmd := exec.CommandContext(ctx, "git", "-C", baseRepoPath, "fetch", "origin", "main")
		_ = fetchCmd.Run()

		if refExists(ctx, baseRepoPath, "refs/remotes/origin/main") {
			cmd = exec.CommandContext(ctx, "git", "-C", baseRepoPath, "worktree", "add", "-b", branchName, worktreePath, "origin/main")
		} else {
			cmd = exec.CommandContext(ctx, "git", "-C", baseRepoPath, "worktree", "add", "-b", branchName, worktreePath)
		}
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveWorktree runs `git -C <baseRepoPath> worktree remove --force <worktreePath>` followed by prune.
func RemoveWorktree(ctx context.Context, baseRepoPath, worktreePath string, force bool) error {
	if worktreePath == "" {
		return errors.New("worktreePath cannot be empty")
	}

	// Try removing via baseRepoPath if provided
	args := []string{}
	runPath := baseRepoPath
	if runPath == "" {
		runPath = worktreePath
	}

	if force {
		args = append(args, "-C", runPath, "worktree", "remove", "--force", worktreePath)
	} else {
		args = append(args, "-C", runPath, "worktree", "remove", worktreePath)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	_, err := cmd.CombinedOutput()
	if err != nil {
		// If worktree directory doesn't exist or git worktree remove fails,
		// attempt to prune anyway and remove directory if present.
		_ = os.RemoveAll(worktreePath)
	}

	prunePath := baseRepoPath
	if prunePath != "" {
		pruneCmd := exec.CommandContext(ctx, "git", "-C", prunePath, "worktree", "prune")
		_ = pruneCmd.Run()
	}

	return nil
}

// GenerateWorktreePath generates a sibling path: <parent>/<baseDir>-<slug(branchName)>-<randomHex(4)>
func GenerateWorktreePath(baseRepoPath, branchName string) (string, error) {
	if baseRepoPath == "" {
		return "", errors.New("baseRepoPath cannot be empty")
	}
	cleanBase := filepath.Clean(baseRepoPath)
	parentDir := filepath.Dir(cleanBase)
	baseName := filepath.Base(cleanBase)
	bSlug := slug(branchName)

	rnd, err := randomHex(4)
	if err != nil {
		return "", err
	}

	name := fmt.Sprintf("%s-%s-%s", baseName, bSlug, rnd)
	return filepath.Join(parentDir, name), nil
}
