package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/crypto"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestCustomProviders_HTTP_Endpoints(t *testing.T) {
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

	// 1. GET /v1/custom-providers -> empty list
	resp, err := http.Get(server.URL + "/v1/custom-providers")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var listResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listResp))
	resp.Body.Close()
	providersList, ok := listResp["custom_providers"].([]any)
	require.True(t, ok)
	require.Empty(t, providersList)

	// 2. POST /v1/custom-providers -> create provider
	createPayload := map[string]any{
		"id":       "my-ollama",
		"label":    "My Local Ollama",
		"base_url": "http://127.0.0.1:11434/v1",
		"protocol": "openai-compatible",
		"api_key":  "secret-ollama-token",
		"enabled":  true,
		"models": []map[string]any{
			{
				"id":             "llama3.2:latest",
				"display_name":   "Llama 3.2",
				"context_window": 131072,
				"max_tokens":     8192,
				"supports_tools": false,
			},
			{
				"id":             "qwen2.5-coder:7b",
				"display_name":   "Qwen 2.5 Coder",
				"context_window": 32768,
				"max_tokens":     4096,
				"supports_tools": true,
			},
		},
	}
	createBytes, _ := json.Marshal(createPayload)

	resp, err = http.Post(server.URL+"/v1/custom-providers", "application/json", bytes.NewReader(createBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var createdResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&createdResp))
	resp.Body.Close()

	providerData, ok := createdResp["custom_provider"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "my-ollama", providerData["id"])
	require.Equal(t, "My Local Ollama", providerData["label"])
	require.Equal(t, "http://127.0.0.1:11434/v1", providerData["base_url"])
	require.Equal(t, true, providerData["has_api_key"])
	require.Equal(t, "sk-••••••••", providerData["api_key_masked"])
	require.Nil(t, providerData["api_key"])
	require.Nil(t, providerData["api_key_ciphertext"])

	modelsData, ok := providerData["models"].([]any)
	require.True(t, ok)
	require.Len(t, modelsData, 2)

	// Verify encryption in store
	stored, err := durable.GetCustomProvider(ctx, "my-ollama")
	require.NoError(t, err)
	require.NotNil(t, stored.Provider.APIKeyCiphertext)
	require.NotEqual(t, "secret-ollama-token", *stored.Provider.APIKeyCiphertext)

	key, err := crypto.DeriveKey("")
	require.NoError(t, err)
	decrypted, err := crypto.Decrypt(*stored.Provider.APIKeyCiphertext, key)
	require.NoError(t, err)
	require.Equal(t, "secret-ollama-token", string(decrypted))

	// 3. GET /v1/custom-providers -> returns created provider with redacted key
	resp, err = http.Get(server.URL + "/v1/custom-providers")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listResp))
	resp.Body.Close()
	providersList = listResp["custom_providers"].([]any)
	require.Len(t, providersList, 1)
	p0 := providersList[0].(map[string]any)
	require.Equal(t, "my-ollama", p0["id"])
	require.Equal(t, true, p0["has_api_key"])
	require.Equal(t, "sk-••••••••", p0["api_key_masked"])

	// 4. GET /v1/custom-providers/{id} -> get single provider with models
	resp, err = http.Get(server.URL + "/v1/custom-providers/my-ollama")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var getResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&getResp))
	resp.Body.Close()
	pGet := getResp["custom_provider"].(map[string]any)
	require.Equal(t, "my-ollama", pGet["id"])
	require.Equal(t, "My Local Ollama", pGet["label"])
	require.Len(t, pGet["models"].([]any), 2)

	// GET non-existent
	resp, err = http.Get(server.URL + "/v1/custom-providers/does-not-exist")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	// 5. PUT /v1/custom-providers/{id} -> update provider
	newKey := "new-secret-key"
	updatePayload := map[string]any{
		"label":    "Updated Ollama",
		"base_url": "http://127.0.0.1:11434/v1",
		"api_key":  newKey,
		"enabled":  false,
		"models": []map[string]any{
			{
				"id":             "deepseek-r1:14b",
				"display_name":   "DeepSeek R1",
				"context_window": 65536,
				"max_tokens":     8192,
				"supports_tools": true,
			},
		},
	}
	updateBytes, _ := json.Marshal(updatePayload)

	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/custom-providers/my-ollama", bytes.NewReader(updateBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var updateResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&updateResp))
	resp.Body.Close()

	pUpdated := updateResp["custom_provider"].(map[string]any)
	require.Equal(t, "Updated Ollama", pUpdated["label"])
	require.Equal(t, false, pUpdated["enabled"])
	require.Equal(t, true, pUpdated["has_api_key"])
	require.Len(t, pUpdated["models"].([]any), 1)

	// Verify updated key is encrypted with new value
	storedUpdated, err := durable.GetCustomProvider(ctx, "my-ollama")
	require.NoError(t, err)
	decryptedUpdated, err := crypto.Decrypt(*storedUpdated.Provider.APIKeyCiphertext, key)
	require.NoError(t, err)
	require.Equal(t, newKey, string(decryptedUpdated))

	// 6. DELETE /v1/custom-providers/{id} -> deletes provider
	req, err = http.NewRequest(http.MethodDelete, server.URL+"/v1/custom-providers/my-ollama", nil)
	require.NoError(t, err)

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Verify deleted
	resp, err = http.Get(server.URL + "/v1/custom-providers/my-ollama")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	// DELETE non-existent returns 404
	req, err = http.NewRequest(http.MethodDelete, server.URL+"/v1/custom-providers/my-ollama", nil)
	require.NoError(t, err)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

func TestCustomProviders_HTTP_Discover(t *testing.T) {
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

	var receivedAuthHeader string
	mockOpenAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		if r.URL.Path == "/v1/models" || r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"id": "qwen2.5-coder:7b", "object": "model"},
					{"id": "llama3.2:latest", "object": "model"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer mockOpenAIServer.Close()

	// 1. Discover models with mock OpenAI endpoint
	discoverPayload := map[string]any{
		"base_url": mockOpenAIServer.URL + "/v1",
		"api_key":  "test-api-key",
	}
	bodyBytes, _ := json.Marshal(discoverPayload)

	resp, err := http.Post(server.URL+"/v1/custom-providers/discover", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var discoverResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&discoverResp))
	resp.Body.Close()

	require.Equal(t, "Bearer test-api-key", receivedAuthHeader)

	modelsRaw, ok := discoverResp["models"].([]any)
	require.True(t, ok)
	require.Len(t, modelsRaw, 2)

	m0 := modelsRaw[0].(map[string]any)
	require.Equal(t, "qwen2.5-coder:7b", m0["id"])
	require.Equal(t, "qwen2.5-coder:7b", m0["display_name"])
	require.Equal(t, float64(131072), m0["context_window"])
	require.Equal(t, float64(8192), m0["max_tokens"])
	require.Equal(t, true, m0["supports_tools"])

	m1 := modelsRaw[1].(map[string]any)
	require.Equal(t, "llama3.2:latest", m1["id"])

	// 2. Discover models without /v1 in base_url
	discoverPayload = map[string]any{
		"base_url": mockOpenAIServer.URL,
	}
	bodyBytes, _ = json.Marshal(discoverPayload)
	resp, err = http.Post(server.URL+"/v1/custom-providers/discover", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 3. Discover with unreachable endpoint
	discoverPayload = map[string]any{
		"base_url": "http://127.0.0.1:59999", // Unreachable port
	}
	bodyBytes, _ = json.Marshal(discoverPayload)
	resp, err = http.Post(server.URL+"/v1/custom-providers/discover", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)

	var errResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	resp.Body.Close()
	require.Equal(t, "discovery_failed", errResp["code"])
}
