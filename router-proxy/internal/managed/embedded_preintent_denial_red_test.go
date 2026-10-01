package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The provider's original path is rejected before intent, but the denial must
// still be a checked, durable SDK result. A post-intent storage fault is not.
func TestEmbeddedPreIntentDenialAndPostIntentFaultRED(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		fault      bool
	}{
		{"path-traversal", "write", false},
		{"storage-fault-after-intent", "external", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			project := filepath.Join(base, "project")
			require.NoError(t, os.Mkdir(project, 0700))
			outside := filepath.Join(base, "outside.txt")
			require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0600))
			path := filepath.Join(base, "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			const callID = "original-denied-or-unknown-call"
			steps := make(chan *ai.ToolResultMessage, 1)
			args := map[string]any{"path": "../outside.txt", "content": "changed"}
			if tc.fault {
				args = map[string]any{}
			}
			reg.SetResponses([]providers.FauxResponseStep{
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(tc.tool, args, callID)}, ai.StopToolUse)
				},
				func(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					var result *ai.ToolResultMessage
					for _, message := range history.Messages {
						if r, ok := message.(ai.ToolResultMessage); ok && r.ToolCallID == callID {
							copy := r
							result = &copy
						}
					}
					steps <- result
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
				},
			})
			executions := make(chan struct{}, 1)
			intentReached := make(chan struct{}, 1)
			var external *agent.AgentTool
			if tc.fault {
				external = &agent.AgentTool{Name: "external", Parameters: &ai.Schema{Type: "object"}, Execute: func(context.Context, string, map[string]any, agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
					executions <- struct{}{}
					return agent.AgentToolResult{}, nil
				}}
			}
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
				Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{project}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
				EmbeddedExternalTool:  external,
				EmbeddedAfterIntent: func(context.Context) {
					_ = db.Close() // fault at final lease check, strictly after durable intent
					intentReached <- struct{}{}
				},
			})
			require.NoError(t, err)
			defer m.Close(ctx)
			_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "denial-root", Cwd: project, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "try the tool", RequestID: "a5555555-5555-4555-8555-555555555586"})
			require.NoError(t, err)
			if tc.fault {
				select {
				case <-intentReached:
				case <-time.After(4 * time.Second):
					t.Fatal("post-intent fault point not reached")
				}
			} else {
				select {
				case result := <-steps:
					require.NotNil(t, result, "no correlated SDK continuation")
					require.Equal(t, callID, result.ToolCallID)
					require.Equal(t, "write", result.ToolName)
					require.True(t, result.IsError)
				case <-time.After(4 * time.Second):
					t.Fatal("no correlated SDK continuation")
				}
			}
			// Independent SQLite reader works even when the fault closes the host store.
			raw, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer raw.Close()
			var intentCount, resultCount int
			require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_effects WHERE effect_id=?`, callID).Scan(&intentCount))
			require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE kind='toolResult'`).Scan(&resultCount))
			if tc.fault {
				require.Equal(t, 1, intentCount, "fault must follow a durable effect intent")
				var state string
				require.NoError(t, raw.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=?`, callID).Scan(&state))
				require.Equal(t, "unknown", state, "post-intent failure remains unresolved")
				require.Zero(t, resultCount, "storage fault must not fabricate a checked result")
				select {
				case <-steps:
					t.Fatal("post-intent fault continued the provider")
				default:
				}
			} else {
				require.Zero(t, intentCount, "policy denial must not create effect intent")
				require.Equal(t, 1, resultCount, "policy denial must survive reopen")
				var body []byte
				require.NoError(t, raw.QueryRowContext(ctx, `SELECT body FROM embedded_entries WHERE kind='toolResult'`).Scan(&body))
				message, err := ai.UnmarshalMessage(body)
				require.NoError(t, err)
				result, ok := message.(ai.ToolResultMessage)
				require.True(t, ok)
				require.Equal(t, callID, result.ToolCallID)
				require.Equal(t, "write", result.ToolName)
				require.True(t, result.IsError)
			}
			select {
			case <-executions:
				t.Fatal("external effect executed")
			default:
			}
			bytes, err := os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, "untouched", string(bytes))
		})
	}
}
