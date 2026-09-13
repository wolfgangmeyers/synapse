package managed

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type launchProcess struct {
	identity  ProcessIdentity
	stopped   chan struct{}
	waitErr   error
	once      sync.Once
	stopMu    sync.Mutex
	stopCalls int
	waitMu    sync.Mutex
	waitCalls int
	waitDone  chan struct{}
	waitOnce  sync.Once
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func (p *launchProcess) Identity() ProcessIdentity { return p.identity }
func (p *launchProcess) Stdin() io.WriteCloser     { return nopWriteCloser{io.Discard} }
func (p *launchProcess) Stdout() io.ReadCloser     { return io.NopCloser(strings.NewReader("")) }
func (p *launchProcess) Wait() error {
	p.waitMu.Lock()
	p.waitCalls++
	p.waitMu.Unlock()
	<-p.stopped
	if p.waitDone != nil {
		p.waitOnce.Do(func() { close(p.waitDone) })
	}
	return p.waitErr
}
func (p *launchProcess) Reap() { p.once.Do(func() { close(p.stopped) }) }
func (p *launchProcess) Stop(context.Context) error {
	p.stopMu.Lock()
	p.stopCalls++
	p.stopMu.Unlock()
	p.Reap()
	return nil
}
func (p *launchProcess) Stops() int {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	return p.stopCalls
}
func (p *launchProcess) Waits() int {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitCalls
}

type launchRPC struct {
	session                string
	calls                  []string
	promptCount            int
	promptDone             chan struct{}
	promptEntered          chan struct{}
	promptContextCancelled chan struct{}
	releasePrompt          chan struct{}
	active                 []string
	policyEnvelope         map[string]any
	snapshotEnvelope       map[string]any
	failAt                 string
	stateErr               error
	beforeState            func()
	abortCalls             int
	abortErr               error
	once                   sync.Once
}

func (r *launchRPC) GetState(context.Context) (map[string]any, error) {
	r.calls = append(r.calls, "state")
	if r.beforeState != nil {
		r.beforeState()
	}
	if r.stateErr != nil {
		return nil, r.stateErr
	}
	return map[string]any{"sessionId": r.session}, nil
}
func (r *launchRPC) GetCommands(context.Context) ([]string, error) {
	r.calls = append(r.calls, "commands")
	return append(append([]string{}, hiddenCommands...), legacyCommands...), nil
}
func (r *launchRPC) Bootstrap(context.Context, map[string]any) error {
	r.calls = append(r.calls, "bootstrap")
	return nil
}
func (r *launchRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	r.calls = append(r.calls, "receipt")
	return nil
}
func (r *launchRPC) GetActiveTools(context.Context) ([]string, error) {
	r.calls = append(r.calls, "active")
	if r.active != nil {
		return append([]string{}, r.active...), nil
	}
	active := append([]string{}, fixedManagedCodingTools...)
	active = append(active, fixedSubagentsTools...)
	return append(active, managedCurrentToolNames...), nil
}
func (r *launchRPC) readinessFailure(step string) error {
	if r.failAt == step {
		return errors.New("scripted " + step + " failure")
	}
	return nil
}
func (r *launchRPC) ApplyPolicy(_ context.Context, envelope map[string]any) (map[string]any, error) {
	r.calls = append(r.calls, "policy")
	r.policyEnvelope = envelope
	if err := r.readinessFailure("policy-command"); err != nil {
		return nil, err
	}
	return map[string]any{"inlineEnabled": false, "inlineCredits": map[string]any{}, "recallContent": "", "settledEnabled": false, "goalLockEnabled": false, "compactionDeferred": false}, nil
}
func (r *launchRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	r.calls = append(r.calls, "policy-receipt")
	if err := r.readinessFailure("policy-receipt"); err != nil {
		return nil, err
	}
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false}}, nil
}
func (r *launchRPC) RequestChildSnapshot(_ context.Context, envelope map[string]any) (map[string]any, error) {
	r.calls = append(r.calls, "snapshot")
	r.snapshotEnvelope = envelope
	if err := r.readinessFailure("snapshot-command"); err != nil {
		return nil, err
	}
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": r.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (r *launchRPC) WaitForChildSnapshot(context.Context) (map[string]any, error) {
	r.calls = append(r.calls, "snapshot-receipt")
	if err := r.readinessFailure("snapshot-receipt"); err != nil {
		return nil, err
	}
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": r.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (r *launchRPC) Prompt(ctx context.Context, _ string) error {
	r.calls = append(r.calls, "prompt")
	r.promptCount++
	if r.promptDone != nil {
		r.once.Do(func() { close(r.promptDone) })
	}
	if r.promptEntered != nil {
		close(r.promptEntered)
	}
	if r.releasePrompt != nil {
		select {
		case <-r.releasePrompt:
		case <-ctx.Done():
			if r.promptContextCancelled != nil {
				close(r.promptContextCancelled)
			}
			<-r.releasePrompt
		}
	}
	return nil
}
func (r *launchRPC) Abort(context.Context) error {
	r.abortCalls++
	return r.abortErr
}
func (r *launchRPC) Close() error { return nil }

type launchFactory struct {
	process *launchProcess
	spec    CommandSpec
	rpc     *launchRPC
}

func (f *launchFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.spec = spec
	f.rpc.session = spec.SessionID
	return f.process, nil
}

type launchRPCFactory struct{ rpc *launchRPC }

func (f launchRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

func TestManagerMarksReadyOnlyAfterReadinessSources(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 123, StartMarker: "start", Incarnation: "incarnation"}, stopped: make(chan struct{})}
	rpc := &launchRPC{promptDone: make(chan struct{})}
	factory := &launchFactory{process: process, rpc: rpc}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "alpha", Cwd: root, InitialInstruction: "hello", RequestID: "request"})
	require.NoError(t, err)
	deadline := time.Now().Add(2 * time.Second)
	for {
		agent, readErr := durable.ManagedAgent(ctx, "alpha")
		require.NoError(t, readErr)
		if agent.LifecycleState == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed agent did not become ready: %+v", agent)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-rpc.promptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("initial instruction was not sent")
	}
	require.Equal(t, []string{"state", "commands", "bootstrap", "receipt", "active", "policy", "policy-receipt", "snapshot", "snapshot-receipt", "prompt"}, rpc.calls)
	require.Equal(t, 1, rpc.promptCount)
	source, err := durable.ManagedReadinessSource(ctx, "alpha")
	require.NoError(t, err)
	require.NotNil(t, source.AppliedPolicy)
	require.NotNil(t, source.Snapshot)
	require.Equal(t, store.DefaultManagedReadinessPolicy(), *source.AppliedPolicy)
	require.Equal(t, "mecha.policy/v1", rpc.policyEnvelope["protocol"])
	require.Equal(t, "mecha.child-snapshot/v1", rpc.snapshotEnvelope["protocol"])
	require.Equal(t, manifest.CanonicalSHA256, rpc.policyEnvelope["manifest_sha256"])
	require.Equal(t, manifest.CanonicalSHA256, rpc.snapshotEnvelope["manifest_sha256"])
	require.NoError(t, manager.Close(context.Background()))
	stopped, err := durable.ManagedAgent(ctx, "alpha")
	require.NoError(t, err)
	require.Equal(t, "running", stopped.LifecycleState)
	require.Equal(t, "stopped", stopped.ActivityState)
	require.False(t, stopped.AcceptingPrompts)
	require.False(t, stopped.SlotHeld)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	require.Equal(t, canonicalRoot, factory.spec.Cwd)
	require.Equal(t, "openai-codex", factory.spec.Provider)
}

func TestManagerSourceReadinessFailuresNeverAdmitTheInitialInstruction(t *testing.T) {
	for _, test := range []struct {
		name                     string
		failAt                   string
		wantPolicy, wantSnapshot bool
	}{
		{"policy command", "policy-command", false, false},
		{"policy receipt", "policy-receipt", false, false},
		{"snapshot command", "snapshot-command", true, false},
		{"snapshot receipt", "snapshot-receipt", true, false},
		{"final source-confirmed ready transition", "final-source-confirmation", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, durable.Close()) })
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			root := t.TempDir()
			process := &launchProcess{identity: ProcessIdentity{PID: 800, StartMarker: "start", Incarnation: "incarnation"}, stopped: make(chan struct{})}
			rpc := &launchRPC{failAt: test.failAt}
			factory := &launchFactory{process: process, rpc: rpc}
			groupChecks := 0
			manager, err := NewManager(ManagerOptions{
				Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
				ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				ProcessGroupLeaderVerifier: func(ProcessIdentity) bool {
					groupChecks++
					// The sixth check occurs after both source commits and immediately
					// before MarkManagedReadyFromSources. It proves the final source-
					// confirmed transition cannot release the initial instruction.
					return test.failAt != "final-source-confirmation" || groupChecks != 6
				},
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = manager.Close(context.Background()) })
			_, err = manager.Launch(ctx, LaunchRequest{Name: "failure-agent", Cwd: root, InitialInstruction: "must not send", RequestID: "failure-request"})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				agent, readErr := durable.ManagedAgent(ctx, "failure-agent")
				return readErr == nil && agent.LifecycleState == "failed"
			}, 2*time.Second, time.Millisecond)
			agent, err := durable.ManagedAgent(ctx, "failure-agent")
			require.NoError(t, err)
			require.Equal(t, "READINESS_FAILED", agent.LastErrorCode)
			require.False(t, agent.AcceptingPrompts)
			require.False(t, agent.InitialInstructionSent)
			require.Equal(t, 0, rpc.promptCount)
			var notReady *store.ManagedAgentNotReadyError
			require.ErrorAs(t, manager.AdmitPrompt(ctx, "failure-agent", "must not admit"), &notReady)
			source, err := durable.ManagedReadinessSource(ctx, "failure-agent")
			require.NoError(t, err)
			require.Equal(t, test.wantPolicy, source.AppliedPolicy != nil)
			require.Equal(t, test.wantSnapshot, source.Snapshot != nil)
		})
	}
}

func TestManagerRecordsConfirmedPreReadinessEOFAsSafeProcessExit(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	processExit := exec.Command("/bin/sh", "-c", "exit 17").Run()
	require.Error(t, processExit)
	process := &launchProcess{identity: ProcessIdentity{PID: 801, StartMarker: "start", Incarnation: "incarnation"}, waitErr: &managedProcessExitError{cause: processExit, detail: "fatal token=do-not-retain /private/router/session.json"}, stopped: make(chan struct{}), waitDone: make(chan struct{})}
	process.Reap() // The fake Pi closes stdout before get_state can answer.
	rpc := &launchRPC{stateErr: io.EOF, beforeState: func() {
		<-process.waitDone
		// The process waiter owns the one Wait call. Give its completion handoff
		// a turn before returning EOF from the fake RPC pipe.
		time.Sleep(time.Millisecond)
	}}
	root := t.TempDir()
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: &launchFactory{process: process, rpc: rpc}, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	_, err = manager.Launch(ctx, LaunchRequest{Name: "eof-exit", Cwd: root, InitialInstruction: "must not prompt", RequestID: "eof-exit-request"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "eof-exit")
		return readErr == nil && agent.LifecycleState == "failed"
	}, time.Second, time.Millisecond)
	agent, err := durable.ManagedAgent(ctx, "eof-exit")
	require.NoError(t, err)
	require.Equal(t, "PROCESS_EXITED", agent.LastErrorCode)
	require.Contains(t, agent.LastErrorMessage, "code 17")
	for _, forbidden := range []string{"do-not-retain", "/private/router/session.json"} {
		require.NotContains(t, agent.LastErrorMessage, forbidden)
	}
	require.Equal(t, 0, rpc.promptCount)
	require.Equal(t, 0, process.Stops(), "a confirmed exit must not be stopped again")
	require.Equal(t, 1, process.Waits(), "the waiter is the only Process.Wait caller")
	operation, err := durable.ManagedOperation(ctx, "eof-exit-request")
	require.NoError(t, err)
	require.Equal(t, "terminal", operation.State)
	require.Equal(t, 202, operation.ResponseStatus)
}

func TestManagerKeepsUnconfirmedReadinessPipeFailuresAsReadinessFailures(t *testing.T) {
	for _, stateErr := range []error{io.EOF, context.DeadlineExceeded} {
		ctx := context.Background()
		durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
		require.NoError(t, err)
		manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
		require.NoError(t, err)
		root := t.TempDir()
		process := &launchProcess{identity: ProcessIdentity{PID: 802, StartMarker: "start", Incarnation: "incarnation"}, stopped: make(chan struct{})}
		rpc := &launchRPC{stateErr: stateErr}
		manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: &launchFactory{process: process, rpc: rpc}, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
		require.NoError(t, err)
		_, err = manager.Launch(ctx, LaunchRequest{Name: "unconfirmed-" + strings.ReplaceAll(stateErr.Error(), " ", "-"), Cwd: root, RequestID: "unconfirmed-" + strings.ReplaceAll(stateErr.Error(), " ", "-")})
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			agent, readErr := durable.ManagedAgent(ctx, "unconfirmed-"+strings.ReplaceAll(stateErr.Error(), " ", "-"))
			return readErr == nil && agent.LifecycleState == "failed"
		}, time.Second, time.Millisecond)
		agent, err := durable.ManagedAgent(ctx, "unconfirmed-"+strings.ReplaceAll(stateErr.Error(), " ", "-"))
		require.NoError(t, err)
		require.Equal(t, "READINESS_FAILED", agent.LastErrorCode)
		require.Equal(t, 1, process.Stops())
		require.NoError(t, manager.Close(context.Background()))
		require.NoError(t, durable.Close())
	}
}

func TestManagerCloseCommitsLaunchStateAfterRunContextCancelled(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 456, StartMarker: "start", Incarnation: "incarnation"}, stopped: make(chan struct{})}
	releasePrompt := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releasePrompt) }) }
	rpc := &launchRPC{promptEntered: make(chan struct{}), promptContextCancelled: make(chan struct{}), releasePrompt: releasePrompt}
	factory := &launchFactory{process: process, rpc: rpc}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	t.Cleanup(release)

	_, err = manager.Launch(ctx, LaunchRequest{Name: "alpha", Cwd: root, InitialInstruction: "hello", RequestID: "request"})
	require.NoError(t, err)
	select {
	case <-rpc.promptEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("initial instruction did not begin")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close(context.Background()) }()
	select {
	case <-rpc.promptContextCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not cancel the launch runtime")
	}
	release()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not finish shutdown")
	}

	agent, err := durable.ManagedAgent(ctx, "alpha")
	require.NoError(t, err)
	require.True(t, agent.InitialInstructionSent)
	require.Empty(t, agent.PendingInitialInstruction)
	require.Equal(t, "stopped", agent.ActivityState)
	op, err := durable.ManagedOperation(ctx, "request")
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
}

func TestManagerMarksReapedManagedProcessFailed(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 123, StartMarker: "start", Incarnation: "incarnation"}, waitErr: errors.New("signal: killed"), stopped: make(chan struct{})}
	rpc := &launchRPC{}
	factory := &launchFactory{process: process, rpc: rpc}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "reaped", Cwd: root, RequestID: "request"})
	require.NoError(t, err)
	deadline := time.Now().Add(2 * time.Second)
	for {
		agent, readErr := durable.ManagedAgent(ctx, "reaped")
		require.NoError(t, readErr)
		if agent.LifecycleState == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed agent did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	process.Reap()
	for {
		agent, readErr := durable.ManagedAgent(ctx, "reaped")
		require.NoError(t, readErr)
		if agent.LifecycleState == "failed" {
			require.Equal(t, "failed", agent.ActivityState)
			require.Equal(t, "PROCESS_EXITED", agent.LastErrorCode)
			require.False(t, agent.AcceptingPrompts)
			require.False(t, agent.SlotHeld)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reaped managed agent was not marked failed")
		}
		time.Sleep(time.Millisecond)
	}
	require.NoError(t, manager.Close(context.Background()))
}
