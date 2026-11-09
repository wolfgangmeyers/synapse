package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestSubagentStreamContextNormalizedSystemMessageAndTools(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_model_adapter.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-model-adapter")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var mu sync.Mutex
	var defaultReqMessages []ai.Message
	var planReqMessages []ai.Message
	defaultDone := make(chan struct{}, 1)
	planDone := make(chan struct{}, 1)

	capture := func(req ai.TranscriptContext) {
		sysPrompt := ai.GetCurrentSystemPrompt(req.Messages)
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(sysPrompt, "CRITICAL: READ-ONLY MODE") {
			planReqMessages = req.Messages
			select {
			case planDone <- struct{}{}:
			default:
			}
		} else {
			// Only capture the child subagent's request, not the parent wake notification
			for _, m := range req.Messages {
				if u, ok := m.(ai.UserMessage); ok {
					for _, c := range u.Content {
						if t, ok := c.(ai.TextContent); ok && t.Text == "run default check" {
							defaultReqMessages = req.Messages
							select {
							case defaultDone <- struct{}{}:
							default:
							}
							return
						}
					}
				}
			}
		}
	}

	steps := make([]providers.FauxResponseStep, 10)
	for i := range steps {
		steps[i] = func(req ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			capture(req)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "task finished"},
			}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	// 1. Test default subagent
	resDefault, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-model-default",
		Description: "default model stream check",
		Prompt:      "run default check",
	})
	require.NoError(t, err)
	require.Equal(t, "running", resDefault.Status)

	select {
	case <-defaultDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for default subagent stream callback")
	}

	mu.Lock()
	capturedDefault := defaultReqMessages
	mu.Unlock()

	require.NotEmpty(t, capturedDefault)

	var defaultSysMessages []ai.SystemMessage
	for _, m := range capturedDefault {
		if sys, ok := m.(ai.SystemMessage); ok {
			defaultSysMessages = append(defaultSysMessages, sys)
		}
	}
	require.Equal(t, 1, len(defaultSysMessages), "must have exactly one leading system message for default subagent")
	firstDefaultSys := defaultSysMessages[0]
	defaultToolNames := make([]string, 0, len(firstDefaultSys.ToolsAdded))
	for _, tool := range firstDefaultSys.ToolsAdded {
		defaultToolNames = append(defaultToolNames, tool.Name)
	}
	require.ElementsMatch(t, []string{"read", "edit", "write", "bash"}, defaultToolNames, "initial system message must contain all 4 tools")
	require.NotEmpty(t, ai.GetCurrentSystemPrompt(capturedDefault), "system prompt must be present")

	// 2. Test Plan subagent
	resPlan, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:      "subagent-model-plan",
		Description:  "plan model stream check",
		SubagentType: "Plan",
		Prompt:       "run plan check",
	})
	require.NoError(t, err)
	require.Equal(t, "running", resPlan.Status)

	select {
	case <-planDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for plan subagent stream callback")
	}

	mu.Lock()
	capturedPlan := planReqMessages
	mu.Unlock()

	require.NotEmpty(t, capturedPlan)

	var planSysMessages []ai.SystemMessage
	for _, m := range capturedPlan {
		if sys, ok := m.(ai.SystemMessage); ok {
			planSysMessages = append(planSysMessages, sys)
		}
	}
	require.Equal(t, 1, len(planSysMessages), "must have exactly one leading system message for Plan subagent")
	firstPlanSys := planSysMessages[0]
	planToolNames := make([]string, 0, len(firstPlanSys.ToolsAdded))
	for _, tool := range firstPlanSys.ToolsAdded {
		planToolNames = append(planToolNames, tool.Name)
	}
	require.ElementsMatch(t, []string{"read", "edit", "write", "bash"}, planToolNames, "initial system message must contain all 4 tools for Plan subagent")
	require.Contains(t, ai.GetCurrentSystemPrompt(capturedPlan), "CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS")
}

func TestClaudeChildNeverFallsBackToPiResolver(t *testing.T) {
	called := false
	mgr := &Manager{embeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model {
		called = true
		return &ai.Model{Provider: "google-vertex", ID: "fallback"}
	}}
	model, stream, err := mgr.resolveSubagentModel(store.ManagedAgentRecord{
		SelectedModel: store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"},
	})
	require.ErrorContains(t, err, "scoped child process bridge")
	require.Nil(t, model)
	require.Nil(t, stream)
	require.False(t, called)
}

func TestClaudeChildProcessLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "claude-child-parent")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	mgr := makeSubagentTestManager(t, db, reg, 2)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)
	cfg := claudeTestConfig(t, `{"turns":[[{"mcp_call":{"id":"read-1","name":"read","input":{"path":"note.txt"}}},{"text":"child done"}]]}`)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "note.txt"), []byte("hello child"), 0600))
	_, err = raw.Exec(`UPDATE managed_agents SET cwd=?, selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, cfg.Cwd, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}))
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	for _, v := range cfg.Environment {
		if strings.HasPrefix(v, "FAKE_CLAUDE_") {
			k, val, _ := strings.Cut(v, "=")
			t.Setenv(k, val)
		}
	}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "claude-child-test", ParentAgentID: parent.AgentID, Status: "running"}))
	task := &subagentTask{parent: parent, childID: "claude-child-test", attempt: 1, status: "running", prompt: "read note", timeout: 10 * time.Second}
	mgr.subagentsMu.Lock()
	mgr.subagentsActive[task.childID] = task
	mgr.subagentsRegistry[task.childID] = task
	mgr.subagentsMu.Unlock()
	go mgr.runSubagent(task)
	var last *store.ManagedSubagentRecord
	var lookupErr error
	defer func() {
		if t.Failed() {
			t.Logf("last record: %+v, lookup error: %v", last, lookupErr)
		}
	}()
	require.Eventually(t, func() bool {
		last, lookupErr = db.ManagedSubagent(ctx, parent, "claude-child-test")
		return lookupErr == nil && IsTerminalSubagentStatus(last.Status)
	}, 10*time.Second, 20*time.Millisecond)
	rec, err := db.ManagedSubagent(ctx, parent, "claude-child-test")
	require.NoError(t, err)
	require.Equal(t, "completed", rec.Status, "error: %s", rec.Error)
	require.Equal(t, "child done", rec.ResultText)
	require.Equal(t, 1, rec.ToolUseCount)
	transcript, err := db.ManagedSubagentTranscript(ctx, parent, "claude-child-test")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(transcript.Entries), 4)
}

func TestClaudeChildNativeToolsLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "claude-child-parent")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	mgr := makeSubagentTestManager(t, db, reg, 2)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)
	cfg := claudeTestConfig(t, `{"turns":[[{"tool":{"id":"1","name":"Bash","result":"ok"}},{"tool":{"id":"2","name":"Read","result":"ok"}},{"tool":{"id":"3","name":"Write","result":"ok"}},{"tool":{"id":"4","name":"Edit","result":"ok"}},{"tool":{"id":"5","name":"Glob","result":"ok"}},{"tool":{"id":"6","name":"Grep","result":"ok"}},{"tool":{"id":"7","name":"ToolSearch","result":"ok"}},{"mcp_call":{"id":"8","name":"read","input":{"path":"note.txt"}}},{"text":"child done"}]]}`)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Cwd, "note.txt"), []byte("hello child"), 0600))
	_, err = raw.Exec(`UPDATE managed_agents SET cwd=?, selected_model_provider='claude-code', selected_model_id='sonnet' WHERE agent_id=?`, cfg.Cwd, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}))
	t.Setenv("MECHA_CLAUDE_BIN", cfg.Binary)
	for _, v := range cfg.Environment {
		if strings.HasPrefix(v, "FAKE_CLAUDE_") {
			k, val, _ := strings.Cut(v, "=")
			t.Setenv(k, val)
		}
	}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "claude-child-test", ParentAgentID: parent.AgentID, Status: "running"}))
	task := &subagentTask{parent: parent, childID: "claude-child-test", attempt: 1, status: "running", prompt: "read note", timeout: 10 * time.Second}
	mgr.subagentsMu.Lock()
	mgr.subagentsActive[task.childID] = task
	mgr.subagentsRegistry[task.childID] = task
	mgr.subagentsMu.Unlock()
	go mgr.runSubagent(task)
	var last *store.ManagedSubagentRecord
	var lookupErr error
	defer func() {
		if t.Failed() {
			t.Logf("last record: %+v, lookup error: %v", last, lookupErr)
		}
	}()
	require.Eventually(t, func() bool {
		last, lookupErr = db.ManagedSubagent(ctx, parent, "claude-child-test")
		return lookupErr == nil && IsTerminalSubagentStatus(last.Status)
	}, 10*time.Second, 20*time.Millisecond)
	rec, err := db.ManagedSubagent(ctx, parent, "claude-child-test")
	require.NoError(t, err)
	require.Equal(t, "completed", rec.Status, "error: %s", rec.Error)
	require.Equal(t, "child done", rec.ResultText)
	require.Equal(t, 8, rec.ToolUseCount)
	transcript, err := db.ManagedSubagentTranscript(ctx, parent, "claude-child-test")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(transcript.Entries), 18)
}
