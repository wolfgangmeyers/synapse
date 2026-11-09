package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestClaudeChildResumeIsBlockedBeforeAttemptChanges(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "resume-guard-parent")
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "resume-guard-child", ParentAgentID: parent.AgentID, Status: "running"}))
	require.NoError(t, db.UpdateManagedSubagentAttempt(ctx, parent, "resume-guard-child", 1, "running", store.ManagedSubagentResult{Status: "failed"}))
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id='resume-guard-child'`)
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{Resume: "resume-guard-child", Prompt: "retry"})
	require.ErrorContains(t, err, "Claude child resume")
	rec, err := db.ManagedSubagent(ctx, parent, "resume-guard-child")
	require.NoError(t, err)
	require.Equal(t, 1, rec.Attempt)
	require.Equal(t, "failed", rec.Status)
}

func TestClaudeChildCheckedOutcomeStillCannotResume(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "checked-resume-parent")
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "checked-resume-child", ParentAgentID: parent.AgentID, Status: "running"}))
	require.NoError(t, db.UpdateManagedSubagentAttempt(ctx, parent, "checked-resume-child", 1, "running", store.ManagedSubagentResult{Status: "completed", ResultText: "done"}))
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id='checked-resume-child'`)
	require.NoError(t, err)
	_, err = db.ResumeManagedSubagent(ctx, parent, "checked-resume-child", "continue")
	require.ErrorContains(t, err, "one attempt")
	rec, err := db.ManagedSubagent(ctx, parent, "checked-resume-child")
	require.NoError(t, err)
	require.Equal(t, 1, rec.Attempt)
	require.Equal(t, "completed", rec.Status)
	require.Equal(t, "done", rec.ResultText)
}

func TestClaudeChildCleanupClosesProcessBeforeBlockedTool(t *testing.T) {
	turn := &claudeChildTurn{changed: make(chan struct{}), calls: map[string]*claudeChildCall{}}
	turn.serial.Lock()
	closed := make(chan struct{})
	done := make(chan struct{})
	go func() { cleanupClaudeChildTurn(turn, func() error { close(closed); return nil }); close(done) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("process close blocked by tool")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup blocked by tool")
	}
	turn.serial.Unlock()
}

func TestClaudeChildSoftTurnLimitSendsWrapUp(t *testing.T) {
	cfg := claudeTestConfig(t, `{"turns":[[{"mcp_call":{"id":"read-1","name":"read","input":{"path":"note.txt"}}},{"hang":true}]]}`)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "note.txt"), []byte("hello"), 0600))
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	for _, v := range cfg.Environment {
		if strings.HasPrefix(v, "FAKE_CLAUDE_") {
			k, val, _ := strings.Cut(v, "=")
			t.Setenv(k, val)
		}
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "soft-parent")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET cwd=? WHERE agent_id=?`, cfg.Cwd, parent.AgentID)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id=?`, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}))
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "soft-child", ParentAgentID: parent.AgentID, Status: "running"}))
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id='soft-child'`)
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	task := &subagentTask{parent: parent, childID: "soft-child", attempt: 1, status: "running", prompt: "read", maxTurns: 1, timeout: 3 * time.Second}
	mgr.subagentsMu.Lock()
	mgr.subagentsActive[task.childID] = task
	mgr.subagentsRegistry[task.childID] = task
	mgr.subagentsMu.Unlock()
	go mgr.runSubagent(task)
	require.Eventually(t, func() bool { task.mu.Lock(); defer task.mu.Unlock(); return task.wrapUpSteered }, 2*time.Second, 10*time.Millisecond)
}

func TestClaudeChildResolvedCallsDoNotExhaustCapacity(t *testing.T) {
	steps := make([]map[string]any, 140)
	for i := range steps {
		steps[i] = map[string]any{"mcp_call": map[string]any{"id": fmt.Sprintf("read-%d", i), "name": "read", "input": map[string]any{"path": "note.txt"}}}
	}
	steps = append(steps, map[string]any{"text": "done"})
	script, err := json.Marshal(map[string]any{"turns": []any{steps}})
	require.NoError(t, err)
	cfg := claudeTestConfig(t, string(script))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "note.txt"), []byte("hello"), 0600))
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	for _, v := range cfg.Environment {
		if strings.HasPrefix(v, "FAKE_CLAUDE_") {
			k, val, _ := strings.Cut(v, "=")
			t.Setenv(k, val)
		}
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "calls-parent")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET cwd=? WHERE agent_id=?`, cfg.Cwd, parent.AgentID)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id=?`, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}))
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "calls-child", ParentAgentID: parent.AgentID, Status: "running"}))
	_, err = raw.Exec(`UPDATE managed_agents SET selected_model_provider='claude-code',selected_model_id='sonnet' WHERE agent_id='calls-child'`)
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	task := &subagentTask{parent: parent, childID: "calls-child", attempt: 1, status: "running", prompt: "read", timeout: 30 * time.Second}
	mgr.subagentsMu.Lock()
	mgr.subagentsActive[task.childID] = task
	mgr.subagentsRegistry[task.childID] = task
	mgr.subagentsMu.Unlock()
	go mgr.runSubagent(task)
	require.Eventually(t, func() bool {
		rec, e := db.ManagedSubagent(ctx, parent, task.childID)
		return e == nil && IsTerminalSubagentStatus(rec.Status)
	}, 25*time.Second, 20*time.Millisecond)
	rec, err := db.ManagedSubagent(ctx, parent, task.childID)
	require.NoError(t, err)
	require.Equal(t, "completed", rec.Status, rec.Error)
	require.Equal(t, 140, rec.ToolUseCount)
}

func TestClaudeChildSerialWaitHonorsCancellation(t *testing.T) {
	turn := &claudeChildTurn{changed: make(chan struct{}), calls: map[string]*claudeChildCall{}}
	turn.serial.Lock()
	defer turn.serial.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- turn.lock(ctx) }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("serial wait did not stop on cancellation")
	}
}

func TestClaudeChildSettlementPreservesCleanupFailureOnStop(t *testing.T) {
	status, result, diagnostic := settleClaudeChildOutcome("failed", "", "Claude process cleanup: unconfirmed", true, false, false)
	require.Equal(t, "failed", status)
	require.Empty(t, result)
	require.Contains(t, diagnostic, "cleanup")
}

func TestClaudeChildFinalResponseMustBeCurrent(t *testing.T) {
	var answer claudeChildAnswer
	require.Error(t, answer.complete())
	answer.assistant(true)
	require.NoError(t, answer.complete())
	answer.assistant(false)
	require.Error(t, answer.complete())
	answer.assistant(true)
	require.NoError(t, answer.complete())
}

func TestClaudeChildCleanupFailureQuarantinesResult(t *testing.T) {
	status, result, diagnostic := quarantineClaudeChildOutcome("completed", "untrusted", "", fmt.Errorf("unconfirmed"))
	require.Equal(t, "failed", status)
	require.Empty(t, result)
	require.Contains(t, diagnostic, "cleanup")
}

func TestClaudeChildAllowedToolClassification(t *testing.T) {
	for _, name := range []string{"Bash", "Read", "Write", "Edit", "Glob", "Grep", "ToolSearch", "mcp__mecha__read"} {
		require.True(t, claudeChildAllowedTool(name), name)
	}
	for _, name := range []string{"read", "Task", "mcp__other__read", ""} {
		require.False(t, claudeChildAllowedTool(name), name)
	}
}
