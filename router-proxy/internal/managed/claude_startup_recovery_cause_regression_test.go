package managed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaudeStartupRecoveryReportsSafeFilesystemCause(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	secret := filepath.Join(root, "sensitive-token-file")
	require.NoError(t, os.WriteFile(secret, []byte("private"), 0600))
	var events []Event
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: secret, AllowedWorkingDirectoryRoots: []string{root}}, EventSink: func(e Event) { events = append(events, e) }})
	require.NoError(t, err)
	_, err = m.claudeCodeProcesses.admitExternal()
	require.Error(t, err)
	require.NotEmpty(t, events)
	require.Contains(t, events[0].ErrorText, "not a directory")
	require.NotContains(t, events[0].ErrorText, "sensitive-token-file")
	err = m.Close(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a directory")
	require.NotContains(t, err.Error(), "sensitive-token-file")
	require.NotErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
}

func TestSafeClaudeRecoveryErrorDoesNotLeakUntrustedDetails(t *testing.T) {
	for _, err := range []error{
		errors.New("secret-record-content"),
		fmt.Errorf("secret-filename: %w", syscall.EACCES),
		errors.Join(errors.New("secret-record-content"), fmt.Errorf("secret-path: %w", syscall.ENOTDIR)),
	} {
		safe := safeClaudeRecoveryError(err).Error()
		require.NotContains(t, safe, "secret")
	}
	require.Contains(t, safeClaudeRecoveryError(fmt.Errorf("private: %w", syscall.EACCES)).Error(), "permission denied")
}
