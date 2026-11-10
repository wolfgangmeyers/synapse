package managed

import (
	"context"
	"errors"
	"testing"

	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestResetStaleBindingDoesNotInterruptSuccessorRealStore(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	lane := f.m.laneFor(owner.binding.AgentID)
	runtime := &resetBarrierRuntime{make(chan struct{}), make(chan struct{})}
	require.True(t, lane.installEmbeddedRuntime(runtime, nil))
	defer lane.removeEmbeddedRuntime(runtime)
	stale := owner.binding
	stale.Epoch--
	require.Error(t, f.m.ExecuteContextReset(context.Background(), stale, "stale"))
	select {
	case <-runtime.interrupted:
		t.Fatal("successor interrupted")
	default:
	}
	select {
	case <-runtime.closed:
		t.Fatal("successor closed")
	default:
	}
	_, err := f.db.ReopenEmbedded(context.Background(), owner.binding)
	require.NoError(t, err)
}

func TestResetCleanupFailureAndTransactionRetryRealStore(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	jobs := NewBackgroundBashManager(nil)
	f.m.mu.Lock()
	f.m.backgroundBash = jobs
	f.m.mu.Unlock()
	done := make(chan struct{})
	close(done)
	entry := &backgroundBashEntry{job: BackgroundBashJob{ID: "uncertain", Owner: backgroundBashOwnerKey(owner), Status: "uncertain", Error: "unconfirmed"}, done: done}
	jobs.mu.Lock()
	jobs.jobs["uncertain"] = entry
	jobs.mu.Unlock()
	ctx := context.Background()
	err := f.m.ExecuteContextReset(ctx, owner.binding, "cleanup")
	require.ErrorContains(t, err, "unconfirmed")
	_, err = f.db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	f.m.mu.Lock()
	current := f.m.embeddedOwners[owner.binding.AgentID]
	f.m.mu.Unlock()
	require.Equal(t, owner, current)
	_, err = owner.backgroundBash.launch(func() (BackgroundBashJob, error) { return BackgroundBashJob{}, nil })
	require.ErrorIs(t, err, errBackgroundBashOwnerRetired)
	jobs.mu.Lock()
	entry.job.Status = "killed"
	jobs.mu.Unlock()
	// A retry can now advance the same real-store epoch.
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("awake")})
	require.NoError(t, f.m.ExecuteContextReset(ctx, owner.binding, "retry"))
	f.m.mu.Lock()
	next := f.m.embeddedOwners[owner.binding.AgentID]
	f.m.mu.Unlock()
	require.Equal(t, owner.binding.Epoch+1, next.binding.Epoch)
	_, err = f.db.ReopenEmbedded(ctx, next.binding)
	require.NoError(t, err)
}

func TestResetTransactionFailurePreservesOwnerRealStore(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	_, err := f.raw.Exec(`CREATE TRIGGER reject_reset_epoch BEFORE UPDATE OF session_epoch ON managed_agents BEGIN SELECT RAISE(ABORT, 'reset transaction fault'); END`)
	require.NoError(t, err)
	err = f.m.ExecuteContextReset(context.Background(), owner.binding, "failure")
	require.ErrorContains(t, err, "reset transaction fault")
	_, err = f.db.ReopenEmbedded(context.Background(), owner.binding)
	require.NoError(t, err)
	f.m.mu.Lock()
	current := f.m.embeddedOwners[owner.binding.AgentID]
	f.m.mu.Unlock()
	require.Equal(t, owner, current)
	_, err = owner.backgroundBash.launch(func() (BackgroundBashJob, error) { return BackgroundBashJob{}, nil })
	require.False(t, errors.Is(err, errBackgroundBashOwnerRetired))
	require.NoError(t, err)
}
