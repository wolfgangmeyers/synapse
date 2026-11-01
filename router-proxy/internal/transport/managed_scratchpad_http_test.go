package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestManagedScratchpadGET(t *testing.T) {
	server, durable := newManagedHTTPTestServer(t)
	ctx := context.Background()

	agentID := "worker-scratchpad-test"

	// Append entries with small delay to ensure distinct timestamps
	e1, err := durable.AppendScratchpadEntry(ctx, agentID, "First unpinned entry", false)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	e2, err := durable.AppendScratchpadEntry(ctx, agentID, "First pinned entry", true)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	e3, err := durable.AppendScratchpadEntry(ctx, agentID, "Second unpinned entry", false)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	e4, err := durable.AppendScratchpadEntry(ctx, agentID, "Second pinned entry", true)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	e5, err := durable.AppendScratchpadEntry(ctx, agentID, "Third unpinned entry", false)
	require.NoError(t, err)

	// 1. Fetch recent entries (default limit)
	res, err := http.Get(server.URL + "/v1/managed-agents/" + agentID + "/scratchpad")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)

	var resp struct {
		Entries    []store.ScratchpadEntry `json:"entries"`
		TotalCount int                     `json:"total_count"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&resp))
	require.Equal(t, 5, resp.TotalCount)
	require.Len(t, resp.Entries, 5)

	// In FetchScratchpadRecent, pinned entries are first (DESC created_at), then unpinned entries (DESC created_at)
	require.True(t, resp.Entries[0].Pinned)
	require.Equal(t, e4.ID, resp.Entries[0].ID)
	require.True(t, resp.Entries[1].Pinned)
	require.Equal(t, e2.ID, resp.Entries[1].ID)
	require.False(t, resp.Entries[2].Pinned)
	require.Equal(t, e5.ID, resp.Entries[2].ID)
	require.False(t, resp.Entries[3].Pinned)
	require.Equal(t, e3.ID, resp.Entries[3].ID)
	require.False(t, resp.Entries[4].Pinned)
	require.Equal(t, e1.ID, resp.Entries[4].ID)

	// 2. Fetch with limit=1 for unpinned
	resLimit, err := http.Get(server.URL + "/v1/managed-agents/" + agentID + "/scratchpad?limit=1")
	require.NoError(t, err)
	defer resLimit.Body.Close()
	require.Equal(t, http.StatusOK, resLimit.StatusCode)

	var respLimit struct {
		Entries    []store.ScratchpadEntry `json:"entries"`
		TotalCount int                     `json:"total_count"`
	}
	require.NoError(t, json.NewDecoder(resLimit.Body).Decode(&respLimit))
	require.Equal(t, 5, respLimit.TotalCount)
	// 2 pinned + 1 unpinned = 3 entries
	require.Len(t, respLimit.Entries, 3)
	require.True(t, respLimit.Entries[0].Pinned)
	require.True(t, respLimit.Entries[1].Pinned)
	require.False(t, respLimit.Entries[2].Pinned)

	// 3. Paginate with offset=2, limit=2
	resOffset, err := http.Get(server.URL + "/v1/managed-agents/" + agentID + "/scratchpad?offset=2&limit=2")
	require.NoError(t, err)
	defer resOffset.Body.Close()
	require.Equal(t, http.StatusOK, resOffset.StatusCode)

	var respOffset struct {
		Entries    []store.ScratchpadEntry `json:"entries"`
		TotalCount int                     `json:"total_count"`
	}
	require.NoError(t, json.NewDecoder(resOffset.Body).Decode(&respOffset))
	require.Equal(t, 5, respOffset.TotalCount)
	require.Len(t, respOffset.Entries, 2)

	// 4. Empty agent with no scratchpad entries
	resEmpty, err := http.Get(server.URL + "/v1/managed-agents/nonexistent-agent/scratchpad")
	require.NoError(t, err)
	defer resEmpty.Body.Close()
	require.Equal(t, http.StatusOK, resEmpty.StatusCode)

	var respEmpty struct {
		Entries    []store.ScratchpadEntry `json:"entries"`
		TotalCount int                     `json:"total_count"`
	}
	require.NoError(t, json.NewDecoder(resEmpty.Body).Decode(&respEmpty))
	require.Equal(t, 0, respEmpty.TotalCount)
	require.Empty(t, respEmpty.Entries)
}

func TestManagedScratchpadTruncatePOST(t *testing.T) {
	server, durable := newManagedHTTPTestServer(t)
	ctx := context.Background()

	// Create a managed agent record so that the agent exists
	agentID := "worker-scratchpad-trunc-test"
	require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{
		AgentID:          agentID,
		Name:             agentID,
		SlotHeld:         true,
		Cwd:              "/tmp",
		SelectedModel:    store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "active",
		PiSessionID:      agentID + "-session",
		SessionEpoch:     1,
		ManifestRevision: 1,
		ManifestHash:     strings.Repeat("a", 64),
	}, store.ManagedOperation{
		OperationID:    agentID + "-op",
		Method:         http.MethodPost,
		CanonicalPath:  "/v1/managed-agents",
		RequestID:      agentID + "-req",
		ResponseStatus: http.StatusAccepted,
		ResponseBody:   []byte(`{}`),
	}))

	postTruncate := func(agent string, body string) (*http.Response, map[string]any) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents/"+agent+"/scratchpad/truncate", bytes.NewBufferString(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		var resMap map[string]any
		_ = json.NewDecoder(res.Body).Decode(&resMap)
		return res, resMap
	}

	t.Run("Successful truncation by keep_last", func(t *testing.T) {
		// Populate entries
		for i := 1; i <= 10; i++ {
			time.Sleep(5 * time.Millisecond)
			_, err := durable.AppendScratchpadEntry(ctx, agentID, "entry "+string(rune('A'+i)), false)
			require.NoError(t, err)
		}
		// Add 2 pinned entries
		_, err := durable.AppendScratchpadEntry(ctx, agentID, "pinned 1", true)
		require.NoError(t, err)
		_, err = durable.AppendScratchpadEntry(ctx, agentID, "pinned 2", true)
		require.NoError(t, err)

		// 10 unpinned + 2 pinned = 12 total. Keep last 3 unpinned -> 7 deleted.
		res, resBody := postTruncate(agentID, `{"keep_last": 3}`)
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Equal(t, true, resBody["ok"])
		require.Equal(t, agentID, resBody["agent_id"])
		require.Equal(t, float64(7), resBody["deleted_count"])
		require.Equal(t, true, resBody["pinned_preserved"])

		// Verify count
		count, err := durable.CountScratchpadEntries(ctx, agentID)
		require.NoError(t, err)
		require.Equal(t, 5, count) // 3 unpinned + 2 pinned
	})

	t.Run("Successful truncation by before_id", func(t *testing.T) {
		// Clean up by truncating with keep_last: 1 then deleting remaining
		cleanAgent := "worker-trunc-before-id"
		require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{
			AgentID:          cleanAgent,
			Name:             cleanAgent,
			SlotHeld:         true,
			Cwd:              "/tmp",
			SelectedModel:    store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
			DesiredState:     "running",
			LifecycleState:   "running",
			ActivityState:    "active",
			PiSessionID:      cleanAgent + "-session",
			SessionEpoch:     1,
			ManifestRevision: 1,
			ManifestHash:     strings.Repeat("a", 64),
		}, store.ManagedOperation{
			OperationID:    cleanAgent + "-op",
			Method:         http.MethodPost,
			CanonicalPath:  "/v1/managed-agents",
			RequestID:      cleanAgent + "-req",
			ResponseStatus: http.StatusAccepted,
			ResponseBody:   []byte(`{}`),
		}))

		e1, err := durable.AppendScratchpadEntry(ctx, cleanAgent, "entry 1", false)
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
		e2, err := durable.AppendScratchpadEntry(ctx, cleanAgent, "entry 2", false)
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
		e3, err := durable.AppendScratchpadEntry(ctx, cleanAgent, "entry 3", false)
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
		p1, err := durable.AppendScratchpadEntry(ctx, cleanAgent, "pinned before", true)
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
		e4, err := durable.AppendScratchpadEntry(ctx, cleanAgent, "entry 4", false)
		require.NoError(t, err)

		// Truncate before e3: e1 and e2 deleted (2 entries). e3, p1, e4 preserved.
		res, resBody := postTruncate(cleanAgent, `{"before_id": "`+e3.ID+`"}`)
		require.Equal(t, http.StatusOK, res.StatusCode)
		require.Equal(t, true, resBody["ok"])
		require.Equal(t, cleanAgent, resBody["agent_id"])
		require.Equal(t, float64(2), resBody["deleted_count"])
		require.Equal(t, true, resBody["pinned_preserved"])

		entries, total, err := durable.FetchScratchpadRecent(ctx, cleanAgent, 10)
		require.NoError(t, err)
		require.Equal(t, 3, total) // p1, e3, e4
		var ids []string
		for _, e := range entries {
			ids = append(ids, e.ID)
		}
		require.Contains(t, ids, p1.ID)
		require.Contains(t, ids, e3.ID)
		require.Contains(t, ids, e4.ID)
		require.NotContains(t, ids, e1.ID)
		require.NotContains(t, ids, e2.ID)
	})

	t.Run("Validation 400 when both or neither provided, or keep_last <= 0", func(t *testing.T) {
		// Neither
		res, _ := postTruncate(agentID, `{}`)
		require.Equal(t, http.StatusBadRequest, res.StatusCode)

		// Both
		res, _ = postTruncate(agentID, `{"before_id": "some-id", "keep_last": 5}`)
		require.Equal(t, http.StatusBadRequest, res.StatusCode)

		// keep_last = 0
		res, _ = postTruncate(agentID, `{"keep_last": 0}`)
		require.Equal(t, http.StatusBadRequest, res.StatusCode)

		// keep_last < 0
		res, _ = postTruncate(agentID, `{"keep_last": -5}`)
		require.Equal(t, http.StatusBadRequest, res.StatusCode)

		// Invalid JSON
		res, _ = postTruncate(agentID, `not json`)
		require.Equal(t, http.StatusBadRequest, res.StatusCode)
	})

	t.Run("404 when agent or before_id not found", func(t *testing.T) {
		// Non-existent agent
		res, _ := postTruncate("nonexistent-agent-id", `{"keep_last": 5}`)
		require.Equal(t, http.StatusNotFound, res.StatusCode)

		// Non-existent before_id on existing agent
		res, _ = postTruncate(agentID, `{"before_id": "nonexistent-before-id"}`)
		require.Equal(t, http.StatusNotFound, res.StatusCode)
	})
}

