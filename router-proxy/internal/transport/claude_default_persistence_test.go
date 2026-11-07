package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/stretchr/testify/require"
)

func TestClaudeDefaultHTTPValidationPersistenceReplayAndClear(t *testing.T) {
	db, _, _, server := newLaunchValidationServer(t, managed.CatalogFromModels(), &launchValidationRPCFactory{})
	ctx := context.Background()
	put := func(id, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/default-model", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", id)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(b)
	}
	const id = "123e4567-e89b-42d3-a456-426614174101"
	const body = `{"model":{"provider":"claude-code","id":"sonnet"}}`
	status, response := put(id, body)
	require.Equal(t, http.StatusOK, status, response)
	status, replay := put(id, body)
	require.Equal(t, http.StatusOK, status, replay)
	require.Equal(t, response, replay)
	status, _ = put(id, `{"model":{"provider":"claude-code","id":"opus"}}`)
	require.Equal(t, http.StatusConflict, status)
	for i, alias := range []string{"unknown", "bad alias"} {
		ids := []string{"123e4567-e89b-42d3-a456-426614174102", "123e4567-e89b-42d3-a456-426614174103"}
		payload, err := json.Marshal(map[string]any{"model": map[string]string{"provider": "claude-code", "id": alias}})
		require.NoError(t, err)
		status, _ = put(ids[i], string(payload))
		require.Equal(t, http.StatusUnprocessableEntity, status)
	}
	saved, err := db.GetDefaultModel(ctx)
	require.NoError(t, err)
	require.Equal(t, "claude-code", saved.Provider)
	require.Equal(t, "sonnet", saved.ID)
	resp, err := http.Get(server.URL + "/v1/models")
	require.NoError(t, err)
	defer resp.Body.Close()
	var snapshot struct {
		DefaultModel *managed.Model `json:"default_model"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&snapshot))
	require.NotNil(t, snapshot.DefaultModel)
	require.Equal(t, "sonnet", snapshot.DefaultModel.ID)
	status, _ = put("123e4567-e89b-42d3-a456-426614174104", `{"model":null}`)
	require.Equal(t, http.StatusOK, status)
	saved, err = db.GetDefaultModel(ctx)
	require.NoError(t, err)
	require.Nil(t, saved)
}
