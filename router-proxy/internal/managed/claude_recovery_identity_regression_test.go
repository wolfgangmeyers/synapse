package managed

import (
	"context"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// Recovery has no live parent-held group pin. Even a matching probe can race
// process exit and group-ID reuse before kill; it must not signal at all.
func TestClaudeRecoveryDoesNotSignalUnpinnedMatchingGroup(t *testing.T) {
	cmd := orphanDummy(t)
	root := t.TempDir()
	path, err := persistClaudeCodeOrphan(root, cmd.Process.Pid, "recovery-race")
	require.NoError(t, err)
	err = reapClaudeCodeOrphans(context.Background(), root)
	require.Error(t, err)
	require.NoError(t, syscall.Kill(cmd.Process.Pid, 0))
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestClaudeRecoveryWeakMarkerNeverAuthorizesSignal(t *testing.T) {
	cmd := orphanDummy(t)
	err := reapClaudeCodeOrphan(context.Background(), ProcessIdentity{PID: cmd.Process.Pid, StartMarker: "Mon Jan  2 15:04:05 2006", Incarnation: "weak"})
	require.Error(t, err)
	require.NoError(t, syscall.Kill(cmd.Process.Pid, 0))
}
