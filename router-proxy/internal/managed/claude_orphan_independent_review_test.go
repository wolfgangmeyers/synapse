package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Recovery must reject unexpected names even when their contents are valid.
func TestClaudeOrphanReviewUnexpectedNameBlocksRecovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claude-orphans")
	require.NoError(t, os.Mkdir(dir, 0700))
	data, err := json.Marshal(claudeCodeOrphanRecord{Version: 1, Boot: "prior-boot", Identity: ProcessIdentity{PID: 12345, StartMarker: "valid-marker", Incarnation: "test"}})
	require.NoError(t, err)
	path := filepath.Join(dir, "unexpected.json")
	require.NoError(t, os.WriteFile(path, data, 0600))
	require.Error(t, reapClaudeCodeOrphans(context.Background(), root))
	require.FileExists(t, path)
}

// A near-full ledger must not exceed its bound when launches race. The records
// deliberately need not decode: publication only counts directory entries.
func TestClaudeOrphanReviewConcurrentCapacity(t *testing.T) {
	cmd := orphanDummy(t)
	root := t.TempDir()
	dir := filepath.Join(root, "claude-orphans")
	require.NoError(t, os.Mkdir(dir, 0700))
	for i := 0; i < claudeCodeOrphanLimit-1; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("group-fill-%04d.json", i)), nil, 0600))
	}
	const launches = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < launches; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = persistClaudeCodeOrphan(root, cmd.Process.Pid, "capacity-test")
		}()
	}
	close(start)
	wg.Wait()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.LessOrEqual(t, len(entries), claudeCodeOrphanLimit)
}
