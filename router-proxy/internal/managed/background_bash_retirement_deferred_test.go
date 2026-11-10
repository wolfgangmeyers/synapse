//go:build linux

package managed

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestRetirementTimeoutCleansAdmittedLaunchWithoutRetry(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	jobs.startCommand = func(cmd *exec.Cmd) error { close(entered); <-release; return cmd.Start() }
	owner := embeddedOwner{identity: "retired", backgroundBash: &backgroundBashOwnerCapability{}}
	successor := embeddedOwner{identity: "successor", backgroundBash: &backgroundBashOwnerCapability{}}
	m := &Manager{backgroundBash: jobs}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = jobs.CloseContext(ctx)
	}()
	launched := make(chan error, 1)
	go func() {
		_, err := owner.backgroundBash.launch(func() (BackgroundBashJob, error) {
			return jobs.Launch(backgroundBashOwnerKey(owner), BackgroundBashLaunch{Command: "exec sleep 30"})
		})
		launched <- err
	}()
	<-entered
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := m.retireBackgroundBashOwnerContext(ctx, owner)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("retire: %v", err)
		}
	}
	_, err := owner.backgroundBash.launch(func() (BackgroundBashJob, error) { t.Error("stale launch admitted"); return BackgroundBashJob{}, nil })
	if !errors.Is(err, errBackgroundBashOwnerRetired) {
		t.Fatalf("fence: %v", err)
	}
	close(release)
	if err := <-launched; err != nil {
		t.Fatal(err)
	}
	jobs.startCommand = nil
	other, err := successor.backgroundBash.launch(func() (BackgroundBashJob, error) {
		return jobs.Launch(backgroundBashOwnerKey(successor), BackgroundBashLaunch{Command: "exec sleep 30"})
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list := jobs.List(backgroundBashOwnerKey(owner))
		if len(list) == 1 && list[0].Status == "killed" {
			if jobs.List(backgroundBashOwnerKey(successor))[0].Status != "running" {
				t.Fatal("successor stopped")
			}
			_ = other
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("retired job not cleaned: %+v", jobs.List(backgroundBashOwnerKey(owner)))
}

func TestRetirementDeferredCleanupScheduledOnce(t *testing.T) {
	cap := &backgroundBashOwnerCapability{}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, _ = cap.launch(func() (BackgroundBashJob, error) { close(entered); <-release; return BackgroundBashJob{}, nil })
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = cap.revokeContext(ctx, func() error { return nil })
	calls := make(chan struct{}, 2)
	cap.deferCleanupAfterLaunch(func() { calls <- struct{}{} })
	cap.deferCleanupAfterLaunch(func() { calls <- struct{}{} })
	close(release)
	<-done
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("cleanup not called")
	}
	select {
	case <-calls:
		t.Fatal("duplicate cleanup")
	case <-time.After(50 * time.Millisecond):
	}
	cap.deferCleanupAfterLaunch(func() { calls <- struct{}{} })
	select {
	case <-calls:
		t.Fatal("cleanup rescheduled")
	case <-time.After(50 * time.Millisecond):
	}
}
