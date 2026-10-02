package managed

import (
	"context"
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

func TestEmbeddedToolVisibilityInCompleteHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	base := t.TempDir()
	project := filepath.Join(base, "project")
	require.NoError(t, os.Mkdir(project, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(project, "hello.txt"), []byte("hello world\n"), 0600))

	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	// Step 1: Run bash ls (success)
	// Step 2: Run bash nonexistent command (failed)
	// Step 3: Run read hello.txt
	// Step 4: Run write new.txt
	// Step 5: Run edit hello.txt
	// Step 6: Run mecha_mailbox_send
	// Step 7: Done
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("bash", map[string]any{"command": "ls -la"}, "call-bash-success"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("bash", map[string]any{"command": "exit 1"}, "call-bash-fail"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("read", map[string]any{"path": "hello.txt"}, "call-read"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("write", map[string]any{"path": "new.txt", "content": "created file\n"}, "call-write"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("edit", map[string]any{
					"path": "hello.txt",
					"edits": []any{
						map[string]any{"oldText": "hello world", "newText": "hello universe"},
					},
				}, "call-edit"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("mecha_mailbox_send", map[string]any{
					"recipient": "supervisor",
					"body":      "status update",
				}, "call-mail"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "all tools finished"}}, ai.StopStop)
		},
	})

	m, err := NewManager(ManagerOptions{
		Store:         db,
		Manifest:      manifest,
		Config:        Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}},
		CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
			return reg.GetModel()
		},
		CodexCredentialStore: testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer m.Close(context.Background())

	generation, err := db.MintEmbeddedProcessGeneration(ctx, "visibility-test-fixture")
	require.NoError(t, err)
	require.NoError(t, m.SetEmbeddedStartupGeneration(generation))

	const agentID = "visibility-test-root"
	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               agentID,
		Cwd:                project,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "run the visibility tools",
		RequestID:          "a5555555-5555-4555-8555-555555555511",
	})
	require.NoError(t, err)
	require.Equal(t, project, launched.Agent.Cwd)

	// Wait for agent to reach idle
	require.Eventually(t, func() bool {
		saved, readErr := db.ManagedAgent(ctx, agentID)
		return readErr == nil && saved.ActivityState == "idle"
	}, 10*time.Second, 50*time.Millisecond, "agent must settle to idle")

	// Query Complete View history
	page, err := db.ManagedHistoryPage(ctx, agentID, "complete", 0, 50)
	require.NoError(t, err)

	// Verify tool executions in complete view
	toolOutcomes := map[string]string{}
	toolInputs := map[string]string{}
	for _, entry := range page.Entries {
		if entry.Presentation != nil && entry.Presentation.Kind == "tool" {
			key := fmt.Sprintf("%s:%s", entry.Presentation.ToolName, entry.Presentation.Outcome)
			toolOutcomes[key] = entry.Message
			if len(entry.ToolInput) > 0 {
				toolInputs[key] = string(entry.ToolInput)
			}
			t.Logf("Complete entry: id=%s level=%s msg=%q presentation=%+v input=%s", entry.ID, entry.Level, entry.Message, entry.Presentation, string(entry.ToolInput))
		}
	}

	// Assert visibility of tools
	require.Contains(t, toolOutcomes, "bash:success", "successful bash must appear in complete view")
	require.Equal(t, "Ran bash", toolOutcomes["bash:success"])
	require.Contains(t, toolInputs, "bash:success")
	require.JSONEq(t, `{"command": "ls -la"}`, toolInputs["bash:success"])

	require.Contains(t, toolOutcomes, "bash:failed", "failed bash must appear in complete view")
	require.Equal(t, "Bash failed", toolOutcomes["bash:failed"])

	require.Contains(t, toolOutcomes, "read:success", "read tool must appear in complete view")
	require.Equal(t, "Ran read", toolOutcomes["read:success"])
	require.Contains(t, toolInputs, "read:success")
	require.JSONEq(t, `{"path": "hello.txt"}`, toolInputs["read:success"])

	require.Contains(t, toolOutcomes, "write:success", "write tool must appear in complete view")
	require.Equal(t, "Ran write", toolOutcomes["write:success"])
	require.Contains(t, toolInputs, "write:success")
	require.JSONEq(t, `{"content": "created file\n", "path": "new.txt"}`, toolInputs["write:success"])

	require.Contains(t, toolOutcomes, "edit:success", "edit tool must appear in complete view")
	require.Equal(t, "Ran edit", toolOutcomes["edit:success"])
	require.Contains(t, toolInputs, "edit:success")
	require.JSONEq(t, `{"edits": [{"newText": "hello universe", "oldText": "hello world"}], "path": "hello.txt"}`, toolInputs["edit:success"])

	require.Contains(t, toolOutcomes, "mecha_mailbox_send:success", "mailbox send must appear in complete view")
	require.Equal(t, "Ran mecha_mailbox_send", toolOutcomes["mecha_mailbox_send:success"])
	require.Contains(t, toolInputs, "mecha_mailbox_send:success")
	require.JSONEq(t, `{"body": "status update", "recipient": "supervisor"}`, toolInputs["mecha_mailbox_send:success"])
}
