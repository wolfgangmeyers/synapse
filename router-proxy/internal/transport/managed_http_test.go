package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func newManagedHTTPTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifestPath := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := managed.LoadManifest(manifestPath)
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(context.Background()); _ = durable.Close() })
	return server, durable
}

func TestManagedSessionRequiresExactObjectAndReflectsCredentialedOrigin(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", strings.NewReader(`{"extra":true}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusBadRequest, response.StatusCode)

	request, err = http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", strings.NewReader(`{}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://one.example")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.Equal(t, "https://one.example", response.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", response.Header.Get("Access-Control-Allow-Credentials"))

	request, err = http.NewRequest(http.MethodOptions, server.URL+"/v1/managed-agents", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://one.example")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.Equal(t, "https://one.example", response.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", response.Header.Get("Access-Control-Allow-Credentials"))

	request, err = http.NewRequest(http.MethodOptions, server.URL+"/v1/managed-agents", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "not-an-origin")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
	require.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))
}

func TestManagedSessionAndProjectionAreAuthenticated(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	response, err := http.Post(server.URL+"/v1/ui-session", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.Contains(t, response.Header.Get("Set-Cookie"), "HttpOnly")
	require.Contains(t, response.Header.Get("Set-Cookie"), "SameSite=Strict")
	require.Contains(t, response.Header.Get("Set-Cookie"), "Path=/v1")
	cookie := response.Cookies()[0]
	var bootstrap struct {
		CSRF string `json:"csrf_token"`
	}
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &bootstrap))
	require.NotEmpty(t, bootstrap.CSRF)

	csrfMissing, err := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents", strings.NewReader(`{"name":"alpha","cwd":"/tmp"}`))
	require.NoError(t, err)
	csrfMissing.Header.Set("Content-Type", "application/json")
	csrfMissing.AddCookie(cookie)
	csrfResponse, err := http.DefaultClient.Do(csrfMissing)
	require.NoError(t, err)
	defer csrfResponse.Body.Close()
	require.Equal(t, http.StatusForbidden, csrfResponse.StatusCode)

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/managed-agents/missing", nil)
	require.NoError(t, err)
	request.AddCookie(cookie)
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNotFound, response.StatusCode)

	response, err = http.Get(server.URL + "/v1/managed-agents/missing")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)

	invalidRequest, err := http.NewRequest(http.MethodGet, server.URL+"/v1/managed-agents/missing", nil)
	require.NoError(t, err)
	invalidRequest.AddCookie(&http.Cookie{Name: uiSessionCookieName, Value: "invalid"})
	invalidResponse, err := http.DefaultClient.Do(invalidRequest)
	require.NoError(t, err)
	defer invalidResponse.Body.Close()
	require.Equal(t, http.StatusUnauthorized, invalidResponse.StatusCode)
	cleared := invalidResponse.Cookies()[0]
	require.Equal(t, uiSessionCookieName, cleared.Name)
	require.Equal(t, "/v1", cleared.Path)
	require.Equal(t, -1, cleared.MaxAge)
}
