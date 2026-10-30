package gitsync_test

import (
	"context"
	"os"
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

func TestGitSyncEngine_PushAndPullDirectory(t *testing.T) {
	ctx := context.Background()
	remoteURL := setupBareRemote(t)
	cacheBase := t.TempDir()

	engine, err := gitsync.New(gitsync.WithBaseDir(cacheBase))
	require.NoError(t, err)

	sourceDir := t.TempDir()
	file1 := filepath.Join(sourceDir, "INDEX.md")
	file2 := filepath.Join(sourceDir, "topic1.md")
	subFile := filepath.Join(sourceDir, "sub", "topic2.md")

	require.NoError(t, os.WriteFile(file1, []byte("# Index Content"), 0644))
	require.NoError(t, os.WriteFile(file2, []byte("# Topic 1 Content"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Dir(subFile), 0755))
	require.NoError(t, os.WriteFile(subFile, []byte("# Topic 2 Content"), 0644))

	// Push directory to empty bare repo
	err = engine.PushDirectory(ctx, remoteURL, sourceDir, "Initial kb sync")
	require.NoError(t, err)

	// Pull into a fresh target directory using clientEngine
	clientCache := t.TempDir()
	clientEngine, err := gitsync.New(gitsync.WithBaseDir(clientCache))
	require.NoError(t, err)

	targetDir := t.TempDir()
	err = clientEngine.PullDirectory(ctx, remoteURL, targetDir)
	require.NoError(t, err)

	// Verify targetDir contents
	t1, err := os.ReadFile(filepath.Join(targetDir, "INDEX.md"))
	require.NoError(t, err)
	require.Equal(t, "# Index Content", string(t1))

	t2, err := os.ReadFile(filepath.Join(targetDir, "topic1.md"))
	require.NoError(t, err)
	require.Equal(t, "# Topic 1 Content", string(t2))

	t3, err := os.ReadFile(filepath.Join(targetDir, "sub", "topic2.md"))
	require.NoError(t, err)
	require.Equal(t, "# Topic 2 Content", string(t3))

	// Modify sourceDir: delete topic1.md, update topic2.md, add topic3.md
	require.NoError(t, os.Remove(file2))
	require.NoError(t, os.WriteFile(subFile, []byte("# Topic 2 Updated"), 0644))
	file3 := filepath.Join(sourceDir, "topic3.md")
	require.NoError(t, os.WriteFile(file3, []byte("# Topic 3 Content"), 0644))

	// Push directory changes
	err = engine.PushDirectory(ctx, remoteURL, sourceDir, "Update kb files")
	require.NoError(t, err)

	// Pull into targetDir again with clientEngine
	err = clientEngine.PullDirectory(ctx, remoteURL, targetDir)
	require.NoError(t, err)

	// Verify topic1.md is now gone from targetDir
	_, err = os.Stat(filepath.Join(targetDir, "topic1.md"))
	require.True(t, os.IsNotExist(err))

	// Verify topic2 updated
	t3Updated, err := os.ReadFile(filepath.Join(targetDir, "sub", "topic2.md"))
	require.NoError(t, err)
	require.Equal(t, "# Topic 2 Updated", string(t3Updated))

	// Verify topic3 present
	t4, err := os.ReadFile(filepath.Join(targetDir, "topic3.md"))
	require.NoError(t, err)
	require.Equal(t, "# Topic 3 Content", string(t4))

	// Push directory without changes: should return nil (no-op)
	err = engine.PushDirectory(ctx, remoteURL, sourceDir, "No-op kb sync")
	require.NoError(t, err)
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
