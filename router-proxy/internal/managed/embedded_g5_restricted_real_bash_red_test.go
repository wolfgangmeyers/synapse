package managed

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedG5RestrictedRealBashRED(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := t.TempDir()
	projects := [2]string{filepath.Join(base, "root-project"), filepath.Join(base, "worker-project")}
	for i, p := range projects {
		require.NoError(t, os.Mkdir(p, 0700))
		value := fmt.Sprintf("private-%d\n", i)
		require.NoError(t, os.WriteFile(filepath.Join(p, "one.txt"), []byte(value), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(p, "copy.txt"), []byte(value), 0600))
		require.NoError(t, os.Symlink(filepath.Join(projects[1-i], "one.txt"), filepath.Join(p, "escape.txt")))
	}
	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	type observation struct {
		index      int
		registered bool
		result     *ai.ToolResultMessage
	}
	observed := make(chan observation, 32)
	cases := []struct {
		command string
		success bool
		output  string
	}{
		{"pwd", true, projects[0]},
		{"ls -la", true, "one.txt"},
		{"cat one.txt", true, "private-0"},
		{"grep -F -n -- private-0 one.txt", true, "1:private-0"},
		{"cmp -- one.txt copy.txt", true, "exit=0"},
		{"wc -c -- one.txt", true, "one.txt"},
		{"grep -F -n -- private-1 one.txt", true, "exit=1"},
		{"", false, ""},
	}
	// Read the SDK's advertised tools, not a locally supplied executor.
	bashListed := func(h ai.TranscriptContext) bool {
		for _, msg := range h.Messages {
			if sys, ok := msg.(ai.SystemMessage); ok {
				for _, tool := range sys.ToolsAdded {
					if tool.Name == "bash" {
						return true
					}
				}
			}
		}
		return false
	}
	rootReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	workerReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer rootReg.Unregister()
	defer workerReg.Unregister()
	steps := make([]providers.FauxResponseStep, 0, len(cases)+1)
	steps = append(steps, func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		observed <- observation{index: 0, registered: bashListed(h)}
		return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": cases[0].command}, "bash-0")}, ai.StopToolUse)
	})
	for i := range cases {
		i := i
		steps = append(steps, func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var result *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == fmt.Sprintf("bash-%d", i) {
					copy := r
					result = &copy
				}
			}
			observed <- observation{index: i, result: result}
			if i+1 == len(cases) {
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "checked"}}, ai.StopStop)
			}
			observed <- observation{index: i + 1, registered: bashListed(h)}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": cases[i+1].command}, fmt.Sprintf("bash-%d", i+1))}, ai.StopToolUse)
		})
	}
	rootReg.SetResponses(steps)
	workerReg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			observed <- observation{index: -2, registered: bashListed(h)}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": "grep -F -n -- private-1 one.txt"}, "worker-bash")}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var r *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if v, ok := msg.(ai.ToolResultMessage); ok && v.ToolCallID == "worker-bash" {
					copy := v
					r = &copy
				}
			}
			observed <- observation{index: -1, result: r}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "checked"}}, ai.StopStop)
		},
	})
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
		if row.AgentID == "restricted-root" {
			return rootReg.GetModel()
		}
		return workerReg.GetModel()
	}, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	defer m.Close(context.Background())
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "restricted-root", Cwd: projects[0], Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "verify private project", RequestID: "a5555555-5555-4555-8555-555555555598"})
	require.NoError(t, err)
	require.Equal(t, projects[0], launched.Agent.Cwd)
	take := func() observation {
		t.Helper()
		select {
		case o := <-observed:
			return o
		case <-ctx.Done():
			t.Fatal("SDK continuation timed out")
			return observation{}
		}
	}
	// Reachability order: no later case is credited if this first registration
	// or result fails. Subsequent prompts only run after the prior checked result.
	for i, tc := range cases {
		first := take()
		require.Equal(t, i, first.index)
		require.True(t, first.registered, "fixed bash tool missing from SDK transcript")
		next := take()
		require.Equal(t, i, next.index)
		require.NotNil(t, next.result, "missing checked SDK tool result")
		require.Equal(t, fmt.Sprintf("bash-%d", i), next.result.ToolCallID)
		require.Equal(t, !tc.success, next.result.IsError, "command=%q result=%v", tc.command, next.result.Content)
		if tc.output != "" {
			require.Contains(t, fmt.Sprint(next.result.Content), tc.output)
		}
		require.NoFileExists(t, filepath.Join(projects[0], "PWN"))
	}
	worker, err := m.Launch(ctx, LaunchRequest{Name: "restricted-worker", Cwd: projects[1], Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "verify worker project", RequestID: "a5555555-5555-4555-8555-555555555599"})
	require.NoError(t, err)
	require.Equal(t, projects[1], worker.Agent.Cwd)
	workerTools := take()
	require.Equal(t, -2, workerTools.index)
	require.True(t, workerTools.registered, "fixed bash tool missing from worker SDK transcript")
	w := take()
	require.Equal(t, -1, w.index)
	require.NotNil(t, w.result)
	require.False(t, w.result.IsError)
	require.Contains(t, fmt.Sprint(w.result.Content), "1:private-1", "worker must use its own saved cwd")
	check, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer check.Close()
	for _, id := range []string{"restricted-root", "restricted-worker"} {
		var count int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, id).Scan(&count))
		require.Positive(t, count, "checked durable result %s", id)

		var histCount int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_tool'`, id).Scan(&histCount))
		require.Positive(t, histCount, "managed complete history tool entries %s", id)
	}
}
