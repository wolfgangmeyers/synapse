package managed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Attachment rendezvous must not leak its five-second deadline into execution
// or checked-result persistence. Request/grant cancellation is still inherited.
func TestClaudeGrantAttachmentDeadlineDoesNotLimitTool(t *testing.T) {
	ctx := context.Background()
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	seen := false
	a := &embeddedTurnToolAuthority{persistence: p, beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error { return nil }}
	a.tools = []agent.AgentTool{{Name: "probe", Execute: func(ctx context.Context, _ string, _ map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		_, seen = ctx.Deadline()
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "ok"}}}, nil
	}}}
	bridge := newClaudeCodeTurnBridge(a)
	bridge.admitted = true
	call := ai.ToolCall{ID: "deadline", Name: "probe", Arguments: map[string]any{}}
	require.NoError(t, bridge.producer(ctx, call))
	grant := &claudeCodeGrantBinding{}
	grant.attach(bridge)
	result, err := grant.execute(ctx, ClaudeCodeMCPCall{ToolUseID: call.ID, Name: call.Name, Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.Contains(t, string(result), "ok")
	require.False(t, seen, "attachment timeout must not constrain execution")
}
