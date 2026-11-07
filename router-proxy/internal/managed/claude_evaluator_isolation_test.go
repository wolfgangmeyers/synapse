package managed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaudeEvaluatorDefaultSelectsConfiguredAlias(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := &Manager{store: db}
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: claudeCodeProvider, ID: "haiku"}))
	model, err := m.claudeCodeEvaluatorModel(ctx)
	require.NoError(t, err)
	require.Equal(t, "haiku", model.ID)
	require.Equal(t, claudeCodeProvider, string(model.Provider))
}

func TestClaudeEvaluatorProcessBoundary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	args := filepath.Join(dir, "args")
	input := filepath.Join(dir, "input")
	script := `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true}'; exit 0; fi
printf '%s\n' "$@" > "$ARGS_FILE"
cat > "$INPUT_FILE"
case "$MODE" in
 fail) exit 7;;
 overflow) head -c 70000 /dev/zero;;
 hang) exec sleep 30;;
 *) printf 'VERDICT: COMPLETED\nREASON: done\n';;
esac
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
	for _, mode := range []string{"ok", "fail", "overflow", "hang"} {
		t.Run(mode, func(t *testing.T) {
			cfg := ClaudeCodeProcessConfig{Binary: bin, Cwd: dir, Environment: append(os.Environ(), "ARGS_FILE="+args, "INPUT_FILE="+input, "MODE="+mode)}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			out, err := runClaudeGoalEvaluator(ctx, cfg, "sonnet", "bounded prompt")
			if mode != "ok" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Contains(t, out, "COMPLETED")
			b, e := os.ReadFile(args)
			require.NoError(t, e)
			require.Contains(t, string(b), "--tools\n\n")
			require.Contains(t, string(b), "--strict-mcp-config\n")
			require.NotContains(t, string(b), "--mcp-config\n")
			require.Contains(t, string(b), "--model\nsonnet\n")
			b, e = os.ReadFile(input)
			require.NoError(t, e)
			require.Equal(t, "bounded prompt", string(b))
			require.False(t, strings.Contains(string(b), "secret"))
		})
	}
}

func TestClaudeEvaluatorCancellationKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	pidPath := filepath.Join(dir, "child")
	script := `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true}'; exit 0; fi
sleep 30 &
echo $! > "$PID_FILE"
wait
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
	cfg := ClaudeCodeProcessConfig{Binary: bin, Cwd: dir, Environment: append(os.Environ(), "PID_FILE="+pidPath)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runClaudeGoalEvaluator(ctx, cfg, "sonnet", "prompt"); done <- err }()
	var data []byte
	require.Eventually(t, func() bool { data, _ = os.ReadFile(pidPath); return len(data) > 0 }, 3*time.Second, 10*time.Millisecond)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("process not reaped")
	}
	require.Eventually(t, func() bool {
		b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		return os.IsNotExist(e) || (e == nil && strings.Contains(string(b), ") Z "))
	}, 3*time.Second, 10*time.Millisecond)
}
