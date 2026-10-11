package transport

import (
	"context"
	"encoding/json"
	"net/http"
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
