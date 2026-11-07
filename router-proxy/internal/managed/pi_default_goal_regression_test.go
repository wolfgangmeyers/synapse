package managed_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestPiForegroundGoalIgnoresChangedClaudeDefault(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	marker := filepath.Join(dir, "model")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = auth ]; then echo '{\"loggedIn\":true}'; exit 0; fi\nprintf '%s\\n' \"$@\" > \"$MARKER\"\ncat >/dev/null\nprintf 'VERDICT: COMPLETED\\nREASON: done\\n'\n"), 0700))
	t.Setenv("MECHA_CLAUDE_BIN", bin)
	t.Setenv("MARKER", marker)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	entered := make(chan struct{})
	release := make(chan struct{})
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "finish the task"}, "goal-call")}, ai.StopToolUse)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			close(entered)
			<-release
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "work finished"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "VERDICT: COMPLETED\nREASON: Pi evaluator"}}, ai.StopStop)
		},
	})
	h := newBehavioralHarness(t, func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() })
	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{Name: "pi-goal-default", Cwd: h.root, Runtime: "embedded", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "Set a goal and work", RequestID: "12345678-1234-4234-8234-123456789abc"})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("foreground did not start")
	}
	require.NoError(t, h.db.SetDefaultModel(h.ctx, &store.ManagedModel{Provider: "claude-code", ID: "haiku"}))
	close(release)
	db := h.sqlDB(t)
	require.Eventually(t, func() bool {
		var state string
		err := db.QueryRowContext(h.ctx, `SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&state)
		return err == nil && state == "DONE"
	}, 5*time.Second, 20*time.Millisecond)
	_, err = os.Stat(marker)
	require.True(t, os.IsNotExist(err), "Claude evaluator must not run for a Pi foreground turn")
}
