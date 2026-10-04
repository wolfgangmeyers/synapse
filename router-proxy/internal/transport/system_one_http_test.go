package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSystemOneTransportRoutes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "router.sqlite")

	durable, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	router, err := New(Options{
		Store: durable,
		Host:  "127.0.0.1",
		Port:  8765,
	})
	require.NoError(t, err)
	defer router.Close(ctx)

	server := httptest.NewServer(router)
	defer server.Close()

	// 1. GET /v1/settings/systemone (initially unconfigured)
	resp, err := http.Get(server.URL + "/v1/settings/systemone")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var initialCfg store.SystemOneConfig
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&initialCfg))
	resp.Body.Close()
	require.False(t, initialCfg.Enabled)
	require.Empty(t, initialCfg.Host)

	// 2. POST /v1/systemone/test when unconfigured
	testResp, err := http.Post(server.URL+"/v1/systemone/test", "application/json", bytes.NewReader([]byte("{}")))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, testResp.StatusCode)

	var testResult map[string]any
	require.NoError(t, json.NewDecoder(testResp.Body).Decode(&testResult))
	testResp.Body.Close()
	require.Equal(t, "error", testResult["status"])
	require.True(t, testResult["error"] == "system one is disabled" || testResult["error"] == "system one is not configured")

	// 3. Mock System 1 backend
	mockBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/systemone", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "clef",
			"answers": map[string]any{
				"is_sql_injection": map[string]any{
					"type": "noul",
					"noul": 0.99,
				},
			},
			"usage": map[string]any{
				"input_tokens": 100,
			},
		})
	}))
	defer mockBackend.Close()

	u, err := url.Parse(mockBackend.URL)
	require.NoError(t, err)
	mockPort, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	// 4. PUT /v1/settings/systemone to configure it
	putBody, err := json.Marshal(store.SystemOneConfig{
		Host:    u.Hostname(),
		Port:    mockPort,
		Enabled: true,
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/systemone", bytes.NewReader(putBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	putResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, putResp.StatusCode)
	putResp.Body.Close()

	// 5. POST /v1/systemone/test with default example
	testResp2, err := http.Post(server.URL+"/v1/systemone/test", "application/json", bytes.NewReader([]byte("{}")))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, testResp2.StatusCode)

	var testResult2 map[string]any
	require.NoError(t, json.NewDecoder(testResp2.Body).Decode(&testResult2))
	testResp2.Body.Close()
	require.Equal(t, "ok", testResult2["status"])
	require.NotNil(t, testResult2["response"])

	// 6. Test direct router helper function InvokeSystemOne
	helperResp, err := router.InvokeSystemOne(ctx, map[string]any{
		"state": "test state",
		"questions": map[string]any{
			"is_sql_injection": map[string]any{"type": "noul"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "clef", helperResp.Model)
	noulVal, err := helperResp.Noul("is_sql_injection")
	require.NoError(t, err)
	require.InDelta(t, 0.99, noulVal, 0.001)
}
