package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/stretchr/testify/require"
)

func insertManagedArchiveHTTPTest(t *testing.T, database, key, agentID string) {
	t.Helper()
	db, err := sql.Open("sqlite", database)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`INSERT INTO managed_agent_archives (
		archive_key,agent_id,name,terminal_outcome,completed_at,disposition,total,
		queued_never_attempted,in_flight_or_unknown,completed,released_or_failed)
		VALUES (?, ?, ?, 'terminated', '2026-09-01T12:00:00.000Z', 'completed', 1, 0, 0, 1, 0)`, key, agentID, agentID)
	require.NoError(t, err)
}

func TestManagedArchiveHTTPContractSeparatesClosedReadFromLiveDetail(t *testing.T) {
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
	defer func() { server.Close(); _ = router.Close(ctx); _ = durable.Close() }()

	key := "123e4567-e89b-42d3-a456-426614174081"
	insertManagedArchiveHTTPTest(t, database, key, "alpha")
	require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{AgentID: "beta", Name: "beta", SlotHeld: true, Cwd: "/tmp/beta", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}, store.ManagedOperation{OperationID: "beta-operation", Method: http.MethodPost, CanonicalPath: "/v1/managed-agents", RequestID: "beta-request", CanonicalBodyHash: strings.Repeat("b", 64), ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}))

	request := func(method, path string) (*http.Response, map[string]any) {
		req, requestErr := http.NewRequest(method, server.URL+path, nil)
		require.NoError(t, requestErr)
		response, requestErr := http.DefaultClient.Do(req)
		require.NoError(t, requestErr)
		body, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		require.NoError(t, response.Body.Close())
		payload := map[string]any{}
		if len(body) > 0 {
			require.NoError(t, json.Unmarshal(body, &payload))
		}
		return response, payload
	}

	live, liveBody := request(http.MethodGet, "/v1/managed-agents/alpha")
	require.Equal(t, http.StatusNotFound, live.StatusCode)
	require.Equal(t, "AGENT_NOT_FOUND", liveBody["error"].(map[string]any)["code"])

	list, listBody := request(http.MethodGet, "/v1/managed-agents")
	require.Equal(t, http.StatusOK, list.StatusCode)
	listAgents, ok := listBody["agents"].([]any)
	require.True(t, ok)
	require.Len(t, listAgents, 1)
	require.Equal(t, "beta", listAgents[0].(map[string]any)["agent_id"])
	require.NotContains(t, listAgents[0], "record_kind")

	archive, archiveBody := request(http.MethodGet, "/v1/managed-agents/archive/"+key)
	require.Equal(t, http.StatusOK, archive.StatusCode)
	require.Equal(t, "no-store", archive.Header.Get("Cache-Control"))
	require.Equal(t, "archived", archiveBody["agent"].(map[string]any)["record_kind"])
	require.Equal(t, key, archiveBody["agent"].(map[string]any)["archive_key"])

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		response, body := request(method, "/v1/managed-agents/archive/"+key)
		require.Equal(t, http.StatusConflict, response.StatusCode, method)
		require.Equal(t, "ARCHIVED_READ_ONLY", body["error"].(map[string]any)["code"], method)
		require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	}
	methodNotAllowed, _ := request(http.MethodHead, "/v1/managed-agents/archive/"+key)
	require.Equal(t, http.StatusMethodNotAllowed, methodNotAllowed.StatusCode)
	require.Equal(t, "no-store", methodNotAllowed.Header.Get("Cache-Control"))

	for _, path := range []string{
		"/v1/managed-agents/archive",
		"/v1/managed-agents/archive/not-a-uuid",
		"/v1/managed-agents/archive/123E4567-e89b-42d3-a456-426614174081",
		"/v1/managed-agents/archive/" + key + "/extra",
	} {
		response, body := request(http.MethodGet, path)
		require.Equal(t, http.StatusBadRequest, response.StatusCode, path)
		require.Equal(t, "INVALID_ARCHIVE_KEY", body["error"].(map[string]any)["code"], path)
	}
	for _, invalid := range []struct{ path, rawPath string }{
		{path: "/v1/managed-agents/archive/%ZZ", rawPath: "/v1/managed-agents/archive/%ZZ"},
		{path: "/v1/managed-agents/archive//", rawPath: "/v1/managed-agents/archive/%2F"},
		{path: "/v1/managed-agents/archive/\\", rawPath: "/v1/managed-agents/archive/%5C"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path, req.URL.RawPath = invalid.path, invalid.rawPath
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		require.Equal(t, http.StatusBadRequest, response.Code, invalid.rawPath)
		var body map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		require.Equal(t, "INVALID_ARCHIVE_KEY", body["error"].(map[string]any)["code"], invalid.rawPath)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		missing, body := request(method, "/v1/managed-agents/archive/123e4567-e89b-42d3-a456-426614174082")
		require.Equal(t, http.StatusNotFound, missing.StatusCode, method)
		require.Equal(t, "ARCHIVE_NOT_FOUND", body["error"].(map[string]any)["code"], method)
	}

	options := httptest.NewRequest(http.MethodOptions, "/v1/managed-agents/archive/"+key, nil)
	options.Header.Set("Origin", "https://unrelated.example")
	optionsResponse := httptest.NewRecorder()
	router.ServeHTTP(optionsResponse, options)
	require.Equal(t, http.StatusNoContent, optionsResponse.Code)
	require.Equal(t, "*", optionsResponse.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "no-store", optionsResponse.Header().Get("Cache-Control"))
}

func TestManagedGoalSettingsAcceptsOnlyTheSharedEditorShape(t *testing.T) {
	valid := map[string]json.RawMessage{"settings": json.RawMessage(`{"goal_lock_enabled":true,"snooze_enabled":true,"snooze_duration_ms":120000,"snooze_turn_limit":null,"snooze_budget_cap_ms":null,"snooze_refill_yield_ms":30000,"snooze_refill_elapsed_ms":60000}`)}
	settings, err := managedGoalSettings(valid)
	require.NoError(t, err)
	require.Equal(t, true, settings["goal_lock_enabled"])
	require.Equal(t, int64(120000), settings["snooze_duration_ms"])
	require.Nil(t, settings["snooze_turn_limit"])

	_, err = managedGoalSettings(map[string]json.RawMessage{"settings": json.RawMessage(`{"goal_lock_enabled":true,"snooze_enabled":true,"snooze_duration_ms":0,"snooze_turn_limit":null,"snooze_budget_cap_ms":null,"snooze_refill_yield_ms":30000,"snooze_refill_elapsed_ms":60000}`)})
	require.Error(t, err)
	_, err = managedGoalSettings(map[string]json.RawMessage{"settings": json.RawMessage(`{"goal_lock_enabled":true,"snooze_enabled":true,"snooze_duration_ms":60000,"snooze_turn_limit":21,"snooze_budget_cap_ms":null,"snooze_refill_yield_ms":30000,"snooze_refill_elapsed_ms":60000}`)})
	require.Error(t, err)
}

func TestManagedGoalStateKeepsDurableSharedEditorSettings(t *testing.T) {
	state := managedGoalState(map[string]any{"goal_state": map[string]any{"enabled": false, "goal": "stale text", "goal_lock_enabled": true, "snooze_duration_ms": int64(120000)}})
	require.False(t, state["enabled"].(bool))
	require.Equal(t, "", state["goal"])
	require.Equal(t, true, state["goal_lock_enabled"])
	require.Equal(t, int64(120000), state["snooze_duration_ms"])
}

func newManagedHTTPTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	durable, _ := storetest.OpenTestStore(t)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Available: true})})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(context.Background()); _ = durable.Close() })
	return server, durable
}

type providerAuthHTTPRun struct {
	events          chan managed.ProviderAuthEvent
	responseStarted chan struct{}
	release         chan struct{}
	done            chan struct{}
	mu              sync.Mutex
	respondCount    int
	finishOnce      sync.Once
}

func newProviderAuthHTTPRun() *providerAuthHTTPRun {
	return &providerAuthHTTPRun{events: make(chan managed.ProviderAuthEvent, 4), responseStarted: make(chan struct{}, 1), release: make(chan struct{}), done: make(chan struct{})}
}
func (r *providerAuthHTTPRun) Events() <-chan managed.ProviderAuthEvent { return r.events }
func (r *providerAuthHTTPRun) Respond(ctx context.Context, promptID, value string) error {
	if promptID != "prompt-1" || value == "" {
		return errors.New("invalid provider test response")
	}
	r.mu.Lock()
	r.respondCount++
	r.mu.Unlock()
	select {
	case r.responseStarted <- struct{}{}:
	default:
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return errors.New("provider test run closed")
	}
	r.events <- managed.ProviderAuthEvent{Kind: "terminal", OperationID: "123e4567-e89b-42d3-a456-426614174001", Provider: "openai-codex", Status: "succeeded", Providers: []store.ManagedProviderStatus{
		{Provider: "openai-codex", Label: "ChatGPT", Authenticated: true, Status: "authenticated"},
		{Provider: "google", Label: "Google", Authenticated: false, Status: "signed_out"},
		{Provider: "cursor", Label: "Cursor", Authenticated: false, Status: "signed_out"},
		{Provider: "google-vertex", Label: "Google Vertex AI", Authenticated: false, Status: "signed_out"},
	}, Models: []store.ManagedProviderModel{{Provider: "openai-codex", ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Available: true}}}
	close(r.events)
	r.finish()
	return nil
}
func (r *providerAuthHTTPRun) Cancel()     { r.finish() }
func (r *providerAuthHTTPRun) Wait() error { <-r.done; return nil }
func (r *providerAuthHTTPRun) finish()     { r.finishOnce.Do(func() { close(r.done) }) }

type providerAuthHTTPHelper struct{ run *providerAuthHTTPRun }

func (h providerAuthHTTPHelper) Start(_ context.Context, operationID, provider, action string) (managed.ProviderAuthRun, error) {
	if operationID != "123e4567-e89b-42d3-a456-426614174001" || provider != "openai-codex" || action != "login" {
		return nil, errors.New("unexpected provider test operation")
	}
	h.run.events <- managed.ProviderAuthEvent{Kind: "prompt", OperationID: operationID, Provider: provider, Prompt: &managed.ProviderAuthPrompt{ID: "prompt-1", Kind: "secret", Message: "Enter the requested sign-in value."}}
	return h.run, nil
}

func TestProviderPromptResponseReservesWorkerWideReplayBeforeForwardingValue(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, database, store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	run := newProviderAuthHTTPRun()
	coordinator, err := managed.NewProviderAuthCoordinator(durable, manager, providerAuthHTTPHelper{run: run})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager, ProviderAuth: coordinator})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); coordinator.Close(); _ = router.Close(ctx); _ = durable.Close() })

	login, err := http.NewRequest(http.MethodPost, server.URL+"/v1/provider-auth/openai-codex/login", strings.NewReader(`{}`))
	require.NoError(t, err)
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174001")
	response, err := http.DefaultClient.Do(login)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	_ = response.Body.Close()

	request := func() *http.Request {
		value, requestErr := http.NewRequest(http.MethodPost, server.URL+"/v1/provider-auth/operations/123e4567-e89b-42d3-a456-426614174001/response", strings.NewReader(`{"prompt_id":"prompt-1","value":"sentinel-never-persisted"}`))
		require.NoError(t, requestErr)
		value.Header.Set("Content-Type", "application/json")
		value.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174002")
		return value
	}
	first := make(chan *http.Response, 1)
	go func() {
		result, requestErr := http.DefaultClient.Do(request())
		require.NoError(t, requestErr)
		first <- result
	}()
	require.Eventually(t, func() bool {
		select {
		case <-run.responseStarted:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	// The first request is still blocked in the helper. A duplicate must replay
	// its accepted admission rather than forwarding a second secret value.
	second, err := http.DefaultClient.Do(request())
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, second.StatusCode)
	_ = second.Body.Close()
	run.mu.Lock()
	require.Equal(t, 1, run.respondCount)
	run.mu.Unlock()
	close(run.release)
	completed := <-first
	require.Equal(t, http.StatusAccepted, completed.StatusCode)
	_ = completed.Body.Close()
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := durable.ManagedProviderSnapshot(ctx)
		return snapshotErr == nil && snapshot.AuthRevision == 1
	}, time.Second, 10*time.Millisecond)
	// The response value is never part of the durable operation body. Check the
	// actual SQLite file rather than merely the public response projection.
	rawDatabase, err := os.ReadFile(database)
	require.NoError(t, err)
	require.NotContains(t, string(rawDatabase), "sentinel-never-persisted")
}

func TestCompleteRouteMatrix(t *testing.T) {
	type routeDeclaration struct {
		Methods []string `json:"methods"`
		Path    string   `json:"path"`
		Managed bool     `json:"managed"`
	}
	type manifestDocument struct {
		HTTP []routeDeclaration `json:"http"`
	}
	type routeMatrix struct {
		Schema string `json:"schema"`
		Routes []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"routes"`
	}
	manifestBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "router", "contract", "manifest.json"))
	require.NoError(t, err)
	matrixBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "fixtures", "mecha-rpc", "route-matrix.json"))
	require.NoError(t, err)
	var manifest manifestDocument
	var matrix routeMatrix
	require.NoError(t, json.Unmarshal(manifestBytes, &manifest))
	require.NoError(t, json.Unmarshal(matrixBytes, &matrix))
	require.Equal(t, "mecha.rpc.managed-route-matrix/v1", matrix.Schema)
	declared := make(map[string]struct{})
	for _, route := range manifest.HTTP {
		if !route.Managed {
			continue
		}
		for _, method := range route.Methods {
			declared[method+" "+route.Path] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	for _, route := range matrix.Routes {
		key := route.Method + " " + route.Path
		t.Run(key, func(t *testing.T) {
			_, present := declared[key]
			require.True(t, present, "fixture route is absent from the managed manifest")
			_, duplicate := seen[key]
			require.False(t, duplicate, "fixture has a duplicate managed route")
			seen[key] = struct{}{}
		})
	}
	require.Equal(t, declared, seen, "every managed manifest method/route must appear exactly once in the fixture")
}

func TestCanonicalUUIDUsesTheStandardFiveGroups(t *testing.T) {
	require.True(t, canonicalUUID("123e4567-e89b-42d3-a456-426614174081"))
	require.False(t, canonicalUUID("123e4567-e89b-42d3-a456426614174081"))
}

func TestManagedProjectionIsDirectAndKeepsItsVersion(t *testing.T) {
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

	require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{AgentID: "alpha", Name: "alpha", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: "manifest"}, store.ManagedOperation{OperationID: "operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "operation", CanonicalBodyHash: "body", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{"operation_id":"operation"}`)}))
	get := func() (map[string]json.RawMessage, int64) {
		request, requestErr := http.NewRequest(http.MethodGet, server.URL+"/v1/managed-agents/alpha", nil)
		require.NoError(t, requestErr)
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

	missing, err := http.Get(server.URL + "/v1/managed-agents/missing")
	require.NoError(t, err)
	defer missing.Body.Close()
	require.Equal(t, http.StatusNotFound, missing.StatusCode)

	require.NoError(t, router.Close(ctx))
	server.Close()
	require.NoError(t, durable.Close())

	reopened, err := store.Open(ctx, database, store.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	manager, err = managed.NewManager(managed.ManagerOptions{Store: reopened, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err = New(Options{Store: reopened, Managed: manager})
	require.NoError(t, err)
	server = httptest.NewServer(router)
	defer func() { server.Close(); _ = router.Close(ctx) }()
	envelope, reopenedVersion := get()
	require.Equal(t, thirdVersion, reopenedVersion)
	var agent map[string]any
	require.NoError(t, json.Unmarshal(envelope["agent"], &agent))
	require.Equal(t, "alpha", agent["agent_id"])
}

type replayProcess struct {
	*managedTestProcess
	session string
}

type replayProcessFactory struct {
	release     chan struct{}
	started     chan struct{}
	startedOnce sync.Once
	mu          sync.Mutex
	count       int
}

func (f *replayProcessFactory) Start(_ context.Context, spec managed.CommandSpec) (managed.Process, error) {
	f.startedOnce.Do(func() { close(f.started) })
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
func (r replayRPC) ApplyPolicy(context.Context, map[string]any) (map[string]any, error) {
	if r.failed {
		return nil, errors.New("scripted startup failure")
	}
	return map[string]any{"inlineEnabled": false, "inlineCredits": map[string]any{}, "recallContent": "", "settledEnabled": false, "goalLockEnabled": false, "compactionDeferred": false}, nil
}
func (r replayRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	if r.failed {
		return nil, errors.New("scripted startup failure")
	}
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false}}, nil
}
func (r replayRPC) RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error) {
	if r.failed {
		return nil, errors.New("scripted startup failure")
	}
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": r.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (r replayRPC) WaitForChildSnapshot(context.Context) (map[string]any, error) {
	if r.failed {
		return nil, errors.New("scripted startup failure")
	}
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": r.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (r replayRPC) Prompt(context.Context, string) error { return nil }
func (r replayRPC) Abort(context.Context) error          { return nil }
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
	return []string{"mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1", "mecha-post-compaction-notification-v1"}
}
func managedLegacyCommandsForTest() []string {
	return []string{"goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"}
}

func expectedManagedActiveToolsForTransportTest(manifest managed.Manifest) []string {
	active := make([]string, 0, len(manifest.Tools)+9)
	active = append(active, "read", "bash", "edit", "write", "subagent", "get_subagent_result", "steer_subagent", "list_subagents", "stop_subagent")
	for _, tool := range manifest.Tools {
		active = append(active, tool.Name)
	}
	sort.Strings(active)
	return active
}

func TestLaunchReplayLifecycleUsesOneWorkerWideOperation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	active := expectedManagedActiveToolsForTransportTest(manifest)
	processFactory := &replayProcessFactory{release: make(chan struct{}), started: make(chan struct{})}
	rpcFactory := &replayRPCFactory{active: active}
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: processFactory, RPCFactory: rpcFactory, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer func() { server.Close(); _ = router.Close(ctx); _ = durable.Close() }()

	request := func(id, body string) (int, []byte, http.Header) {
		req, requestErr := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents", strings.NewReader(body))
		require.NoError(t, requestErr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", id)
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
	require.Eventually(t, func() bool {
		select {
		case <-processFactory.started:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	pendingStatus, pendingResponse, pendingHeaders := request("123e4567-e89b-42d3-a456-426614174081", alphaBody)
	require.Equal(t, alphaStatus, pendingStatus)
	require.Equal(t, alphaResponse, pendingResponse)
	require.Equal(t, alphaHeaders.Get("X-Request-ID"), pendingHeaders.Get("X-Request-ID"))
	conflictStatus, _, _ := request("123e4567-e89b-42d3-a456-426614174081", `{"name":"alpha","cwd":"`+root+`","initial_instruction":"different"}`)
	require.Equal(t, http.StatusConflict, conflictStatus)

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

func TestUISessionRouteIsNotAnActiveManagedRoute(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	response, err := http.Post(server.URL+"/v1/ui-session", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNotFound, response.StatusCode)
}

func TestManagedRestartHTTPContract(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	request := func(id, body string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents/missing/restart", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return response
	}
	assertError := func(response *http.Response, status int, code string, requestID any) {
		t.Helper()
		defer response.Body.Close()
		require.Equal(t, status, response.StatusCode)
		require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
		require.Equal(t, code, payload["error"].(map[string]any)["code"])
		require.Equal(t, requestID, payload["request_id"])
	}
	assertError(request("", `{}`), http.StatusBadRequest, "MISSING_REQUEST_ID", nil)
	assertError(request("not-a-uuid", `{}`), http.StatusBadRequest, "INVALID_REQUEST_ID", nil)
	valid := "123e4567-e89b-42d3-a456-426614174081"
	assertError(request(valid, `{"unexpected":true}`), http.StatusBadRequest, "UNKNOWN_FIELD", valid)
	assertError(request(valid, `{"model":{"provider":"openai-codex"}}`), http.StatusUnprocessableEntity, "INVALID_MODEL", valid)
	assertError(request(valid, `{}`), http.StatusNotFound, "AGENT_NOT_FOUND", valid)
}

func TestManagedFailedAgentRestartReturnsOK(t *testing.T) {
	server, durable := newManagedHTTPTestServer(t)
	ctx := context.Background()
	// Insert an agent in failed state
	failedAgent := store.ManagedAgentRecord{
		AgentID:          "failed-worker",
		Name:             "failed-worker",
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
		DesiredState:     "running",
		LifecycleState:   "failed",
		ActivityState:    "failed",
		Runtime:          "embedded",
		PiSessionID:      "test-session",
		SessionEpoch:     1,
		ManifestRevision: 1,
		ManifestHash:     strings.Repeat("a", 64),
		LastErrorCode:    "EMBEDDED_START_FAILED",
		LastErrorMessage: "claimed restart wake requires reconciliation before rotation",
	}
	op := store.ManagedOperation{
		OperationID:       "launch-op-1",
		Method:            http.MethodPost,
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "123e4567-e89b-42d3-a456-426614174099",
		CanonicalBodyHash: "hash",
		ResponseStatus:    http.StatusAccepted,
		ResponseBody:      []byte(`{}`),
	}
	require.NoError(t, durable.ReserveManagedLaunchWithLimit(ctx, failedAgent, op, 10))

	// POST /v1/managed-agents/failed-worker/restart
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents/failed-worker/restart", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var payload map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.Equal(t, true, payload["ok"])

	// Verify agent state was reset to idle/running with error cleared
	reloaded, err := durable.ManagedAgent(ctx, "failed-worker")
	require.NoError(t, err)
	require.Equal(t, "running", reloaded.LifecycleState)
	require.Equal(t, "idle", reloaded.ActivityState)
	require.Empty(t, reloaded.LastErrorCode)
	require.Empty(t, reloaded.LastErrorMessage)
}

func TestManagedResetHTTPContract(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	request := func(id, body string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents/missing/reset", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return response
	}
	assertError := func(response *http.Response, status int, code string, requestID any) {
		t.Helper()
		defer response.Body.Close()
		require.Equal(t, status, response.StatusCode)
		require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
		require.Equal(t, code, payload["error"].(map[string]any)["code"])
		require.Equal(t, requestID, payload["request_id"])
	}
	assertError(request("", `{}`), http.StatusBadRequest, "MISSING_REQUEST_ID", nil)
	assertError(request("not-a-uuid", `{}`), http.StatusBadRequest, "INVALID_REQUEST_ID", nil)
	valid := "123e4567-e89b-42d3-a456-426614174081"
	assertError(request(valid, `{"unexpected":true}`), http.StatusBadRequest, "UNKNOWN_FIELD", valid)
	assertError(request(valid, `{}`), http.StatusNotFound, "AGENT_NOT_FOUND", valid)
}

func TestManagedRestartAllHTTPContractStoresAndReplaysOneBatchResponse(t *testing.T) {
	server, durable := newManagedHTTPTestServer(t)
	requestID := "123e4567-e89b-42d3-a456-426614174090"
	request := func(method, id, body string) (*http.Response, []byte) {
		req, err := http.NewRequest(method, server.URL+"/v1/managed-agents/restart-all", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		payload, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		return response, payload
	}
	first, firstBody := request(http.MethodPost, requestID, `{}`)
	require.Equal(t, http.StatusAccepted, first.StatusCode)
	require.Equal(t, "no-store", first.Header.Get("Cache-Control"))
	require.Equal(t, requestID, first.Header.Get("X-Request-ID"))
	require.Equal(t, "application/json; charset=utf-8", first.Header.Get("Content-Type"))
	var response struct {
		OperationID string `json:"operation_id"`
		Agents      []any  `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(firstBody, &response))
	require.Equal(t, requestID, response.OperationID)
	require.Empty(t, response.Agents)
	replay, replayBody := request(http.MethodPost, requestID, `{}`)
	require.Equal(t, first.StatusCode, replay.StatusCode)
	require.Equal(t, firstBody, replayBody)

	invalid, _ := request(http.MethodPost, "123e4567-e89b-42d3-a456-426614174091", `{"unexpected":true}`)
	require.Equal(t, http.StatusBadRequest, invalid.StatusCode)
	wrongMethod, _ := request(http.MethodGet, "", ``)
	require.Equal(t, http.StatusNotFound, wrongMethod.StatusCode)
	stored, err := durable.FindManagedOperation(context.Background(), http.MethodPost, requestID)
	require.NoError(t, err)
	require.Equal(t, firstBody, stored.ResponseBody)
	_, err = durable.FindManagedOperation(context.Background(), http.MethodPost, "123e4567-e89b-42d3-a456-426614174091")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestManagedRestartAllHTTPRejectsEveryStrictRequestBoundary(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	requestID := "123e4567-e89b-42d3-a456-426614174092"
	request := func(id, contentType, body string) (int, string) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/managed-agents/restart-all", strings.NewReader(body))
		require.NoError(t, err)
		if id != "" {
			req.Header.Set("X-Request-ID", id)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
		return response.StatusCode, payload.Error.Code
	}
	for _, testCase := range []struct {
		name, id, contentType, body, code string
		status                            int
	}{
		{name: "missing request ID", contentType: "application/json", body: `{}`, status: http.StatusBadRequest, code: "MISSING_REQUEST_ID"},
		{name: "invalid request ID", id: "invalid", contentType: "application/json", body: `{}`, status: http.StatusBadRequest, code: "INVALID_REQUEST_ID"},
		{name: "non JSON content type", id: requestID, contentType: "text/plain", body: `{}`, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "malformed JSON", id: requestID, contentType: "application/json", body: `{`, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "duplicate key", id: requestID, contentType: "application/json", body: `{"x":1,"x":2}`, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "non object", id: requestID, contentType: "application/json", body: `[]`, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "trailing JSON", id: requestID, contentType: "application/json", body: `{} {}`, status: http.StatusBadRequest, code: "INVALID_JSON"},
		{name: "unknown field", id: requestID, contentType: "application/json", body: `{"x":1}`, status: http.StatusBadRequest, code: "UNKNOWN_FIELD"},
		{name: "over limit", id: requestID, contentType: "application/json", body: strings.Repeat(" ", 64*1024+1), status: http.StatusRequestEntityTooLarge, code: "PAYLOAD_TOO_LARGE"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			status, code := request(testCase.id, testCase.contentType, testCase.body)
			require.Equal(t, testCase.status, status)
			require.Equal(t, testCase.code, code)
		})
	}
}

func TestQuietHistoryRequiresConsumedManagedMailSendAuthorization(t *testing.T) {
	server, durable := newManagedHTTPTestServer(t)
	require.NoError(t, durable.SetCanonicalQuietRecipient(context.Background(), "supervisor"))
	post := func(id, controlRequestID string) {
		payload := strings.NewReader(`{"id":"` + id + `","from":"managed-agent","to":"supervisor","body":"quiet candidate ` + id + `","created_at":"2030-01-01T00:00:00.000Z","source":{"kind":"agent"}}`)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", payload)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if controlRequestID != "" {
			req.Header.Set("X-Mecha-Managed-Control-Request-ID", controlRequestID)
		}
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusCreated, response.StatusCode)
	}
	post("ordinary-agent-message", "")
	quiet, err := http.Get(server.URL + "/v1/history?agent_id=managed-agent&view=quiet")
	require.NoError(t, err)
	defer quiet.Body.Close()
	var empty store.ManagedHistoryPage
	require.NoError(t, json.NewDecoder(quiet.Body).Decode(&empty))
	require.Empty(t, empty.Entries, "caller-provided source.kind must not classify a message as quiet")

	require.NoError(t, durable.ReserveManagedQuietSendAuthorization(context.Background(), "control-1", "managed-agent", "supervisor"))
	post("trusted-tool-message", "control-1")
	quiet, err = http.Get(server.URL + "/v1/history?agent_id=managed-agent&view=quiet")
	require.NoError(t, err)
	defer quiet.Body.Close()
	var page store.ManagedHistoryPage
	require.NoError(t, json.NewDecoder(quiet.Body).Decode(&page))
	require.Len(t, page.Entries, 1)
	require.Equal(t, "trusted-tool-message", page.Entries[0].ID)
}

func TestManagedHistoryUsesTheDirectPagedContract(t *testing.T) {
	server, durable := newManagedHTTPTestServer(t)
	_, err := durable.AppendManagedCompleteHistory(context.Background(), "alpha", "lifecycle", "info", "Managed worker entered running")
	require.NoError(t, err)
	response, err := http.Get(server.URL + "/v1/history?agent_id=alpha&view=complete&limit=1")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var body struct {
		View    string `json:"view"`
		Entries []struct {
			ID      string `json:"id"`
			AgentID string `json:"agent_id"`
			Message string `json:"message"`
		} `json:"entries"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Equal(t, "complete", body.View)
	require.Len(t, body.Entries, 1)
	require.Equal(t, "alpha", body.Entries[0].AgentID)
	require.Equal(t, "Managed worker entered running", body.Entries[0].Message)
	invalid, err := http.Get(server.URL + "/v1/history?agent_id=alpha&view=complete&unknown=value")
	require.NoError(t, err)
	defer invalid.Body.Close()
	require.Equal(t, http.StatusUnprocessableEntity, invalid.StatusCode)
}

func TestManagedHistoryHTTPPreservesPresentEmptyMessageDetailsAndOmitsLegacyNulls(t *testing.T) {
	server, durable := newManagedHTTPTestServer(t)
	ctx := context.Background()
	const agentID = "history-agent"
	reserveUILiveAgent(t, durable, agentID)
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agentID, agentID+"-session", store.ProcessIdentityRecord{PID: 701, StartMarker: "history-marker", Incarnation: "history-incarnation"}, "history-generation"))
	fence := store.ManagedRuntimeFence{AgentID: agentID, PiSessionID: agentID + "-session", SessionEpoch: 1, ProcessPID: 701, ProcessStartMarker: "history-marker", ProcessIncarnation: "history-incarnation", RuntimeGeneration: "history-generation", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	snapshot := store.ManagedChildSnapshot{SnapshotID: "history-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"history-agent-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"history-snapshot"}`), SHA256: strings.Repeat("b", 64)}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	legacy, err := durable.AppendManagedCompleteHistory(ctx, agentID, "managed_lifecycle", "info", "Legacy detail-free event")
	require.NoError(t, err)
	source := protocol.MessageSource{Kind: "agent"}
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "empty-http-body", From: "original-sender", To: agentID, Body: "", CreatedAt: "2030-01-01T00:00:00.000Z", Source: &source})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, "empty-http-body")
	require.NoError(t, err)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *attempt, "http-cursor"))
	changed, history, err := durable.AcknowledgeManagedDirectResultWithHistory(ctx, *attempt, "http-receipt", true)
	require.NoError(t, err)
	require.True(t, changed)

	response, err := http.Get(server.URL + "/v1/history?agent_id=" + agentID + "&view=complete&limit=10")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var page struct {
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&page))
	require.Len(t, page.Entries, 2)
	byID := make(map[string]map[string]json.RawMessage, len(page.Entries))
	for _, entry := range page.Entries {
		var id string
		require.NoError(t, json.Unmarshal(entry["id"], &id))
		byID[id] = entry
	}
	require.JSONEq(t, `""`, string(byID[history.ID]["message_content"]))
	require.JSONEq(t, `"original-sender"`, string(byID[history.ID]["sender_identity"]))
	require.NotContains(t, byID[legacy.ID], "message_content")
	require.NotContains(t, byID[legacy.ID], "sender_identity")
}

func TestManagedRestartAllMissingManagerReturnsSafeInternalError(t *testing.T) {
	router := &Router{}
	req := httptest.NewRequest(http.MethodPost, "/v1/managed-agents/restart-all", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174093")
	response := httptest.NewRecorder()
	require.True(t, router.handleManagedHTTP(response, req))
	require.Equal(t, http.StatusInternalServerError, response.Code)
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, "INTERNAL", payload.Error.Code)
}

func TestManagedLaunchErrorsDescribeNameCollisionsForEveryReservationLayer(t *testing.T) {
	for _, launchErr := range []error{
		&store.ManagedNameConflictError{Name: "alpha"},
		&store.RecipientSourceConflictError{Recipient: "alpha"},
	} {
		candidate, ok := managedLaunchError(launchErr).(*managedHTTPError)
		require.True(t, ok)
		require.Equal(t, http.StatusConflict, candidate.status)
		require.Equal(t, "NAME_IN_USE", candidate.code)
		require.Equal(t, "That name is in use by an active or launching agent. Choose another name or wait for it to stop.", candidate.message)
	}
}

func TestManagedLaunchErrorsDescribeGitWorktreeErrors(t *testing.T) {
	cases := []struct {
		name            string
		err             error
		expectedMessage string
	}{
		{
			name:            "already used by worktree",
			err:             fmt.Errorf("failed to create git worktree: git worktree add failed: exit status 128: fatal: 'main' is already used by worktree at '/foo/bar'"),
			expectedMessage: "Git worktree error: 'main' is already used by worktree at '/foo/bar'",
		},
		{
			name:            "already exists",
			err:             fmt.Errorf("failed to create git worktree: git worktree add failed: exit status 255: Preparing worktree (new branch 'live-domains')\nfatal: a branch named 'live-domains' already exists"),
			expectedMessage: "Git worktree error: a branch named 'live-domains' already exists",
		},
		{
			name:            "worktree branch is required",
			err:             errors.New("worktree branch is required"),
			expectedMessage: "Worktree branch is required",
		},
		{
			name:            "not a git repository",
			err:             errors.New("working directory is not a git repository"),
			expectedMessage: "Working directory is not a git repository",
		},
		{
			name:            "not main git worktree",
			err:             errors.New("working directory is not the main git worktree"),
			expectedMessage: "Working directory is not the main git worktree",
		},
		{
			name:            "failed to generate worktree path",
			err:             fmt.Errorf("failed to generate worktree path: baseRepoPath cannot be empty"),
			expectedMessage: "Failed to generate worktree path: baseRepoPath cannot be empty",
		},
		{
			name:            "git worktree add failed generic",
			err:             fmt.Errorf("failed to create git worktree: git worktree add failed: exit status 1: permission denied"),
			expectedMessage: "Git worktree error: permission denied",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate, ok := managedLaunchError(tc.err).(*managedHTTPError)
			require.True(t, ok)
			require.Equal(t, http.StatusUnprocessableEntity, candidate.status)
			require.Equal(t, "VALIDATION_FAILED", candidate.code)
			require.Equal(t, tc.expectedMessage, candidate.message)
		})
	}
}

func TestManagedRestartAllErrorStaysWithinTheSafeRouteContract(t *testing.T) {
	idempotency := managedRestartAllError(&store.ManagedIdempotencyConflictError{})
	candidate, ok := idempotency.(*managedHTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusConflict, candidate.status)
	require.Equal(t, "IDEMPOTENCY_CONFLICT", candidate.code)
	internal := managedRestartAllError(errors.New("private dispatch detail"))
	candidate, ok = internal.(*managedHTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusInternalServerError, candidate.status)
	require.Equal(t, "INTERNAL", candidate.code)
	require.NotContains(t, candidate.message, "dispatch")
}

func TestModelsAndDefaultModelUseTheDirectManagedEnvelope(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)
	modelsResponse, err := http.Get(server.URL + "/v1/models")
	require.NoError(t, err)
	defer modelsResponse.Body.Close()
	require.Equal(t, http.StatusOK, modelsResponse.StatusCode)
	var catalog struct {
		AuthRevision int64               `json:"auth_revision"`
		Models       []managed.Model     `json:"models"`
		DefaultModel *store.ManagedModel `json:"default_model"`
	}
	require.NoError(t, json.NewDecoder(modelsResponse.Body).Decode(&catalog))
	require.Zero(t, catalog.AuthRevision)
	require.Nil(t, catalog.DefaultModel)
	require.Contains(t, catalog.Models, managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Available: true})

	requestID := "123e4567-e89b-42d3-a456-426614174081"
	request, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/default-model", strings.NewReader(`{"model":{"provider":"openai-codex","id":"gpt-5.6-terra"}}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", requestID)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"operation_id":"`+requestID+`"`)
	require.NotContains(t, string(body), "credential")

	// Exact request-ID replay does not need a second mutation and carries the
	// original non-secret response.
	replay, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/default-model", strings.NewReader(`{"model":{"provider":"openai-codex","id":"gpt-5.6-terra"}}`))
	require.NoError(t, err)
	replay.Header = request.Header.Clone()
	replayResponse, err := http.DefaultClient.Do(replay)
	require.NoError(t, err)
	defer replayResponse.Body.Close()
	replayBody, err := io.ReadAll(replayResponse.Body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(replayBody))
}

func sortedJSONKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestManagedAgentInterruptAndSubagentStopHTTP(t *testing.T) {
	server, _ := newManagedHTTPTestServer(t)

	// POST /managed-agents/agent-1/interrupt and /v1/managed-agents/agent-1/interrupt
	for _, path := range []string{"/managed-agents/agent-1/interrupt", "/v1/managed-agents/agent-1/interrupt"} {
		req, err := http.NewRequest(http.MethodPost, server.URL+path, nil)
		require.NoError(t, err)
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		require.Equal(t, http.StatusOK, res.StatusCode)
		var body map[string]any
		require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
		require.Equal(t, true, body["ok"])
	}

	// POST /managed-agents/agent-1/subagents/sub-1/stop and /v1/managed-agents/agent-1/subagents/sub-1/stop
	// When subagent not found, returns 500 error
	for _, path := range []string{"/managed-agents/agent-1/subagents/sub-1/stop", "/v1/managed-agents/agent-1/subagents/sub-1/stop"} {
		req, err := http.NewRequest(http.MethodPost, server.URL+path, nil)
		require.NoError(t, err)
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		require.Equal(t, http.StatusInternalServerError, res.StatusCode)
	}
}
