package managed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestBackgroundBashPublicJobsAreAllowlistedAndEmpty(t *testing.T) {
	jobs := backgroundBashPublicJobs([]BackgroundBashJob{{ID: "j1", Owner: "secret-owner", Command: "secret-command", Output: "secret-output", Status: "running"}})
	raw, err := json.Marshal(jobs)
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":"j1","status":"running"}]`, string(raw))
	raw, err = json.Marshal(backgroundBashPublicJobs(nil))
	require.NoError(t, err)
	require.JSONEq(t, `[]`, string(raw))
}

func TestBackgroundBashProjectionTwoManagersDoNotOverwriteEachOther(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("background bash launch is supported only on Linux")
	}
	first, db1, _, id1, _ := requiredToolsFixture(t, "root")
	second, db2, _, id2, _ := requiredToolsFixture(t, "root")
	require.Equal(t, id1, id2)
	ctx := context.Background()
	first.ensureBackgroundBash()
	second.ensureBackgroundBash()
	first.mu.Lock()
	owner1 := first.embeddedOwners[id1]
	first.mu.Unlock()
	second.mu.Lock()
	owner2 := second.embeddedOwners[id2]
	second.mu.Unlock()
	job1, err := first.backgroundBash.Launch(backgroundBashOwnerKey(owner1), BackgroundBashLaunch{Command: "sleep 30"})
	require.NoError(t, err)
	job2, err := second.backgroundBash.Launch(backgroundBashOwnerKey(owner2), BackgroundBashLaunch{Command: "sleep 30"})
	require.NoError(t, err)
	record1, err := db1.ManagedAgent(ctx, id1)
	require.NoError(t, err)
	record2, err := db2.ManagedAgent(ctx, id2)
	require.NoError(t, err)
	require.Equal(t, []map[string]string{{"id": job1.ID, "status": "running"}}, first.BackgroundBashJobs(record1))
	require.Equal(t, []map[string]string{{"id": job2.ID, "status": "running"}}, second.BackgroundBashJobs(record2))
	stale := *record1
	stale.PiSessionID = "replaced"
	require.Empty(t, first.BackgroundBashJobs(&stale))
	require.Empty(t, first.BackgroundBashJobs(nil))
}

// The live callbacks may outlive the owner lease. A stolen fence must reject
// the actual notification admission, not just a prior preflight check.
func TestBackgroundBashCompletionRejectsStaleOwner(t *testing.T) {
	m, db, _, id, _ := requiredToolsFixture(t, "worker")
	m.mu.Lock()
	owner := m.embeddedOwners[id]
	m.mu.Unlock()
	// Use the old token after installing a new owner; fixture mail loops may
	// independently reacquire ownership, so remove the active runner first.
	require.NoError(t, m.Close(context.Background()))
	staleManager := &Manager{store: db, clock: m.clock, embeddedOwners: map[string]embeddedOwner{id: owner}}
	_, err := db.AcquireEmbeddedOwner(context.Background(), owner.binding, "replacement-owner", time.Minute)
	require.NoError(t, err)
	staleManager.backgroundBashCompleted(BackgroundBashJob{ID: "stale-notice", Owner: backgroundBashOwnerKey(owner), Status: "succeeded"})
	message, err := db.MessageFor(context.Background(), "background-bash-completed-stale-notice")
	require.NoError(t, err)
	require.Nil(t, message)
}

func TestBackgroundBashProjectionDoesNotTriggerPruneCallbacks(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	t.Cleanup(func() { _ = jobs.Close() })
	calls := 0
	jobs.SetChangeCallback(func() { calls++ })
	jobs.mu.Lock()
	jobs.jobs["old"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "old", Owner: "owner", Status: "succeeded", FinishedAt: time.Now().Add(-2 * BackgroundBashRetention)}}
	jobs.mu.Unlock()
	require.Empty(t, jobs.projectionJobs("owner"))
	require.Zero(t, calls)
}

func TestBackgroundBashGlobalChangeReservesProjectionVersion(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	before, err := db.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	calls := 0
	manager := &Manager{store: db}
	manager.SetProjectionPublisher(func() { calls++ })
	// No live owners are required for a global invalidation: pruning a stale
	// owner's job must still publish removal of another owner's visible record.
	require.NoError(t, manager.backgroundBashChanged(ctx))
	after, err := db.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, before.ProjectionVersion+1, after.ProjectionVersion)
	require.Equal(t, 1, calls)
}

func TestBackgroundBashProjectionDiscardsOwnerReplacedDuringRead(t *testing.T) {
	manager, db, _, id, _ := requiredToolsFixture(t, "root")
	manager.ensureBackgroundBash()
	manager.mu.Lock()
	owner := manager.embeddedOwners[id]
	jobs := manager.backgroundBash
	manager.mu.Unlock()
	record, err := db.ManagedAgent(context.Background(), id)
	require.NoError(t, err)
	jobs.mu.Lock()
	jobs.jobs["old-job"] = &backgroundBashEntry{job: BackgroundBashJob{ID: "old-job", Owner: backgroundBashOwnerKey(owner), Status: "succeeded", FinishedAt: time.Now()}}
	jobs.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	type outcome struct {
		jobs []map[string]string
		err  error
	}
	result := make(chan outcome, 1)
	go func() {
		jobs, err := manager.backgroundBashJobsContext(context.Background(), record, func() { close(entered); <-release })
		result <- outcome{jobs, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot did not reach fence")
	}
	manager.mu.Lock()
	replacement := owner
	replacement.identity += "-replacement"
	manager.embeddedOwners[id] = replacement
	manager.mu.Unlock()
	close(release)
	select {
	case got := <-result:
		require.Error(t, got.err)
		require.Empty(t, got.jobs)
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot blocked")
	}
	manager.mu.Lock()
	manager.embeddedOwners[id] = owner
	manager.mu.Unlock()
}
