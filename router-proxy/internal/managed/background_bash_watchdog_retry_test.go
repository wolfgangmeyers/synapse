//go:build linux

package managed

import (
	"context"
	"errors"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestBackgroundLaunchWatchdogRetriesDuringIdentification(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	entered := make(chan int, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	succeeded := make(chan struct{}, 1)
	var calls atomic.Int32
	m.identify = func(pid int) (string, error) { entered <- pid; <-release; return processStartMarker(pid) }
	m.killGroup = func(pid int, sig syscall.Signal) error {
		if calls.Add(1) == 1 {
			return errors.New("injected signal failure")
		}
		err := syscall.Kill(pid, sig)
		if err == nil {
			succeeded <- struct{}{}
		}
		return err
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
	select {
	case <-succeeded:
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not retry failed signal while identification blocked")
	}
	var info unix.Siginfo
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil); err != nil {
			t.Fatalf("leader no longer waitable: %v", err)
		}
		if info.Signo != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("leader did not exit")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Fatalf("signals after success: %d", n)
	}
}
