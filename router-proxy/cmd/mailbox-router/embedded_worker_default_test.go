package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedWorkerDefaultFlagOptIn(t *testing.T) {
	ctx := context.Background()
	parent := "/srv/disk-tmp"
	parentInfo, err := os.Lstat(parent)
	require.NoError(t, err)
	require.True(t, parentInfo.IsDir() && parentInfo.Mode()&os.ModeSymlink == 0 && parentInfo.Mode()&os.ModeSticky != 0, "fixture parent must be a sticky directory, not a symlink")
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	require.Equal(t, uint32(0), parentStat.Uid, "fixture parent must be root-owned")
	base, err := os.MkdirTemp(parent, "embedded-worker-default-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(base)) })
	require.Equal(t, parent, filepath.Dir(base), "fixture must be directly under the sticky parent")
	require.NoError(t, os.Chmod(base, 0o700))
	baseInfo, err := os.Lstat(base)
	require.NoError(t, err)
	require.True(t, baseInfo.IsDir() && baseInfo.Mode()&os.ModeSymlink == 0, "fixture base must be a directory, not a symlink")
	require.Equal(t, os.FileMode(0o700), baseInfo.Mode().Perm(), "fixture base must be private")
	baseStat, ok := baseInfo.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	require.Equal(t, uint32(os.Geteuid()), baseStat.Uid, "fixture base must be owned by the test user")
	args := []string{"--database", filepath.Join(base, "router.db")}
	off, err := parseConfig(args)
	require.NoError(t, err)
	require.False(t, off.embeddedWorkerDefault, "bare launches retain the RPC default")
	require.False(t, embeddedWorkerDefaultEnabled(off))
	path := filepath.Join(base, "codex.json")
	rootArgs := append(append([]string{}, args...), "--codex-credential-path", path, "--embedded-root-bootstrap", "--embedded-root-request-id", "a0666666-6666-4666-8666-666666666666", "--embedded-root-name", "root", "--embedded-root-cwd", base, "--embedded-root-model-provider", "openai-codex", "--embedded-root-model-id", "gpt-5.6-terra", "--embedded-root-initial-instruction", "root instruction")
	bootstrap, err := parseConfig(rootArgs)
	require.NoError(t, err)
	require.False(t, embeddedWorkerDefaultEnabled(bootstrap), "root bootstrap alone does not alter HTTP defaults")
	for _, bad := range [][]string{
		append(append([]string{}, args...), "--embedded-worker-default"),
	} {
		_, err := parseConfig(bad)
		require.Error(t, err)
	}
	opted, err := parseConfig(append(append([]string{}, args...), "--embedded-worker-default", "--codex-credential-path", path))
	require.NoError(t, err)
	require.True(t, opted.embeddedWorkerDefault)
	require.True(t, embeddedWorkerDefaultEnabled(opted))
	managerOptions := managed.ManagerOptions{}
	require.NoError(t, configureCodexCredentials(ctx, opted, &managerOptions), "protected absence permits browser login without ambient credential fallback")
	require.NotNil(t, managerOptions.CodexCredentialStore)
	require.NotNil(t, managerOptions.CodexRefresh)
	_, err = managerOptions.CodexCredentialStore.Load(ctx)
	require.ErrorIs(t, err, os.ErrNotExist, "startup must leave the protected store signed out")
	fixture, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	require.NoError(t, fixture.Save(ctx, store.CodexCredential{AccessToken: "fake-access", RefreshToken: "fake-refresh", AccountID: "fake-account", ExpiresAt: time.Now().Add(time.Hour)}))
	require.NoError(t, configureCodexCredentials(ctx, opted, &managerOptions))
	require.NoError(t, os.Chmod(path, 0o644))
	require.Error(t, configureCodexCredentials(ctx, opted, &managed.ManagerOptions{}), "public credential blocks startup")
}
