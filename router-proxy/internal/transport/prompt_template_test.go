package transport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestPromptTemplateHTTPAcceptsMetadataAndRejectsPortableMetadataErrors(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource("123e4567-e89b-42d3-a456-426614174000")})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Clock: clock, IDSource: deterministic.NewSequenceIDSource()})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	empty := promptRequest(t, server.Client(), http.MethodGet, server.URL+"/v1/prompt-templates", nil)
	require.Equal(t, float64(http.StatusOK), empty["status"])
	require.Equal(t, []any{}, empty["prompt_templates"])

	id := "legacy/Punctuation?%# mixed-case é"
	updated := "2029-12-31T23:59:59.999Z"
	created := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
		"id": id, "name": "portable", "body": "body", "updated_at": updated,
	})
	require.Equal(t, float64(http.StatusCreated), created["status"])
	response := created["prompt_template"].(map[string]any)
	require.Equal(t, id, response["id"])
	require.Equal(t, updated, response["updated_at"])
	require.Equal(t, "2030-01-01T00:00:00.000Z", response["created_at"])

	for _, value := range []any{"2030-01-01T00:00:00Z", "2030-01-01T00:00:00.00Z", nil, ""} {
		result := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
			"name": fmt.Sprintf("bad-%v", value), "body": "body", "updated_at": value,
		})
		require.Equal(t, float64(http.StatusBadRequest), result["status"])
		require.Equal(t, "invalid_prompt_template", result["error"].(map[string]any)["code"])
	}
	for _, key := range []string{"created_at", "surprise"} {
		result := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
			key: "metadata", "name": "bad-" + key, "body": "body",
		})
		require.Equal(t, "invalid_prompt_template", result["error"].(map[string]any)["code"])
	}

	patched := promptRequest(t, server.Client(), http.MethodPatch, server.URL+"/v1/prompt-templates/"+url.PathEscape(id), map[string]any{"updated_at": updated, "body": "patched"})
	require.Equal(t, float64(http.StatusOK), patched["status"])
	require.Equal(t, updated, patched["prompt_template"].(map[string]any)["updated_at"])
}

func TestPromptTemplateHTTPUsesOneDecodeForOpaqueIDs(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	for index, id := range []string{"slash/id", "percent%id", "question?id", "hash#id", "日本語"} {
		result := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
			"id": id, "name": fmt.Sprintf("name-%d", index), "body": "body",
		})
		require.Equal(t, float64(http.StatusCreated), result["status"])
		path := server.URL + "/v1/prompt-templates/" + url.PathEscape(id)
		fetched := promptRequest(t, server.Client(), http.MethodGet, path, nil)
		require.Equal(t, float64(http.StatusOK), fetched["status"])
		require.Equal(t, id, fetched["prompt_template"].(map[string]any)["id"])
		patched := promptRequest(t, server.Client(), http.MethodPatch, path, map[string]any{"body": "patched"})
		require.Equal(t, float64(http.StatusOK), patched["status"])
		response, err := server.Client().Do(mustRequest(t, http.MethodDelete, path, nil))
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, response.StatusCode)
		response.Body.Close()
	}
}

func promptRequest(t *testing.T, client *http.Client, method, endpoint string, body map[string]any) map[string]any {
	t.Helper()
	request := mustRequest(t, method, endpoint, body)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	result := map[string]any{"status": float64(response.StatusCode)}
	if response.StatusCode != http.StatusNoContent {
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	}
	return result
}

func mustRequest(t *testing.T, method, endpoint string, body map[string]any) *http.Request {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		require.NoError(t, err)
	}
	request, err := http.NewRequest(method, endpoint, bytesReader(payload))
	require.NoError(t, err)
	return request
}
