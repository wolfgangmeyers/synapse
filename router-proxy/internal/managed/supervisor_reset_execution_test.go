package managed

import (
	"context"
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
	mu      sync.Mutex
	present map[int]bool
	probes  []ProcessIdentity
	signals []ProcessIdentity
	waits   []ProcessIdentity
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
	o.mu.Unlock()
	return ProcessProbe{State: ProcessAbsent}
}

func TestResetCreatesFreshSessionAndOpensPromptsOnlyAfterArtifactCleanup(t *testing.T) {
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
