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

func TestEmbeddedToolsAllowAbsoluteAndOutsidePaths(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	base := t.TempDir()
	project := filepath.Join(base, "project")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.Mkdir(project, 0700))
	require.NoError(t, os.Mkdir(outside, 0700))

	outsideRead := filepath.Join(outside, "read_test.txt")
	require.NoError(t, os.WriteFile(outsideRead, []byte("outside read content\n"), 0600))

	// Test tilde path expansion
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	tildeFileName := fmt.Sprintf(".test_tilde_%d.txt", time.Now().UnixNano())
	tildeFilePath := filepath.Join(home, tildeFileName)
	require.NoError(t, os.WriteFile(tildeFilePath, []byte("tilde content\n"), 0600))
	defer os.Remove(tildeFilePath)

	outsideWrite := filepath.Join(outside, "write_test.txt")
	outsideEdit := filepath.Join(outside, "edit_test.txt")
	require.NoError(t, os.WriteFile(outsideEdit, []byte("before edit\n"), 0600))

	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	// Step 1: read outside file with absolute path
	// Step 2: read file with tilde path
	// Step 3: write outside file with absolute path
	// Step 4: edit outside file with absolute path
	// Step 5: done
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("read", map[string]any{"path": outsideRead}, "call-read-abs"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("read", map[string]any{"path": "~/" + tildeFileName}, "call-read-tilde"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("write", map[string]any{"path": outsideWrite, "content": "outside write content\n"}, "call-write-abs"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("edit", map[string]any{
					"path": outsideEdit,
					"edits": []any{
						map[string]any{"oldText": "before edit", "newText": "after edit"},
					},
				}, "call-edit-abs"),
			}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "all done"}}, ai.StopStop)
		},
	})

	m, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer m.Close(ctx)

	published := make([]store.ManagedHistoryEntry, 0)
	m.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		published = append(published, entry)
	})

	generation, err := db.MintEmbeddedProcessGeneration(ctx, "unjailed-test-fixture")
	require.NoError(t, err)
	require.NoError(t, m.SetEmbeddedStartupGeneration(generation))

	const agentID = "unjailed-agent"
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               agentID,
		Cwd:                project,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "test unjailed tools",
		RequestID:          "test-unjailed-launch",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		saved, readErr := db.ManagedAgent(ctx, agentID)
		return readErr == nil && saved.ActivityState == "idle"
	}, 10*time.Second, 50*time.Millisecond, "agent must settle to idle")

	// Verify file outcomes on disk outside project
	writtenBytes, err := os.ReadFile(outsideWrite)
	require.NoError(t, err)
	require.Equal(t, "outside write content\n", string(writtenBytes))

	editedBytes, err := os.ReadFile(outsideEdit)
	require.NoError(t, err)
	require.Equal(t, "after edit\n", string(editedBytes))

	// Verify Complete history recorded the tool calls
	page, err := db.ManagedHistoryPage(ctx, "unjailed-agent", "complete", 0, 50)
	require.NoError(t, err)

	toolNames := make(map[string]bool)
	for _, entry := range page.Entries {
		if entry.Presentation != nil && entry.Presentation.Kind == "tool" {
			toolNames[entry.Presentation.ToolName] = true
			require.Equal(t, "success", entry.Presentation.Outcome)
		}
	}
	require.True(t, toolNames["read"], "read must be in complete history")
	require.True(t, toolNames["write"], "write must be in complete history")
	require.True(t, toolNames["edit"], "edit must be in complete history")
}
