package managed_test

import (
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedProgressIntegration(t *testing.T) {
	ctx := context.Background()

	// 1. Create a test store using store.OpenTestStore(t)
	testStore, _ := store.OpenTestStore(t)

	// 2. Create a test router using transport.New(transport.Options{Store: testStore})
	router, err := transport.New(transport.Options{
		Store: testStore,
	})
	require.NoError(t, err)

	// 3. Create a managed agent in the store
	agentID := "test-agent-progress"
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
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
		CreatedAt:        nowStr,
		UpdatedAt:        nowStr,
	}
	op := store.ManagedOperation{
		OperationID:    "op-" + agentID,
		Method:         "POST",
		CanonicalPath:  "/v1/managed-agents",
		RequestID:      "req-" + agentID,
		ResponseStatus: 202,
		ResponseBody:   []byte(`{}`),
	}
	require.NoError(t, testStore.ReserveManagedLaunch(ctx, launchRecord, op))

	// 4. Call testStore.SetAgentProgress(ctx, agent.AgentID, 35, "step 1")
	require.NoError(t, testStore.SetAgentProgress(ctx, agentID, 35, "step 1"))

	// 5. Call PublicAgent(agentRecord) and assert progress is populated with percent: 35, note: "step 1"
	agentRecord, err := testStore.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	proj := managed.PublicAgent(agentRecord)
	require.NotNil(t, proj["progress"])
	progressMap, ok := proj["progress"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 35, progressMap["percent"])
	require.Equal(t, "step 1", progressMap["note"])

	// 6. Make GET request to /v1/agents/{id}/progress/history using httptest. Assert status 200, length 1, percent 35.
	{
		req := httptest.NewRequest(http.MethodGet, "/v1/agents/"+agentID+"/progress/history", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var history []struct {
			Percent   int    `json:"percent"`
			Note      string `json:"note"`
			Timestamp string `json:"timestamp"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &history))
		require.Len(t, history, 1)
		require.Equal(t, 35, history[0].Percent)
		require.Equal(t, "step 1", history[0].Note)
	}

	// 7. Make GET request to /v1/agents/{id}/progress/chart.png using httptest. Assert status 200, Content-Type image/png, and assert png.Decode succeeds.
	{
		req := httptest.NewRequest(http.MethodGet, "/v1/agents/"+agentID+"/progress/chart.png", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "image/png", rec.Header().Get("Content-Type"))

		img, err := png.Decode(rec.Body)
		require.NoError(t, err)
		require.NotNil(t, img)
	}

	// 8. Call testStore.SetAgentProgress(ctx, agent.AgentID, 80, "step 2")
	require.NoError(t, testStore.SetAgentProgress(ctx, agentID, 80, "step 2"))

	// 9. Call testStore.ClearAgentProgress(ctx, agent.AgentID)
	require.NoError(t, testStore.ClearAgentProgress(ctx, agentID))

	// 10. Call PublicAgent(agentRecord) and assert progress is nil.
	agentRecord, err = testStore.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	proj = managed.PublicAgent(agentRecord)
	require.Nil(t, proj["progress"])

	// 11. Make GET request to /v1/agents/{id}/progress/history. Assert status 200, length 2 (history preserved after clear).
	{
		req := httptest.NewRequest(http.MethodGet, "/v1/agents/"+agentID+"/progress/history", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var history []struct {
			Percent   int    `json:"percent"`
			Note      string `json:"note"`
			Timestamp string `json:"timestamp"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &history))
		require.Len(t, history, 2)
		require.Equal(t, 35, history[0].Percent)
		require.Equal(t, "step 1", history[0].Note)
		require.Equal(t, 80, history[1].Percent)
		require.Equal(t, "step 2", history[1].Note)
	}

	// 12. Make GET request for unknown agent ID to /v1/agents/unknown-agent/progress/history. Assert status 404.
	{
		req := httptest.NewRequest(http.MethodGet, "/v1/agents/unknown-agent/progress/history", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code)
	}

	// 13. Make GET request for unknown agent ID to /v1/agents/unknown-agent/progress/chart.png. Assert status 404.
	{
		req := httptest.NewRequest(http.MethodGet, "/v1/agents/unknown-agent/progress/chart.png", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code)
	}
}
