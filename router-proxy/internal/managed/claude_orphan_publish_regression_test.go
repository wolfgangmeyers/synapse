package managed

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeOrphanInterruptedTempRecovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claude-orphans")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".orphan-tmp-interrupted"), []byte(`{"Version":`), 0600))
	require.NoError(t, reapClaudeCodeOrphans(context.Background(), root))
	_, err := os.Stat(filepath.Join(dir, ".orphan-tmp-interrupted"))
	require.True(t, os.IsNotExist(err))
}

func TestClaudeOrphanUntrustedTempBlocks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claude-orphans")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(dir, ".orphan-tmp-bad")))
	require.Error(t, reapClaudeCodeOrphans(context.Background(), root))
}
