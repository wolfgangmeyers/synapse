package managed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedVertexAdmission(t *testing.T) {
	m := &Manager{}
	model, err := m.admitEmbeddedModel(context.Background(), Model{
		Provider: "google-vertex",
		ID:       "gemini-2.5-flash",
	})
	require.NoError(t, err)
	require.Equal(t, "google-vertex", model.Provider)
	require.Equal(t, "gemini-2.5-flash", model.ID)
}

func TestEmbeddedVertexAuthAuthorizedUser(t *testing.T) {
	tempDir := t.TempDir()

	// Mock token server
	var requestedGrantType, requestedRefreshToken string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, r.ParseForm())
		requestedGrantType = r.FormValue("grant_type")
		requestedRefreshToken = r.FormValue("refresh_token")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-vertex-adc-access-token",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	}))
	defer tokenServer.Close()

	adcContent := map[string]any{
		"type":          "authorized_user",
		"client_id":     "fake-client-id",
		"client_secret": "fake-client-secret",
		"refresh_token": "fake-refresh-token-12345",
		"token_uri":     tokenServer.URL,
		"project_id":    "test-project-adc",
	}
	adcBytes, err := json.Marshal(adcContent)
	require.NoError(t, err)

	adcFile := filepath.Join(tempDir, "application_default_credentials.json")
	require.NoError(t, os.WriteFile(adcFile, adcBytes, 0o600))

	authJSONContent := map[string]any{
		"google-vertex": map[string]any{
			"type": "api_key",
			"env": map[string]any{
				"GOOGLE_APPLICATION_CREDENTIALS": adcFile,
				"GOOGLE_CLOUD_PROJECT":           "test-project-auth",
				"GOOGLE_CLOUD_LOCATION":          "us-central1",
			},
		},
	}
	authJSONBytes, err := json.Marshal(authJSONContent)
	require.NoError(t, err)

	piAgentDir := filepath.Join(tempDir, "agent")
	require.NoError(t, os.MkdirAll(piAgentDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(piAgentDir, "auth.json"), authJSONBytes, 0o600))

	m := &Manager{
		config: Config{
			PiAgentDir:  piAgentDir,
			ManagedHome: tempDir,
		},
	}

	// Reset cached token
	currentVertexToken = vertexTokenCache{}

	token, projectID, location, err := m.embeddedVertexAuth(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "fake-vertex-adc-access-token", token)
	require.Equal(t, "test-project-auth", projectID)
	require.Equal(t, "us-central1", location)
	require.Equal(t, "refresh_token", requestedGrantType)
	require.Equal(t, "fake-refresh-token-12345", requestedRefreshToken)
}

func TestEmbeddedVertexStreamGenerateContent(t *testing.T) {
	tempDir := t.TempDir()

	// Mock token server
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-vertex-token",
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	}))
	defer tokenServer.Close()

	adcContent := map[string]any{
		"type":          "authorized_user",
		"refresh_token": "fake-refresh-token",
		"token_uri":     tokenServer.URL,
		"project_id":    "vertex-proj-99",
	}
	adcBytes, _ := json.Marshal(adcContent)
	adcFile := filepath.Join(tempDir, "adc.json")
	require.NoError(t, os.WriteFile(adcFile, adcBytes, 0o600))

	// Mock Vertex AI streamGenerateContent server
	var capturedAuthHeader string
	var capturedQuery string
	vertexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		capturedQuery = r.URL.RawQuery

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		ssePayload := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"thought\":true,\"text\":\"Thinking step...\"},{\"text\":\"Hello from Vertex ADC!\"}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":20,\"totalTokenCount\":30}}}\n\n"
		_, _ = w.Write([]byte(ssePayload))
		flusher.Flush()
	}))
	defer vertexServer.Close()

	origTemplate := vertexGenerateContentURLTemplate
	defer func() { vertexGenerateContentURLTemplate = origTemplate }()
	vertexGenerateContentURLTemplate = vertexServer.URL + "?location=%s&project=%s&location2=%s&model=%s"

	authJSONContent := map[string]any{
		"google-vertex": map[string]any{
			"type": "api_key",
			"env": map[string]any{
				"GOOGLE_APPLICATION_CREDENTIALS": adcFile,
				"GOOGLE_CLOUD_PROJECT":           "vertex-proj-99",
				"GOOGLE_CLOUD_LOCATION":          "us-east4",
			},
		},
	}
	authJSONBytes, _ := json.Marshal(authJSONContent)
	piAgentDir := filepath.Join(tempDir, "agent")
	require.NoError(t, os.MkdirAll(piAgentDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(piAgentDir, "auth.json"), authJSONBytes, 0o600))

	m := &Manager{
		config: Config{
			PiAgentDir:  piAgentDir,
			ManagedHome: tempDir,
		},
	}

	currentVertexToken = vertexTokenCache{}

	reqModel := &ai.Model{
		Provider: "google-vertex",
		ID:       "gemini-2.5-flash",
	}
	transcript := ai.TranscriptContext{
		Messages: []ai.Message{
			ai.UserMessage{
				Content: []ai.Content{
					ai.TextContent{Text: "Ping Vertex"},
				},
			},
		},
	}

	stream := m.streamEmbeddedVertex(context.Background(), reqModel, transcript, nil)
	var finalMsg *ai.AssistantMessage
	var sawThinkingDelta, sawTextDelta bool

	for ev := range stream.Events() {
		switch ev.Type {
		case ai.EventThinkingDelta:
			sawThinkingDelta = true
		case ai.EventTextDelta:
			sawTextDelta = true
		case ai.EventDone:
			finalMsg = ev.Message
		case ai.EventError:
			if ev.Error != nil {
				t.Fatalf("stream error: %s", ev.Error.ErrorMessage)
			}
			t.Fatalf("stream error event without message")
		}
	}

	require.NotNil(t, finalMsg)
	require.True(t, sawThinkingDelta)
	require.True(t, sawTextDelta)
	require.Equal(t, "Bearer fake-vertex-token", capturedAuthHeader)
	require.True(t, strings.Contains(capturedQuery, "vertex-proj-99"))
	require.True(t, strings.Contains(capturedQuery, "us-east4"))
	require.True(t, strings.Contains(capturedQuery, "gemini-2.5-flash"))

	var textParts []string
	for _, c := range finalMsg.Content {
		if tc, ok := c.(ai.TextContent); ok {
			textParts = append(textParts, tc.Text)
		}
	}
	require.Equal(t, "Hello from Vertex ADC!", strings.Join(textParts, ""))
}
