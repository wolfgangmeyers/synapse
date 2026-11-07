package managed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestClaudeEarlyCleanupFailureRetainsExecution(t *testing.T) {
	for _, path := range []string{"admission", "preflight"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			r, p, pool, _ := claudeRuntimeFixture(t, `{}`)
			token, err := p.db.EnterEmbeddedSDKExecution(ctx, p.binding, nil)
			require.NoError(t, err)
			proc := cleanupTestProcess(poolTestStdin{})
			proc.waitErr = errClaudeCodeGroupUnconfirmed
			revoked := 0
			pool.entries = map[string]*claudeCodeProcessEntry{p.binding.AgentID: {binding: p.binding, process: proc, revoke: func() { revoked++ }}}
			if path == "admission" {
				r.beforeSubmit = func(context.Context, int) error { return errors.New("admission denied") }
			} else {
				r.config.Binary = "/nonexistent/claude"
			}
			_, err = r.Run(ctx, "input")
			require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
			require.Equal(t, 1, revoked)
			require.True(t, pool.closed)
			closed := make(chan error, 1)
			go func() { closed <- r.closeScoped() }()
			require.ErrorIs(t, waitPoolShutdownError(t, closed, "scoped close"), errClaudeCodeGroupUnconfirmed)
			// The real settlement guard must not clear a still-owned execution.
			go func() { closed <- r.closeScoped() }()
			if waitPoolShutdownError(t, closed, "repeated scoped close") == nil {
				require.NoError(t, p.db.ClearEmbeddedExecution(ctx, p.binding, token))
			}
			_, err = p.db.EnterEmbeddedSDKExecution(ctx, p.binding, nil)
			require.Error(t, err)
		})
	}
}

func TestClaudeManagerPreflightFailureKeepsDurableExecution(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	saved.SelectedModel.Provider = claudeCodeProvider
	saved.SelectedModel.ID = "sonnet"
	proc := cleanupTestProcess(poolTestStdin{})
	proc.waitErr = errClaudeCodeGroupUnconfirmed
	revoked := 0
	pool := &f.m.claudeCodeProcesses
	pool.entries = map[string]*claudeCodeProcessEntry{owner.binding.AgentID: {binding: owner.binding, process: proc, revoke: func() { revoked++ }}}
	t.Cleanup(func() { pool.mu.Lock(); pool.entries = nil; pool.closed = false; pool.mu.Unlock() })
	err = f.m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "input")
	require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
	require.Equal(t, 1, revoked)
	require.True(t, pool.closed)
	var execution string
	require.NoError(t, f.raw.QueryRow(`SELECT execution_json FROM embedded_sessions WHERE agent_id=?`, owner.binding.AgentID).Scan(&execution))
	require.NotEmpty(t, execution)
	_, err = f.db.EnterEmbeddedSDKExecution(ctx, owner.binding, nil)
	require.ErrorContains(t, err, "already active")
}

// Hold the real stdin close past the stop deadline while leaving process startup,
// control traffic, signalling and reaping on their real boundaries.
type probeBlockedClose struct {
	io.WriteCloser
	release <-chan struct{}
}

func (w probeBlockedClose) Close() error {
	<-w.release
	return w.WriteCloser.Close()
}

func TestClaudeRealProbeUnconfirmedStopForbidsFallback(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprint(resumed), func(t *testing.T) {
			r, p, pool, _ := claudeRuntimeFixture(t, `{}`)
			dir := t.TempDir()
			entered, release, starts := filepath.Join(dir, "entered"), filepath.Join(dir, "release"), filepath.Join(dir, "starts")
			binary := filepath.Join(dir, "claude")
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true}'; exit 0; fi
echo start >> %q
read line
echo 'No conversation found with session ID: missing' >&2
touch %q
while [ ! -e %q ]; do sleep 0.01; done
echo '{"type":"control_response","response":{"subtype":"error","request_id":"mecha-1","error":"probe failed"}}'
while read line; do :; done
`, starts, entered, release)
			require.NoError(t, os.WriteFile(binary, []byte(script), 0700))
			r.config.Binary = binary
			if resumed {
				require.NoError(t, p.db.MarkClaudeBindingStarted(context.Background(), p.binding))
			}
			revoked := 0
			r.newGrant = func() (claudeCodeProcessGrant, error) {
				return claudeCodeProcessGrant{Revoke: func() { revoked++ }}, nil
			}
			closeRelease := make(chan struct{})
			defer close(closeRelease)
			pool.afterStart = func(proc *ClaudeCodeProcess) {
				proc.stdin = probeBlockedClose{WriteCloser: proc.stdin, release: closeRelease}
			}
			result := make(chan error, 1)
			go func() { _, err := r.Run(context.Background(), "input"); result <- err }()
			require.Eventually(t, func() bool { _, err := os.Stat(entered); return err == nil }, 5*time.Second, 10*time.Millisecond)
			require.NoError(t, os.WriteFile(release, nil, 0600))
			select {
			case err := <-result:
				require.ErrorContains(t, err, "Claude process stop unconfirmed")
			case <-time.After(8 * time.Second):
				t.Fatal("probe did not finish")
			}
			closed := make(chan error, 1)
			go func() { closed <- r.closeScoped() }()
			require.ErrorContains(t, waitPoolShutdownError(t, closed, "scoped close"), "Claude process stop unconfirmed")
			require.Equal(t, 1, revoked)
			require.True(t, pool.closed)
			data, err := os.ReadFile(starts)
			require.NoError(t, err)
			require.Equal(t, "start\n", string(data))
			snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
			require.NoError(t, err)
			require.Empty(t, snap.Entries)
		})
	}
}

// Exercise the manager's actual Claude wake path through a checked final and
// failed process cleanup; a final alone must not settle the claimed wake.
func TestClaudeManagerClaimedWakeUnconfirmedCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := claudeTestConfig(t, `{"turns":[[{"text":"wake final"}]]}`)
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	t.Setenv("FAKE_CLAUDE_SCRIPT", filepath.Join(cfg.Cwd, "script.json"))
	t.Setenv("FAKE_CLAUDE_RECORD", filepath.Join(cfg.Cwd, "record.jsonl"))
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	f.m.config.RouterURL = "http://127.0.0.1:8765"
	saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	saved.SelectedModel.Provider = claudeCodeProvider
	saved.SelectedModel.ID = "sonnet"
	g, err := f.db.MintEmbeddedProcessGeneration(ctx, "wake-manager-process")
	require.NoError(t, err)
	_, err = f.db.ReserveEmbeddedRestartWake(ctx, owner.binding, g, "wake-manager", "root-idle", "projection")
	require.NoError(t, err)
	require.NoError(t, f.db.ClaimEmbeddedRestartWake(ctx, owner.binding, g, "wake-manager"))
	scope := embeddedClaimedWakeScope{binding: owner.binding, processGeneration: g, id: "wake-manager", kind: "root-idle"}
	release := make(chan struct{})
	defer close(release)
	pool := &f.m.claudeCodeProcesses
	pool.afterStart = func(proc *ClaudeCodeProcess) {
		proc.stdin = probeBlockedClose{WriteCloser: proc.stdin, release: release}
	}
	result := make(chan error, 1)
	go func() {
		result <- f.m.runEmbeddedClaimedWake(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, scope, []agent.AgentMessage{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "wake"}}}})
	}()
	require.ErrorContains(t, waitPoolShutdownError(t, result, "claimed wake"), "unconfirmed")
	snap, err := f.db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	require.NotEmpty(t, snap.Entries)
	require.Equal(t, "assistant", snap.Entries[len(snap.Entries)-1].Kind)
	wake, err := f.db.EmbeddedRestartWake(ctx, owner.binding, g, scope.id)
	require.NoError(t, err)
	require.Equal(t, "claimed", wake.State)
	// The fixture manager's shutdown reports this deliberately injected failure.
	pool.mu.Lock()
	fenceErr := pool.startErr
	pool.entries = nil
	pool.closed = false
	pool.startErr = nil
	pool.mu.Unlock()
	require.ErrorIs(t, fenceErr, errClaudeCodeGroupUnconfirmed)
}
