package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

// IR-001, IR-003: the option defaults off and omission on update preserves it.
func TestImplicitReturnHTTPConfiguration(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	router, err := transport.New(transport.Options{Store: db})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	call := func(method, path string, payload map[string]any, status int) map[string]any {
		t.Helper()
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		require.Equal(t, status, res.StatusCode)
		var result map[string]any
		require.NoError(t, json.NewDecoder(res.Body).Decode(&result))
		return result
	}
	created := call("POST", "/v1/workflow-states", map[string]any{"name": "return-test"}, http.StatusCreated)["workflow_state"].(map[string]any)
	require.Equal(t, false, created["allow_implicit_return"])
	id := created["id"].(string)
	path := "/v1/workflow-states/" + id
	enabled := call("PUT", path, map[string]any{"name": "return-test", "allow_implicit_return": true}, http.StatusOK)["workflow_state"].(map[string]any)
	require.Equal(t, true, enabled["allow_implicit_return"])
	preserved := call("PUT", path, map[string]any{"name": "return-test"}, http.StatusOK)["workflow_state"].(map[string]any)
	require.Equal(t, true, preserved["allow_implicit_return"])
	got := call("GET", path, nil, http.StatusOK)["workflow_state"].(map[string]any)
	require.Equal(t, true, got["allow_implicit_return"])
	disabled := call("PUT", path, map[string]any{"name": "return-test", "allow_implicit_return": false}, http.StatusOK)["workflow_state"].(map[string]any)
	require.Equal(t, false, disabled["allow_implicit_return"])
}

// IR-003: invalid flag types are rejected on both create and update without mutation.
func TestImplicitReturnHTTPInvalidTypes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	router, err := transport.New(transport.Options{Store: db})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()
	yes := true
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "existing", AllowImplicitReturn: &yes})
	require.NoError(t, err)
	for _, value := range []string{`null`, `1`, `"true"`, `{}`, `[]`} {
		for _, request := range []struct{ method, path string }{{"POST", "/v1/workflow-states"}, {"PUT", "/v1/workflow-states/" + state.ID}} {
			t.Run(request.method+value, func(t *testing.T) {
				body := `{"name":"existing","allow_implicit_return":` + value + `}`
				req, err := http.NewRequest(request.method, server.URL+request.path, bytes.NewBufferString(body))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				resp, err := server.Client().Do(req)
				require.NoError(t, err)
				defer resp.Body.Close()
				require.Equal(t, http.StatusBadRequest, resp.StatusCode)
				got, err := db.GetWorkflowState(ctx, state.ID)
				require.NoError(t, err)
				require.True(t, got.AllowImplicitReturn)
			})
		}
	}
}
