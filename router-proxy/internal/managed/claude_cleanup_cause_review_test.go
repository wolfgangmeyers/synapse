package managed

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudePreflightReportsRecordRemovalFailure(t *testing.T) {
	cfg := claudeTestConfig(t, `{}`)
	cfg.OrphanRoot = t.TempDir()
	var fenced error
	cfg.OnUnconfirmedStop = func(err error) { fenced = err }
	cfg.waitForCleanup = func(cmd *exec.Cmd, id ProcessIdentity, confirmed func()) error {
		return waitClaudeCodeCommandConfirmed(cmd, id, func() {
			entries, err := os.ReadDir(filepath.Join(cfg.OrphanRoot, "claude-orphans"))
			if err != nil || len(entries) != 1 {
				t.Errorf("records: %v %v", entries, err)
				return
			}
			if err := os.Remove(filepath.Join(cfg.OrphanRoot, "claude-orphans", entries[0].Name())); err != nil {
				t.Error(err)
			}
			confirmed()
		})
	}
	_, err := PreflightClaudeCode(context.Background(), cfg)
	if err == nil || fenced == nil || !errors.Is(err, os.ErrNotExist) || !errors.Is(fenced, os.ErrNotExist) {
		t.Fatalf("error=%v fence=%v", err, fenced)
	}
	if errors.Is(err, errClaudeCodeGroupUnconfirmed) || errors.Is(fenced, errClaudeCodeGroupUnconfirmed) {
		t.Fatalf("incorrect group uncertainty: %v / %v", err, fenced)
	}
}

func TestClaudeConfirmedOrphanRemovalFailurePreservesFenceCause(t *testing.T) {
	var pool claudeCodeProcessPool
	p := &ClaudeCodeProcess{orphanPath: filepath.Join(t.TempDir(), "missing"), orphanIdentity: ProcessIdentity{PID: 123}, onUnconfirmedStop: pool.fenceStartup}
	p.forgetClaudeOrphanConfirmed()
	_, err := pool.admitExternal()
	if !errors.Is(err, os.ErrNotExist) || errors.Is(err, errClaudeCodeGroupUnconfirmed) {
		t.Fatalf("fence cause: %v", err)
	}
}

func TestClaudePreflightPersistenceFailureFencesActualCause(t *testing.T) {
	cfg := claudeTestConfig(t, `{}`)
	cfg.OrphanRoot = filepath.Join(t.TempDir(), "private-file")
	if err := os.WriteFile(cfg.OrphanRoot, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	var pool claudeCodeProcessPool
	cfg.OnUnconfirmedStop = pool.fenceStartup
	_, err := PreflightClaudeCode(context.Background(), cfg)
	var cleanup *claudeStartCleanupError
	if !errors.As(err, &cleanup) || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("preflight: %v", err)
	}
	_, fenced := pool.admitExternal()
	if !errors.Is(fenced, syscall.ENOTDIR) || errors.Is(fenced, errClaudeCodeGroupUnconfirmed) {
		t.Fatalf("fence: %v", fenced)
	}
}

func TestClaudeLaunchPreflightPersistenceFailureReportsFence(t *testing.T) {
	ctx := context.Background()
	db, b, _ := completionFixture(t)
	saved, err := db.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	cfg := claudeTestConfig(t, `{}`)
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	root := t.TempDir()
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: root, AllowedWorkingDirectoryRoots: []string{saved.Cwd}}, CatalogSource: StoreCatalogSource{Store: db}})
	require.NoError(t, err)
	defer m.Close(ctx)
	// Break persistence after startup recovery has completed.
	require.NoError(t, os.RemoveAll(filepath.Join(root, "claude-orphans")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "claude-orphans"), []byte("x"), 0600))
	request := LaunchRequest{Name: "fence-test", Cwd: saved.Cwd, Runtime: "embedded", Model: &Model{Provider: "claude-code", ID: "sonnet"}}
	for i := 0; i < 2; i++ {
		_, err = m.Launch(ctx, request)
		var fenced *ClaudeAdmissionFencedError
		require.ErrorAs(t, err, &fenced)
		require.ErrorIs(t, err, syscall.ENOTDIR)
		require.NotErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
		require.NotContains(t, err.Error(), "sign in")
		require.NotContains(t, err.Error(), root)
	}
}

func TestClaudeProcessPersistenceFailureFencesActualCause(t *testing.T) {
	cfg := claudeTestConfig(t, `{}`)
	cfg.OrphanRoot = t.TempDir()
	var pool claudeCodeProcessPool
	cfg.OnUnconfirmedStop = pool.fenceStartup
	calls := 0
	cfg.releaseGate = func(release func() (int, error)) (int, error) {
		calls++
		return release()
	}
	// Preflight removes its record after release; replace the directory before
	// the subsequent process attempts to persist its own identity.
	cfg.waitForCleanup = func(cmd *exec.Cmd, id ProcessIdentity, confirmed func()) error {
		err := waitClaudeCodeCommandConfirmed(cmd, id, confirmed)
		dir := filepath.Join(cfg.OrphanRoot, "claude-orphans")
		require.NoError(t, os.Remove(dir))
		require.NoError(t, os.WriteFile(dir, []byte("x"), 0600))
		return err
	}
	_, err := StartClaudeCodeProcess(context.Background(), cfg)
	require.ErrorIs(t, err, syscall.ENOTDIR)
	require.Equal(t, 1, calls)
	_, fenced := pool.admitExternal()
	require.ErrorIs(t, fenced, syscall.ENOTDIR)
	require.NotErrorIs(t, fenced, errClaudeCodeGroupUnconfirmed)
}
