//go:build linux

package managed

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestBackgroundWatchdogSuccessSurvivesIdentifyFailure(t *testing.T) {
	m := NewBackgroundBashManager(nil)
	entered, release, signaled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.identify = func(int) (string, error) { close(entered); <-release; return "", errors.New("identity unavailable") }
	m.killGroup = func(pid int, sig syscall.Signal) error {
		if calls.Add(1) != 1 {
			return errors.New("redundant signal denied")
		}
		err := syscall.Kill(pid, sig)
		close(signaled)
		return err
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.Launch("owner", BackgroundBashLaunch{Command: "exec sleep 30", TimeoutSeconds: 1})
		result <- err
	}()
	<-entered
	select {
	case <-signaled:
	case <-time.After(4 * time.Second):
		close(release)
		t.Fatal("watchdog did not signal")
	}
	close(release)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "identity unavailable") {
		t.Fatalf("launch error: %v", err)
	}
	m.mu.Lock()
	var entry *backgroundBashEntry
	for _, e := range m.jobs {
		entry = e
	}
	m.mu.Unlock()
	select {
	case <-entry.done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not finish")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("unverified group cleanup must remain uncertain: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("signals after successful watchdog: %d", n)
	}
}
