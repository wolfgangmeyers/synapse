package transport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// This ledger deliberately covers the complete S4 candidate. Its order is
// stable so the artifact identifies exactly the source it exercised.
var managedRootControlCandidatePaths = managedPhase2CandidatePaths

func requireManagedRootControlCandidateLedger(t *testing.T, root string) {
	t.Helper()
	status := commandOutput(t, root, "git", "status", "--porcelain", "--untracked-files=all")
	actual := make([]string, 0)
	for _, line := range strings.Split(status, "\n") {
		start := 3
		// commandOutput trims leading whitespace from the first porcelain row.
		if len(line) >= 2 && line[0] != ' ' && line[1] == ' ' {
			start = 2
		}
		if len(line) > start {
			actual = append(actual, line[start:])
		}
	}
	for _, path := range actual {
		require.Contains(t, managedRootControlCandidatePaths, path, "the S4 proof cannot include a dirty path outside its candidate ledger")
	}
}

func managedRootControlCandidateDigests(t *testing.T, root string) (string, map[string]string) {
	t.Helper()
	requireManagedRootControlCandidateLedger(t, root)
	hash := sha256.New()
	fileDigests := make(map[string]string, len(managedRootControlCandidatePaths))
	for _, relative := range managedRootControlCandidatePaths {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if os.IsNotExist(err) {
			// The release removes this source path; keep that absence in the
			// candidate identity instead of silently omitting it from S4 proof.
			fileDigests[relative] = "deleted"
			_, err = fmt.Fprintf(hash, "%s\x00deleted\n", relative)
			require.NoError(t, err)
			continue
		}
		require.NoError(t, err)
		sum := sha256.Sum256(contents)
		fileDigests[relative] = hex.EncodeToString(sum[:])
		_, err = fmt.Fprintf(hash, "%s\x00%x\n", relative, sum)
		require.NoError(t, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), fileDigests
}

type managedRootControlNativeObservation struct {
	g4LockVerified, lockedExecutable, noPathFallback                           bool
	noModelTurn, zeroProviderCalls, fullManifestRootEnvelope, noLegacyEmission bool
	rootPendingBeforeEmission, forcedResultBeforeSendResolution                bool
	rootPendingBeforeResult, sendFailureRollsBackPending                       bool
	exactGetEntryRecovery, duplicateAlreadyPresent, changedReceiptRejected     bool
	resetFenceCancelled, handlerRegistryComplete                               bool
	providerCalls, handlerExecutions, receiverRebinds                          int
	rootRequestCountBeforeFailure, rootRequestCountAfterFailure                int
	exactEntryLookups, resultSubmissions                                       int
	rootOrder, recoveryTrace                                                   []string
	probeRecords                                                               []map[string]any
}

type nativeRootControlTrace struct {
	mu     sync.Mutex
	method []string
}

func (t *nativeRootControlTrace) record(method string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.method = append(t.method, method)
}

func (t *nativeRootControlTrace) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.method...)
}

func tracePositionAfter(trace []string, method string, after int) int {
	for index := after + 1; index < len(trace); index++ {
		if trace[index] == method {
			return index
		}
	}
	return -1
}

func traceCount(trace []string, method string) int {
	count := 0
	for _, value := range trace {
		if value == method {
			count++
		}
	}
	return count
}

type nativeRootControlProofControls struct {
	mu                  sync.Mutex
	releaseFirstResult  bool
	interruptNextResult bool
	interrupted         bool
	receiverRebinds     int
	exactEntryLookups   int
	resultSubmissions   int
	blockNextHandler    bool
	handlerEntered      chan struct{}
	handlerRelease      chan struct{}
	handlerEnteredOnce  sync.Once
}

func (c *nativeRootControlProofControls) releaseAfterResult() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.releaseFirstResult {
		return false
	}
	c.releaseFirstResult = false
	return true
}

func (c *nativeRootControlProofControls) interruptAfterPersist() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.interruptNextResult {
		return false
	}
	c.interruptNextResult = false
	c.interrupted = true
	return true
}

func (c *nativeRootControlProofControls) noteRebind() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.receiverRebinds++
}

func (c *nativeRootControlProofControls) noteLookup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exactEntryLookups++
}

func (c *nativeRootControlProofControls) noteSubmission() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resultSubmissions++
}

func (c *nativeRootControlProofControls) snapshot() (interrupted bool, rebinds, lookups, submissions int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interrupted, c.receiverRebinds, c.exactEntryLookups, c.resultSubmissions
}

type nativeRootControlTraceRPC struct {
	managed.RPC
	root     managed.RootControlRPC
	trace    *nativeRootControlTrace
	controls *nativeRootControlProofControls

	mu      sync.Mutex
	handler func(managed.RootControlEvent)
}

func (r *nativeRootControlTraceRPC) Prompt(ctx context.Context, message string) error {
	if strings.HasPrefix(message, "/") {
		r.trace.record("prompt:slash:start")
	} else {
		r.trace.record("prompt:non-slash:start")
	}
	err := r.RPC.Prompt(ctx, message)
	if strings.HasPrefix(message, "/mecha-s4-root-control-probe-v1 goal-status") {
		r.trace.record("probe:goal:send-resolved")
	} else if strings.HasPrefix(message, "/") {
		r.trace.record("prompt:slash:resolved")
	} else {
		r.trace.record("prompt:non-slash:resolved")
	}
	return err
}

type nativeRootControlReadinessRPC interface {
	ApplyPolicy(context.Context, map[string]any) (map[string]any, error)
	WaitForPolicyApplied(context.Context, string, int64) (map[string]any, error)
	RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error)
	WaitForChildSnapshot(context.Context) (map[string]any, error)
}

func (r *nativeRootControlTraceRPC) readiness() (nativeRootControlReadinessRPC, error) {
	source, ok := r.RPC.(nativeRootControlReadinessRPC)
	if !ok {
		return nil, errors.New("installed Pi RPC does not expose readiness-source methods")
	}
	return source, nil
}
func (r *nativeRootControlTraceRPC) ApplyPolicy(ctx context.Context, policy map[string]any) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.ApplyPolicy(ctx, policy)
}
func (r *nativeRootControlTraceRPC) WaitForPolicyApplied(ctx context.Context, sessionID string, epoch int64) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.WaitForPolicyApplied(ctx, sessionID, epoch)
}
func (r *nativeRootControlTraceRPC) RequestChildSnapshot(ctx context.Context, request map[string]any) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.RequestChildSnapshot(ctx, request)
}
func (r *nativeRootControlTraceRPC) WaitForChildSnapshot(ctx context.Context) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.WaitForChildSnapshot(ctx)
}

func (r *nativeRootControlTraceRPC) installRootReceiver(handler func(managed.RootControlEvent), rebind bool) {
	r.root.SetRootControlEventHandler(func(event managed.RootControlEvent) {
		r.trace.record("root:request")
		handler(event)
	})
	if rebind {
		r.controls.noteRebind()
		r.trace.record("root:receiver:rebound")
	}
}

func (r *nativeRootControlTraceRPC) SetRootControlEventHandler(handler func(managed.RootControlEvent)) {
	r.mu.Lock()
	r.handler = handler
	r.mu.Unlock()
	r.installRootReceiver(handler, false)
}

func (r *nativeRootControlTraceRPC) rebindRootReceiver() error {
	r.mu.Lock()
	handler := r.handler
	r.mu.Unlock()
	if handler == nil {
		return errors.New("root control receiver was not installed")
	}
	r.installRootReceiver(handler, true)
	return nil
}

func (r *nativeRootControlTraceRPC) ArmRootControlResult(requestID, entryID string) (managed.RootControlAppendToken, error) {
	r.trace.record("root:arm")
	return r.root.ArmRootControlResult(requestID, entryID)
}

func (r *nativeRootControlTraceRPC) SubmitRootControlResult(ctx context.Context, token managed.RootControlAppendToken, payload map[string]any) (<-chan managed.RootControlResultReceipt, error) {
	r.controls.noteSubmission()
	r.trace.record("root:submit:start")
	wait, err := r.root.SubmitRootControlResult(ctx, token, payload)
	if err != nil {
		r.trace.record("root:submit:error")
		return nil, err
	}
	r.trace.record("root:submit:resolved")
	if r.controls.releaseAfterResult() {
		r.trace.record("probe:release:start")
		releaseErr := r.RPC.Prompt(ctx, "/mecha-s4-root-control-probe-v1 release")
		r.trace.record("probe:release:resolved")
		if releaseErr != nil {
			return nil, releaseErr
		}
	}
	if r.controls.interruptAfterPersist() {
		requireNoError := r.rebindRootReceiver()
		if requireNoError != nil {
			return nil, requireNoError
		}
		r.trace.record("root:submit:interrupted")
		return nil, errors.New("injected interruption after persisted explicit result append")
	}
	return wait, nil
}

func (r *nativeRootControlTraceRPC) GetRootControlEntry(ctx context.Context, entryID string) (map[string]any, error) {
	r.controls.noteLookup()
	r.trace.record("root:get_entry")
	return r.root.GetRootControlEntry(ctx, entryID)
}

type nativeRootControlTraceRPCFactory struct {
	base     managed.RPCFactory
	trace    *nativeRootControlTrace
	controls *nativeRootControlProofControls
	mu       sync.Mutex
	opened   []*nativeRootControlTraceRPC
}

func (f *nativeRootControlTraceRPCFactory) Open(ctx context.Context, process managed.Process) (managed.RPC, error) {
	rpc, err := f.base.Open(ctx, process)
	if err != nil {
		return nil, err
	}
	root, ok := rpc.(managed.RootControlRPC)
	if !ok {
		_ = rpc.Close()
		return nil, errors.New("installed Pi RPC does not expose root-control methods")
	}
	wrapped := &nativeRootControlTraceRPC{RPC: rpc, root: root, trace: f.trace, controls: f.controls}
	f.mu.Lock()
	f.opened = append(f.opened, wrapped)
	f.mu.Unlock()
	return wrapped, nil
}

func (f *nativeRootControlTraceRPCFactory) latest(t *testing.T) *nativeRootControlTraceRPC {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.opened)
	return f.opened[len(f.opened)-1]
}

type nativeRootControlHandlerTracker struct {
	mu         sync.Mutex
	executions int
	blockNext  bool
	entered    chan struct{}
	release    chan struct{}
	once       sync.Once
	full       bool
}

func (t *nativeRootControlHandlerTracker) execute(identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	t.mu.Lock()
	t.executions++
	block := t.blockNext
	if block {
		t.blockNext = false
	}
	t.full = identity.CapabilityID == "goal" && identity.ToolName == "goal" && identity.DefinitionID != "" && identity.DefinitionSHA256 != "" && identity.OperationID == "goal.control" && invocation.OperationID == "goal.control" && invocation.RequestID != "" && invocation.ResultID != "" && invocation.ToolCallID != ""
	t.mu.Unlock()
	if block {
		t.once.Do(func() { close(t.entered) })
		<-t.release
	}
	if !t.full {
		return managed.RootControlHandlerResult{}, errors.New("S4 probe reached the wrong root handler")
	}
	return managed.RootControlHandlerResult{Status: "ok", Content: "Goal status checked.", Details: map[string]any{"action": "status"}}, nil
}

func (t *nativeRootControlHandlerTracker) snapshot() (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.executions, t.full
}

func rootControlNativeHandlers(t *testing.T, manifest managed.Manifest, tracker *nativeRootControlHandlerTracker, observed *managedRootControlNativeObservation) []managed.RootControlHandler {
	t.Helper()
	handlers, err := managed.NewOperationalRootControlHandlers(manifest, managed.RootControlOperationExecutorFunc{
		ExecuteFunc: func(_ context.Context, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
			return tracker.execute(identity, invocation)
		},
		ReconcileFunc: func(_ context.Context, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
			return managed.RootControlHandlerResult{Status: "ok", Content: "Goal status checked.", Details: map[string]any{"action": "status", "reconciled": true, "operation": identity.OperationID}}, nil
		},
	})
	require.NoError(t, err)
	observed.handlerRegistryComplete = len(handlers) == len(manifest.Tools)
	require.True(t, observed.handlerRegistryComplete)
	return handlers
}

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

func nativeProviderInterceptor(t *testing.T) (string, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "provider-interceptor.json")
	modulePath := filepath.Join(t.TempDir(), "provider-interceptor.cjs")
	module := `const fs = require("node:fs");
const log = process.env.MECHA_RPC_PROVIDER_INTERCEPTOR_LOG;
if (!log) throw new Error("provider interceptor log is required");
let calls = 0;
const write = () => fs.writeFileSync(log, JSON.stringify({ provider_call_count: calls }));
const intercept = (target, name) => {
  if (typeof target[name] !== "function") return;
  const original = target[name];
  target[name] = function (...args) {
    calls += 1;
    write();
    throw new Error("native S4 provider interceptor blocked a provider transport call");
  };
  return original;
};
intercept(globalThis, "fetch");
intercept(require("node:http"), "request");
intercept(require("node:https"), "request");
write();
`
	require.NoError(t, os.WriteFile(modulePath, []byte(module), 0o600))
	return modulePath, logPath
}

func nativeProviderCalls(t *testing.T, path string) int {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err, "the provider interceptor must start before Pi")
	var observed struct {
		ProviderCallCount int `json:"provider_call_count"`
	}
	require.NoError(t, json.Unmarshal(contents, &observed))
	return observed.ProviderCallCount
}

func nativeRootProbeRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err, "the installed extension must publish its native observation")
	var value map[string]any
	require.NoError(t, json.Unmarshal(contents, &value))
	recordsValue, ok := value["records"].([]any)
	require.True(t, ok, "S4 fixture must return a bounded observation record")
	records := make([]map[string]any, 0, len(recordsValue))
	for _, raw := range recordsValue {
		record, ok := raw.(map[string]any)
		require.True(t, ok, "S4 fixture observation record is invalid")
		records = append(records, record)
	}
	return records
}

func nativeRecordBool(t *testing.T, record map[string]any, key string) bool {
	t.Helper()
	value, ok := record[key].(bool)
	require.Truef(t, ok, "S4 fixture observation %s is missing", key)
	return value
}

func nativeRecordString(t *testing.T, record map[string]any, key string) string {
	t.Helper()
	value, ok := record[key].(string)
	require.Truef(t, ok, "S4 fixture observation %s is missing", key)
	return value
}

func rootControlRows(t *testing.T, ctx context.Context, databasePath string) []store.ManagedControlRequest {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT request_id FROM managed_control_requests ORDER BY received_at,request_id`)
	require.NoError(t, err)
	defer rows.Close()
	var result []store.ManagedControlRequest
	for rows.Next() {
		var requestID string
		require.NoError(t, rows.Scan(&requestID))
		// Use the typed Store projection in the caller to retain its fence. This
		// direct query only enumerates request IDs; it never reads session history.
		result = append(result, store.ManagedControlRequest{RequestID: requestID})
	}
	require.NoError(t, rows.Err())
	return result
}

func managedRootControlRows(t *testing.T, ctx context.Context, durable *store.Store, databasePath string) []store.ManagedControlRequest {
	t.Helper()
	ids := rootControlRows(t, ctx, databasePath)
	rows := make([]store.ManagedControlRequest, 0, len(ids))
	for _, item := range ids {
		row, err := durable.ManagedControlRequest(ctx, item.RequestID)
		require.NoError(t, err)
		rows = append(rows, *row)
	}
	return rows
}

func writeManagedRootControlArtifact(t *testing.T, root string, lockedPi managedResetLockedPi, manifest managed.Manifest, observed managedRootControlNativeObservation, methodTrace []string) string {
	t.Helper()
	head := commandOutput(t, root, "git", "rev-parse", "HEAD")
	tree := commandOutput(t, root, "git", "rev-parse", "HEAD^{tree}")
	candidate, candidatePathSHA256 := managedRootControlCandidateDigests(t, root)
	registry := sha256.New()
	for _, tool := range manifest.Tools {
		_, err := fmt.Fprintf(registry, "%s\x00%s\x00%s\x00%s\x00%s\n", tool.CapabilityID, tool.Name, tool.DefinitionID, tool.DefinitionSHA256, tool.OperationID)
		require.NoError(t, err)
	}
	artifact := map[string]any{
		"protocol": "mecha.phase2.root-control-s4/v1", "result": "pass", "mecha_base_head": head, "mecha_base_tree": tree,
		"candidate_content_sha256": candidate, "candidate_paths": managedRootControlCandidatePaths,
		"candidate_path_count": len(managedRootControlCandidatePaths), "candidate_path_sha256": candidatePathSHA256,
		"manifest_sha256": manifest.CanonicalSHA256, "registry_sha256": hex.EncodeToString(registry.Sum(nil)),
		"locked_pi":            map[string]any{"package_name": lockedPi.packageName, "package_version": lockedPi.packageVersion, "executable_sha256": lockedPi.executableSHA256, "source_lock_sha256": lockedPi.sourceLockSHA256, "source_commit": lockedPi.sourceCommit, "source_tree": lockedPi.sourceTree, "source_package_lock_sha256": lockedPi.sourcePackageLockSHA256, "pi_packages_commit": lockedPi.piPackagesCommit, "pi_packages_tree": lockedPi.piPackagesTree},
		"gate_evidence_sha256": map[string]string{"G1": safeFileSHA256(t, filepath.Join(root, "docs", "rpc-phase2-lifecycle-controls-gate-record.md")), "G2": safeFileSHA256(t, filepath.Join(root, "docs", "rpc-phase2-lifecycle-controls-gate-record.md")), "G3": safeFileSHA256(t, filepath.Join(root, "docs", "rpc-phase2-lifecycle-controls-gate-record.md")), "G4": safeFileSHA256(t, filepath.Join(root, "docs", "rpc-phase2-lifecycle-controls-gate-record.md"))},
		"assertions": map[string]bool{
			"g4_lock_verified":                     observed.g4LockVerified,
			"locked_executable":                    observed.lockedExecutable,
			"no_path_fallback":                     observed.noPathFallback,
			"no_model_turn":                        observed.noModelTurn,
			"zero_provider_calls":                  observed.zeroProviderCalls,
			"full_manifest_root_envelope":          observed.fullManifestRootEnvelope,
			"no_legacy_emission":                   observed.noLegacyEmission,
			"root_pending_before_emission":         observed.rootPendingBeforeEmission,
			"forced_result_before_send_resolution": observed.forcedResultBeforeSendResolution,
			"root_pending_before_result":           observed.rootPendingBeforeResult,
			"send_failure_rolls_back_pending":      observed.sendFailureRollsBackPending,
			"exact_get_entry_recovery":             observed.exactGetEntryRecovery,
			"duplicate_already_present":            observed.duplicateAlreadyPresent,
			"changed_receipt_rejected":             observed.changedReceiptRejected,
			"reset_fence_cancelled":                observed.resetFenceCancelled,
			"handler_registry_complete":            observed.handlerRegistryComplete,
		},
		"observed": map[string]any{
			"provider_interceptor":                     map[string]any{"provider_call_count": observed.providerCalls},
			"root_request_count_before_forced_failure": observed.rootRequestCountBeforeFailure,
			"root_request_count_after_forced_failure":  observed.rootRequestCountAfterFailure,
			"handler_executions":                       observed.handlerExecutions,
			"receiver_rebinds":                         observed.receiverRebinds,
			"exact_entry_lookups":                      observed.exactEntryLookups,
			"result_submissions":                       observed.resultSubmissions,
			"root_order":                               observed.rootOrder,
			"recovery_trace":                           observed.recoveryTrace,
			"fixture_records":                          observed.probeRecords,
			"rpc_method_trace":                         methodTrace,
		},
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	path := filepath.Join("/tmp", "mecha-rpc-acceptance", head, "phase2-root-control-s4", candidate, "root-control-s4-v1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	return path
}

// TestManagedRootControlInstalledPi uses the locked installed Pi to exercise
// the S4 root delivery seam. Every artifact boolean below is calculated from
// an observed Pi response, durable row, or interceptor count, never from the
// source shape that is being proved.
func TestManagedRootControlInstalledPi(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	lockedPi := requireManagedRootControlLockedPi(t, root)
	observed := managedRootControlNativeObservation{g4LockVerified: true}
	observed.lockedExecutable = lockedPi.executableSHA256 == safeFileSHA256(t, lockedPi.executable)
	resolvedSuppliedBinary, err := filepath.EvalSymlinks(os.Getenv("MECHA_PI_BIN"))
	require.NoError(t, err)
	observed.noPathFallback = resolvedSuppliedBinary == lockedPi.executable
	providerModule, providerLog := nativeProviderInterceptor(t)
	observationPath := filepath.Join(t.TempDir(), "root-control-observation.json")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	home := t.TempDir()
	sessionRoot := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700))
	factory := &nativeBlockingProcessFactory{base: managed.DefaultProcessFactory(), entered: make(chan struct{}), release: make(chan struct{})}
	trace := &nativeRootControlTrace{}
	controls := &nativeRootControlProofControls{releaseFirstResult: true}
	tracker := &nativeRootControlHandlerTracker{}
	rpcFactory := &nativeRootControlTraceRPCFactory{base: managed.DefaultRPCFactory(), trace: trace, controls: controls}
	defer func() {
		select {
		case <-factory.release:
		default:
			close(factory.release)
		}
	}()
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable, Manifest: manifest, ProcessFactory: factory, RPCFactory: rpcFactory, RootControlHandlers: rootControlNativeHandlers(t, manifest, tracker, &observed),
		Config:        managed.Config{Enabled: true, PiExecutable: lockedPi.executable, ManagedHome: home, PiAgentDir: filepath.Join(home, ".pi", "agent"), SessionRoot: sessionRoot, ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts"), PiSubagentsExtension: lockedPi.subagentsEntry, TestOnlyExtensions: []string{filepath.Join(root, "tests", "fixtures", "mecha-rpc", "s4-native-root-control-probe.ts")}, TestOnlyEnvironment: []string{"MECHA_RPC_TEST_ROOT_CONTROL_PROBE=1", "MECHA_RPC_TEST_ROOT_CONTROL_HOLD_FIRST_EMISSION=1", "MECHA_RPC_TEST_ROOT_CONTROL_DETACH_EMISSION=1", "MECHA_RPC_TEST_ROOT_CONTROL_SEND_FAILURE_AT=2", "MECHA_RPC_TEST_ROOT_CONTROL_OBSERVATION_PATH=" + observationPath, "MECHA_RPC_PROVIDER_INTERCEPTOR_LOG=" + providerLog, "NODE_OPTIONS=--require=" + providerModule}, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
	})
	require.NoError(t, err)
	defer func() { _ = manager.Close(context.Background()) }()
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
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 25*time.Second, 100*time.Millisecond)

	const probeCommand = "/mecha-s4-root-control-probe-v1 goal-status"
	// The test-only probe returns after it starts its real shared emission. The
	// emission promise remains held until the actual router result command has
	// returned, so the live ordering race is deterministic.
	require.NoError(t, manager.AdmitPrompt(ctx, "native-root-control", probeCommand))
	t.Cleanup(func() { t.Logf("S4 trace: %v", trace.snapshot()) })
	require.Eventually(t, func() bool {
		rows := managedRootControlRows(t, ctx, durable, databasePath)
		return len(rows) == 1 && rows[0].State == "delivered" && strings.HasPrefix(rows[0].ResultEntryID, "mcr1_")
	}, 20*time.Second, 100*time.Millisecond)
	primary := managedRootControlRows(t, ctx, durable, databasePath)[0]
	primaryRPC := rpcFactory.latest(t)
	var records []map[string]any
	require.Eventually(t, func() bool {
		records = nativeRootProbeRecords(t, observationPath)
		return len(records) == 1 && nativeRecordBool(t, records[0], "send_resolved")
	}, 10*time.Second, 50*time.Millisecond)
	first := records[0]
	require.True(t, nativeRecordBool(t, first, "pending_before_emission"))
	require.True(t, nativeRecordBool(t, first, "request_emitted"))
	require.True(t, nativeRecordBool(t, first, "pending_before_result"))
	require.False(t, nativeRecordBool(t, first, "send_resolved_before_result"))
	require.Equal(t, "accepted", nativeRecordString(t, first, "append_outcome"))
	require.True(t, nativeRecordBool(t, first, "send_resolved"))

	traceBeforeFailure := trace.snapshot()
	observed.rootRequestCountBeforeFailure = traceCount(traceBeforeFailure, "root:request")
	require.Equal(t, 1, observed.rootRequestCountBeforeFailure)
	// The injected transport error happens before Pi receives a request. Pi's
	// prompt RPC acknowledges dispatch first, so the installed extension's
	// observation records the handler-side rejection and rollback.
	require.NoError(t, manager.AdmitPrompt(ctx, "native-root-control", probeCommand))
	traceAfterFailure := trace.snapshot()
	observed.rootRequestCountAfterFailure = traceCount(traceAfterFailure, "root:request")
	require.Equal(t, observed.rootRequestCountBeforeFailure, observed.rootRequestCountAfterFailure)
	require.Eventually(t, func() bool {
		records = nativeRootProbeRecords(t, observationPath)
		return len(records) == 2 && nativeRecordBool(t, records[1], "send_rejected_before_emission")
	}, 10*time.Second, 50*time.Millisecond)
	failed := records[1]
	require.True(t, nativeRecordBool(t, failed, "send_rejected_before_emission"))
	require.False(t, nativeRecordBool(t, failed, "pending_after_send_failure"))

	// This performs a second real G4 explicit-ID append against the persisted
	// result. Pi must report already_present and retain the original entry.
	require.NoError(t, manager.AdmitPrompt(ctx, "native-root-control", "/mecha-s4-root-control-probe-v1 duplicate-append"))
	require.Eventually(t, func() bool {
		records = nativeRootProbeRecords(t, observationPath)
		return len(records) >= 1 && nativeRecordString(t, records[0], "duplicate_outcome") == "already_present"
	}, 10*time.Second, 50*time.Millisecond)
	entry, err := primaryRPC.GetRootControlEntry(ctx, primary.ResultEntryID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	message := entry
	if nested, ok := entry["message"].(map[string]any); ok {
		message = nested
	}
	require.Equal(t, "mecha.control.result.v1", message["customType"])
	require.Equal(t, primary.ResultContent, message["content"])
	beforeDuplicateEffects, _ := tracker.snapshot()
	require.Equal(t, 1, beforeDuplicateEffects)
	require.NoError(t, manager.AdmitPrompt(ctx, "native-root-control", "/mecha-s4-root-control-probe-v1 changed-append"))
	require.Eventually(t, func() bool {
		records = nativeRootProbeRecords(t, observationPath)
		return len(records) >= 1 && nativeRecordBool(t, records[0], "changed_append_rejected")
	}, 10*time.Second, 50*time.Millisecond)
	entryAfterChanged, err := primaryRPC.GetRootControlEntry(ctx, primary.ResultEntryID)
	require.NoError(t, err)
	require.NotNil(t, entryAfterChanged)
	changedMessage := entryAfterChanged
	if nested, ok := entryAfterChanged["message"].(map[string]any); ok {
		changedMessage = nested
	}
	require.Equal(t, primary.ResultContent, changedMessage["content"], "changed explicit append must not replace the persisted receipt")
	records = nativeRootProbeRecords(t, observationPath)
	require.Equal(t, "already_present", nativeRecordString(t, records[0], "duplicate_outcome"))
	require.True(t, nativeRecordBool(t, records[0], "changed_append_attempted"))
	afterDuplicateEffects, _ := tracker.snapshot()
	require.Equal(t, beforeDuplicateEffects, afterDuplicateEffects)

	// The next append really persists in Pi, then the wrapper interrupts the
	// command result and rebinds the receiver. Manager recovery must use the
	// sole exact get_entry seam to settle it without another handler execution.
	controls.mu.Lock()
	controls.interruptNextResult = true
	controls.mu.Unlock()
	require.NoError(t, manager.AdmitPrompt(ctx, "native-root-control", probeCommand))
	require.Eventually(t, func() bool {
		rows := managedRootControlRows(t, ctx, durable, databasePath)
		return len(rows) == 2 && rows[1].State == "delivered"
	}, 20*time.Second, 100*time.Millisecond)
	afterRecoveryEffects, _ := tracker.snapshot()
	require.Equal(t, beforeDuplicateEffects+1, afterRecoveryEffects)

	// Reserve one current-fence root row without dispatching a handler. The
	// reset below is the real S1 executor: its admission transaction must cancel
	// this row before the source session is replaced.
	resetRequestID := "123e4567-e89b-42d3-a456-426614174161"
	resetResultID := "123e4567-e89b-42d3-a456-426614174162"
	_, err = durable.ReserveManagedControlRequest(ctx, store.ManagedControlRequestInput{
		RequestID: resetRequestID, ResultID: resetResultID, AgentID: primary.AgentID, ToolCallID: "native-reset-fence-probe", CapabilityID: primary.CapabilityID, ToolName: primary.ToolName, DefinitionID: primary.DefinitionID, OperationID: primary.OperationID,
		CanonicalArgumentsSHA256: primary.CanonicalArgumentsSHA256, RequestFingerprint: strings.Repeat("b", 64), Fence: primary.Fence, ReceivedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	require.NoError(t, err)
	beforeReset, err := durable.ManagedAgent(ctx, "native-root-control")
	require.NoError(t, err)
	reset, err := manager.Reset(ctx, managed.ResetRequest{AgentID: "native-root-control", Method: "POST", CanonicalPath: "/v1/managed-agents/native-root-control/reset", RequestID: "123e4567-e89b-42d3-a456-426614174163", CanonicalBodyHash: strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.False(t, reset.Replayed)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174163")
		agent, agentErr := durable.ManagedAgent(ctx, "native-root-control")
		return actionErr == nil && agentErr == nil && action.State == "succeeded" && agent.LifecycleState == "running" && agent.AcceptingPrompts && agent.SessionEpoch == beforeReset.SessionEpoch+1
	}, 25*time.Second, 100*time.Millisecond)
	cancelled, err := durable.ManagedControlRequest(ctx, resetRequestID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.State)
	require.Equal(t, "CANCELLED_BY_RESET", cancelled.ErrorCode)
	replacementRPC := rpcFactory.latest(t)
	require.NotSame(t, primaryRPC, replacementRPC)
	oldEntry, err := replacementRPC.GetRootControlEntry(ctx, "mcr1_"+strings.ReplaceAll(resetResultID, "-", ""))
	require.NoError(t, err)
	require.Nil(t, oldEntry, "the replacement session must not receive the cancelled source result")

	methodTrace := trace.snapshot()
	interrupted, rebinds, lookups, submissions := controls.snapshot()
	observed.providerCalls = nativeProviderCalls(t, providerLog)
	observed.handlerExecutions, observed.fullManifestRootEnvelope = tracker.snapshot()
	observed.receiverRebinds, observed.exactEntryLookups, observed.resultSubmissions = rebinds, lookups, submissions
	observed.probeRecords = records
	firstRequest := tracePositionAfter(methodTrace, "root:request", -1)
	firstSubmit := tracePositionAfter(methodTrace, "root:submit:resolved", firstRequest)
	firstRelease := tracePositionAfter(methodTrace, "probe:release:resolved", firstSubmit)
	if firstRequest >= 0 && firstSubmit >= 0 && firstRelease >= 0 {
		observed.rootOrder = []string{methodTrace[firstRequest], methodTrace[firstSubmit], methodTrace[firstRelease]}
	}
	for _, method := range methodTrace {
		if method == "root:receiver:rebound" || method == "root:submit:interrupted" || method == "root:get_entry" {
			observed.recoveryTrace = append(observed.recoveryTrace, method)
		}
	}
	observed.rootPendingBeforeEmission = nativeRecordBool(t, first, "pending_before_emission") && nativeRecordBool(t, first, "request_emitted")
	observed.forcedResultBeforeSendResolution = firstSubmit >= 0 && firstRelease > firstSubmit && !nativeRecordBool(t, first, "send_resolved_before_result") && nativeRecordBool(t, first, "send_resolved")
	observed.rootPendingBeforeResult = nativeRecordBool(t, first, "pending_before_result") && observed.forcedResultBeforeSendResolution
	observed.sendFailureRollsBackPending = nativeRecordBool(t, failed, "send_rejected_before_emission") && !nativeRecordBool(t, failed, "pending_after_send_failure") && observed.rootRequestCountAfterFailure == observed.rootRequestCountBeforeFailure
	observed.exactGetEntryRecovery = interrupted && rebinds == 1 && tracePositionAfter(methodTrace, "root:receiver:rebound", -1) >= 0 && tracePositionAfter(methodTrace, "root:get_entry", tracePositionAfter(methodTrace, "root:submit:interrupted", -1)) >= 0
	observed.duplicateAlreadyPresent = nativeRecordString(t, records[0], "duplicate_outcome") == "already_present" && beforeDuplicateEffects == afterDuplicateEffects && message["content"] == changedMessage["content"]
	observed.changedReceiptRejected = nativeRecordBool(t, records[0], "changed_append_attempted") && changedMessage["content"] == primary.ResultContent
	observed.resetFenceCancelled = cancelled.State == "cancelled" && cancelled.ErrorCode == "CANCELLED_BY_RESET" && oldEntry == nil
	observed.zeroProviderCalls = observed.providerCalls == 0
	observed.noModelTurn = observed.zeroProviderCalls && traceCount(methodTrace, "prompt:non-slash:start") == 0
	observed.noLegacyEmission = observed.fullManifestRootEnvelope && observed.rootRequestCountBeforeFailure == 1

	for name, passed := range map[string]bool{
		"G4 lock":                      observed.g4LockVerified,
		"locked executable":            observed.lockedExecutable,
		"no PATH fallback":             observed.noPathFallback,
		"no model turn":                observed.noModelTurn,
		"zero provider calls":          observed.zeroProviderCalls,
		"complete root envelope":       observed.fullManifestRootEnvelope,
		"no legacy emission":           observed.noLegacyEmission,
		"root pending before emission": observed.rootPendingBeforeEmission,
		"forced early result":          observed.forcedResultBeforeSendResolution,
		"root pending before result":   observed.rootPendingBeforeResult,
		"send failure rollback":        observed.sendFailureRollsBackPending,
		"exact get_entry recovery":     observed.exactGetEntryRecovery,
		"duplicate acceptance":         observed.duplicateAlreadyPresent,
		"changed receipt rejection":    observed.changedReceiptRejected,
		"reset fence cancellation":     observed.resetFenceCancelled,
		"complete handler registry":    observed.handlerRegistryComplete,
	} {
		require.Truef(t, passed, "native S4 observation failed: %s", name)
	}
	artifact := writeManagedRootControlArtifact(t, root, lockedPi, manifest, observed, methodTrace)
	fmt.Printf("MECHA_ROOT_CONTROL_S4_ARTIFACT=%s\n", artifact)
}
