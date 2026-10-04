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
	"time"

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

	// 7. GET /v1/systemone when configured
	getSysResp, err := http.Get(server.URL + "/v1/systemone")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getSysResp.StatusCode)
	var getSysBody map[string]any
	require.NoError(t, json.NewDecoder(getSysResp.Body).Decode(&getSysBody))
	getSysResp.Body.Close()
	require.Equal(t, true, getSysBody["available"])

	// 8. POST /v1/systemone when configured (direct inference)
	validReq := map[string]any{
		"model": "clef",
		"state": "SELECT * FROM users",
		"questions": map[string]any{
			"is_sql_injection": map[string]any{"type": "noul"},
		},
	}
	validBody, err := json.Marshal(validReq)
	require.NoError(t, err)
	postSysResp, err := http.Post(server.URL+"/v1/systemone", "application/json", bytes.NewReader(validBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, postSysResp.StatusCode)
	var postSysBody map[string]any
	require.NoError(t, json.NewDecoder(postSysResp.Body).Decode(&postSysBody))
	postSysResp.Body.Close()
	require.Equal(t, "clef", postSysBody["model"])
	require.NotNil(t, postSysBody["answers"])
}

func TestSystemOneEndpoint_GracefulDegradation(t *testing.T) {
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

	// 1. Initially unconfigured: GET and POST /v1/systemone return 503
	getResp, err := http.Get(server.URL + "/v1/systemone")
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, getResp.StatusCode)
	var getBody map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&getBody))
	getResp.Body.Close()
	require.Equal(t, false, getBody["available"])
	require.Equal(t, "system one is not configured or disabled", getBody["error"])

	validReq := map[string]any{
		"state": "test input",
		"questions": map[string]any{
			"q1": map[string]any{"type": "noul"},
		},
	}
	validBytes, _ := json.Marshal(validReq)
	postResp, err := http.Post(server.URL+"/v1/systemone", "application/json", bytes.NewReader(validBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, postResp.StatusCode)
	var postBody map[string]any
	require.NoError(t, json.NewDecoder(postResp.Body).Decode(&postBody))
	postResp.Body.Close()
	require.Equal(t, false, postBody["available"])
	require.Equal(t, "system one is not configured or disabled", postBody["error"])

	// 2. Configure with a backend that fails (HTTP 500)
	failBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal clef error", http.StatusInternalServerError)
	}))
	defer failBackend.Close()

	u, err := url.Parse(failBackend.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	err = durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
		Host:    u.Hostname(),
		Port:    port,
		Enabled: true,
	})
	require.NoError(t, err)

	// Now GET /v1/systemone shows available (configured in store)
	getResp2, err := http.Get(server.URL + "/v1/systemone")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp2.StatusCode)
	getResp2.Body.Close()

	// POST /v1/systemone returns 502 Bad Gateway because backend failed
	postFailResp, err := http.Post(server.URL+"/v1/systemone", "application/json", bytes.NewReader(validBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, postFailResp.StatusCode)
	var postFailBody map[string]any
	require.NoError(t, json.NewDecoder(postFailResp.Body).Decode(&postFailBody))
	postFailResp.Body.Close()
	require.Equal(t, false, postFailBody["available"])
	require.Contains(t, postFailBody["error"], "system one backend call failed")

	// 3. Configure with an unreachable backend (connection refused)
	err = durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
		Host:    "127.0.0.1",
		Port:    65534, // closed port
		Enabled: true,
	})
	require.NoError(t, err)

	postUnreachResp, err := http.Post(server.URL+"/v1/systemone", "application/json", bytes.NewReader(validBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, postUnreachResp.StatusCode)
	var postUnreachBody map[string]any
	require.NoError(t, json.NewDecoder(postUnreachResp.Body).Decode(&postUnreachBody))
	postUnreachResp.Body.Close()
	require.Equal(t, false, postUnreachBody["available"])

	// 5. Timeout handling: backend hangs, client context times out -> 504
	slowBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowBackend.Close()

	slowU, err := url.Parse(slowBackend.URL)
	require.NoError(t, err)
	slowPort, err := strconv.Atoi(slowU.Port())
	require.NoError(t, err)

	err = durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
		Host:    slowU.Hostname(),
		Port:    slowPort,
		Enabled: true,
	})
	require.NoError(t, err)

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	timeoutReq, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, server.URL+"/v1/systemone", bytes.NewReader(validBytes))
	require.NoError(t, err)
	timeoutReq.Header.Set("Content-Type", "application/json")
	timeoutResp, err := http.DefaultClient.Do(timeoutReq)
	if err == nil {
		defer timeoutResp.Body.Close()
		require.Equal(t, http.StatusGatewayTimeout, timeoutResp.StatusCode)
	}

	// 6. Request validation: empty body and missing fields return 400
	emptyResp, err := http.Post(server.URL+"/v1/systemone", "application/json", bytes.NewReader([]byte("")))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, emptyResp.StatusCode)
	emptyResp.Body.Close()

	missingFieldsResp, err := http.Post(server.URL+"/v1/systemone", "application/json", bytes.NewReader([]byte(`{"state":"foo"}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, missingFieldsResp.StatusCode)
	missingFieldsResp.Body.Close()

	// 7. Method not allowed for other verbs
	delReq, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/systemone", nil)
	require.NoError(t, err)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusMethodNotAllowed, delResp.StatusCode)
	delResp.Body.Close()
}
