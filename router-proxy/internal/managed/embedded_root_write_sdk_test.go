package managed

import (
	"context"
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

func TestEmbeddedRootWriteSDKCreateOnly(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		deny       bool
	}{
		{"create", "new/created.txt", false},
		{"existing", "note.txt", true},
		{"symlink", "escape.txt", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			root := filepath.Join(base, "project")
			require.NoError(t, os.Mkdir(root, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "note.txt"), []byte("original"), 0600))
			outside := filepath.Join(base, "outside.txt")
			require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0600))
			require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape.txt")))
			db, err := store.Open(ctx, filepath.Join(base, "router.db"), store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			type observation struct {
				tools  []ai.Tool
				result *ai.ToolResultMessage
			}
			seen := make(chan observation, 2)
			reg.SetResponses([]providers.FauxResponseStep{
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					var tools []ai.Tool
					for _, msg := range h.Messages {
						if sys, ok := msg.(ai.SystemMessage); ok {
							tools = append(tools, sys.ToolsAdded...)
						}
					}
					seen <- observation{tools: tools}
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("write", map[string]any{"path": tc.path, "content": "created\n"}, "root-write-1")}, ai.StopToolUse)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					var result *ai.ToolResultMessage
					for _, msg := range h.Messages {
						if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "root-write-1" {
							copy := r
							result = &copy
						}
					}
					seen <- observation{result: result}
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
				},
			})
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
				Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
			})
			require.NoError(t, err)
			defer m.Close(ctx)
			_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "write-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "create the file", RequestID: "a5555555-5555-4555-8555-555555555590"})
			require.NoError(t, err)
			for i := 0; i < 2; i++ {
				select {
				case got := <-seen:
					if i == 0 {
						found, foundBash := false, false
						for _, tool := range got.tools {
							if tool.Name == "write" {
								found = true
							}
							if tool.Name == "bash" {
								foundBash = true
								// Check the declaration, not execution confinement.
								require.Equal(t, "Run a restricted read-only command in the saved private project.", tool.Description)
							}
						}
						require.True(t, found)
						require.True(t, foundBash, "root must advertise restricted host bash")
					} else {
						require.NotNil(t, got.result)
						require.Equal(t, "root-write-1", got.result.ToolCallID)
						require.Equal(t, "write", got.result.ToolName)
						require.Equal(t, tc.deny, got.result.IsError)
						if !tc.deny {
							require.Contains(t, got.result.Content[0].(ai.TextContent).Text, "Successfully wrote")
						}
					}
				case <-time.After(4 * time.Second):
					t.Fatal("SDK continuation timed out")
				}
			}
			bytes, err := os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, "untouched", string(bytes))
			bytes, err = os.ReadFile(filepath.Join(root, "note.txt"))
			require.NoError(t, err)
			require.Equal(t, "original", string(bytes))
			if !tc.deny {
				bytes, err = os.ReadFile(filepath.Join(root, tc.path))
				require.NoError(t, err)
				require.Equal(t, "created\n", string(bytes))
			}
		})
	}
}
