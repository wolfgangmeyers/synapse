//go:build !linux

package managed

import (
	"errors"
	"os/exec"
)

func waitClaudeCodeCommand(cmd *exec.Cmd, identity ProcessIdentity) error {
	return waitClaudeCodeCommandConfirmed(cmd, identity, nil)
}

func waitClaudeCodeCommandConfirmed(cmd *exec.Cmd, identity ProcessIdentity, confirmed func()) error {
	// After reaping, only an absent group is conclusive. Never signal a group
	// whose leader is no longer pinned: its identity could have been reused.
	err := cmd.Wait()
	if (defaultProcessGroupOps{}).Probe(identity).State != ProcessAbsent {
		return errors.Join(errClaudeCodeGroupUnconfirmed, err)
	}
	if confirmed != nil {
		confirmed()
	}
	return err
}
