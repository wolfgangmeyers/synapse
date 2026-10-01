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

// RED: these calls must go through the real embedded SDK session. The project
// is disposable and no shell tool is registered. Stop at the first missing
// capability rather than treating a provider's proposed call as execution.
func TestEmbeddedG5ProjectFileToolsRED(t *testing.T) {
	for _, tc := range []struct {
		name   string
		worker bool
		escape string
		calls  []struct {
			name string
			args map[string]any
			want string
		}
	}{
		{name: "root-read", calls: []struct {
			name string
			args map[string]any
			want string
		}{
			{"read", map[string]any{"path": "note.txt"}, "original\n"},
		}},
		{name: "independent-worker-read-write-edit-read", worker: true, calls: []struct {
			name string
			args map[string]any
			want string
		}{
			{"read", map[string]any{"path": "note.txt"}, "original\n"},
			{"write", map[string]any{"path": "created.txt", "content": "before\n"}, "Successfully wrote"},
			{"edit", map[string]any{"path": "created.txt", "edits": []any{map[string]any{"oldText": "before", "newText": "after"}}}, "Successfully"},
			{"read", map[string]any{"path": "created.txt"}, "after\n"},
		}},
		{name: "deny-parent-traversal", escape: "../outside.txt"},
		{name: "deny-absolute-path", escape: "ABSOLUTE"},
		{name: "deny-url", escape: "file:///outside.txt"},
		{name: "deny-tilde", escape: "~/outside.txt"},
		{name: "deny-symlink-escape", escape: "escape.txt"},
	} {
		if !t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			project := filepath.Join(root, "project")
			require.NoError(t, os.Mkdir(project, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(project, "note.txt"), []byte("original\n"), 0600))
			outside := filepath.Join(root, "outside.txt")
			if tc.escape != "" {
				require.NoError(t, os.WriteFile(outside, []byte("untouched\n"), 0600))
				require.NoError(t, os.Symlink(outside, filepath.Join(project, "escape.txt")))
				attack := tc.escape
				if attack == "ABSOLUTE" {
					attack = outside
				}
				tc.calls = []struct {
					name string
					args map[string]any
					want string
				}{{"write", map[string]any{"path": attack, "content": "changed\n"}, ""}}
			}
			db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			type seen struct {
				tools  []ai.Tool
				result *ai.ToolResultMessage
			}
			observed := make(chan seen, len(tc.calls)*2+1)
			steps := make([]providers.FauxResponseStep, 0, len(tc.calls)+1)
			for i, call := range tc.calls {
				i, call := i, call
				steps = append(steps, func(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					var tools []ai.Tool
					for _, message := range history.Messages {
						if system, ok := message.(ai.SystemMessage); ok {
							tools = append(tools, system.ToolsAdded...)
						}
					}
					var prior *ai.ToolResultMessage
					if i > 0 {
						for _, message := range history.Messages {
							if r, ok := message.(ai.ToolResultMessage); ok && r.ToolCallID == "project-call-"+string(rune('a'+i-1)) {
								copy := r
								prior = &copy
							}
						}
					}
					observed <- seen{tools: tools, result: prior}
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(call.name, call.args, "project-call-"+string(rune('a'+i)))}, ai.StopToolUse)
				})
			}
			steps = append(steps, func(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				var last *ai.ToolResultMessage
				for _, message := range history.Messages {
					if r, ok := message.(ai.ToolResultMessage); ok && r.ToolCallID == "project-call-"+string(rune('a'+len(tc.calls)-1)) {
						copy := r
						last = &copy
					}
				}
				observed <- seen{result: last}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
			})
			reg.SetResponses(steps)
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
				Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{project}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
			})
			require.NoError(t, err)
			defer m.Close(ctx)
			request := LaunchRequest{Name: "g5-project-root", Cwd: project, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "read the project", RequestID: "a5555555-5555-4555-8555-555555555580"}
			if tc.worker {
				request.Name = "g5-project-worker"
				request.RequestID = "a5555555-5555-4555-8555-555555555581"
			}
			if tc.worker {
				_, err = m.Launch(ctx, request)
			} else {
				_, err = m.LaunchEmbeddedRoot(ctx, request)
			}
			require.NoError(t, err)
			var proposal seen
			select {
			case proposal = <-observed:
			case <-time.After(4 * time.Second):
				t.Fatal("provider did not receive the SDK turn")
			}
			for i, call := range tc.calls {
				registered := false
				for _, tool := range proposal.tools {
					if tool.Name == call.name {
						registered = true
					}
				}
				require.True(t, registered, "%s not advertised to the SDK provider (first genuine RED; later file calls not executed)", call.name)
				var continuation seen
				select {
				case continuation = <-observed:
				case <-time.After(4 * time.Second):
					t.Fatal("no correlated SDK continuation")
				}
				require.NotNil(t, continuation.result, "missing tool result for %s", call.name)
				require.Equal(t, "project-call-"+string(rune('a'+i)), continuation.result.ToolCallID)
				require.Equal(t, call.name, continuation.result.ToolName)
				if tc.escape != "" {
					require.True(t, continuation.result.IsError, "%s must be denied", tc.escape)
					bytes, err := os.ReadFile(outside)
					require.NoError(t, err)
					require.Equal(t, "untouched\n", string(bytes))
				} else {
					require.False(t, continuation.result.IsError)
					require.NotEmpty(t, continuation.result.Content)
					require.Contains(t, continuation.result.Content[0].(ai.TextContent).Text, call.want)
				}
				proposal = continuation
			}
			if tc.worker {
				bytes, err := os.ReadFile(filepath.Join(project, "created.txt"))
				require.NoError(t, err)
				require.Equal(t, "after\n", string(bytes))
			}
		}) {
			return
		} // First RED gates every later provider attempt.
	}
}
