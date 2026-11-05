package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

type securityHTTPTransport func(*http.Request) (*http.Response, error)

func (f securityHTTPTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// No listener, fake resolver, provider service or credentials outside this test.
func TestEmbeddedPublicHTTPAcceptsGoogleAndRejectsCredentialTree(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "offline-google-ambient-key")
	ctx := context.Background()
	base := testfixture.PrivateDir(t)
	project := filepath.Join(base, "private-project")
	require.NoError(t, os.Mkdir(project, 0700))
	credentialPath := filepath.Join(base, "private", "credential.json")
	credentials, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	require.NoError(t, credentials.Save(ctx, store.CodexCredential{AccessToken: "http-private-secret-sentinel", RefreshToken: "offline-refresh", AccountID: "offline-account", ExpiresAt: time.Now().Add(time.Hour)}))
	beforeBytes, err := os.ReadFile(credentialPath)
	require.NoError(t, err)
	db, err := store.Open(ctx, filepath.Join(base, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	_, err = db.CommitManagedProviderSnapshot(ctx, []store.ManagedProviderStatus{
		{Provider: "openai-codex", Label: "ChatGPT", Status: "signed_out"},
		{Provider: "google", Label: "Google", Status: "authenticated", Authenticated: true},
		{Provider: "cursor", Label: "Cursor", Status: "signed_out"},
		{Provider: "google-vertex", Label: "Google Vertex AI", Status: "signed_out"},
	}, []store.ManagedProviderModel{{Provider: "google", ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Available: true}})
	require.NoError(t, err)
	var calls atomic.Int32
	offline := &http.Client{Transport: securityHTTPTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("offline provider fixture")), Request: r}, nil
	})}
	// Embedded Google uses a fresh client and bypasses the SDK provider hook.
	// Intercept all default-transport requests, not just a specific host.
	originalTransport := http.DefaultTransport
	http.DefaultTransport = securityHTTPTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return offline.Transport.RoundTrip(r)
	})
	defer func() { http.DefaultTransport = originalTransport }()
	google, ok := ai.GetApiProvider("google-generative-ai")
	require.True(t, ok)
	original := google.StreamSimple
	google.StreamSimple = func(callCtx context.Context, model *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		calls.Add(1)
		copy := ai.SimpleStreamOptions{}
		if opts != nil {
			copy = *opts
		}
		copy.HTTPClient = offline
		return original(callCtx, model, transcript, &copy)
	}
	ai.RegisterApiProvider(google)
	defer func() { google.StreamSimple = original; ai.RegisterApiProvider(google) }()
	m, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: mustEffectManifest(t), Config: managed.Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}}, CatalogSource: managed.StoreCatalogSource{Store: db}, CodexCredentialStore: credentials, CodexHTTPTransport: securityHTTPTransport(func(r *http.Request) (*http.Response, error) { calls.Add(1); return offline.Transport.RoundTrip(r) })})
	require.NoError(t, err)
	defer m.Close(ctx)
	router, err := New(Options{Store: db, Managed: m})
	require.NoError(t, err)
	defer router.Close(ctx)
	call := func(path, id, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-ID", id)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.NotContains(t, response.Body.String(), "http-private-secret-sentinel")
		return response
	}
	for i, tc := range []struct{ name, cwd, model string }{
		{"credential-overlap", filepath.Dir(credentialPath), ""},
	} {
		id := fmt.Sprintf("b4000000-0000-4000-8000-%012d", i+1)
		response := call("/v1/managed-agents", id, fmt.Sprintf(`{"name":%q,"cwd":%q,"runtime":"embedded"%s}`, tc.name, tc.cwd, tc.model))
		require.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
		_, err = db.ManagedAgent(ctx, tc.name)
		require.ErrorIs(t, err, sql.ErrNoRows)
		_, err = db.ManagedOperation(ctx, id)
		require.ErrorIs(t, err, sql.ErrNoRows)
	}
	response := call("/v1/managed-agents", "b4000000-0000-4000-8000-000000000002", fmt.Sprintf(`{"name":"google-explicit","cwd":%q,"runtime":"embedded","model":{"provider":"google","id":"gemini-2.5-flash"}}`, project))
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	replay := call("/v1/managed-agents", "b4000000-0000-4000-8000-000000000002", fmt.Sprintf(`{"name":"google-explicit","cwd":%q,"runtime":"embedded","model":{"provider":"google","id":"gemini-2.5-flash"}}`, project))
	require.Equal(t, response.Code, replay.Code)
	require.Equal(t, response.Body.String(), replay.Body.String())
	// Vertex is supported even when the provider snapshot is signed out; launch
	// does not promise that a later ADC-backed prompt is authorized.
	vertex := call("/v1/managed-agents", "b4000000-0000-4000-8000-000000000006", fmt.Sprintf(`{"name":"vertex-signed-out","cwd":%q,"runtime":"embedded","model":{"provider":"google-vertex","id":"gemini-2.5-flash"}}`, project))
	require.Equal(t, http.StatusAccepted, vertex.Code, vertex.Body.String())
	vertexRow, err := db.ManagedAgent(ctx, "vertex-signed-out")
	require.NoError(t, err)
	require.Equal(t, "google-vertex", vertexRow.SelectedModel.Provider)
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "google", ID: "gemini-2.5-flash"}))
	response = call("/v1/managed-agents", "b4000000-0000-4000-8000-000000000003", fmt.Sprintf(`{"name":"google-default","cwd":%q,"runtime":"embedded"}`, project))
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	for _, name := range []string{"google-explicit", "google-default"} {
		row, err := db.ManagedAgent(ctx, name)
		require.NoError(t, err)
		require.Equal(t, "google", row.SelectedModel.Provider)
	}
	require.NoError(t, db.SetDefaultModel(ctx, nil))
	response = call("/v1/managed-agents", "b4000000-0000-4000-8000-000000000004", fmt.Sprintf(`{"name":"safe-codex","cwd":%q,"runtime":"embedded"}`, project))
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, "safe-codex")
		return e == nil && row.LifecycleState == "running" && row.AcceptingPrompts
	}, 3*time.Second, 10*time.Millisecond)
	response = call("/v1/managed-agents/safe-codex/restart", "b4000000-0000-4000-8000-000000000005", `{"model":{"provider":"google","id":"gemini-2.5-flash"}}`)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	_, err = db.ManagedOperation(ctx, "b4000000-0000-4000-8000-000000000005")
	require.NoError(t, err)
	rows, err := db.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 4)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "http-private-secret-sentinel")
	afterBytes, err := os.ReadFile(credentialPath)
	require.NoError(t, err)
	require.Equal(t, beforeBytes, afterBytes)
	require.Zero(t, calls.Load())
}
