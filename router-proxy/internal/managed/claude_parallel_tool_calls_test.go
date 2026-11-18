package managed

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// TestClaudeParallelToolCallsExceedingPersistenceTimeout regression tests GitHub issue #113.
// When Claude invokes tools and Tool 1 takes longer than the 5-second message persistence
// timeout while holding b.serial:
// - While Tool 1 is executing for > 5s, appendChecked is called in a goroutine with an incoming message.
// - In the old code, appendChecked would fail with context deadline exceeded because it created a 5s
//   timeout before acquiring b.serial.
// - In the new code, appendChecked waits for b.serial, acquires it after Tool 1 finishes, and successfully
//   appends without context deadline exceeded.
// - Tool 2, which was waiting for b.serial, executes cleanly after Tool 1. Both complete without errors.
func TestClaudeParallelToolCallsExceedingPersistenceTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}

	slowEntered := make(chan struct{})
	slowRelease := make(chan struct{})

	customTools := []agent.AgentTool{
		{
			Name: "mock_slow_tool",
			Execute: func(ctx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
				close(slowEntered)
				select {
				case <-slowRelease:
				case <-ctx.Done():
					return agent.AgentToolResult{}, ctx.Err()
				}
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "slow tool done"}}}, nil
			},
		},
		{
			Name: "mock_fast_tool",
			Execute: func(ctx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "fast tool done"}}}, nil
			},
		},
	}

	authority := &embeddedTurnToolAuthority{
		persistence:       p,
		beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error { return nil },
		tools:             customTools,
		baseTools:         customTools,
	}

	// Admitted user turn
	require.NoError(t, authority.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "run tools"}}}))

	bridge := newClaudeCodeTurnBridge(authority)
	bridge.admitted = true
	r := &claudeCodeRuntime{p: p, bridge: bridge}

	call1 := ai.ToolCall{ID: "call-slow-1", Name: "mock_slow_tool", Arguments: map[string]any{"query": "projects"}}
	call2 := ai.ToolCall{ID: "call-fast-2", Name: "mock_fast_tool", Arguments: map[string]any{"query": "issues"}}

	// Producer registers calls in the bridge and appends assistant tool-call message
	bridge.serial.Lock()
	require.NoError(t, authority.AppendCheckedMessage(ctx, ai.AssistantMessage{
		Content:    ai.ContentList{call1, call2},
		StopReason: ai.StopToolUse,
	}))
	require.NoError(t, bridge.producer(ctx, call1))
	require.NoError(t, bridge.producer(ctx, call2))
	bridge.serial.Unlock()

	args1, err := json.Marshal(call1.Arguments)
	require.NoError(t, err)
	args2, err := json.Marshal(call2.Arguments)
	require.NoError(t, err)

	var wg sync.WaitGroup
	var tool1Result json.RawMessage
	var tool1Err error
	var tool2Result json.RawMessage
	var tool2Err error

	// Launch Tool 1 (mock_slow_tool)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tool1Result, tool1Err = bridge.execute(ctx, ClaudeCodeMCPCall{
			ToolUseID: call1.ID,
			Name:      call1.Name,
			Arguments: args1,
		})
	}()

	// Wait until Tool 1 has actually entered its execution function holding b.serial
	select {
	case <-slowEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("tool 1 never entered execution")
	}

	// While Tool 1 is running, launch Tool 2 which will wait for b.serial.
	wg.Add(1)
	go func() {
		defer wg.Done()
		tool2Result, tool2Err = bridge.execute(ctx, ClaudeCodeMCPCall{
			ToolUseID: call2.ID,
			Name:      call2.Name,
			Arguments: args2,
		})
	}()

	// While Tool 1 is still running, call appendChecked in a goroutine with an incoming message.
	// In the old code, this would fail with context deadline exceeded after 5 seconds because
	// the 5-second timeout started before acquiring b.serial.
	var appendErr error
	var appendDone = make(chan struct{})
	go func() {
		midMessage := ai.AssistantMessage{
			Content: ai.ContentList{ai.TextContent{Text: "concurrent progress"}},
		}
		appendErr = r.appendChecked(ctx, midMessage)
		close(appendDone)
	}()

	// Tool 1 executes for 5.2 seconds holding b.serial.
	time.Sleep(5200 * time.Millisecond)

	// Release Tool 1 after 5.2 seconds
	close(slowRelease)

	// Wait for appendChecked to finish after acquiring b.serial
	select {
	case <-appendDone:
	case <-time.After(5 * time.Second):
		t.Fatal("appendChecked did not complete within 5 seconds after tool 1 release")
	}
	require.NoError(t, appendErr, "appendChecked must succeed without context deadline exceeded")

	wg.Wait()

	require.NoError(t, tool1Err, "tool 1 execution failed")
	require.NoError(t, tool2Err, "tool 2 execution failed")
	require.Contains(t, string(tool1Result), "slow tool done")
	require.Contains(t, string(tool2Result), "fast tool done")

	// Verify entries exist in checked persistence
	snap, err := db.ReopenEmbedded(ctx, b)
	require.NoError(t, err)

	var toolResultIDs []string
	var assistantTexts []string
	for _, entry := range snap.Entries {
		msg, err := ai.UnmarshalMessage(entry.Body)
		require.NoError(t, err)
		if tr, ok := msg.(ai.ToolResultMessage); ok {
			toolResultIDs = append(toolResultIDs, tr.ToolCallID)
		} else if as, ok := msg.(ai.AssistantMessage); ok {
			for _, c := range as.Content {
				if tc, ok := c.(ai.TextContent); ok {
					assistantTexts = append(assistantTexts, tc.Text)
				}
			}
		}
	}

	require.Contains(t, toolResultIDs, call1.ID)
	require.Contains(t, toolResultIDs, call2.ID)
	require.Contains(t, assistantTexts, "concurrent progress")
}
