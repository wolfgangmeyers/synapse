//go:build linux

package managed

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeShortCommandCleansInheritedPipeBeforeReturning(t *testing.T) {
	for _, phase := range []string{"preflight", "evaluator"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "claude")
			pidfile := filepath.Join(dir, "child")
			script := `#!/bin/sh
if [ "$1" = auth ]; then
  if [ "$PHASE" = preflight ]; then sleep 30 & echo $! > "$PID_FILE"; fi
  echo '{"loggedIn":true}'
  exit 0
fi
sleep 30 & echo $! > "$PID_FILE"
echo 'VERDICT: COMPLETED'
`
			require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
			cfg := ClaudeCodeProcessConfig{Binary: bin, Cwd: dir, Environment: append(os.Environ(), "PHASE="+phase, "PID_FILE="+pidfile)}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if phase == "preflight" {
				_, err := PreflightClaudeCode(ctx, cfg)
				require.NoError(t, err)
			} else {
				_, err := runClaudeGoalEvaluator(ctx, cfg, "sonnet", "x")
				require.NoError(t, err)
			}
			data, err := os.ReadFile(pidfile)
			require.NoError(t, err)
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			require.NoError(t, err)
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			require.True(t, os.IsNotExist(err) || (err == nil && strings.Contains(string(stat), ") Z ")), "descendant still executing: %s %v", stat, err)
		})
	}
}

func TestClaudeShortCommandRetainsRecoveryOnUnconfirmedCleanup(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"loggedIn\":true}'\n"), 0700))
	fenced := false
	cfg := ClaudeCodeProcessConfig{Binary: bin, Cwd: dir, OrphanRoot: dir, OnUnconfirmedStop: func() { fenced = true }}
	cfg.waitForCleanup = func(cmd *exec.Cmd, id ProcessIdentity, confirmed func()) error {
		_ = waitClaudeCodeCommandConfirmed(cmd, id, nil)
		return errClaudeCodeGroupUnconfirmed
	}
	_, err := PreflightClaudeCode(context.Background(), cfg)
	require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
	require.True(t, fenced)
	entries, err := os.ReadDir(filepath.Join(dir, "claude-orphans"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
