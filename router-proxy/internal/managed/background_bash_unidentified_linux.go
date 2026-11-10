//go:build linux

package managed

import (
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The unreaped leader pins its process group until the last group signal.
// A successful signal and leader exit cannot prove that all descendants died.
func waitUnidentifiedBackground(cmd *exec.Cmd, _ time.Duration, kill func(int, syscall.Signal) error, _ bool, attempts int) bool {
	return waitUnidentifiedBackgroundWithWait(cmd, 0, kill, false, attempts, nil, nil)
}

func waitUnidentifiedBackgroundWithWait(cmd *exec.Cmd, _ time.Duration, kill func(int, syscall.Signal) error, _ bool, attempts int, wait func(*exec.Cmd) error, observed func(int) (bool, error)) bool {
	if wait == nil {
		wait = (*exec.Cmd).Wait
	}
	// Launch already made one group-signal attempt. Bound all further attempts,
	// including successful ones, and never signal the group after Wait begins.
	next := time.Now().Add(100 * time.Millisecond)
	var reapDeadline time.Time
	for {
		var exited bool
		var err error
		if observed != nil {
			exited, err = observed(cmd.Process.Pid)
		} else {
			var info unix.Siginfo
			err = unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
			exited = info.Signo != 0
		}
		if err == syscall.EINTR {
			continue
		}
		if err != nil || exited {
			if err == nil {
				_ = wait(cmd)
			} else {
				_ = cmd.Process.Kill()
				_ = wait(cmd)
			}
			return false
		}
		now := time.Now()
		if attempts < 3 && !now.Before(next) {
			_ = kill(-cmd.Process.Pid, syscall.SIGKILL)
			attempts++
			next = now.Add(100 * time.Millisecond)
		}
		if attempts == 3 && reapDeadline.IsZero() {
			// Kill the direct child as a best effort. The tracked waiter may stay
			// blocked on a stuck child; caller deadlines still bound shutdown.
			_ = cmd.Process.Kill()
			reapDeadline = now.Add(100 * time.Millisecond)
		}
		if !reapDeadline.IsZero() && !now.Before(reapDeadline) {
			_ = wait(cmd)
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
