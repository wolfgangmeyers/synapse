package gitsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ValidateRepoURL ensures the URL starts with https://, http://, git@, or ssh://,
// contains no leading dashes or flags, and rejects dangerous protocols (ext::, etc.).
func ValidateRepoURL(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return errors.New("repository URL is required")
	}
	if strings.HasPrefix(trimmed, "-") {
		return errors.New("repository URL cannot start with a dash")
	}

	// Disallow dangerous git remote helper protocol patterns (like ext:: or fd::)
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "::") {
		return errors.New("repository URL contains unsupported protocol helper syntax")
	}

	// Check allowed prefixes
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		u, err := url.Parse(trimmed)
		if err != nil || u.Host == "" {
			return errors.New("invalid HTTP(S) repository URL")
		}
		return nil
	}

	if strings.HasPrefix(lower, "ssh://") {
		u, err := url.Parse(trimmed)
		if err != nil || u.Host == "" {
			return errors.New("invalid SSH repository URL")
		}
		return nil
	}

	// SCP-style syntax: git@host:owner/repo.git or user@host:path
	if strings.HasPrefix(lower, "git@") {
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 && parts[1] != "" && !strings.HasPrefix(parts[1], "-") {
			return nil
		}
		return errors.New("invalid SCP-style git@ URL format")
	}

	return errors.New("unsupported repository URL scheme: must start with https://, http://, ssh://, or git@")
}

// Syncer provides git repository pull and push operations against a local clone cache.
type Syncer interface {
	Pull(ctx context.Context, repoURL, filename string) (content []byte, exists bool, err error)
	Push(ctx context.Context, repoURL, filename string, content []byte, commitMessage string) error
	PullDirectory(ctx context.Context, repoURL, targetDir string) error
	PushDirectory(ctx context.Context, repoURL, sourceDir, commitMessage string) error
}

// Engine implements Syncer using system git.
type Engine struct {
	baseDir string
	mu      sync.Mutex // serializes operations per cache or globally to prevent concurrent git conflicts
}

// Option configures an Engine.
type Option func(*Engine)

// WithBaseDir sets the base directory for git repository clones.
func WithBaseDir(dir string) Option {
	return func(e *Engine) {
		e.baseDir = dir
	}
}

// New creates a new git sync Engine.
func New(opts ...Option) (*Engine, error) {
	e := &Engine{}
	for _, opt := range opts {
		opt(e)
	}

	if e.baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("user home dir: %w", err)
		}
		e.baseDir = filepath.Join(home, ".mecha", "git-sync")
	}

	if err := os.MkdirAll(e.baseDir, 0700); err != nil {
		return nil, fmt.Errorf("create base dir %s: %w", e.baseDir, err)
	}

	return e, nil
}

// RepoDir returns the cached directory path for a repo URL.
func (e *Engine) RepoDir(repoURL string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(repoURL)))
	return filepath.Join(e.baseDir, hex.EncodeToString(h[:16]))
}

// runGit executes a git command in dir with context.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmdArgs := make([]string, 0, len(args)+2)
	if dir != "" {
		cmdArgs = append(cmdArgs, "-C", dir)
	}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s failed: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// hasRemoteBranch checks if the remote has the given branch.
func hasRemoteBranch(ctx context.Context, dir, branch string) (bool, error) {
	out, err := runGit(ctx, dir, "ls-remote", "--heads", "origin", branch)
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "refs/heads/"+branch), nil
}

// ensureCloned ensures repoURL is cloned into cacheDir.
func (e *Engine) ensureCloned(ctx context.Context, repoURL, cacheDir string) error {
	gitDir := filepath.Join(cacheDir, ".git")
	fi, err := os.Stat(gitDir)
	if err == nil && fi.IsDir() {
		// Already cloned
		return nil
	}

	// Remove any partial dir
	_ = os.RemoveAll(cacheDir)
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0700); err != nil {
		return err
	}

	// Clone repo
	_, err = runGit(ctx, "", "clone", "--", repoURL, cacheDir)
	if err != nil {
		// If clone fails, check if it's an empty repo or fatal error
		return fmt.Errorf("clone repo: %w", err)
	}
	return nil
}

// Pull fetches the latest changes from origin/main and reads filename.
func (e *Engine) Pull(ctx context.Context, repoURL, filename string) ([]byte, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil, false, errors.New("repo URL is required")
	}
	if filename == "" {
		return nil, false, errors.New("filename is required")
	}

	cacheDir := e.RepoDir(repoURL)
	if err := e.ensureCloned(ctx, repoURL, cacheDir); err != nil {
		return nil, false, err
	}

	hasMain, err := hasRemoteBranch(ctx, cacheDir, "main")
	if err != nil {
		return nil, false, fmt.Errorf("check remote main: %w", err)
	}

	if hasMain {
		// Fetch, checkout, reset
		if _, err := runGit(ctx, cacheDir, "fetch", "origin", "main"); err != nil {
			return nil, false, fmt.Errorf("fetch origin main: %w", err)
		}
		if _, err := runGit(ctx, cacheDir, "checkout", "main"); err != nil {
			return nil, false, fmt.Errorf("checkout main: %w", err)
		}
		if _, err := runGit(ctx, cacheDir, "reset", "--hard", "origin/main"); err != nil {
			return nil, false, fmt.Errorf("reset hard origin/main: %w", err)
		}
	}

	filePath := filepath.Join(cacheDir, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read file: %w", err)
	}

	return data, true, nil
}

// Push writes content to filename, commits, and pushes to origin main.
func (e *Engine) Push(ctx context.Context, repoURL, filename string, content []byte, commitMessage string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return errors.New("repo URL is required")
	}
	if filename == "" {
		return errors.New("filename is required")
	}
	if commitMessage == "" {
		commitMessage = "Update " + filename
	}

	cacheDir := e.RepoDir(repoURL)
	if err := e.ensureCloned(ctx, repoURL, cacheDir); err != nil {
		return err
	}

	hasMain, err := hasRemoteBranch(ctx, cacheDir, "main")
	if err != nil {
		return fmt.Errorf("check remote main: %w", err)
	}

	if hasMain {
		if _, err := runGit(ctx, cacheDir, "fetch", "origin", "main"); err != nil {
			return fmt.Errorf("fetch origin main: %w", err)
		}
		if _, err := runGit(ctx, cacheDir, "checkout", "main"); err != nil {
			return fmt.Errorf("checkout main: %w", err)
		}
		if _, err := runGit(ctx, cacheDir, "reset", "--hard", "origin/main"); err != nil {
			return fmt.Errorf("reset hard origin/main: %w", err)
		}
	} else {
		// Repository is empty or main does not exist yet.
		// Checkout or create branch 'main'
		_, _ = runGit(ctx, cacheDir, "checkout", "-b", "main")
	}

	// Write file
	filePath := filepath.Join(cacheDir, filename)
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return fmt.Errorf("mkdir file dir: %w", err)
	}
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// git add
	if _, err := runGit(ctx, cacheDir, "add", filename); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// Check status
	status, err := runGit(ctx, cacheDir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		// Nothing changed
		return nil
	}

	// git commit
	if _, err := runGit(ctx, cacheDir, "commit", "-m", commitMessage); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	// git push origin main
	if _, err := runGit(ctx, cacheDir, "push", "origin", "main"); err != nil {
		return fmt.Errorf("git push: %w", err)
	}

	return nil
}

// PullDirectory fetches the latest changes from origin/main and copies all files from cacheDir to targetDir (skipping .git).
func (e *Engine) PullDirectory(ctx context.Context, repoURL, targetDir string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return errors.New("repo URL is required")
	}
	if targetDir == "" {
		return errors.New("target directory is required")
	}

	cacheDir := e.RepoDir(repoURL)
	if err := e.ensureCloned(ctx, repoURL, cacheDir); err != nil {
		return err
	}

	hasMain, err := hasRemoteBranch(ctx, cacheDir, "main")
	if err != nil {
		return fmt.Errorf("check remote main: %w", err)
	}

	if hasMain {
		// Fetch, checkout, reset
		if _, err := runGit(ctx, cacheDir, "fetch", "origin", "main"); err != nil {
			return fmt.Errorf("fetch origin main: %w", err)
		}
		if _, err := runGit(ctx, cacheDir, "checkout", "main"); err != nil {
			return fmt.Errorf("checkout main: %w", err)
		}
		if _, err := runGit(ctx, cacheDir, "reset", "--hard", "origin/main"); err != nil {
			return fmt.Errorf("reset hard origin/main: %w", err)
		}
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("create target dir %s: %w", targetDir, err)
	}

	// Build map of relative paths existing in cacheDir (excluding .git)
	cacheFiles := make(map[string]bool)
	err = filepath.Walk(cacheDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(cacheDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() {
			cacheFiles[rel] = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan cacheDir: %w", err)
	}

	// Remove files from targetDir that are not in cacheDir (excluding .git)
	err = filepath.Walk(targetDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(targetDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() && !cacheFiles[rel] {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove stale target file %s: %w", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cleanup targetDir: %w", err)
	}

	// Copy all files from cacheDir to targetDir (skipping .git)
	err = filepath.Walk(cacheDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(cacheDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		dstPath := filepath.Join(targetDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dstPath), err)
		}
		if err := os.WriteFile(dstPath, data, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write %s: %w", dstPath, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("copy directory: %w", err)
	}

	return nil
}

// PushDirectory syncs contents of sourceDir into cacheDir (excluding .git), commits, and pushes to origin main.
func (e *Engine) PushDirectory(ctx context.Context, repoURL, sourceDir, commitMessage string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return errors.New("repo URL is required")
	}
	if sourceDir == "" {
		return errors.New("source directory is required")
	}
	if commitMessage == "" {
		commitMessage = "Update files"
	}

	cacheDir := e.RepoDir(repoURL)
	if err := e.ensureCloned(ctx, repoURL, cacheDir); err != nil {
		return err
	}

	hasMain, err := hasRemoteBranch(ctx, cacheDir, "main")
	if err != nil {
		return fmt.Errorf("check remote main: %w", err)
	}

	if hasMain {
		if _, err := runGit(ctx, cacheDir, "fetch", "origin", "main"); err != nil {
			return fmt.Errorf("fetch origin main: %w", err)
		}
		if _, err := runGit(ctx, cacheDir, "checkout", "main"); err != nil {
			return fmt.Errorf("checkout main: %w", err)
		}
		if _, err := runGit(ctx, cacheDir, "reset", "--hard", "origin/main"); err != nil {
			return fmt.Errorf("reset hard origin/main: %w", err)
		}
	} else {
		// Repository is empty or main does not exist yet.
		// Checkout or create branch 'main'
		_, _ = runGit(ctx, cacheDir, "checkout", "-b", "main")
	}

	// Build map of relative paths existing in sourceDir (excluding .git)
	sourceFiles := make(map[string]bool)
	if _, err := os.Stat(sourceDir); err == nil {
		err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(sourceDir, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !info.IsDir() {
				sourceFiles[rel] = true
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("walk sourceDir: %w", err)
		}
	}

	// Remove files from cacheDir that were removed from sourceDir (excluding .git)
	err = filepath.Walk(cacheDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(cacheDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() && !sourceFiles[rel] {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove stale file %s: %w", path, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cleanup cacheDir: %w", err)
	}

	// Copy files from sourceDir to cacheDir
	for rel := range sourceFiles {
		srcPath := filepath.Join(sourceDir, rel)
		dstPath := filepath.Join(cacheDir, rel)

		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", srcPath, err)
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dstPath), err)
		}
		if err := os.WriteFile(dstPath, data, 0644); err != nil {
			return fmt.Errorf("write %s: %w", dstPath, err)
		}
	}

	// git add -A .
	if _, err := runGit(ctx, cacheDir, "add", "-A", "."); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// Check git status --porcelain
	status, err := runGit(ctx, cacheDir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		// Nothing changed
		return nil
	}

	// git commit -m <commitMessage>
	if _, err := runGit(ctx, cacheDir, "commit", "-m", commitMessage); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	// git push origin main
	if _, err := runGit(ctx, cacheDir, "push", "origin", "main"); err != nil {
		return fmt.Errorf("git push: %w", err)
	}

	return nil
}
