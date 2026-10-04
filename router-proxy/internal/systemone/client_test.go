package systemone

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSystemOneClient_NotConfigured(t *testing.T) {
	client := NewClient(nil)
	ctx := context.Background()

	// Disabled
	_, err := client.Invoke(ctx, Config{Host: "127.0.0.1", Port: 11434, Enabled: false}, Request{})
	require.ErrorIs(t, err, ErrDisabled)

	// Enabled but empty host
	_, err = client.Invoke(ctx, Config{Host: "", Port: 11434, Enabled: true}, Request{})
	require.ErrorIs(t, err, ErrNotConfigured)

	// Enabled but zero port
	_, err = client.Invoke(ctx, Config{Host: "127.0.0.1", Port: 0, Enabled: true}, Request{})
	require.ErrorIs(t, err, ErrNotConfigured)
}

func TestSystemOneClient_InvokeSuccess(t *testing.T) {
	mockResponse := map[string]any{
		"model": "clef",
		"answers": map[string]any{
			"is_sql_injection": map[string]any{
				"type": "noul",
				"noul": 0.985,
			},
			"category": map[string]any{
				"type":       "choice",
				"choice":     "infrastructure",
				"confidence": 0.92,
			},
			"rating": map[string]any{
				"type":  "score",
				"score": 3.8,
			},
		},
		"usage": map[string]any{
			"input_tokens":  150,
			"output_tokens": 0,
		},
	}

	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/systemone", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&receivedBody))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	client := NewClient(server.Client())
	ctx := context.Background()

	req := Request{
		State: "SELECT * FROM users WHERE id = 1",
		Questions: map[string]any{
			"is_sql_injection": map[string]any{
				"type": "noul",
			},
		},
	}

	cfg := Config{
		Host:    u.Hostname(),
		Port:    port,
		Enabled: true,
	}

	resp, err := client.Invoke(ctx, cfg, req)
	require.NoError(t, err)
	require.Equal(t, "clef", resp.Model)
	require.NotNil(t, resp.Raw)

	// Check model defaulted to clef in received request
	require.Equal(t, "clef", receivedBody["model"])

	// Check typed accessors
	noul, err := resp.Noul("is_sql_injection")
	require.NoError(t, err)
	require.InDelta(t, 0.985, noul, 0.001)

	choice, err := resp.Choice("category")
	require.NoError(t, err)
	require.Equal(t, "infrastructure", choice)

	conf, err := resp.Confidence("category")
	require.NoError(t, err)
	require.InDelta(t, 0.92, conf, 0.001)

	score, err := resp.Score("rating")
	require.NoError(t, err)
	require.InDelta(t, 3.8, score, 0.001)
}

func TestSystemOneClient_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	client := NewClient(server.Client())
	ctx := context.Background()

	cfg := Config{
		Host:    u.Hostname(),
		Port:    port,
		Enabled: true,
	}

	_, err = client.Invoke(ctx, cfg, Request{State: "foo", Questions: "bar"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackendFailed)
	require.Contains(t, err.Error(), "status 400")
	require.Contains(t, err.Error(), "model not found")
}

func TestSystemOneClient_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	client := NewClient(&http.Client{Timeout: 10 * time.Millisecond})
	ctx := context.Background()

	cfg := Config{
		Host:    u.Hostname(),
		Port:    port,
		Enabled: true,
	}

	_, err = client.Invoke(ctx, cfg, Request{State: "foo", Questions: "bar"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrTimeout)
}

func TestSystemOneClient_ConnectionRefused(t *testing.T) {
	// Use an unallocated/closed local port
	client := NewClient(&http.Client{Timeout: 500 * time.Millisecond})
	ctx := context.Background()

	cfg := Config{
		Host:    "127.0.0.1",
		Port:    65534,
		Enabled: true,
	}

	_, err := client.Invoke(ctx, cfg, Request{State: "foo", Questions: "bar"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBackendFailed)
}

func TestSystemOneClient_WithImages(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&receivedBody))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "clef",
			"answers": map[string]any{
				"has_cat": map[string]any{"type": "noul", "noul": 0.99},
			},
		})
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	client := NewClient(server.Client())
	ctx := context.Background()

	req := Request{
		Model:     "clef",
		State:     "Look at this picture",
		Questions: map[string]any{"has_cat": map[string]any{"type": "noul"}},
		Images:    []string{"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="},
	}

	cfg := Config{
		Host:    u.Hostname(),
		Port:    port,
		Enabled: true,
	}

	resp, err := client.Invoke(ctx, cfg, req)
	require.NoError(t, err)
	require.Equal(t, "clef", resp.Model)
	require.NotNil(t, receivedBody["images"])
	imgs, ok := receivedBody["images"].([]any)
	require.True(t, ok)
	require.Len(t, imgs, 1)
}
