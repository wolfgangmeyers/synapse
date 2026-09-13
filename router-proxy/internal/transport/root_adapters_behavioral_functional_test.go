package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// This suite intentionally drives the installed root handler, the real Python
// host adapter, and the task router over a temporary SQLite store. Supervisor
// state is isolated with MECHA_STATE_FILE. The decoy router is given a
// different SQLite file and URL; it must remain empty throughout the test.
type rootAdapterPythonExecutor struct {
	routerURL      string
	ambient        string
	home           string
	stateFile      string
	searchEndpoint string

	mu    sync.Mutex
	calls map[string]int
}

func (e *rootAdapterPythonExecutor) run(ctx context.Context, mode string, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	request := domain1RunnerRequest{
		RequestID: invocation.RequestID, ResultID: invocation.ResultID, ToolCallID: invocation.ToolCallID,
		OperationID: invocation.OperationID, Identity: identity, Arguments: invocation.Arguments,
		Fence: map[string]any{
			"agent_id": invocation.Fence.AgentID, "pi_session_id": invocation.Fence.PiSessionID,
			"session_epoch": invocation.Fence.SessionEpoch, "process_pid": invocation.Fence.ProcessPID,
			"process_start_marker": invocation.Fence.ProcessStartMarker, "process_incarnation": invocation.Fence.ProcessIncarnation,
			"runtime_generation": invocation.Fence.RuntimeGeneration, "manifest_revision": invocation.Fence.ManifestRevision,
			"manifest_hash": invocation.Fence.ManifestHash,
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return managed.RootControlHandlerResult{}, err
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		return managed.RootControlHandlerResult{}, err
	}
	script := filepath.Join(root, ".claude", "skills", "supervisor", "tools", "managed_root_control.py")
	python, err := exec.LookPath("python3")
	if err != nil {
		return managed.RootControlHandlerResult{}, err
	}
	command := exec.CommandContext(ctx, python, script, mode, "--home", e.home)
	environment := make([]string, 0, len(os.Environ())+4)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "MECHA_ROUTER_URL=") || strings.HasPrefix(value, "MECHA_STATE_FILE=") || strings.HasPrefix(value, "MECHA_MANAGED_WEB_SEARCH_ENDPOINT=") {
			continue
		}
		environment = append(environment, value)
	}
	if e.ambient != "" {
		environment = append(environment, "MECHA_ROUTER_URL="+e.ambient)
	}
	environment = append(environment, "MECHA_ROUTER_URL="+e.routerURL, "MECHA_STATE_FILE="+e.stateFile, "MECHA_MANAGED_WEB_SEARCH_ENDPOINT="+e.searchEndpoint)
	command.Env = environment
	command.Stdin = bytes.NewReader(encoded)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	e.mu.Lock()
	e.calls[identity.OperationID]++
	e.mu.Unlock()
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return managed.RootControlHandlerResult{}, fmt.Errorf("runner failed: %s", detail)
	}
	var result domain1RunnerResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return managed.RootControlHandlerResult{}, err
	}
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	return managed.RootControlHandlerResult{Status: result.Status, Content: result.Content, Details: result.Details, ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage}, nil
}

func (e *rootAdapterPythonExecutor) ExecuteRootControl(ctx context.Context, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	return e.run(ctx, "execute", identity, invocation)
}

func (e *rootAdapterPythonExecutor) ReconcileRootControl(ctx context.Context, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	return e.run(ctx, "reconcile", identity, invocation)
}

func (e *rootAdapterPythonExecutor) callCount(operation string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[operation]
}

type rootAdapterFixture struct {
	ctx         context.Context
	root        string
	manifest    managed.Manifest
	store       *store.Store
	decoy       *store.Store
	router      *Router
	decoyRouter *Router
	server      *httptest.Server
	decoyServer *httptest.Server
	rpc         *domain1RootRPC
	executor    *rootAdapterPythonExecutor
	agent       *store.ManagedAgentRecord
	fence       store.ManagedRuntimeFence
	stateFile   string
	sequence    int
	stateTaskID int
	searchMu    sync.Mutex
	queries     []string
	decoyHits   atomic.Int32
}

func newRootAdapterFixture(t *testing.T) *rootAdapterFixture {
	t.Helper()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	fixture := &rootAdapterFixture{ctx: ctx, root: root, manifest: manifest, stateFile: filepath.Join(t.TempDir(), "supervisor-state.json")}
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fixture.searchMu.Lock()
		fixture.queries = append(fixture.queries, req.URL.Query().Get("q"))
		fixture.searchMu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><li class="b_algo"><h2><a href="https://fixture.test/source">Deterministic fixture source</a></h2><div class="b_caption"><p>fixture snippet</p></div></li></html>`)
	}))

	var errOpen error
	fixture.store, errOpen = store.Open(ctx, filepath.Join(t.TempDir(), "task.db"), store.Options{Clock: clock})
	require.NoError(t, errOpen)
	fixture.decoy, errOpen = store.Open(ctx, filepath.Join(t.TempDir(), "decoy.db"), store.Options{Clock: clock})
	require.NoError(t, errOpen)
	fixture.decoyRouter, errOpen = New(Options{Store: fixture.decoy, Clock: clock})
	require.NoError(t, errOpen)
	fixture.decoyServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fixture.decoyHits.Add(1)
		fixture.decoyRouter.ServeHTTP(w, req)
	}))

	process := &managedTestProcess{identity: managed.ProcessIdentity{PID: 1801, StartMarker: "root-adapter-start", Incarnation: "root-adapter-incarnation"}, stopped: make(chan struct{})}
	session := ""
	fixture.rpc = &domain1RootRPC{managedTestRPC: managedTestRPC{session: &session, active: expectedManagedActiveToolsForTransport(manifest)}, store: fixture.store, installed: make(chan struct{}), submissions: make(chan domain1Submission, 64)}
	factory := &domain1ProcessFactory{process: process, rpc: fixture.rpc, session: &session}
	fixture.executor = &rootAdapterPythonExecutor{home: t.TempDir(), stateFile: fixture.stateFile, ambient: fixture.decoyServer.URL, searchEndpoint: search.URL + "/search", calls: map[string]int{}}
	handlers, err := managed.NewOperationalRootControlHandlers(manifest, fixture.executor)
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: fixture.store, Config: managed.Config{RouterURL: "http://127.0.0.1:1", AllowedWorkingDirectoryRoots: []string{root}},
		Manifest: manifest, ProcessFactory: factory, RPCFactory: factory, RootControlHandlers: handlers,
		CatalogSource:              managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	fixture.router, err = New(Options{Store: fixture.store, Clock: clock, Managed: manager})
	require.NoError(t, err)
	fixture.server = httptest.NewServer(fixture.router)
	fixture.executor.routerURL = fixture.server.URL
	require.NoError(t, manager.SetRouterURL(fixture.server.URL))

	t.Cleanup(func() {
		search.Close()
		fixture.server.Close()
		fixture.decoyServer.Close()
		_ = fixture.router.Close(ctx)
		_ = fixture.decoyRouter.Close(ctx)
		_ = manager.Close(ctx)
		_ = fixture.store.Close()
		_ = fixture.decoy.Close()
	})

	request, err := http.NewRequest(http.MethodPost, fixture.server.URL+"/v1/managed-agents", strings.NewReader(`{"name":"root-adapter-agent","cwd":"`+root+`","initial_instruction":"ready"}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174101")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	body, readErr := io.ReadAll(response.Body)
	require.NoError(t, readErr)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusAccepted, response.StatusCode, string(body))
	select {
	case <-fixture.rpc.installed:
	case <-time.After(5 * time.Second):
		t.Fatal("root adapter receiver did not install")
	}
	fixture.agent, err = fixture.store.ManagedAgent(ctx, "root-adapter-agent")
	require.NoError(t, err)
	fixture.fence = domain1Fence(fixture.agent)
	return fixture
}

func (f *rootAdapterFixture) tool(t *testing.T, capability string) managed.ManifestTool {
	t.Helper()
	for _, candidate := range f.manifest.Tools {
		if candidate.CapabilityID == capability {
			return candidate
		}
	}
	t.Fatalf("manifest capability %q not found", capability)
	return managed.ManifestTool{}
}

func (f *rootAdapterFixture) event(tool managed.ManifestTool, arguments any) managed.RootControlEvent {
	f.sequence++
	return domain1Event(f.fence, tool, fmt.Sprintf("123e4567-e89b-42d3-a456-%012d", f.sequence), fmt.Sprintf("root-adapter-call-%d", f.sequence), arguments)
}

func (f *rootAdapterFixture) invoke(t *testing.T, tool managed.ManifestTool, arguments any) (store.ManagedControlRequest, managed.RootControlEvent) {
	t.Helper()
	event := f.event(tool, arguments)
	require.NoError(t, f.rpc.emit(event))
	submission := domain1WaitSubmission(t, f.rpc, f.store, event.RequestID)
	return domain1WaitDelivered(t, f.store, event.RequestID, submission), event
}

func rootAdapterState(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{"tasks": []any{}, "priorities": []any{}}
	}
	require.NoError(t, err)
	var state map[string]any
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

func rootAdapterHostState(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "root-control", "requests.json"))
	require.NoError(t, err)
	var state map[string]any
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

func rootAdapterResultDetails(t *testing.T, row store.ManagedControlRequest) map[string]any {
	t.Helper()
	var details map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.ResultDetailsJSON), &details))
	return details
}

func requireRootAdapterOK(t *testing.T, row store.ManagedControlRequest) {
	t.Helper()
	require.Equal(t, "delivered", row.State)
	require.Equal(t, "ok", row.ResultStatus, row.ErrorCode+": "+row.ErrorDetail)
	require.NotEmpty(t, row.ResultContent)
}

func TestRootAdaptersBehavioralFunctionalTable(t *testing.T) {
	fixture := newRootAdapterFixture(t)

	tests := []struct {
		capability string
		run        func(*testing.T, *rootAdapterFixture, managed.ManifestTool)
	}{
		{capability: "mecha_state", run: func(t *testing.T, f *rootAdapterFixture, tool managed.ManifestTool) {
			createRow, create := f.invoke(t, tool, map[string]any{"action": "create", "alias": "root-state", "type": "functional", "project": "root-adapters", "description": "root adapter state"})
			requireRootAdapterOK(t, createRow)
			state := rootAdapterState(t, f.stateFile)
			tasks, ok := state["tasks"].([]any)
			require.True(t, ok)
			require.Len(t, tasks, 1)
			task, ok := tasks[0].(map[string]any)
			require.True(t, ok)
			f.stateTaskID = int(task["id"].(float64))
			require.Equal(t, "root-state", task["alias"])

			row, _ := f.invoke(t, tool, map[string]any{"action": "update", "task": "root-state", "field": "summary", "value": "updated through root adapter"})
			requireRootAdapterOK(t, row)
			state = rootAdapterState(t, f.stateFile)
			tasks = state["tasks"].([]any)
			task = tasks[0].(map[string]any)
			require.Equal(t, "updated through root adapter", task["last_summary"])

			// Exact replay returns the same durable result and does not invoke the
			// Python process a second time. A changed payload is a conflict.
			beforeCalls := f.executor.callCount(tool.OperationID)
			beforeState := string(mustReadFile(t, f.stateFile))
			require.NoError(t, f.rpc.emit(create))
			domain1AssertNoSubmission(t, f.rpc)
			replayed, err := f.store.ManagedControlRequest(f.ctx, create.RequestID)
			require.NoError(t, err)
			require.Equal(t, createRow.ResultID, replayed.ResultID)
			require.Equal(t, createRow.ResultContent, replayed.ResultContent)
			require.Equal(t, beforeCalls, f.executor.callCount(tool.OperationID))
			require.Equal(t, beforeState, string(mustReadFile(t, f.stateFile)))
			conflict := create
			conflict.Arguments = map[string]any{"action": "create", "alias": "root-state", "type": "functional", "project": "root-adapters", "description": "changed"}
			require.NoError(t, f.rpc.emit(conflict))
			domain1AssertNoSubmission(t, f.rpc)
			require.Equal(t, beforeCalls, f.executor.callCount(tool.OperationID))
			require.Equal(t, beforeState, string(mustReadFile(t, f.stateFile)))
		}},
		{capability: "mecha_priority", run: func(t *testing.T, f *rootAdapterFixture, tool managed.ManifestTool) {
			ids := []any{json.Number(fmt.Sprint(f.stateTaskID))}
			row, _ := f.invoke(t, tool, map[string]any{"action": "create", "priorityId": "root-priority", "name": "Root priority", "taskIds": ids})
			requireRootAdapterOK(t, row)
			row, _ = f.invoke(t, tool, map[string]any{"action": "update", "priorityId": "root-priority", "name": "Updated root priority", "status": "blocked", "taskIds": ids})
			requireRootAdapterOK(t, row)
			row, _ = f.invoke(t, tool, map[string]any{"action": "status", "priorityId": "root-priority", "status": "completed", "evidence": "functional fixture"})
			requireRootAdapterOK(t, row)
			state := rootAdapterState(t, f.stateFile)
			priorities := state["priorities"].([]any)
			require.Len(t, priorities, 1)
			priority := priorities[0].(map[string]any)
			require.Equal(t, "root-priority", priority["id"])
			require.Equal(t, "completed", priority["status"])
			require.Equal(t, "functional fixture", priority["completion"].(map[string]any)["evidence"])
		}},
		{capability: "mecha_verification_handoff", run: func(t *testing.T, f *rootAdapterFixture, tool managed.ManifestTool) {
			row, _ := f.invoke(t, tool, map[string]any{"action": "record", "task": "root-state", "reminderId": "root-verification", "successor": "root-successor", "remoteVerified": "true"})
			requireRootAdapterOK(t, row)
			task := rootAdapterState(t, f.stateFile)["tasks"].([]any)[0].(map[string]any)
			handoff := task["verification_handoff"].(map[string]any)
			require.Equal(t, "root-verification", handoff["reminder_id"])
			require.Equal(t, true, handoff["remote_verified"])
			require.Equal(t, "root-successor", handoff["successor"])
		}},
		{capability: "mecha_recall", run: func(t *testing.T, f *rootAdapterFixture, tool managed.ManifestTool) {
			for _, args := range []map[string]any{{"action": "enable", "stores": []any{"fixture.md"}}, {"action": "status"}, {"action": "disable"}} {
				row, _ := f.invoke(t, tool, args)
				requireRootAdapterOK(t, row)
			}
			settings := rootAdapterHostState(t, f.executor.home)["settings"].(map[string]any)[f.agent.AgentID].(map[string]any)
			require.Equal(t, "disable", settings[tool.OperationID].(map[string]any)["action"])
		}},
		{capability: "mecha_settled_notifications", run: func(t *testing.T, f *rootAdapterFixture, tool managed.ManifestTool) {
			for _, action := range []string{"enable", "status", "disable"} {
				row, _ := f.invoke(t, tool, map[string]any{"action": action})
				requireRootAdapterOK(t, row)
			}
			settings := rootAdapterHostState(t, f.executor.home)["settings"].(map[string]any)[f.agent.AgentID].(map[string]any)
			require.Equal(t, "disable", settings[tool.OperationID].(map[string]any)["action"])
		}},
		{capability: "mecha_context", run: func(t *testing.T, f *rootAdapterFixture, tool managed.ManifestTool) {
			for _, args := range []map[string]any{{"action": "status"}, {"action": "set_policy", "policy": map[string]any{"levels": []any{}}, "customInstructions": "bounded fixture", "postCompactionNotification": "fixture"}} {
				row, _ := f.invoke(t, tool, args)
				requireRootAdapterOK(t, row)
			}
			settings := rootAdapterHostState(t, f.executor.home)["settings"].(map[string]any)[f.agent.AgentID].(map[string]any)
			policy := settings[tool.OperationID].(map[string]any)
			require.Equal(t, "set_policy", policy["action"])
		}},
		{capability: "goal", run: func(t *testing.T, f *rootAdapterFixture, tool managed.ManifestTool) {
			row, _ := f.invoke(t, tool, map[string]any{"action": "set", "goal": "finish root adapter proof"})
			requireRootAdapterOK(t, row)
			policy, err := f.store.ManagedGoalPolicyFor(f.ctx, f.agent.AgentID)
			require.NoError(t, err)
			require.True(t, policy.Config.Enabled)
			require.Equal(t, "finish root adapter proof", policy.Config.Goal)
			row, _ = f.invoke(t, tool, map[string]any{"action": "status"})
			requireRootAdapterOK(t, row)
			require.Contains(t, row.ResultContent, "active")
			row, _ = f.invoke(t, tool, map[string]any{"action": "off"})
			requireRootAdapterOK(t, row)
			policy, err = f.store.ManagedGoalPolicyFor(f.ctx, f.agent.AgentID)
			require.NoError(t, err)
			require.False(t, policy.Config.Enabled)
		}},
		{capability: "web_search", run: func(t *testing.T, f *rootAdapterFixture, tool managed.ManifestTool) {
			row, _ := f.invoke(t, tool, map[string]any{"query": "deterministic fixture", "freshness": "cached", "maxResults": json.Number("1")})
			requireRootAdapterOK(t, row)
			details := rootAdapterResultDetails(t, row)
			require.Equal(t, "bing", details["provider"])
			sources := details["sources"].([]any)
			require.Len(t, sources, 1)
			require.Equal(t, "https://fixture.test/source", sources[0].(map[string]any)["url"])
			f.searchMu.Lock()
			queries := append([]string(nil), f.queries...)
			f.searchMu.Unlock()
			require.Equal(t, []string{"deterministic fixture"}, queries)
		}},
	}

	seen := map[string]bool{}
	for _, testCase := range tests {
		tool := fixture.tool(t, testCase.capability)
		require.False(t, seen[tool.OperationID], "duplicate table operation %s", tool.OperationID)
		seen[tool.OperationID] = true
		t.Run(tool.OperationID, func(t *testing.T) { testCase.run(t, fixture, tool) })
	}
	require.Zero(t, fixture.decoyHits.Load(), "ambient decoy router must remain untouched")
}

func TestRootAdaptersBehavioralFunctionalInvalidClosure(t *testing.T) {
	fixture := newRootAdapterFixture(t)
	beforeState := string(optionalReadFile(t, fixture.stateFile))
	invalid := []struct {
		name       string
		capability string
		args       map[string]any
		mutate     func(*managed.RootControlEvent)
	}{
		{name: "invalid action", capability: "mecha_recall", args: map[string]any{"action": "bogus"}},
		{name: "unknown field", capability: "mecha_recall", args: map[string]any{"action": "status", "unknown": true}},
		{name: "unknown operation", capability: "mecha_recall", args: map[string]any{"action": "status"}, mutate: func(event *managed.RootControlEvent) { event.OperationID = "unknown.operation" }},
	}
	seen := map[string]bool{}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			tool := fixture.tool(t, testCase.capability)
			seen[testCase.name] = true
			event := fixture.event(tool, testCase.args)
			if testCase.mutate != nil {
				testCase.mutate(&event)
			}
			beforeCalls := fixture.executor.callCount(tool.OperationID)
			require.NoError(t, fixture.rpc.emit(event))
			domain1AssertNoSubmission(t, fixture.rpc)
			_, err := fixture.store.ManagedControlRequest(fixture.ctx, event.RequestID)
			require.Error(t, err)
			require.Equal(t, beforeCalls, fixture.executor.callCount(tool.OperationID))
			require.Equal(t, beforeState, string(optionalReadFile(t, fixture.stateFile)))
		})
	}
	require.Len(t, seen, len(invalid))

	// mail.parent is present in the current revision-2 manifest but its handler
	// is child-only. It produces a truthful correlated error and never starts
	// the Python subprocess or changes either router's state.
	child := fixture.tool(t, "mecha_parent_message")
	beforeCalls := fixture.executor.callCount(child.OperationID)
	row, _ := fixture.invoke(t, child, map[string]any{"message": "root must reject child-only"})
	require.Equal(t, "error", row.ResultStatus)
	require.Equal(t, "CHILD_ONLY_OPERATION", row.ErrorCode)
	require.Equal(t, beforeCalls, fixture.executor.callCount(child.OperationID))
	require.Equal(t, beforeState, string(optionalReadFile(t, fixture.stateFile)))
	require.Zero(t, fixture.decoyHits.Load(), "ambient decoy router must remain untouched")
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func optionalReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return data
}

// Keep the compiler honest if the byte-oriented subprocess boundary changes.
var _ io.Reader = (*bytes.Reader)(nil)
