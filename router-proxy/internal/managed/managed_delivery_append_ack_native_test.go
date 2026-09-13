package managed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type appendAckNativeProcess struct{ identity ProcessIdentity }

func (p *appendAckNativeProcess) Identity() ProcessIdentity { return p.identity }
func (p *appendAckNativeProcess) Stdin() io.WriteCloser     { return nopWriteCloser{io.Discard} }
func (p *appendAckNativeProcess) Stdout() io.ReadCloser     { return io.NopCloser(strings.NewReader("")) }
func (*appendAckNativeProcess) Wait() error                 { select {} }
func (*appendAckNativeProcess) Stop(context.Context) error  { return nil }

type appendAckNativeRPC struct {
	launchRPC
	mu            sync.Mutex
	prompts       int
	recoveryCalls int
	appendID      string
	body          string
	interrupt     func()
	recoveryGate  func()
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
	r.recoveryCalls++
	call := r.recoveryCalls
	gate := r.recoveryGate
	r.mu.Unlock()
	if gate != nil {
		gate()
	}
	return DirectMailboxRecovery{Receipt: &DirectMailboxReceipt{EntryIdentity: fmt.Sprintf("%s-recovery-%d", r.appendID, call)}}, nil
}
func (r *appendAckNativeRPC) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prompts, r.recoveryCalls
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
	ackInterrupted := make(chan struct{})
	startupRecoveryEntered := make(chan struct{})
	startupRecoveryRelease := make(chan struct{})
	var startupRecoveryOnce sync.Once
	newManager := func(gateStartup bool) *Manager {
		manager, managerErr := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, ProcessFactory: factory, RPCFactory: appendAckNativeRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "mock", ID: "mock", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: &coldRecoveryOps{states: map[int]ProcessProbeState{4101: ProcessOwned}}, BeforeManagedDirectRecovery: func(context.Context, string) {
			if gateStartup {
				startupRecoveryOnce.Do(func() { close(startupRecoveryEntered); <-startupRecoveryRelease })
			}
		}, BeforeManagedDirectAcknowledge: func(context.Context, store.ManagedDirectAttempt) error {
			defer close(ackInterrupted)
			return errors.New("drop before message_end ACK")
		}})
		require.NoError(t, managerErr)
		return manager
	}
	manager := newManager(true)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "append-agent", Cwd: paths["work"], RequestID: "append-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { a, e := durable.ManagedAgent(ctx, "append-agent"); return e == nil && a.AcceptingPrompts }, time.Second, time.Millisecond)
	// Hold the launcher's automatic scan until readiness is visible, then release
	// it and wait on an explicit scan of the same lane. This drains both triggers
	// before creating the crash-boundary row; no scheduler timing or sleep is used.
	select {
	case <-startupRecoveryEntered:
	case <-time.After(time.Second):
		t.Fatal("startup recovery scan did not reach its barrier")
	}
	close(startupRecoveryRelease)
	require.NoError(t, manager.RecoverManagedDirectAttempts(ctx))
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
	select {
	case <-ackInterrupted:
	case <-time.After(time.Second):
		t.Fatal("prompt did not reach the interrupted ACK boundary")
	}
	agent, err := durable.ManagedAgent(ctx, "append-agent")
	require.NoError(t, err)
	// Keep the ordinary row alive after the first recovered ACK. A recurring
	// reminder owned by another recipient makes a losing ACK observable: the
	// state guard must reject it while the message still exists.
	rawDB, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer rawDB.Close()
	_, err = rawDB.ExecContext(ctx, `PRAGMA busy_timeout=5000`)
	require.NoError(t, err)
	_, err = rawDB.ExecContext(ctx, `INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?) ON CONFLICT(agent_id) DO NOTHING`, "other-agent", time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	_, err = rawDB.ExecContext(ctx, `INSERT INTO reminders(id,target,creator,body,created_at,next_due_at,interval_ms,state,linked_message_id,occurrence_number,attempt_count) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "duplicate-claim-reminder", "other-agent", "sender", "reminder", time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), 1000, "scheduled", "123e4567-e89b-42d3-a456-426614174099", 1, 0)
	require.NoError(t, err)
	// Cancel the old router without marking the still-running Pi session stopped.
	// The replacement manager reattaches the same durable fence before recovery.
	manager.cancel()

	manager = newManager(false)
	binding := &managedBinding{agentID: agent.AgentID, lane: manager.laneFor(agent.AgentID), process: process, rpc: rpc, identity: process.Identity(), sessionID: agent.PiSessionID, sessionEpoch: agent.SessionEpoch, runtimeGeneration: agent.RuntimeGeneration, manifestRevision: agent.ManifestRevision, manifestHash: agent.ManifestHash}
	require.True(t, manager.installBinding(binding))
	duplicate := newManager(false)
	duplicateBinding := &managedBinding{agentID: agent.AgentID, lane: duplicate.laneFor(agent.AgentID), process: process, rpc: rpc, identity: process.Identity(), sessionID: agent.PiSessionID, sessionEpoch: agent.SessionEpoch, runtimeGeneration: agent.RuntimeGeneration, manifestRevision: agent.ManifestRevision, manifestHash: agent.ManifestHash}
	require.True(t, duplicate.installBinding(duplicateBinding))
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	rpc.recoveryGate = func() { entered <- struct{}{}; <-release }
	results := make(chan error, 2)
	go func() { results <- manager.RecoverManagedDirectAttempts(ctx) }()
	select {
	case <-entered:
	case err := <-results:
		require.NoError(t, err)
		t.Fatal("first recovery scan completed without reaching the side-effect barrier")
	case <-time.After(time.Second):
		t.Fatal("first recovery scan did not reach the side-effect barrier")
	}
	go func() { results <- duplicate.RecoverManagedDirectAttempts(ctx) }()
	for range 1 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("duplicate recovery scan did not reach the side-effect barrier")
		}
	}
	close(release)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	stored, err := durable.ManagedDirectAttempt(ctx, "123e4567-e89b-42d3-a456-426614174099", 1)
	require.NoError(t, err)
	require.Equal(t, "acknowledged", stored.State)
	require.Contains(t, stored.ReceiptEntryIdentity, "pi-entry-fixed-recovery-")
	message, err := durable.MessageFor(ctx, "123e4567-e89b-42d3-a456-426614174099")
	require.NoError(t, err)
	require.NotNil(t, message)
	history, err := durable.ConversationHistory(ctx, "sender", "append-agent")
	require.NoError(t, err)
	require.Empty(t, history)
	// The second claimant must lose the durable state transition. Removing the
	// state='direct_prompted' guard makes this call overwrite the claimed receipt.
	require.Error(t, durable.AcknowledgeManagedDirectRecovered(ctx, *stored, "late-conflicting-receipt"))
	prompts, recoveryCalls := rpc.counts()
	require.Equal(t, 1, prompts)
	// This counts recovery-function entries from automatic and explicit scans,
	// not claimed effects. The durable row, prompt receipt, and final settlement
	// above prove that only one append was claimed and acknowledged.
	require.GreaterOrEqual(t, recoveryCalls, 1)
	require.LessOrEqual(t, recoveryCalls, 2)
	require.NoError(t, manager.RecoverManagedDirectAttempts(ctx))
	storedAgain, err := durable.ManagedDirectAttempt(ctx, "123e4567-e89b-42d3-a456-426614174099", 1)
	require.NoError(t, err)
	require.Equal(t, stored.ReceiptEntryIdentity, storedAgain.ReceiptEntryIdentity, "recovery remains terminal and idempotent")
	prompts, recoveryCalls = rpc.counts()
	require.Equal(t, 1, prompts)
	require.GreaterOrEqual(t, recoveryCalls, 1)
	require.LessOrEqual(t, recoveryCalls, 2)
	require.NoError(t, duplicate.Close(ctx))
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, durable.Close())
}
