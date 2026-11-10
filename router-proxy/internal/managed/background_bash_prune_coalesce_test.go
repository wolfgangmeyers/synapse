package managed

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundBashGlobalInvalidationCoalescesAndCloseDrains(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.SetChangeCallback(func() {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	})
	prune := func(owner string, i int) {
		m.mu.Lock()
		id := fmt.Sprint(i)
		m.jobs[id] = &backgroundBashEntry{job: BackgroundBashJob{ID: id, Owner: owner, Status: "succeeded", FinishedAt: time.Now().Add(-2 * BackgroundBashRetention)}}
		m.mu.Unlock()
		m.List(owner)
	}
	prune("first", 0)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher not entered")
	}
	for i := 1; i <= 1000; i++ {
		prune(fmt.Sprint(i), i)
	}
	m.mu.Lock()
	if len(m.jobs) != 0 || !m.projectionWorker || !m.projectionDirty || m.callbackCount != 1 {
		t.Errorf("jobs=%d worker=%v dirty=%v callbacks=%d", len(m.jobs), m.projectionWorker, m.projectionDirty, m.callbackCount)
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if m.CloseContext(ctx) == nil {
		t.Error("close did not wait for publisher")
	}
	close(release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := m.CloseContext(ctx2); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected two global publications, got %d", got)
	}
}

func TestBackgroundBashBlockedProjectionDoesNotDelayCompletionNotice(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	notice := make(chan BackgroundBashJob, 1)
	m := NewBackgroundBashManager(func(job BackgroundBashJob) { notice <- job })
	var calls atomic.Int32
	m.SetChangeCallback(func() {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	})
	job, err := m.Launch("owner", BackgroundBashLaunch{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher not entered")
	}
	select {
	case got := <-notice:
		if got.ID != job.ID {
			t.Fatalf("notice: %+v", got)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("notice blocked behind publisher")
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundBashProjectionRetriesTransientFailureWithoutNewChange(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	defer m.Close()
	var attempts atomic.Int32
	delivered := make(chan struct{})
	// The production callback reports reservation/publication failure to the
	// coalescer, which must retry without a second mutation.
	m.SetChangeCallbackContext(func(ctx context.Context) error {
		if attempts.Add(1) == 1 {
			return errors.New("temporary store failure")
		}
		close(delivered)
		return nil
	})
	m.mu.Lock()
	m.changedAsyncLocked(map[string]struct{}{"owner": {}})
	m.mu.Unlock()
	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("invalidation lost after transient failure")
	}
}

func TestBackgroundBashShutdownCancelsBlockedProjection(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	entered, exited := make(chan struct{}), make(chan struct{})
	m.SetChangeCallbackContext(func(ctx context.Context) error { close(entered); <-ctx.Done(); close(exited); return ctx.Err() })
	m.mu.Lock()
	m.changedAsyncLocked(map[string]struct{}{"owner": {}})
	m.mu.Unlock()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("publisher not entered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("publisher outlived shutdown")
	}
}
