package managed

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type launchProcess struct {
	identity ProcessIdentity
	stopped  chan struct{}
	waitErr  error
	once     sync.Once
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func (p *launchProcess) Identity() ProcessIdentity  { return p.identity }
func (p *launchProcess) Stdin() io.WriteCloser      { return nopWriteCloser{io.Discard} }
func (p *launchProcess) Stdout() io.ReadCloser      { return io.NopCloser(strings.NewReader("")) }
func (p *launchProcess) Wait() error                { <-p.stopped; return p.waitErr }
func (p *launchProcess) Reap()                      { p.once.Do(func() { close(p.stopped) }) }
func (p *launchProcess) Stop(context.Context) error { p.Reap(); return nil }

type launchRPC struct {
	session     string
	calls       []string
	promptCount int
	promptDone  chan struct{}
	once        sync.Once
}

func (r *launchRPC) GetState(context.Context) (map[string]any, error) {
	r.calls = append(r.calls, "state")
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
	return append(append([]string{}, fixedSubagentsTools...), baselineManagedNames...), nil
}
func (r *launchRPC) Prompt(context.Context, string) error {
	r.calls = append(r.calls, "prompt")
	r.promptCount++
	if r.promptDone != nil {
		r.once.Do(func() { close(r.promptDone) })
	}
	return nil
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

func TestManagerMarksReadyOnlyAfterRPCReadinessAndSendsInstructionOnce(t *testing.T) {
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
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true})})
	require.NoError(t, err)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "alpha", Cwd: root, InitialInstruction: "hello", SessionIdentityHash: "session", RequestID: "request"})
	require.NoError(t, err)
	deadline := time.Now().Add(2 * time.Second)
	for {
		agent, readErr := durable.ManagedAgent(ctx, "alpha")
		require.NoError(t, readErr)
		if agent.LifecycleState == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed agent did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-rpc.promptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("initial instruction was not sent")
	}
	require.Equal(t, []string{"state", "commands", "bootstrap", "receipt", "active", "prompt"}, rpc.calls)
	require.Equal(t, 1, rpc.promptCount)
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
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true})})
	require.NoError(t, err)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "reaped", Cwd: root, SessionIdentityHash: "session", RequestID: "request"})
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
