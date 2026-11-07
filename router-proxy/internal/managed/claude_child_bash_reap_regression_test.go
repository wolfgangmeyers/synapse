package managed

import (
	"context"
	"database/sql"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeChildBashReapsBackgroundDescendantBeforeSuccess(t *testing.T) {
	dir := t.TempDir()
	_, err := executeSubagentBashOwned(context.Background(), dir, `(sleep 1; echo unsafe > escaped) >/dev/null 2>&1 &`, 5*time.Second, "")
	require.NoError(t, err)
	time.Sleep(1500 * time.Millisecond)
	_, err = os.Stat(filepath.Join(dir, "escaped"))
	require.True(t, os.IsNotExist(err), "background descendant survived checked result")
}

func TestClaudeChildPreflightFailureIsActionableAndCannotResume(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "preflight-parent")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id=?`, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}))
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "preflight-child", ParentAgentID: parent.AgentID, Status: "running"}))
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id='preflight-child'`)
	require.NoError(t, err)
	t.Setenv("MECHA_CLAUDE_BIN", filepath.Join(t.TempDir(), "missing-claude"))
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	task := &subagentTask{parent: parent, childID: "preflight-child", attempt: 1, status: "running", prompt: "hello", timeout: 5 * time.Second}
	mgr.runSubagent(task)
	rec, err := db.ManagedSubagent(ctx, parent, task.childID)
	require.NoError(t, err)
	require.Equal(t, "failed", rec.Status)
	require.Contains(t, rec.Error, "install Claude CLI and sign in on the host")
	_, err = db.ResumeManagedSubagent(ctx, parent, task.childID, "retry")
	require.ErrorContains(t, err, "one attempt")
	require.ErrorContains(t, err, "spawn a new child")
	after, err := db.ManagedSubagent(ctx, parent, task.childID)
	require.NoError(t, err)
	require.Equal(t, rec.Status, after.Status)
	require.Equal(t, rec.Attempt, after.Attempt)
}

func TestClaudeChildOwnedBashPersistsOrphanUntilConfirmedGone(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := executeSubagentBashOwned(context.Background(), dir, `sleep 30`, time.Minute, root)
		done <- err
	}()
	var entries []os.DirEntry
	require.Eventually(t, func() bool { entries, _ = os.ReadDir(filepath.Join(root, "claude-orphans")); return len(entries) == 1 }, 3*time.Second, 10*time.Millisecond)
	rec, err := readClaudeCodeOrphan(filepath.Join(root, "claude-orphans", entries[0].Name()))
	require.NoError(t, err)
	require.NoError(t, (defaultProcessGroupOps{}).SignalVerifiedProcessGroup(rec.Identity, syscall.SIGKILL))
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("bash did not stop")
	}
	entries, err = os.ReadDir(filepath.Join(root, "claude-orphans"))
	require.NoError(t, err)
	require.Empty(t, entries)
}
