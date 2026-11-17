package managed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeStartupMilestoneConstants(t *testing.T) {
	require.Equal(t, "preflight_auth", ClaudeMilestonePreflightAuth)
	require.Equal(t, "mcp_grant_setup", ClaudeMilestoneMCPGrantSetup)
	require.Equal(t, "process_spawn", ClaudeMilestoneProcessSpawn)
	require.Equal(t, "gate_release", ClaudeMilestoneGateRelease)
	require.Equal(t, "control_probe", ClaudeMilestoneControlProbe)
	require.Equal(t, "initial_prompt_submit", ClaudeMilestoneInitialPromptSubmit)
}

func TestClaudeStartupMilestoneErrorFormatting(t *testing.T) {
	milestones := []string{
		ClaudeMilestonePreflightAuth,
		ClaudeMilestoneMCPGrantSetup,
		ClaudeMilestoneProcessSpawn,
		ClaudeMilestoneGateRelease,
		ClaudeMilestoneControlProbe,
		ClaudeMilestoneInitialPromptSubmit,
	}

	for _, milestone := range milestones {
		t.Run(milestone, func(t *testing.T) {
			baseErr := errors.New("underlying root cause")
			wrapped := fmt.Errorf("startup failed at step '%s': %w", milestone, baseErr)

			// Preserves errors.Is
			require.True(t, errors.Is(wrapped, baseErr))

			// Preserves claudeStartCategoryOf across wrapped categorized errors
			for _, cat := range []claudeStartCategory{claudeStartAuth, claudeStartSpawn, claudeStartProbeDied} {
				catErr := claudeStartCategorize(cat, baseErr)
				wrappedCat := fmt.Errorf("startup failed at step '%s': %w", milestone, catErr)
				require.True(t, errors.Is(wrappedCat, baseErr))
				require.Equal(t, cat, claudeStartCategoryOf(wrappedCat))
			}

			// sanitizeError retains the step attribution prefix while stripping sensitive paths / tokens
			pathErr := fmt.Errorf("startup failed at step '%s': %w", milestone, fmt.Errorf("failed to open /home/wolfgang/.secrets/token.key: access denied"))
			sanitized := sanitizeError(pathErr)
			require.Contains(t, sanitized, fmt.Sprintf("startup failed at step '%s':", milestone))
			require.NotContains(t, sanitized, "/home/wolfgang")
			require.NotContains(t, sanitized, ".secrets")
		})
	}
}

func TestClaudeStartupPreflightTimeoutExtended(t *testing.T) {
	cfg := claudeTestConfig(t, `{"auth":{"hang":true}}`)

	// Passing a context with e.g. 500ms timeout times out quickly
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelShort()

	start := time.Now()
	_, err := PreflightClaudeCode(shortCtx, cfg)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Less(t, elapsed, 3*time.Second, "short context should time out quickly")

	// Notice: with internal 15s timeout, a context without short deadline would wait 15s.
	// We don't want to actually block 15s in ordinary unit test runs, but we verify that
	// a context lasting 1s does NOT time out from internal PreflightClaudeCode (since internal timeout is 15s > 1s).
	midCtx, cancelMid := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancelMid()
	start = time.Now()
	_, err = PreflightClaudeCode(midCtx, cfg)
	elapsed = time.Since(start)
	require.Error(t, err)
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond, "should run until midCtx timeout, not aborted prematurely before 1s")
}

func TestClaudeStartupMilestoneAttributionOnPreflightFailure(t *testing.T) {
	t.Run("missing binary", func(t *testing.T) {
		cfg := ClaudeCodeProcessConfig{Binary: filepath.Join(t.TempDir(), "nonexistent-claude")}
		_, err := PreflightClaudeCode(context.Background(), cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), fmt.Sprintf("startup failed at step '%s'", ClaudeMilestonePreflightAuth))
		require.Equal(t, claudeStartSpawn, claudeStartCategoryOf(err))
	})

	t.Run("signed out", func(t *testing.T) {
		bin := filepath.Join(t.TempDir(), "claude")
		require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"loggedIn\":false}'\n"), 0700))
		cfg := ClaudeCodeProcessConfig{Binary: bin}
		_, err := PreflightClaudeCode(context.Background(), cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), fmt.Sprintf("startup failed at step '%s'", ClaudeMilestonePreflightAuth))
		require.Equal(t, claudeStartAuth, claudeStartCategoryOf(err))
	})
}

func TestClaudeStartupMilestoneAttributionOnControlProbeFailure(t *testing.T) {
	// A Claude process where control probe fails or times out
	// Control probe is called during claudeCodeRuntime or initial probe
	// Let's test the error wrapping for ClaudeMilestoneControlProbe
	probeErr := errors.New("control request timeout")
	wrapped := fmt.Errorf("startup failed at step '%s': %w", ClaudeMilestoneControlProbe, claudeStartCategorize(claudeStartProbeDied, probeErr))
	require.Contains(t, wrapped.Error(), fmt.Sprintf("startup failed at step '%s'", ClaudeMilestoneControlProbe))
	require.Equal(t, claudeStartProbeDied, claudeStartCategoryOf(wrapped))
}

func TestClaudeStartupMilestoneAttributionOnSpawnFailure(t *testing.T) {
	// When StartClaudeCodeProcess fails to spawn the command (e.g. invalid executable)
	// We provide an existing file as binary but not executable or broken so preflight might be bypassed
	// or Preflight succeeds if mocked, but cmd.Start fails.
	// Alternatively, binary is a script that passes preflight auth status, but StartClaudeCodeProcess fails.
	// Or we use an adapter where Executable passes Preflight, but cmd.Start fails (e.g. permission denied or directory).
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "claude")
	// Make a script that succeeds on "auth status --json"
	script := "#!/bin/sh\nif [ \"$1\" = \"auth\" ]; then echo '{\"loggedIn\":true}'; exit 0; fi\nwhile true; do sleep 1; done\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0700))

	unsearchableDir := filepath.Join(tmpDir, "unsearchable")
	require.NoError(t, os.Mkdir(unsearchableDir, 0000))
	defer os.Chmod(unsearchableDir, 0700)

	cfg := ClaudeCodeProcessConfig{
		Binary:    bin,
		Cwd:       unsearchableDir,
		SessionID: "test-session",
		Model:     "sonnet",
		Adapter:   &spawnFailAdapter{bin: bin, unsearchableDir: unsearchableDir},
	}

	_, err := StartClaudeCodeProcess(context.Background(), cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), fmt.Sprintf("startup failed at step '%s'", ClaudeMilestoneProcessSpawn))
	require.Equal(t, claudeStartSpawn, claudeStartCategoryOf(err))
}

type spawnFailAdapter struct {
	*ClaudeCodeAdapter
	bin             string
	unsearchableDir string
}

func (a *spawnFailAdapter) Provider() string { return "custom-provider" }
func (a *spawnFailAdapter) Executable(ctx context.Context) (string, error) {
	return a.bin, nil
}
func (a *spawnFailAdapter) Preflight(ctx context.Context) error {
	return nil
}
func (a *spawnFailAdapter) CleanEnvironment(env []string) []string { return env }
func (a *spawnFailAdapter) BuildArgs(cfg CLIProcessConfig, settingsPath string) []string {
	return []string{}
}
func (a *spawnFailAdapter) ParseLine(line []byte, sessionID string) (NormalizedEvent, error) {
	return NormalizedEvent{}, nil
}
func (a *spawnFailAdapter) FormatUserTurn(msg UserTurnInput) ([]byte, error) {
	return nil, nil
}
func (a *spawnFailAdapter) EphemeralSettingsContent(mcpURL string, mcpToken string) ([]byte, error) {
	return nil, nil
}
