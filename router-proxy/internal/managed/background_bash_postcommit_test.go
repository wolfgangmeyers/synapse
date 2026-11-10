package managed

import (
	"context"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestBackgroundBashCompletionDoesNotReplayPostCommitError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs fault fixture")
	}
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	var published []store.EnqueueResult
	f.m.SetEmbeddedMailPublisher(func(r store.EnqueueResult) { published = append(published, r) })
	var calls atomic.Int32
	finished := time.Date(2026, 9, 27, 23, 59, 59, 0, time.UTC)
	f.m.enqueueBackgroundBashCompletion(BackgroundBashJob{ID: "postcommit", Owner: backgroundBashOwnerKey(owner), Status: "succeeded", FinishedAt: finished}, func() (func(bool), error) {
		calls.Add(1)
		if err := os.Rename(f.path, f.path+".saved"); err != nil {
			return nil, err
		}
		if err := os.Symlink("/proc/version", f.path); err != nil {
			_ = os.Rename(f.path+".saved", f.path)
			return nil, err
		}
		return func(committed bool) {
			require.True(t, committed)
			require.NoError(t, os.Remove(f.path))
			require.NoError(t, os.Rename(f.path+".saved", f.path))
			require.NoError(t, f.clock.Advance(time.Second))
		}, nil
	})
	require.EqualValues(t, 1, calls.Load())
	require.Empty(t, published)
	msg, err := f.db.MessageFor(context.Background(), "background-bash-completed-postcommit")
	require.NoError(t, err)
	require.NotNil(t, msg)
	var count int
	require.NoError(t, f.raw.QueryRow(`SELECT count(*) FROM messages WHERE id=?`, msg.ID).Scan(&count))
	require.Equal(t, 1, count)
}
