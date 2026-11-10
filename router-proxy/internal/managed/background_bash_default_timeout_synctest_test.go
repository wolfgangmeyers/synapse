//go:build linux

package managed

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

// This exercises the manager's omitted-timeout path with no OS process started
// or signaled. It does not cover the real process-group implementation.
func TestBackgroundBashOmittedDefaultTimeoutFakeProcess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewBackgroundBashManager(nil)
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		defer func() {
			unblock()
			synctest.Wait()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := m.CloseContext(ctx); err != nil {
				t.Errorf("manager cleanup: %v", err)
			}
		}()
		m.startCommand = func(cmd *exec.Cmd) error {
			// FindProcess constructs a handle only; no method is called on it.
			cmd.Process, _ = os.FindProcess(123456789)
			return nil
		}
		m.identify = func(int) (string, error) { return "fake", nil }
		var signals []syscall.Signal
		m.signal = func(_ ProcessIdentity, sig syscall.Signal) error {
			signals = append(signals, sig)
			return nil
		}
		m.killGroup = func(int, syscall.Signal) error { t.Error("unexpected launch watchdog signal"); return nil }
		m.waitCommand = func(_ *exec.Cmd, _ ProcessIdentity, _ func(func() error) error) error {
			<-release
			return nil
		}
		job, err := m.Launch("owner", BackgroundBashLaunch{Command: "fake"})
		if err != nil {
			t.Fatal(err)
		}
		if got := job.Deadline.Sub(job.StartedAt); got != 600*time.Second {
			t.Fatalf("deadline = %v", got)
		}
		synctest.Wait()
		time.Sleep(599 * time.Second)
		synctest.Wait()
		got, err := m.Get("owner", job.ID)
		if err != nil || got.Status != "running" || len(signals) != 0 {
			t.Fatalf("at 599s: job=%+v signals=%v err=%v", got, signals, err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if len(signals) != 1 || signals[0] != syscall.SIGKILL {
			t.Fatalf("at 600s: signals=%v", signals)
		}
		unblock()
		synctest.Wait()
		got, err = m.Get("owner", job.ID)
		if err != nil || got.Status != "timed_out" || got.FinishedAt.IsZero() || len(signals) != 1 {
			t.Fatalf("terminal: job=%+v signals=%v err=%v", got, signals, err)
		}
	})
}
