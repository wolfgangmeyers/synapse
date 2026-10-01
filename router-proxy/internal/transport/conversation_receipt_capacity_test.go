package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestConversationReceiptCapacityHasStableHTTPError(t *testing.T) {
	response := httptest.NewRecorder()
	(&Router{}).writeError(response, &store.ConversationReceiptCapacityError{})
	require.Equal(t, http.StatusInsufficientStorage, response.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	problem, ok := body["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "conversation_receipt_capacity", problem["code"])
	require.NotEmpty(t, problem["message"])
	require.Empty(t, response.Header().Get("Retry-After"))
}
