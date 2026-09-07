package managed

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type readinessRPC struct {
	calls    []string
	active   []string
	envelope map[string]any
}

func (r *readinessRPC) GetState(context.Context) (map[string]any, error) {
	r.calls = append(r.calls, "get_state")
	return map[string]any{"sessionId": "session"}, nil
}
func (r *readinessRPC) GetCommands(context.Context) ([]string, error) {
	r.calls = append(r.calls, "get_commands")
	return append(append([]string{}, hiddenCommands...), legacyCommands...), nil
}
func (r *readinessRPC) Bootstrap(_ context.Context, envelope map[string]any) error {
	r.calls = append(r.calls, "bootstrap")
	r.envelope = envelope
	return nil
}
func (r *readinessRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	r.calls = append(r.calls, "receipt")
	return nil
}
func (r *readinessRPC) GetActiveTools(context.Context) ([]string, error) {
	r.calls = append(r.calls, "get_active_tools")
	return r.active, nil
}
func (r *readinessRPC) Prompt(context.Context, string) error {
	r.calls = append(r.calls, "prompt")
	return nil
}
func (r *readinessRPC) Close() error { return nil }

type admissionProcess struct {
	*launchProcess
	session string
}

type admissionFactory struct {
	mu      sync.Mutex
	count   int
	started chan struct{}
	release chan struct{}
	active  []string
}

func (f *admissionFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.mu.Lock()
	f.count++
	process := &admissionProcess{launchProcess: &launchProcess{identity: ProcessIdentity{PID: 1000 + f.count, StartMarker: spec.SessionID, Incarnation: spec.SessionID}, stopped: make(chan struct{})}, session: spec.SessionID}
	if f.count == 32 {
		close(f.started)
	}
	f.mu.Unlock()
	<-f.release
	return process, nil
}

type admissionRPCFactory struct{ active []string }

func (f admissionRPCFactory) Open(_ context.Context, process Process) (RPC, error) {
	return &launchRPC{session: process.(*admissionProcess).session, active: f.active}, nil
}

func TestManagedProcessLimitRejectsThirtyThirdConcurrentStart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	active := expectedManagedActiveTools(manifest)
	factory := &admissionFactory{started: make(chan struct{}), release: make(chan struct{}), active: active}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: admissionRPCFactory{active: active}, CatalogSource: CatalogFromModels(Model{Provider: "openai", ID: "gpt-5.6-terra", Available: true})})
	require.NoError(t, err)
	for index := 0; index < 32; index++ {
		_, err = manager.Launch(ctx, LaunchRequest{Name: fmt.Sprintf("agent-%02d", index), Cwd: root, SessionIdentityHash: "session", RequestID: fmt.Sprintf("request-%02d", index)})
		require.NoError(t, err)
	}
	select {
	case <-factory.started:
	case <-time.After(2 * time.Second):
		t.Fatal("32 concurrent starts did not reach the process factory")
	}
	rows, err := durable.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 32)
	for _, row := range rows {
		require.True(t, row.SlotHeld, row.Name)
	}
	_, err = manager.Launch(ctx, LaunchRequest{Name: "agent-33", Cwd: root, SessionIdentityHash: "session", RequestID: "request-33"})
	var limit *store.ManagedSlotLimitError
	require.ErrorAs(t, err, &limit)
	factory.mu.Lock()
	require.Equal(t, 32, factory.count)
	factory.mu.Unlock()
	close(factory.release)
	require.Eventually(t, func() bool {
		rows, readErr := durable.ManagedAgents(ctx)
		if readErr != nil || len(rows) != 32 {
			return false
		}
		for _, row := range rows {
			if row.LifecycleState != "running" {
				return false
			}
		}
		return true
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, manager.Close(context.Background()))
	rows, err = durable.ManagedAgents(ctx)
	require.NoError(t, err)
	for _, row := range rows {
		require.False(t, row.SlotHeld, row.Name)
		require.Equal(t, "stopped", row.ActivityState, row.Name)
	}

	reserve := func(name, operation string) {
		require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{AgentID: name, Name: name, SlotHeld: true, Cwd: root, SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: "manifest"}, store.ManagedOperation{OperationID: operation, SessionIdentityHash: "session-" + name, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: operation, CanonicalBodyHash: "body-" + name, ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{"operation_id":"` + operation + `"}`)}))
	}
	reserve("failed-start", "failed-start-operation")
	versionBeforeFailure, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedFailed(ctx, "failed-start", "PROCESS_START_FAILED", "start failed"))
	failedStart, err := durable.ManagedAgent(ctx, "failed-start")
	require.NoError(t, err)
	require.False(t, failedStart.SlotHeld)
	versionAfterFailure, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Greater(t, versionAfterFailure, versionBeforeFailure)
	require.NoError(t, durable.MarkManagedFailed(ctx, "failed-start", "PROCESS_EXITED", "stale exit"))
	require.NoError(t, durable.ReleaseManagedSlot(ctx, "failed-start"))
	versionAfterRepeatedRelease, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, versionAfterFailure, versionAfterRepeatedRelease)

	reserve("current-exit", "current-exit-operation")
	reserve("stale-exit", "stale-exit-operation")
	require.NoError(t, durable.MarkManagedFailed(ctx, "stale-exit", "PROCESS_EXITED", "old process exited"))
	current, err := durable.ManagedAgent(ctx, "current-exit")
	require.NoError(t, err)
	require.True(t, current.SlotHeld)
	require.NoError(t, durable.MarkManagedFailed(ctx, "current-exit", "PROCESS_EXITED", "current process exited"))
	current, err = durable.ManagedAgent(ctx, "current-exit")
	require.NoError(t, err)
	require.False(t, current.SlotHeld)
}

func TestReadinessDefersPolicyAndChildSnapshot(t *testing.T) {
	path := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := LoadManifest(path)
	require.NoError(t, err)
	rpc := &readinessRPC{active: expectedManagedActiveTools(manifest)}
	require.NoError(t, readinessGate(context.Background(), rpc, manifest, "session", 1, "generation"))
	require.Equal(t, []string{"get_state", "get_commands", "bootstrap", "receipt", "get_active_tools"}, rpc.calls)
	require.NotContains(t, rpc.calls, "policy")
	require.NotContains(t, rpc.calls, "child_snapshot")
	require.Equal(t, "mecha.bootstrap/v1", rpc.envelope["protocol"])
	require.Equal(t, manifest.Raw, rpc.envelope["desired_manifest"])
	require.Equal(t, manifest.CanonicalSHA256, rpc.envelope["manifest_sha256"])
}

func TestReadinessGateRejectsExtraCommandOrActiveTool(t *testing.T) {
	commands := append(append([]string{}, hiddenCommands...), legacyCommands...)
	commands = append(commands, hiddenCommands[0])
	require.Error(t, requireManagedCommands(commands))
	commands = append(append([]string{}, hiddenCommands...), legacyCommands...)
	commands[0] = "/" + commands[0] + ":2"
	require.Error(t, requireManagedCommands(commands))
	path := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := LoadManifest(path)
	require.NoError(t, err)
	rpc := &readinessRPC{active: expectedManagedActiveTools(manifest)[:18]}
	require.Error(t, readinessGate(context.Background(), rpc, manifest, "session", 1, "generation"))
	require.NotContains(t, rpc.calls, "prompt")
}
