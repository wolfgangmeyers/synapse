package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type delayedCodexSnapshotHelper struct {
	fakeProviderAuthSnapshotHelper
	entered, release chan struct{}
	calls            atomic.Int64
}

func (h *delayedCodexSnapshotHelper) Snapshot(ctx context.Context) ([]store.ManagedProviderStatus, []store.ManagedProviderModel, error) {
	if h.calls.Add(1) == 1 {
		close(h.entered)
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	return h.fakeProviderAuthSnapshotHelper.Snapshot(ctx)
}

func browserAuthStore(t *testing.T) (*store.Store, *store.LocalCodexCredentialStore, string) {
	t.Helper()
	base, err := os.MkdirTemp("/srv/disk-tmp", "codex-helper-fence-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(base)) })
	durable, err := store.Open(context.Background(), filepath.Join(base, "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	path := filepath.Join(base, "codex.json")
	credentials, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	require.NoError(t, credentials.Save(context.Background(), store.CodexCredential{AccessToken: "fake-owned-current", RefreshToken: "fake-owned-refresh", AccountID: "fake-account", ExpiresAt: time.Now().Add(time.Hour)}))
	return durable, credentials, path
}
func cachedOrdinarySnapshot(run *fakeProviderAuthRun) fakeProviderAuthSnapshotHelper {
	return fakeProviderAuthSnapshotHelper{fakeProviderAuthHelper: fakeProviderAuthHelper{run: run}, providers: []store.ManagedProviderStatus{
		{Provider: "openai-codex", Label: "ChatGPT", Authenticated: true, Status: "authenticated"},
		{Provider: "google", Label: "Google", Authenticated: true, Status: "authenticated"},
		{Provider: "cursor", Label: "Cursor", Authenticated: false, Status: "signed_out"},
	}, models: []store.ManagedProviderModel{{Provider: "openai-codex", ID: "stale-helper-model", Name: "Stale", Available: true}, {Provider: "google", ID: "google-fixture", Name: "Google fixture", Available: true}}}
}

func TestCodexBrowserSnapshotCommitSerializesLogoutAndNewOwnedRevision(t *testing.T) {
	ctx := context.Background()
	durable, credentials, path := browserAuthStore(t)
	peer, err := store.OpenLocalCodexCredentialStore(path)
	require.NoError(t, err)
	var rejectProjection atomic.Bool
	credentials.SetProjection(func(context.Context, *store.CodexCredential) error {
		if rejectProjection.Load() {
			return errors.New("offline private consumer failed")
		}
		return nil
	})
	helper := &delayedCodexSnapshotHelper{fakeProviderAuthSnapshotHelper: cachedOrdinarySnapshot(newFakeProviderAuthRun()), entered: make(chan struct{}), release: make(chan struct{})}
	coordinator, err := NewProviderAuthCoordinator(durable, nil, CodexBrowserAuth{Ordinary: helper, Credentials: credentials})
	require.NoError(t, err)
	t.Cleanup(coordinator.Close)
	result := make(chan error, 1)
	go func() { _, e := coordinator.Snapshot(ctx); result <- e }()
	select {
	case <-helper.entered:
	case <-time.After(time.Second):
		t.Fatal("helper snapshot did not enter")
	}
	// The independent store instance cannot logout while an earlier status
	// read owns the file lock through its SQLite commit.
	blocked, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	require.ErrorIs(t, peer.Delete(blocked), context.DeadlineExceeded)
	cancel()
	close(helper.release)
	require.NoError(t, <-result)
	assertProjectionFailure := func() {
		t.Helper()
		before, e := durable.ManagedProviderSnapshot(ctx)
		require.NoError(t, e)
		calls := helper.calls.Load()
		rejectProjection.Store(true)
		failed, e := coordinator.Snapshot(ctx)
		require.Error(t, e)
		require.Empty(t, failed.Providers)
		require.Empty(t, failed.Models)
		after, e := durable.ManagedProviderSnapshot(ctx)
		require.NoError(t, e)
		require.Equal(t, before, after)
		require.Equal(t, calls, helper.calls.Load(), "projection failure must stop before helper discovery or status commit")
		rejectProjection.Store(false)
	}
	require.NoError(t, peer.Delete(ctx))
	assertProjectionFailure()
	current, err := coordinator.Snapshot(ctx)
	require.NoError(t, err)
	for _, p := range current.Providers {
		if p.Provider == "openai-codex" {
			require.False(t, p.Authenticated)
			require.Equal(t, "signed_out", p.Status)
		}
	}
	for _, m := range current.Models {
		require.NotEqual(t, "openai-codex", m.Provider, "stale helper model must not survive logout")
	}
	require.NoError(t, peer.Save(ctx, store.CodexCredential{AccessToken: "fake-new-owned-revision", RefreshToken: "fake-new-owned-refresh", AccountID: "fake-account", ExpiresAt: time.Now().Add(time.Hour)}))
	assertProjectionFailure()
	current, err = coordinator.Snapshot(ctx)
	require.NoError(t, err)
	var codex, google bool
	for _, p := range current.Providers {
		if p.Provider == "openai-codex" {
			codex = p.Authenticated
		}
		if p.Provider == "google" {
			google = p.Authenticated
		}
	}
	require.True(t, codex)
	require.True(t, google)
	for _, m := range current.Models {
		require.NotEqual(t, "stale-helper-model", m.ID)
	}
	saved, err := durable.ManagedProviderSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, current, saved)
	credential, err := credentials.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, "fake-new-owned-revision", credential.AccessToken)
}

func TestCodexBrowserOtherProviderTerminalCannotOverwriteOwnedCodexStatus(t *testing.T) {
	ctx := context.Background()
	durable, credentials, _ := browserAuthStore(t)
	run := newFakeProviderAuthRun()
	helper := cachedOrdinarySnapshot(run)
	coordinator, err := NewProviderAuthCoordinator(durable, nil, CodexBrowserAuth{Ordinary: helper, Credentials: credentials})
	require.NoError(t, err)
	t.Cleanup(coordinator.Close)
	require.NoError(t, coordinator.Begin(ctx, "google-operation", "google", "login"))
	stale := providerTerminal("google-operation", "google")
	for i := range stale.Providers {
		if stale.Providers[i].Provider == "openai-codex" {
			stale.Providers[i].Authenticated = false
			stale.Providers[i].Status = "signed_out"
		}
	}
	stale.Models = nil
	run.events <- stale
	require.Eventually(t, func() bool {
		snapshot, e := durable.ManagedProviderSnapshot(ctx)
		if e != nil {
			return false
		}
		var codex, google bool
		for _, p := range snapshot.Providers {
			if p.Provider == "openai-codex" {
				codex = p.Authenticated
			}
			if p.Provider == "google" {
				google = p.Authenticated
			}
		}
		return codex && google && len(snapshot.Models) > 1
	}, time.Second, 10*time.Millisecond)
	_, err = credentials.Load(ctx)
	require.NoError(t, err)
}
