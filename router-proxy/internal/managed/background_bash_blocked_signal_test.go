//go:build linux

package managed

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func TestBackgroundBashBlockedSignalDoesNotBlockCloseRetriesOrOtherJobs(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	first, err := m.Launch("a", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Launch("b", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	firstPID := m.jobs[first.ID].identity.PID
	m.mu.Unlock()
	entered, release, other := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.mu.Lock()
	m.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		if id.PID == firstPID {
			if calls.Add(1) == 1 {
				close(entered)
			}
			<-release
		} else {
			close(other)
		}
		return DefaultProcessGroupOps().SignalVerifiedProcessGroup(id, sig)
	}
	m.mu.Unlock()
	defer func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.CloseContext(ctx); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.CloseContext(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("blocked signal not entered")
	}
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("other job not signaled")
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel2()
	if err := m.CloseContext(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retry: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("concurrent signals for same job: %d", n)
	}
	_ = second
}

func TestSupervisorCloseHonorsBlockedBackgroundSignalCancellation(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	m.ensureBackgroundBash()
	jobs := m.backgroundBash
	_, err = jobs.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	jobs.mu.Lock()
	jobs.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		close(entered)
		<-release
		return DefaultProcessGroupOps().SignalVerifiedProcessGroup(id, sig)
	}
	jobs.mu.Unlock()
	defer func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Close(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("signal not entered")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor close blocked")
	}
}

func TestBackgroundBashRetirementBlockedSignalDoesNotBlockShutdownOrOtherJob(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	cap := &backgroundBashOwnerCapability{}
	a, err := cap.launch(func() (BackgroundBashJob, error) { return m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"}) })
	if err != nil {
		t.Fatal(err)
	}
	_, err = cap.launch(func() (BackgroundBashJob, error) { return m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"}) })
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	pid := m.jobs[a.ID].identity.PID
	entered, release, other := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m.signal = func(id ProcessIdentity, sig syscall.Signal) error {
		if id.PID == pid {
			close(entered)
			<-release
			return errors.New("injected failure")
		}
		close(other)
		return DefaultProcessGroupOps().SignalVerifiedProcessGroup(id, sig)
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cap.revoke(func() error { return m.StopOwnerContext(ctx, "owner") }) }()
	<-entered
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("other job not signaled")
	}
	_, err = cap.launch(func() (BackgroundBashJob, error) { return m.Launch("owner", BackgroundBashLaunch{Command: "true"}) })
	if !errors.Is(err, errBackgroundBashOwnerRetired) {
		t.Fatalf("launch: %v", err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer closeCancel()
	if err := m.CloseContext(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close: %v", err)
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retire: %v", err)
	}
	m.mu.Lock()
	attempt := m.jobs[a.ID].signalActive
	m.signal = nil
	m.mu.Unlock()
	close(release)
	<-attempt.done
	m.mu.Lock()
	m.signal = nil
	m.mu.Unlock()
	cleanup, cleanupCancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cleanupCancel()
	if err := m.CloseContext(cleanup); err != nil {
		t.Fatal(err)
	}
}
