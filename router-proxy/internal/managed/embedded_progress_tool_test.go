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

func TestEmbeddedProgressTool(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openTestStore(t, ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-progress-tool-test"
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
	mgr := &Manager{store: db}
	mgr.SetProjectionPublisher(func() {
		publishedCount++
	})
	tool := mgr.embeddedProgressTool(owner)
	require.Equal(t, "mecha_progress", tool.Name)

	// 1. Initial get: null progress, count 0
	{
		res, err := tool.Execute(ctx, "c1", map[string]any{"action": "get"}, nil)
		require.NoError(t, err)
		require.Len(t, res.Content, 1)
		txt := res.Content[0].(ai.TextContent).Text
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(txt), &data))
		require.Equal(t, true, data["ok"])
		require.Nil(t, data["percent"])
		require.Nil(t, data["note"])
		require.Nil(t, data["updated_at"])
		require.Equal(t, float64(0), data["count"])
		require.Equal(t, 0, publishedCount)
	}

	// 2. Set progress 25% with note
	{
		res, err := tool.Execute(ctx, "c2", map[string]any{
			"action":  "set",
			"percent": 25,
			"note":    "downloading assets",
		}, nil)
		require.NoError(t, err)
		txt := res.Content[0].(ai.TextContent).Text
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(txt), &data))
		require.Equal(t, true, data["ok"])
		require.Equal(t, float64(25), data["percent"])
		require.Equal(t, "downloading assets", data["note"])
		require.NotEmpty(t, data["updated_at"])
		require.Equal(t, 1, publishedCount, "projection must be published after setting progress")
	}

	// 3. Get progress
	{
		res, err := tool.Execute(ctx, "c3", map[string]any{"action": "get"}, nil)
		require.NoError(t, err)
		txt := res.Content[0].(ai.TextContent).Text
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(txt), &data))
		require.Equal(t, true, data["ok"])
		require.Equal(t, float64(25), data["percent"])
		require.Equal(t, "downloading assets", data["note"])
		require.Equal(t, float64(1), data["count"])
	}

	// 4. Validation error: percent out of bounds (< 0)
	{
		_, err := tool.Execute(ctx, "c4", map[string]any{
			"action":  "set",
			"percent": -1,
		}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "percent must be between 0 and 100")
	}

	// 5. Validation error: percent out of bounds (> 100)
	{
		_, err := tool.Execute(ctx, "c5", map[string]any{
			"action":  "set",
			"percent": 105,
		}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "percent must be between 0 and 100")
	}

	// 6. Validation error: note > 120 chars
	{
		longNote := ""
		for i := 0; i < 125; i++ {
			longNote += "a"
		}
		_, err := tool.Execute(ctx, "c6", map[string]any{
			"action":  "set",
			"percent": 50,
			"note":    longNote,
		}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "note exceeds maximum length of 120 characters")
	}

	// 7. Clear progress
	{
		res, err := tool.Execute(ctx, "c7", map[string]any{"action": "clear"}, nil)
		require.NoError(t, err)
		txt := res.Content[0].(ai.TextContent).Text
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(txt), &data))
		require.Equal(t, true, data["ok"])
		require.Equal(t, true, data["cleared"])
		require.Equal(t, 2, publishedCount, "projection must be published after clearing progress")
	}

	// 8. Get after clear: snapshot is null, count is preserved (1)
	{
		res, err := tool.Execute(ctx, "c8", map[string]any{"action": "get"}, nil)
		require.NoError(t, err)
		txt := res.Content[0].(ai.TextContent).Text
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(txt), &data))
		require.Equal(t, true, data["ok"])
		require.Nil(t, data["percent"])
		require.Nil(t, data["note"])
		require.Nil(t, data["updated_at"])
		require.Equal(t, float64(1), data["count"])
	}
}
