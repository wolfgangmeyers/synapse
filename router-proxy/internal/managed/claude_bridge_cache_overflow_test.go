package managed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestClaudeBridgeCheckedCacheOverflowStopsTurn(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "terminating"}[terminal], func(t *testing.T) {
			ctx := context.Background()
			db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
			p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
			executions, fatals := 0, 0
			authority := &embeddedTurnToolAuthority{persistence: p, beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error { return nil }, tools: []agent.AgentTool{{Name: "scratchpad", Execute: func(_ context.Context, id string, _ map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
				executions++
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: id}}, Terminate: terminal && id == "overflow"}, nil
			}}}}
			bridge := newClaudeCodeTurnBridge(authority)
			bridge.admitted = true
			bridge.fatal = func(error) { fatals++ }
			for _, id := range []string{"cached", "overflow", "later"} {
				call := ai.ToolCall{ID: id, Name: "scratchpad", Arguments: map[string]any{}}
				require.NoError(t, p.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
				require.NoError(t, bridge.producer(ctx, call))
			}
			request := func(id string) ClaudeCodeMCPCall {
				return ClaudeCodeMCPCall{ToolUseID: id, Name: "scratchpad", Arguments: json.RawMessage(`{}`)}
			}
			first, err := bridge.execute(ctx, request("cached"))
			require.NoError(t, err)
			again, err := bridge.execute(ctx, request("cached"))
			require.NoError(t, err)
			require.Equal(t, first, again)
			conflict := request("cached")
			conflict.Arguments = json.RawMessage(`{"x":1}`)
			_, err = bridge.execute(ctx, conflict)
			require.ErrorContains(t, err, "conflict")
			bridge.bytes = 16 << 20
			_, err = bridge.execute(ctx, request("overflow"))
			require.ErrorContains(t, err, "checked result buffer overflow")
			require.ErrorContains(t, bridge.err(), "checked result buffer overflow")
			require.Equal(t, 1, fatals)
			require.Equal(t, terminal, bridge.terminal)
			for _, id := range []string{"overflow", "later"} {
				_, err = bridge.execute(ctx, request(id))
				require.Error(t, err)
			}
			require.Equal(t, 2, executions)
			snapshot, err := db.ReopenEmbedded(ctx, binding)
			require.NoError(t, err)
			found := 0
			for _, entry := range snapshot.Entries {
				message, err := ai.UnmarshalMessage(entry.Body)
				require.NoError(t, err)
				if result, ok := message.(ai.ToolResultMessage); ok && result.ToolCallID == "overflow" {
					found++
					require.False(t, result.IsError)
				}
			}
			require.Equal(t, 1, found)
		})
	}
}
