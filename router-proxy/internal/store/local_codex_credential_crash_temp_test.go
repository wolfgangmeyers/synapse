package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/stretchr/testify/require"
)

const fakeCrashTemp = "credential.tmp-0123456789abcdef0123456789abcdef"

func committedFakeCredential(t *testing.T) (string, store.CodexCredential) {
	t.Helper()
	path := filepath.Join(testfixture.PrivateDir(t), "router", "credential")
	s, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	original := store.CodexCredential{AccessToken: "fake-committed", RefreshToken: "fake-refresh"}
	require.NoError(t, s.Save(context.Background(), original))
	return path, original
}

func TestEmbeddedCodexCrashTempOpenRemovesLeftover(t *testing.T) {
	path, original := committedFakeCredential(t)
	temp := filepath.Join(filepath.Dir(path), fakeCrashTemp)
	require.NoError(t, os.WriteFile(temp, []byte("fake-abandoned"), 0600))
	assertOwnerOnlyRegularFile(t, temp)
	_, err := os.Lstat(temp)
	require.NoError(t, err, "fixture must exist before Open")
	reopened, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	_, err = os.Lstat(temp)
	require.ErrorIs(t, err, os.ErrNotExist, "Open must remove the crash leftover")
	got, err := reopened.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, original, got)
}

func TestEmbeddedCodexCrashTempOpenLockWaitIsBounded(t *testing.T) {
	path, _ := committedFakeCredential(t)
	first, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- first.Update(context.Background(), func(c store.CodexCredential) (store.CodexCredential, error) {
			close(entered)
			<-release
			return c, nil
		})
	}()
	openDone := make(chan error, 1)
	openStarted := false
	openReceived := false
	defer func() {
		close(release)
		select {
		case err := <-updateDone:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("held Update did not finish after release")
		}
		if openStarted && !openReceived {
			select {
			case <-openDone:
			case <-time.After(5 * time.Second):
				t.Error("Open did not finish after lock release")
			}
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Update did not acquire the lock")
	}
	temp := filepath.Join(filepath.Dir(path), fakeCrashTemp)
	require.NoError(t, os.WriteFile(temp, []byte("fake-abandoned"), 0600))
	go func() {
		_, err := store.OpenLocalCodexCredentialStore(path)
		openDone <- err
	}()
	openStarted = true
	select {
	case err := <-openDone:
		openReceived = true
		require.True(t, errors.Is(err, context.DeadlineExceeded), "contended Open must time out, got %v", err)
	case <-time.After(7 * time.Second):
		t.Error("contended Open did not time out within seven seconds")
	}
	_, err = os.Lstat(temp)
	require.NoError(t, err, "Open must not clean crash leftovers while Update holds the lock")
}

func TestEmbeddedCodexCrashTempLoadRemovesLeftover(t *testing.T) {
	path, original := committedFakeCredential(t)
	temp := filepath.Join(filepath.Dir(path), fakeCrashTemp)
	require.NoError(t, os.WriteFile(temp, []byte("fake-abandoned"), 0600))
	s, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	// Recreate after Open: Load must independently clean under the lock.
	require.NoError(t, os.WriteFile(temp, []byte("fake-abandoned"), 0600))
	got, err := s.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, original, got)
	_, err = os.Lstat(temp)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEmbeddedCodexCrashTempUnsafeFailsClosed(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			path, original := committedFakeCredential(t)
			dir := filepath.Dir(path)
			temp := filepath.Join(dir, fakeCrashTemp)
			target := filepath.Join(dir, "fake-target")
			require.NoError(t, os.WriteFile(target, []byte("fake-target"), 0600))
			switch kind {
			case "symlink":
				require.NoError(t, os.Symlink(target, temp))
			case "hardlink":
				require.NoError(t, os.Link(target, temp))
			case "oversize":
				require.NoError(t, os.WriteFile(temp, []byte(strings.Repeat("x", (64<<10)+1)), 0600))
			}
			_, err := store.OpenLocalCodexCredentialStore(path)
			require.Error(t, err)
			_, err = os.Lstat(temp)
			require.NoError(t, err, "unsafe temp must remain for inspection")
			data, err := os.ReadFile(target)
			require.NoError(t, err)
			require.Equal(t, "fake-target", string(data))
			// Remove only the test fixture, then verify the committed record survived.
			require.NoError(t, os.Remove(temp))
			restored, err := store.OpenLocalCodexCredentialStore(path)
			require.NoError(t, err)
			got, err := restored.Load(context.Background())
			require.NoError(t, err)
			require.Equal(t, original, got)
		})
	}
}

func TestEmbeddedCodexCrashTempPreviouslyOpenedStoreFailsClosed(t *testing.T) {
	path, original := committedFakeCredential(t)
	s, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	dir := filepath.Dir(path)
	target := filepath.Join(dir, "fake-target")
	require.NoError(t, os.WriteFile(target, []byte("fake-target"), 0600))
	temp := filepath.Join(dir, fakeCrashTemp)
	require.NoError(t, os.Symlink(target, temp))
	_, err = s.Load(context.Background())
	require.Error(t, err)
	err = s.Save(context.Background(), store.CodexCredential{AccessToken: "fake-new", RefreshToken: "fake-new"})
	require.Error(t, err)
	called := false
	err = s.Update(context.Background(), func(c store.CodexCredential) (store.CodexCredential, error) {
		called = true
		return c, nil
	})
	require.Error(t, err)
	require.False(t, called, "unsafe leftover must block callback")
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "fake-target", string(data))
	_, err = os.Lstat(temp)
	require.NoError(t, err)
	require.NoError(t, os.Remove(temp))
	got, err := s.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, original, got)
}

func TestEmbeddedCodexCrashTempBoundedCount(t *testing.T) {
	path, original := committedFakeCredential(t)
	dir := filepath.Dir(path)
	for i := 0; i < 33; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("credential.tmp-%032x", i)), []byte("fake"), 0600))
	}
	_, err := store.OpenLocalCodexCredentialStore(path)
	require.Error(t, err)
	first := filepath.Join(dir, fmt.Sprintf("credential.tmp-%032x", 0))
	_, err = os.Lstat(first)
	require.NoError(t, err, "limit must not partially clean the directory")
	for i := 0; i < 33; i++ {
		require.NoError(t, os.Remove(filepath.Join(dir, fmt.Sprintf("credential.tmp-%032x", i))))
	}
	s, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	got, err := s.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, original, got)
}

func TestEmbeddedCodexCrashTempBoundedDirectoryScan(t *testing.T) {
	path, _ := committedFakeCredential(t)
	dir := filepath.Dir(path)
	for i := 0; i < 4096; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("unrelated-%04d", i)), nil, 0600))
	}
	temp := filepath.Join(dir, fakeCrashTemp)
	require.NoError(t, os.WriteFile(temp, []byte("fake"), 0600))
	_, err := store.OpenLocalCodexCredentialStore(path)
	require.Error(t, err)
	_, err = os.Lstat(temp)
	require.NoError(t, err, "scan limit must not partially clean")
}
