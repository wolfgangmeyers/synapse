package transport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

var managedRootControlCandidatePaths = managedPhase2CandidatePaths

// Keep the shared native proof lock check available to the neighboring native
// tests in this package. The root test itself still uses the locked executable
// and package selected by the same immutable source lock.
func requireManagedRootControlLockedPi(t *testing.T, root string) managedResetLockedPi {
	t.Helper()
	sourceLockPath := os.Getenv("MECHA_PI_SOURCE_LOCK")
	packageRootPath := os.Getenv("MECHA_PI_PACKAGE_ROOT")
	executablePath := os.Getenv("MECHA_PI_BIN")
	for name, value := range map[string]string{"MECHA_PI_SOURCE_LOCK": sourceLockPath, "MECHA_PI_PACKAGE_ROOT": packageRootPath, "MECHA_PI_BIN": executablePath} {
		require.Truef(t, filepath.IsAbs(value), "%s must be an absolute path supplied to the native proof", name)
	}
	lockedPi := requireManagedResetLockedPiFromLock(t, root, sourceLockPath)
	resolvedPackageRoot, err := filepath.EvalSymlinks(packageRootPath)
	require.NoError(t, err)
	resolvedExecutable, err := filepath.EvalSymlinks(executablePath)
	require.NoError(t, err)
	require.Equal(t, lockedPi.packageRoot, resolvedPackageRoot, "MECHA_PI_PACKAGE_ROOT must match the supplied G4 lock")
	require.Equal(t, lockedPi.executable, resolvedExecutable, "MECHA_PI_BIN must match the supplied G4 lock")
	return lockedPi
}

type nativeRootControlProviderReply struct {
	toolName string
	args     map[string]any
	content  string
}

type nativeRootControlProvider struct {
	requests chan map[string]any
	replies  chan nativeRootControlProviderReply
	server   *httptest.Server
	sequence atomic.Int64
}

func newNativeRootControlProvider(t *testing.T) *nativeRootControlProvider {
	t.Helper()
	provider := &nativeRootControlProvider{requests: make(chan map[string]any, 16), replies: make(chan nativeRootControlProviderReply, 16)}
	provider.server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case provider.requests <- parsed:
		case <-request.Context().Done():
			return
		}
		var reply nativeRootControlProviderReply
		select {
		case reply = <-provider.replies:
		case <-request.Context().Done():
			return
		}
		sequence := provider.sequence.Add(1)
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		if reply.toolName != "" {
			arguments, marshalErr := json.Marshal(reply.args)
			if marshalErr != nil {
				return
			}
			provider.writeEvent(response, map[string]any{"id": fmt.Sprintf("native-root-%d", sequence), "model": "gpt-5.6-terra", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("native-root-call-%d", sequence), "type": "function", "function": map[string]any{"name": reply.toolName, "arguments": string(arguments)}}}}, "finish_reason": nil}}})
			provider.writeEvent(response, map[string]any{"id": fmt.Sprintf("native-root-%d", sequence), "model": "gpt-5.6-terra", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		} else {
			provider.writeEvent(response, map[string]any{"id": fmt.Sprintf("native-root-%d", sequence), "model": "gpt-5.6-terra", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply.content}, "finish_reason": nil}}})
			provider.writeEvent(response, map[string]any{"id": fmt.Sprintf("native-root-%d", sequence), "model": "gpt-5.6-terra", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
		}
		provider.writeLine(response, "data: [DONE]\n\n")
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *nativeRootControlProvider) writeEvent(response http.ResponseWriter, value map[string]any) {
	encoded, _ := json.Marshal(value)
	p.writeLine(response, "data: "+string(encoded)+"\n\n")
}

func (p *nativeRootControlProvider) writeLine(response http.ResponseWriter, value string) {
	_, _ = io.WriteString(response, value)
	if flusher, ok := response.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (p *nativeRootControlProvider) nextRequest(ctx context.Context) map[string]any {
	select {
	case request := <-p.requests:
		return request
	case <-ctx.Done():
		return nil
	}
}

func (p *nativeRootControlProvider) reply(reply nativeRootControlProviderReply) {
	p.replies <- reply
}

func writeNativeRootControlModels(t *testing.T, home, baseURL string) {
	t.Helper()
	path := filepath.Join(home, ".pi", "agent")
	require.NoError(t, os.MkdirAll(path, 0o700))
	models := map[string]any{"providers": map[string]any{"openai-codex": map[string]any{
		"baseUrl": baseURL + "/v1", "api": "openai-completions", "apiKey": "deterministic-root-control-test-key",
		"models": []any{map[string]any{"id": "gpt-5.6-terra", "name": "Deterministic root control model", "reasoning": false, "input": []string{"text"}, "cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}, "contextWindow": 16_384, "maxTokens": 1_024}},
	}}}
	encoded, err := json.Marshal(models)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(path, "models.json"), encoded, 0o600))
}

type nativeRootActivityEvent struct {
	state    string
	sequence int64
}

type nativeRootActivityRPC struct {
	managed.RPC
	events chan<- nativeRootActivityEvent
}

func (r *nativeRootActivityRPC) SetParentActivityEventHandler(handler func(map[string]any)) {
	source, ok := r.RPC.(managed.ParentActivityRPC)
	if !ok {
		return
	}
	source.SetParentActivityEventHandler(func(event map[string]any) {
		state, _ := event["state"].(string)
		var sequence int64
		switch value := event["sequence"].(type) {
		case int64:
			sequence = value
		case float64:
			sequence = int64(value)
		}
		observed := nativeRootActivityEvent{state: state, sequence: sequence}
		if r.events != nil {
			select {
			case r.events <- observed:
			default:
			}
		}
		handler(event)
	})
}

type nativeRootReadinessRPC interface {
	ApplyPolicy(context.Context, map[string]any) (map[string]any, error)
	WaitForPolicyApplied(context.Context, string, int64) (map[string]any, error)
	RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error)
	WaitForChildSnapshot(context.Context) (map[string]any, error)
}

func (r *nativeRootActivityRPC) readiness() (nativeRootReadinessRPC, error) {
	source, ok := r.RPC.(nativeRootReadinessRPC)
	if !ok {
		return nil, errors.New("installed Pi RPC does not expose readiness-source methods")
	}
	return source, nil
}
func (r *nativeRootActivityRPC) ApplyPolicy(ctx context.Context, value map[string]any) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.ApplyPolicy(ctx, value)
}
func (r *nativeRootActivityRPC) WaitForPolicyApplied(ctx context.Context, generation string, epoch int64) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.WaitForPolicyApplied(ctx, generation, epoch)
}
func (r *nativeRootActivityRPC) RequestChildSnapshot(ctx context.Context, value map[string]any) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.RequestChildSnapshot(ctx, value)
}
func (r *nativeRootActivityRPC) WaitForChildSnapshot(ctx context.Context) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.WaitForChildSnapshot(ctx)
}
func (r *nativeRootActivityRPC) SetRootControlEventHandler(handler func(managed.RootControlEvent)) {
	r.RPC.(managed.RootControlRPC).SetRootControlEventHandler(handler)
}
func (r *nativeRootActivityRPC) ArmRootControlResult(requestID, entryID string) (managed.RootControlAppendToken, error) {
	return r.RPC.(managed.RootControlRPC).ArmRootControlResult(requestID, entryID)
}
func (r *nativeRootActivityRPC) SubmitRootControlResult(ctx context.Context, token managed.RootControlAppendToken, payload map[string]any) (<-chan managed.RootControlResultReceipt, error) {
	return r.RPC.(managed.RootControlRPC).SubmitRootControlResult(ctx, token, payload)
}
func (r *nativeRootActivityRPC) GetRootControlEntry(ctx context.Context, entryID string) (map[string]any, error) {
	return r.RPC.(managed.RootControlRPC).GetRootControlEntry(ctx, entryID)
}
func (r *nativeRootActivityRPC) SettlePreparedRestartTurn(ctx context.Context, fence store.ManagedRuntimeFence) error {
	return r.RPC.(managed.RestartTurnSettler).SettlePreparedRestartTurn(ctx, fence)
}

type nativeRootRPCFactory struct {
	base   managed.RPCFactory
	events chan<- nativeRootActivityEvent
	mu     sync.Mutex
	latest *nativeRootActivityRPC
}

func (f *nativeRootRPCFactory) Open(ctx context.Context, process managed.Process) (managed.RPC, error) {
	rpc, err := f.base.Open(ctx, process)
	if err != nil {
		return nil, err
	}
	wrapped := &nativeRootActivityRPC{RPC: rpc, events: f.events}
	f.mu.Lock()
	f.latest = wrapped
	f.mu.Unlock()
	return wrapped, nil
}

func (f *nativeRootRPCFactory) latestRPC(t *testing.T) *nativeRootActivityRPC {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotNil(t, f.latest)
	return f.latest
}

func rootControlRowsForNativeTest(t *testing.T, ctx context.Context, durable *store.Store, databasePath string) []store.ManagedControlRequest {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT request_id FROM managed_control_requests WHERE agent_id=? ORDER BY received_at,request_id`, "native-root-control")
	require.NoError(t, err)
	defer rows.Close()
	var result []store.ManagedControlRequest
	for rows.Next() {
		var requestID string
		require.NoError(t, rows.Scan(&requestID))
		row, readErr := durable.ManagedControlRequest(ctx, requestID)
		require.NoError(t, readErr)
		result = append(result, *row)
	}
	require.NoError(t, rows.Err())
	return result
}

func nativeRootToolNames(t *testing.T, request map[string]any) []string {
	t.Helper()
	raw, ok := request["tools"].([]any)
	require.True(t, ok, "provider request must include tools")
	result := make([]string, 0, len(raw))
	for _, value := range raw {
		tool, ok := value.(map[string]any)
		require.True(t, ok)
		function, ok := tool["function"].(map[string]any)
		require.True(t, ok)
		name, ok := function["name"].(string)
		require.True(t, ok)
		result = append(result, name)
	}
	return result
}

func requireNativeQueuedToolResult(t *testing.T, continuation map[string]any, toolName string) string {
	t.Helper()
	messages := fmt.Sprint(continuation["messages"])
	prefix := "Queued router operation " + toolName + " ("
	suffix := "). The result will arrive in a later control message."
	start := strings.LastIndex(messages, prefix)
	require.GreaterOrEqual(t, start, 0, "provider continuation must include the authoritative queued-operation acknowledgement")
	requestStart := start + len(prefix)
	relativeEnd := strings.Index(messages[requestStart:], suffix)
	require.GreaterOrEqual(t, relativeEnd, 0, "queued-operation acknowledgement must use the authoritative bounded text")
	requestID := messages[requestStart : requestStart+relativeEnd]
	require.Truef(t, canonicalUUID(requestID), "queued-operation acknowledgement must expose a canonical request ID, got %q", requestID)
	return requestID
}

func waitNativeRootSettled(ctx context.Context, events <-chan nativeRootActivityEvent, after int64) nativeRootActivityEvent {
	for {
		select {
		case event := <-events:
			if event.state == "idle" && event.sequence > after {
				return event
			}
		case <-ctx.Done():
			panic(fmt.Sprintf("installed Pi did not emit normalized agent_settled: %v", ctx.Err()))
		}
	}
}

func nativeRootControlHandlers(t *testing.T, manifest managed.Manifest, executions *atomic.Int32) []managed.RootControlHandler {
	t.Helper()
	handlers, err := managed.NewOperationalRootControlHandlers(manifest, managed.RootControlOperationExecutorFunc{
		ExecuteFunc: func(context.Context, managed.RootControlHandlerIdentity, managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
			executions.Add(1)
			return managed.RootControlHandlerResult{Status: "ok", Content: "unexpected executor effect", Details: map[string]any{}}, nil
		},
		ReconcileFunc: func(context.Context, managed.RootControlHandlerIdentity, managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
			executions.Add(1)
			return managed.RootControlHandlerResult{Status: "ok", Content: "unexpected reconcile effect", Details: map[string]any{}}, nil
		},
	})
	require.NoError(t, err)
	return handlers
}

func TestManagedRootControlInstalledPi(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	lockedPi := requireManagedResetLockedPi(t, root)
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	expectedTools := expectedManagedActiveToolsForTransport(manifest)
	provider := newNativeRootControlProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	home := t.TempDir()
	sessionRoot := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	writeNativeRootControlModels(t, home, provider.server.URL)
	events := make(chan nativeRootActivityEvent, 32)
	rpcFactory := &nativeRootRPCFactory{base: managed.DefaultRPCFactory(), events: events}
	factory := &nativeBlockingProcessFactory{base: managed.DefaultProcessFactory(), entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-factory.release:
		default:
			close(factory.release)
		}
	})
	var handlerExecutions atomic.Int32
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable, Manifest: manifest, ProcessFactory: factory, RPCFactory: rpcFactory,
		RootControlHandlers: nativeRootControlHandlers(t, manifest, &handlerExecutions),
		Config:              managed.Config{Enabled: true, PiExecutable: lockedPi.executable, ManagedHome: home, PiAgentDir: filepath.Join(home, ".pi", "agent"), SessionRoot: sessionRoot, RouterURL: "http://127.0.0.1:18775", ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts"), PiSubagentsExtension: lockedPi.subagentsEntry, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
		CatalogSource:       managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
	})
	require.NoError(t, err)
	defer manager.Close(context.Background())
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer router.Close(context.Background())
	routerURL, _ := router.URLs()
	require.NoError(t, manager.SetRouterURL(routerURL))
	_, err = manager.Launch(ctx, managed.LaunchRequest{Name: "native-root-control", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174151"})
	require.NoError(t, err)
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal("installed Pi did not reach the readiness seed boundary")
	}
	require.NoError(t, seedNativeReadinessPolicy(ctx, databasePath, "native-root-control"))
	close(factory.release)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "native-root-control")
		if readErr != nil {
			t.Logf("readiness state read failed: %v", readErr)
			return false
		}
		t.Logf("readiness state: lifecycle=%s activity=%s accepting=%t session=%s error=%s:%s", agent.LifecycleState, agent.ActivityState, agent.AcceptingPrompts, agent.PiSessionID, agent.LastErrorCode, agent.LastErrorMessage)
		return agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 25*time.Second, 100*time.Millisecond)

	var lastSequence int64
	runTurn := func(prompt string, final string) string {
		promptDone := make(chan error, 1)
		go func() { promptDone <- manager.AdmitPrompt(ctx, "native-root-control", prompt) }()
		initial := provider.nextRequest(ctx)
		require.NotNil(t, initial, "installed Pi did not make the deterministic provider request")
		require.ElementsMatch(t, expectedTools, nativeRootToolNames(t, initial))
		provider.reply(nativeRootControlProviderReply{toolName: "goal", args: map[string]any{"action": "status"}})
		continuation := provider.nextRequest(ctx)
		require.NotNil(t, continuation, "installed Pi did not request the tool-result continuation")
		require.ElementsMatch(t, expectedTools, nativeRootToolNames(t, continuation))
		requestID := requireNativeQueuedToolResult(t, continuation, "goal")
		provider.reply(nativeRootControlProviderReply{content: final})
		require.NoError(t, <-promptDone)
		event := waitNativeRootSettled(ctx, events, lastSequence)
		lastSequence = event.sequence
		return requestID
	}

	primaryRequestID := runTurn("Use the real root goal operation.", "The root goal operation was queued.")
	var rows []store.ManagedControlRequest
	require.Eventually(t, func() bool {
		rows = rootControlRowsForNativeTest(t, ctx, durable, databasePath)
		return len(rows) == 1 && rows[0].RequestID == primaryRequestID && rows[0].State == "delivered"
	}, 15*time.Second, 50*time.Millisecond)
	require.Len(t, rows, 1)
	require.Equal(t, primaryRequestID, rows[0].RequestID)
	require.Equal(t, "delivered", rows[0].State)
	primary := rows[0]
	require.NotEmpty(t, primary.ResultEntryID)
	require.Zero(t, handlerExecutions.Load(), "manager-owned goal status must not invoke the external handler executor")

	// Hold the second ordinary provider continuation. Reset is admitted against
	// the active source turn, cancels its old-fence row, then waits for this same
	// normal Pi turn to emit its native settled boundary.
	promptDone := make(chan error, 1)
	go func() {
		promptDone <- manager.AdmitPrompt(ctx, "native-root-control", "Run the root goal operation during reset.")
	}()
	initial := provider.nextRequest(ctx)
	require.NotNil(t, initial)
	require.ElementsMatch(t, expectedTools, nativeRootToolNames(t, initial))
	provider.reply(nativeRootControlProviderReply{toolName: "goal", args: map[string]any{"action": "status"}})
	continuation := provider.nextRequest(ctx)
	require.NotNil(t, continuation)
	require.ElementsMatch(t, expectedTools, nativeRootToolNames(t, continuation))
	pendingRequestID := requireNativeQueuedToolResult(t, continuation, "goal")
	var resetRow store.ManagedControlRequest
	require.Eventually(t, func() bool {
		rows = rootControlRowsForNativeTest(t, ctx, durable, databasePath)
		if len(rows) != 2 {
			return false
		}
		resetRow = rows[1]
		return resetRow.State != "delivered" && resetRow.State != "failed" && resetRow.State != "cancelled"
	}, 15*time.Second, 50*time.Millisecond)
	require.Equal(t, pendingRequestID, resetRow.RequestID)
	require.Equal(t, primary.Fence, resetRow.Fence)
	beforeReset, err := durable.ManagedAgent(ctx, "native-root-control")
	require.NoError(t, err)
	reset, err := manager.Reset(ctx, managed.ResetRequest{AgentID: "native-root-control", Method: http.MethodPost, CanonicalPath: "/v1/managed-agents/native-root-control/reset", RequestID: "123e4567-e89b-42d3-a456-426614174163", CanonicalBodyHash: strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.False(t, reset.Replayed)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, reset.Operation.OperationID)
		return actionErr == nil && action.State == "awaiting_settle" && action.Stage == "source_settle"
	}, 10*time.Second, 50*time.Millisecond)
	provider.reply(nativeRootControlProviderReply{content: "The root goal operation completed."})
	require.NoError(t, <-promptDone)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, reset.Operation.OperationID)
		agent, agentErr := durable.ManagedAgent(ctx, "native-root-control")
		return actionErr == nil && agentErr == nil && action.State == "succeeded" && agent.LifecycleState == "running" && agent.AcceptingPrompts && agent.SessionEpoch == beforeReset.SessionEpoch+1
	}, 30*time.Second, 100*time.Millisecond)

	cancelled, err := durable.ManagedControlRequest(ctx, resetRow.RequestID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.State)
	require.Equal(t, "CANCELLED_BY_RESET", cancelled.ErrorCode)
	rows = rootControlRowsForNativeTest(t, ctx, durable, databasePath)
	require.Len(t, rows, 2, "reset must not append a second durable row for the old request")
	require.Equal(t, primary.ResultEntryID, rows[0].ResultEntryID)
	require.NotEqual(t, rows[0].ResultEntryID, rows[1].ResultEntryID)
	require.Zero(t, handlerExecutions.Load(), "reset recovery must not replay the manager-owned goal handler")
	replacement := rpcFactory.latestRPC(t)
	require.NotSame(t, replacement, nil)
	oldEntry, err := replacement.GetRootControlEntry(ctx, "mcr1_"+strings.ReplaceAll(resetRow.ResultID, "-", ""))
	require.NoError(t, err)
	require.Nil(t, oldEntry, "the replacement session must not receive the cancelled old-fence result")
	require.GreaterOrEqual(t, provider.sequence.Load(), int64(4), "the provider mock must cover both normal agent turns")

	// Keep the artifact bound to the exact checked-out candidate and the locked
	// installed Pi. It records only immutable identities and observed booleans.
	candidateHash := sha256.New()
	for _, relative := range managedRootControlCandidatePaths {
		contents, readErr := os.ReadFile(filepath.Join(root, relative))
		if os.IsNotExist(readErr) {
			_, writeErr := fmt.Fprintf(candidateHash, "%s\x00deleted\n", relative)
			require.NoError(t, writeErr)
			continue
		}
		require.NoError(t, readErr)
		sum := sha256.Sum256(contents)
		_, writeErr := fmt.Fprintf(candidateHash, "%s\x00%s\n", relative, hex.EncodeToString(sum[:]))
		require.NoError(t, writeErr)
	}
	artifact := map[string]any{"protocol": "mecha.phase2.root-control-s4/v2", "result": "pass", "mecha_base_head": commandOutput(t, root, "git", "rev-parse", "HEAD"), "mecha_base_tree": commandOutput(t, root, "git", "rev-parse", "HEAD^{tree}"), "candidate_content_sha256": hex.EncodeToString(candidateHash.Sum(nil)), "candidate_paths": managedRootControlCandidatePaths, "locked_pi": map[string]any{"executable_sha256": lockedPi.executableSHA256, "source_commit": lockedPi.sourceCommit, "source_tree": lockedPi.sourceTree, "pi_packages_commit": lockedPi.piPackagesCommit, "pi_packages_tree": lockedPi.piPackagesTree}, "assertions": map[string]bool{"provider_mock_used": true, "normal_turn_settled": lastSequence > 0, "queued_root_result": true, "old_fence_cancelled": cancelled.State == "cancelled", "handler_effects_zero": handlerExecutions.Load() == 0, "entry_unique": rows[0].ResultEntryID != rows[1].ResultEntryID, "replacement_old_entry_absent": oldEntry == nil}}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	artifactPath := filepath.Join("/tmp", "mecha-rpc-acceptance", commandOutput(t, root, "git", "rev-parse", "HEAD"), "phase2-root-control-s4", "root-control-s4-v2.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(artifactPath), 0o700))
	require.NoError(t, os.WriteFile(artifactPath, append(encoded, '\n'), 0o600))
	fmt.Printf("MECHA_ROOT_CONTROL_S4_ARTIFACT=%s\n", artifactPath)
}
