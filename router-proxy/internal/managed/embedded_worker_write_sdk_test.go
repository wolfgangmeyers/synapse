package managed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedWorkerWriteSDK(t *testing.T) {
	for _, tc := range []struct {
		name, path, content string
		deny                bool
	}{
		{"create", "created.txt", "hello\n", false},
		{"nested", "new/dir/created.txt", "hello\n", false},
		{"existing", "note.txt", "replaced", true},
		{"parent-traversal", "../outside.txt", "changed", true},
		{"absolute", "ABSOLUTE", "changed", true},
		{"url", "file:///outside.txt", "changed", true},
		{"tilde", "~/outside.txt", "changed", true},
		{"symlink", "escape.txt", "changed", true},
		{"oversize", "large.txt", strings.Repeat("x", embeddedWriteLimit+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			root := filepath.Join(base, "worker")
			require.NoError(t, os.Mkdir(root, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "note.txt"), []byte("original"), 0600))
			outside := filepath.Join(base, "outside.txt")
			require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0600))
			require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape.txt")))
			if tc.path == "ABSOLUTE" {
				tc.path = outside
			}
			db, err := store.Open(ctx, filepath.Join(base, "router.db"), store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			type observed struct {
				tools  []ai.Tool
				result *ai.ToolResultMessage
			}
			seen := make(chan observed, 2)
			reg.SetResponses([]providers.FauxResponseStep{
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					var tools []ai.Tool
					for _, msg := range h.Messages {
						if sys, ok := msg.(ai.SystemMessage); ok {
							tools = append(tools, sys.ToolsAdded...)
						}
					}
					seen <- observed{tools: tools}
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("write", map[string]any{"path": tc.path, "content": tc.content}, "sdk-write-1")}, ai.StopToolUse)
				},
				func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					var result *ai.ToolResultMessage
					for _, msg := range h.Messages {
						if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "sdk-write-1" {
							copy := r
							result = &copy
						}
					}
					seen <- observed{result: result}
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
			_, err = m.Launch(ctx, LaunchRequest{Name: "write-worker", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "create the file", RequestID: "a5555555-5555-4555-8555-555555555585"})
			require.NoError(t, err)
			// Strict pre-intent policy denials now produce checked SDK results.
			for i := 0; i < 2; i++ {
				select {
				case got := <-seen:
					if i == 0 {
						found := false
						for _, tool := range got.tools {
							if tool.Name == "write" {
								found = true
							}
						}
						require.True(t, found)
					} else {
						require.NotNil(t, got.result)
						require.Equal(t, "sdk-write-1", got.result.ToolCallID)
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
				require.Equal(t, tc.content, string(bytes))
			}
		})
	}
}
