package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestManagedTerminateHTTPIsStrictReplayableAndNoStore(t *testing.T) {
	server, durable := newManagedHTTPTestServer(t)
	ctx := context.Background()
	launch := store.ManagedOperation{OperationID: "launch-termination-http", Method: http.MethodPost, CanonicalPath: "/v1/managed-agents", RequestID: "launch-termination-http", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "pending", ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{AgentID: "termination-http", Name: "termination-http", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: "manifest"}, launch))

	request := func(id string, body []byte) (int, []byte, http.Header) {
		req, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/managed-agents/termination-http", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("X-Request-ID", id)
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, payload, response.Header.Clone()
	}
	status, first, headers := request("123e4567-e89b-42d3-a456-426614174301", nil)
	require.Equal(t, http.StatusAccepted, status)
	require.Equal(t, "no-store", headers.Get("Cache-Control"))
	require.Equal(t, "123e4567-e89b-42d3-a456-426614174301", headers.Get("X-Request-ID"))
	status, replay, headers := request("123e4567-e89b-42d3-a456-426614174301", nil)
	require.Equal(t, http.StatusAccepted, status)
	require.Equal(t, first, replay)
	require.Equal(t, "no-store", headers.Get("Cache-Control"))

	bad, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/managed-agents/termination-http", bytes.NewBufferString(`{}`))
	require.NoError(t, err)
	bad.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174302")
	response, err := http.DefaultClient.Do(bad)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
}
