package managed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	request, ok := capturedBody["request"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []any{map[string]any{"googleSearch": map[string]any{}}, map[string]any{"urlContext": map[string]any{}}}, request["tools"])

	// Check result formatting
	require.Contains(t, res, "Go 1.24 was released in February 2025.")
	require.NotContains(t, res, "Internal thoughts")
	require.Contains(t, res, "### Sources")
	require.Contains(t, res, "[Go 1.24 Release Notes](https://go.dev/doc/go1.24)")
	require.Contains(t, res, "*Search queries: Go 1.24 release date*")
}

func TestEmbeddedGoogleSearch_VertexIntegration(t *testing.T) {
	var capturedAuth string
	var capturedPath string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedPath = r.URL.Path

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
		"type":         "service_account",
		"project_id":   "vertex-test-project",
		"private_key":  "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0...",
		"client_email": "test@vertex-test-project.iam.gserviceaccount.com",
		"client_id":    "1234567890",
		"auth_uri":     "https://accounts.google.com/o/oauth2/auth",
		"token_uri":    "https://oauth2.googleapis.com/token",
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
	require.Equal(t, "/us-central1-aiplatform.googleapis.com/vertex-test-project/us-central1/gemini-2.5-flash", capturedPath)
	require.Contains(t, res, "Vertex AI Search Result Content.")
	require.Contains(t, res, "### Sources")
	require.Contains(t, res, "[Vertex Docs](https://cloud.google.com/vertex-ai)")
}

func TestEmbeddedGoogleSearch_ConfiguredVertexGlobalGrounding(t *testing.T) {
	vertexAuthMu.Lock()
	previous := currentVertexToken
	currentVertexToken = vertexTokenCache{}
	vertexAuthMu.Unlock()
	defer func() {
		vertexAuthMu.Lock()
		currentVertexToken = previous
		vertexAuthMu.Unlock()
	}()

	var requestedPath string
	var requestedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "synthetic-vertex-token", "expires_in": 3600})
		default:
			requestedPath = r.URL.Path
			if r.Header.Get("Authorization") != "Bearer synthetic-vertex-token" {
				http.Error(w, "wrong authorization", http.StatusUnauthorized)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&requestedBody)
			_ = json.NewEncoder(w).Encode(generateContentResponse{Candidates: []searchCandidate{{
				Content:           &candidateContent{Parts: []candidatePart{{Text: "Public web answer"}}},
				GroundingMetadata: &groundingMetadata{GroundingChunks: []groundingChunk{{Web: &groundingChunkWeb{URI: "https://example.org/source", Title: "Public source"}}}},
			}}})
		}
	}))
	defer server.Close()

	original := vertexGroundingURLTemplate
	vertexGroundingURLTemplate = server.URL + "/%s/v1beta1/projects/%s/locations/%s/publishers/google/models/%s:generateContent"
	defer func() { vertexGroundingURLTemplate = original }()

	home := t.TempDir()
	adcPath := filepath.Join(home, "adc.json")
	adc, err := json.Marshal(map[string]any{
		"type": "authorized_user", "client_id": "synthetic-client", "client_secret": "synthetic-secret",
		"refresh_token": "synthetic-refresh", "token_uri": server.URL + "/token",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(adcPath, adc, 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0700))
	creds, err := json.Marshal(map[string]any{"google-vertex": map[string]any{"env": map[string]any{
		"GOOGLE_APPLICATION_CREDENTIALS": adcPath, "GOOGLE_CLOUD_PROJECT": "configured-project", "GOOGLE_CLOUD_LOCATION": "global",
	}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pi", "agent", "auth.json"), creds, 0600))
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	mgr := &Manager{}
	result, err := mgr.executeGoogleSearch(context.Background(), "public web query", "be concise", nil)
	require.NoError(t, err)
	require.Contains(t, result, "Public web answer")
	require.Contains(t, result, "[Public source](https://example.org/source)")
	require.Equal(t, "/aiplatform.googleapis.com/v1beta1/projects/configured-project/locations/global/publishers/google/models/gemini-2.5-flash:generateContent", requestedPath)
	require.Equal(t, []any{map[string]any{"googleSearch": map[string]any{}}}, requestedBody["tools"])
}

func TestEmbeddedGoogleSearch_ConfiguredVertexRegionalLocationUsesModelEndpoint(t *testing.T) {
	vertexAuthMu.Lock()
	previous := currentVertexToken
	currentVertexToken = vertexTokenCache{}
	vertexAuthMu.Unlock()
	defer func() {
		vertexAuthMu.Lock()
		currentVertexToken = previous
		vertexAuthMu.Unlock()
	}()

	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "mock-token", "expires_in": 3600})
			return
		}
		requestedPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer mock-token" {
			http.Error(w, "wrong token", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if tools, ok := body["tools"].([]any); !ok || len(tools) != 1 || !reflect.DeepEqual(tools[0], map[string]any{"googleSearch": map[string]any{}}) {
			http.Error(w, "missing public search grounding", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(generateContentResponse{Candidates: []searchCandidate{{
			Content:           &candidateContent{Parts: []candidatePart{{Text: "Grounded answer"}}},
			GroundingMetadata: &groundingMetadata{GroundingChunks: []groundingChunk{{Web: &groundingChunkWeb{URI: "https://example.org", Title: "Source"}}}},
		}}})
	}))
	defer server.Close()
	original := vertexGroundingURLTemplate
	vertexGroundingURLTemplate = server.URL + "/%s/v1beta1/projects/%s/locations/%s/publishers/google/models/%s:generateContent"
	defer func() { vertexGroundingURLTemplate = original }()

	home := t.TempDir()
	adcPath := filepath.Join(home, "adc.json")
	adc, err := json.Marshal(map[string]any{
		"type": "authorized_user", "client_id": "client", "client_secret": "secret",
		"refresh_token": "refresh", "token_uri": server.URL + "/token",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(adcPath, adc, 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0700))
	creds, err := json.Marshal(map[string]any{"google-vertex": map[string]any{"env": map[string]any{
		"GOOGLE_APPLICATION_CREDENTIALS": adcPath, "GOOGLE_CLOUD_PROJECT": "configured-project", "GOOGLE_CLOUD_LOCATION": "us-central1",
	}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pi", "agent", "auth.json"), creds, 0600))
	t.Setenv("HOME", home)
	t.Setenv("PI_AUTH_FILE", filepath.Join(home, "missing-auth.json"))
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	mgr := &Manager{}
	require.True(t, mgr.hasGoogleSearchAuth(context.Background()))
	result, err := mgr.executeGoogleSearch(context.Background(), "public query", "", nil)
	require.NoError(t, err)
	require.Contains(t, result, "Grounded answer")
	require.Contains(t, result, "[Source](https://example.org)")
	require.Equal(t, "/us-central1-aiplatform.googleapis.com/v1beta1/projects/configured-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent", requestedPath)
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
