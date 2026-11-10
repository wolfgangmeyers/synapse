//go:build linux

package managed

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestManagerCloseDeadlineWithBlockedAdmittedBackgroundLaunch(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	m.ensureBackgroundBash()
	jobs := m.backgroundBash
	cap := &backgroundBashOwnerCapability{}
	owner := embeddedOwner{binding: store.EmbeddedBinding{AgentID: "blocked-close"}, identity: "blocked-close", backgroundBash: cap}
	m.mu.Lock()
	m.embeddedOwners = map[string]embeddedOwner{"blocked-close": owner}
	m.mu.Unlock()

	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	// Block an admitted launch without creating an unidentified group: that
	// separate uncertainty must not be mistaken for a retryable close barrier.
	jobs.mu.Lock()
	jobs.identify = func(pid int) (string, error) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		return processStartMarker(pid)
	}
	jobs.mu.Unlock()
	launched := make(chan error, 1)
	go func() {
		_, err := cap.launch(func() (BackgroundBashJob, error) {
			return jobs.Launch(backgroundBashOwnerKey(owner), BackgroundBashLaunch{Command: "sleep 30"})
		})
		launched <- err
	}()
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("identification not entered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- m.Close(ctx) }()
	select {
	case err := <-closed:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("Manager.Close ignored deadline")
	}
	_, err = jobs.Launch("new-owner", BackgroundBashLaunch{Command: "exit 0"})
	require.ErrorContains(t, err, "manager closed")
	invoked := false
	_, err = cap.launch(func() (BackgroundBashJob, error) { invoked = true; return BackgroundBashJob{}, nil })
	require.ErrorIs(t, err, errBackgroundBashOwnerRetired)
	require.False(t, invoked)
	select {
	case err := <-launched:
		t.Fatalf("admitted launch returned before identification release: %v", err)
	default:
	}
	unblock()
	select {
	case err := <-launched:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("admitted launch did not finish")
	}
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	require.NoError(t, m.Close(retryCtx))
}
