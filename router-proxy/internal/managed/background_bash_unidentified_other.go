//go:build !linux

package managed

import (
	"os/exec"
	"syscall"
	"time"
)

// Without a non-reaping wait, further group signals could target a reused PID.
func waitUnidentifiedBackground(cmd *exec.Cmd, _ time.Duration, _ func(int, syscall.Signal) error, initialSignalSucceeded bool, _ int) bool {
	if !initialSignalSucceeded {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	return false
}

func waitUnidentifiedBackgroundWithWait(cmd *exec.Cmd, timeout time.Duration, kill func(int, syscall.Signal) error, initial bool, attempts int, wait func(*exec.Cmd) error, _ func(int) (bool, error)) bool {
	if wait == nil {
		return waitUnidentifiedBackground(cmd, timeout, kill, initial, attempts)
	}
	if !initial {
		_ = cmd.Process.Kill()
	}
	_ = wait(cmd)
	return false
}
