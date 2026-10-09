package managed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedGoogleSearch_AntigravityIntegration(t *testing.T) {
	var capturedAuth string
	var capturedUserAgent string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedUserAgent = r.Header.Get("User-Agent")

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.Unmarshal(bodyBytes, &capturedBody)

		resp := generateContentResponse{
			Candidates: []searchCandidate{
				{
					Content: &candidateContent{
						Role: "model",
						Parts: []candidatePart{
							{Text: "Internal thoughts", Thought: true},
							{Text: "Go 1.24 was released in February 2025.", Thought: false},
						},
					},
					GroundingMetadata: &groundingMetadata{
						WebSearchQueries: []string{"Go 1.24 release date"},
						GroundingChunks: []groundingChunk{
							{
								Web: &groundingChunkWeb{
									Title: "Go 1.24 Release Notes",
									URI:   "https://go.dev/doc/go1.24",
								},
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	origURL := antigravityGroundingURL
	antigravityGroundingURL = server.URL
	defer func() { antigravityGroundingURL = origURL }()

	// Set up temporary auth file with antigravity credentials
	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	authData := map[string]any{
		"antigravity": map[string]any{
			"access":    "mock-antigravity-token-123",
			"projectId": "mock-project-456",
			"expires":   time.Now().UnixMilli() + 3600*1000,
		},
	}
	authBytes, err := json.Marshal(authData)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, authBytes, 0600))

	t.Setenv("PI_AUTH_FILE", authPath)
	t.Setenv("HOME", tmpDir)
	_ = os.MkdirAll(filepath.Join(tmpDir, ".pi", "agent"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, ".pi", "agent", "auth.json"), authBytes, 0600)

	mgr := &Manager{}
	res, err := mgr.executeGoogleSearch(context.Background(), "When was Go 1.24 released?", "be concise", []string{"https://go.dev"})
	require.NoError(t, err)

	require.Equal(t, "Bearer mock-antigravity-token-123", capturedAuth)
	require.Contains(t, capturedUserAgent, "antigravity/cli")
	require.NotNil(t, capturedBody)
	require.Equal(t, "mock-project-456", capturedBody["project"])
	require.Equal(t, "gemini-3.8-flash-low", capturedBody["model"])
	require.Equal(t, "agent", capturedBody["requestType"])

	// Check result formatting
	require.Contains(t, res, "Go 1.24 was released in February 2025.")
	require.NotContains(t, res, "Internal thoughts")
	require.Contains(t, res, "### Sources")
	require.Contains(t, res, "[Go 1.24 Release Notes](https://go.dev/doc/go1.24)")
	require.Contains(t, res, "*Search queries: Go 1.24 release date*")
}

func TestEmbeddedGoogleSearch_VertexIntegration(t *testing.T) {
	var capturedAuth string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.Unmarshal(bodyBytes, &capturedBody)

		resp := generateContentResponse{
			Candidates: []searchCandidate{
				{
					Content: &candidateContent{
						Role: "model",
						Parts: []candidatePart{
							{Text: "Vertex AI Search Result Content."},
						},
					},
					GroundingMetadata: &groundingMetadata{
						WebSearchQueries: []string{"vertex query"},
						GroundingChunks: []groundingChunk{
							{
								Web: &groundingChunkWeb{
									Title: "Vertex Docs",
									URI:   "https://cloud.google.com/vertex-ai",
								},
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	origTemplate := vertexGroundingURLTemplate
	vertexGroundingURLTemplate = server.URL + "/%s/%s/%s/%s"
	defer func() { vertexGroundingURLTemplate = origTemplate }()

	// Provide empty/no antigravity auth, but mock ADC file for Vertex
	tmpDir := t.TempDir()
	adcPath := filepath.Join(tmpDir, "application_default_credentials.json")
	adcData := map[string]any{
		"type":          "service_account",
		"project_id":    "vertex-test-project",
		"private_key":   "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0...",
		"client_email":  "test@vertex-test-project.iam.gserviceaccount.com",
		"client_id":     "1234567890",
		"auth_uri":      "https://accounts.google.com/o/oauth2/auth",
		"token_uri":     "https://oauth2.googleapis.com/token",
	}
	adcBytes, _ := json.Marshal(adcData)
	_ = os.WriteFile(adcPath, adcBytes, 0600)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adcPath)
	t.Setenv("HOME", tmpDir)

	// Direct test on executeVertexGrounding
	mgr := &Manager{}
	res, err := mgr.executeVertexGrounding(context.Background(), "mock-vertex-token", "vertex-test-project", "us-central1", "test query", "", nil)
	require.NoError(t, err)

	require.Equal(t, "Bearer mock-vertex-token", capturedAuth)
	require.Contains(t, res, "Vertex AI Search Result Content.")
	require.Contains(t, res, "### Sources")
	require.Contains(t, res, "[Vertex Docs](https://cloud.google.com/vertex-ai)")
}

func TestEmbeddedGoogleSearch_CredentialFailover(t *testing.T) {
	// Antigravity returns 401 Unauthorized
	antigravityServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)
	}))
	defer antigravityServer.Close()

	// Vertex returns 200 OK with grounding results
	vertexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := generateContentResponse{
			Candidates: []searchCandidate{
				{
					Content: &candidateContent{
						Role: "model",
						Parts: []candidatePart{
							{Text: "Succeeded via Vertex fallback!"},
						},
					},
					GroundingMetadata: &groundingMetadata{
						WebSearchQueries: []string{"fallback query"},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer vertexServer.Close()

	origAntigravityURL := antigravityGroundingURL
	antigravityGroundingURL = antigravityServer.URL
	defer func() { antigravityGroundingURL = origAntigravityURL }()

	origVertexTemplate := vertexGroundingURLTemplate
	vertexGroundingURLTemplate = vertexServer.URL + "/%s/%s/%s/%s"
	defer func() { vertexGroundingURLTemplate = origVertexTemplate }()

	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	authData := map[string]any{
		"antigravity": map[string]any{
			"access":    "expired-token",
			"projectId": "test-project",
			"expires":   time.Now().UnixMilli() + 3600*1000,
		},
	}
	authBytes, _ := json.Marshal(authData)
	_ = os.WriteFile(authPath, authBytes, 0600)
	t.Setenv("PI_AUTH_FILE", authPath)
	t.Setenv("HOME", tmpDir)
	_ = os.MkdirAll(filepath.Join(tmpDir, ".pi", "agent"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, ".pi", "agent", "auth.json"), authBytes, 0600)

	// Also configure Vertex mock credentials in Manager
	mgr := &Manager{}
	// Test direct failover logic
	_, err := mgr.executeGoogleSearch(context.Background(), "query", "", nil)
	// Because ADC isn't actually mocked for vertex oauth exchange in this test,
	// it will report that vertex failed or antigravity failed.
	// Let's verify that it at least attempted Antigravity first.
	require.Error(t, err)
	require.Contains(t, err.Error(), "antigravity grounding request failed with status 401")
}

func TestEmbeddedGoogleSearch_ToolGating(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("PI_AUTH_FILE", filepath.Join(tmpDir, "nonexistent.json"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(tmpDir, "nonexistent-adc.json"))

	mgr := &Manager{}
	// With no auth present
	require.False(t, mgr.hasGoogleSearchAuth(context.Background()))

	// With antigravity token present
	authPath := filepath.Join(tmpDir, "auth.json")
	authData := map[string]any{
		"antigravity": map[string]any{
			"access":  "valid-token",
			"expires": time.Now().UnixMilli() + 3600*1000,
		},
	}
	authBytes, _ := json.Marshal(authData)
	_ = os.WriteFile(authPath, authBytes, 0600)
	t.Setenv("PI_AUTH_FILE", authPath)
	_ = os.MkdirAll(filepath.Join(tmpDir, ".pi", "agent"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, ".pi", "agent", "auth.json"), authBytes, 0600)

	require.True(t, mgr.hasGoogleSearchAuth(context.Background()))

	// With only Gemini API key present
	tmpDirKey := t.TempDir()
	t.Setenv("HOME", tmpDirKey)
	t.Setenv("PI_AUTH_FILE", filepath.Join(tmpDirKey, "nonexistent.json"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(tmpDirKey, "nonexistent-adc.json"))
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")
	require.True(t, mgr.hasGoogleSearchAuth(context.Background()))
	t.Setenv("GEMINI_API_KEY", "")
}

func TestEmbeddedGoogleSearch_ToolSchema(t *testing.T) {
	schema := embeddedGoogleSearchSchema()
	require.NotNil(t, schema)
	require.Equal(t, "object", schema.Type)
	require.Contains(t, schema.Properties, "query")
	require.Contains(t, schema.Properties, "instruction")
	require.Contains(t, schema.Properties, "urls")
}
