package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
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
func (r *readinessRPC) ApplyPolicy(_ context.Context, envelope map[string]any) (map[string]any, error) {
	r.calls = append(r.calls, "policy")
	return map[string]any{"inlineEnabled": false, "inlineCredits": map[string]any{}, "recallContent": "", "settledEnabled": false, "goalLockEnabled": false, "compactionDeferred": false}, nil
}
func (r *readinessRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	r.calls = append(r.calls, "policy_receipt")
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false}}, nil
}
func (r *readinessRPC) RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error) {
	r.calls = append(r.calls, "child_snapshot")
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": "owner", "sequence": int64(0), "runs": []any{}}, nil
}
func (r *readinessRPC) WaitForChildSnapshot(context.Context) (map[string]any, error) {
	r.calls = append(r.calls, "snapshot_receipt")
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": "owner", "sequence": int64(0), "runs": []any{}}, nil
}
func (r *readinessRPC) Prompt(context.Context, string) error {
	r.calls = append(r.calls, "prompt")
	return nil
}
func (r *readinessRPC) Abort(context.Context) error { return nil }
func (r *readinessRPC) Close() error                { return nil }

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

type recoveryDirectRPC struct {
	launchRPC
	recovery      DirectMailboxRecovery
	recoveryCalls int
	promptCalls   int
}

func (r *recoveryDirectRPC) PrepareManagedMailbox(context.Context) (string, error) {
	return "recovery-cursor", nil
}
func (r *recoveryDirectRPC) PromptManagedMailbox(context.Context, store.ManagedDirectAttempt) (<-chan DirectMailboxEvent, error) {
	r.promptCalls++
	events := make(chan DirectMailboxEvent)
	close(events)
	return events, nil
}
func (r *recoveryDirectRPC) RecoverManagedMailbox(context.Context, store.ManagedDirectAttempt) (DirectMailboxRecovery, error) {
	r.recoveryCalls++
	return r.recovery, nil
}

type recoveryDirectFactory struct {
	process *launchProcess
	rpc     *recoveryDirectRPC
}

func (f recoveryDirectFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.rpc.session = spec.SessionID
	f.rpc.sessionFile = spec.SessionPath
	return f.process, nil
}

type recoveryDirectRPCFactory struct{ rpc *recoveryDirectRPC }

func (f recoveryDirectRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

type failingDirectRPC struct {
	launchRPC
	directMu       sync.Mutex
	directPrompts  int
	directPrompted chan struct{}
	directOnce     sync.Once
}

func (r *failingDirectRPC) PrepareManagedMailbox(context.Context) (string, error) {
	return "direct-cursor", nil
}
func (r *failingDirectRPC) PromptManagedMailbox(context.Context, store.ManagedDirectAttempt) (<-chan DirectMailboxEvent, error) {
	r.directMu.Lock()
	r.directPrompts++
	r.directMu.Unlock()
	if r.directPrompted != nil {
		r.directOnce.Do(func() { close(r.directPrompted) })
	}
	return nil, errors.New("scripted direct prompt failure")
}
func (r *failingDirectRPC) RecoverManagedMailbox(context.Context, store.ManagedDirectAttempt) (DirectMailboxRecovery, error) {
	return DirectMailboxRecovery{}, nil
}
func (r *failingDirectRPC) directPromptCount() int {
	r.directMu.Lock()
	defer r.directMu.Unlock()
	return r.directPrompts
}

type holdingDirectRPC struct {
	launchRPC
	prompted chan struct{}
	events   chan DirectMailboxEvent
	once     sync.Once
}

func (r *holdingDirectRPC) PrepareManagedMailbox(context.Context) (string, error) {
	return "holding-cursor", nil
}
func (r *holdingDirectRPC) PromptManagedMailbox(context.Context, store.ManagedDirectAttempt) (<-chan DirectMailboxEvent, error) {
	r.once.Do(func() { close(r.prompted) })
	return r.events, nil
}
func (r *holdingDirectRPC) RecoverManagedMailbox(context.Context, store.ManagedDirectAttempt) (DirectMailboxRecovery, error) {
	return DirectMailboxRecovery{}, nil
}

type holdingDirectFactory struct {
	process *launchProcess
	rpc     *holdingDirectRPC
}

func (f holdingDirectFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.rpc.session = spec.SessionID
	return f.process, nil
}

type holdingDirectRPCFactory struct{ rpc *holdingDirectRPC }

func (f holdingDirectRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

type replyingDirectRPC struct {
	launchRPC
}

func (r *replyingDirectRPC) PrepareManagedMailbox(context.Context) (string, error) {
	return "reply-cursor", nil
}
func (r *replyingDirectRPC) PromptManagedMailbox(context.Context, store.ManagedDirectAttempt) (<-chan DirectMailboxEvent, error) {
	events := make(chan DirectMailboxEvent, 3)
	events <- DirectMailboxEvent{Receipt: &DirectMailboxReceipt{EntryIdentity: "append-entry"}}
	events <- DirectMailboxEvent{Reply: &DirectMailboxReply{EntryIdentity: "reply-entry", Body: "managed reply works"}}
	events <- DirectMailboxEvent{Settled: true}
	close(events)
	return events, nil
}
func (r *replyingDirectRPC) RecoverManagedMailbox(context.Context, store.ManagedDirectAttempt) (DirectMailboxRecovery, error) {
	return DirectMailboxRecovery{}, nil
}

type replyingDirectFactory struct {
	process *launchProcess
	rpc     *replyingDirectRPC
}

func (f replyingDirectFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.rpc.session = spec.SessionID
	return f.process, nil
}

type replyingDirectRPCFactory struct{ rpc *replyingDirectRPC }

func (f replyingDirectRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

type failingDirectFactory struct {
	process *launchProcess
	rpc     *failingDirectRPC
}

func (f failingDirectFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.rpc.session = spec.SessionID
	return f.process, nil
}

type failingDirectRPCFactory struct{ rpc *failingDirectRPC }

func (f failingDirectRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

func readyFailingDirectManager(t *testing.T) (*Manager, *store.Store, string, *failingDirectRPC) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 993, StartMarker: "direct-start", Incarnation: "direct-incarnation"}, stopped: make(chan struct{})}
	rpc := &failingDirectRPC{launchRPC: launchRPC{active: expectedManagedActiveTools(manifest)}, directPrompted: make(chan struct{})}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: failingDirectFactory{process: process, rpc: rpc}, RPCFactory: failingDirectRPCFactory{rpc: rpc},
		CatalogSource:              CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "direct-agent", Cwd: root, RequestID: "direct-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "direct-agent")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	// Launch schedules a one-time empty direct-attempt recovery after readiness.
	// Let it release the Store writer before this fixture installs a SQLite fault
	// trigger through the independent test connection below.
	time.Sleep(25 * time.Millisecond)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager, durable, path, rpc
}

func TestManagedDirectPromptShowsWorkingUntilSettled(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 995, StartMarker: "holding-start", Incarnation: "holding-incarnation"}, stopped: make(chan struct{})}
	rpc := &holdingDirectRPC{launchRPC: launchRPC{active: expectedManagedActiveTools(manifest)}, prompted: make(chan struct{}), events: make(chan DirectMailboxEvent, 3)}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: holdingDirectFactory{process: process, rpc: rpc}, RPCFactory: holdingDirectRPCFactory{rpc: rpc},
		CatalogSource:              CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	defer manager.Close(context.Background())
	_, err = manager.Launch(ctx, LaunchRequest{Name: "holding-agent", Cwd: root, RequestID: "holding-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "holding-agent")
		return readErr == nil && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	projectionPublications := make(chan struct{}, 2)
	manager.SetProjectionPublisher(func() { projectionPublications <- struct{}{} })
	replyPublished := make(chan DirectMailboxReply, 1)
	manager.SetDirectReplyPublisher(func(_ context.Context, _ store.ManagedDirectAttempt, reply DirectMailboxReply) error {
		replyPublished <- reply
		return nil
	})
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "holding-message", From: "sender", To: "holding-agent", Body: "work", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, "holding-message")
	require.NoError(t, err)
	manager.OfferDirect("holding-agent")
	select {
	case <-rpc.prompted:
	case <-time.After(time.Second):
		t.Fatal("managed direct prompt was not submitted")
	}
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "holding-agent")
		return readErr == nil && agent.ActivityState == "active"
	}, time.Second, time.Millisecond)
	select {
	case <-projectionPublications:
	case <-time.After(time.Second):
		t.Fatal("accepted direct prompt did not publish its working projection")
	}

	rpc.events <- DirectMailboxEvent{Receipt: &DirectMailboxReceipt{EntryIdentity: "holding-append"}}
	rpc.events <- DirectMailboxEvent{Reply: &DirectMailboxReply{EntryIdentity: "holding-reply", Body: "done"}}
	select {
	case reply := <-replyPublished:
		require.Equal(t, "done", reply.Body)
	case <-time.After(time.Second):
		t.Fatal("managed direct reply was not published")
	}
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "holding-agent")
		return readErr == nil && agent.ActivityState == "active"
	}, time.Second, time.Millisecond, "a visible reply must not clear work before the turn settles")
	select {
	case <-projectionPublications:
		t.Fatal("a visible reply cleared the working projection before the turn settled")
	case <-time.After(50 * time.Millisecond):
	}

	rpc.events <- DirectMailboxEvent{Settled: true}
	close(rpc.events)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "holding-agent")
		return readErr == nil && agent.ActivityState == "idle"
	}, time.Second, time.Millisecond)
	select {
	case <-projectionPublications:
	case <-time.After(time.Second):
		t.Fatal("settled direct prompt did not publish its cleared projection")
	}
}

func TestManagedDirectReceiptStreamClosureInterruptsPersistedFinalOnlyAfterDeliveryAck(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 996, StartMarker: "closing-start", Incarnation: "closing-incarnation"}, stopped: make(chan struct{})}
	rpc := &holdingDirectRPC{launchRPC: launchRPC{active: expectedManagedActiveTools(manifest)}, prompted: make(chan struct{}), events: make(chan DirectMailboxEvent, 3)}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: holdingDirectFactory{process: process, rpc: rpc}, RPCFactory: holdingDirectRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	_, err = manager.Launch(ctx, LaunchRequest{Name: "closing-agent", Cwd: root, RequestID: "closing-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "closing-agent")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	agent, err := durable.ManagedAgent(ctx, "closing-agent")
	require.NoError(t, err)
	fence := storedAgentFence(agent)
	published := make(chan struct{}, 1)
	manager.SetDirectReplyPublisher(func(context.Context, store.ManagedDirectAttempt, DirectMailboxReply) error {
		published <- struct{}{}
		return nil
	})
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "closing-message", From: "sender", To: "closing-agent", Body: "work", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, "closing-message")
	require.NoError(t, err)
	manager.OfferDirect("closing-agent")
	select {
	case <-rpc.prompted:
	case <-time.After(time.Second):
		t.Fatal("managed direct prompt was not submitted")
	}
	attempt, err := durable.ManagedDirectAttempt(ctx, "closing-message", 1)
	require.NoError(t, err)
	require.Equal(t, "direct_prompted", attempt.State)
	rpc.events <- DirectMailboxEvent{Receipt: &DirectMailboxReceipt{EntryIdentity: "closing-receipt"}}
	rpc.events <- DirectMailboxEvent{Reply: &DirectMailboxReply{EntryIdentity: "closing-reply", Body: "done"}}
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("managed final was not delivered")
	}
	// Acknowledging the ordinary row deletes it and deliberately cascades its
	// direct-attempt record. Parent work retains the final-delivery audit row.
	_, err = durable.ManagedDirectAttempt(ctx, "closing-message", 1)
	require.ErrorIs(t, err, sql.ErrNoRows)
	pending, err := durable.PendingManagedFinalDeliveries(ctx, fence)
	require.NoError(t, err)
	require.Empty(t, pending, "the publisher receipt closes the final-delivery outbox item")
	_, err = durable.ApplyManagedParentActivity(ctx, fence, store.ManagedParentActivityEvent{Sequence: 1, State: "active"})
	require.NoError(t, err)
	close(rpc.events)
	require.Eventually(t, func() bool {
		db, openErr := sql.Open("sqlite", databasePath)
		if openErr != nil {
			return false
		}
		defer db.Close()
		var state string
		err := db.QueryRow(`SELECT state FROM managed_parent_work WHERE message_id=? AND message_incarnation_id=? AND attempt_number=?`, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber).Scan(&state)
		return err == nil && state == "interrupted"
	}, time.Second, time.Millisecond)
	requireManagedParentTerminalForTest(t, databasePath, *attempt, "interrupted", "delivered")
	current, err := durable.ManagedAgent(ctx, "closing-agent")
	require.NoError(t, err)
	require.Equal(t, "active", current.ActivityState, "native activity remains a separate live source")
	_, err = durable.ApplyManagedParentActivity(ctx, fence, store.ManagedParentActivityEvent{Sequence: 2, State: "idle"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		current, readErr := durable.ManagedAgent(ctx, "closing-agent")
		return readErr == nil && current.ActivityState == "idle"
	}, time.Second, time.Millisecond)
}

func TestManagedDirectReplyPublishesAfterAppendAndBeforeNextOffer(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 994, StartMarker: "reply-start", Incarnation: "reply-incarnation"}, stopped: make(chan struct{})}
	rpc := &replyingDirectRPC{launchRPC: launchRPC{active: expectedManagedActiveTools(manifest)}}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: replyingDirectFactory{process: process, rpc: rpc}, RPCFactory: replyingDirectRPCFactory{rpc: rpc},
		CatalogSource:              CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	defer manager.Close(context.Background())
	_, err = manager.Launch(ctx, LaunchRequest{Name: "reply-agent", Cwd: root, RequestID: "reply-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "reply-agent")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)

	published := make(chan struct {
		attempt store.ManagedDirectAttempt
		reply   DirectMailboxReply
	}, 1)
	manager.SetDirectReplyPublisher(func(_ context.Context, attempt store.ManagedDirectAttempt, reply DirectMailboxReply) error {
		published <- struct {
			attempt store.ManagedDirectAttempt
			reply   DirectMailboxReply
		}{attempt: attempt, reply: reply}
		return nil
	})
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "reply-message", From: "sender", To: "reply-agent", Body: "reply please", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, "reply-message")
	require.NoError(t, err)
	manager.OfferDirect("reply-agent")

	select {
	case result := <-published:
		require.Equal(t, "reply-message", result.attempt.MessageID)
		require.Equal(t, DirectMailboxReply{EntryIdentity: "reply-entry", Body: "managed reply works"}, result.reply)
	case <-time.After(time.Second):
		t.Fatal("managed direct reply was not published")
	}
	require.Eventually(t, func() bool {
		message, readErr := durable.MessageFor(ctx, "reply-message")
		return readErr == nil && message == nil
	}, time.Second, time.Millisecond)
}

func TestManagedDirectPromptStatePersistenceFailureReleasesAttempt(t *testing.T) {
	manager, durable, path, rpc := readyFailingDirectManager(t)
	_, err := durable.Enqueue(context.Background(), protocol.Envelope{ID: "prompt-state-failure", From: "sender", To: "direct-agent", Body: "body", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(context.Background(), "prompt-state-failure")
	require.NoError(t, err)

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	require.Eventually(t, func() bool {
		_, createErr := db.Exec(`CREATE TRIGGER reject_direct_prompt_state BEFORE UPDATE OF state ON managed_direct_delivery_attempts
			WHEN NEW.state = 'direct_prompted' BEGIN SELECT RAISE(FAIL, 'prompt state write failed'); END`)
		return createErr == nil
	}, time.Second, 5*time.Millisecond)
	manager.OfferDirect("direct-agent")

	require.Eventually(t, func() bool {
		attempt, readErr := durable.ManagedDirectAttempt(context.Background(), "prompt-state-failure", 1)
		return readErr == nil && attempt.State == "released" && attempt.FailureCode == "prompt_state_persistence_failed"
	}, time.Second, time.Millisecond)
	require.Zero(t, rpc.directPromptCount(), "a failed prompted-state write must not reach Pi")
}

func TestManagedDirectPromptFailureBeforeAppendReleasesAttempt(t *testing.T) {
	manager, durable, _, rpc := readyFailingDirectManager(t)
	ctx := context.Background()
	_, err := durable.Enqueue(ctx, protocol.Envelope{ID: "pre-append-failure", From: "sender", To: "direct-agent", Body: "body", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, "pre-append-failure")
	require.NoError(t, err)

	manager.OfferDirect("direct-agent")
	require.Eventually(t, func() bool {
		attempt, readErr := durable.ManagedDirectAttempt(ctx, "pre-append-failure", 1)
		return readErr == nil && attempt.State == "released" && attempt.FailureCode == "prompt_failed"
	}, time.Second, time.Millisecond)
	require.Equal(t, 1, rpc.directPromptCount())
}

func TestManagedDirectReleasePersistenceFailureRetriesWithoutStranding(t *testing.T) {
	manager, durable, path, rpc := readyFailingDirectManager(t)
	_, err := durable.Enqueue(context.Background(), protocol.Envelope{ID: "release-persistence-failure", From: "sender", To: "direct-agent", Body: "body", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(context.Background(), "release-persistence-failure")
	require.NoError(t, err)

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	require.Eventually(t, func() bool {
		_, createErr := db.Exec(`CREATE TRIGGER reject_direct_release BEFORE UPDATE OF state ON managed_direct_delivery_attempts
			WHEN NEW.state = 'released' BEGIN SELECT RAISE(FAIL, 'release write failed'); END`)
		return createErr == nil
	}, time.Second, 5*time.Millisecond)
	manager.OfferDirect("direct-agent")
	select {
	case <-rpc.directPrompted:
	case <-time.After(time.Second):
		t.Fatal("direct prompt did not reach its injected failure")
	}
	require.Eventually(t, func() bool {
		var count int
		err := db.QueryRow(`SELECT COUNT(*) FROM managed_direct_delivery_diagnostics WHERE code = 'release_persistence_failed'`).Scan(&count)
		return err == nil && count > 0
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		_, dropErr := db.Exec(`DROP TRIGGER reject_direct_release`)
		return dropErr == nil
	}, 2*time.Second, 5*time.Millisecond, "release retries must not permanently lock persistence recovery")

	require.Eventually(t, func() bool {
		attempt, readErr := durable.ManagedDirectAttempt(context.Background(), "release-persistence-failure", 1)
		return readErr == nil && attempt.State == "released" && attempt.FailureCode == "prompt_failed"
	}, 2*time.Second, time.Millisecond)
	var parentState string
	require.NoError(t, db.QueryRow(`SELECT state FROM managed_parent_work WHERE message_id=? AND attempt_number=1`, "release-persistence-failure").Scan(&parentState))
	require.Equal(t, "interrupted", parentState)
	require.Eventually(t, func() bool {
		history, readErr := durable.ManagedHistoryPage(context.Background(), "direct-agent", "complete", 0, 10)
		if readErr != nil {
			return false
		}
		for _, entry := range history.Entries {
			if entry.Message == "Message delivery failed" {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
}

func TestOfferDirectAndCloseShareTaskAdmission(t *testing.T) {
	manager, _, _, _ := readyFailingDirectManager(t)
	start := make(chan struct{})
	var offers sync.WaitGroup
	for index := 0; index < 64; index++ {
		offers.Add(1)
		go func() {
			defer offers.Done()
			<-start
			for attempt := 0; attempt < 16; attempt++ {
				manager.OfferDirect("direct-agent")
			}
		}()
	}
	closeDone := make(chan error, 1)
	go func() {
		<-start
		closeDone <- manager.Close(context.Background())
	}()
	close(start)
	offers.Wait()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("manager close did not wait for admitted offers")
	}
	manager.OfferDirect("direct-agent")
}

func TestManagedDirectRecoveryWakeup(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	sessionRoot := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	process := &launchProcess{identity: ProcessIdentity{PID: 991, StartMarker: "start", Incarnation: "incarnation"}, stopped: make(chan struct{})}
	rpc := &recoveryDirectRPC{recovery: DirectMailboxRecovery{Receipt: &DirectMailboxReceipt{EntryIdentity: "persisted-append"}}}
	recoveryEntered := make(chan struct{})
	releaseRecovery := make(chan struct{})
	var recoveryHookOnce sync.Once
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: recoveryDirectFactory{process: process, rpc: rpc}, RPCFactory: recoveryDirectRPCFactory{rpc: rpc},
		CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		BeforeManagedDirectRecovery: func(context.Context, string) {
			recoveryHookOnce.Do(func() {
				close(recoveryEntered)
				<-releaseRecovery
			})
		},
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "recovery-agent", Cwd: root, RequestID: "recovery-launch"})
	require.NoError(t, err)
	select {
	case <-recoveryEntered:
	case <-time.After(time.Second):
		t.Fatal("launch did not enter its direct recovery scan")
	}
	close(releaseRecovery)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "recovery-agent")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)

	agent, err := durable.ManagedAgent(ctx, "recovery-agent")
	require.NoError(t, err)
	fence := store.ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "recovery-direct", From: "sender", To: agent.Name, Body: "persisted body", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, "recovery-direct")
	require.NoError(t, err)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *attempt, "recovery-cursor"))
	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- manager.RecoverManagedDirectAttempts(ctx) }()
	select {
	case recoveryErr := <-recoveryDone:
		require.NoError(t, recoveryErr)
	case <-time.After(time.Second):
		t.Fatal("managed direct recovery did not finish")
	}
	require.Eventually(t, func() bool {
		message, readErr := durable.MessageFor(ctx, "recovery-direct")
		return readErr == nil && message == nil
	}, time.Second, time.Millisecond)
	require.Equal(t, 1, rpc.recoveryCalls)
	require.Zero(t, rpc.promptCalls, "a recovered append must not send a second command")
	require.NoError(t, manager.Close(context.Background()))
}

func TestManagedDirectOfferUsesPrivateRPCCapability(t *testing.T) {
	// Direct delivery is deliberately a private capability, not another method
	// on the launch/readiness RPC interface used by existing manager fakes.
	var direct DirectMailboxRPC = &rpcClient{}
	require.NotNil(t, direct)
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
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: admissionRPCFactory{active: active}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	for index := 0; index < 32; index++ {
		_, err = manager.Launch(ctx, LaunchRequest{Name: fmt.Sprintf("agent-%02d", index), Cwd: root, RequestID: fmt.Sprintf("request-%02d", index)})
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
	_, err = manager.Launch(ctx, LaunchRequest{Name: "agent-33", Cwd: root, RequestID: "request-33"})
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
		require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{AgentID: name, Name: name, SlotHeld: true, Cwd: root, SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: "manifest"}, store.ManagedOperation{OperationID: operation, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: operation, CanonicalBodyHash: "body-" + name, ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{"operation_id":"` + operation + `"}`)}))
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

func TestPhase1ReadinessGatesRemainUnchanged(t *testing.T) {
	path := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := LoadManifest(path)
	require.NoError(t, err)
	rpc := &readinessRPC{active: expectedManagedActiveTools(manifest)}
	require.NoError(t, readinessGate(context.Background(), rpc, manifest, "session", 1, "generation", nil))
	require.Equal(t, []string{"get_state", "get_commands", "bootstrap", "receipt", "get_active_tools"}, rpc.calls)
	require.Equal(t, "mecha.bootstrap/v1", rpc.envelope["protocol"])
	require.Equal(t, manifest.Raw, rpc.envelope["desired_manifest"])
	require.Equal(t, manifest.CanonicalSHA256, rpc.envelope["manifest_sha256"])
}

type restartPreparationRPC struct {
	mu                     sync.Mutex
	session                string
	active                 []string
	stateCalls             int
	commandCalls           int
	bootstrapCalls         int
	receiptCalls           int
	activeCalls            int
	prepareStarted         chan struct{}
	releasePrepare         chan struct{}
	preparationError       error
	sessionFile            string
	promptCalls            int
	closeCalls             int
	promptEntered          chan struct{}
	releasePrompt          chan struct{}
	promptContextCancelled chan struct{}
}

func (r *restartPreparationRPC) GetState(ctx context.Context) (map[string]any, error) {
	r.mu.Lock()
	r.stateCalls++
	call := r.stateCalls
	err := r.preparationError
	started, release := r.prepareStarted, r.releasePrepare
	r.mu.Unlock()
	if call > 1 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if started != nil {
			select {
			case <-started:
			default:
				close(started)
			}
		}
		if release != nil {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"sessionId": r.session, "sessionFile": r.sessionFile}, nil
}
func (r *restartPreparationRPC) GetCommands(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commandCalls++
	return append(append([]string{}, hiddenCommands...), legacyCommands...), nil
}
func (r *restartPreparationRPC) Bootstrap(context.Context, map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bootstrapCalls++
	return nil
}
func (r *restartPreparationRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.receiptCalls++
	return nil
}
func (r *restartPreparationRPC) GetActiveTools(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeCalls++
	return append([]string{}, r.active...), nil
}
func (r *restartPreparationRPC) ApplyPolicy(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"inlineEnabled": false, "inlineCredits": map[string]any{}, "recallContent": "", "settledEnabled": false, "goalLockEnabled": false, "compactionDeferred": false}, nil
}
func (r *restartPreparationRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false}}, nil
}
func (r *restartPreparationRPC) RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": r.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (r *restartPreparationRPC) WaitForChildSnapshot(context.Context) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": r.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (r *restartPreparationRPC) Prompt(ctx context.Context, _ string) error {
	r.mu.Lock()
	r.promptCalls++
	entered, release, cancelled := r.promptEntered, r.releasePrompt, r.promptContextCancelled
	r.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			if cancelled != nil {
				close(cancelled)
			}
			<-release
		}
	}
	return nil
}

// SettlePreparedRestartTurn models the native settlement boundary for the
// prompt-admitting restart fixtures. The product path still invokes this
// capability; this fixture has no provider work to drain.
func (r *restartPreparationRPC) SettlePreparedRestartTurn(context.Context, store.ManagedRuntimeFence) error {
	return nil
}
func (r *restartPreparationRPC) Abort(context.Context) error { return nil }
func (r *restartPreparationRPC) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeCalls++
	return nil
}
func (r *restartPreparationRPC) counts() (state, commands, bootstrap, receipt, active, prompts, closes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stateCalls, r.commandCalls, r.bootstrapCalls, r.receiptCalls, r.activeCalls, r.promptCalls, r.closeCalls
}

type restartPreparationFactory struct {
	process *launchProcess
	rpc     *restartPreparationRPC
}

func (f restartPreparationFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.rpc.session = spec.SessionID
	if spec.LaunchMode == CreateNew {
		f.rpc.sessionFile = spec.SessionPath
	}
	return f.process, nil
}

type restartPreparationRPCFactory struct{ rpc *restartPreparationRPC }

func (f restartPreparationRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

func readyRestartManager(t *testing.T, preparationError error, models ...Model) (*Manager, *store.Store, *restartPreparationRPC) {
	t.Helper()
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 321, StartMarker: "restart-start", Incarnation: "restart-incarnation"}, stopped: make(chan struct{})}
	rpc := &restartPreparationRPC{active: expectedManagedActiveTools(manifest), preparationError: preparationError}
	if len(models) == 0 {
		models = []Model{{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}}
	}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{SessionRoot: root, AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: restartPreparationFactory{process: process, rpc: rpc}, RPCFactory: restartPreparationRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(models...), IDSource: deterministic.NewSequenceIDSource("restart-session", "initial-generation", "replacement-generation", "123e4567-e89b-42d3-a456-000000000004"), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "restart-agent", Cwd: root, RequestID: "launch-operation"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "restart-agent")
		if readErr != nil || agent.LifecycleState != "running" {
			return false
		}
		op, opErr := durable.ManagedOperation(ctx, "launch-operation")
		return opErr == nil && op.State == "terminal"
	}, 2*time.Second, time.Millisecond)
	return manager, durable, rpc
}

func TestPromptAndRestartLinearizeAtOneAgentLane(t *testing.T) {
	request := restartRequest()

	t.Run("prompt reaches RPC before reservation", func(t *testing.T) {
		manager, durable, rpc := readyRestartManager(t, nil)
		promptEntered := make(chan struct{})
		releasePrompt := make(chan struct{})
		rpc.promptEntered, rpc.releasePrompt = promptEntered, releasePrompt
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releasePrompt) }) }
		t.Cleanup(func() {
			release()
			require.NoError(t, manager.Close(context.Background()))
		})

		promptDone := make(chan error, 1)
		go func() { promptDone <- manager.AdmitPrompt(context.Background(), request.AgentID, "racing-input") }()
		select {
		case <-promptEntered:
		case <-time.After(time.Second):
			t.Fatal("prompt did not reach the RPC boundary")
		}
		// The prompt keeps the short admission lock while its RPC call is
		// outstanding. This barrier makes the lock ownership observable and
		// catches a regression that leaves prompt admission unlocked.
		admissionProbe := make(chan struct{}, 1)
		go func() {
			manager.laneFor(request.AgentID).admission.Lock()
			admissionProbe <- struct{}{}
			manager.laneFor(request.AgentID).admission.Unlock()
		}()
		select {
		case <-admissionProbe:
			t.Fatal("prompt released admission before its RPC call returned")
		case <-time.After(25 * time.Millisecond):
		}

		restartDone := make(chan struct {
			result RestartResult
			err    error
		}, 1)
		go func() {
			result, err := manager.ReserveRestart(context.Background(), request)
			restartDone <- struct {
				result RestartResult
				err    error
			}{result: result, err: err}
		}()
		// The prompt holds the agent lane at the RPC boundary. The restart
		// reservation cannot return until this prompt releases that lane.
		stored, err := durable.ManagedAgent(context.Background(), request.AgentID)
		require.NoError(t, err)
		require.Equal(t, "running", stored.LifecycleState)
		require.True(t, stored.AcceptingPrompts)

		release()
		select {
		case err := <-promptDone:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("admitted prompt did not finish")
		}
		select {
		case outcome := <-restartDone:
			require.NoError(t, outcome.err)
			require.False(t, outcome.result.Replayed)
		case <-time.After(time.Second):
			t.Fatal("restart reservation did not finish after the prompt")
		}
		stored, err = durable.ManagedAgent(context.Background(), request.AgentID)
		require.NoError(t, err)
		require.Equal(t, "restarting", stored.LifecycleState)
		require.False(t, stored.AcceptingPrompts)
		_, _, _, _, _, prompts, _ := rpc.counts()
		require.Equal(t, 1, prompts)
	})

	t.Run("reservation closes prompt admission", func(t *testing.T) {
		manager, durable, rpc := readyRestartManager(t, nil)
		manager.processOps = &resetProcessOps{present: map[int]bool{321: true}}
		rpc.prepareStarted, rpc.releasePrepare = make(chan struct{}), make(chan struct{})
		var releasePrepareOnce sync.Once
		releasePrepare := func() { releasePrepareOnce.Do(func() { close(rpc.releasePrepare) }) }
		t.Cleanup(func() {
			releasePrepare()
			require.NoError(t, manager.Close(context.Background()))
		})
		result, err := manager.ReserveRestart(context.Background(), request)
		require.NoError(t, err)
		require.False(t, result.Replayed)
		stored, err := durable.ManagedAgent(context.Background(), request.AgentID)
		require.NoError(t, err)
		require.Equal(t, "restarting", stored.LifecycleState)
		require.False(t, stored.AcceptingPrompts)
		select {
		case <-rpc.prepareStarted:
		case <-time.After(time.Second):
			t.Fatal("restart preparation did not start")
		}

		promptDone := make(chan error, 1)
		go func() { promptDone <- manager.AdmitPrompt(context.Background(), request.AgentID, "late-input") }()
		var notReady *store.ManagedAgentNotReadyError
		select {
		case promptErr := <-promptDone:
			require.ErrorAs(t, promptErr, &notReady)
		case <-time.After(time.Second):
			t.Fatal("concurrent prompt did not observe the lifecycle reservation")
		}
		_, _, _, _, _, prompts, _ := rpc.counts()
		require.Zero(t, prompts)
		releasePrepare()
	})
}

func TestAdmitPromptReturnsStoreError(t *testing.T) {
	manager, _, rpc := readyRestartManager(t, nil)
	defer manager.Close(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := manager.AdmitPrompt(ctx, "restart-agent", "must not be admitted")
	require.ErrorIs(t, err, context.Canceled)
	_, _, _, _, _, prompts, _ := rpc.counts()
	require.Zero(t, prompts)
}

func TestReserveManagedRestartAtomicallyDisablesPromptAdmission(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, nil)
	manager.processOps = &resetProcessOps{present: map[int]bool{321: true}}
	defer manager.Close(context.Background())
	require.NoError(t, manager.AdmitPrompt(context.Background(), "restart-agent", "before"))
	rpc.prepareStarted, rpc.releasePrepare = make(chan struct{}), make(chan struct{})
	request := RestartRequest{AgentID: "restart-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "restart-operation", CanonicalBodyHash: "empty-object"}
	result, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	select {
	case <-rpc.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("restart preparation did not start")
	}
	var notReady *store.ManagedAgentNotReadyError
	require.ErrorAs(t, manager.AdmitPrompt(context.Background(), "restart-agent", "after"), &notReady)
	_, _, _, _, _, prompts, _ := rpc.counts()
	require.Equal(t, 1, prompts)
	stored, err := durable.ManagedAgent(context.Background(), "restart-agent")
	require.NoError(t, err)
	require.Equal(t, "restarting", stored.LifecycleState)
	require.False(t, stored.AcceptingPrompts)
	close(rpc.releasePrepare)
}

func TestReserveRestartAtomicallyChangesTheReplacementModel(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, nil,
		Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true},
		Model{Provider: "google", ID: "gemini-2.5-pro", Available: true},
	)
	manager.processOps = &resetProcessOps{present: map[int]bool{321: true}}
	defer manager.Close(context.Background())
	rpc.prepareStarted, rpc.releasePrepare = make(chan struct{}), make(chan struct{})
	request := RestartRequest{AgentID: "restart-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "restart-with-model", CanonicalBodyHash: "new-model", Model: &Model{Provider: "google", ID: "gemini-2.5-pro"}}
	result, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "google", result.Agent.SelectedModel.Provider)
	require.Equal(t, "gemini-2.5-pro", result.Agent.SelectedModel.ID)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(context.Background(), "restart-agent")
		return readErr == nil && agent.LifecycleState == "restarting" && agent.SelectedModel.Provider == "google" && agent.SelectedModel.ID == "gemini-2.5-pro"
	}, time.Second, time.Millisecond)
	close(rpc.releasePrepare)
}

func TestRestartPreparationFailurePersistsExactDurableError(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, errors.New("state failed"))
	manager.processOps = &resetProcessOps{present: map[int]bool{321: true}}
	defer manager.Close(context.Background())
	request := RestartRequest{AgentID: "restart-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "restart-operation", CanonicalBodyHash: "empty-object"}
	_, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, readErr := durable.ManagedLifecycleAction(context.Background(), request.RequestID)
		return readErr == nil && action.State == "failed" && action.TerminalCode == "RESTART_PREPARATION_FAILED"
	}, time.Second, time.Millisecond)
	stored, err := durable.ManagedAgent(context.Background(), "restart-agent")
	require.NoError(t, err)
	require.Equal(t, "running", stored.LifecycleState)
	require.True(t, stored.AcceptingPrompts)
	require.Empty(t, stored.LastErrorCode)
	require.Empty(t, stored.LastErrorMessage)
	op, err := durable.ManagedOperation(context.Background(), request.RequestID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
	states, _, _, _, _, prompts, closes := rpc.counts()
	require.Equal(t, 2, states)
	require.Equal(t, 0, closes)
	require.Equal(t, 0, prompts)
}

func restartRequest() RestartRequest {
	return RestartRequest{AgentID: "restart-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "restart-operation", CanonicalBodyHash: "empty-object"}
}

func TestRestartPreparationDeadlineAndSessionMismatchPersistExactError(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*Manager, *restartPreparationRPC)
	}{
		{name: "deadline", configure: func(manager *Manager, _ *restartPreparationRPC) {
			manager.preparationDeadline = func(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
				require.Equal(t, 5*time.Second, duration)
				child, cancel := context.WithCancel(parent)
				cancel()
				return child, func() {}
			}
		}},
		{name: "session mismatch", configure: func(_ *Manager, rpc *restartPreparationRPC) { rpc.session = "wrong-session" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, durable, rpc := readyRestartManager(t, nil)
			defer manager.Close(context.Background())
			test.configure(manager, rpc)
			_, err := manager.ReserveRestart(context.Background(), restartRequest())
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				action, readErr := durable.ManagedLifecycleAction(context.Background(), restartRequest().RequestID)
				return readErr == nil && action.State == "failed" && action.TerminalCode == "RESTART_PREPARATION_FAILED"
			}, time.Second, time.Millisecond)
			stored, err := durable.ManagedAgent(context.Background(), "restart-agent")
			require.NoError(t, err)
			require.Equal(t, "running", stored.LifecycleState)
			require.True(t, stored.AcceptingPrompts)
			require.Empty(t, stored.LastErrorMessage)
			_, commands, _, _, _, prompts, closes := rpc.counts()
			require.Equal(t, 0, prompts)
			require.Equal(t, 0, closes)
			require.Equal(t, 1, commands)
		})
	}
}

func TestReserveManagedRestartRejectsInvalidLiveBindingWithoutEffects(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Manager, *managedBinding)
	}{
		{name: "group leader", mutate: func(manager *Manager, _ *managedBinding) {
			manager.groupLeader = func(ProcessIdentity) bool { return false }
		}},
		{name: "missing rpc", mutate: func(_ *Manager, binding *managedBinding) { binding.rpc = nil }},
		{name: "mismatched incarnation", mutate: func(_ *Manager, binding *managedBinding) { binding.identity.Incarnation = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, durable, _ := readyRestartManager(t, nil)
			defer manager.Close(context.Background())
			binding, ok := manager.bindingFor("restart-agent")
			require.True(t, ok)
			binding.serial.Lock()
			test.mutate(manager, binding)
			binding.serial.Unlock()
			before, err := durable.GetProjectionVersion(context.Background())
			require.NoError(t, err)
			_, err = manager.ReserveRestart(context.Background(), restartRequest())
			var notReady *store.ManagedAgentNotReadyError
			require.ErrorAs(t, err, &notReady)
			after, err := durable.GetProjectionVersion(context.Background())
			require.NoError(t, err)
			require.Equal(t, before, after)
			_, err = durable.ManagedRestartTransition(context.Background(), "restart-agent")
			require.Error(t, err)
		})
	}
}

func TestRestartTransitionFirstAdmissionRejectsFreshManagerWithoutBinding(t *testing.T) {
	manager, durable, _ := readyRestartManager(t, nil)
	defer manager.Close(context.Background())
	manager.mu.Lock()
	delete(manager.bindings, "restart-agent")
	delete(manager.processes, "restart-agent")
	delete(manager.rpcs, "restart-agent")
	manager.mu.Unlock()
	_, err := manager.ReserveRestart(context.Background(), restartRequest())
	var notReady *store.ManagedAgentNotReadyError
	require.ErrorAs(t, err, &notReady)
	_, err = durable.ManagedRestartTransition(context.Background(), "restart-agent")
	require.Error(t, err)
}

func TestRestartPreparationAttachesArtifactBeforeStoppingSource(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, nil)
	manager.processOps = &resetProcessOps{present: map[int]bool{321: true}}
	defer manager.Close(context.Background())
	_, err := manager.ReserveRestart(context.Background(), restartRequest())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, readErr := durable.ManagedLifecycleAction(context.Background(), restartRequest().RequestID)
		return readErr == nil && action.State == "failed" && action.TargetArtifactID != ""
	}, time.Second, time.Millisecond)
	action, err := durable.ManagedLifecycleAction(context.Background(), restartRequest().RequestID)
	require.NoError(t, err)
	require.Equal(t, "lifecycle", action.ExecutionOwner)
	require.Equal(t, "RESTART_IDENTITY_UNSAFE", action.TerminalCode)
	states, commands, bootstrap, receipt, active, prompts, _ := rpc.counts()
	require.GreaterOrEqual(t, states, 2)
	require.Equal(t, 1, commands)
	require.Equal(t, 1, bootstrap)
	require.Equal(t, 1, receipt)
	require.Equal(t, 1, active)
	require.Zero(t, prompts)
}

func TestReserveManagedRestartReplaysExactOperationWithoutSecondPreparation(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, nil)
	manager.processOps = &resetProcessOps{present: map[int]bool{321: true}}
	defer manager.Close(context.Background())
	rpc.prepareStarted, rpc.releasePrepare = make(chan struct{}), make(chan struct{})
	request := restartRequest()
	first, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	select {
	case <-rpc.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("restart preparation did not start")
	}
	replayed, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, first.Operation.ResponseBody, replayed.Operation.ResponseBody)
	changedPath := request
	changedPath.CanonicalPath += "/other"
	var conflict *store.ManagedIdempotencyConflictError
	_, err = manager.ReserveRestart(context.Background(), changedPath)
	require.ErrorAs(t, err, &conflict)
	changedBody := request
	changedBody.CanonicalBodyHash = "different"
	_, err = manager.ReserveRestart(context.Background(), changedBody)
	require.ErrorAs(t, err, &conflict)
	var response map[string]any
	require.NoError(t, json.Unmarshal(first.Operation.ResponseBody, &response))
	require.Equal(t, request.RequestID, response["operation_id"])
	require.Equal(t, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, first.Operation.ResponseHeaders)
	_, hasPrivatePID := response["agent"].(map[string]any)["process_pid"]
	require.False(t, hasPrivatePID)
	states, _, _, _, _, _, _ := rpc.counts()
	require.Equal(t, 2, states)
	close(rpc.releasePrepare)
	_ = durable
}

func TestRestartRollbackReopensPromptAdmissionAndReplaysImmutableOperation(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, errors.New("preparation failed"))
	manager.processOps = &resetProcessOps{present: map[int]bool{321: true}}
	defer manager.Close(context.Background())
	request := restartRequest()
	first, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		op, readErr := durable.ManagedOperation(context.Background(), request.RequestID)
		return readErr == nil && op.State == "terminal"
	}, time.Second, time.Millisecond)
	require.NoError(t, manager.AdmitPrompt(context.Background(), "restart-agent", "after rollback"))
	replayed, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, first.Operation.ResponseBody, replayed.Operation.ResponseBody)
	op, err := durable.ManagedOperation(context.Background(), request.RequestID)
	require.NoError(t, err)
	require.NotEmpty(t, op.ExpiresAt)
	states, _, _, _, _, prompts, _ := rpc.counts()
	require.Equal(t, 1, prompts)
	require.Equal(t, 2, states)
}

func TestReserveManagedRestartRejectsPendingLaunchOperationWithoutEffects(t *testing.T) {
	manager, durable, _ := readyRestartManager(t, nil)
	defer manager.Close(context.Background())
	binding, ok := manager.bindingFor("restart-agent")
	require.True(t, ok)
	pending := store.ManagedOperation{OperationID: "pending-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "pending-launch", CanonicalBodyHash: "pending", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.PutManagedOperation(context.Background(), pending))
	binding.serial.Lock()
	binding.operationID, binding.lifecycleAction = pending.OperationID, "launch"
	binding.serial.Unlock()
	before, err := durable.GetProjectionVersion(context.Background())
	require.NoError(t, err)
	_, err = manager.ReserveRestart(context.Background(), restartRequest())
	var conflict *store.ManagedLifecycleConflictError
	require.ErrorAs(t, err, &conflict)
	after, err := durable.GetProjectionVersion(context.Background())
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoError(t, durable.MarkManagedOperationTerminal(context.Background(), pending.OperationID))
	binding.serial.Lock()
	binding.lifecycleAction = ""
	binding.serial.Unlock()
	_, err = manager.ReserveRestart(context.Background(), restartRequest())
	require.NoError(t, err)
}

func TestRestartShutdownStopsOnlyTheLifecycleOwnedSource(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, nil)
	manager.processOps = &resetProcessOps{present: map[int]bool{321: true}}
	rpc.prepareStarted, rpc.releasePrepare = make(chan struct{}), make(chan struct{})
	_, err := manager.ReserveRestart(context.Background(), restartRequest())
	require.NoError(t, err)
	select {
	case <-rpc.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("restart preparation did not start")
	}
	close(rpc.releasePrepare)
	require.NoError(t, manager.Close(context.Background()))
	stored, err := durable.ManagedAgent(context.Background(), "restart-agent")
	require.NoError(t, err)
	require.Equal(t, "running", stored.DesiredState)
	require.Equal(t, "restarting", stored.LifecycleState)
	require.Equal(t, "stopped", stored.ActivityState)
	require.False(t, stored.AcceptingPrompts)
	require.True(t, stored.SlotHeld)
	action, err := durable.ManagedLifecycleAction(context.Background(), restartRequest().RequestID)
	require.NoError(t, err)
	require.Equal(t, "lifecycle", action.ExecutionOwner)
	require.Equal(t, "starting_target", action.State)
	require.Equal(t, "target_start", action.Stage)
	_, err = durable.ManagedRestartTransition(context.Background(), "restart-agent")
	require.Error(t, err)
	op, err := durable.ManagedOperation(context.Background(), restartRequest().RequestID)
	require.NoError(t, err)
	require.Equal(t, "pending", op.State)
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
	require.Error(t, readinessGate(context.Background(), rpc, manifest, "session", 1, "generation", nil))
	require.NotContains(t, rpc.calls, "prompt")
}
