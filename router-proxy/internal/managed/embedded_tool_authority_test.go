package managed

import (
	"context"
	"fmt"
	"github.com/sky-valley/pi/agent"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// RE-007/009: a checked-first caller must not publish success on a fenced append.
func TestEmbeddedCheckedFirstProjection(t *testing.T) {
	for _, checkedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "pi-order", true: "checked-first"}[checkedFirst], func(t *testing.T) {
			ctx := context.Background()
			db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
			p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
			p.binding.OwnerToken++ // deterministic stale writer; history itself is not fenced
			result := ai.ToolResultMessage{ToolCallID: "call", ToolName: "scratchpad", Content: ai.ContentList{ai.TextContent{Text: "saved"}}}
			published := 0
			p.publishComplete = func(_ store.ManagedHistoryEntry) { published++ }
			var err error
			if checkedFirst {
				err = p.AppendCheckedMessage(ctx, result)
			} else {
				err = p.AppendMessage(ctx, result)
			}
			require.Error(t, err)
			if checkedFirst {
				require.Zero(t, published)
			} else {
				require.Equal(t, 1, published, "Pi early projection remains unchanged")
			}
		})
	}
}

// Real SQLite and existing Execute closures, with no session or provider fake.
// This is the integration target for a serialized Claude producer/MCP pair.
func TestEmbeddedTurnToolAuthorityCheckedExecute(t *testing.T) {
	ctx := context.Background()
	db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
	m := &Manager{store: db}
	generation, err := db.MintEmbeddedProcessGeneration(ctx, "authority-test")
	require.NoError(t, err)
	m.embeddedStartupGeneration = generation
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis"}
	a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: b, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
	require.NoError(t, err)
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "remember this"}}}))
	call := ai.ToolCall{ID: "scratch-1", Name: "scratchpad", Arguments: map[string]any{"action": "append", "content": "durable note"}}
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
	require.NoError(t, a.BeforeToolExecute(ctx, agent.BeforeToolCallContext{ToolCall: call}))
	var tool agent.AgentTool
	for _, candidate := range a.Tools() {
		if candidate.Name == call.Name {
			tool = candidate
		}
	}
	require.NotNil(t, tool.Execute)
	result, err := tool.Execute(ctx, call.ID, call.Arguments, nil)
	require.NoError(t, err)
	published := 0
	p.publishComplete = func(_ store.ManagedHistoryEntry) {
		snap, readErr := db.ReopenEmbedded(ctx, b)
		require.NoError(t, readErr)
		require.Equal(t, "toolResult", snap.Entries[len(snap.Entries)-1].Kind, "checked result precedes projection")
		published++
	}
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: result.Content, Details: result.Details}))
	require.Equal(t, 1, published)
	// Existing Execute really wrote the scratchpad; read it through the real tool.
	fetched, err := tool.Execute(ctx, "fetch", map[string]any{"action": "fetch"}, nil)
	require.NoError(t, err)
	require.Contains(t, fmt.Sprint(fetched.Content), "durable note")
	bad := ai.ToolCall{ID: "bad", Name: "scratchpad", Arguments: map[string]any{"action": 123}}
	require.Error(t, a.BeforeToolExecute(ctx, agent.BeforeToolCallContext{ToolCall: bad}))
}

func TestEmbeddedTurnToolAuthorityGoalWitness(t *testing.T) {
	ctx := context.Background()
	db, b, _ := completionFixture(t)
	m := &Manager{store: db}
	g, err := db.MintEmbeddedProcessGeneration(ctx, "authority-goal")
	require.NoError(t, err)
	m.embeddedStartupGeneration = g
	token, err := db.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	p := &embeddedPersistence{db: db, binding: b, digest: "genesis", executionToken: token}
	a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: b, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
	require.NoError(t, err)
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "set a goal"}}}))
	require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, b, p.turnSeq, p.turnDigest))
	call := ai.ToolCall{ID: "goal-1", Name: "goal", Arguments: map[string]any{"action": "set", "goal": "finish the extraction"}}
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
	require.NoError(t, a.BeforeToolExecute(ctx, agent.BeforeToolCallContext{ToolCall: call}))
	forged := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: ai.ContentList{ai.TextContent{Text: "invented success"}}}
	require.ErrorContains(t, a.AppendCheckedMessage(ctx, forged), "no current Host Execute completion")
	var goal agent.AgentTool
	for _, candidate := range a.Tools() {
		if candidate.Name == call.Name {
			goal = candidate
		}
	}
	require.NotNil(t, goal.Execute)
	result, err := goal.Execute(ctx, call.ID, call.Arguments, nil)
	require.NoError(t, err)
	actual := ai.ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: result.Content, Details: result.Details, Timestamp: 1234}
	require.NoError(t, a.AppendCheckedMessage(ctx, actual))
	effect, err := db.RecoverEmbeddedEffect(ctx, b, call.ID)
	require.NoError(t, err)
	require.Equal(t, "observed", effect.State)
	require.ErrorContains(t, a.BeforeToolExecute(ctx, agent.BeforeToolCallContext{ToolCall: call}), "no new admission")
}
