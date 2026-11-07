package managed

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChildBashOrphanCleanupFailureIsUnconfirmed(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		timeout       time.Duration
	}{
		{"normal", "true", time.Second},
		{"timeout", "sleep 30", 50 * time.Millisecond},
		{"overflow", "yes x", 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := executeSubagentBashWithWait(context.Background(), t.TempDir(), tc.command, tc.timeout, root, func(cmd *exec.Cmd, id ProcessIdentity, confirmed func()) error {
				return waitClaudeCodeCommandConfirmed(cmd, id, func() {
					entries, e := os.ReadDir(filepath.Join(root, "claude-orphans"))
					if e == nil && len(entries) == 1 {
						e = os.Remove(filepath.Join(root, "claude-orphans", entries[0].Name()))
					}
					if e != nil || len(entries) != 1 {
						t.Errorf("fault setup: entries=%d error=%v", len(entries), e)
					}
					confirmed()
				})
			})
			require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
		})
	}
}
