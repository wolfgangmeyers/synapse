package managed

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChildBashUnverifiedWaitNeverReturnsCheckedResult(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		timeout       time.Duration
	}{
		{"normal", "true", time.Second},
		{"timeout", "sleep 30", 50 * time.Millisecond},
		{"overflow", "yes x", 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			injected := errors.New("unverified wait failure")
			_, err := executeSubagentBashWithWait(context.Background(), t.TempDir(), tc.command, tc.timeout, "", func(cmd *exec.Cmd, id ProcessIdentity, confirmed func()) error {
				_ = waitClaudeCodeCommandConfirmed(cmd, id, confirmed)
				return injected
			})
			require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
		})
	}
}
