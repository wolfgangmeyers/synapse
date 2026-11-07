package managed

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A matching live orphan is cleaned up during startup.
func TestRequestedLiveOrphanRecovery(t *testing.T) {
	cmd := orphanDummy(t)
	root := t.TempDir()
	path, err := persistClaudeCodeOrphan(root, cmd.Process.Pid, "recovery-red")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, reapClaudeCodeOrphans(ctx, root), "verified live orphan should be killed and confirmed absent")
	require.NoFileExists(t, path)
}
