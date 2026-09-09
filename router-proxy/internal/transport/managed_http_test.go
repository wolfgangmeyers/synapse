package transport

import (
	"context"
	"database/sql"
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
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(context.Background()); _ = durable.Close() })
	return server, durable
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
	return []string{"mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1"}
}
func managedLegacyCommandsForTest() []string {
	return []string{"goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"}
}

func TestLaunchReplayLifecycleUsesOneWorkerWideOperation(t *testing.T) {
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
	assertError(request(valid, `{}`), http.StatusNotFound, "AGENT_NOT_FOUND", valid)
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

func sortedJSONKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
