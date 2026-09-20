package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const nativeGoalSeedPolicy = `{"compaction":{"deferred":false},"goal_lock":{"enabled":false},"inline":{"credits":{"bash":2},"enabled":true},"recall":{"content":"readiness seed"},"settled":{"enabled":true}}`

func seedNativeGoalReadinessPolicy(ctx context.Context, path, agentID string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(nativeGoalSeedPolicy))
	result, err := db.ExecContext(ctx, `UPDATE managed_readiness_sources SET desired_policy_canonical=?, desired_policy_sha256=? WHERE agent_id=? AND pi_session_id IS NULL AND applied_policy_canonical IS NULL AND snapshot_id IS NULL`, []byte(nativeGoalSeedPolicy), fmt.Sprintf("%x", sum), agentID)
	if err != nil {
		_, _ = db.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		_, _ = db.ExecContext(context.Background(), "ROLLBACK")
		if err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	_, err = db.ExecContext(ctx, "COMMIT")
	return err
}

type nativeGoalProviderReply struct {
	toolName string
	args     map[string]any
	content  string
	release  <-chan struct{}
}

type nativeGoalProvider struct {
	requests  chan map[string]any
	replies   chan nativeGoalProviderReply
	server    *httptest.Server
	onSettled func()
}

type nativeGoalActivityEvent struct {
	state    string
	sequence int64
}

type nativeGoalActivityBridge struct {
	managed.RPC
	settled chan nativeGoalActivityEvent
	states  chan nativeGoalActivityEvent
	trace   chan string

	mu                    sync.Mutex
	managerRegistrations  int
	sourceRegistrations   int
	managerHandlerPointer uintptr
	managerCallbackCalls  int
	managerHandler        func(map[string]any)
}

// SetParentActivityEventHandler is the only registration seam Manager sees.
// The source callback is installed exactly once, at the moment Manager gives us
// its production handler; the callback tees bounded observation into the test
// and then invokes that same production handler. No later setter call can shadow
// the manager receiver.
func (r *nativeGoalActivityBridge) SetParentActivityEventHandler(handler func(map[string]any)) {
	r.mu.Lock()
	r.managerRegistrations++
	r.managerHandlerPointer = reflect.ValueOf(handler).Pointer()
	r.managerHandler = handler
	r.mu.Unlock()
}

func (r *nativeGoalActivityBridge) teeParentActivityEvent(event map[string]any) {
	state, _ := event["state"].(string)
	sequence, _ := event["sequence"].(int64)
	select {
	case r.trace <- fmt.Sprintf("activity:event state=%s sequence=%d type=%T", state, sequence, event["sequence"]):
	default:
	}
	observed := nativeGoalActivityEvent{state: state, sequence: sequence}
	select {
	case r.states <- observed:
	default:
	}
	if state == "idle" {
		select {
		case r.settled <- observed:
		default:
		}
	}
	r.mu.Lock()
	handler := r.managerHandler
	if handler != nil {
		r.managerCallbackCalls++
	}
	r.mu.Unlock()
	if handler != nil {
		handler(event)
	}
}

func (r *nativeGoalActivityBridge) registrationSnapshot() (manager, source, calls int, handler uintptr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.managerRegistrations, r.sourceRegistrations, r.managerCallbackCalls, r.managerHandlerPointer
}

func (r *nativeGoalActivityBridge) traceContext(step string, ctx context.Context) {
	deadline, hasDeadline := ctx.Deadline()
	value := fmt.Sprintf("%s deadline=%s has_deadline=%t err=%v", step, deadline.UTC().Format(time.RFC3339Nano), hasDeadline, ctx.Err())
	select {
	case r.trace <- value:
	default:
	}
}

func (r *nativeGoalActivityBridge) ApplyPolicy(ctx context.Context, value map[string]any) (map[string]any, error) {
	r.traceContext("rpc:apply:start", ctx)
	result, err := r.RPC.(interface {
		ApplyPolicy(context.Context, map[string]any) (map[string]any, error)
	}).ApplyPolicy(ctx, value)
	r.traceContext(fmt.Sprintf("rpc:apply:end err=%v", err), ctx)
	return result, err
}
func (r *nativeGoalActivityBridge) WaitForPolicyApplied(ctx context.Context, generation string, epoch int64) (map[string]any, error) {
	r.traceContext("rpc:receipt:start", ctx)
	result, err := r.RPC.(interface {
		WaitForPolicyApplied(context.Context, string, int64) (map[string]any, error)
	}).WaitForPolicyApplied(ctx, generation, epoch)
	r.traceContext(fmt.Sprintf("rpc:receipt:end err=%v", err), ctx)
	return result, err
}
func (r *nativeGoalActivityBridge) RequestChildSnapshot(ctx context.Context, value map[string]any) (map[string]any, error) {
	return r.RPC.(interface {
		RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error)
	}).RequestChildSnapshot(ctx, value)
}
func (r *nativeGoalActivityBridge) WaitForChildSnapshot(ctx context.Context) (map[string]any, error) {
	return r.RPC.(interface {
		WaitForChildSnapshot(context.Context) (map[string]any, error)
	}).WaitForChildSnapshot(ctx)
}
func (r *nativeGoalActivityBridge) StartManagedGoalEvaluation(ctx context.Context, fence store.ManagedRuntimeFence, policy store.ManagedGoalPolicy) error {
	r.traceContext("rpc:evaluate:start", ctx)
	err := r.RPC.(managed.ManagedGoalEvaluationRPC).StartManagedGoalEvaluation(ctx, fence, policy)
	r.traceContext(fmt.Sprintf("rpc:evaluate:end err=%v", err), ctx)
	return err
}
func (r *nativeGoalActivityBridge) WaitForManagedGoalEvaluation(ctx context.Context) (string, string, error) {
	r.traceContext("rpc:evaluate-receipt:start", ctx)
	outcome, reason, err := r.RPC.(managed.ManagedGoalEvaluationRPC).WaitForManagedGoalEvaluation(ctx)
	r.traceContext(fmt.Sprintf("rpc:evaluate-receipt:end outcome=%s reason=%q err=%v", outcome, reason, err), ctx)
	return outcome, reason, err
}
func (r *nativeGoalActivityBridge) SetRootControlEventHandler(handler func(managed.RootControlEvent)) {
	r.RPC.(managed.RootControlRPC).SetRootControlEventHandler(handler)
}
func (r *nativeGoalActivityBridge) ArmRootControlResult(requestID, entryID string) (managed.RootControlAppendToken, error) {
	return r.RPC.(managed.RootControlRPC).ArmRootControlResult(requestID, entryID)
}
func (r *nativeGoalActivityBridge) SubmitRootControlResult(ctx context.Context, token managed.RootControlAppendToken, payload map[string]any) (<-chan managed.RootControlResultReceipt, error) {
	return r.RPC.(managed.RootControlRPC).SubmitRootControlResult(ctx, token, payload)
}
func (r *nativeGoalActivityBridge) GetRootControlEntry(ctx context.Context, entryID string) (map[string]any, error) {
	return r.RPC.(managed.RootControlRPC).GetRootControlEntry(ctx, entryID)
}
func (r *nativeGoalActivityBridge) PrepareManagedMailbox(ctx context.Context) (string, error) {
	return r.RPC.(managed.DirectMailboxRPC).PrepareManagedMailbox(ctx)
}
func (r *nativeGoalActivityBridge) PromptManagedMailbox(ctx context.Context, attempt store.ManagedDirectAttempt) (<-chan managed.DirectMailboxEvent, error) {
	return r.RPC.(managed.DirectMailboxRPC).PromptManagedMailbox(ctx, attempt)
}
func (r *nativeGoalActivityBridge) RecoverManagedMailbox(ctx context.Context, attempt store.ManagedDirectAttempt) (managed.DirectMailboxRecovery, error) {
	return r.RPC.(managed.DirectMailboxRPC).RecoverManagedMailbox(ctx, attempt)
}

type nativeGoalRPCFactory struct {
	base   managed.RPCFactory
	mu     sync.Mutex
	latest *nativeGoalActivityBridge
}

func (f *nativeGoalRPCFactory) Open(ctx context.Context, process managed.Process) (managed.RPC, error) {
	rpc, err := f.base.Open(ctx, process)
	if err != nil {
		return nil, err
	}
	bridge := &nativeGoalActivityBridge{RPC: rpc, settled: make(chan nativeGoalActivityEvent, 8), states: make(chan nativeGoalActivityEvent, 16), trace: make(chan string, 64)}
	source, ok := rpc.(interface{ SetParentActivityEventHandler(func(map[string]any)) })
	if !ok {
		return nil, fmt.Errorf("native goal RPC has no parent activity source")
	}
	bridge.mu.Lock()
	bridge.sourceRegistrations++
	bridge.mu.Unlock()
	source.SetParentActivityEventHandler(bridge.teeParentActivityEvent)
	f.mu.Lock()
	f.latest = bridge
	f.mu.Unlock()
	return bridge, nil
}

func (f *nativeGoalRPCFactory) discardSettled() {
	f.mu.Lock()
	bridge := f.latest
	f.mu.Unlock()
	if bridge == nil {
		return
	}
	for {
		select {
		case <-bridge.settled:
		default:
			return
		}
	}
}

func (f *nativeGoalRPCFactory) waitSettled(ctx context.Context) (nativeGoalActivityEvent, error) {
	f.mu.Lock()
	bridge := f.latest
	f.mu.Unlock()
	if bridge == nil {
		return nativeGoalActivityEvent{}, fmt.Errorf("native goal RPC was not opened")
	}
	select {
	case event := <-bridge.settled:
		return event, nil
	case <-ctx.Done():
		states := make([]nativeGoalActivityEvent, 0, len(bridge.states))
		for {
			select {
			case state := <-bridge.states:
				states = append(states, state)
			default:
				return nativeGoalActivityEvent{}, fmt.Errorf("%w (observed events: %v)", ctx.Err(), states)
			}
		}
	}
}

func newNativeGoalProvider(t *testing.T) *nativeGoalProvider {
	t.Helper()
	provider := &nativeGoalProvider{
		requests: make(chan map[string]any, 8),
		replies:  make(chan nativeGoalProviderReply, 8),
	}
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
		if json.Unmarshal(body, &parsed) != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case provider.requests <- parsed:
		case <-request.Context().Done():
			return
		}
		var reply nativeGoalProviderReply
		select {
		case reply = <-provider.replies:
		case <-request.Context().Done():
			return
		}
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		if reply.toolName != "" {
			arguments, marshalErr := json.Marshal(reply.args)
			if marshalErr != nil {
				return
			}
			provider.writeEvent(response, map[string]any{"id": "native-goal", "model": "gpt-5.6-terra", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "native-goal-call", "type": "function", "function": map[string]any{"name": reply.toolName, "arguments": string(arguments)}}}}, "finish_reason": nil}}})
			provider.writeEvent(response, map[string]any{"id": "native-goal", "model": "gpt-5.6-terra", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		} else {
			if reply.release != nil {
				select {
				case <-reply.release:
				case <-request.Context().Done():
					return
				}
			}
			provider.writeEvent(response, map[string]any{"id": "native-goal", "model": "gpt-5.6-terra", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply.content}, "finish_reason": nil}}})
			provider.writeEvent(response, map[string]any{"id": "native-goal", "model": "gpt-5.6-terra", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
		}
		provider.writeLine(response, "data: [DONE]\n\n")
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *nativeGoalProvider) writeEvent(response http.ResponseWriter, value map[string]any) {
	encoded, _ := json.Marshal(value)
	p.writeLine(response, "data: "+string(encoded)+"\n\n")
}

func (p *nativeGoalProvider) writeLine(response http.ResponseWriter, value string) {
	_, _ = io.WriteString(response, value)
	if flusher, ok := response.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (p *nativeGoalProvider) nextRequest(ctx context.Context) map[string]any {
	select {
	case request := <-p.requests:
		return request
	case <-ctx.Done():
		return nil
	}
}

func requireNoNativeGoalRequest(t *testing.T, provider *nativeGoalProvider, duration time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	require.Nil(t, provider.nextRequest(ctx), "native Pi made an unexpected additional provider request")
}

func (p *nativeGoalProvider) reply(reply nativeGoalProviderReply) {
	p.replies <- reply
}

func writeNativeGoalModels(t *testing.T, home, baseURL string) {
	t.Helper()
	path := filepath.Join(home, ".pi", "agent")
	require.NoError(t, os.MkdirAll(path, 0o700))
	models := map[string]any{"providers": map[string]any{"openai-codex": map[string]any{
		"baseUrl": baseURL + "/v1", "api": "openai-completions", "apiKey": "deterministic-goal-test-key",
		"models": []any{map[string]any{"id": "gpt-5.6-terra", "name": "Deterministic goal model", "reasoning": false, "input": []string{"text"}, "cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}, "contextWindow": 16_384, "maxTokens": 1_024}},
	}}}
	encoded, err := json.Marshal(models)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(path, "models.json"), encoded, 0o600))
}

func nativeGoalToolNames(t *testing.T, request map[string]any) []string {
	t.Helper()
	raw, ok := request["tools"].([]any)
	require.True(t, ok, "provider request must include a tools array")
	names := make([]string, 0, len(raw))
	for _, value := range raw {
		tool, ok := value.(map[string]any)
		require.True(t, ok)
		function, ok := tool["function"].(map[string]any)
		require.True(t, ok)
		name, ok := function["name"].(string)
		require.True(t, ok)
		names = append(names, name)
	}
	return names
}

func waitNativeGoalReady(ctx context.Context, durable *store.Store, agentID string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		agent, err := durable.ManagedAgent(ctx, agentID)
		if err == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			diagnostic, diagnosticErr := durable.ManagedAgent(context.Background(), agentID)
			if diagnosticErr != nil {
				return fmt.Errorf("managed Pi did not become ready: %w (agent lookup: %v)", ctx.Err(), diagnosticErr)
			}
			return fmt.Errorf("managed Pi did not become ready: %w (lifecycle=%s activity=%s accepting_prompts=%t error_code=%s error_message=%s)", ctx.Err(), diagnostic.LifecycleState, diagnostic.ActivityState, diagnostic.AcceptingPrompts, diagnostic.LastErrorCode, diagnostic.LastErrorMessage)
		}
	}
}

func nativeGoalCount(t *testing.T, path, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	var count int
	require.NoError(t, db.QueryRow(query, args...).Scan(&count))
	return count
}

func nativeGoalDirectAttemptStates(t *testing.T, path, agentID string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT message_id || ":" || attempt_number || ":" || state FROM managed_direct_delivery_attempts WHERE agent_id=? ORDER BY rowid`, agentID)
	require.NoError(t, err)
	defer rows.Close()
	var states []string
	for rows.Next() {
		var state string
		require.NoError(t, rows.Scan(&state))
		states = append(states, state)
	}
	require.NoError(t, rows.Err())
	return states
}

func installNativeGoalDirectAttemptTrace(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE native_goal_direct_attempt_trace (
			trace_id INTEGER PRIMARY KEY AUTOINCREMENT,
			event TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			attempt_number INTEGER NOT NULL,
			state TEXT NOT NULL
		);
		CREATE TRIGGER native_goal_direct_attempt_insert
		AFTER INSERT ON managed_direct_delivery_attempts
		BEGIN
			INSERT INTO native_goal_direct_attempt_trace(event,agent_id,message_id,attempt_number,state)
			VALUES ('allocated',NEW.agent_id,NEW.message_id,NEW.attempt_number,NEW.state);
		END;
		CREATE TRIGGER native_goal_direct_attempt_state
		AFTER UPDATE OF state ON managed_direct_delivery_attempts
		WHEN OLD.state <> NEW.state
		BEGIN
			INSERT INTO native_goal_direct_attempt_trace(event,agent_id,message_id,attempt_number,state)
			VALUES ('state',NEW.agent_id,NEW.message_id,NEW.attempt_number,NEW.state);
		END;
		CREATE TRIGGER native_goal_direct_attempt_delete
		AFTER DELETE ON managed_direct_delivery_attempts
		BEGIN
			INSERT INTO native_goal_direct_attempt_trace(event,agent_id,message_id,attempt_number,state)
			VALUES ('removed',OLD.agent_id,OLD.message_id,OLD.attempt_number,OLD.state);
		END;
	`)
	require.NoError(t, err)
}

func nativeGoalDirectAttemptTrace(t *testing.T, path, agentID string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`SELECT event || ":" || attempt_number || ":" || state FROM native_goal_direct_attempt_trace WHERE agent_id=? ORDER BY trace_id`, agentID)
	require.NoError(t, err)
	defer rows.Close()
	var trace []string
	for rows.Next() {
		var event string
		require.NoError(t, rows.Scan(&event))
		trace = append(trace, event)
	}
	require.NoError(t, rows.Err())
	return trace
}

func waitNativeGoalPolicy(ctx context.Context, durable *store.Store, agentID string, ready func(*store.ManagedGoalPolicy) bool) (store.ManagedGoalPolicy, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		policy, err := durable.ManagedGoalPolicyFor(ctx, agentID)
		if err == nil && ready(policy) {
			return *policy, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			diagnostic, diagnosticErr := durable.ManagedGoalPolicyFor(context.Background(), agentID)
			if diagnosticErr != nil {
				return store.ManagedGoalPolicy{}, fmt.Errorf("managed goal policy did not reach the expected state: %w (policy lookup: %v)", ctx.Err(), diagnosticErr)
			}
			agent, agentErr := durable.ManagedAgent(context.Background(), agentID)
			return store.ManagedGoalPolicy{}, fmt.Errorf("managed goal policy did not reach the expected state: %w (evaluator_state=%s last_outcome=%s last_settled_sequence=%d evaluator_settled_sequence=%d evaluator_reason=%s last_error_code=%s last_error_message=%s agent_err=%v agent=%+v)", ctx.Err(), diagnostic.EvaluatorState, diagnostic.LastOutcome, diagnostic.LastSettledSequence, diagnostic.EvaluatorSettledSequence, diagnostic.EvaluatorReason, diagnostic.LastErrorCode, diagnostic.LastErrorMessage, agentErr, agent)
		}
	}
}

func TestManagedGoalEvaluatorInstalledPiCoreFunctionalSlice(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	lockedPi := requireManagedRootControlLockedPi(t, root)
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	expectedTools := expectedManagedActiveToolsForTransport(manifest)
	require.Len(t, manifest.Tools, 17, "revision 2 manifest count is derived from the checked-in manifest")
	for _, testCase := range []struct {
		name       string
		evaluator  string
		wantState  string
		wantResult string
		wantReason string
	}{
		{name: "NOT_DONE persists once and wakes once", evaluator: "NOT_DONE", wantState: "idle", wantResult: store.ManagedGoalEvaluationNotDone, wantReason: "More work remains."},
		{name: "DONE persists without a wakeup", evaluator: "DONE", wantState: "done", wantResult: store.ManagedGoalEvaluationCompleted, wantReason: "The goal is complete."},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			chronology := make([]string, 0, 5)
			defer cancel()
			provider := newNativeGoalProvider(t)
			databasePath := filepath.Join(t.TempDir(), "router.db")
			durable, err := store.Open(ctx, databasePath, store.Options{})
			require.NoError(t, err)
			defer durable.Close()
			installNativeGoalDirectAttemptTrace(t, databasePath)
			home := t.TempDir()
			sessionRoot := filepath.Join(home, "sessions")
			require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
			writeNativeGoalModels(t, home, provider.server.URL)
			factory := &nativeBlockingProcessFactory{base: managed.DefaultProcessFactory(), entered: make(chan struct{}), release: make(chan struct{})}
			rpcFactory := &nativeGoalRPCFactory{base: managed.DefaultRPCFactory()}
			t.Cleanup(func() {
				select {
				case <-factory.release:
				default:
					close(factory.release)
				}
			})
			manager, err := managed.NewManager(managed.ManagerOptions{
				Store: durable, Manifest: manifest, ProcessFactory: factory, RPCFactory: rpcFactory,
				Config:        managed.Config{Enabled: true, PiExecutable: lockedPi.executable, ManagedHome: home, PiAgentDir: filepath.Join(home, ".pi", "agent"), SessionRoot: sessionRoot, RouterURL: "http://127.0.0.1:18775", ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts"), PiSubagentsExtension: lockedPi.subagentsEntry, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
				CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
			})
			require.NoError(t, err)
			defer manager.Close(context.Background())
			router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
			require.NoError(t, err)
			require.NoError(t, router.Listen(ctx))
			defer router.Close(context.Background())
			httpURL, _ := router.URLs()
			require.NoError(t, manager.SetRouterURL(httpURL))
			launchBody, err := json.Marshal(map[string]string{"name": "native-goal", "cwd": root})
			require.NoError(t, err)
			launchRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/managed-agents", bytes.NewReader(launchBody))
			require.NoError(t, err)
			launchRequest.Header.Set("Content-Type", "application/json")
			launchRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174120")
			launchResponse, err := http.DefaultClient.Do(launchRequest)
			require.NoError(t, err)
			require.Equal(t, http.StatusAccepted, launchResponse.StatusCode)
			require.NoError(t, launchResponse.Body.Close())
			select {
			case <-factory.entered:
			case <-ctx.Done():
				t.Fatal("installed Pi did not reach the readiness barrier")
			}
			require.NoError(t, seedNativeGoalReadinessPolicy(ctx, databasePath, "native-goal"))
			close(factory.release)
			require.NoError(t, waitNativeGoalReady(ctx, durable, "native-goal"))
			rpcFactory.discardSettled()

			require.NoError(t, manager.AdmitPrompt(ctx, "native-goal", "Set the goal and continue."))
			initial := provider.nextRequest(ctx)
			if initial == nil {
				agent, _ := durable.ManagedAgent(ctx, "native-goal")
				t.Fatalf("provider request barrier timed out: agent=%+v", agent)
			}
			chronology = append(chronology, "goal tool turn")
			initialTools := nativeGoalToolNames(t, initial)
			require.ElementsMatch(t, expectedTools, initialTools)
			provider.reply(nativeGoalProviderReply{toolName: "goal", args: map[string]any{"action": "set", "goal": "finish the native goal slice"}})
			continuation := provider.nextRequest(ctx)
			if continuation == nil {
				agent, _ := durable.ManagedAgent(ctx, "native-goal")
				t.Fatalf("continuation provider barrier timed out: agent=%+v", agent)
			}
			chronology = append(chronology, "root-control continuation")
			require.Equal(t, initialTools, nativeGoalToolNames(t, continuation))
			require.Contains(t, fmt.Sprint(continuation["messages"]), "Set the goal and continue.")
			releaseContinuation := make(chan struct{})
			provider.reply(nativeGoalProviderReply{content: "Goal control accepted.", release: releaseContinuation})
			// Let the continuation settle before waiting for the hidden policy
			// receipt. A provider response held open here would prevent Pi from
			// publishing that receipt and make the harness deadlock the test.
			close(releaseContinuation)
			_, err = waitNativeGoalPolicy(ctx, durable, "native-goal", func(value *store.ManagedGoalPolicy) bool {
				return value.Config.Enabled && value.Config.Goal != "" && value.ProjectionState == "applied"
			})
			if err != nil {
				rpcFactory.mu.Lock()
				bridge := rpcFactory.latest
				rpcFactory.mu.Unlock()
				if bridge != nil {
					for {
						select {
						case line := <-bridge.trace:
							t.Log(line)
						default:
							goto traceDone
						}
					}
				}
			traceDone:
			}
			require.NoError(t, err)
			observed, err := rpcFactory.waitSettled(ctx)
			require.NoError(t, err, "installed Pi did not emit its native agent_settled event")
			require.Equal(t, "idle", observed.state)
			require.Greater(t, observed.sequence, int64(0), "normalized idle event must carry a positive sequence")
			managerRegistrations, sourceRegistrations, managerCallbackCalls, managerHandlerPointer := rpcFactory.latest.registrationSnapshot()
			require.Equal(t, 1, managerRegistrations, "Manager must register one production activity receiver")
			require.Equal(t, 1, sourceRegistrations, "the source must receive one tee receiver registration")
			require.GreaterOrEqual(t, managerCallbackCalls, 2, "the tee must forward every observed lifecycle boundary to production")
			require.NotZero(t, managerHandlerPointer, "production receiver identity must be captured")
			settledPolicy, err := waitNativeGoalPolicy(ctx, durable, "native-goal", func(value *store.ManagedGoalPolicy) bool {
				return value.LastSettledSequence >= observed.sequence
			})
			if err != nil {
				rpcFactory.mu.Lock()
				bridge := rpcFactory.latest
				rpcFactory.mu.Unlock()
				if bridge != nil {
					for {
						select {
						case line := <-bridge.trace:
							t.Log(line)
						default:
							goto settledTraceDone
						}
					}
				}
			settledTraceDone:
			}
			require.NoError(t, err)
			require.GreaterOrEqual(t, settledPolicy.LastSettledSequence, observed.sequence, "durable settled sequence must include the observed native event")

			evaluatorRequest := provider.nextRequest(ctx)
			if evaluatorRequest == nil {
				agent, _ := durable.ManagedAgent(context.Background(), "native-goal")
				goalPolicy, _ := durable.ManagedGoalPolicyFor(context.Background(), "native-goal")
				t.Fatalf("evaluator provider barrier timed out: agent=%+v policy=%+v", agent, goalPolicy)
			}
			chronology = append(chronology, "hidden evaluator #1")
			require.Empty(t, nativeGoalToolNames(t, evaluatorRequest), "the hidden evaluator request must advertise zero tools")
			require.Contains(t, fmt.Sprint(evaluatorRequest["messages"]), "finish the native goal slice")
			rootCountBefore := nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM managed_control_requests WHERE agent_id=?", "native-goal")
			require.Equal(t, 1, rootCountBefore)
			provider.reply(nativeGoalProviderReply{content: testCase.evaluator + "\nREASON: " + testCase.wantReason})

			policy, err := waitNativeGoalPolicy(ctx, durable, "native-goal", func(value *store.ManagedGoalPolicy) bool {
				return value.EvaluatorState == testCase.wantState && value.LastOutcome == testCase.wantResult
			})
			if err != nil {
				rpcFactory.mu.Lock()
				bridge := rpcFactory.latest
				rpcFactory.mu.Unlock()
				if bridge != nil {
					for {
						select {
						case line := <-bridge.trace:
							t.Log(line)
						default:
							goto evaluatorTraceDone
						}
					}
				}
			evaluatorTraceDone:
			}
			require.NoError(t, err)
			require.Equal(t, testCase.wantResult, policy.LastOutcome)
			require.Equal(t, testCase.wantReason, policy.EvaluatorReason)
			require.Equal(t, rootCountBefore, nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM managed_control_requests WHERE agent_id=?", "native-goal"), "the evaluator must have no root-control side effect")
			if testCase.evaluator == "NOT_DONE" {
				require.EqualValues(t, 1, policy.NotDoneWakeups)
				require.NotEmpty(t, policy.WakeupMessageID)
				require.Equal(t, 1, nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM managed_goal_wakeups WHERE agent_id=?", "native-goal"))
				wakeup := provider.nextRequest(ctx)
				require.NotNil(t, wakeup)
				wakeupTools := nativeGoalToolNames(t, wakeup)
				// The provider request is the native prompt observation. The durable
				// trigger preserves the short-lived attempt rows, including the case
				// where Pi appends its receipt before the provider response finishes.
				beforeReleaseTrace := nativeGoalDirectAttemptTrace(t, databasePath, "native-goal")
				t.Logf("NOT_DONE wakeup before provider release: tools=%v direct_trace=%v", wakeupTools, beforeReleaseTrace)
				require.Equal(t, 1, nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM native_goal_direct_attempt_trace WHERE agent_id=? AND event='allocated'", "native-goal"))
				require.Equal(t, 1, nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM native_goal_direct_attempt_trace WHERE agent_id=? AND event='state' AND state='direct_prompted'", "native-goal"))
				providerRelease := make(chan struct{})
				provider.reply(nativeGoalProviderReply{content: "Wakeup handled.", release: providerRelease})
				close(providerRelease)
				secondSettled, settleErr := rpcFactory.waitSettled(ctx)
				require.NoError(t, settleErr, "ordinary continuation did not settle")
				require.Greater(t, secondSettled.sequence, observed.sequence)
				policy, err = waitNativeGoalPolicy(ctx, durable, "native-goal", func(value *store.ManagedGoalPolicy) bool { return value.NotDoneWakeups == 1 })
				require.NoError(t, err)
				require.EqualValues(t, 1, policy.NotDoneWakeups)
				require.Eventually(t, func() bool {
					return nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM managed_direct_delivery_attempts WHERE agent_id=? AND state IN ('direct_pending','direct_prompted')", "native-goal") == 0
				}, time.Second, 10*time.Millisecond, "the acknowledged continuation must leave no active direct attempt")
				afterReleaseTrace := nativeGoalDirectAttemptTrace(t, databasePath, "native-goal")
				t.Logf("NOT_DONE wakeup after provider release and settlement: direct_trace=%v", afterReleaseTrace)
				require.Equal(t, 1, nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM native_goal_direct_attempt_trace WHERE agent_id=? AND event='state' AND state='acknowledged'", "native-goal"))
				require.Equal(t, 1, nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM native_goal_direct_attempt_trace WHERE agent_id=? AND event='removed'", "native-goal"))
				chronology = append(chronology, "ordinary continuation")
				require.Equal(t, initialTools, wakeupTools, "the one ordinary continuation must restore the exact pre-goal tools")
				require.Contains(t, fmt.Sprint(wakeup["messages"]), "finish the native goal slice")
				evaluatorTwo := provider.nextRequest(ctx)
				require.NotNil(t, evaluatorTwo)
				chronology = append(chronology, "hidden evaluator #2")
				require.Empty(t, nativeGoalToolNames(t, evaluatorTwo), "the second hidden evaluator request must advertise zero tools")
				require.Contains(t, fmt.Sprint(evaluatorTwo["messages"]), "finish the native goal slice")
				provider.reply(nativeGoalProviderReply{content: "DONE\nREASON: The goal is complete."})
				finalPolicy, finalErr := waitNativeGoalPolicy(ctx, durable, "native-goal", func(value *store.ManagedGoalPolicy) bool {
					return value.EvaluatorState == "done" && value.LastOutcome == store.ManagedGoalEvaluationCompleted && value.EvaluatorAttempts == 2
				})
				require.NoError(t, finalErr)
				require.EqualValues(t, 1, finalPolicy.NotDoneWakeups)
				require.Equal(t, 1, nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM managed_goal_wakeups WHERE agent_id=?", "native-goal"))
				requireNoNativeGoalRequest(t, provider, 250*time.Millisecond)
			} else {
				require.Zero(t, policy.NotDoneWakeups)
				require.Empty(t, policy.WakeupMessageID)
				require.Equal(t, 0, nativeGoalCount(t, databasePath, "SELECT COUNT(*) FROM managed_goal_wakeups WHERE agent_id=?", "native-goal"))
				require.EqualValues(t, 1, policy.EvaluatorAttempts)
				requireNoNativeGoalRequest(t, provider, 250*time.Millisecond)
			}
			wantChronology := []string{"goal tool turn", "root-control continuation", "hidden evaluator #1"}
			if testCase.evaluator == "NOT_DONE" {
				wantChronology = append(wantChronology, "ordinary continuation", "hidden evaluator #2")
			}
			require.Equal(t, wantChronology, chronology)
		})
	}
}
