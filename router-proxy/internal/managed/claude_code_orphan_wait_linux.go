//go:build linux

package managed

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func waitClaudeCodeCommand(cmd *exec.Cmd, identity ProcessIdentity) error {
	return waitClaudeCodeCommandConfirmed(cmd, identity, nil)
}

func waitClaudeCodeCommandConfirmed(cmd *exec.Cmd, identity ProcessIdentity, confirmed func()) error {
	return waitClaudeCodeCommandWithReapHook(cmd, identity, confirmed, nil)
}

func waitClaudeCodeCommandWithReapHook(cmd *exec.Cmd, identity ProcessIdentity, confirmed func(), reap func(func() error) error) error {
	// WNOWAIT pins the exact zombie leader until all executable members vanish.
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: waitid: %v", errClaudeCodeGroupUnconfirmed, err)
		}
		break
	}
	ops := defaultProcessGroupOps{}
	probe := ops.Probe(identity)
	switch probe.State {
	case ProcessAbsent:
	case ProcessPresent:
		// This is our child and this function is its sole waiter. WNOWAIT above
		// pins its PID and process group until cmd.Wait below, unlike a shared
		// probe or a persisted orphan record after router restart.
		marker, markerErr := processStartMarker(identity.PID)
		state, group, statErr := linuxProcStat(identity.PID)
		if markerErr != nil || statErr != nil || marker != identity.StartMarker || state != 'Z' || group != identity.PID || cmd.Process.Pid != identity.PID {
			return fmt.Errorf("%w: pinned child identity unverified", errClaudeCodeGroupUnconfirmed)
		}
		if err := signalOwnedProcessGroup(identity.PID, syscall.SIGKILL); err != nil {
			return fmt.Errorf("%w: %v", errClaudeCodeGroupUnconfirmed, err)
		}
	default:
		return fmt.Errorf("%w: unsafe pinned child identity: %s", errClaudeCodeGroupUnconfirmed, probe.State)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := confirmClaudeCodeGroupAbsent(ctx, func() ProcessProbeState { return ops.Probe(identity).State }); err != nil {
		// Do NOT call Wait: preserve the verifiable leader and fail admission closed.
		return fmt.Errorf("%w: %v", errClaudeCodeGroupUnconfirmed, err)
	}
	if confirmed != nil {
		confirmed()
	}
	if reap != nil {
		return reap(cmd.Wait)
	}
	return cmd.Wait()
}
