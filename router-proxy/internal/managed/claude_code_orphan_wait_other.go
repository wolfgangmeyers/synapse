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
	// After reaping, only an absent group is conclusive. Never signal a group
	// whose leader is no longer pinned: its identity could have been reused.
	return waitClaudeCodeReapedCommand(cmd.Wait, func() ProcessProbeState {
		return (defaultProcessGroupOps{}).Probe(identity).State
	}, confirmed, 2*time.Second)
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
