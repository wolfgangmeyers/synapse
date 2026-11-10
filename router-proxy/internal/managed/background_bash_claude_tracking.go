package managed

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Only Claude child commands need the existing durable Claude startup fence.
func backgroundBashCommand(command, root string) (*exec.Cmd, *os.File, error) {
	if root == "" {
		return exec.Command("bash", "-c", command), nil, nil
	}
	return claudeCodeGatedCommand("bash", []string{"-c", command})
}

func releaseTrackedBackgroundBash(gate *os.File, root string, identity ProcessIdentity) (string, error) {
	path, err := persistClaudeCodeOrphan(root, identity.PID, identity.Incarnation)
	if err != nil {
		_ = gate.Close()
		return "", fmt.Errorf("persist Claude bash orphan: %w", err)
	}
	record, err := readClaudeCodeOrphan(path)
	if err != nil || record.Identity != identity {
		_ = gate.Close()
		return path, errors.New("Claude bash orphan identity mismatch")
	}
	if _, err = gate.Write([]byte("ready\n")); err != nil {
		_ = gate.Close()
		return path, fmt.Errorf("release Claude bash gate: %w", err)
	}
	_ = gate.Close()
	return path, nil
}
