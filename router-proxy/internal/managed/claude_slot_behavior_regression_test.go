//go:build claude_capacity_test_only

package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestSlotBehaviorCancellationSettlementAndWake(t *testing.T) {
	ctx := context.Background()
	cfg := claudeTestConfig(t, `{"turns":[[{"mcp_call":{"id":"capacity-held","name":"bash","input":{"command":"echo ignored"}}},{"text":"late answer"}]]}`)
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	for _, v := range cfg.Environment {
		if strings.HasPrefix(v, "FAKE_CLAUDE_") {
			k, val, _ := strings.Cut(v, "=")
			t.Setenv(k, val)
		}
	}
	fixture := t.TempDir()
	t.Setenv("MECHA_TEST_CLAUDE_CAPACITY_DIR", fixture)
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "slot-parent")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}))
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "slot-held", ParentAgentID: parent.AgentID, Status: "running"}))
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code', selected_model_id='sonnet', cwd=? WHERE agent_id='slot-held'`, cfg.Cwd)
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer func() {
		_ = os.WriteFile(filepath.Join(fixture, "capacity-release"), []byte("release"), 0600)
		mgr.Close(ctx)
	}()
	task := &subagentTask{parent: parent, childID: "slot-held", attempt: 1, claude: true, status: "running", prompt: "hello", timeout: 30 * time.Second}
	mgr.subagentsMu.Lock()
	mgr.subagentsActive[task.childID] = task
	mgr.subagentsRegistry[task.childID] = task
	mgr.subagentsMu.Unlock()
	returned := make(chan struct{})
	go func() { defer close(returned); mgr.runSubagent(task) }()
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(fixture, "capacity-release"), []byte("release"), 0600) })
	require.Eventually(t, func() bool { _, e := os.Stat(filepath.Join(fixture, "capacity-entered")); return e == nil }, 10*time.Second, 10*time.Millisecond, "tool entered")
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-sol"}))
	admitted := make(chan struct{}, 1)
	mgr.subagentAdmissionTestHook = func() { admitted <- struct{}{} }
	queued, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{AgentID: "slot-next", Prompt: "next"})
	require.NoError(t, err)
	require.Equal(t, "queued", queued.Status)
	require.NoError(t, mgr.StopSubagent(ctx, parent.AgentID, task.childID))
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled execution did not settle and hand off scheduler release")
	}
	rec, err := db.ManagedSubagent(ctx, parent, task.childID)
	require.NoError(t, err)
	require.Equal(t, "stopped", rec.Status)
	// runSubagent's release defer has completed: this is the cancellation barrier,
	// not merely the early stopped database update from StopSubagent.
	mgr.subagentsMu.Lock()
	active := mgr.subagentsActive[task.childID]
	queueLen := len(mgr.subagentsQueue)
	mgr.subagentsMu.Unlock()
	require.Same(t, task, active)
	require.Equal(t, 1, queueLen)
	rec, err = db.ManagedSubagent(ctx, parent, "slot-next")
	require.NoError(t, err)
	require.Equal(t, "queued", rec.Status)
	select {
	case <-admitted:
		t.Fatal("queued child admitted before tool cleanup")
	default:
	}
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "capacity-release"), []byte("release"), 0600))
	select {
	case <-admitted:
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup did not admit queued child")
	}
	require.Eventually(t, func() bool {
		mgr.subagentsMu.Lock()
		defer mgr.subagentsMu.Unlock()
		return len(mgr.subagentsQueue) == 0
	}, 10*time.Second, 10*time.Millisecond, "cleanup must wake scheduler")
	require.Eventually(t, func() bool { _, e := os.Stat(filepath.Join(fixture, "capacity-returned")); return e == nil }, time.Second, 10*time.Millisecond)
	rec, err = db.ManagedSubagent(ctx, parent, task.childID)
	require.NoError(t, err)
	require.Equal(t, "stopped", rec.Status)
	var count int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id=? AND CAST(body AS TEXT) LIKE '%capacity-late-success-marker%'`, task.childID).Scan(&count))
	require.Zero(t, count)
}
