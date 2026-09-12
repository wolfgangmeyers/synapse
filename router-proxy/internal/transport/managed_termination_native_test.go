package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// This explicit ledger is the complete dirty release candidate used by both
// locked native proofs. It includes Phase 3 release and evidence paths as well
// as Phase 2 implementation dependencies, so neither proof can be detached
// from the exact candidate that will be released.
var managedPhase2CandidatePaths = []string{
	".claude/skills/supervisor/tools/managed_root_control.py",
	"RPC_IMPLEMENTATION_PLAN.html",
	"docs/rpc-phase2-lifecycle-controls-gate-record.md",
	"pi-extensions/mecha-rpc/index.ts",
	"pi-extensions/mecha-rpc/protocol.ts",
	"pi-extensions/mecha-rpc/tool-registry.ts",
	"router-proxy/cmd/mailbox-router/main.go",
	"router-proxy/cmd/mailbox-router/managed_root_control.go",
	"router-proxy/cmd/mailbox-router/managed_root_control_test.go",
	"router-proxy/cmd/managed-acceptance-router/main.go",
	"router-proxy/internal/managed/config.go",
	"router-proxy/internal/managed/manifest.go",
	"router-proxy/internal/managed/manifest_test.go",
	"router-proxy/internal/managed/process.go",
	"router-proxy/internal/managed/projection.go",
	"router-proxy/internal/managed/root_control_handlers.go",
	"router-proxy/internal/managed/root_control_manager.go",
	"router-proxy/internal/managed/root_control_rpc_test.go",
	"router-proxy/internal/managed/rpc.go",
	"router-proxy/internal/managed/rpc_abort_s5_test.go",
	"router-proxy/internal/managed/supervisor.go",
	"router-proxy/internal/managed/supervisor_launch_test.go",
	"router-proxy/internal/managed/supervisor_reset_s1.go",
	"router-proxy/internal/managed/supervisor_restart_s3.go",
	"router-proxy/internal/managed/supervisor_restart_execution_test.go",
	"router-proxy/internal/managed/supervisor_test.go",
	"router-proxy/internal/managed/supervisor_termination_s5.go",
	"router-proxy/internal/managed/supervisor_termination_s5_test.go",
	"router-proxy/internal/store/managed.go",
	"router-proxy/internal/store/managed_lifecycle_s0.go",
	"router-proxy/internal/store/managed_root_control_s4.go",
	"router-proxy/internal/store/managed_root_control_s4_test.go",
	"router-proxy/internal/store/managed_restart_all_s2.go",
	"router-proxy/internal/store/managed_test.go",
	"router-proxy/internal/store/managed_termination_s5.go",
	"router-proxy/internal/store/managed_termination_s5_test.go",
	"router-proxy/internal/store/migrations/029_managed_termination_diagnostics.sql",
	"router-proxy/internal/store/store.go",
	"router-proxy/internal/store/store_test.go",
	"router-proxy/internal/transport/managed_http.go",
	"router-proxy/internal/transport/managed_http_test.go",
	"router-proxy/internal/transport/managed_launch_http_test.go",
	"router-proxy/internal/transport/managed_native_test.go",
	"router-proxy/internal/transport/managed_reset_native_test.go",
	"router-proxy/internal/transport/managed_root_control_native_test.go",
	"router-proxy/internal/transport/managed_termination_http_test.go",
	"router-proxy/internal/transport/managed_termination_native_test.go",
	"router-proxy/internal/transport/transport_test.go",
	"router-proxy/internal/transport/ui_live.go",
	"router-proxy/internal/transport/ui_live_test.go",
	"router/contract/http-outcomes.json",
	"router/contract/managed-tools-v1.json",
	"router/contract/manifest.json",
	"tests/fixtures/mecha-rpc/acceptance-evidence-v1.json",
	"tests/fixtures/mecha-rpc/s4-native-root-control-probe.ts",
	"tests/mecha_rpc_extension.test.ts",
	"tests/mecha_rpc_manifest.test.ts",
	"tests/mecha_rpc_native_pi_e2e.test.ts",
	"tools/check_rpc_acceptance.mjs",
	"tools/generate_managed_tools.mjs",
	"tools/validate_mecha_rpc_phase1.mjs",
	"web/router-ui-react/src/App.tsx",
	"web/router-ui-react/src/app.css",
	"web/router-ui-react/src/components/AgentRow.tsx",
	"web/router-ui-react/src/components/Conversation.tsx",
	"web/router-ui-react/src/components/ConversationHeader.tsx",
	"web/router-ui-react/src/components/ManagedStateDialog.tsx",
	"web/router-ui-react/src/components/MessageList.tsx",
	"web/router-ui-react/src/components/SubagentBadge.tsx",
	"web/router-ui-react/tests/acceptance/bcb-managed-rpc-release-runner.mjs",
	"web/router-ui-react/tests/acceptance/bcb-runner-selectors.test.mjs",
	"web/router-ui-react/tests/component/app-integration.test.tsx",
	"web/router-ui-react/tests/component/complete-timeline-entry.test.tsx",
	"web/router-ui-react/tests/component/managed-children.test.tsx",
	"web/router-ui-react/tests/component/managed-history.test.tsx",
}

func requireManagedPhase2CandidateLedger(t *testing.T, root string) {
	t.Helper()
	status := commandOutput(t, root, "git", "status", "--porcelain", "--untracked-files=all")
	for _, line := range strings.Split(status, "\n") {
		start := 3
		// commandOutput trims leading whitespace from the first porcelain row.
		if len(line) >= 2 && line[0] != ' ' && line[1] == ' ' {
			start = 2
		}
		if len(line) > start {
			require.Contains(t, managedPhase2CandidatePaths, line[start:], "the locked native proof cannot omit a dirty Phase 2 path")
		}
	}
}

func managedPhase2CandidateDigests(t *testing.T, root string) (string, map[string]string) {
	t.Helper()
	requireManagedPhase2CandidateLedger(t, root)
	hash := sha256.New()
	digests := make(map[string]string, len(managedPhase2CandidatePaths))
	for _, relative := range managedPhase2CandidatePaths {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if os.IsNotExist(err) {
			// The release removes this source path; retain that absence in the
			// candidate identity rather than silently dropping it from the proof.
			digests[relative] = "deleted"
			_, err = fmt.Fprintf(hash, "%s\x00deleted\n", relative)
			require.NoError(t, err)
			continue
		}
		require.NoError(t, err)
		sum := sha256.Sum256(contents)
		digest := hex.EncodeToString(sum[:])
		digests[relative] = digest
		_, err = fmt.Fprintf(hash, "%s\x00%s\n", relative, digest)
		require.NoError(t, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), digests
}

type nativeTerminationWireTrace struct {
	mu                          sync.Mutex
	stdin, stdout               []byte
	abortRequest, abortResponse map[string]any
	abortElapsed                time.Duration
	abortErr                    error
}

func (t *nativeTerminationWireTrace) observeRequest(value []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stdin = append(t.stdin, value...)
	t.consumeLocked(&t.stdin, func(message map[string]any) {
		if message["type"] == "abort" {
			t.abortRequest = cloneNativeTerminationMessage(message)
		}
	})
}

func (t *nativeTerminationWireTrace) observeResponse(value []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stdout = append(t.stdout, value...)
	t.consumeLocked(&t.stdout, func(message map[string]any) {
		if t.abortRequest == nil || message["id"] != t.abortRequest["id"] {
			return
		}
		t.abortResponse = cloneNativeTerminationMessage(message)
	})
}

func (t *nativeTerminationWireTrace) consumeLocked(buffer *[]byte, handle func(map[string]any)) {
	for {
		index := bytes.IndexByte(*buffer, '\n')
		if index < 0 {
			return
		}
		line := (*buffer)[:index]
		*buffer = (*buffer)[index+1:]
		var message map[string]any
		if json.Unmarshal(line, &message) == nil {
			handle(message)
		}
	}
}

func (t *nativeTerminationWireTrace) recordAbort(elapsed time.Duration, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.abortElapsed, t.abortErr = elapsed, err
}

func (t *nativeTerminationWireTrace) snapshot() (map[string]any, map[string]any, time.Duration, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneNativeTerminationMessage(t.abortRequest), cloneNativeTerminationMessage(t.abortResponse), t.abortElapsed, t.abortErr
}

func cloneNativeTerminationMessage(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

type nativeTerminationTraceWriteCloser struct {
	io.WriteCloser
	trace *nativeTerminationWireTrace
}

func (w nativeTerminationTraceWriteCloser) Write(value []byte) (int, error) {
	count, err := w.WriteCloser.Write(value)
	if count > 0 {
		w.trace.observeRequest(value[:count])
	}
	return count, err
}

type nativeTerminationTraceReadCloser struct {
	io.ReadCloser
	trace *nativeTerminationWireTrace
}

func (r nativeTerminationTraceReadCloser) Read(value []byte) (int, error) {
	count, err := r.ReadCloser.Read(value)
	if count > 0 {
		r.trace.observeResponse(value[:count])
	}
	return count, err
}

type nativeTerminationTraceProcess struct {
	base   managed.Process
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func newNativeTerminationTraceProcess(process managed.Process, trace *nativeTerminationWireTrace) managed.Process {
	return &nativeTerminationTraceProcess{
		base:   process,
		stdin:  nativeTerminationTraceWriteCloser{WriteCloser: process.Stdin(), trace: trace},
		stdout: nativeTerminationTraceReadCloser{ReadCloser: process.Stdout(), trace: trace},
	}
}

func (p *nativeTerminationTraceProcess) Identity() managed.ProcessIdentity { return p.base.Identity() }
func (p *nativeTerminationTraceProcess) Stdin() io.WriteCloser             { return p.stdin }
func (p *nativeTerminationTraceProcess) Stdout() io.ReadCloser             { return p.stdout }
func (p *nativeTerminationTraceProcess) Wait() error                       { return p.base.Wait() }
func (p *nativeTerminationTraceProcess) Stop(ctx context.Context) error    { return p.base.Stop(ctx) }

type nativeTerminationTraceRPC struct {
	managed.RPC
	trace *nativeTerminationWireTrace
}

func (r *nativeTerminationTraceRPC) Abort(ctx context.Context) error {
	started := time.Now()
	err := r.RPC.Abort(ctx)
	r.trace.recordAbort(time.Since(started), err)
	return err
}

func (r *nativeTerminationTraceRPC) readinessSource() (nativeResetReadinessRPC, error) {
	source, ok := r.RPC.(nativeResetReadinessRPC)
	if !ok {
		return nil, fmt.Errorf("installed Pi RPC does not expose readiness-source methods")
	}
	return source, nil
}

func (r *nativeTerminationTraceRPC) ApplyPolicy(ctx context.Context, value map[string]any) (map[string]any, error) {
	source, err := r.readinessSource()
	if err != nil {
		return nil, err
	}
	return source.ApplyPolicy(ctx, value)
}

func (r *nativeTerminationTraceRPC) WaitForPolicyApplied(ctx context.Context, generation string, epoch int64) (map[string]any, error) {
	source, err := r.readinessSource()
	if err != nil {
		return nil, err
	}
	return source.WaitForPolicyApplied(ctx, generation, epoch)
}

func (r *nativeTerminationTraceRPC) RequestChildSnapshot(ctx context.Context, value map[string]any) (map[string]any, error) {
	source, err := r.readinessSource()
	if err != nil {
		return nil, err
	}
	return source.RequestChildSnapshot(ctx, value)
}

func (r *nativeTerminationTraceRPC) WaitForChildSnapshot(ctx context.Context) (map[string]any, error) {
	source, err := r.readinessSource()
	if err != nil {
		return nil, err
	}
	return source.WaitForChildSnapshot(ctx)
}

type nativeTerminationTraceRPCFactory struct {
	base  managed.RPCFactory
	trace *nativeTerminationWireTrace
}

func (f nativeTerminationTraceRPCFactory) Open(ctx context.Context, process managed.Process) (managed.RPC, error) {
	rpc, err := f.base.Open(ctx, newNativeTerminationTraceProcess(process, f.trace))
	if err != nil {
		return nil, err
	}
	return &nativeTerminationTraceRPC{RPC: rpc, trace: f.trace}, nil
}

func writeManagedTerminationNativeArtifact(t *testing.T, root string, lockedPi managedResetLockedPi, candidate string, digests map[string]string, assertions map[string]bool, elapsed time.Duration) string {
	t.Helper()
	head := commandOutput(t, root, "git", "rev-parse", "HEAD")
	tree := commandOutput(t, root, "git", "rev-parse", "HEAD^{tree}")
	artifact := map[string]any{
		"protocol":                 "mecha.phase2.termination-s5/v1",
		"result":                   "pass",
		"mecha_base_head":          head,
		"mecha_base_tree":          tree,
		"candidate_content_sha256": candidate,
		"candidate_paths":          managedPhase2CandidatePaths,
		"candidate_path_count":     len(managedPhase2CandidatePaths),
		"candidate_path_sha256":    digests,
		"locked_pi": map[string]any{
			"package_name":               lockedPi.packageName,
			"package_version":            lockedPi.packageVersion,
			"executable_sha256":          lockedPi.executableSHA256,
			"source_lock_sha256":         lockedPi.sourceLockSHA256,
			"source_commit":              lockedPi.sourceCommit,
			"source_tree":                lockedPi.sourceTree,
			"source_package_lock_sha256": lockedPi.sourcePackageLockSHA256,
			"pi_packages_commit":         lockedPi.piPackagesCommit,
			"pi_packages_tree":           lockedPi.piPackagesTree,
		},
		"gate_evidence_sha256": map[string]string{
			"G1": safeFileSHA256(t, filepath.Join(root, "docs", "rpc-phase2-lifecycle-controls-gate-record.md")),
			"G2": safeFileSHA256(t, filepath.Join(root, "docs", "rpc-phase2-lifecycle-controls-gate-record.md")),
			"G3": safeFileSHA256(t, filepath.Join(root, "docs", "rpc-phase2-lifecycle-controls-gate-record.md")),
			"G4": safeFileSHA256(t, filepath.Join(root, "docs", "rpc-phase2-lifecycle-controls-gate-record.md")),
		},
		"assertions": assertions,
		"observed": map[string]any{
			"abort_elapsed_milliseconds": elapsed.Milliseconds(),
		},
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	path := filepath.Join("/tmp", "mecha-rpc-acceptance", head, "phase2-termination-s5", candidate, "termination-s5-v1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	return path
}

// TestManagedTerminateInstalledPi is the S5 proof. It starts the exact locked
// Pi, traces the live JSONL request and response, and then proves that the
// typed Abort acknowledgement and five-second deadline occur before durable
// exact-group termination removes the managed row.
func TestManagedTerminateInstalledPi(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	lockedPi := requireManagedRootControlLockedPi(t, root)
	candidate, digests := managedPhase2CandidateDigests(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
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
	defer func() {
		select {
		case <-factory.release:
		default:
			close(factory.release)
		}
	}()
	trace := &nativeTerminationWireTrace{}
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store: durable, Manifest: manifest, ProcessFactory: factory, RPCFactory: nativeTerminationTraceRPCFactory{base: managed.DefaultRPCFactory(), trace: trace},
		Config: managed.Config{
			Enabled: true, PiExecutable: lockedPi.executable, ManagedHome: home, PiAgentDir: filepath.Join(home, ".pi", "agent"), SessionRoot: sessionRoot, RouterURL: "http://127.0.0.1:18775",
			ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts"), PiSubagentsExtension: lockedPi.subagentsEntry,
			AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor",
			Limits: managed.Limits{GracefulStop: 5 * time.Second, TermStop: 5 * time.Second},
		},
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
	})
	require.NoError(t, err)
	defer func() { _ = manager.Close(context.Background()) }()
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer func() { _ = router.Close(context.Background()) }()
	baseURL, _ := router.URLs()

	launchBody, err := json.Marshal(map[string]string{"name": "native-termination", "cwd": root})
	require.NoError(t, err)
	launch, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/managed-agents", bytes.NewReader(launchBody))
	require.NoError(t, err)
	launch.Header.Set("Content-Type", "application/json")
	launch.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174401")
	response, err := http.DefaultClient.Do(launch)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal("locked Pi launch did not reach the readiness seed boundary")
	}
	require.NoError(t, seedNativeReadinessPolicy(ctx, databasePath, "native-termination"))
	close(factory.release)
	var lastAgent *store.ManagedAgentRecord
	var lastAgentErr error
	readyDeadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(readyDeadline) {
		lastAgent, lastAgentErr = durable.ManagedAgent(ctx, "native-termination")
		if lastAgentErr == nil && lastAgent.LifecycleState == "running" && lastAgent.AcceptingPrompts {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NotNilf(t, lastAgent, "locked Pi did not leave an inspectable managed agent: %v", lastAgentErr)
	require.Truef(t, lastAgent.LifecycleState == "running" && lastAgent.AcceptingPrompts, "locked Pi did not become ready: lifecycle=%s activity=%s accepting=%t error=%s message=%s", lastAgent.LifecycleState, lastAgent.ActivityState, lastAgent.AcceptingPrompts, lastAgent.LastErrorCode, lastAgent.LastErrorMessage)

	terminate, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/v1/managed-agents/native-termination", nil)
	require.NoError(t, err)
	terminate.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174402")
	response, err = http.DefaultClient.Do(terminate)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174402")
		_, agentErr := durable.ManagedAgent(ctx, "native-termination")
		return actionErr == nil && action.State == "succeeded" && action.TerminalCode == "TERMINATED" && agentErr != nil
	}, 25*time.Second, 100*time.Millisecond)

	request, abortResponse, elapsed, abortErr := trace.snapshot()
	require.NoError(t, abortErr)
	require.NotNil(t, request)
	require.NotNil(t, abortResponse)
	require.Equal(t, "abort", request["type"])
	require.Len(t, request, 2, "Abort must use the stock direct wire command")
	require.Equal(t, request["id"], abortResponse["id"])
	require.Equal(t, "response", abortResponse["type"])
	require.Equal(t, "abort", abortResponse["command"])
	require.Equal(t, true, abortResponse["success"])
	require.LessOrEqual(t, elapsed, 5*time.Second)

	resolvedBinary, err := filepath.EvalSymlinks(os.Getenv("MECHA_PI_BIN"))
	require.NoError(t, err)
	assertions := map[string]bool{
		"g4_lock_verified":             true,
		"locked_executable":            lockedPi.executableSHA256 == safeFileSHA256(t, lockedPi.executable),
		"no_path_fallback":             resolvedBinary == lockedPi.executable,
		"abort_wire_command_observed":  request["type"] == "abort" && len(request) == 2,
		"abort_typed_success_response": abortResponse["type"] == "response" && abortResponse["command"] == "abort" && abortResponse["success"] == true,
		"abort_within_five_seconds":    elapsed <= 5*time.Second,
		"termination_completed":        true,
	}
	artifact := writeManagedTerminationNativeArtifact(t, root, lockedPi, candidate, digests, assertions, elapsed)
	fmt.Printf("MECHA_TERMINATION_S5_ARTIFACT=%s\n", artifact)
}
