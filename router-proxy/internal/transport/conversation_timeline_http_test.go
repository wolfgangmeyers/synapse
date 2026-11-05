package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestConversationTimelineHTTP(t *testing.T) {
	router, st, _ := newUILiveTestRouter(t)
	agentID := "agent-http-timeline"

	// 1. Missing agent_id -> 400
	req := httptest.NewRequest(http.MethodGet, "/v1/conversation", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, 400, w.Code)

	// 2. Empty events -> 200 with empty array
	req = httptest.NewRequest(http.MethodGet, "/v1/conversation?agent_id="+agentID, nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	var resp struct {
		AgentID       string                            `json:"agent_id"`
		Events        []store.ConversationTimelineEvent `json:"events"`
		NextBeforeSeq *int64                            `json:"next_before_seq"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, agentID, resp.AgentID)
	require.Empty(t, resp.Events)
	require.Nil(t, resp.NextBeforeSeq)

	// 3. Populate store with events
	roleUser := "user"
	content1 := "msg 1"
	_, err := st.RecordConversationTimelineTx(req.Context(), "evt-1", agentID, "message", &roleUser, &content1, nil, 1000)
	require.NoError(t, err)

	content2 := "msg 2"
	_, err = st.RecordConversationTimelineTx(req.Context(), "evt-2", agentID, "tool", nil, &content2, nil, 2000)
	require.NoError(t, err)

	content3 := "msg 3"
	_, err = st.RecordConversationTimelineTx(req.Context(), "evt-3", agentID, "message", nil, &content3, nil, 3000)
	require.NoError(t, err)

	// 4. Query with limit=2
	req = httptest.NewRequest(http.MethodGet, "/v1/conversation?agent_id="+agentID+"&limit=2", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Events, 2)
	require.NotNil(t, resp.NextBeforeSeq)
	require.Equal(t, int64(2), *resp.NextBeforeSeq)

	// 5. Query with before_seq=2
	req = httptest.NewRequest(http.MethodGet, "/v1/conversation?agent_id="+agentID+"&limit=2&before_seq=2", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Events, 1)
	require.Nil(t, resp.NextBeforeSeq)
	require.Equal(t, int64(1), resp.Events[0].Seq)
}

func TestConversationTimelineEventBoundary(t *testing.T) {
	router, st, _ := newUILiveTestRouter(t)
	ctx := httptest.NewRequest(http.MethodGet, "/", nil).Context()
	for _, id := range []string{"one", "two", "three", "four"} {
		_, err := st.RecordConversationTimelineTx(ctx, id, "alpha", "message", nil, nil, nil, 1)
		require.NoError(t, err)
	}
	get := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/conversation?"+query, nil))
		return w
	}
	w := get("agent_id=alpha&before_event_id=four&limit=2")
	require.Equal(t, 200, w.Code)
	var result struct {
		Events []store.ConversationTimelineEvent `json:"events"`
		Next   *int64                            `json:"next_before_seq"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Equal(t, []string{"two", "three"}, []string{result.Events[0].ID, result.Events[1].ID})
	require.EqualValues(t, 2, *result.Next)
	for _, query := range []string{"before_event_id=four", "agent_id=alpha&before_event_id=four&before_seq=2", "agent_id=alpha&before_event_id="} {
		require.Equal(t, 400, get(query).Code)
	}
	for _, query := range []string{"agent_id=alpha&before_event_id=absent", "agent_id=other&before_event_id=four"} {
		w := get(query)
		require.Equal(t, 409, w.Code)
		require.Contains(t, w.Body.String(), "timeline_boundary_not_found")
	}
}
