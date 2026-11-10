//go:build linux

package managed

import (
	"context"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestBackgroundIdentificationFailureBlockedCleanupDoesNotBlockManager(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	identifyErr := errors.New("injected identification failure")
	entered := make(chan int, 1)
	release := make(chan struct{})
	launchDone := make(chan error, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	m.identify = func(pid int) (string, error) { return "", identifyErr }
	m.killGroup = func(pid int, sig syscall.Signal) error {
		select {
		case entered <- pid:
		default:
		}
		<-release
		return syscall.Kill(pid, sig)
	}
	go func() {
		_, err := m.Launch("failed", BackgroundBashLaunch{Command: "exit 0"})
		launchDone <- err
	}()
	defer func() {
		unblock()
		select {
		case <-launchDone:
		case <-time.After(5 * time.Second):
			t.Error("launch did not finish during cleanup")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// The unidentified job intentionally retains uncertainty after reaping.
		_ = m.CloseContext(ctx)
	}()
	var group int
	select {
	case group = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup signal not entered")
	}
	pid := -group
	if pid <= 0 {
		t.Fatalf("cleanup did not target child process group: %d", group)
	}
	// Wait for the leader to exit without reaping it. The zombie reserves the PID
	// throughout the blocked signal, so that signal cannot target a reused group.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var info unix.Siginfo
		if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil); err != nil {
			t.Fatalf("child no longer waitable: %v", err)
		}
		if info.Signo != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not exit")
		}
		time.Sleep(time.Millisecond)
	}
	operations := make(chan error, 1)
	go func() {
		jobs := m.List("failed")
		if len(jobs) != 1 || jobs[0].Status != "uncertain" {
			operations <- errors.New("missing uncertain job")
			return
		}
		if _, err := m.Get("failed", jobs[0].ID); err != nil {
			operations <- err
			return
		}
		m.mu.Lock()
		m.identify = nil
		m.mu.Unlock()
		_, err := m.Launch("other", BackgroundBashLaunch{Command: "sleep 30"})
		operations <- err
	}()
	select {
	case err := <-operations:
		if err != nil {
			t.Fatalf("other operations: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("other manager operations blocked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- m.CloseContext(ctx) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("context-aware shutdown blocked")
	}
	jobs := m.List("other")
	if len(jobs) != 1 || jobs[0].Status == "running" {
		t.Fatalf("other job not stopped: %+v", jobs)
	}
	select {
	case err := <-launchDone:
		launchDone <- err
		t.Fatalf("launch returned before cleanup signal released: %v", err)
	default:
	}
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil); err != nil || info.Signo == 0 {
		t.Fatalf("child PID not pinned before signal release: info=%+v err=%v", info, err)
	}
	// Release explicitly to verify the error and eventual reap; defer still owns cleanup.
	unblock()
	select {
	case err := <-launchDone:
		if !errors.Is(err, identifyErr) || !strings.Contains(err.Error(), "cleanup uncertain") {
			t.Fatalf("launch error: %v", err)
		}
		launchDone <- err
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not return after signal release")
	}
	m.mu.Lock()
	var entry *backgroundBashEntry
	for _, e := range m.jobs {
		if e.job.Owner == "failed" {
			entry = e
		}
	}
	m.mu.Unlock()
	select {
	case <-entry.done:
		if entry.cmd.ProcessState == nil {
			t.Fatal("child not reaped")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child not reaped after cleanup")
	}
}
