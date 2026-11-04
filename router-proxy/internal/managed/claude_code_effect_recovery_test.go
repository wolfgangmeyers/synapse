package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// A replacement bridge must reject durable effects before fresh admission.
func TestClaudeBridgePriorEffectBeforeFreshAdmission(t *testing.T) {
	ctx := context.Background()
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	m := &Manager{store: db}
	a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: binding, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
	require.NoError(t, err)
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "save"}}}))
	require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, binding, p.turnSeq, p.turnDigest))
	call := ai.ToolCall{ID: "durable-call", Name: "mecha_reminder", Arguments: map[string]any{"action": "create", "body": "one durable reminder", "delay_seconds": float64(60)}}
	require.NoError(t, a.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
	bridge := newClaudeCodeTurnBridge(a)
	bridge.admitted = true
	require.NoError(t, bridge.producer(ctx, call))
	args, err := json.Marshal(call.Arguments)
	require.NoError(t, err)
	request := ClaudeCodeMCPCall{ToolUseID: call.ID, Name: call.Name, Arguments: args}
	first, err := bridge.execute(ctx, request)
	require.NoError(t, err)
	evidence, err := db.RecoverEmbeddedEffect(ctx, binding, call.ID)
	require.NoError(t, err)
	require.Equal(t, "observed", evidence.State)
	again, err := bridge.execute(ctx, request)
	require.NoError(t, err)
	require.Equal(t, first, again)
	recovered := newClaudeCodeTurnBridge(a)
	recovered.admitted = true
	require.NoError(t, recovered.producer(ctx, call))
	a.beforeToolExecute = func(context.Context, agent.BeforeToolCallContext) error {
		t.Error("duplicate reached fresh admission")
		return errors.New("forbidden")
	}
	for i := 0; i < 2; i++ {
		_, err := recovered.execute(ctx, request)
		require.ErrorContains(t, err, "prior tool effect")
	}
	after, err := db.RecoverEmbeddedEffect(ctx, binding, call.ID)
	require.NoError(t, err)
	require.Equal(t, evidence, after)
	request.Arguments = json.RawMessage(`{"action":"create","body":"changed","delay_seconds":60}`)
	_, err = recovered.execute(ctx, request)
	require.ErrorContains(t, err, "conflict")
}

func TestClaudeBridgePriorUnknownAndStaleOwnerFailClosed(t *testing.T) {
	ctx := context.Background()
	db, binding, _, _ := wakePersistenceFixtureWithClaim(t, false)
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	m := &Manager{store: db}
	a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: binding, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
	require.NoError(t, err)
	call := ai.ToolCall{ID: "unknown-reminder", Name: "mecha_reminder", Arguments: map[string]any{"action": "create"}}
	intent, err := json.Marshal(call)
	require.NoError(t, err)
	require.NoError(t, db.RecordEmbeddedEffectIntent(ctx, binding, call.ID, intent))
	a.beforeToolExecute = func(context.Context, agent.BeforeToolCallContext) error {
		t.Error("reached fresh admission")
		return errors.New("forbidden")
	}
	args, err := json.Marshal(call.Arguments)
	require.NoError(t, err)
	for _, stale := range []bool{false, true} {
		bridge := newClaudeCodeTurnBridge(a)
		bridge.admitted = true
		require.NoError(t, bridge.producer(ctx, call))
		if stale {
			p.binding.OwnerToken += 100
		}
		_, err := bridge.execute(ctx, ClaudeCodeMCPCall{ToolUseID: call.ID, Name: call.Name, Arguments: args})
		require.Error(t, err)
		if !stale {
			require.ErrorContains(t, err, "prior tool effect")
		} else {
			require.NotErrorIs(t, err, sql.ErrNoRows)
		}
	}
}
