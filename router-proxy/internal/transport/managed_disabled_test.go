package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestManagedDisabledOwnsDirectRoutesAndCapabilities(t *testing.T) {
	durable, err := store.Open(context.Background(), t.TempDir()+"/router.db", store.Options{})
	require.NoError(t, err)
	router, err := New(Options{Store: durable})
	require.NoError(t, err)
	t.Cleanup(func() { _ = router.Close(context.Background()); _ = durable.Close() })

	capabilities := httptest.NewRecorder()
	router.ServeHTTP(capabilities, httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil))
	require.Equal(t, http.StatusOK, capabilities.Code)
	var capabilityBody map[string]any
	require.NoError(t, json.Unmarshal(capabilities.Body.Bytes(), &capabilityBody))
	require.Equal(t, false, capabilityBody["managed_v1"])
	require.Equal(t, false, capabilityBody["ui_live_v1"])

	for _, path := range []string{
		"/v1/managed-agents",
		"/v1/managed-agents/alpha/children",
		"/v1/managed-directories",
		"/v1/history?view=quiet",
		"/v1/ui-live",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equalf(t, http.StatusServiceUnavailable, response.Code, path)
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
		require.Equalf(t, "MANAGED_DISABLED", payload.Error.Code, path)
	}
}

func TestManagedErrorIncludesOnlySafeActionableDiagnostics(t *testing.T) {
	durable, err := store.Open(context.Background(), t.TempDir()+"/router.db", store.Options{})
	require.NoError(t, err)
	router, err := New(Options{Store: durable})
	require.NoError(t, err)
	t.Cleanup(func() { _ = router.Close(context.Background()); _ = durable.Close() })

	request := httptest.NewRequest(http.MethodGet, "/v1/managed-agents/alpha", nil)
	request.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174001")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, "123e4567-e89b-42d3-a456-426614174001", response.Header().Get("X-Request-ID"))

	var payload struct {
		RequestID string `json:"request_id"`
		Error     struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, "MANAGED_DISABLED", payload.Error.Code)
	require.Equal(t, "123e4567-e89b-42d3-a456-426614174001", payload.RequestID)
	require.Equal(t, "managed_agent_read", payload.Error.Details["operation"])
	require.Equal(t, "alpha", payload.Error.Details["agent_id"])
	require.Equal(t, "123e4567-e89b-42d3-a456-426614174001", payload.Error.Details["request_id"])
	require.Equal(t, "disabled", payload.Error.Details["state"])
	require.Equal(t, "unavailable", payload.Error.Details["fence"])
	require.Equal(t, "managed_surface_disabled", payload.Error.Details["cause"])
	encoded := response.Body.String()
	for _, forbidden := range []string{"pi_session_id", "process_pid", "credential", "authorization"} {
		require.NotContains(t, encoded, forbidden)
	}
}
