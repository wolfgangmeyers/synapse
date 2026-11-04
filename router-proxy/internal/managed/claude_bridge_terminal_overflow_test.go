package managed

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Exercise the actual bridge and runtime publication fence after a durable
// terminating result, including failure to cache its HTTP response.
func TestClaudeBridgeTerminalResultFencesOutput(t *testing.T) {
	for _, name := range []string{"context_reset", "mecha_worker_terminate"} {
		for _, overflow := range []bool{false, true} {
			label := name + "/normal"
			if overflow {
				label = name + "/overflow"
			}
			t.Run(label, func(t *testing.T) {
				ctx := context.Background()
				db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
				p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
				var published []store.ManagedHistoryEntry
				p.publishComplete = func(entry store.ManagedHistoryEntry) { published = append(published, entry) }
				authority := &embeddedTurnToolAuthority{persistence: p, beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error { return nil }, tools: []agent.AgentTool{{Name: name, Execute: func(context.Context, string, map[string]any, agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
					return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "terminated"}}, Terminate: true}, nil
				}}}}
				bridge := newClaudeCodeTurnBridge(authority)
				bridge.admitted = true
				interrupted := make(chan struct{})
				bridge.interrupt = func() { close(interrupted) }
				runtime := &claudeCodeRuntime{p: p, bridge: bridge}
				require.NoError(t, p.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "start"}}}))
				call := ai.ToolCall{ID: "end", Name: name, Arguments: map[string]any{}}
				require.NoError(t, runtime.appendChecked(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
				require.NoError(t, bridge.producer(ctx, call))
				if overflow {
					bridge.bytes = 16 << 20
				}
				args, err := json.Marshal(call.Arguments)
				require.NoError(t, err)
				_, err = bridge.execute(ctx, ClaudeCodeMCPCall{ToolUseID: call.ID, Name: name, Arguments: args})
				if overflow {
					require.ErrorContains(t, err, "checked result buffer overflow")
				} else {
					require.NoError(t, err)
				}
				select {
				case <-interrupted:
				case <-time.After(2 * time.Second):
					t.Fatal("terminating result did not request interrupt")
				}
				before := len(published)
				require.Positive(t, before, "fixture must exercise publication")
				for _, text := range []string{"forbidden one", "forbidden two"} {
					err := runtime.appendChecked(ctx, ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: text}}})
					require.ErrorIs(t, err, errClaudeTerminalOutput)
				}
				require.Len(t, published, before, "late output must not be published")
				snapshot, err := db.ReopenEmbedded(ctx, binding)
				require.NoError(t, err)
				found := false
				for _, entry := range snapshot.Entries {
					message, err := ai.UnmarshalMessage(entry.Body)
					require.NoError(t, err)
					if result, ok := message.(ai.ToolResultMessage); ok && result.ToolCallID == call.ID {
						found = true
						continue
					}
					if found {
						require.NotEqual(t, "assistant", entry.Kind)
					}
				}
				require.True(t, found, "checked terminating result must be durable")
			})
		}
	}
}
