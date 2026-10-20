package gitsync_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/gitsync"
	"github.com/stretchr/testify/require"
)

func runCmd(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "cmd %s %v failed: %s", name, args, string(out))
	return strings.TrimSpace(string(out))
}

func setupBareRemote(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	remoteDir := filepath.Join(tempDir, "remote.git")
	runCmd(t, "", "git", "init", "--bare", "-b", "main", remoteDir)
	return remoteDir
}

func TestGitSyncEngine_PushAndPull(t *testing.T) {
	ctx := context.Background()
	remoteURL := setupBareRemote(t)
	cacheBase := t.TempDir()

	engine, err := gitsync.New(gitsync.WithBaseDir(cacheBase))
	require.NoError(t, err)

	filename := "test-file.json"
	content1 := []byte(`{"version":1,"data":"hello world"}`)

	// Push to empty bare repo
	err = engine.Push(ctx, remoteURL, filename, content1, "Initial commit")
	require.NoError(t, err)

	// Pull from another engine instance using a new cache dir
	clientCache := t.TempDir()
	clientEngine, err := gitsync.New(gitsync.WithBaseDir(clientCache))
	require.NoError(t, err)

	gotBytes, exists, err := clientEngine.Pull(ctx, remoteURL, filename)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, content1, gotBytes)

	// Check non-existent file
	gotBytes2, exists2, err := clientEngine.Pull(ctx, remoteURL, "nonexistent.json")
	require.NoError(t, err)
	require.False(t, exists2)
	require.Nil(t, gotBytes2)

	// Push identical content (no-op commit check)
	err = clientEngine.Push(ctx, remoteURL, filename, content1, "No-op commit")
	require.NoError(t, err)

	// Push updated content from first engine
	content2 := []byte(`{"version":2,"data":"updated"}`)
	err = engine.Push(ctx, remoteURL, filename, content2, "Update commit")
	require.NoError(t, err)

	// Pull updated content from clientEngine
	gotBytes3, exists3, err := clientEngine.Pull(ctx, remoteURL, filename)
	require.NoError(t, err)
	require.True(t, exists3)
	require.Equal(t, content2, gotBytes3)
}

func TestGitSyncEngine_PullEmptyRepo(t *testing.T) {
	ctx := context.Background()
	remoteURL := setupBareRemote(t)
	cacheBase := t.TempDir()

	engine, err := gitsync.New(gitsync.WithBaseDir(cacheBase))
	require.NoError(t, err)

	// Pulling a file from an empty repo should return exists: false without error
	gotBytes, exists, err := engine.Pull(ctx, remoteURL, "any.json")
	require.NoError(t, err)
	require.False(t, exists)
	require.Nil(t, gotBytes)
}

func TestValidateRepoURL(t *testing.T) {
	// Valid URLs
	require.NoError(t, gitsync.ValidateRepoURL("https://github.com/org/repo.git"))
	require.NoError(t, gitsync.ValidateRepoURL("http://gitlab.local/user/repo"))
	require.NoError(t, gitsync.ValidateRepoURL("git@github.com:org/repo.git"))
	require.NoError(t, gitsync.ValidateRepoURL("ssh://git@github.com/org/repo.git"))

	// Invalid URLs
	require.Error(t, gitsync.ValidateRepoURL(""))
	require.Error(t, gitsync.ValidateRepoURL("   "))
	require.Error(t, gitsync.ValidateRepoURL("--upload-pack=touch /tmp/pwn"))
	require.Error(t, gitsync.ValidateRepoURL("-oProxyCommand=touch /tmp/pwn"))
	require.Error(t, gitsync.ValidateRepoURL("ext::sh -c touch% /tmp/pwn"))
	require.Error(t, gitsync.ValidateRepoURL("git@github.com:-flag"))
	require.Error(t, gitsync.ValidateRepoURL("ftp://github.com/repo.git"))
	require.Error(t, gitsync.ValidateRepoURL("file:///etc/passwd"))
}
