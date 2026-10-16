package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestContextThresholdHTTP(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "router.sqlite")

	s, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer s.Close()

	router, err := New(Options{Store: s})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	// 1. GET returns default 10% / 20%
	resp, err := http.Get(server.URL + "/v1/settings/context-thresholds")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var getCfg store.ContextThresholdConfig
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&getCfg))
	require.Equal(t, 10, getCfg.WarningPercent)
	require.Equal(t, 20, getCfg.ResetPercent)
	require.NotNil(t, getCfg.ModelThresholds)
	require.Empty(t, getCfg.ModelThresholds)

	// 2. PUT valid configuration
	putPayload := store.ContextThresholdConfig{
		WarningPercent: 15,
		ResetPercent:   25,
		ModelThresholds: map[string]store.ModelContextThreshold{
			"google-vertex/gemini-3.8-flash": {
				WarningPercent: 12,
				ResetPercent:   22,
			},
		},
	}
	putBytes, err := json.Marshal(putPayload)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/context-thresholds", bytes.NewReader(putBytes))
	require.NoError(t, err)
	putResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode)

	var savedCfg store.ContextThresholdConfig
	require.NoError(t, json.NewDecoder(putResp.Body).Decode(&savedCfg))
	require.Equal(t, 15, savedCfg.WarningPercent)
	require.Equal(t, 25, savedCfg.ResetPercent)
	require.Equal(t, 12, savedCfg.ModelThresholds["google-vertex/gemini-3.8-flash"].WarningPercent)
	require.Equal(t, 22, savedCfg.ModelThresholds["google-vertex/gemini-3.8-flash"].ResetPercent)

	// 3. GET verifies persistence
	getResp2, err := http.Get(server.URL + "/v1/settings/context-thresholds")
	require.NoError(t, err)
	defer getResp2.Body.Close()
	require.Equal(t, http.StatusOK, getResp2.StatusCode)

	var checkCfg store.ContextThresholdConfig
	require.NoError(t, json.NewDecoder(getResp2.Body).Decode(&checkCfg))
	require.Equal(t, 15, checkCfg.WarningPercent)
	require.Equal(t, 25, checkCfg.ResetPercent)
	require.Equal(t, 12, checkCfg.ModelThresholds["google-vertex/gemini-3.8-flash"].WarningPercent)

	// 4. PUT invalid configuration cases
	invalidCases := []string{
		`{"warning_percent": 0, "reset_percent": 20}`,
		`{"warning_percent": 25, "reset_percent": 25}`,
		`{"warning_percent": 30, "reset_percent": 20}`,
		`{"warning_percent": 10, "reset_percent": 101}`,
		`{"warning_percent": 10, "reset_percent": 20, "model_thresholds": {"bad": {"warning_percent": 50, "reset_percent": 40}}}`,
		`{"warning_percent": 10, "reset_percent": 20, "model_thresholds": {"": {"warning_percent": 10, "reset_percent": 20}}}`,
		`not json`,
	}

	for _, tc := range invalidCases {
		badReq, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/context-thresholds", bytes.NewReader([]byte(tc)))
		require.NoError(t, err)
		badResp, err := http.DefaultClient.Do(badReq)
		require.NoError(t, err)
		io.Copy(io.Discard, badResp.Body)
		badResp.Body.Close()
		require.Equal(t, http.StatusBadRequest, badResp.StatusCode, "expected 400 for payload: %s", tc)
	}
}
