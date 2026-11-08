package transport

import (
	"bytes"
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
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nativeInlineAllowanceProvider is deliberately a local, queued provider. The
// test controls every model turn and never depends on an external model API.
type nativeInlineAllowanceProvider struct {
	mu       sync.Mutex
	requests chan map[string]any
	replies  chan nativeInlineAllowanceReply
	sequence int
	server   *httptest.Server
}

type nativeInlineAllowanceReply struct {
	toolName string
	args     map[string]any
	content  string
}

func newNativeInlineAllowanceProvider(t *testing.T) *nativeInlineAllowanceProvider {
	t.Helper()
	provider := &nativeInlineAllowanceProvider{
		requests: make(chan map[string]any, 32),
		replies:  make(chan nativeInlineAllowanceReply, 32),
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
		if err := json.Unmarshal(body, &parsed); err != nil {
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case provider.requests <- parsed:
		case <-request.Context().Done():
			return
		}
		var reply nativeInlineAllowanceReply
		select {
		case reply = <-provider.replies:
		case <-request.Context().Done():
			return
		}
		provider.mu.Lock()
		provider.sequence++
		sequence := provider.sequence
		provider.mu.Unlock()
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		if reply.toolName != "" {
			arguments, marshalErr := json.Marshal(reply.args)
			if marshalErr != nil {
				return
			}
			provider.writeEvent(response, map[string]any{
				"id": fmt.Sprintf("native-inline-%d", sequence), "model": "gpt-5.6-terra",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
					"index": 0, "id": fmt.Sprintf("native-inline-call-%d", sequence), "type": "function",
					"function": map[string]any{"name": reply.toolName, "arguments": string(arguments)},
				}}}, "finish_reason": nil}},
			})
			provider.writeEvent(response, map[string]any{
				"id": fmt.Sprintf("native-inline-%d", sequence), "model": "gpt-5.6-terra",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
			})
		} else {
			provider.writeEvent(response, map[string]any{
				"id": fmt.Sprintf("native-inline-%d", sequence), "model": "gpt-5.6-terra",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply.content}, "finish_reason": nil}},
			})
			provider.writeEvent(response, map[string]any{
				"id": fmt.Sprintf("native-inline-%d", sequence), "model": "gpt-5.6-terra",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			})
		}
		provider.writeLine(response, "data: [DONE]\n\n")
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *nativeInlineAllowanceProvider) writeEvent(response http.ResponseWriter, value map[string]any) {
	encoded, _ := json.Marshal(value)
	p.writeLine(response, "data: "+string(encoded)+"\n\n")
}

func (p *nativeInlineAllowanceProvider) writeLine(response http.ResponseWriter, value string) {
	_, _ = io.WriteString(response, value)
	if flusher, ok := response.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (p *nativeInlineAllowanceProvider) nextRequest(ctx context.Context) map[string]any {
	select {
	case request := <-p.requests:
		return request
	case <-ctx.Done():
		return nil
	}
}

func (p *nativeInlineAllowanceProvider) reply(reply nativeInlineAllowanceReply) {
	p.replies <- reply
}

type nativeInlineAllowanceActivityRPC struct {
	managed.RPC
	settled chan<- struct{}
}

// SetParentActivityEventHandler observes Pi's native lifecycle frames while
// preserving the production callback that updates the durable activity row.
// The test never manufactures a settled event or changes durable state itself.
func (r *nativeInlineAllowanceActivityRPC) SetParentActivityEventHandler(handler func(map[string]any)) {
	source, ok := r.RPC.(interface {
		SetParentActivityEventHandler(func(map[string]any))
	})
	if !ok {
		return
	}
	source.SetParentActivityEventHandler(func(event map[string]any) {
		if state, _ := event["state"].(string); state == "idle" && r.settled != nil {
			select {
			case r.settled <- struct{}{}:
			default:
			}
		}
		handler(event)
	})
}

func (r *nativeInlineAllowanceActivityRPC) readiness() (interface {
	ApplyPolicy(context.Context, map[string]any) (map[string]any, error)
	WaitForPolicyApplied(context.Context, string, int64) (map[string]any, error)
	RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error)
	WaitForChildSnapshot(context.Context) (map[string]any, error)
}, error) {
	source, ok := r.RPC.(interface {
		ApplyPolicy(context.Context, map[string]any) (map[string]any, error)
		WaitForPolicyApplied(context.Context, string, int64) (map[string]any, error)
		RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error)
		WaitForChildSnapshot(context.Context) (map[string]any, error)
	})
	if !ok {
		return nil, errors.New("installed Pi RPC does not expose readiness-source methods")
	}
	return source, nil
}

func (r *nativeInlineAllowanceActivityRPC) ApplyPolicy(ctx context.Context, value map[string]any) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.ApplyPolicy(ctx, value)
}
func (r *nativeInlineAllowanceActivityRPC) WaitForPolicyApplied(ctx context.Context, generation string, epoch int64) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.WaitForPolicyApplied(ctx, generation, epoch)
}
func (r *nativeInlineAllowanceActivityRPC) RequestChildSnapshot(ctx context.Context, value map[string]any) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.RequestChildSnapshot(ctx, value)
}
func (r *nativeInlineAllowanceActivityRPC) WaitForChildSnapshot(ctx context.Context) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.WaitForChildSnapshot(ctx)
}
func (r *nativeInlineAllowanceActivityRPC) SetRootControlEventHandler(handler func(managed.RootControlEvent)) {
	r.RPC.(managed.RootControlRPC).SetRootControlEventHandler(handler)
}
func (r *nativeInlineAllowanceActivityRPC) ArmRootControlResult(requestID, entryID string) (managed.RootControlAppendToken, error) {
	return r.RPC.(managed.RootControlRPC).ArmRootControlResult(requestID, entryID)
}
func (r *nativeInlineAllowanceActivityRPC) SubmitRootControlResult(ctx context.Context, token managed.RootControlAppendToken, payload map[string]any) (<-chan managed.RootControlResultReceipt, error) {
	return r.RPC.(managed.RootControlRPC).SubmitRootControlResult(ctx, token, payload)
}
func (r *nativeInlineAllowanceActivityRPC) ReconcileRootControlResult(ctx context.Context, payload map[string]any) (managed.RootControlPersistedReceipt, error) {
	return r.RPC.(managed.RootControlRPC).ReconcileRootControlResult(ctx, payload)
}

type nativeInlineAllowanceRPCFactory struct {
	base    managed.RPCFactory
	settled chan<- struct{}
}

func (f *nativeInlineAllowanceRPCFactory) Open(ctx context.Context, process managed.Process) (managed.RPC, error) {
	rpc, err := f.base.Open(ctx, process)
	if err != nil {
		return nil, err
	}
	return &nativeInlineAllowanceActivityRPC{RPC: rpc, settled: f.settled}, nil
}

func writeNativeInlineAllowanceModels(t *testing.T, home, baseURL string) {
	t.Helper()
	path := filepath.Join(home, ".pi", "agent")
	require.NoError(t, os.MkdirAll(path, 0o700))
	models := map[string]any{"providers": map[string]any{"openai-codex": map[string]any{
		"baseUrl": baseURL + "/v1", "api": "openai-completions", "apiKey": "deterministic-inline-allowance-test-key",
		"models": []any{map[string]any{"id": "gpt-5.6-terra", "name": "Deterministic inline allowance model", "reasoning": false, "input": []string{"text"}, "cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}, "contextWindow": 16_384, "maxTokens": 1_024}},
	}}}
	encoded, err := json.Marshal(models)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(path, "models.json"), encoded, 0o600))
}

func seedNativeInlineAllowanceReadinessPolicy(ctx context.Context, path, agentID string) error {
	canonical := []byte(`{"compaction":{"deferred":false},"inline":{"credits":{},"enabled":true},"recall":{"content":""},"settled":{"enabled":false}}`)
	sum := sha256.Sum256(canonical)
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
	rollback := func(err error) error {
		_, _ = db.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE managed_readiness_sources
		SET desired_policy_canonical = ?, desired_policy_sha256 = ?
		WHERE agent_id = ? AND pi_session_id IS NULL AND applied_policy_canonical IS NULL AND snapshot_id IS NULL`, canonical, hex.EncodeToString(sum[:]), agentID)
	if err != nil {
		return rollback(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return rollback(err)
	}
	if changed != 1 {
		return rollback(sql.ErrNoRows)
	}
	_, err = db.ExecContext(ctx, "COMMIT")
	return err
}

func waitNativeInlineAllowanceReady(ctx context.Context, durable *store.Store, agentID string) error {
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
			return fmt.Errorf("managed Pi did not become ready: %w", ctx.Err())
		}
	}
}

func installNativeInlineAllowanceStateTrace(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE native_inline_allowance_state_trace (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL,
			state TEXT NOT NULL
		);
		CREATE TRIGGER native_inline_allowance_trace_insert
		AFTER INSERT ON managed_control_requests
		WHEN NEW.agent_id='native-inline-allowance' AND NEW.operation_id='policy.inline_allowance'
		BEGIN
			INSERT INTO native_inline_allowance_state_trace(request_id,state) VALUES (NEW.request_id,NEW.state);
		END;
		CREATE TRIGGER native_inline_allowance_trace_update
		AFTER UPDATE OF state ON managed_control_requests
		WHEN NEW.agent_id='native-inline-allowance' AND NEW.operation_id='policy.inline_allowance' AND NEW.state<>OLD.state
		BEGIN
			INSERT INTO native_inline_allowance_state_trace(request_id,state) VALUES (NEW.request_id,NEW.state);
		END;`)
	require.NoError(t, err)
}

func nativeInlineAllowanceToolNames(t *testing.T, request map[string]any) []string {
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

func nativeInlineAllowanceStateHistory(path string) ([]string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT state FROM native_inline_allowance_state_trace ORDER BY sequence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var states []string
	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func runNativeInlineAllowanceTurn(ctx context.Context, t *testing.T, manager *managed.Manager, provider *nativeInlineAllowanceProvider, settled <-chan struct{}, databasePath, agentID, prompt string, first, continuation nativeInlineAllowanceReply, expectedTools []string) []string {
	t.Helper()
	var observed []string
	observeDone := make(chan struct{})
	observeResult := make(chan []string, 1)
	observeReady := make(chan struct{})
	if first.toolName == "mecha_request_inline_tool_allowance" && first.args["usageCount"] == 1 {
		go func() {
			ready := false
			ticker := time.NewTicker(100 * time.Microsecond)
			defer ticker.Stop()
			for {
				states, err := nativeInlineAllowanceStateHistory(databasePath)
				if !ready {
					close(observeReady)
					ready = true
				}
				if err == nil && len(states) > len(observed) {
					observed = append(observed, states[len(observed):]...)
				}
				if len(observed) > 0 {
					state := observed[len(observed)-1]
					if state == "delivered" || state == "failed" || state == "cancelled" {
						observeResult <- append([]string(nil), observed...)
						return
					}
				}
				select {
				case <-ticker.C:
				case <-observeDone:
					observeResult <- append([]string(nil), observed...)
					return
				case <-ctx.Done():
					observeResult <- append([]string(nil), observed...)
					return
				}
			}
		}()
		select {
		case <-observeReady:
		case <-ctx.Done():
			t.Fatalf("inline allowance observer did not become ready: %v", ctx.Err())
		}
	} else {
		close(observeReady)
	}
	defer close(observeDone)
	for {
		select {
		case <-settled:
		default:
			goto drained
		}
	}

drained:
	promptDone := make(chan error, 1)
	go func() { promptDone <- manager.AdmitPrompt(ctx, agentID, prompt) }()
	initial := provider.nextRequest(ctx)
	require.NotNil(t, initial, "installed Pi did not make the initial deterministic provider request")
	require.ElementsMatch(t, expectedTools, nativeInlineAllowanceToolNames(t, initial))
	provider.reply(first)
	next := provider.nextRequest(ctx)
	require.NotNil(t, next, "installed Pi did not continue after its tool result")
	require.ElementsMatch(t, expectedTools, nativeInlineAllowanceToolNames(t, next))
	provider.reply(continuation)
	require.NoError(t, <-promptDone)
	select {
	case <-settled:
	case <-ctx.Done():
		t.Fatalf("installed Pi did not emit its native settled event: %v", ctx.Err())
	}
	if first.toolName == "mecha_request_inline_tool_allowance" && first.args["usageCount"] == 1 {
		select {
		case observed = <-observeResult:
		case <-ctx.Done():
			t.Fatalf("inline allowance row did not reach a terminal state: %v", ctx.Err())
		}
	}
	return observed
}

func queryNativeInlineAllowanceRow(t *testing.T, path string) (count int, state, status, content, details, session string, epoch, pid int64, startMarker, incarnation, generation string, revision int64, manifest string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	row := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(state),''), COALESCE(MAX(result_status),''), COALESCE(MAX(result_content),''), COALESCE(MAX(result_details_json),''), COALESCE(MAX(pi_session_id),''), COALESCE(MAX(session_epoch),0), COALESCE(MAX(process_pid),0), COALESCE(MAX(process_start_marker),''), COALESCE(MAX(process_incarnation),''), COALESCE(MAX(runtime_generation),''), COALESCE(MAX(manifest_revision),0), COALESCE(MAX(manifest_hash),'') FROM managed_control_requests WHERE agent_id=? AND operation_id=?`, "native-inline-allowance", "policy.inline_allowance")
	require.NoError(t, row.Scan(&count, &state, &status, &content, &details, &session, &epoch, &pid, &startMarker, &incarnation, &generation, &revision, &manifest))
	return
}

func managedInlineAllowanceProjection(t *testing.T, ctx context.Context, baseURL, agentID string) map[string]any {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/managed-agents/"+agentID, nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var projection map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&projection))
	return projection
}

// TestManagedInlineAllowanceInstalledPiFunctional proves the cross-process
// behavior rather than only the router handler: a locked installed Pi advertises
// the manifest, blocks default-deny bash, accepts one fenced allowance, spends
// it once, and returns to default-deny after the runtime is replaced.
func TestManagedInlineAllowanceInstalledPiFunctional(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	lockedPi := requireManagedRootControlLockedPi(t, root)
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	expectedTools := expectedManagedActiveToolsForTransport(manifest)
	provider := newNativeInlineAllowanceProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	installNativeInlineAllowanceStateTrace(t, databasePath)
	home := t.TempDir()
	sessionRoot := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	writeNativeInlineAllowanceModels(t, home, provider.server.URL)
	settled := make(chan struct{}, 32)
	rpcFactory := &nativeInlineAllowanceRPCFactory{base: nativeObservedRPCFactory{counter: &nativeDirectCommandCounter{}}, settled: settled}
	factory := &nativeBlockingProcessFactory{base: managed.DefaultProcessFactory(), entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-factory.release:
		default:
			close(factory.release)
		}
	})
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable, Manifest: manifest, ProcessFactory: factory, RPCFactory: rpcFactory,
		Config: managed.Config{
			Enabled: true, PiExecutable: lockedPi.executable, ManagedHome: home, PiAgentDir: filepath.Join(home, ".pi", "agent"), SessionRoot: sessionRoot,
			RouterURL: "http://127.0.0.1:18775", ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"),
			MechaRPCExtension: filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts"), PiSubagentsExtension: lockedPi.subagentsEntry,
			AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor",
		},
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

	launchBody, err := json.Marshal(map[string]string{"name": "native-inline-allowance", "cwd": root})
	require.NoError(t, err)
	launchRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/managed-agents", bytes.NewReader(launchBody))
	require.NoError(t, err)
	launchRequest.Header.Set("Content-Type", "application/json")
	launchRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174220")
	launchResponse, err := http.DefaultClient.Do(launchRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, launchResponse.StatusCode)
	require.NoError(t, launchResponse.Body.Close())
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal("installed Pi did not reach the readiness barrier")
	}
	require.NoError(t, seedNativeInlineAllowanceReadinessPolicy(ctx, databasePath, "native-inline-allowance"))
	close(factory.release)
	require.NoError(t, waitNativeInlineAllowanceReady(ctx, durable, "native-inline-allowance"))

	// The managed setting path confirms the enabled/default-deny projection before
	// any model turn. Credits remain empty until the control tool is admitted.
	require.NoError(t, manager.SetManagedInlineTools(ctx, "native-inline-allowance", true))
	before := managedInlineAllowanceProjection(t, ctx, httpURL, "native-inline-allowance")
	require.NotEmpty(t, before["generated_at"])
	require.NotEmpty(t, before["agent"])

	blockedMarker := filepath.Join(t.TempDir(), "blocked-before-grant")
	runNativeInlineAllowanceTurn(ctx, t, manager, provider, settled, databasePath, "native-inline-allowance", "Try the inline bash tool.", nativeInlineAllowanceReply{
		toolName: "bash", args: map[string]any{"command": fmt.Sprintf("printf blocked > %q", blockedMarker)},
	}, nativeInlineAllowanceReply{content: "The default-deny tool call was handled."}, expectedTools)
	_, err = os.Stat(blockedMarker)
	require.ErrorIs(t, err, os.ErrNotExist, "default-deny must reject builtin bash before execution")

	// The manifest boundary rejects usageCount=6 before a router control row is
	// created; this is a failed grant, not a partially applied credit.
	invalidMarker := filepath.Join(t.TempDir(), "invalid-grant")
	runNativeInlineAllowanceTurn(ctx, t, manager, provider, settled, databasePath, "native-inline-allowance", "Request an invalid allowance.", nativeInlineAllowanceReply{
		toolName: "mecha_request_inline_tool_allowance", args: map[string]any{"toolName": "bash", "usageCount": 6},
	}, nativeInlineAllowanceReply{content: "Invalid allowance rejected."}, expectedTools)
	invalidCount, _, _, _, _, _, _, _, _, _, _, _, _ := queryNativeInlineAllowanceRow(t, databasePath)
	require.Zero(t, invalidCount)
	_, err = os.Stat(invalidMarker)
	require.ErrorIs(t, err, os.ErrNotExist)

	grantedMarker := filepath.Join(t.TempDir(), "granted-once")
	grantStates := runNativeInlineAllowanceTurn(ctx, t, manager, provider, settled, databasePath, "native-inline-allowance", "Grant one inline bash use, then continue.", nativeInlineAllowanceReply{
		toolName: "mecha_request_inline_tool_allowance", args: map[string]any{"toolName": "bash", "usageCount": 1},
	}, nativeInlineAllowanceReply{content: "One bash use granted."}, expectedTools)
	require.Contains(t, grantStates, "received", "allowance admission must create the queued control row")
	require.Contains(t, grantStates, "executing", "allowance admission must claim the control row")
	require.Equal(t, "delivered", grantStates[len(grantStates)-1], "allowance result must correlate to a terminal delivered row")
	require.Eventually(t, func() bool {
		count, state, _, _, _, _, _, _, _, _, _, _, _ := queryNativeInlineAllowanceRow(t, databasePath)
		return count == 1 && state == "delivered"
	}, 20*time.Second, 20*time.Millisecond)
	count, state, status, content, details, session, epoch, pid, startMarker, incarnation, generation, revision, manifestHash := queryNativeInlineAllowanceRow(t, databasePath)
	require.Equal(t, 1, count)
	require.Equal(t, "delivered", state)
	require.Equal(t, "ok", status)
	require.Equal(t, "Granted 1 inline bash use(s).", content)
	require.Contains(t, details, `"tool_name":"bash"`)
	require.Contains(t, details, `"usage_count":1`)
	agent, err := durable.ManagedAgent(ctx, "native-inline-allowance")
	require.NoError(t, err)
	require.Equal(t, agent.PiSessionID, session)
	require.EqualValues(t, agent.SessionEpoch, epoch)
	require.EqualValues(t, agent.ProcessPID, pid)
	require.Equal(t, agent.ProcessStartMarker, startMarker)
	require.Equal(t, agent.ProcessIncarnation, incarnation)
	require.Equal(t, agent.RuntimeGeneration, generation)
	require.EqualValues(t, agent.ManifestRevision, revision)
	require.Equal(t, agent.ManifestHash, manifestHash)

	runNativeInlineAllowanceTurn(ctx, t, manager, provider, settled, databasePath, "native-inline-allowance", "Use the granted inline bash tool once.", nativeInlineAllowanceReply{
		toolName: "bash", args: map[string]any{"command": fmt.Sprintf("printf granted > %q", grantedMarker)},
	}, nativeInlineAllowanceReply{content: "The granted bash call completed."}, expectedTools)
	contents, err := os.ReadFile(grantedMarker)
	require.NoError(t, err)
	require.Equal(t, "granted", string(contents))

	postGrantMarker := filepath.Join(t.TempDir(), "blocked-after-consumption")
	runNativeInlineAllowanceTurn(ctx, t, manager, provider, settled, databasePath, "native-inline-allowance", "Try bash again without another grant.", nativeInlineAllowanceReply{
		toolName: "bash", args: map[string]any{"command": fmt.Sprintf("printf blocked > %q", postGrantMarker)},
	}, nativeInlineAllowanceReply{content: "The spent allowance blocked the second call."}, expectedTools)
	_, err = os.Stat(postGrantMarker)
	require.ErrorIs(t, err, os.ErrNotExist, "one credit must be consumed before builtin execution")

	beforeReset, err := durable.ManagedAgent(ctx, "native-inline-allowance")
	require.NoError(t, err)
	reset, err := manager.Reset(ctx, managed.ResetRequest{AgentID: "native-inline-allowance", Method: http.MethodPost, CanonicalPath: "/v1/managed-agents/native-inline-allowance/reset", RequestID: "123e4567-e89b-42d3-a456-426614174221", CanonicalBodyHash: strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.False(t, reset.Replayed)
	resetReady := false
	if !assert.Eventually(t, func() bool {
		replaced, readErr := durable.ManagedAgent(ctx, "native-inline-allowance")
		resetReady = readErr == nil && replaced.LifecycleState == "running" && replaced.AcceptingPrompts && (replaced.RuntimeGeneration != beforeReset.RuntimeGeneration || replaced.SessionEpoch != beforeReset.SessionEpoch)
		return resetReady
	}, 30*time.Second, 20*time.Millisecond) {
		action, actionErr := durable.ManagedLifecycleAction(ctx, reset.Operation.OperationID)
		replaced, agentErr := durable.ManagedAgent(ctx, "native-inline-allowance")
		t.Fatalf("reset did not reach a new ready fence: action=%+v action_err=%v agent=%+v agent_err=%v", action, actionErr, replaced, agentErr)
	}
	afterReset, err := durable.ManagedAgent(ctx, "native-inline-allowance")
	require.NoError(t, err)
	require.NotEqual(t, beforeReset.RuntimeGeneration, afterReset.RuntimeGeneration, "replacement must have a new runtime generation fence")

	replacementMarker := filepath.Join(t.TempDir(), "blocked-on-replacement")
	runNativeInlineAllowanceTurn(ctx, t, manager, provider, settled, databasePath, "native-inline-allowance", "Try the old inline allowance after reset.", nativeInlineAllowanceReply{
		toolName: "bash", args: map[string]any{"command": fmt.Sprintf("printf blocked > %q", replacementMarker)},
	}, nativeInlineAllowanceReply{content: "The replacement rejected the old credit."}, expectedTools)
	_, err = os.Stat(replacementMarker)
	require.ErrorIs(t, err, os.ErrNotExist, "old process-local credit must not cross the replacement fence")
	after := managedInlineAllowanceProjection(t, ctx, httpURL, "native-inline-allowance")
	require.NotEmpty(t, after["generated_at"])
	require.NotEmpty(t, after["agent"])

	// Child exemption and direct stale-fence rejection remain owned by the
	// narrower managed/extension tests: this installed-Pi path must not weaken
	// either independent contract while proving the root behavior above.
}
