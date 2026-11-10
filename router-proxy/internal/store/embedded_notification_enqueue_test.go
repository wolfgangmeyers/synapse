package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedOwnerEnqueueFencesAndPreservesOrdinaryAdmission(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "notifications.db")
	a, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer a.Close()
	b, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer b.Close()
	binding := embeddedManagedAgent(t, a, "notification-owner")
	require.NoError(t, a.BindEmbeddedSession(ctx, binding, "root"))
	binding.OwnerToken, err = a.AcquireEmbeddedOwner(ctx, binding, "first", time.Minute)
	require.NoError(t, err)
	e := testEnvelope("notice-one", "done")
	e.To = binding.AgentID
	opts := EnqueueOptions{Conversation: &ConversationOptions{Body: &e.Body}}
	result, err := a.EnqueueEmbeddedOwnerNotification(ctx, binding, "first", e, opts)
	require.NoError(t, err)
	require.True(t, result.Created)
	require.True(t, result.ConversationCreated)
	replay, err := a.EnqueueEmbeddedOwnerNotification(ctx, binding, "first", e, opts)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, result.Message.ID, replay.Message.ID)
	conflict := e
	conflict.Body = "different"
	_, err = a.EnqueueEmbeddedOwnerNotification(ctx, binding, "first", conflict, opts)
	require.Error(t, err)
	next := binding
	next.OwnerToken, err = b.AcquireEmbeddedOwner(ctx, binding, "second", time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, binding.OwnerToken, next.OwnerToken)
	// Even replays are fenced before they can observe an accepted receipt.
	_, err = a.EnqueueEmbeddedOwnerNotification(ctx, binding, "first", e, opts)
	require.ErrorIs(t, err, ErrStaleEmbeddedFence)
	e.ID = "notice-stale"
	_, err = a.EnqueueEmbeddedOwnerNotification(ctx, binding, "first", e, opts)
	require.ErrorIs(t, err, ErrStaleEmbeddedFence)
	_, err = b.EnqueueEmbeddedOwnerNotification(ctx, next, "first", e, opts)
	require.ErrorIs(t, err, ErrStaleEmbeddedFence)
	e.To = "another-agent"
	_, err = b.EnqueueEmbeddedOwnerNotification(ctx, next, "second", e, opts)
	require.Error(t, err)
	e.To = next.AgentID
	_, err = b.EnqueueEmbeddedOwnerNotification(ctx, next, "second", e, opts)
	require.NoError(t, err)
}

func TestEmbeddedOwnerEnqueueSerializesWithTakeover(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "race.db")
	a, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer a.Close()
	b, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer b.Close()
	binding := embeddedManagedAgent(t, a, "race-notification")
	require.NoError(t, a.BindEmbeddedSession(ctx, binding, "root"))
	binding.OwnerToken, err = a.AcquireEmbeddedOwner(ctx, binding, "first", time.Minute)
	require.NoError(t, err)
	e := testEnvelope("serialized-notice", "done")
	e.To = binding.AgentID
	entered, release := make(chan struct{}), make(chan struct{})
	enqueued, acquired := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := a.EnqueueEmbeddedOwnerNotification(ctx, binding, "first", e, EnqueueOptions{OnFreshAdmission: func() (func(bool), error) {
			close(entered)
			<-release
			return nil, nil
		}})
		enqueued <- err
	}()
	<-entered
	go func() { _, err := b.AcquireEmbeddedOwner(ctx, binding, "second", time.Minute); acquired <- err }()
	select {
	case err := <-acquired:
		t.Fatalf("takeover passed an in-flight admission: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-enqueued)
	require.NoError(t, <-acquired)
	e.ID = "after-takeover"
	_, err = a.EnqueueEmbeddedOwnerNotification(ctx, binding, "first", e)
	require.ErrorIs(t, err, ErrStaleEmbeddedFence)
}

func TestEmbeddedOwnerNotificationsWithSameContentRemainDistinct(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "distinct.db"), Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := embeddedManagedAgent(t, db, "distinct-owner")
	require.NoError(t, db.BindEmbeddedSession(ctx, binding, "root"))
	binding.OwnerToken, err = db.AcquireEmbeddedOwner(ctx, binding, "owner", time.Minute)
	require.NoError(t, err)
	first := testEnvelope("reset-one", "same wake")
	first.To = binding.AgentID
	second := first
	second.ID = "reset-two"
	a, err := db.EnqueueEmbeddedOwnerNotification(ctx, binding, "owner", first)
	require.NoError(t, err)
	b, err := db.EnqueueEmbeddedOwnerNotification(ctx, binding, "owner", second)
	require.NoError(t, err)
	require.True(t, a.Created)
	require.True(t, b.Created)
	require.Equal(t, second.ID, b.Message.ID)
}

// SQLite commits through its open handles; the state-file permission step then
// fails on a procfs target. This exercises the real post-COMMIT error path.
func TestEmbeddedOwnerNotificationRealPostCommitFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs fault fixture")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "postcommit.db")
	db, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := embeddedManagedAgent(t, db, "postcommit-owner")
	require.NoError(t, db.BindEmbeddedSession(ctx, binding, "root"))
	binding.OwnerToken, err = db.AcquireEmbeddedOwner(ctx, binding, "owner", time.Minute)
	require.NoError(t, err)
	e := testEnvelope("postcommit-wake", "wake")
	e.To = binding.AgentID
	opts := EnqueueOptions{Conversation: &ConversationOptions{Body: &e.Body}}
	opts.OnFreshAdmission = func() (func(bool), error) {
		if err := os.Rename(path, path+".saved"); err != nil {
			return nil, err
		}
		if err := os.Symlink("/proc/version", path); err != nil {
			_ = os.Rename(path+".saved", path)
			return nil, err
		}
		return nil, nil
	}
	_, err = db.EnqueueEmbeddedOwnerNotification(ctx, binding, "owner", e, opts)
	require.ErrorContains(t, err, "secure SQLite state file")
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Rename(path+".saved", path))
	opts.OnFreshAdmission = nil
	replay, err := db.EnqueueEmbeddedOwnerNotification(ctx, binding, "owner", e, opts)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.NotNil(t, replay.ConversationMessage)
	require.Equal(t, e.ID, replay.Message.ID)
}
