//go:build !linux

package managed

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

func waitClaudeCodeCommand(cmd *exec.Cmd, identity ProcessIdentity) error {
	return waitClaudeCodeCommandConfirmed(cmd, identity, nil)
}

func waitClaudeCodeCommandConfirmed(cmd *exec.Cmd, identity ProcessIdentity, confirmed func()) error {
	return waitClaudeCodeCommandWithReapHook(cmd, identity, confirmed, nil)
}

func waitClaudeCodeCommandWithReapHook(cmd *exec.Cmd, identity ProcessIdentity, confirmed func(), reap func(func() error) error) error {
	// After reaping, only an absent group is conclusive. Never signal a group
	// whose leader is no longer pinned: its identity could have been reused.
	wait := cmd.Wait
	if reap != nil {
		wait = func() error { return reap(cmd.Wait) }
	}
	return waitClaudeCodeReapedCommand(wait, func() ProcessProbeState {
		return (defaultProcessGroupOps{}).Probe(identity).State
	}, confirmed, claudeGroupConfirmTimeout)
}

func waitClaudeCodeReapedCommand(wait func() error, probe func() ProcessProbeState, confirmed func(), timeout time.Duration) error {
	err := wait()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if confirmErr := confirmClaudeCodeGroupAbsent(ctx, probe); confirmErr != nil {
		return errors.Join(errClaudeCodeGroupUnconfirmed, err, confirmErr)
	}
	if confirmed != nil {
		confirmed()
	}
	return err
}
