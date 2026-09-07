package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func newManagedHTTPTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifestPath := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := managed.LoadManifest(manifestPath)
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(context.Background()); _ = durable.Close() })
	return server, durable
}

func TestManagedSessionRequiresExactObjectAndReflectsCredentialedOrigin(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", strings.NewReader(`{"extra":true}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusBadRequest, response.StatusCode)

	request, err = http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", strings.NewReader(`{}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://one.example")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.Equal(t, "https://one.example", response.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", response.Header.Get("Access-Control-Allow-Credentials"))

	request, err = http.NewRequest(http.MethodOptions, server.URL+"/v1/managed-agents", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://one.example")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.Equal(t, "https://one.example", response.Header.Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", response.Header.Get("Access-Control-Allow-Credentials"))

	request, err = http.NewRequest(http.MethodOptions, server.URL+"/v1/managed-agents", nil)
	require.NoError(t, err)
	request.Header.Set("Origin", "not-an-origin")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
	require.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))
}

func TestManagedCredentialedCORSByOrigin(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	for _, origin := range []string{"https://one.example", "https://two.example:8443"} {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", strings.NewReader(`{}`))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", origin)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		require.Equal(t, http.StatusCreated, response.StatusCode)
		require.Equal(t, origin, response.Header.Get("Access-Control-Allow-Origin"))
		require.Equal(t, "true", response.Header.Get("Access-Control-Allow-Credentials"))
		require.Equal(t, "Origin", response.Header.Get("Vary"))

		request, err = http.NewRequest(http.MethodOptions, server.URL+"/v1/managed-agents", nil)
		require.NoError(t, err)
		request.Header.Set("Origin", origin)
		request.Header.Set("Access-Control-Request-Method", "POST")
		request.Header.Set("Access-Control-Request-Headers", "content-type, x-mecha-csrf, x-request-id")
		response, err = http.DefaultClient.Do(request)
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, http.StatusNoContent, response.StatusCode)
		require.Equal(t, origin, response.Header.Get("Access-Control-Allow-Origin"))
		require.Equal(t, "true", response.Header.Get("Access-Control-Allow-Credentials"))
		require.Equal(t, "GET, POST, PUT, DELETE, OPTIONS", response.Header.Get("Access-Control-Allow-Methods"))
		require.Equal(t, "Content-Type, X-Mecha-CSRF, X-Request-ID", response.Header.Get("Access-Control-Allow-Headers"))
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", strings.NewReader(`{}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "not-an-origin")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
	require.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))

	request, err = http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", strings.NewReader(`{}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))
	require.Empty(t, response.Header.Get("Access-Control-Allow-Credentials"))

	legacy, err := http.Get(server.URL + "/v1/agents")
	require.NoError(t, err)
	legacyBody, err := io.ReadAll(legacy.Body)
	require.NoError(t, err)
	legacy.Body.Close()
	require.Equal(t, http.StatusOK, legacy.StatusCode)
	require.Empty(t, legacy.Header.Get("Access-Control-Allow-Origin"))
	legacyOriginRequest, err := http.NewRequest(http.MethodGet, server.URL+"/v1/agents", nil)
	require.NoError(t, err)
	legacyOriginRequest.Header.Set("Origin", "https://legacy.example")
	legacyOrigin, err := http.DefaultClient.Do(legacyOriginRequest)
	require.NoError(t, err)
	legacyOriginBody, err := io.ReadAll(legacyOrigin.Body)
	require.NoError(t, err)
	legacyOrigin.Body.Close()
	var legacyJSON, legacyOriginJSON map[string]any
	require.NoError(t, json.Unmarshal(legacyBody, &legacyJSON))
	require.NoError(t, json.Unmarshal(legacyOriginBody, &legacyOriginJSON))
	delete(legacyJSON, "generated_at")
	delete(legacyOriginJSON, "generated_at")
	require.Equal(t, legacyJSON, legacyOriginJSON)
	require.Equal(t, "*", legacyOrigin.Header.Get("Access-Control-Allow-Origin"))
}

func TestManagedAgentGETEnvelopeAndProjectionVersion(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, database, store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	bootstrap := func() (*http.Cookie, string) {
		request, requestErr := http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", strings.NewReader(`{}`))
		require.NoError(t, requestErr)
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := http.DefaultClient.Do(request)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		var body struct {
			CSRF string `json:"csrf_token"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
		require.Equal(t, http.StatusCreated, response.StatusCode)
		return response.Cookies()[0], body.CSRF
	}
	cookie, _ := bootstrap()
	require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{AgentID: "alpha", Name: "alpha", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: "manifest"}, store.ManagedOperation{OperationID: "operation", SessionIdentityHash: "session", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "operation", CanonicalBodyHash: "body", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{"operation_id":"operation"}`)}))
	get := func() (map[string]json.RawMessage, int64) {
		request, requestErr := http.NewRequest(http.MethodGet, server.URL+"/v1/managed-agents/alpha", nil)
		require.NoError(t, requestErr)
		request.AddCookie(cookie)
		response, requestErr := http.DefaultClient.Do(request)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var envelope map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(response.Body).Decode(&envelope))
		require.Equal(t, []string{"agent", "generated_at", "projection_version"}, sortedJSONKeys(envelope))
		var version int64
		require.NoError(t, json.Unmarshal(envelope["projection_version"], &version))
		return envelope, version
	}
	_, firstVersion := get()
	_, secondVersion := get()
	require.Equal(t, firstVersion, secondVersion)
	require.NoError(t, durable.MarkManagedReady(ctx, "alpha"))
	_, thirdVersion := get()
	require.Greater(t, thirdVersion, secondVersion)
	finalVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, thirdVersion, finalVersion)
	require.NoError(t, router.Close(ctx))
	require.NoError(t, durable.Close())
	server.Close()

	reopened, err := store.Open(ctx, database, store.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	reopenedVersion, err := reopened.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, thirdVersion, reopenedVersion)
	manager, err = managed.NewManager(managed.ManagerOptions{Store: reopened, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err = New(Options{Store: reopened, Managed: manager})
	require.NoError(t, err)
	server = httptest.NewServer(router)
	defer func() { server.Close(); _ = router.Close(ctx) }()
	cookie, _ = bootstrap()
	envelope, afterReopenVersion := get()
	require.Equal(t, thirdVersion, afterReopenVersion)
	var agent map[string]any
	require.NoError(t, json.Unmarshal(envelope["agent"], &agent))
	require.Equal(t, "alpha", agent["agent_id"])
}

type replayProcess struct {
	*managedTestProcess
	session string
}

type replayProcessFactory struct {
	release chan struct{}
	mu      sync.Mutex
	count   int
}

func (f *replayProcessFactory) Start(_ context.Context, spec managed.CommandSpec) (managed.Process, error) {
	f.mu.Lock()
	f.count++
	process := &replayProcess{managedTestProcess: &managedTestProcess{identity: managed.ProcessIdentity{PID: 5000 + f.count, StartMarker: spec.SessionID, Incarnation: spec.SessionID}, stopped: make(chan struct{})}, session: spec.SessionID}
	f.mu.Unlock()
	<-f.release
	return process, nil
}

type replayRPC struct {
	session string
	active  []string
	failed  bool
}

func (r replayRPC) GetState(context.Context) (map[string]any, error) {
	if r.failed {
		return nil, errors.New("scripted startup failure")
	}
	return map[string]any{"sessionId": r.session}, nil
}
func (r replayRPC) GetCommands(context.Context) ([]string, error) {
	if r.failed {
		return nil, errors.New("scripted startup failure")
	}
	return append(append([]string{}, managedHiddenCommandsForTest()...), managedLegacyCommandsForTest()...), nil
}
func (r replayRPC) Bootstrap(context.Context, map[string]any) error { return nil }
func (r replayRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	if r.failed {
		return errors.New("scripted startup failure")
	}
	return nil
}
func (r replayRPC) GetActiveTools(context.Context) ([]string, error) {
	if r.failed {
		return nil, errors.New("scripted startup failure")
	}
	return append([]string{}, r.active...), nil
}
func (r replayRPC) Prompt(context.Context, string) error { return nil }
func (r replayRPC) Close() error                         { return nil }

type replayRPCFactory struct {
	active []string
	mu     sync.Mutex
	count  int
}

func (f *replayRPCFactory) Open(_ context.Context, process managed.Process) (managed.RPC, error) {
	f.mu.Lock()
	f.count++
	failed := f.count == 2
	f.mu.Unlock()
	return replayRPC{session: process.(*replayProcess).session, active: f.active, failed: failed}, nil
}

func managedHiddenCommandsForTest() []string {
	return []string{"mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1"}
}
func managedLegacyCommandsForTest() []string {
	return []string{"goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"}
}

func TestLaunchReplayLifecycle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	active := make([]string, 0, len(manifest.Tools)+3)
	active = append(active, "subagent", "get_subagent_result", "steer_subagent")
	for _, tool := range manifest.Tools {
		active = append(active, tool.Name)
	}
	sort.Strings(active)
	processFactory := &replayProcessFactory{release: make(chan struct{})}
	rpcFactory := &replayRPCFactory{active: active}
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: processFactory, RPCFactory: rpcFactory, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai", ID: "gpt-5.6-terra", Available: true})})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer func() { server.Close(); _ = router.Close(ctx); _ = durable.Close() }()
	bootstrapRequest, err := http.NewRequest(http.MethodPost, server.URL+"/v1/ui-session", strings.NewReader(`{}`))
	require.NoError(t, err)
	bootstrapRequest.Header.Set("Content-Type", "application/json")
	bootstrapResponse, err := http.DefaultClient.Do(bootstrapRequest)
	require.NoError(t, err)
	var bootstrap struct {
		CSRF string `json:"csrf_token"`
	}
	require.NoError(t, json.NewDecoder(bootstrapResponse.Body).Decode(&bootstrap))
	bootstrapResponse.Body.Close()
	cookie := bootstrapResponse.Cookies()[0]
	request := func(id, body string) (int, []byte, http.Header) {
		req, requestErr := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents", strings.NewReader(body))
		require.NoError(t, requestErr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Mecha-CSRF", bootstrap.CSRF)
		req.Header.Set("X-Request-ID", id)
		req.AddCookie(cookie)
		response, requestErr := http.DefaultClient.Do(req)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		bodyBytes, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		return response.StatusCode, bodyBytes, response.Header.Clone()
	}
	alphaBody := `{"name":"alpha","cwd":"` + root + `"}`
	alphaStatus, alphaResponse, alphaHeaders := request("123e4567-e89b-42d3-a456-426614174081", alphaBody)
	require.Equal(t, http.StatusAccepted, alphaStatus)
	pendingStatus, pendingResponse, pendingHeaders := request("123e4567-e89b-42d3-a456-426614174081", alphaBody)
	require.Equal(t, alphaStatus, pendingStatus)
	require.Equal(t, alphaResponse, pendingResponse)
	require.Equal(t, alphaHeaders.Get("X-Request-ID"), pendingHeaders.Get("X-Request-ID"))
	close(processFactory.release)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "alpha")
		return readErr == nil && agent.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	readyStatus, readyResponse, readyHeaders := request("123e4567-e89b-42d3-a456-426614174081", alphaBody)
	require.Equal(t, alphaStatus, readyStatus)
	require.Equal(t, alphaResponse, readyResponse)
	require.Equal(t, alphaHeaders.Get("Content-Type"), readyHeaders.Get("Content-Type"))
	betaStatus, betaResponse, betaHeaders := request("123e4567-e89b-42d3-a456-426614174082", `{"name":"beta","cwd":"`+root+`"}`)
	require.Equal(t, http.StatusAccepted, betaStatus)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "beta")
		return readErr == nil && agent.LifecycleState == "failed"
	}, 3*time.Second, 10*time.Millisecond)
	failedStatus, failedResponse, failedHeaders := request("123e4567-e89b-42d3-a456-426614174082", `{"name":"beta","cwd":"`+root+`"}`)
	require.Equal(t, betaStatus, failedStatus)
	require.Equal(t, betaResponse, failedResponse)
	require.Equal(t, betaHeaders.Get("X-Request-ID"), failedHeaders.Get("X-Request-ID"))
}

func sortedJSONKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestManagedSessionAndProjectionAreAuthenticated(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	response, err := http.Post(server.URL+"/v1/ui-session", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.Contains(t, response.Header.Get("Set-Cookie"), "HttpOnly")
	require.Contains(t, response.Header.Get("Set-Cookie"), "SameSite=Strict")
	require.Contains(t, response.Header.Get("Set-Cookie"), "Path=/v1")
	cookie := response.Cookies()[0]
	var bootstrap struct {
		CSRF string `json:"csrf_token"`
	}
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &bootstrap))
	require.NotEmpty(t, bootstrap.CSRF)

	csrfMissing, err := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents", strings.NewReader(`{"name":"alpha","cwd":"/tmp"}`))
	require.NoError(t, err)
	csrfMissing.Header.Set("Content-Type", "application/json")
	csrfMissing.AddCookie(cookie)
	csrfResponse, err := http.DefaultClient.Do(csrfMissing)
	require.NoError(t, err)
	defer csrfResponse.Body.Close()
	require.Equal(t, http.StatusForbidden, csrfResponse.StatusCode)

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/managed-agents/missing", nil)
	require.NoError(t, err)
	request.AddCookie(cookie)
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNotFound, response.StatusCode)

	response, err = http.Get(server.URL + "/v1/managed-agents/missing")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)

	invalidRequest, err := http.NewRequest(http.MethodGet, server.URL+"/v1/managed-agents/missing", nil)
	require.NoError(t, err)
	invalidRequest.AddCookie(&http.Cookie{Name: uiSessionCookieName, Value: "invalid"})
	invalidResponse, err := http.DefaultClient.Do(invalidRequest)
	require.NoError(t, err)
	defer invalidResponse.Body.Close()
	require.Equal(t, http.StatusUnauthorized, invalidResponse.StatusCode)
	cleared := invalidResponse.Cookies()[0]
	require.Equal(t, uiSessionCookieName, cleared.Name)
	require.Equal(t, "/v1", cleared.Path)
	require.Equal(t, -1, cleared.MaxAge)
}
