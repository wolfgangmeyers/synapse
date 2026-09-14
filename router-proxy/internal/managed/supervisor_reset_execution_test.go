package managed

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type resetProcessFactory struct {
	mu        sync.Mutex
	processes []*launchProcess
	specs     []CommandSpec
	next      int
}

func (f *resetProcessFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	process := f.processes[f.next]
	f.next++
	return process, nil
}

type resetRPC struct {
	*launchRPC
	file string
}

func (r *resetRPC) GetState(context.Context) (map[string]any, error) {
	r.calls = append(r.calls, "state")
	return map[string]any{"sessionId": r.session, "sessionFile": r.file}, nil
}

type resetRPCFactory struct {
	mu   sync.Mutex
	rpcs []*resetRPC
	next int
}

type resetSettlementWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *resetSettlementWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

type resetNativeActivityRPC struct {
	*launchRPC
	native                       *rpcClient
	file                         string
	ready                        chan struct{}
	readyOnce                    sync.Once
	idleEntered                  chan struct{}
	idleRelease                  <-chan struct{}
	idleOnce                     sync.Once
	settleEntered                chan struct{}
	settlementWaitEntered        chan struct{}
	artifactDiscoveryEntered     chan struct{}
	artifactDiscoveryRelease     <-chan struct{}
	artifactDiscoveryEnteredOnce sync.Once
	stateMu                      sync.Mutex
	stateCalls                   int
}

func (r *resetNativeActivityRPC) GetState(context.Context) (map[string]any, error) {
	r.stateMu.Lock()
	r.stateCalls++
	call := r.stateCalls
	r.stateMu.Unlock()
	if call == 2 {
		r.artifactDiscoveryEnteredOnce.Do(func() { close(r.artifactDiscoveryEntered) })
		<-r.artifactDiscoveryRelease
	}
	return map[string]any{"sessionId": r.session, "sessionFile": r.file}, nil
}

func (r *resetNativeActivityRPC) WaitForChildSnapshot(ctx context.Context) (map[string]any, error) {
	snapshot, err := r.launchRPC.WaitForChildSnapshot(ctx)
	r.readyOnce.Do(func() { close(r.ready) })
	return snapshot, err
}
func (r *resetNativeActivityRPC) Prompt(ctx context.Context, message string) error {
	return r.native.Prompt(ctx, message)
}
func (r *resetNativeActivityRPC) PromptAdmittedRestartTurn(ctx context.Context, message string, fence store.ManagedRuntimeFence) error {
	return r.native.PromptAdmittedRestartTurn(ctx, message, fence)
}
func (r *resetNativeActivityRPC) SettlePreparedRestartTurn(ctx context.Context, fence store.ManagedRuntimeFence) error {
	r.settleEntered <- struct{}{}
	observed := &resetSettlementWaitContext{Context: ctx, entered: r.settlementWaitEntered}
	return r.native.SettlePreparedRestartTurn(observed, fence)
}
func (r *resetNativeActivityRPC) SetParentActivityEventHandler(handler func(map[string]any)) {
	r.native.SetParentActivityEventHandler(func(event map[string]any) {
		handler(event)
		if event["state"] == "idle" {
			r.idleOnce.Do(func() {
				close(r.idleEntered)
				<-r.idleRelease
			})
		}
	})
}
func (r *resetNativeActivityRPC) ParentActivitySequence() uint64 {
	return r.native.ParentActivitySequence()
}
func (r *resetNativeActivityRPC) Close() error { return r.native.Close() }

func (f *resetRPCFactory) Open(_ context.Context, process Process) (RPC, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rpc := f.rpcs[f.next]
	f.next++
	// The process factory owns the launch spec; the test RPC only needs the
	// session ID the manager places in its matching process identity marker.
	rpc.session = process.Identity().StartMarker
	return rpc, nil
}

type resetProcessOps struct {
	mu           sync.Mutex
	present      map[int]bool
	probes       []ProcessIdentity
	signals      []ProcessIdentity
	waits        []ProcessIdentity
	waitsEntered chan ProcessIdentity
}

func (o *resetProcessOps) Probe(identity ProcessIdentity) ProcessProbe {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.probes = append(o.probes, identity)
	if o.present[identity.PID] {
		return ProcessProbe{State: ProcessOwned}
	}
	return ProcessProbe{State: ProcessAbsent}
}
func (o *resetProcessOps) CloseProcessInput(Process) error { return nil }
func (o *resetProcessOps) SignalVerifiedProcessGroup(identity ProcessIdentity, _ syscall.Signal) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals = append(o.signals, identity)
	return nil
}
func (o *resetProcessOps) SignalVerifiedProcessGroupAfterLeaderExit(identity ProcessIdentity) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals = append(o.signals, identity)
	return nil
}

func (o *resetProcessOps) waitCount(pid int) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for _, identity := range o.waits {
		if identity.PID == pid {
			count++
		}
	}
	return count
}

func (o *resetProcessOps) signalCount(pid int) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for _, identity := range o.signals {
		if identity.PID == pid {
			count++
		}
	}
	return count
}
func (o *resetProcessOps) WaitForProcessGroupEmpty(_ context.Context, identity ProcessIdentity, _ <-chan error) ProcessProbe {
	o.mu.Lock()
	o.waits = append(o.waits, identity)
	o.present[identity.PID] = false
	waitsEntered := o.waitsEntered
	o.mu.Unlock()
	if waitsEntered != nil {
		waitsEntered <- identity
	}
	return ProcessProbe{State: ProcessAbsent}
}

func TestResetWaitsForSecondAdmittedPromptSettlementBeforeSourceDiscoveryOrStop(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	sourceFile := filepath.Join(sessionRoot, "source.json")
	targetFile := filepath.Join(sessionRoot, "target.json")
	require.NoError(t, os.WriteFile(sourceFile, []byte("{}"), 0o600))

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	native := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	requests := readJSONLRequests(stdinReader)
	idleRelease := make(chan struct{})
	artifactDiscoveryRelease := make(chan struct{})
	var releaseIdle sync.Once
	release := func() { releaseIdle.Do(func() { close(idleRelease) }) }
	var releaseArtifactDiscovery sync.Once
	releaseDiscovery := func() { releaseArtifactDiscovery.Do(func() { close(artifactDiscoveryRelease) }) }
	t.Cleanup(release)
	t.Cleanup(releaseDiscovery)
	t.Cleanup(func() { require.NoError(t, native.Close()) })
	sourceRPC := &resetNativeActivityRPC{
		launchRPC:                &launchRPC{},
		native:                   native,
		file:                     sourceFile,
		ready:                    make(chan struct{}),
		idleEntered:              make(chan struct{}),
		idleRelease:              idleRelease,
		settleEntered:            make(chan struct{}, 1),
		settlementWaitEntered:    make(chan struct{}),
		artifactDiscoveryEntered: make(chan struct{}),
		artifactDiscoveryRelease: artifactDiscoveryRelease,
	}
	targetRPC := &resetRPC{launchRPC: &launchRPC{}, file: targetFile}
	source := &launchProcess{identity: ProcessIdentity{PID: 101, StartMarker: "123e4567-e89b-42d3-a456-426614174001", Incarnation: "source-incarnation"}, stopped: make(chan struct{})}
	target := &launchProcess{identity: ProcessIdentity{PID: 202, StartMarker: "123e4567-e89b-42d3-a456-426614174003", Incarnation: "target-incarnation"}, stopped: make(chan struct{})}
	factory := &resetProcessFactory{processes: []*launchProcess{source, target}}
	var opened int
	rpcFactory := RPCFactoryFunc(func(_ context.Context, process Process) (RPC, error) {
		if opened == 0 {
			opened++
			sourceRPC.session = process.Identity().StartMarker
			return sourceRPC, nil
		}
		opened++
		targetRPC.session = process.Identity().StartMarker
		return targetRPC, nil
	})
	ids := &resetTestIDs{values: []string{
		"123e4567-e89b-42d3-a456-426614174001", "123e4567-e89b-42d3-a456-426614174002",
		"123e4567-e89b-42d3-a456-426614174003", "123e4567-e89b-42d3-a456-426614174004",
		"123e4567-e89b-42d3-a456-426614174005", "123e4567-e89b-42d3-a456-426614174006",
	}}
	ops := &resetProcessOps{present: map[int]bool{101: true, 202: true}, waitsEntered: make(chan ProcessIdentity, 2)}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false, SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: rpcFactory, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: ids, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	// Release the semantic hold before manager cleanup joins the native reader.
	t.Cleanup(release)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "reset-agent", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174011"})
	require.NoError(t, err)
	<-sourceRPC.ready

	turnAResult := make(chan error, 1)
	go func() { turnAResult <- manager.AdmitPrompt(ctx, "reset-agent", "turn A") }()
	select {
	case err := <-turnAResult:
		require.NoError(t, err, "turn A admission must reach the native RPC")
	case turnA := <-requests:
		require.NotNil(t, turnA)
		require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": turnA["id"], "type": "response", "success": true}))
		require.NoError(t, <-turnAResult)
	}
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_start"}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	<-sourceRPC.idleEntered

	turnBResult := make(chan error, 1)
	go func() { turnBResult <- manager.AdmitPrompt(ctx, "reset-agent", "turn B") }()
	select {
	case err := <-turnBResult:
		require.NoError(t, err, "a settled prior prompt must not reject a fresh admission")
	case turnB := <-requests:
		require.NotNil(t, turnB)
		require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": turnB["id"], "type": "response", "success": true}))
		require.NoError(t, <-turnBResult)
	}

	reset, err := manager.Reset(ctx, ResetRequest{AgentID: "reset-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/reset-agent/reset", RequestID: "123e4567-e89b-42d3-a456-426614174012", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	require.NoError(t, err)
	require.NotNil(t, reset.Operation)
	<-sourceRPC.settleEntered
	select {
	case <-sourceRPC.settlementWaitEntered:
	case <-time.After(time.Second):
		t.Fatal("reset did not reach the post-scan settlement wait for turn B")
	}
	action, err := durable.ManagedLifecycleAction(ctx, reset.Operation.OperationID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_settle", action.State)
	require.Equal(t, "source_settle", action.Stage)
	require.Zero(t, ops.waitCount(source.identity.PID), "reset stopped the source before turn B settled")

	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	<-sourceRPC.artifactDiscoveryEntered
	releaseDiscovery()
	waited := <-ops.waitsEntered
	require.Equal(t, source.identity, waited)
	release()
}

func TestResetCreatesFreshSessionAndOpensPromptsOnlyAfterArtifactCleanup(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	sourceFile := filepath.Join(sessionRoot, "source.json")
	require.NoError(t, os.WriteFile(sourceFile, []byte("{}"), 0o600))
	targetFile := filepath.Join(sessionRoot, "target.json")
	source := &launchProcess{identity: ProcessIdentity{PID: 101, StartMarker: "123e4567-e89b-42d3-a456-426614174001", Incarnation: "source-incarnation"}, stopped: make(chan struct{})}
	target := &launchProcess{identity: ProcessIdentity{PID: 202, StartMarker: "123e4567-e89b-42d3-a456-426614174003", Incarnation: "target-incarnation"}, stopped: make(chan struct{})}
	factory := &resetProcessFactory{processes: []*launchProcess{source, target}}
	rpcs := &resetRPCFactory{rpcs: []*resetRPC{{launchRPC: &launchRPC{}, file: sourceFile}, {launchRPC: &launchRPC{}, file: targetFile}}}
	ids := &resetTestIDs{values: []string{
		"123e4567-e89b-42d3-a456-426614174001", "123e4567-e89b-42d3-a456-426614174002",
		"123e4567-e89b-42d3-a456-426614174003", "123e4567-e89b-42d3-a456-426614174004",
		"123e4567-e89b-42d3-a456-426614174005", "123e4567-e89b-42d3-a456-426614174006",
	}}
	ops := &resetProcessOps{present: map[int]bool{101: true, 202: true}}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false, SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: rpcs, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: ids, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	_, err = manager.Launch(ctx, LaunchRequest{Name: "reset-agent", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174011"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "reset-agent")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 3*time.Second, time.Millisecond)
	before, err := durable.ManagedAgent(ctx, "reset-agent")
	require.NoError(t, err)
	sourceAttempt := queueManagedParentFinalForTest(t, durable, storedAgentFence(before), "reset-source-turn")
	db, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE managed_agents SET activity_state='idle' WHERE agent_id='reset-agent'`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	result, err := manager.Reset(ctx, ResetRequest{AgentID: "reset-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/reset-agent/reset", RequestID: "123e4567-e89b-42d3-a456-426614174012", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, result.Operation.OperationID)
		agent, agentErr := durable.ManagedAgent(ctx, "reset-agent")
		return actionErr == nil && agentErr == nil && action.State == "succeeded" && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 5*time.Second, time.Millisecond)
	after, err := durable.ManagedAgent(ctx, "reset-agent")
	require.NoError(t, err)
	requireManagedParentTerminalForTest(t, databasePath, sourceAttempt, "interrupted", "interrupted")
	require.NotEqual(t, before.PiSessionID, after.PiSessionID)
	require.Equal(t, before.SessionEpoch+1, after.SessionEpoch)
	require.Equal(t, "123e4567-e89b-42d3-a456-426614174003", after.PiSessionID)
	_, err = os.Stat(sourceFile)
	require.ErrorIs(t, err, os.ErrNotExist)
	sourceArtifact, err := durable.ManagedSessionArtifactForSession(ctx, "reset-agent", before.SessionEpoch, before.PiSessionID)
	require.NoError(t, err)
	require.Equal(t, "unlinked", sourceArtifact.CleanupState)
	require.Len(t, factory.specs, 2)
	require.Equal(t, CreateNew, factory.specs[0].LaunchMode)
	require.Equal(t, CreateNew, factory.specs[1].LaunchMode)
	require.Zero(t, rpcs.rpcs[1].promptCount, "reset must not replay launch-only input")
}
