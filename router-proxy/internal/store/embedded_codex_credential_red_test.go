package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/stretchr/testify/require"
)

// RED-only: the router owns a local, unencrypted credential file. All values
// here are fake; no agent session or provider is involved.
func TestEmbeddedCodexLocalCredentialRoundTripAcrossRestart(t *testing.T) {
	ctx := context.Background()
	credentialPath := filepath.Join(testfixture.PrivateDir(t), "router", "codex.credential")
	original := store.CodexCredential{
		AccessToken:  "fake-access-g4",
		RefreshToken: "fake-refresh-g4",
		ExpiresAt:    time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	owner, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	require.NoError(t, owner.Save(ctx, original))
	assertOwnerOnlyRegularFile(t, credentialPath)
	assertOwnerOnlyDirectory(t, filepath.Dir(credentialPath))

	// A new owner reads the committed record without an agent-specific copy.
	reopened, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	got, err := reopened.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, original, got)

	rotated := original
	rotated.AccessToken = "fake-rotated-access-g4"
	rotated.RefreshToken = "fake-rotated-refresh-g4"
	require.NoError(t, owner.Save(ctx, rotated))
	got, err = reopened.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, rotated, got)
	assertOwnerOnlyRegularFile(t, credentialPath)
}

func TestEmbeddedCodexLocalCredentialRejectsUnsafePaths(t *testing.T) {
	root := testfixture.PrivateDir(t)
	ownerDir := filepath.Join(root, "owner")
	require.NoError(t, os.Mkdir(ownerDir, 0o700))
	target := filepath.Join(root, "fake-target")
	require.NoError(t, os.WriteFile(target, []byte("fake-credential-g4"), 0o600))
	assertPrivateTarget(t, target)

	t.Run("credential symlink", func(t *testing.T) {
		path := filepath.Join(ownerDir, "symlink.credential")
		require.NoError(t, os.Symlink(target, path))
		assertPrivateTarget(t, target)
		_, err := store.OpenLocalCodexCredentialStore(path)
		require.Error(t, err)
	})
	t.Run("parent symlink", func(t *testing.T) {
		alias := filepath.Join(root, "alias")
		require.NoError(t, os.Symlink(ownerDir, alias))
		assertOwnerOnlyDirectory(t, ownerDir)
		_, err := store.OpenLocalCodexCredentialStore(filepath.Join(alias, "codex.credential"))
		require.Error(t, err)
	})
	t.Run("hardlink", func(t *testing.T) {
		path := filepath.Join(ownerDir, "hardlink.credential")
		require.NoError(t, os.Link(target, path))
		assertPrivateTarget(t, target)
		_, err := store.OpenLocalCodexCredentialStore(path)
		require.Error(t, err)
	})
	t.Run("world readable file", func(t *testing.T) {
		path := filepath.Join(ownerDir, "public.credential")
		require.NoError(t, os.WriteFile(path, []byte("fake-credential-g4"), 0o644))
		require.NoError(t, os.Chmod(path, 0o644))
		assertMode(t, path, 0o644)
		_, err := store.OpenLocalCodexCredentialStore(path)
		require.Error(t, err)
	})
	t.Run("world accessible directory", func(t *testing.T) {
		publicDir := filepath.Join(root, "public")
		require.NoError(t, os.Mkdir(publicDir, 0o755))
		require.NoError(t, os.Chmod(publicDir, 0o755))
		assertMode(t, publicDir, 0o755)
		_, err := store.OpenLocalCodexCredentialStore(filepath.Join(publicDir, "codex.credential"))
		require.Error(t, err)
	})
}

// Update must hold an adjacent, stable lock across read, callback and atomic
// replacement so separate router instances cannot lose a refresh rotation.
func TestEmbeddedCodexLocalCredentialSerializesRefreshAcrossInstances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(testfixture.PrivateDir(t), "router", "codex.credential")
	first, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	second, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	initial := store.CodexCredential{AccessToken: "fake-0", RefreshToken: "fake-refresh-0"}
	require.NoError(t, first.Save(ctx, initial))

	enteredFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	}()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.Update(ctx, func(current store.CodexCredential) (store.CodexCredential, error) {
			close(enteredFirst)
			<-releaseFirst
			current.AccessToken = "fake-1"
			current.RefreshToken = "fake-refresh-1"
			return current, nil
		})
	}()
	select {
	case <-enteredFirst:
	case err := <-firstDone:
		t.Fatalf("first refresh failed before callback: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first refresh never entered")
	}

	enteredSecond := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.Update(ctx, func(current store.CodexCredential) (store.CodexCredential, error) {
			close(enteredSecond)
			if current.AccessToken != "fake-1" {
				return current, fmt.Errorf("second refresh saw %q instead of fake-1", current.AccessToken)
			}
			current.AccessToken = "fake-2"
			current.RefreshToken = "fake-refresh-2"
			return current, nil
		})
	}()
	select {
	case <-enteredSecond:
		close(releaseFirst)
		t.Fatal("second refresh entered while first held the lock")
	case <-time.After(100 * time.Millisecond):
		close(releaseFirst)
	}
	select {
	case err := <-firstDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("first refresh did not finish")
	}
	select {
	case err := <-secondDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("second refresh did not finish")
	}
	got, err := first.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, "fake-2", got.AccessToken)
	require.Equal(t, "fake-refresh-2", got.RefreshToken)
	assertOwnerOnlyRegularFile(t, path)
	assertOwnerOnlyRegularFile(t, path+".lock")
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.Equal(t, mode, info.Mode().Perm(), "%s permissions", path)
}

func assertPrivateTarget(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular(), "%s must be a regular file", path)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func assertOwnerOnlyDirectory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.True(t, info.IsDir(), "%s must be a directory", path)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func assertOwnerOnlyRegularFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular(), "%s must be a regular file", path)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "%s must have link metadata", path)
	require.Equal(t, uint64(1), uint64(stat.Nlink))
}
