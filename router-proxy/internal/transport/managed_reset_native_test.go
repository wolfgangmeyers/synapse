package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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

const managedResetG4SourceLock = "/private/tmp/rpc-phase2-pi-g4-source-lock.json"

type managedResetG4Lock struct {
	Schema string `json:"schema"`
	Pi     struct {
		SourceRoot   string `json:"source_root"`
		SourceRemote string `json:"source_remote"`
		PublicBase   struct {
			Commit string `json:"commit"`
			Tree   string `json:"tree"`
		} `json:"public_base"`
		Scoped struct {
			Commit            string   `json:"commit"`
			Tree              string   `json:"tree"`
			MergeBase         string   `json:"merge_base"`
			Clean             bool     `json:"clean"`
			ChangedPaths      []string `json:"changed_paths"`
			PackageLockSHA256 string   `json:"package_lock_sha256"`
			PackageName       string   `json:"package_name"`
			PackageVersion    string   `json:"package_version"`
		} `json:"scoped"`
		Tarball struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"tarball"`
		Installed struct {
			PackageRealPath    string `json:"package_real_path"`
			ExecutableRealPath string `json:"executable_real_path"`
			ExecutableSHA256   string `json:"executable_sha256"`
			PackageVersion     string `json:"package_version"`
		} `json:"installed"`
	} `json:"pi"`
	PiPackages struct {
		Path   string `json:"path"`
		Commit string `json:"commit"`
		Tree   string `json:"tree"`
	} `json:"pi_packages"`
}

type managedResetLockedPi struct {
	executable, packageRoot, packageName, packageVersion                string
	executableSHA256, executablePathSHA256, packagePathSHA256           string
	sourceCommit, sourceTree, sourceLockSHA256, sourcePackageLockSHA256 string
	subagentsEntry, piPackagesCommit, piPackagesTree                    string
}

func requireManagedResetLockedPi(t *testing.T, root string) managedResetLockedPi {
	t.Helper()
	return requireManagedResetLockedPiFromLock(t, root, managedResetG4SourceLock)
}

// requireManagedResetLockedPiFromLock verifies a caller-selected G4 lock. The
// S4 proof uses this path so its command-line lock cannot be silently ignored.
func requireManagedResetLockedPiFromLock(t *testing.T, root, sourceLockPath string) managedResetLockedPi {
	t.Helper()
	contents, err := os.ReadFile(sourceLockPath)
	require.NoError(t, err, "the accepted G4 source lock is required for native proof")
	lockSHA := sha256.Sum256(contents)
	var lock managedResetG4Lock
	require.NoError(t, json.Unmarshal(contents, &lock))
	require.Equal(t, "mecha.pi.g4-source-lock/v1", lock.Schema)
	require.Equal(t, "107d79f11072bbc8a3a757ed7fd69596bee7d68c", lock.Pi.PublicBase.Commit)
	require.Equal(t, "f5103239060686ea2983e18906857b3df54428f5", lock.Pi.PublicBase.Tree)
	require.Equal(t, lock.Pi.PublicBase.Commit, lock.Pi.Scoped.MergeBase)
	require.True(t, lock.Pi.Scoped.Clean)
	require.NotEmpty(t, lock.Pi.SourceRoot)
	require.NotEmpty(t, lock.Pi.Installed.PackageRealPath)
	require.NotEmpty(t, lock.Pi.Installed.ExecutableRealPath)
	require.NotEmpty(t, lock.Pi.Scoped.PackageLockSHA256)
	require.NotEmpty(t, lock.Pi.Tarball.Path)
	require.Regexp(t, `^[a-f0-9]{64}$`, lock.Pi.Tarball.SHA256)
	require.NotEmpty(t, lock.Pi.Scoped.ChangedPaths)
	for _, changedPath := range lock.Pi.Scoped.ChangedPaths {
		require.False(t, filepath.IsAbs(changedPath))
		require.False(t, strings.HasPrefix(changedPath, "../"))
		require.True(t, strings.HasPrefix(changedPath, "packages/coding-agent/src/") || strings.HasPrefix(changedPath, "packages/coding-agent/test/"), "G4 lock path escapes the approved coding-agent scope: %s", changedPath)
	}

	sourceRoot, err := filepath.EvalSymlinks(lock.Pi.SourceRoot)
	require.NoError(t, err)
	require.Equal(t, lock.Pi.SourceRoot, sourceRoot)
	require.Equal(t, lock.Pi.Scoped.Commit, commandOutput(t, root, "git", "-C", sourceRoot, "rev-parse", "HEAD"))
	require.Equal(t, lock.Pi.Scoped.Tree, commandOutput(t, root, "git", "-C", sourceRoot, "rev-parse", "HEAD^{tree}"))
	require.Equal(t, lock.Pi.PublicBase.Tree, commandOutput(t, root, "git", "-C", sourceRoot, "rev-parse", lock.Pi.PublicBase.Commit+"^{tree}"))
	require.Equal(t, lock.Pi.Scoped.MergeBase, commandOutput(t, root, "git", "-C", sourceRoot, "merge-base", lock.Pi.Scoped.Commit, lock.Pi.PublicBase.Commit))
	require.Empty(t, commandOutput(t, root, "git", "-C", sourceRoot, "status", "--porcelain"), "locked Pi source checkout must be clean")
	require.Equal(t, lock.Pi.SourceRemote, commandOutput(t, root, "git", "-C", sourceRoot, "remote", "get-url", "origin"))
	require.Equal(t, lock.Pi.Scoped.PackageLockSHA256, safeFileSHA256(t, filepath.Join(sourceRoot, "package-lock.json")))
	changed := strings.Fields(commandOutput(t, root, "git", "-C", sourceRoot, "diff", "--name-only", lock.Pi.PublicBase.Commit+".."+lock.Pi.Scoped.Commit))
	require.Equal(t, lock.Pi.Scoped.ChangedPaths, changed, "the G4 lock must name every and only approved source change")
	tarballPath, err := filepath.EvalSymlinks(lock.Pi.Tarball.Path)
	require.NoError(t, err)
	require.Equal(t, lock.Pi.Tarball.Path, tarballPath)
	require.Equal(t, lock.Pi.Tarball.SHA256, safeFileSHA256(t, tarballPath))

	packageRoot, err := filepath.EvalSymlinks(lock.Pi.Installed.PackageRealPath)
	require.NoError(t, err)
	require.Equal(t, lock.Pi.Installed.PackageRealPath, packageRoot)
	executable, err := filepath.EvalSymlinks(lock.Pi.Installed.ExecutableRealPath)
	require.NoError(t, err)
	require.Equal(t, lock.Pi.Installed.ExecutableRealPath, executable)
	relativeExecutable, err := filepath.Rel(packageRoot, executable)
	require.NoError(t, err)
	require.NotEqual(t, "..", relativeExecutable)
	require.False(t, strings.HasPrefix(relativeExecutable, ".."+string(filepath.Separator)))
	require.Equal(t, lock.Pi.Installed.ExecutableSHA256, safeFileSHA256(t, executable))
	packageJSON, err := os.ReadFile(filepath.Join(packageRoot, "package.json"))
	require.NoError(t, err)
	var metadata struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(packageJSON, &metadata))
	require.Equal(t, lock.Pi.Scoped.PackageName, metadata.Name)
	require.Equal(t, lock.Pi.Scoped.PackageVersion, metadata.Version)
	require.Equal(t, lock.Pi.Installed.PackageVersion, metadata.Version)
	require.Equal(t, lock.Pi.Installed.PackageVersion, commandOutput(t, root, executable, "--version"))

	piPackagesRoot, err := filepath.EvalSymlinks(lock.PiPackages.Path)
	require.NoError(t, err)
	require.Equal(t, lock.PiPackages.Path, piPackagesRoot)
	require.Equal(t, lock.PiPackages.Commit, commandOutput(t, root, "git", "-C", piPackagesRoot, "rev-parse", "HEAD"))
	require.Equal(t, lock.PiPackages.Tree, commandOutput(t, root, "git", "-C", piPackagesRoot, "rev-parse", "HEAD^{tree}"))
	require.Empty(t, commandOutput(t, root, "git", "-C", piPackagesRoot, "status", "--porcelain"), "locked pi-packages checkout must be clean")
	subagentsEntry := filepath.Join(piPackagesRoot, "packages", "pi-subagents", "src", "index.ts")
	_, err = os.Stat(subagentsEntry)
	require.NoError(t, err)

	packagePathSHA := sha256.Sum256([]byte(packageRoot))
	executablePathSHA := sha256.Sum256([]byte(executable))
	return managedResetLockedPi{
		executable: executable, packageRoot: packageRoot, packageName: metadata.Name, packageVersion: metadata.Version,
		executableSHA256: lock.Pi.Installed.ExecutableSHA256, executablePathSHA256: hex.EncodeToString(executablePathSHA[:]), packagePathSHA256: hex.EncodeToString(packagePathSHA[:]),
		sourceCommit: lock.Pi.Scoped.Commit, sourceTree: lock.Pi.Scoped.Tree, sourceLockSHA256: hex.EncodeToString(lockSHA[:]), sourcePackageLockSHA256: lock.Pi.Scoped.PackageLockSHA256,
		subagentsEntry: subagentsEntry, piPackagesCommit: lock.PiPackages.Commit, piPackagesTree: lock.PiPackages.Tree,
	}
}

var managedResetCandidatePaths = []string{
	"router-proxy/contract_manifest_test.go",
	"router-proxy/internal/store/managed.go",
	"router-proxy/internal/store/managed_lifecycle_s0.go",
	"router-proxy/internal/store/managed_reset_s1.go",
	"router-proxy/internal/store/managed_reset_s1_test.go",
	"router-proxy/internal/managed/process.go",
	"router-proxy/internal/managed/process_test.go",
	"router-proxy/internal/managed/session_artifacts.go",
	"router-proxy/internal/managed/session_artifacts_test.go",
	"router-proxy/internal/managed/supervisor.go",
	"router-proxy/internal/managed/supervisor_reset_s1.go",
	"router-proxy/internal/managed/supervisor_reset_execution_test.go",
	"router-proxy/internal/managed/supervisor_reset_recovery_test.go",
	"router-proxy/internal/managed/supervisor_reset_retry_test.go",
	"router-proxy/internal/transport/managed_http.go",
	"router-proxy/internal/transport/managed_http_test.go",
	"router-proxy/internal/transport/managed_reset_native_test.go",
	"router-proxy/internal/transport/router.go",
	"router/contract/manifest.json",
	"router/contract/http-outcomes.json",
}

func managedResetCandidateSHA256(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	for _, relative := range managedResetCandidatePaths {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		require.NoError(t, err)
		fileHash := sha256.Sum256(contents)
		_, err = hash.Write([]byte(relative + "\x00" + hex.EncodeToString(fileHash[:]) + "\n"))
		require.NoError(t, err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func safeFileSHA256(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func writeManagedResetNativeArtifact(t *testing.T, root string, lockedPi managedResetLockedPi, before, after *store.ManagedAgentRecord, action *store.ManagedLifecycleAction, sourceCleanupComplete bool, matrix managedResetNativeMatrix) {
	t.Helper()
	var random [8]byte
	_, err := rand.Read(random[:])
	require.NoError(t, err)
	head := commandOutput(t, root, "git", "rev-parse", "HEAD")
	tree := commandOutput(t, root, "git", "rev-parse", "HEAD^{tree}")
	candidate := managedResetCandidateSHA256(t, root)
	artifact := map[string]any{
		"protocol":                 "mecha.phase2.reset-s1/v1",
		"result":                   "pass",
		"mecha_base_head":          head,
		"mecha_base_tree":          tree,
		"candidate_content_sha256": candidate,
		"candidate_paths":          managedResetCandidatePaths,
		"locked_pi": map[string]any{
			"package_name":               lockedPi.packageName,
			"package_version":            lockedPi.packageVersion,
			"package_path_sha256":        lockedPi.packagePathSHA256,
			"executable_path_sha256":     lockedPi.executablePathSHA256,
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
		"assertions": map[string]any{
			"fresh_session":                before.PiSessionID != after.PiSessionID,
			"epoch_increment":              after.SessionEpoch == before.SessionEpoch+1,
			"reset_outcome":                action.State == "succeeded" && action.TerminalCode == "RESET",
			"source_artifact_cleanup":      sourceCleanupComplete,
			"prompt_admission_open":        after.AcceptingPrompts,
			"initial_instruction_replayed": after.InitialInstructionSent,
			"target_epoch":                 after.SessionEpoch,
			"target_start_retry_resume":    matrix.targetStartRetryResume,
			"cleanup_retry_starts":         matrix.cleanupRetryStarts,
			"prebind_recovery_starts":      matrix.prebindRecoveryStarts,
			"prebind_recovery_signals":     matrix.prebindRecoverySignals,
		},
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	path := filepath.Join("/tmp", "mecha-rpc-acceptance", head, "phase2-reset-s1", candidate, hex.EncodeToString(random[:]), "reset-s1-v1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, forbidden := range []string{before.PiSessionID, after.PiSessionID, "MECHA_"} {
		require.NotContains(t, string(written), forbidden)
	}
}

type managedResetNativeMatrix struct {
	targetStartRetryResume bool
	cleanupRetryStarts     int
	prebindRecoveryStarts  int
	prebindRecoverySignals int
}

type nativeResetTrackedProcess struct {
	managed.Process
	mu        sync.Mutex
	stopCalls int
}

func (p *nativeResetTrackedProcess) Stop(ctx context.Context) error {
	p.mu.Lock()
	p.stopCalls++
	p.mu.Unlock()
	return p.Process.Stop(ctx)
}

func (p *nativeResetTrackedProcess) stops() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopCalls
}

type nativeResetCountingFactory struct {
	base        managed.ProcessFactory
	sessionRoot string
	fakeOps     *nativeResetProcessOps
	fakeSource  bool
	mu          sync.Mutex
	specs       []managed.CommandSpec
	trackNext   bool
	tracked     *nativeResetTrackedProcess
}

func (f *nativeResetCountingFactory) Start(ctx context.Context, spec managed.CommandSpec) (managed.Process, error) {
	f.mu.Lock()
	useFakeTarget := f.fakeOps != nil && (f.fakeSource || len(f.specs) > 0)
	f.mu.Unlock()
	var process managed.Process
	var err error
	if useFakeTarget {
		process, err = f.fakeOps.newProcess(spec, f.sessionRoot)
	} else {
		process, err = f.base.Start(ctx, spec)
	}
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if f.trackNext {
		f.trackNext = false
		f.tracked = &nativeResetTrackedProcess{Process: process}
		return f.tracked, nil
	}
	return process, nil
}

func (f *nativeResetCountingFactory) starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.specs)
}

func (f *nativeResetCountingFactory) spec(index int) managed.CommandSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[index]
}

func (f *nativeResetCountingFactory) captureNext() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trackNext = true
}

func (f *nativeResetCountingFactory) captured() *nativeResetTrackedProcess {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tracked
}

type nativeResetNoopWriteCloser struct{}

func (nativeResetNoopWriteCloser) Write(value []byte) (int, error) { return len(value), nil }
func (nativeResetNoopWriteCloser) Close() error                    { return nil }

type nativeResetFakeProcess struct {
	identity    managed.ProcessIdentity
	sessionFile string
	stopped     chan struct{}
	once        sync.Once
}

func (p *nativeResetFakeProcess) Identity() managed.ProcessIdentity { return p.identity }
func (p *nativeResetFakeProcess) Stdin() io.WriteCloser             { return nativeResetNoopWriteCloser{} }
func (p *nativeResetFakeProcess) Stdout() io.ReadCloser             { return io.NopCloser(strings.NewReader("")) }
func (p *nativeResetFakeProcess) Wait() error                       { <-p.stopped; return nil }
func (p *nativeResetFakeProcess) Stop(context.Context) error        { p.reap(); return nil }
func (p *nativeResetFakeProcess) reap()                             { p.once.Do(func() { close(p.stopped) }) }

type nativeResetProcessOps struct {
	base    managed.ProcessGroupOps
	mu      sync.Mutex
	nextPID int
	fake    map[int]*nativeResetFakeProcess
	signals []managed.ProcessIdentity
}

func (o *nativeResetProcessOps) newProcess(spec managed.CommandSpec, sessionRoot string) (managed.Process, error) {
	sessionFile := filepath.Join(sessionRoot, "fake-"+spec.SessionID+".json")
	if err := os.WriteFile(sessionFile, []byte("{}"), 0o600); err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.nextPID++
	process := &nativeResetFakeProcess{identity: managed.ProcessIdentity{PID: o.nextPID, StartMarker: spec.SessionID, Incarnation: "native-reset-fake-" + spec.SessionID}, sessionFile: sessionFile, stopped: make(chan struct{})}
	o.fake[process.identity.PID] = process
	return process, nil
}

func (o *nativeResetProcessOps) fakeProcess(identity managed.ProcessIdentity) *nativeResetFakeProcess {
	o.mu.Lock()
	defer o.mu.Unlock()
	process := o.fake[identity.PID]
	if process == nil || process.identity != identity {
		return nil
	}
	return process
}

func (o *nativeResetProcessOps) Probe(identity managed.ProcessIdentity) managed.ProcessProbe {
	if process := o.fakeProcess(identity); process != nil {
		select {
		case <-process.stopped:
			return managed.ProcessProbe{State: managed.ProcessAbsent}
		default:
			return managed.ProcessProbe{State: managed.ProcessOwned}
		}
	}
	return o.base.Probe(identity)
}

func (o *nativeResetProcessOps) CloseProcessInput(process managed.Process) error {
	if _, ok := process.(*nativeResetFakeProcess); ok {
		return nil
	}
	if _, ok := process.(*nativeResetTrackedProcess); ok {
		return nil
	}
	return o.base.CloseProcessInput(process)
}

func (o *nativeResetProcessOps) SignalVerifiedProcessGroup(identity managed.ProcessIdentity, signal syscall.Signal) error {
	if process := o.fakeProcess(identity); process != nil {
		o.mu.Lock()
		o.signals = append(o.signals, identity)
		o.mu.Unlock()
		process.reap()
		return nil
	}
	return o.base.SignalVerifiedProcessGroup(identity, signal)
}

func (o *nativeResetProcessOps) WaitForProcessGroupEmpty(ctx context.Context, identity managed.ProcessIdentity, done <-chan error) managed.ProcessProbe {
	if process := o.fakeProcess(identity); process != nil {
		process.reap()
		return managed.ProcessProbe{State: managed.ProcessAbsent}
	}
	return o.base.WaitForProcessGroupEmpty(ctx, identity, done)
}

func (o *nativeResetProcessOps) signalCount(identity managed.ProcessIdentity) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for _, signal := range o.signals {
		if signal == identity {
			count++
		}
	}
	return count
}

type nativeResetRPCFactory struct {
	base         managed.RPCFactory
	active       []string
	mu           sync.Mutex
	fakeOpens    int
	failFakeOpen int
}

func (f *nativeResetRPCFactory) Open(ctx context.Context, process managed.Process) (managed.RPC, error) {
	if fake, ok := process.(*nativeResetFakeProcess); ok {
		f.mu.Lock()
		f.fakeOpens++
		fail := f.fakeOpens == f.failFakeOpen
		f.mu.Unlock()
		return &nativeResetFakeRPC{sessionID: fake.Identity().StartMarker, sessionFile: fake.sessionFile, active: append([]string(nil), f.active...), failReadiness: fail}, nil
	}
	if tracked, ok := process.(*nativeResetTrackedProcess); ok {
		if fake, ok := tracked.Process.(*nativeResetFakeProcess); ok {
			f.mu.Lock()
			f.fakeOpens++
			fail := f.fakeOpens == f.failFakeOpen
			f.mu.Unlock()
			return &nativeResetFakeRPC{sessionID: fake.Identity().StartMarker, sessionFile: fake.sessionFile, active: append([]string(nil), f.active...), failReadiness: fail}, nil
		}
	}
	rpc, err := f.base.Open(ctx, process)
	if err != nil {
		return nil, err
	}
	return &nativeResetRPC{RPC: rpc}, nil
}

type nativeResetReadinessRPC interface {
	ApplyPolicy(context.Context, map[string]any) (map[string]any, error)
	WaitForPolicyApplied(context.Context, string, int64) (map[string]any, error)
	RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error)
	WaitForChildSnapshot(context.Context) (map[string]any, error)
}

type nativeResetRPC struct {
	managed.RPC
	failReadiness bool
}

func (r *nativeResetRPC) readinessSource() (nativeResetReadinessRPC, error) {
	source, ok := r.RPC.(nativeResetReadinessRPC)
	if !ok {
		return nil, errors.New("installed Pi RPC does not expose readiness source methods")
	}
	return source, nil
}

func (r *nativeResetRPC) ApplyPolicy(ctx context.Context, value map[string]any) (map[string]any, error) {
	if r.failReadiness {
		return nil, errors.New("injected native target readiness failure")
	}
	source, err := r.readinessSource()
	if err != nil {
		return nil, err
	}
	return source.ApplyPolicy(ctx, value)
}

func (r *nativeResetRPC) WaitForPolicyApplied(ctx context.Context, sessionID string, epoch int64) (map[string]any, error) {
	source, err := r.readinessSource()
	if err != nil {
		return nil, err
	}
	return source.WaitForPolicyApplied(ctx, sessionID, epoch)
}

func (r *nativeResetRPC) RequestChildSnapshot(ctx context.Context, value map[string]any) (map[string]any, error) {
	source, err := r.readinessSource()
	if err != nil {
		return nil, err
	}
	return source.RequestChildSnapshot(ctx, value)
}

func (r *nativeResetRPC) WaitForChildSnapshot(ctx context.Context) (map[string]any, error) {
	source, err := r.readinessSource()
	if err != nil {
		return nil, err
	}
	return source.WaitForChildSnapshot(ctx)
}

// nativeResetFakeRPC is a bounded provider-free replacement used only for the
// failure matrix after the locked Pi has completed the source launch. It
// returns the same fixed readiness protocol shape that the manager validates.
type nativeResetFakeRPC struct {
	sessionID, sessionFile string
	active                 []string
	failReadiness          bool
	policy                 map[string]any
}

func (r *nativeResetFakeRPC) GetState(context.Context) (map[string]any, error) {
	return map[string]any{"sessionId": r.sessionID, "sessionFile": r.sessionFile}, nil
}
func (r *nativeResetFakeRPC) GetCommands(context.Context) ([]string, error) {
	return []string{"mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1", "goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"}, nil
}
func (r *nativeResetFakeRPC) Bootstrap(context.Context, map[string]any) error { return nil }
func (r *nativeResetFakeRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	return nil
}
func (r *nativeResetFakeRPC) GetActiveTools(context.Context) ([]string, error) {
	return append([]string(nil), r.active...), nil
}
func (r *nativeResetFakeRPC) Prompt(context.Context, string) error { return nil }
func (r *nativeResetFakeRPC) Close() error                         { return nil }
func (r *nativeResetFakeRPC) ApplyPolicy(_ context.Context, envelope map[string]any) (map[string]any, error) {
	if r.failReadiness {
		return nil, errors.New("injected bounded fake-provider readiness failure")
	}
	policy, ok := envelope["policy"].(map[string]any)
	if !ok {
		return nil, errors.New("native fake policy is missing")
	}
	r.policy = policy
	return map[string]any{"success": true, "command": "prompt"}, nil
}
func (r *nativeResetFakeRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	if r.policy == nil {
		return nil, errors.New("native fake policy was not applied")
	}
	inline, ok := r.policy["inline"].(map[string]any)
	if !ok {
		return nil, errors.New("native fake inline policy is missing")
	}
	compaction, ok := r.policy["compaction"].(map[string]any)
	if !ok {
		return nil, errors.New("native fake compaction policy is missing")
	}
	goalLock, ok := r.policy["goal_lock"].(map[string]any)
	if !ok {
		return nil, errors.New("native fake goal-lock policy is missing")
	}
	recall, ok := r.policy["recall"].(map[string]any)
	if !ok {
		return nil, errors.New("native fake recall policy is missing")
	}
	settled, ok := r.policy["settled"].(map[string]any)
	if !ok {
		return nil, errors.New("native fake settled policy is missing")
	}
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{"inline_enabled": inline["enabled"], "inline_credits": inline["credits"], "recall_content": recall["content"], "settled_enabled": settled["enabled"], "goal_lock_enabled": goalLock["enabled"], "compaction_deferred": compaction["deferred"]}}, nil
}
func (r *nativeResetFakeRPC) RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"success": true, "command": "prompt"}, nil
}
func (r *nativeResetFakeRPC) WaitForChildSnapshot(context.Context) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "native-fake-snapshot", "owner_session_id": r.sessionID, "sequence": int64(0), "runs": []any{}}, nil
}

type nativeResetCleanupFailure struct {
	mu      sync.Mutex
	enabled bool
	failed  bool
}

func (f *nativeResetCleanupFailure) before(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.enabled && !f.failed {
		f.failed = true
		return errors.New("injected native old-artifact cleanup failure")
	}
	return nil
}

func (f *nativeResetCleanupFailure) setEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = enabled
}

type nativeResetPrebindCrash struct {
	mu      sync.Mutex
	enabled bool
	process managed.Process
}

func (c *nativeResetPrebindCrash) after(_ context.Context, _ string, process managed.Process) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return nil
	}
	c.process = process
	return errors.New("injected native pre-bind reset crash")
}

func (c *nativeResetPrebindCrash) setEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = enabled
}

func (c *nativeResetPrebindCrash) captured() managed.Process {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.process
}

func nativeResetRequest(agentID, requestID string) managed.ResetRequest {
	return managed.ResetRequest{AgentID: agentID, Method: http.MethodPost, CanonicalPath: "/v1/managed-agents/" + agentID + "/reset", RequestID: requestID, CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
}

// runManagedResetNativeFailureMatrix uses the same locked installed Pi with a
// bounded offline provider setup. The two hooks only model the durable crash
// boundaries; they do not change production behavior or use credentials.
func runManagedResetNativeFailureMatrix(t *testing.T, root string, lockedPi managedResetLockedPi) managedResetNativeMatrix {
	t.Helper()
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
	fakeOps := &nativeResetProcessOps{base: managed.DefaultProcessGroupOps(), nextPID: 9000, fake: map[int]*nativeResetFakeProcess{}}
	counting := &nativeResetCountingFactory{base: managed.DefaultProcessFactory(), sessionRoot: sessionRoot, fakeOps: fakeOps, fakeSource: true}
	activeTools := []string{"subagent", "get_subagent_result", "steer_subagent"}
	for _, tool := range manifest.Tools {
		activeTools = append(activeTools, tool.Name)
	}
	rpcs := &nativeResetRPCFactory{base: managed.DefaultRPCFactory(), active: activeTools, failFakeOpen: 2}
	cleanup := &nativeResetCleanupFailure{}
	prebind := &nativeResetPrebindCrash{}
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store:    durable,
		Config:   managed.Config{Enabled: true, PiExecutable: lockedPi.executable, ManagedHome: home, SessionRoot: sessionRoot, ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts"), PiSubagentsExtension: lockedPi.subagentsEntry, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
		Manifest: manifest, ProcessFactory: counting, RPCFactory: rpcs,
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupOps: fakeOps, ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true },
		BeforeManagedResetOldArtifactCleanup: cleanup.before, AfterManagedResetTargetStartBeforeBind: prebind.after,
	})
	require.NoError(t, err)
	defer func() { _ = manager.Close(context.Background()) }()
	const agentID = "native-reset-matrix"
	_, err = manager.Launch(ctx, managed.LaunchRequest{Name: agentID, Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174131"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, agentID)
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 25*time.Second, 100*time.Millisecond)

	_, err = manager.Reset(ctx, nativeResetRequest(agentID, "123e4567-e89b-42d3-a456-426614174132"))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174132")
		return actionErr == nil && action.State == "failed" && action.TerminalCode == "RESET_TARGET_START_FAILED" && action.Retryable
	}, 25*time.Second, 100*time.Millisecond)
	failedTarget, err := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174132")
	require.NoError(t, err)
	require.NotNil(t, failedTarget.TargetFence)
	failedArtifact, err := durable.ManagedSessionArtifact(ctx, failedTarget.TargetArtifactID)
	require.NoError(t, err)
	startsBeforeTargetRetry := counting.starts()
	_, err = manager.Reset(ctx, nativeResetRequest(agentID, "123e4567-e89b-42d3-a456-426614174133"))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174133")
		agent, agentErr := durable.ManagedAgent(ctx, agentID)
		return actionErr == nil && agentErr == nil && action.State == "succeeded" && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 25*time.Second, 100*time.Millisecond)
	targetRetry := counting.spec(startsBeforeTargetRetry)
	targetStartRetryResume := targetRetry.LaunchMode == managed.ResumeExisting && targetRetry.SessionID == failedArtifact.PiSessionID && counting.starts() == startsBeforeTargetRetry+1
	require.True(t, targetStartRetryResume, "target-start retry must resume the retained session exactly once")

	cleanup.setEnabled(true)
	_, err = manager.Reset(ctx, nativeResetRequest(agentID, "123e4567-e89b-42d3-a456-426614174134"))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174134")
		agent, agentErr := durable.ManagedAgent(ctx, agentID)
		return actionErr == nil && agentErr == nil && action.State == "failed" && action.TerminalCode == "RESET_OLD_ARTIFACT_CLEANUP_FAILED" && action.Retryable && !agent.AcceptingPrompts
	}, 25*time.Second, 100*time.Millisecond)
	startsBeforeCleanupRetry := counting.starts()
	cleanup.setEnabled(false)
	_, err = manager.Reset(ctx, nativeResetRequest(agentID, "123e4567-e89b-42d3-a456-426614174135"))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174135")
		agent, agentErr := durable.ManagedAgent(ctx, agentID)
		return actionErr == nil && agentErr == nil && action.State == "succeeded" && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 25*time.Second, 100*time.Millisecond)
	cleanupRetryStarts := counting.starts() - startsBeforeCleanupRetry
	require.Zero(t, cleanupRetryStarts, "old-artifact cleanup retry must not start Pi")

	prebind.setEnabled(true)
	counting.captureNext()
	_, err = manager.Reset(ctx, nativeResetRequest(agentID, "123e4567-e89b-42d3-a456-426614174136"))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174136")
		return actionErr == nil && action.State == "starting_target" && action.Stage == "target_start"
	}, 25*time.Second, 100*time.Millisecond)
	captured := prebind.captured()
	require.NotNil(t, captured)
	tracked := counting.captured()
	require.NotNil(t, tracked)
	startsBeforeRecovery := counting.starts()
	recoveryManager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: true, PiExecutable: lockedPi.executable, ManagedHome: home, SessionRoot: sessionRoot, ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts"), PiSubagentsExtension: lockedPi.subagentsEntry, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"}, Manifest: manifest, ProcessFactory: counting, RPCFactory: rpcs, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupOps: fakeOps, ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	defer func() { _ = recoveryManager.Close(context.Background()) }()
	require.NoError(t, recoveryManager.RecoverManagedLifecycleControls(ctx))
	prebindRecoveryStarts := counting.starts() - startsBeforeRecovery
	require.Zero(t, prebindRecoveryStarts, "pre-bind recovery must not create a second target")
	require.Zero(t, tracked.stops(), "pre-bind recovery has no target fence and must not stop the unknown target")
	require.Zero(t, fakeOps.signalCount(captured.Identity()), "pre-bind recovery has no target fence and must not signal the unknown target")
	recoveredPrebind, err := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174136")
	require.NoError(t, err)
	require.Equal(t, "failed", recoveredPrebind.State)
	require.Equal(t, "RESET_PROCESS_IDENTITY_UNSAFE", recoveredPrebind.TerminalCode)
	prebindRecoverySignals := fakeOps.signalCount(captured.Identity())
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	require.NoError(t, captured.Stop(stopCtx), "test cleanup occurs after the no-signal recovery assertion")
	return managedResetNativeMatrix{targetStartRetryResume: targetStartRetryResume, cleanupRetryStarts: cleanupRetryStarts, prebindRecoveryStarts: prebindRecoveryStarts, prebindRecoverySignals: prebindRecoverySignals}
}

// TestManagedResetInstalledPi exercises the locked installed Pi boundary with
// no prompt, provider turn, browser, credential, or extension mutation. Its
// artifact contains only code and package identities plus boolean assertions.
func TestManagedResetInstalledPi(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	lockedPi := requireManagedResetLockedPi(t, root)
	pi, subagents := lockedPi.executable, lockedPi.subagentsEntry
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
	factory := &nativeBlockingProcessFactory{base: managed.DefaultProcessFactory(), entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-factory.release:
		default:
			close(factory.release)
		}
	}()
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store:    durable,
		Config:   managed.Config{Enabled: true, PiExecutable: pi, ManagedHome: home, SessionRoot: sessionRoot, ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts"), PiSubagentsExtension: subagents, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
		Manifest: manifest, ProcessFactory: factory, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
	})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer func() { _ = router.Close(context.Background()) }()
	httpURL, _ := router.URLs()
	request := func(path, id string) (int, []byte) {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+path, bytes.NewReader([]byte(`{}`)))
		require.NoError(t, requestErr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", id)
		response, requestErr := http.DefaultClient.Do(req)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		return response.StatusCode, body
	}
	launchBody, err := json.Marshal(map[string]string{"name": "native-reset", "cwd": root})
	require.NoError(t, err)
	launchRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/managed-agents", bytes.NewReader(launchBody))
	require.NoError(t, err)
	launchRequest.Header.Set("Content-Type", "application/json")
	launchRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174071")
	launchResponse, err := http.DefaultClient.Do(launchRequest)
	require.NoError(t, err)
	launchResponse.Body.Close()
	require.Equal(t, http.StatusAccepted, launchResponse.StatusCode)
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal("installed Pi launch did not reach the readiness seed boundary")
	}
	require.NoError(t, seedNativeReadinessPolicy(ctx, databasePath, "native-reset"))
	close(factory.release)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "native-reset")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 20*time.Second, 100*time.Millisecond)
	before, err := durable.ManagedAgent(ctx, "native-reset")
	require.NoError(t, err)
	status, first := request("/v1/managed-agents/native-reset/reset", "123e4567-e89b-42d3-a456-426614174072")
	require.Equal(t, http.StatusAccepted, status)
	status, replay := request("/v1/managed-agents/native-reset/reset", "123e4567-e89b-42d3-a456-426614174072")
	require.Equal(t, http.StatusAccepted, status)
	require.Equal(t, first, replay)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174072")
		agent, agentErr := durable.ManagedAgent(ctx, "native-reset")
		return actionErr == nil && agentErr == nil && action.State == "succeeded" && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 20*time.Second, 100*time.Millisecond)
	after, err := durable.ManagedAgent(ctx, "native-reset")
	require.NoError(t, err)
	action, err := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174072")
	require.NoError(t, err)
	sourceArtifact, err := durable.ManagedSessionArtifactForSession(ctx, "native-reset", before.SessionEpoch, before.PiSessionID)
	require.NoError(t, err)
	require.Equal(t, "unlinked", sourceArtifact.CleanupState)
	require.False(t, after.InitialInstructionSent)
	matrix := runManagedResetNativeFailureMatrix(t, root, lockedPi)
	writeManagedResetNativeArtifact(t, root, lockedPi, before, after, action, sourceArtifact.CleanupState == "unlinked", matrix)
}
