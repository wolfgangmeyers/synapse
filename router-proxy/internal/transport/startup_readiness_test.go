package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestListenServesOnlyHealthWhileManagedStartupIsBlockedThenCompletes(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)

	entered := make(chan struct{})
	release := make(chan struct{})
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	router.startupWork = func(startupCtx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-startupCtx.Done():
			return startupCtx.Err()
		}
	}
	listenResult := make(chan error, 1)
	go func() { listenResult <- router.Listen(ctx) }()
	<-entered

	httpURL, _ := router.URLs()
	response, err := http.Get(httpURL + "/v1/health")
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "starting", body["status"])
	require.Equal(t, "starting", body["readiness"])
	require.NotContains(t, body, "schema_migrations", "startup health must come from memory")

	response, err = http.Get(httpURL + "/v1/agents")
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.NoError(t, response.Body.Close())

	close(release)
	require.NoError(t, <-listenResult)
	defer router.Close(ctx)
	response, err = http.Get(httpURL + "/v1/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
}

func TestHealthKeepsDirtyRuntimeReady(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable, RuntimeIdentity: RuntimeIdentity{SourceCommit: "local", SourceTree: "dirty"}})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "ok", body["status"])
	require.Equal(t, "ok", body["readiness"])
	require.Equal(t, "dirty", body["runtime"].(map[string]any)["source_tree"])
}

func TestListenClosesTemporaryHTTPAfterBlockedManagedStartupFailure(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)

	entered := make(chan struct{})
	release := make(chan struct{})
	startupErr := errors.New("startup recovery failed")
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	router.startupWork = func(startupCtx context.Context) error {
		close(entered)
		select {
		case <-release:
			return startupErr
		case <-startupCtx.Done():
			return startupCtx.Err()
		}
	}
	listenResult := make(chan error, 1)
	go func() { listenResult <- router.Listen(ctx) }()
	<-entered

	httpURL, _ := router.URLs()
	response, err := http.Get(httpURL + "/v1/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())

	close(release)
	require.ErrorIs(t, <-listenResult, startupErr)
	require.Nil(t, router.listener)
	probe, probeErr := http.Get(httpURL + "/v1/health")
	if probeErr == nil {
		_ = probe.Body.Close()
		t.Fatal("temporary server remained reachable after startup failure")
	}
}
