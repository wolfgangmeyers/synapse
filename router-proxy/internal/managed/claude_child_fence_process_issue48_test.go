package managed

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exercise the child-specific production callback and release gate with real
// subprocesses. Only the inability to confirm cleanup is injected: the test
// still kills and reaps the first process so it cannot leak.
func TestClaudeChildUnconfirmedProcessFencesOnlyChildAndRetainsSlot(t *testing.T) {
	var m Manager
	task := &subagentTask{childID: "child-a", attempt: 1, claude: true}
	m.subagentsActive = map[string]*subagentTask{task.childID: task}
	cfg := claudeTestConfig(t, `{"turns":[]}`)
	cfg.OrphanRoot = t.TempDir()
	m.fenceClaudeChildTaskConfig(&cfg, task)
	admissionFailure := errors.New("test admission failure")
	cfg.BeforeRelease = func() error { return admissionFailure }
	calls := 0
	cfg.waitForCleanup = func(cmd *exec.Cmd, id ProcessIdentity, confirmed func()) error {
		calls++
		if calls == 1 {
			return waitClaudeCodeCommandConfirmed(cmd, id, confirmed)
		}
		err := waitClaudeCodeCommandConfirmed(cmd, id, nil)
		return errors.Join(err, errClaudeCodeGroupUnconfirmed)
	}

	proc, err := StartClaudeCodeProcess(context.Background(), cfg)
	require.Nil(t, proc)
	require.ErrorIs(t, err, admissionFailure)
	require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
	m.releaseSubagentSlot(task)
	require.Same(t, task, m.subagentsActive[task.childID], "unconfirmed child must retain its capacity slot")
	_, err = m.claudeCodeProcesses.admitWorker(task.childID)
	require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)

	// Starting another real gated process for the same ID must not write the
	// release token; a different child must still be admitted and start.
	retry := claudeTestConfig(t, `{"turns":[]}`)
	m.fenceClaudeChildTaskConfig(&retry, task)
	released := false
	retry.writeRelease = func(f *os.File) (int, error) {
		released = true
		return f.Write([]byte("ready\n"))
	}
	proc, err = StartClaudeCodeProcess(context.Background(), retry)
	require.Nil(t, proc)
	require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
	require.False(t, released, "fenced child crossed its release gate")

	other := &subagentTask{childID: "child-b", claude: true}
	release, err := m.claudeCodeProcesses.admitWorker(other.childID)
	require.NoError(t, err)
	defer release()
	otherCfg := claudeTestConfig(t, `{"turns":[]}`)
	m.fenceClaudeChildTaskConfig(&otherCfg, other)
	proc, err = StartClaudeCodeProcess(context.Background(), otherCfg)
	require.NoError(t, err)
	require.NoError(t, proc.Close())
}
