package managed

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedToolTimeoutSchema(t *testing.T) {
	schema := embeddedToolTimeoutSchema()
	require.NotNil(t, schema)
	require.Equal(t, "object", schema.Type)

	// Valid argument combinations
	require.True(t, schema.Check(map[string]any{}))
	require.True(t, schema.Check(map[string]any{"duration_minutes": 15}))
	require.True(t, schema.Check(map[string]any{"duration_minutes": 30, "reason": "compiling large codebase"}))
	require.True(t, schema.Check(map[string]any{"reason": "waiting for migration"}))

	// Unrecognized extra fields denied (AdditionalAllowed = false)
	require.False(t, schema.Check(map[string]any{"extra_field": "disallowed"}))
}

func TestEmbeddedToolTimeoutExecution(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-timeout-test"
	binding := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{store: db}
	tool := mgr.embeddedToolTimeoutTool(owner, "mecha_tool_timeout")
	aliasTool := mgr.embeddedToolTimeoutTool(owner, "extend_tool_timeout")

	require.Equal(t, "mecha_tool_timeout", tool.Name)
	require.Equal(t, "extend_tool_timeout", aliasTool.Name)

	// Lane initially has default timeout (5 minutes)
	lane := mgr.laneFor(agentID)
	require.Equal(t, 5*time.Minute, lane.toolTimeout())

	// 1. Extend with default duration (no duration_minutes arg -> defaults to 30)
	res, err := tool.Execute(ctx, "call-1", map[string]any{}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	outText := res.Content[0].(ai.TextContent).Text
	require.Contains(t, outText, "Tool timeout temporarily extended to 30 minutes (until ")
	require.Contains(t, outText, "). Limit will automatically revert to 5 minutes thereafter.")
	require.Equal(t, 30*time.Minute, lane.toolTimeout())

	// 2. Extend with explicit duration: 15 minutes
	res, err = tool.Execute(ctx, "call-2", map[string]any{
		"duration_minutes": 15,
		"reason":           "running heavy test suite",
	}, nil)
	require.NoError(t, err)
	outText = res.Content[0].(ai.TextContent).Text
	require.Contains(t, outText, "Tool timeout temporarily extended to 15 minutes (until ")
	require.Equal(t, 30*time.Minute, lane.toolTimeout())

	// 3. Extend with clamped duration: 60 minutes -> clamped to 30
	res, err = aliasTool.Execute(ctx, "call-3", map[string]any{
		"duration_minutes": 60,
	}, nil)
	require.NoError(t, err)
	outText = res.Content[0].(ai.TextContent).Text
	require.Contains(t, outText, "Tool timeout temporarily extended to 30 minutes (until ")

	// 4. Extend with <= 0 duration -> defaults to 30
	res, err = tool.Execute(ctx, "call-4", map[string]any{
		"duration_minutes": -5,
	}, nil)
	require.NoError(t, err)
	outText = res.Content[0].(ai.TextContent).Text
	require.Contains(t, outText, "Tool timeout temporarily extended to 30 minutes (until ")

	// 5. When extendedTimeoutUntil expires, toolTimeout automatically reverts to 5 minutes
	lane.activeMu.Lock()
	lane.extendedTimeoutUntil = time.Now().Add(-1 * time.Second)
	lane.activeMu.Unlock()
	require.Equal(t, 5*time.Minute, lane.toolTimeout())
}

func TestWrapEmbeddedToolTimeoutNormalExecution(t *testing.T) {
	ctx := context.Background()
	agentID := "worker-normal-exec"
	mgr := &Manager{}

	executed := false
	normalTool := agent.AgentTool{
		Name: "fast_tool",
		Execute: func(callCtx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			executed = true
			return agent.AgentToolResult{
				Content: ai.ContentList{ai.TextContent{Text: "fast tool result"}},
			}, nil
		},
	}

	wrapped := mgr.wrapEmbeddedToolTimeout(agentID, normalTool)
	res, err := wrapped.Execute(ctx, "fast-1", map[string]any{}, nil)
	require.NoError(t, err)
	require.True(t, executed)
	require.Len(t, res.Content, 1)
	require.Equal(t, "fast tool result", res.Content[0].(ai.TextContent).Text)
}

func TestWrapEmbeddedToolTimeoutCancellationOnExceededLimit(t *testing.T) {
	ctx := context.Background()
	agentID := "worker-timeout-exceeded"
	mgr := &Manager{}
	lane := mgr.laneFor(agentID)

	// Set test override for fast timeout verification (50ms instead of 5 minutes)
	lane.activeMu.Lock()
	lane.defaultTimeoutOverride = 50 * time.Millisecond
	lane.activeMu.Unlock()

	slowTool := agent.AgentTool{
		Name: "slow_tool",
		Execute: func(callCtx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			select {
			case <-callCtx.Done():
				return agent.AgentToolResult{}, callCtx.Err()
			case <-time.After(2 * time.Second):
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "should not be reached"}},
				}, nil
			}
		},
	}

	wrapped := mgr.wrapEmbeddedToolTimeout(agentID, slowTool)
	start := time.Now()
	_, err := wrapped.Execute(ctx, "slow-1", map[string]any{}, nil)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Contains(t, err.Error(), "tool execution timed out")
	require.Contains(t, err.Error(), "(limit: 50ms)")
	require.Less(t, elapsed, 1*time.Second, "must be cancelled promptly by the timeout context")
}

func TestWrapEmbeddedToolTimeoutExtensionPreventsCancellation(t *testing.T) {
	ctx := context.Background()
	agentID := "worker-timeout-extended"
	mgr := &Manager{}
	lane := mgr.laneFor(agentID)

	// Configure test overrides: default 50ms, extended 500ms
	lane.activeMu.Lock()
	lane.defaultTimeoutOverride = 50 * time.Millisecond
	lane.extendedTimeoutOverride = 500 * time.Millisecond
	lane.activeMu.Unlock()

	// Tool takes 150ms: would time out under default 50ms, but succeeds under 500ms
	mediumTool := agent.AgentTool{
		Name: "medium_tool",
		Execute: func(callCtx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			select {
			case <-callCtx.Done():
				return agent.AgentToolResult{}, callCtx.Err()
			case <-time.After(120 * time.Millisecond):
				return agent.AgentToolResult{
					Content: ai.ContentList{ai.TextContent{Text: "medium tool completed"}},
				}, nil
			}
		},
	}

	wrapped := mgr.wrapEmbeddedToolTimeout(agentID, mediumTool)

	// First verify it times out without extension
	_, err := wrapped.Execute(ctx, "med-1", map[string]any{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool execution timed out")

	// Now extend timeout for 30 minutes
	lane.extendToolTimeout(30)
	require.Equal(t, 500*time.Millisecond, lane.toolTimeout())

	// Under extended timeout, the same tool call now succeeds
	res, err := wrapped.Execute(ctx, "med-2", map[string]any{}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	require.Equal(t, "medium tool completed", res.Content[0].(ai.TextContent).Text)

	// Expire the extension and verify it falls back to 50ms and times out again
	lane.activeMu.Lock()
	lane.extendedTimeoutUntil = time.Now().Add(-1 * time.Second)
	lane.activeMu.Unlock()
	require.Equal(t, 50*time.Millisecond, lane.toolTimeout())

	_, err = wrapped.Execute(ctx, "med-3", map[string]any{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool execution timed out")
}

func TestEmbeddedToolTimeoutRegistryIntegration(t *testing.T) {
	for _, role := range []string{"root", "worker"} {
		t.Run(role, func(t *testing.T) {
			m, db, provider, caller, _ := requiredToolsFixture(t, role)
			callID := "timeout-" + role
			result := requiredToolsCall(t, m, db, provider, caller, "mecha_tool_timeout", callID, map[string]any{
				"duration_minutes": 25,
				"reason":           "running heavy compile",
			})
			require.Equal(t, "mecha_tool_timeout", result.ToolName)
			require.False(t, result.IsError)
			text, ok := result.Content[0].(ai.TextContent)
			require.True(t, ok)
			require.Contains(t, text.Text, "Tool timeout temporarily extended to 25 minutes")

			lane := m.laneFor(caller)
			require.Equal(t, 30*time.Minute, lane.toolTimeout())
		})
	}
}

func TestExtendToolTimeoutAliasIntegration(t *testing.T) {
	m, db, provider, caller, _ := requiredToolsFixture(t, "worker")
	callID := "extend-timeout-alias"
	result := requiredToolsCall(t, m, db, provider, caller, "extend_tool_timeout", callID, map[string]any{})
	require.Equal(t, "extend_tool_timeout", result.ToolName)
	require.False(t, result.IsError)
	text, ok := result.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "Tool timeout temporarily extended to 30 minutes")
	lane := m.laneFor(caller)
	require.Equal(t, 30*time.Minute, lane.toolTimeout())
}

func TestEmbeddedSessionToolTimeoutCancellationIntegration(t *testing.T) {
	ctx := context.Background()
	m, db, provider, caller, _ := requiredToolsFixture(t, "worker")
	require.NoError(t, db.SetManagedAgentAllowInlineTools(ctx, caller, true))
	lane := m.laneFor(caller)
	lane.activeMu.Lock()
	lane.defaultTimeoutOverride = 60 * time.Millisecond
	lane.activeMu.Unlock()

	callID := "timeout-cancellation"
	results := make(chan ai.ToolResultMessage, 1)
	provider.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": "sleep 5"}, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range h.Messages {
				if result, ok := msg.(ai.ToolResultMessage); ok && result.ToolCallID == callID {
					results <- result
					break
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "final after timeout"}}, ai.StopStop)
		},
	})

	_, err := db.Enqueue(ctx, protocol.Envelope{
		ID:        "inbound-" + callID,
		From:      "sender",
		To:        caller,
		Body:      "trigger sleep",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, "inbound-"+callID)
	require.NoError(t, err)
	m.OfferDirect(caller)

	var result ai.ToolResultMessage
	select {
	case result = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("SDK did not produce tool result after timeout")
	}

	require.Equal(t, "bash", result.ToolName)
	require.Equal(t, callID, result.ToolCallID)
	require.True(t, result.IsError, "tool result must be an error on timeout")
	text := fmt.Sprint(result.Content)
	require.Contains(t, text, "tool execution timed out")
	require.Contains(t, text, "(limit: 60ms)")
}
