//go:build linux

package managed

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestBackgroundBashShutdownReportsCleanupFailure(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	m.ensureBackgroundBash()
	m.mu.Lock()
	jobs := m.backgroundBash
	m.mu.Unlock()
	job, err := jobs.Launch("shutdown-owner", BackgroundBashLaunch{Command: "sleep 30"})
	require.NoError(t, err)
	t.Cleanup(func() {
		jobs.mu.Lock()
		jobs.signal = nil
		jobs.mu.Unlock()
		_, _ = jobs.Kill("shutdown-owner", job.ID)
	})
	jobs.mu.Lock()
	jobs.signal = func(ProcessIdentity, syscall.Signal) error { return errors.New("injected background cleanup failure") }
	jobs.mu.Unlock()
	err = m.Close(ctx)
	require.ErrorContains(t, err, "injected background cleanup failure")
}

func TestBackgroundBashStopOwnerFencesConcurrentLaunch(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	cap := &backgroundBashOwnerCapability{}
	job, err := cap.launch(func() (BackgroundBashJob, error) {
		return jobs.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
	})
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	jobs.mu.Lock()
	jobs.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		close(entered)
		<-release
		return DefaultProcessGroupOps().SignalVerifiedProcessGroup(id, sig)
	}
	jobs.mu.Unlock()
	stopped := make(chan error, 1)
	go func() { stopped <- cap.revoke(func() error { return jobs.StopOwner("owner") }) }()
	<-entered
	launched := make(chan error, 1)
	go func() {
		_, err := cap.launch(func() (BackgroundBashJob, error) {
			return jobs.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
		})
		launched <- err
	}()
	close(release)
	require.NoError(t, <-stopped)
	require.ErrorContains(t, <-launched, "owner retired")
	snapshot, err := jobs.Get("owner", job.ID)
	require.NoError(t, err)
	require.Equal(t, "killed", snapshot.Status)
	jobs.mu.Lock()
	jobs.signal = nil
	jobs.mu.Unlock()
	_, err = jobs.Launch("replacement", BackgroundBashLaunch{Command: "true"})
	require.NoError(t, err)
}

func TestBackgroundBashClaudeTerminationStopsAndFencesOwner(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "claude-bg-terminate", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	owner := embeddedOwner{binding: binding, identity: "test-owner", backgroundBash: &backgroundBashOwnerCapability{}}
	m.mu.Lock()
	if m.embeddedOwners == nil {
		m.embeddedOwners = make(map[string]embeddedOwner)
	}
	m.embeddedOwners[binding.AgentID] = owner
	m.mu.Unlock()
	m.ensureBackgroundBash()
	jobs := m.backgroundBash
	key := backgroundBashOwnerKey(owner)
	job, err := jobs.Launch(key, BackgroundBashLaunch{Command: "sleep 30"})
	require.NoError(t, err)
	req := TerminateRequest{AgentID: binding.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + binding.AgentID, RequestID: "123e4567-e89b-42d3-a456-426614174291", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	_, err = m.Terminate(ctx, req)
	require.NoError(t, err)
	result, err := jobs.Get(key, job.ID)
	require.NoError(t, err)
	require.Equal(t, "killed", result.Status)
	_, err = owner.backgroundBash.launch(func() (BackgroundBashJob, error) { return jobs.Launch(key, BackgroundBashLaunch{Command: "true"}) })
	require.ErrorContains(t, err, "owner retired")
}

func TestBackgroundBashReplacementRetiresOnlyPreviousOwner(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	previous := embeddedOwner{binding: store.EmbeddedBinding{AgentID: "agent", SessionID: "old"}, identity: "old", backgroundBash: &backgroundBashOwnerCapability{}}
	replacement := embeddedOwner{binding: store.EmbeddedBinding{AgentID: "agent", SessionID: "new"}, identity: "new", backgroundBash: &backgroundBashOwnerCapability{}}
	m.ensureBackgroundBash()
	jobs := m.backgroundBash
	oldKey, newKey := backgroundBashOwnerKey(previous), backgroundBashOwnerKey(replacement)
	job, err := jobs.Launch(oldKey, BackgroundBashLaunch{Command: "sleep 30"})
	require.NoError(t, err)
	require.NoError(t, m.retireBackgroundBashOwner(previous))
	snapshot, err := jobs.Get(oldKey, job.ID)
	require.NoError(t, err)
	require.Equal(t, "killed", snapshot.Status)
	_, err = previous.backgroundBash.launch(func() (BackgroundBashJob, error) { return jobs.Launch(oldKey, BackgroundBashLaunch{Command: "true"}) })
	require.Error(t, err)
	_, err = replacement.backgroundBash.launch(func() (BackgroundBashJob, error) { return jobs.Launch(newKey, BackgroundBashLaunch{Command: "true"}) })
	require.NoError(t, err)
}
