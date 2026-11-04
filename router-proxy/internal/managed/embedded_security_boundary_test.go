package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Uses the ordinary SDK resolver. Only the external HTTP transport is mocked.
func securityBoundaryManager(t *testing.T, script string, captures ...http.RoundTripper) (*Manager, *store.Store, string, string, *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	base := testfixture.PrivateDir(t)
	project := filepath.Join(base, "private-project") // same-prefix sibling of the protected tree
	private := filepath.Join(base, "private")
	require.NoError(t, os.Mkdir(project, 0700))
	credentialPath := filepath.Join(private, "credential.json")
	credentials, err := store.OpenLocalCodexCredentialStore(credentialPath)
	require.NoError(t, err)
	require.NoError(t, credentials.Save(ctx, store.CodexCredential{AccessToken: "security-private-secret-sentinel", RefreshToken: "security-refresh-sentinel", AccountID: "offline-account", ExpiresAt: time.Now().Add(time.Hour)}))
	db, err := store.Open(ctx, filepath.Join(base, "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	_, err = db.CommitManagedProviderSnapshot(ctx, []store.ManagedProviderStatus{
		{Provider: "openai-codex", Label: "ChatGPT", Status: "signed_out"},
		{Provider: "google", Label: "Google", Status: "authenticated", Authenticated: true},
		{Provider: "cursor", Label: "Cursor", Status: "signed_out"},
		{Provider: "google-vertex", Label: "Google Vertex AI", Status: "signed_out"},
	}, []store.ManagedProviderModel{{Provider: "google", ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Available: true}})
	require.NoError(t, err)
	calls := &atomic.Int32{}
	// The embedded Google implementation uses a fresh client with the default
	// transport, bypassing the SDK provider registry. Fail closed for every URL.
	originalTransport := http.DefaultTransport
	var capture http.RoundTripper
	if len(captures) > 0 {
		capture = captures[0]
	}
	if capture == nil {
		capture = codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("offline provider fixture")), Request: r}, nil
		})
	}
	http.DefaultTransport = capture
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	// Keep the real Google adapter and resolver. Inject only its HTTP boundary
	// so a regressed guard cannot send even the synthetic ambient key externally.
	google, ok := ai.GetApiProvider("google-generative-ai")
	require.True(t, ok)
	original := google.StreamSimple
	google.StreamSimple = func(callCtx context.Context, model *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		calls.Add(1)
		copy := ai.SimpleStreamOptions{}
		if opts != nil {
			copy = *opts
		}
		copy.HTTPClient = &http.Client{Transport: codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("offline Google fixture")), Request: r}, nil
		})}
		return original(callCtx, model, transcript, &copy)
	}
	ai.RegisterApiProvider(google)
	t.Cleanup(func() { google.StreamSimple = original; ai.RegisterApiProvider(google) })
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}}, CatalogSource: StoreCatalogSource{Store: db}, CodexCredentialStore: credentials, CodexHTTPTransport: codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NotContains(t, string(body), "security-private-secret-sentinel")
		require.Equal(t, embeddedCodexResponsesURL, r.URL.String())
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(script)), Request: r}, nil
	})})
	require.NoError(t, err)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	t.Cleanup(func() { require.NoError(t, m.Close(ctx)); require.NoError(t, db.Close()) })
	return m, db, project, credentialPath, calls
}

const securityDoneSSE = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fake\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fake\",\"status\":\"completed\"}}\n\n"

func securityReady(t *testing.T, m *Manager, db *store.Store, name string) embeddedOwner {
	t.Helper()
	require.Eventually(t, func() bool {
		row, err := db.ManagedAgent(context.Background(), name)
		m.mu.Lock()
		_, ok := m.embeddedOwners[name]
		m.mu.Unlock()
		return err == nil && row.LifecycleState == "running" && row.AcceptingPrompts && ok
	}, 3*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.embeddedOwners[name]
}

func securityNoReservation(t *testing.T, db *store.Store, name, request string) {
	t.Helper()
	_, err := db.ManagedAgent(context.Background(), name)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = db.ManagedOperation(context.Background(), request)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestEmbeddedAvailableGoogleAcceptedAtEverySelectionBoundary(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "offline-ambient-google-key")
	ctx := context.Background()
	m, db, project, _, calls := securityBoundaryManager(t, securityDoneSSE, nil)
	google := Model{Provider: "google", ID: "gemini-2.5-flash"}
	catalog, err := m.catalog.Catalog(ctx)
	require.NoError(t, err)
	ordinary, err := SelectModel(&google, nil, catalog)
	require.NoError(t, err, "shared RPC selector must retain Google")
	require.Equal(t, google.Provider, ordinary.Provider)
	for i, root := range []bool{true, false} {
		request := LaunchRequest{Name: "google-" + string(rune('a'+i)), Cwd: project, Runtime: "embedded", Model: &google, RequestID: "google-launch-" + string(rune('a'+i))}
		if root {
			_, err = m.LaunchEmbeddedRoot(ctx, request)
		} else {
			_, err = m.Launch(ctx, request)
		}
		require.NoError(t, err)
		saved, err := db.ManagedAgent(ctx, request.Name)
		require.NoError(t, err)
		require.Equal(t, google.Provider, saved.SelectedModel.Provider)
	}
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: google.Provider, ID: google.ID}))
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "google-default", Cwd: project, Runtime: "embedded", RequestID: "google-default-launch"})
	require.NoError(t, err)
	defaultAgent, err := db.ManagedAgent(ctx, "google-default")
	require.NoError(t, err)
	require.Equal(t, google.Provider, defaultAgent.SelectedModel.Provider)
	require.NoError(t, db.SetDefaultModel(ctx, nil))
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "codex-root", Cwd: project, Runtime: "embedded", RequestID: "codex-root-launch"})
	require.NoError(t, err)
	owner := securityReady(t, m, db, "codex-root")
	_, err = m.launchEmbeddedWorkerFromOwner(ctx, LaunchRequest{Name: "google-child", Cwd: project, Runtime: "embedded", Model: &google, RequestID: "google-child-launch"}, owner)
	require.NoError(t, err)
	child, err := db.ManagedAgent(ctx, "google-child")
	require.NoError(t, err)
	require.Equal(t, google.Provider, child.SelectedModel.Provider)
	_, err = m.ReserveRestart(ctx, RestartRequest{AgentID: "codex-root", Model: &google, Method: "POST", CanonicalPath: "/v1/managed-agents/codex-root/restart", RequestID: "google-restart", CanonicalBodyHash: "google"})
	require.NoError(t, err)
	restarted, err := db.ManagedAgent(ctx, "codex-root")
	require.NoError(t, err)
	require.Equal(t, google.Provider, restarted.SelectedModel.Provider)
	require.Equal(t, google.ID, restarted.SelectedModel.ID)
	_, err = db.ManagedOperation(ctx, "google-restart")
	require.NoError(t, err)
	unsupported := Model{Provider: "unsupported-fixture", ID: "fixture-model"}
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "unsupported-root", Cwd: project, Runtime: "embedded", Model: &unsupported, RequestID: "unsupported-launch"})
	require.True(t, IsModelUnavailable(err))
	securityNoReservation(t, db, "unsupported-root", "unsupported-launch")
	require.Zero(t, calls.Load())
}

func TestEmbeddedRestoredGoogleDispatchesAndPersists(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "offline-ambient-google-key")
	ctx := context.Background()
	type capturedRequest struct {
		method, url, body string
		headers           http.Header
	}
	var mu sync.Mutex
	var requests []capturedRequest
	capture := codexTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		requests = append(requests, capturedRequest{r.Method, r.URL.String(), string(body), r.Header.Clone()})
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"restored fixture answer\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}]}\n\n")), Request: r}, nil
	})
	m, db, project, _, _ := securityBoundaryManager(t, securityDoneSSE, capture)
	_, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "restore-provider", Cwd: project, Runtime: "embedded", RequestID: "restore-provider-launch"})
	require.NoError(t, err)
	owner := securityReady(t, m, db, "restore-provider")
	saved, err := db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	before, err := db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	// Seed a historic non-Codex selection through the unchanged generic Store
	// contract, then feed its persisted readback to the restored-session runner.
	_, _, err = db.ReserveEmbeddedModelSelection(ctx, saved.AgentID, store.ManagedModel{Provider: "google", ID: "gemini-2.5-flash"}, store.ManagedOperation{OperationID: "legacy-google-selection", RequestID: "legacy-google-selection", Method: "POST", CanonicalPath: "/legacy-selection", CanonicalBodyHash: "legacy", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	require.NoError(t, err)
	saved, err = db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "google", saved.SelectedModel.Provider)
	err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "restored Google prompt")
	require.NoError(t, err)
	after, err := db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	mu.Lock()
	captured := append([]capturedRequest(nil), requests...)
	mu.Unlock()
	require.Len(t, captured, 1)
	request := captured[0]
	require.Equal(t, "POST", request.method)
	parsedURL, err := url.Parse(request.url)
	require.NoError(t, err)
	require.Equal(t, "https", parsedURL.Scheme)
	require.Equal(t, "generativelanguage.googleapis.com", parsedURL.Host)
	require.Equal(t, "/v1beta/models/gemini-2.5-flash:streamGenerateContent", parsedURL.Path)
	require.Equal(t, []string{"offline-ambient-google-key"}, parsedURL.Query()["key"])
	require.Equal(t, 1, strings.Count(request.url, "offline-ambient-google-key"))
	require.Equal(t, "sse", parsedURL.Query().Get("alt"))
	var parsedBody map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(request.body), &parsedBody))
	require.Contains(t, string(parsedBody["contents"]), "restored Google prompt")
	headers, err := json.Marshal(request.headers)
	require.NoError(t, err)
	require.NotContains(t, string(headers), "offline-ambient-google-key")
	require.NotContains(t, request.body, "offline-ambient-google-key")
	for _, secret := range []string{"security-private-secret-sentinel", "security-refresh-sentinel", "offline-account"} {
		require.NotContains(t, request.url, secret)
		require.NotContains(t, string(headers), secret)
		require.NotContains(t, request.body, secret)
	}
	require.GreaterOrEqual(t, len(after.Entries), len(before.Entries)+2)
	require.Equal(t, before.Entries, after.Entries[:len(before.Entries)])
	final := after.Entries[len(after.Entries)-1]
	require.Equal(t, "assistant", final.Kind)
	require.Contains(t, string(final.Body), "restored fixture answer")
	require.Contains(t, string(final.Body), `"stopReason":"stop"`)
}

func TestEmbeddedCredentialTreeDeniedBeforeRootWorkerAndRestoredTools(t *testing.T) {
	ctx := context.Background()
	m, db, project, credentialPath, calls := securityBoundaryManager(t, securityDoneSSE, nil)
	private := filepath.Dir(credentialPath)
	child := filepath.Join(private, "child")
	require.NoError(t, os.Mkdir(child, 0700))
	alias := filepath.Join(filepath.Dir(private), "private-alias")
	require.NoError(t, os.Symlink(private, alias))
	bytesBefore, err := os.ReadFile(credentialPath)
	require.NoError(t, err)
	for i, cwd := range []string{private, filepath.Dir(private), child, alias} {
		for j, root := range []bool{true, false} {
			id := "overlap-" + string(rune('a'+i)) + string(rune('a'+j))
			request := LaunchRequest{Name: id, Cwd: cwd, Runtime: "embedded", RequestID: id}
			if root {
				_, err = m.LaunchEmbeddedRoot(ctx, request)
			} else {
				_, err = m.Launch(ctx, request)
			}
			require.ErrorContains(t, err, "credential tree")
			securityNoReservation(t, db, id, id)
		}
	}
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "safe-project", Cwd: project, Runtime: "embedded", RequestID: "safe-project-launch"})
	require.NoError(t, err, "same-prefix siblings are disjoint")
	owner := securityReady(t, m, db, "safe-project")
	_, err = m.launchEmbeddedWorkerFromOwner(ctx, LaunchRequest{Name: "unsafe-child", Cwd: private, Runtime: "embedded", RequestID: "unsafe-child-launch"}, owner)
	require.ErrorContains(t, err, "credential tree")
	securityNoReservation(t, db, "unsafe-child", "unsafe-child-launch")
	saved, err := db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	before, err := db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	// Test-owned SQLite seeds historic Cwds that current admission must reject.
	// No credential data is written to SQLite.
	fixture, err := sql.Open("sqlite", filepath.Join(filepath.Dir(project), "router.db"))
	require.NoError(t, err)
	defer fixture.Close()
	fixture.SetMaxOpenConns(1)
	_, err = fixture.ExecContext(ctx, `PRAGMA busy_timeout=5000`)
	require.NoError(t, err)
	for _, cwd := range []string{private, child, filepath.Dir(private), alias} {
		_, err = fixture.ExecContext(ctx, `UPDATE managed_agents SET cwd=? WHERE agent_id=?`, cwd, saved.AgentID)
		require.NoError(t, err)
		saved, err = db.ManagedAgent(ctx, owner.binding.AgentID)
		require.NoError(t, err)
		require.Equal(t, cwd, saved.Cwd)
		err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "read/grep/edit/write credential.json")
		require.ErrorContains(t, err, "credential tree")
	}
	after, err := db.ReopenEmbedded(ctx, owner.binding)
	require.NoError(t, err)
	require.Equal(t, before.Entries, after.Entries)
	encoded, err := json.Marshal(after)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "security-private-secret-sentinel")
	bytesAfter, err := os.ReadFile(credentialPath)
	require.NoError(t, err)
	require.Equal(t, bytesBefore, bytesAfter)
	require.Zero(t, calls.Load())
}

// Ensures configuration/admission does not impose the embedded exclusion on RPC.
func TestRPCCwdAndCatalogRemainBroadWithCredentialOverlap(t *testing.T) {
	ctx := context.Background()
	m, db, _, credentialPath, _ := securityBoundaryManager(t, securityDoneSSE, nil)
	identity := ProcessIdentity{PID: 123, StartMarker: "rpc-security-start", Incarnation: "rpc-security-incarnation"}
	process := &launchProcess{identity: identity, stopped: make(chan struct{})}
	rpc := &launchRPC{}
	m.processFactory = &launchFactory{process: process, rpc: rpc}
	m.rpcFactory = launchRPCFactory{rpc: rpc}
	m.groupLeader = func(candidate ProcessIdentity) bool { return candidate == identity }
	cwd := filepath.Dir(credentialPath)
	_, err := ValidateWorkingDirectory(cwd, m.AllowedWorkingDirectoryRoots())
	require.NoError(t, err)
	google := Model{Provider: "google", ID: "gemini-2.5-flash"}
	result, err := m.Launch(ctx, LaunchRequest{Name: "rpc-google", Cwd: cwd, Runtime: "rpc", Model: &google, RequestID: "rpc-google-launch"})
	require.NoError(t, err)
	require.Equal(t, "rpc", result.Agent.Runtime)
	require.Equal(t, google.Provider, result.Agent.SelectedModel.Provider)
	require.Eventually(t, func() bool {
		saved, readErr := db.ManagedAgent(ctx, result.Agent.AgentID)
		return readErr == nil && saved.LifecycleState == "running" && saved.AcceptingPrompts
	}, 3*time.Second, 10*time.Millisecond)
	saved, err := db.ManagedAgent(ctx, result.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, google.Provider, saved.SelectedModel.Provider)
	require.Equal(t, identity.PID, saved.ProcessPID)
	require.Equal(t, identity.StartMarker, saved.ProcessStartMarker)
	require.Equal(t, identity.Incarnation, saved.ProcessIncarnation)
	require.NotEmpty(t, saved.PiSessionID)
	require.NotEmpty(t, saved.RuntimeGeneration)
	require.NoError(t, m.Close(ctx))
	stopped, err := db.ManagedAgent(ctx, result.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "stopped", stopped.ActivityState)
	require.False(t, stopped.AcceptingPrompts)
	require.False(t, stopped.SlotHeld)
	require.Equal(t, 1, process.Stops())
}
