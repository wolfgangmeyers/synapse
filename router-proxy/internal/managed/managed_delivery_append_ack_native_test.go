package managed

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type appendAckNativeProcess struct{ identity ProcessIdentity }

func (p *appendAckNativeProcess) Identity() ProcessIdentity { return p.identity }
func (p *appendAckNativeProcess) Stdin() io.WriteCloser     { return nopWriteCloser{io.Discard} }
func (p *appendAckNativeProcess) Stdout() io.ReadCloser     { return io.NopCloser(strings.NewReader("")) }
func (*appendAckNativeProcess) Wait() error                 { select {} }
func (*appendAckNativeProcess) Stop(context.Context) error  { return nil }

type appendAckNativeRPC struct {
	launchRPC
	mu         sync.Mutex
	prompts    int
	recoveries int
	appendID   string
	body       string
	interrupt  func()
}

func (r *appendAckNativeRPC) PrepareManagedMailbox(context.Context) (string, error) {
	return "cursor-before-message", nil
}
func (r *appendAckNativeRPC) PromptManagedMailbox(context.Context, store.ManagedDirectAttempt) (<-chan DirectMailboxEvent, error) {
	r.mu.Lock()
	r.prompts++
	r.mu.Unlock()
	events := make(chan DirectMailboxEvent, 1)
	events <- DirectMailboxEvent{Receipt: &DirectMailboxReceipt{EntryIdentity: r.appendID}}
	if r.interrupt != nil {
		r.interrupt()
	}
	return events, nil
}
func (r *appendAckNativeRPC) RecoverManagedMailbox(context.Context, store.ManagedDirectAttempt) (DirectMailboxRecovery, error) {
	r.mu.Lock()
	r.recoveries++
	r.mu.Unlock()
	return DirectMailboxRecovery{Receipt: &DirectMailboxReceipt{EntryIdentity: r.appendID}}, nil
}
func (r *appendAckNativeRPC) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prompts, r.recoveries
}

type appendAckNativeFactory struct {
	process Process
	rpc     *appendAckNativeRPC
}

func (f appendAckNativeFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.rpc.session = spec.SessionID
	return f.process, nil
}

type appendAckNativeRPCFactory struct{ rpc *appendAckNativeRPC }

func (f appendAckNativeRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

func TestManagedDeliveryAppendAckRecoveryNative(t *testing.T) {
	ctx := context.Background()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	process := &appendAckNativeProcess{identity: ProcessIdentity{PID: 4101, StartMarker: "native-start", Incarnation: "native-incarnation"}}
	interrupted := make(chan struct{})
	var once sync.Once
	rpc := &appendAckNativeRPC{launchRPC: launchRPC{active: expectedManagedActiveTools(manifest)}, appendID: "pi-entry-fixed", body: "append ack body", interrupt: func() { once.Do(func() { close(interrupted) }) }}
	factory := appendAckNativeFactory{process: process, rpc: rpc}
	newManager := func() *Manager {
		manager, managerErr := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, ProcessFactory: factory, RPCFactory: appendAckNativeRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "mock", ID: "mock", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: &coldRecoveryOps{states: map[int]ProcessProbeState{4101: ProcessOwned}}, BeforeManagedDirectAcknowledge: func(context.Context, store.ManagedDirectAttempt) error {
			return errors.New("drop before message_end ACK")
		}})
		require.NoError(t, managerErr)
		return manager
	}
	manager := newManager()
	_, err = manager.Launch(ctx, LaunchRequest{Name: "append-agent", Cwd: paths["work"], RequestID: "append-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { a, e := durable.ManagedAgent(ctx, "append-agent"); return e == nil && a.AcceptingPrompts }, time.Second, time.Millisecond)
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "123e4567-e89b-42d3-a456-426614174099", From: "sender", To: "append-agent", Body: rpc.body, CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, "123e4567-e89b-42d3-a456-426614174099")
	require.NoError(t, err)
	manager.OfferDirect("append-agent")
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Fatal("direct prompt did not reach append boundary")
	}
	attempt, err := durable.ManagedDirectAttempt(ctx, "123e4567-e89b-42d3-a456-426614174099", 1)
	require.NoError(t, err)
	require.Equal(t, "direct_prompted", attempt.State)
	agent, err := durable.ManagedAgent(ctx, "append-agent")
	require.NoError(t, err)
	// Cancel the old router without marking the still-running Pi session stopped.
	// The replacement manager reattaches the same durable fence before recovery.
	manager.cancel()

	manager = newManager()
	binding := &managedBinding{agentID: agent.AgentID, lane: manager.laneFor(agent.AgentID), process: process, rpc: rpc, identity: process.Identity(), sessionID: agent.PiSessionID, sessionEpoch: agent.SessionEpoch, runtimeGeneration: agent.RuntimeGeneration, manifestRevision: agent.ManifestRevision, manifestHash: agent.ManifestHash}
	require.True(t, manager.installBinding(binding))
	require.NoError(t, manager.RecoverManagedDirectAttempts(ctx))
	_, err = durable.ManagedDirectAttempt(ctx, "123e4567-e89b-42d3-a456-426614174099", 1)
	require.ErrorIs(t, err, sql.ErrNoRows, "settlement removes the exact ordinary row and its attempt")
	message, err := durable.MessageFor(ctx, "123e4567-e89b-42d3-a456-426614174099")
	require.NoError(t, err)
	require.Nil(t, message)
	history, err := durable.ConversationHistory(ctx, "sender", "append-agent")
	require.NoError(t, err)
	require.Empty(t, history)
	prompts, recoveries := rpc.counts()
	require.Equal(t, 1, prompts)
	require.Equal(t, 1, recoveries)
	require.NoError(t, manager.RecoverManagedDirectAttempts(ctx))
	_, err = durable.ManagedDirectAttempt(ctx, "123e4567-e89b-42d3-a456-426614174099", 1)
	require.ErrorIs(t, err, sql.ErrNoRows, "recovery remains terminal and idempotent")
	prompts, recoveries = rpc.counts()
	require.Equal(t, 1, prompts)
	require.Equal(t, 1, recoveries)
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, durable.Close())
}
