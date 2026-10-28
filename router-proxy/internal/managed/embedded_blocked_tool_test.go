package managed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedBlockedTool(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openTestStore(t, ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-blocked-tool-test"
	launchRecord := store.ManagedAgentRecord{
		AgentID:          agentID,
		Name:             agentID,
		SlotHeld:         true,
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		AcceptingPrompts: true,
		PiSessionID:      "session-" + agentID,
		SessionEpoch:     1,
		Runtime:          "embedded",
		ManifestRevision: 1,
		ManifestHash:     "manifest-hash",
		CreatedAt:        "2026-01-01T00:00:00Z",
		UpdatedAt:        "2026-01-01T00:00:00Z",
	}
	op := store.ManagedOperation{
		OperationID:    "op-" + agentID,
		Method:         "POST",
		CanonicalPath:  "/v1/managed-agents",
		RequestID:      "req-" + agentID,
		ResponseStatus: 202,
		ResponseBody:   []byte(`{}`),
	}
	require.NoError(t, db.ReserveManagedLaunch(ctx, launchRecord, op))

	binding := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-" + agentID, Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	publishedCount := 0
	mgr := &Manager{
		store: db,
	}
	mgr.SetProjectionPublisher(func() {
		publishedCount++
	})
	tool := mgr.embeddedBlockedTool(owner)
	require.Equal(t, "mecha_blocked", tool.Name)

	// 1. Validation: missing action
	{
		_, err := tool.Execute(ctx, "c1", map[string]any{}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "action is required")
	}

	// 2. Validation: set without reason
	{
		_, err := tool.Execute(ctx, "c2", map[string]any{"action": "set"}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "reason is required")

		_, err = tool.Execute(ctx, "c3", map[string]any{"action": "set", "reason": "   "}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "reason is required")
	}

	// 3. Action "set" with reason
	reason := "Need external API key for third-party auth service"
	{
		prevPub := publishedCount
		res, err := tool.Execute(ctx, "c4", map[string]any{"action": "set", "reason": reason}, nil)
		require.NoError(t, err)
		require.Len(t, res.Content, 1)
		txt := res.Content[0].(ai.TextContent).Text
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(txt), &data))
		require.Equal(t, true, data["ok"])
		require.Equal(t, true, data["blocked"])
		require.Equal(t, reason, data["reason"])
		require.NotEmpty(t, data["blocked_at"])
		require.Contains(t, data["warning"], "⚠️ Agent execution is now BLOCKED")
		require.Contains(t, data["warning"], "Goal continuation loops are suspended")
		require.Equal(t, prevPub+1, publishedCount)

		// Verify state in store
		blocked, r, at, err := db.GetAgentBlocked(ctx, agentID)
		require.NoError(t, err)
		require.True(t, blocked)
		require.Equal(t, reason, r)
		require.NotEmpty(t, at)

		// Verify projection
		agentRec, err := db.ManagedAgent(ctx, agentID)
		require.NoError(t, err)
		pub := PublicAgent(agentRec)
		require.Equal(t, true, pub["blocked"])
		require.Equal(t, reason, pub["blocked_reason"])
		require.Equal(t, at, pub["blocked_at"])
	}

	// 4. Action "clear"
	{
		prevPub := publishedCount
		res, err := tool.Execute(ctx, "c5", map[string]any{"action": "clear"}, nil)
		require.NoError(t, err)
		require.Len(t, res.Content, 1)
		txt := res.Content[0].(ai.TextContent).Text
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(txt), &data))
		require.Equal(t, true, data["ok"])
		require.Equal(t, false, data["blocked"])
		require.NotEmpty(t, data["cleared_at"])
		require.Contains(t, data["message"], "Worker is unblocked")
		require.Equal(t, prevPub+1, publishedCount)

		// Verify state in store
		blocked, r, at, err := db.GetAgentBlocked(ctx, agentID)
		require.NoError(t, err)
		require.False(t, blocked)
		require.Empty(t, r)
		require.Empty(t, at)

		// Verify projection
		agentRec, err := db.ManagedAgent(ctx, agentID)
		require.NoError(t, err)
		pub := PublicAgent(agentRec)
		require.Equal(t, false, pub["blocked"])
		require.Nil(t, pub["blocked_reason"])
		require.Nil(t, pub["blocked_at"])
	}

	// 5. Unsupported action
	{
		_, err := tool.Execute(ctx, "c6", map[string]any{"action": "invalid"}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported mecha_blocked action")
	}
}
