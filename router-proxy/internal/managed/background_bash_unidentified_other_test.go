//go:build !linux

package managed

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestUnidentifiedBackgroundDoesNotRetryWithoutPinnedIdentity(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	calls := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		waitUnidentifiedBackground(cmd, 20*time.Millisecond, func(int, syscall.Signal) error {
			select {
			case calls <- struct{}{}:
			default:
			}
			return nil
		}, true, 1)
		close(done)
	}()
	select {
	case <-calls:
		t.Fatal("unsafe deadline retry")
	case <-done:
		t.Fatal("wait returned before child exited")
	case <-time.After(200 * time.Millisecond):
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("child not reaped")
	}
	if cmd.ProcessState == nil {
		t.Fatal("missing process wait")
	}
	select {
	case <-calls:
		t.Fatal("unsafe retry after child exit")
	default:
	}
}
