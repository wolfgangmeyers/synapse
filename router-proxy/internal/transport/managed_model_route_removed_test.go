package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/stretchr/testify/require"
)

func TestManagedModelChangeRouteIsRemoved(t *testing.T) {
	router := &Router{managed: &managed.Manager{}}
	request := httptest.NewRequest(http.MethodPut, "/v1/managed-agents/agent/model", strings.NewReader(`{"model":{"provider":"google","id":"gemini-2.5-pro"}}`))
	response := httptest.NewRecorder()

	require.True(t, router.handleManagedHTTP(response, request))
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Contains(t, response.Body.String(), `"code":"NOT_FOUND"`)
}
