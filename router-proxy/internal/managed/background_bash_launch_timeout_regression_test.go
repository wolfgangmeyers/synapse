//go:build linux

package managed

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Identification must not postpone the original launch deadline. Keep the
// child unreaped while identification is blocked so waitid observes its exit.
func TestBackgroundLaunchTimeoutDuringIdentification(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	entered := make(chan int, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	m.identify = func(pid int) (string, error) {
		entered <- pid
		<-release
		return processStartMarker(pid)
	}
	go func() {
		defer close(done)
		_, _ = m.Launch("owner", BackgroundBashLaunch{Command: "exec sleep 30", TimeoutSeconds: 1})
	}()
	pid := <-entered
	defer func() {
		close(release)
		<-done
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.CloseContext(ctx)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var info unix.Siginfo
		if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil); err != nil {
			t.Fatalf("waitid: %v", err)
		}
		if info.Signo != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("started shell remains alive past launch deadline while identification is blocked")
}

func TestBackgroundLaunchDeadlineNotResetAfterSlowStart(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	m.startCommand = func(cmd *exec.Cmd) error {
		close(entered)
		<-release
		return cmd.Start()
	}
	done := make(chan BackgroundBashJob, 1)
	go func() {
		job, _ := m.Launch("owner", BackgroundBashLaunch{Command: "exec sleep 30", TimeoutSeconds: 1})
		done <- job
	}()
	<-entered
	jobs := m.List("owner")
	if len(jobs) != 1 {
		t.Fatal("missing pending launch")
	}
	original := jobs[0].Deadline
	for time.Now().Before(original.Add(50 * time.Millisecond)) {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	select {
	case job := <-done:
		if !job.Deadline.Equal(original) {
			t.Fatalf("deadline changed: %v to %v", original, job.Deadline)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("launch blocked")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		jobs = m.List("owner")
		if len(jobs) == 1 && jobs[0].Status == "timed_out" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(jobs) != 1 || jobs[0].Status != "timed_out" {
		t.Fatalf("not timed out: %+v", jobs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
}
