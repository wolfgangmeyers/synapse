//go:build linux

package managed

import (
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestUnidentifiedSignalDoesNotProveGroupCleanup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if waitUnidentifiedBackground(cmd, time.Second, syscall.Kill, true, 1) {
		t.Fatal("signal and leader exit do not prove group absence")
	}
}

func TestUnidentifiedSuccessfulSignalsAreBounded(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	var calls atomic.Int32
	done := make(chan bool, 1)
	go func() {
		done <- waitUnidentifiedBackground(cmd, 20*time.Millisecond, func(int, syscall.Signal) error { calls.Add(1); return nil }, true, 1)
	}()
	select {
	case confirmed := <-done:
		if confirmed {
			t.Fatal("unverified cleanup")
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("successful signal retries never terminate")
	}
	if calls.Load() > 3 {
		t.Fatalf("excess retries: %d", calls.Load())
	}
}

func TestUnidentifiedInitialFailedSignalCountsTowardLimit(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	m.identify = func(int) (string, error) { return "", errors.New("unavailable") }
	var calls atomic.Int32
	m.killGroup = func(int, syscall.Signal) error { calls.Add(1); return errors.New("denied") }
	_, _ = m.Launch("owner", BackgroundBashLaunch{Command: "sleep 30"})
	m.mu.Lock()
	var e *backgroundBashEntry
	for _, item := range m.jobs {
		e = item
	}
	m.mu.Unlock()
	if e == nil {
		t.Fatal("missing job")
	}
	select {
	case <-e.done:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not finish")
	}
	if n := calls.Load(); n > 3 {
		t.Fatalf("group signals=%d, maximum 3", n)
	}
	_ = m.Close()
}
