package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedCodexLocalCredentialRejectsUnsafeLockAndAcceptsOrdinaryParent(t *testing.T) {
	root := testfixture.PrivateDir(t)
	private := filepath.Join(root, "private")
	require.NoError(t, os.Mkdir(private, 0700))
	target := filepath.Join(private, "target")
	require.NoError(t, os.WriteFile(target, []byte("fake"), 0600))
	assertPrivateTarget(t, target)
	for _, kind := range []string{"symlink", "hardlink", "public"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(private, kind+".credential")
			lock := path + ".lock"
			switch kind {
			case "symlink":
				require.NoError(t, os.Symlink(target, lock))
				assertPrivateTarget(t, target)
			case "hardlink":
				require.NoError(t, os.Link(target, lock))
				assertPrivateTarget(t, target)
			case "public":
				require.NoError(t, os.WriteFile(lock, nil, 0644))
				require.NoError(t, os.Chmod(lock, 0644))
				assertMode(t, lock, 0644)
			}
			_, err := store.OpenLocalCodexCredentialStore(path)
			require.Error(t, err)
		})
	}
	public := filepath.Join(root, "public")
	require.NoError(t, os.Mkdir(public, 0755))
	require.NoError(t, os.Chmod(public, 0755))
	assertMode(t, public, 0755)
	_, err := store.OpenLocalCodexCredentialStore(filepath.Join(public, "credential"))
	require.NoError(t, err)
}

func TestEmbeddedCodexLocalCredentialCancelledOrFailedUpdatePreservesRecord(t *testing.T) {
	path := filepath.Join(testfixture.PrivateDir(t), "router", "credential")
	s, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	initial := store.CodexCredential{AccessToken: "fake-original", RefreshToken: "fake-refresh"}
	require.NoError(t, s.Save(context.Background(), initial))
	err = s.Update(context.Background(), func(c store.CodexCredential) (store.CodexCredential, error) {
		c.AccessToken = "fake-replacement"
		return c, errors.New("refresh failed")
	})
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	err = s.Update(ctx, func(c store.CodexCredential) (store.CodexCredential, error) {
		cancel()
		c.AccessToken = "fake-cancelled"
		return c, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	got, err := s.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, initial, got)
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 2) // credential and stable lock, no temp or previous generation
}

func TestEmbeddedCodexLocalCredentialLockWaitHonorsDeadline(t *testing.T) {
	path := filepath.Join(testfixture.PrivateDir(t), "router", "credential")
	first, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	second, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	require.NoError(t, first.Save(context.Background(), store.CodexCredential{AccessToken: "fake", RefreshToken: "fake"}))
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.Update(context.Background(), func(c store.CodexCredential) (store.CodexCredential, error) { close(entered); <-release; return c, nil })
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not start")
	}
	defer func() {
		close(release)
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("callback did not finish")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = second.Load(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
