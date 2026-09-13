package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const nativeSeedPolicy = `{"compaction":{"deferred":false},"goal_lock":{"enabled":false},"inline":{"credits":{"bash":2},"enabled":true},"recall":{"content":"readiness seed"},"settled":{"enabled":false}}`
const nativeSeedPolicyHash = "7f88a0a98892509ffdd135e163a5ffe20df486b802d2f176f58af87387a640c8"

type nativeBlockingProcessFactory struct {
	base    managed.ProcessFactory
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *nativeBlockingProcessFactory) Start(ctx context.Context, spec managed.CommandSpec) (managed.Process, error) {
	f.once.Do(func() { close(f.entered) })
	select {
	case <-f.release:
		return f.base.Start(ctx, spec)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type nativeLifecycleRestartGate struct {
	mu      sync.Mutex
	enabled bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *nativeLifecycleRestartGate) enable() {
	g.mu.Lock()
	g.enabled = true
	g.mu.Unlock()
}

func (g *nativeLifecycleRestartGate) wait(ctx context.Context) error {
	g.mu.Lock()
	enabled := g.enabled
	g.mu.Unlock()
	if !enabled {
		return nil
	}
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type nativeLifecycleGatedRPC struct {
	managed.RPC
	gate          *nativeLifecycleRestartGate
	failReadiness bool
}

type nativeLifecycleReadinessRPC interface {
	ApplyPolicy(context.Context, map[string]any) (map[string]any, error)
	WaitForPolicyApplied(context.Context, string, int64) (map[string]any, error)
	RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error)
	WaitForChildSnapshot(context.Context) (map[string]any, error)
}

func (r nativeLifecycleGatedRPC) readiness() (nativeLifecycleReadinessRPC, error) {
	source, ok := r.RPC.(nativeLifecycleReadinessRPC)
	if !ok {
		return nil, errors.New("native locked Pi RPC does not support readiness sources")
	}
	return source, nil
}

func (r nativeLifecycleGatedRPC) GetState(ctx context.Context) (map[string]any, error) {
	if err := r.gate.wait(ctx); err != nil {
		return nil, err
	}
	return r.RPC.GetState(ctx)
}
func (r nativeLifecycleGatedRPC) ApplyPolicy(ctx context.Context, value map[string]any) (map[string]any, error) {
	if r.failReadiness {
		return nil, errors.New("native S3 target readiness interruption")
	}
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.ApplyPolicy(ctx, value)
}
func (r nativeLifecycleGatedRPC) WaitForPolicyApplied(ctx context.Context, generation string, epoch int64) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.WaitForPolicyApplied(ctx, generation, epoch)
}
func (r nativeLifecycleGatedRPC) RequestChildSnapshot(ctx context.Context, value map[string]any) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.RequestChildSnapshot(ctx, value)
}
func (r nativeLifecycleGatedRPC) WaitForChildSnapshot(ctx context.Context) (map[string]any, error) {
	source, err := r.readiness()
	if err != nil {
		return nil, err
	}
	return source.WaitForChildSnapshot(ctx)
}
func (r nativeLifecycleGatedRPC) PrepareManagedMailbox(ctx context.Context) (string, error) {
	source, ok := r.RPC.(managed.DirectMailboxRPC)
	if !ok {
		return "", errors.New("native locked Pi RPC does not support direct mail")
	}
	return source.PrepareManagedMailbox(ctx)
}
func (r nativeLifecycleGatedRPC) PromptManagedMailbox(ctx context.Context, attempt store.ManagedDirectAttempt) (<-chan managed.DirectMailboxEvent, error) {
	source, ok := r.RPC.(managed.DirectMailboxRPC)
	if !ok {
		return nil, errors.New("native locked Pi RPC does not support direct mail")
	}
	return source.PromptManagedMailbox(ctx, attempt)
}
func (r nativeLifecycleGatedRPC) RecoverManagedMailbox(ctx context.Context, attempt store.ManagedDirectAttempt) (managed.DirectMailboxRecovery, error) {
	source, ok := r.RPC.(managed.DirectMailboxRPC)
	if !ok {
		return managed.DirectMailboxRecovery{}, errors.New("native locked Pi RPC does not support direct mail")
	}
	return source.RecoverManagedMailbox(ctx, attempt)
}

type nativeLifecycleGatedRPCFactory struct {
	base                managed.RPCFactory
	gate                *nativeLifecycleRestartGate
	failReadinessOnOpen int
	mu                  sync.Mutex
	opens               int
}

func (f *nativeLifecycleGatedRPCFactory) Open(ctx context.Context, process managed.Process) (managed.RPC, error) {
	rpc, err := f.base.Open(ctx, process)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.opens++
	failReadiness := f.failReadinessOnOpen == f.opens
	f.mu.Unlock()
	return nativeLifecycleGatedRPC{RPC: rpc, gate: f.gate, failReadiness: failReadiness}, nil
}

type nativeLifecycleTrackedProcess struct {
	managed.Process
	mu        sync.Mutex
	stopCalls int
}

func (p *nativeLifecycleTrackedProcess) Stop(ctx context.Context) error {
	p.mu.Lock()
	p.stopCalls++
	p.mu.Unlock()
	return p.Process.Stop(ctx)
}

func (p *nativeLifecycleTrackedProcess) stops() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopCalls
}

type nativeLifecycleTrackingFactory struct {
	base      managed.ProcessFactory
	mu        sync.Mutex
	specs     []managed.CommandSpec
	processes []*nativeLifecycleTrackedProcess
}

func (f *nativeLifecycleTrackingFactory) Start(ctx context.Context, spec managed.CommandSpec) (managed.Process, error) {
	process, err := f.base.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	tracked := &nativeLifecycleTrackedProcess{Process: process}
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.processes = append(f.processes, tracked)
	f.mu.Unlock()
	return tracked, nil
}

func (f *nativeLifecycleTrackingFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.specs)
}

func (f *nativeLifecycleTrackingFactory) spec(index int) managed.CommandSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[index]
}

func (f *nativeLifecycleTrackingFactory) process(index int) *nativeLifecycleTrackedProcess {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.processes[index]
}

type nativeLifecycleRecordingOps struct {
	base    managed.ProcessGroupOps
	mu      sync.Mutex
	signals []managed.ProcessIdentity
}

func (o *nativeLifecycleRecordingOps) Probe(identity managed.ProcessIdentity) managed.ProcessProbe {
	return o.base.Probe(identity)
}
func (o *nativeLifecycleRecordingOps) CloseProcessInput(process managed.Process) error {
	return o.base.CloseProcessInput(process)
}
func (o *nativeLifecycleRecordingOps) SignalVerifiedProcessGroup(identity managed.ProcessIdentity, signal syscall.Signal) error {
	o.mu.Lock()
	o.signals = append(o.signals, identity)
	o.mu.Unlock()
	return o.base.SignalVerifiedProcessGroup(identity, signal)
}
func (o *nativeLifecycleRecordingOps) WaitForProcessGroupEmpty(ctx context.Context, identity managed.ProcessIdentity, done <-chan error) managed.ProcessProbe {
	return o.base.WaitForProcessGroupEmpty(ctx, identity, done)
}
func (o *nativeLifecycleRecordingOps) signalsFor(identity managed.ProcessIdentity) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for _, signaled := range o.signals {
		if signaled == identity {
			count++
		}
	}
	return count
}

// This actual-router proof pauses after the launch reservation and before Pi
// starts. The test edits only its temporary database row, so production keeps
// no policy seed surface or route.
func seedNativeReadinessPolicy(ctx context.Context, path, agentID string) error {
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
	rollback := func(err error) error { _, _ = db.ExecContext(context.Background(), "ROLLBACK"); return err }
	result, err := db.ExecContext(ctx, `UPDATE managed_readiness_sources
		SET desired_policy_canonical = ?, desired_policy_sha256 = ?
		WHERE agent_id = ? AND pi_session_id IS NULL AND applied_policy_canonical IS NULL AND snapshot_id IS NULL`, []byte(nativeSeedPolicy), nativeSeedPolicyHash, agentID)
	if err != nil {
		return rollback(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return rollback(err)
		}
		return rollback(sql.ErrNoRows)
	}
	_, err = db.ExecContext(ctx, "COMMIT")
	return err
}

func commandOutput(t *testing.T, directory string, command string, args ...string) string {
	t.Helper()
	result := exec.Command(command, args...)
	result.Dir = directory
	output, err := result.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(output))
}

// nativeDirectCommandCounter observes JSONL accepted by the installed Pi
// process's stdin. Counting after the underlying write succeeds keeps the
// artifact tied to commands that reached the actual Pi harness.
type nativeDirectCommandCounter struct {
	mu      sync.Mutex
	pending []byte
	count   int
}

func (c *nativeDirectCommandCounter) observe(written []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = append(c.pending, written...)
	for {
		lineEnd := bytes.IndexByte(c.pending, '\n')
		if lineEnd < 0 {
			return
		}
		line := c.pending[:lineEnd]
		c.pending = c.pending[lineEnd+1:]
		var command struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(line, &command) == nil && command.Type == "prompt" && strings.HasPrefix(command.Message, "/mecha-mailbox-deliver-v1 ") {
			c.count++
		}
	}
}

func (c *nativeDirectCommandCounter) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

type nativeDirectCommandWriter struct {
	target  io.WriteCloser
	counter *nativeDirectCommandCounter
}

func (w *nativeDirectCommandWriter) Write(value []byte) (int, error) {
	written, err := w.target.Write(value)
	if written > 0 {
		w.counter.observe(value[:written])
	}
	return written, err
}

func (w *nativeDirectCommandWriter) Close() error { return w.target.Close() }

// nativeObservedRPCFactory retains the production RPC client and installed Pi
// process. It adds only a post-write observation point for the native proof.
type nativeObservedRPCFactory struct{ counter *nativeDirectCommandCounter }

func (f nativeObservedRPCFactory) Open(_ context.Context, process managed.Process) (managed.RPC, error) {
	if process == nil || process.Stdin() == nil || process.Stdout() == nil {
		return nil, errors.New("managed process pipes are unavailable")
	}
	return managed.NewRPCClient(&nativeDirectCommandWriter{target: process.Stdin(), counter: f.counter}, process.Stdout(), func() error { return nil }), nil
}

var managedDirectCandidatePaths = []string{
	"router-proxy/internal/store/migrations/026_managed_direct_delivery.sql",
	"router-proxy/internal/store/store_test.go",
	"router-proxy/internal/store/delivery.go",
	"router-proxy/internal/store/managed.go",
	"router-proxy/internal/store/store.go",
	"router-proxy/internal/store/delivery_test.go",
	"router-proxy/internal/store/managed_test.go",
	"router-proxy/internal/store/expiry_test.go",
	"router-proxy/internal/managed/rpc.go",
	"router-proxy/internal/managed/rpc_test.go",
	"router-proxy/internal/managed/supervisor.go",
	"router-proxy/internal/managed/supervisor_test.go",
	"router-proxy/internal/transport/http.go",
	"router-proxy/internal/transport/live.go",
	"router-proxy/internal/transport/router.go",
	"router-proxy/internal/transport/websocket.go",
	"router-proxy/internal/transport/ws_lifecycle.go",
	"router-proxy/internal/transport/transport_test.go",
	"router-proxy/internal/transport/tmux_test.go",
	"router-proxy/internal/transport/managed_native_test.go",
}

// managedDirectCandidateContentSHA256 identifies the exact 20-path direct-mail
// candidate the native router executed, including recovery and diagnostics.
func managedDirectCandidateContentSHA256(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	for _, relativePath := range managedDirectCandidatePaths {
		content, err := os.ReadFile(filepath.Join(root, relativePath))
		require.NoError(t, err)
		fileHash := sha256.Sum256(content)
		_, err = fmt.Fprintf(hash, "%s\x00%x\n", relativePath, fileHash)
		require.NoError(t, err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeNativeManagedDirectArtifact(t *testing.T, root, pi, mecha, subagents string, manifest managed.Manifest, snapshotID, ownerID, proof string, directCommandCounter *nativeDirectCommandCounter) {
	t.Helper()
	directCommands := directCommandCounter.Count()
	wantDirectCommands := 1
	if proof == "recovery" {
		wantDirectCommands = 2
	}
	require.Equal(t, wantDirectCommands, directCommands, "native proof direct command count")
	var random [8]byte
	_, err := rand.Read(random[:])
	require.NoError(t, err)
	head := commandOutput(t, root, "git", "rev-parse", "HEAD")
	tree := commandOutput(t, root, "git", "rev-parse", "HEAD^{tree}")
	candidate := managedDirectCandidateContentSHA256(t, root)
	piRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(subagents))))
	artifact := map[string]any{
		"protocol": "mecha.phase2.managed-direct-mail/v1", "proof": proof, "result": "pass",
		"mecha_base_head": head, "mecha_base_tree": tree,
		"mecha_candidate_identity_algorithm": "sha256-v1:path-NUL-file-sha256-LF",
		"mecha_candidate_content_sha256":     candidate, "mecha_candidate_paths": managedDirectCandidatePaths,
		"manifest_revision": manifest.Revision, "manifest_sha256": manifest.CanonicalSHA256,
		"pi_executable": pi, "pi_version": commandOutput(t, root, pi, "--version"),
		"mecha_rpc_source": mecha, "pi_subagents_entry": subagents,
		"pi_packages_remote":      commandOutput(t, piRoot, "git", "remote", "get-url", "origin"),
		"pi_packages_ref":         commandOutput(t, piRoot, "git", "branch", "--show-current"),
		"pi_packages_commit":      commandOutput(t, piRoot, "git", "rev-parse", "HEAD"),
		"synthetic_policy_sha256": nativeSeedPolicyHash, "synthetic_snapshot_id": snapshotID, "synthetic_owner_session_id": ownerID,
		"direct_commands": directCommands, "managed_websocket_registrations": 0, "managed_mailbox_leases": 0, "managed_tmux_receipt_frames": 0,
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	path := filepath.Join("/tmp", "mecha-rpc-acceptance", head, "phase2-managed-direct-mail", candidate, hex.EncodeToString(random[:]), "managed-direct-mail-v1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(written), "MECHA_")
	require.Contains(t, string(written), nativeSeedPolicyHash)
	require.Contains(t, string(written), candidate)
}

type nativeLifecycleRestartRecoveryMatrix struct {
	unmarkedRecoveryStarts int
	markedRecoveryStarts   int
	markedRecoverySignals  int
	markedTargetStopCalls  int
}

// writeNativeLifecycleRestartArtifact writes redacted evidence from the exact
// locked-Pi S3 recovery matrix. It records only the matrix outcomes and lock
// identity; it never copies session contents or environment.
func writeNativeLifecycleRestartArtifact(t *testing.T, root string, lockedPi managedResetLockedPi, matrix nativeLifecycleRestartRecoveryMatrix) {
	t.Helper()
	require.Equal(t, 1, matrix.unmarkedRecoveryStarts)
	require.Zero(t, matrix.markedRecoveryStarts)
	require.Zero(t, matrix.markedRecoverySignals)
	require.Zero(t, matrix.markedTargetStopCalls)
	var random [8]byte
	_, err := rand.Read(random[:])
	require.NoError(t, err)
	head := commandOutput(t, root, "git", "rev-parse", "HEAD")
	paths := []string{
		"router-proxy/internal/store/managed_restart_s3.go",
		"router-proxy/internal/store/managed_lifecycle_s0.go",
		"router-proxy/internal/managed/supervisor_restart_s3.go",
		"router-proxy/internal/managed/supervisor.go",
		"router-proxy/internal/managed/supervisor_reset_s1.go",
		"router-proxy/internal/managed/session_artifacts.go",
		"router-proxy/internal/managed/process.go",
		"router-proxy/internal/transport/managed_http.go",
		"router-proxy/internal/transport/managed_native_test.go",
	}
	candidate := managedCandidateContentSHA256(t, root, paths)
	artifact := map[string]any{
		"protocol": "mecha.phase2.lifecycle-restart-s3/v1", "result": "pass",
		"mecha_base_head": head, "candidate_content_sha256": candidate, "candidate_paths": paths,
		"locked_pi": map[string]string{
			"package_name": lockedPi.packageName, "package_version": lockedPi.packageVersion,
			"package_path_sha256": lockedPi.packagePathSHA256, "executable_path_sha256": lockedPi.executablePathSHA256,
			"executable_sha256": lockedPi.executableSHA256, "source_lock_sha256": lockedPi.sourceLockSHA256,
			"source_commit": lockedPi.sourceCommit, "source_tree": lockedPi.sourceTree,
			"source_package_lock_sha256": lockedPi.sourcePackageLockSHA256,
		},
		"recovery_matrix": map[string]int{
			"unmarked_recovery_starts": matrix.unmarkedRecoveryStarts,
			"marked_recovery_starts":   matrix.markedRecoveryStarts,
			"marked_recovery_signals":  matrix.markedRecoverySignals,
			"marked_target_stop_calls": matrix.markedTargetStopCalls,
		},
		"assertions": map[string]bool{
			"g4_lock_verified": true, "locked_executable": true, "no_path_fallback": true,
			"unmarked_target_recovers_once": matrix.unmarkedRecoveryStarts == 1,
			"marked_target_fails_closed":    matrix.markedRecoveryStarts == 0 && matrix.markedRecoverySignals == 0 && matrix.markedTargetStopCalls == 0,
		},
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	path := filepath.Join("/tmp", "mecha-rpc-acceptance", head, "phase2-lifecycle-restart-s3", candidate, hex.EncodeToString(random[:]), "lifecycle-restart-s3-v1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(written), "MECHA_")
	require.Contains(t, string(written), candidate)
	require.Contains(t, string(written), `"unmarked_target_recovers_once": true`)
	require.Contains(t, string(written), `"marked_target_fails_closed": true`)
}

func managedCandidateContentSHA256(t *testing.T, root string, paths []string) string {
	t.Helper()
	hash := sha256.New()
	for _, relativePath := range paths {
		content, err := os.ReadFile(filepath.Join(root, relativePath))
		require.NoError(t, err)
		fileHash := sha256.Sum256(content)
		_, err = fmt.Fprintf(hash, "%s\x00%x\n", relativePath, fileHash)
		require.NoError(t, err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func newLockedLifecycleRestartManager(t *testing.T, durable *store.Store, root string, lockedPi managedResetLockedPi, home, sessionRoot string, processFactory managed.ProcessFactory, rpcFactory managed.RPCFactory, ops managed.ProcessGroupOps, hook func(context.Context, string, managed.Process) error) *managed.Manager {
	t.Helper()
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store:    durable,
		Config:   managed.Config{Enabled: true, PiExecutable: lockedPi.executable, ManagedHome: home, PiAgentDir: filepath.Join(home, ".pi", "agent"), SessionRoot: sessionRoot, RouterURL: "http://127.0.0.1:18775", ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts"), PiSubagentsExtension: lockedPi.subagentsEntry, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
		Manifest: manifest, ProcessFactory: processFactory, RPCFactory: rpcFactory,
		CatalogSource:   managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupOps: ops, AfterManagedLifecycleRestartTargetStartBeforeBind: hook,
	})
	require.NoError(t, err)
	return manager
}

func startLockedLifecycleRestartSource(t *testing.T, root string, lockedPi managedResetLockedPi, agentID string, gate *nativeLifecycleRestartGate, failReadinessOnOpen int, hook func(context.Context, string, managed.Process) error) (*store.Store, *managed.Manager, *nativeLifecycleTrackingFactory, *nativeLifecycleRecordingOps, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	home := t.TempDir()
	sessionRoot := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700))
	tracking := &nativeLifecycleTrackingFactory{base: managed.DefaultProcessFactory()}
	blocking := &nativeBlockingProcessFactory{base: tracking, entered: make(chan struct{}), release: make(chan struct{})}
	op := &nativeLifecycleRecordingOps{base: managed.DefaultProcessGroupOps()}
	rpcFactory := &nativeLifecycleGatedRPCFactory{base: nativeObservedRPCFactory{counter: &nativeDirectCommandCounter{}}, gate: gate, failReadinessOnOpen: failReadinessOnOpen}
	manager := newLockedLifecycleRestartManager(t, durable, root, lockedPi, home, sessionRoot, blocking, rpcFactory, op, hook)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	_, err = manager.Launch(ctx, managed.LaunchRequest{Name: agentID, Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174091"})
	require.NoError(t, err)
	select {
	case <-blocking.entered:
	case <-ctx.Done():
		t.Fatal("locked Pi launch did not reach readiness seed boundary")
	}
	require.NoError(t, seedNativeReadinessPolicy(ctx, databasePath, agentID))
	close(blocking.release)
	readyDeadline := time.Now().Add(30 * time.Second)
	for {
		agent, readErr := durable.ManagedAgent(ctx, agentID)
		if readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts {
			break
		}
		if time.Now().After(readyDeadline) {
			require.NoError(t, readErr)
			t.Fatalf("locked Pi source did not become ready: lifecycle=%s activity=%s code=%s message=%s", agent.LifecycleState, agent.ActivityState, agent.LastErrorCode, agent.LastErrorMessage)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// A settled direct-mail append materializes the same Pi session file that
	// ResumeExisting must later retain. This stays on the installed Pi path.
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	t.Cleanup(func() { _ = router.Close(context.Background()) })
	httpURL, _ := router.URLs()
	messageID := "native-s3-materialize-" + agentID
	body, err := json.Marshal(map[string]string{"id": messageID, "from": "native-s3", "to": agentID, "body": "materialize", "created_at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	response, err := http.Post(httpURL+"/v1/messages", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.Eventually(t, func() bool {
		message, messageErr := durable.MessageFor(ctx, messageID)
		return messageErr == nil && message == nil
	}, 30*time.Second, 100*time.Millisecond)
	return durable, manager, tracking, op, home, sessionRoot
}

func runLockedLifecycleRestartRecoveryMatrix(t *testing.T, root string, lockedPi managedResetLockedPi) nativeLifecycleRestartRecoveryMatrix {
	t.Helper()
	const unmarkedAgent = "native-s3-unmarked"
	gate := &nativeLifecycleRestartGate{entered: make(chan struct{}), release: make(chan struct{})}
	durable, manager, tracking, ops, home, sessionRoot := startLockedLifecycleRestartSource(t, root, lockedPi, unmarkedAgent, gate, 0, nil)
	// Hold source artifact discovery until Close has set its closed flag. The
	// lifecycle worker then completes source_stop but must leave unmarked
	// target_start for the recovered manager to launch exactly once.
	gate.enable()
	unmarkedRequest := managed.RestartRequest{AgentID: unmarkedAgent, Method: http.MethodPost, CanonicalPath: "/v1/managed-agents/" + unmarkedAgent + "/restart", RequestID: "123e4567-e89b-42d3-a456-426614174092", CanonicalBodyHash: "native-s3-unmarked"}
	_, err := manager.ReserveRestart(context.Background(), unmarkedRequest)
	require.NoError(t, err)
	select {
	case <-gate.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("locked Pi source did not reach restart artifact boundary")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close(context.Background()) }()
	// Close sets its cancellation state before it waits for this held
	// preparation. Yield it one scheduling interval, then let the lifecycle
	// worker finish source_stop and observe that state.
	time.Sleep(20 * time.Millisecond)
	close(gate.release)
	require.NoError(t, <-closeDone)
	deadline := time.Now().Add(30 * time.Second)
	for {
		action, actionErr := durable.ManagedLifecycleAction(context.Background(), unmarkedRequest.RequestID)
		if actionErr == nil && action.State == "starting_target" && action.Stage == "target_start" && action.RecoveryOwner == "" {
			break
		}
		if time.Now().After(deadline) {
			require.NoError(t, actionErr)
			t.Fatalf("locked Pi unmarked recovery setup did not reach target_start: state=%s stage=%s code=%s message=%s marker=%s", action.State, action.Stage, action.TerminalCode, action.TerminalMessage, action.RecoveryOwner)
		}
		time.Sleep(100 * time.Millisecond)
	}
	unmarked, err := durable.ManagedLifecycleAction(context.Background(), unmarkedRequest.RequestID)
	require.NoError(t, err)
	require.Equalf(t, "starting_target", unmarked.State, "terminal=%s message=%s stage=%s", unmarked.TerminalCode, unmarked.TerminalMessage, unmarked.Stage)
	require.Equal(t, "target_start", unmarked.Stage)
	require.Empty(t, unmarked.RecoveryOwner)
	recovered := newLockedLifecycleRestartManager(t, durable, root, lockedPi, home, sessionRoot, tracking, nativeObservedRPCFactory{counter: &nativeDirectCommandCounter{}}, ops, nil)
	require.NoError(t, recovered.RecoverManagedLifecycleControls(context.Background()))
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(context.Background(), unmarked.ActionID)
		return actionErr == nil && action.State == "succeeded" && action.TerminalCode == "RESTARTED"
	}, 30*time.Second, 100*time.Millisecond)
	unmarkedRecoveryStarts := tracking.count() - 1
	require.Equal(t, 1, unmarkedRecoveryStarts)
	require.Equal(t, managed.ResumeExisting, tracking.spec(1).LaunchMode)
	_ = recovered.Close(context.Background())

	const markedAgent = "native-s3-marked"
	markedGate := &nativeLifecycleRestartGate{entered: make(chan struct{}), release: make(chan struct{})}
	hookEntered := make(chan struct{})
	hookRelease := make(chan struct{})
	hook := func(context.Context, string, managed.Process) error {
		close(hookEntered)
		<-hookRelease
		return errors.New("native S3 post-start pre-bind interruption")
	}
	markedStore, markedManager, markedTracking, markedOps, markedHome, markedSessionRoot := startLockedLifecycleRestartSource(t, root, lockedPi, markedAgent, markedGate, 0, hook)
	markedRequest := managed.RestartRequest{AgentID: markedAgent, Method: http.MethodPost, CanonicalPath: "/v1/managed-agents/" + markedAgent + "/restart", RequestID: "123e4567-e89b-42d3-a456-426614174093", CanonicalBodyHash: "native-s3-marked"}
	_, err = markedManager.ReserveRestart(context.Background(), markedRequest)
	require.NoError(t, err)
	select {
	case <-hookEntered:
	case <-time.After(30 * time.Second):
		t.Fatal("locked Pi restart did not reach post-start pre-bind boundary")
	}
	marked, err := markedStore.ManagedLifecycleAction(context.Background(), markedRequest.RequestID)
	require.NoError(t, err)
	require.Equal(t, "starting_target", marked.State)
	require.Equal(t, "target_start", marked.Stage)
	require.True(t, strings.HasPrefix(marked.RecoveryOwner, store.RestartStartAttemptPrefix))
	target := markedTracking.process(1)
	startsBeforeRecovery := markedTracking.count()
	markedRecovery := newLockedLifecycleRestartManager(t, markedStore, root, lockedPi, markedHome, markedSessionRoot, markedTracking, nativeObservedRPCFactory{counter: &nativeDirectCommandCounter{}}, markedOps, nil)
	require.NoError(t, markedRecovery.RecoverManagedLifecycleControls(context.Background()))
	recoveredMarked, err := markedStore.ManagedLifecycleAction(context.Background(), marked.ActionID)
	require.NoError(t, err)
	require.Equal(t, "failed", recoveredMarked.State)
	require.Equal(t, "RESTART_IDENTITY_UNSAFE", recoveredMarked.TerminalCode)
	markedRecoveryStarts := markedTracking.count() - startsBeforeRecovery
	markedSignals := markedOps.signalsFor(target.Identity())
	markedStops := target.stops()
	require.Zero(t, markedRecoveryStarts)
	require.Zero(t, markedSignals)
	require.Zero(t, markedStops)
	close(hookRelease)
	require.NoError(t, markedManager.Close(context.Background()))
	require.NoError(t, target.Stop(context.Background()), "test cleanup occurs after the zero-stop proof")
	_ = markedRecovery.Close(context.Background())
	return nativeLifecycleRestartRecoveryMatrix{unmarkedRecoveryStarts: unmarkedRecoveryStarts, markedRecoveryStarts: markedRecoveryStarts, markedRecoverySignals: markedSignals, markedTargetStopCalls: markedStops}
}

func TestLockedLifecycleRestartRecoveryMatrix(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	lockedPi := requireManagedResetLockedPi(t, root)
	matrix := runLockedLifecycleRestartRecoveryMatrix(t, root, lockedPi)
	require.Equal(t, 1, matrix.unmarkedRecoveryStarts)
	require.Zero(t, matrix.markedRecoveryStarts)
	require.Zero(t, matrix.markedRecoverySignals)
	require.Zero(t, matrix.markedTargetStopCalls)
	writeNativeLifecycleRestartArtifact(t, root, lockedPi, matrix)
}

func TestListenRecoversManagedRestartTransitionsBeforeBind(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, durable.Close())
	require.Error(t, router.Listen(ctx))
	require.Nil(t, router.listener, "recovery failure must happen before a listener is reachable")
}

func TestManagedDirectMailboxDelivery(t *testing.T) {
	runManagedDirectMailboxNativeProof(t, false, false)
}

func TestManagedDirectMailboxEmptyBody(t *testing.T) {
	runManagedDirectMailboxNativeProof(t, false, true)
}

func TestManagedDirectMailboxRecovery(t *testing.T) {
	runManagedDirectMailboxNativeProof(t, true, false)
}

func runManagedDirectMailboxNativeProof(t *testing.T, recovery, emptyBody bool) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	lockedPi := requireManagedResetLockedPi(t, root)
	pi, subagents := lockedPi.executable, lockedPi.subagentsEntry
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	mecha := filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts")
	factory := &nativeBlockingProcessFactory{base: managed.DefaultProcessFactory(), entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-factory.release:
		default:
			close(factory.release)
		}
	}()
	var appendBeforeAck chan struct{}
	var interruptAppend sync.Once
	options := managed.ManagerOptions{
		Store: durable,
		BeforeManagedDirectAcknowledge: func(context.Context, store.ManagedDirectAttempt) error {
			if !recovery {
				return nil
			}
			interrupted := false
			interruptAppend.Do(func() {
				close(appendBeforeAck)
				interrupted = true
			})
			if interrupted {
				return errors.New("native append-before-ack interruption")
			}
			return nil
		},
		Config: managed.Config{Enabled: true, PiExecutable: pi, ManagedHome: home, PiAgentDir: filepath.Join(home, ".pi", "agent"), SessionRoot: sessionRoot, RouterURL: "http://127.0.0.1:18775",
			ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: mecha,
			PiSubagentsExtension: subagents, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
		Manifest: manifest, ProcessFactory: factory,
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
	}
	if recovery {
		appendBeforeAck = make(chan struct{})
	}
	directCommandCounter := &nativeDirectCommandCounter{}
	options.RPCFactory = nativeObservedRPCFactory{counter: directCommandCounter}
	manager, err := managed.NewManager(options)
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer func() { _ = router.Close(context.Background()) }()
	httpURL, _ := router.URLs()
	launchBody, err := json.Marshal(map[string]string{"name": "native-alpha", "cwd": root})
	require.NoError(t, err)
	launchRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/managed-agents", bytes.NewReader(launchBody))
	require.NoError(t, err)
	launchRequest.Header.Set("Content-Type", "application/json")
	launchRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174081")
	launchResponse, err := http.DefaultClient.Do(launchRequest)
	require.NoError(t, err)
	defer launchResponse.Body.Close()
	require.Equal(t, http.StatusAccepted, launchResponse.StatusCode)
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal("process factory did not reach seed boundary")
	}
	require.NoError(t, seedNativeReadinessPolicy(ctx, databasePath, "native-alpha"))
	close(factory.release)
	deadline := time.Now().Add(20 * time.Second)
	for {
		projectionRequest, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+"/v1/managed-agents/native-alpha", nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		projectionResponse, requestErr := http.DefaultClient.Do(projectionRequest)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		var projection struct {
			Agent struct {
				Lifecycle string `json:"lifecycle_state"`
			} `json:"agent"`
		}
		body, _ := io.ReadAll(projectionResponse.Body)
		projectionResponse.Body.Close()
		if json.Unmarshal(body, &projection) == nil && projection.Agent.Lifecycle == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed Pi did not reach source-confirmed ready: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	source, err := durable.ManagedReadinessSource(ctx, "native-alpha")
	require.NoError(t, err)
	require.Equal(t, []byte(nativeSeedPolicy), source.DesiredPolicy.Canonical)
	require.Equal(t, nativeSeedPolicyHash, source.DesiredPolicy.SHA256)
	require.NotNil(t, source.AppliedPolicy)
	require.Equal(t, source.DesiredPolicy, *source.AppliedPolicy)
	require.NotNil(t, source.Snapshot)
	require.NotContains(t, string(source.Snapshot.Canonical), "context_ref")
	recipientSource, err := durable.RecipientSource(ctx, "native-alpha")
	require.NoError(t, err)
	require.Equal(t, "managed", recipientSource.SourceKind)
	require.Equal(t, "active", recipientSource.State)
	agent, err := durable.ManagedAgent(ctx, "native-alpha")
	require.NoError(t, err)
	require.True(t, agent.AcceptingPrompts)
	require.False(t, agent.InitialInstructionSent, "no model turn occurs before final readiness")
	messageID, body, proof := "native-direct-message", "native direct payload", "delivery"
	if emptyBody {
		messageID, body, proof = "native-empty-message", "", "empty-body"
	}
	if recovery {
		messageID, body, proof = "native-recovery-message", "native recovery payload", "recovery"
	}
	messageBody, err := json.Marshal(map[string]string{"id": messageID, "from": "native-sender", "to": "native-alpha", "body": body, "created_at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	messageRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/messages", bytes.NewReader(messageBody))
	require.NoError(t, err)
	messageRequest.Header.Set("Content-Type", "application/json")
	messageResponse, err := http.DefaultClient.Do(messageRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, messageResponse.StatusCode)
	messageResponse.Body.Close()
	target, err := durable.ResolveMessageTarget(ctx, messageID)
	require.NoError(t, err)
	require.NotNil(t, target)
	require.Equal(t, "managed", target.SourceKind)

	if recovery {
		// The hook interrupts only the old-fence ACK. Recovery may offer the
		// message once under the replacement fence, then must settle that exact
		// replacement attempt without a further prompt.
		select {
		case <-appendBeforeAck:
		case <-ctx.Done():
			t.Fatal("installed Pi did not reach the append-before-ack boundary")
		}
		interrupted, err := durable.ManagedDirectAttempt(ctx, messageID, 1)
		require.NoError(t, err)
		require.Equal(t, "direct_prompted", interrupted.State)
		require.NotEmpty(t, interrupted.SessionEntryWatermark)
		oldGeneration, oldSession, oldEpoch := agent.RuntimeGeneration, agent.PiSessionID, agent.SessionEpoch
		restartRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/managed-agents/native-alpha/restart", strings.NewReader(`{}`))
		require.NoError(t, err)
		restartRequest.Header.Set("Content-Type", "application/json")
		restartRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174082")
		restartResponse, err := http.DefaultClient.Do(restartRequest)
		require.NoError(t, err)
		defer restartResponse.Body.Close()
		require.Equal(t, http.StatusAccepted, restartResponse.StatusCode)
		deadline = time.Now().Add(20 * time.Second)
		for {
			agent, err = durable.ManagedAgent(ctx, "native-alpha")
			if err == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts && agent.RuntimeGeneration != oldGeneration {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("managed Pi did not complete controlled replacement: %+v", agent)
			}
			time.Sleep(100 * time.Millisecond)
		}
		restartAction, err := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174082")
		require.NoError(t, err)
		require.Equal(t, "lifecycle", restartAction.ExecutionOwner)
		require.Equal(t, "succeeded", restartAction.State)
		require.Equal(t, "RESTARTED", restartAction.TerminalCode)
		require.NotNil(t, restartAction.SourceFence)
		require.NotNil(t, restartAction.TargetFence)
		require.Equal(t, oldSession, restartAction.SourceFence.PiSessionID)
		require.Equal(t, oldEpoch, restartAction.SourceFence.SessionEpoch)
		require.Equal(t, oldSession, restartAction.TargetFence.PiSessionID)
		require.Equal(t, oldEpoch, restartAction.TargetFence.SessionEpoch)
		require.NotEmpty(t, restartAction.TargetArtifactID)
		artifact, artifactErr := durable.ManagedSessionArtifact(ctx, restartAction.TargetArtifactID)
		require.NoError(t, artifactErr)
		require.Equal(t, "materialized", artifact.DiscoveryState)
		require.Equal(t, "retained", artifact.CleanupState)
		_, legacyErr := durable.ManagedRestartTransition(ctx, "native-alpha")
		require.Error(t, legacyErr, "a new direct restart must not create a 025 row")
	} else {
		deadline = time.Now().Add(20 * time.Second)
		for {
			message, messageErr := durable.MessageFor(ctx, messageID)
			if messageErr == nil && message == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("managed Pi did not append and acknowledge direct mail: row=%+v error=%v", message, messageErr)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		message, messageErr := durable.MessageFor(ctx, messageID)
		if messageErr == nil && message == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed Pi did not settle native direct mail: row=%+v error=%v", message, messageErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	source, err = durable.ManagedReadinessSource(ctx, "native-alpha")
	require.NoError(t, err)
	require.NotNil(t, source.Fence)
	require.Equal(t, agent.RuntimeGeneration, source.Fence.RuntimeGeneration)
	require.NotNil(t, source.AppliedPolicy)
	require.NotNil(t, source.Snapshot)
	require.False(t, agent.InitialInstructionSent, "restart must not replay launch-only input")
	legacyResponse, err := http.Get(httpURL + "/v1/agents")
	require.NoError(t, err)
	defer legacyResponse.Body.Close()
	var legacy struct {
		Agents []map[string]any `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(legacyResponse.Body).Decode(&legacy))
	require.Empty(t, legacy.Agents)
	lease, err := durable.LeaseFor(ctx, "native-alpha")
	require.NoError(t, err)
	require.Nil(t, lease, "managed direct delivery never acquires a Mailbox lease")
	removedRoute, err := http.Post(httpURL+"/v1/ui-session", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer removedRoute.Body.Close()
	require.Equal(t, http.StatusNotFound, removedRoute.StatusCode)
	writeNativeManagedDirectArtifact(t, root, pi, mecha, subagents, manifest, source.Snapshot.SnapshotID, source.Snapshot.OwnerSessionID, proof, directCommandCounter)
}
