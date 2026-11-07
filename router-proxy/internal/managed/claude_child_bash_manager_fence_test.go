package managed

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeChildToolUnconfirmedFencesManagerAndRetainsSlot(t *testing.T) {
	m := &Manager{}
	task := &subagentTask{}
	require.False(t, m.fenceClaudeChildToolError(task, errors.New("ordinary tool failure")))
	release, err := m.claudeCodeProcesses.admitExternal()
	require.NoError(t, err)
	release()
	require.False(t, task.retainSlot)

	require.True(t, m.fenceClaudeChildToolError(task, fmt.Errorf("bash: %w", errClaudeCodeGroupUnconfirmed)))
	require.True(t, task.retainSlot)
	_, err = m.claudeCodeProcesses.admitExternal()
	require.Error(t, err)
}
