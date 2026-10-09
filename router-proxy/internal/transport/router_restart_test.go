package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestRouterRestartEndpoint(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(context.Background(), database, store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	exitCalled := make(chan int, 1)
	exitFile := filepath.Join(t.TempDir(), "router.exit_code")
	t.Setenv("ROUTER_EXIT_FILE", exitFile)

	router, err := transport.New(transport.Options{
		Store:        durable,
		Port:         0,
		RestartDelay: 10 * time.Millisecond,
		ExitFunc: func(code int) {
			exitCalled <- code
		},
	})
	require.NoError(t, err)

	server := httptest.NewServer(router)
	defer server.Close()

	// GET should be rejected with 405 Method Not Allowed
	getResp, err := http.Get(server.URL + "/v1/router/restart")
	require.NoError(t, err)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, getResp.StatusCode)

	// POST should succeed with 200 and {"ok": true, "restarting": true}
	postResp, err := http.Post(server.URL+"/v1/router/restart", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer postResp.Body.Close()

	require.Equal(t, http.StatusOK, postResp.StatusCode)
	body, err := io.ReadAll(postResp.Body)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, true, payload["ok"])
	require.Equal(t, true, payload["restarting"])

	// Verify asynchronous exit(75) was called
	select {
	case code := <-exitCalled:
		require.Equal(t, 75, code)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for router restart exit call")
	}

	// Verify exit file was written with 75
	content, err := os.ReadFile(exitFile)
	require.NoError(t, err)
	require.Equal(t, "75", strings.TrimSpace(string(content)))
}
