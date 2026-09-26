package managed

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var ErrReadinessBacklogFull = errors.New("managed readiness receipt backlog is full")
var ErrSemanticQueueFull = errors.New("managed RPC semantic dispatcher queue is full")
var ErrAbortResponseMalformed = errors.New("Pi Abort response is malformed")
var ErrAbortResponseRejected = errors.New("Pi Abort response was not successful")
var ErrAbortResponseUnavailable = errors.New("Pi Abort response was unavailable after reader termination")
var ErrManagedGoalEvaluationCleanupStale = errors.New("managed goal evaluation cleanup token is stale")
var ErrManagedGoalEvaluationSettledNotObserved = errors.New("managed goal evaluation settled event was not observed")
var rootControlUUIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type RPC interface {
	GetState(context.Context) (map[string]any, error)
	GetCommands(context.Context) ([]string, error)
	Bootstrap(context.Context, map[string]any) error
	WaitForReceipt(context.Context, string, string, int, int64, string) error
	GetActiveTools(context.Context) ([]string, error)
	Prompt(context.Context, string) error
	// Abort asks Pi to stop the current turn. Callers must still reap the
	// exact owned process group when this RPC times out or fails.
	Abort(context.Context) error
	Close() error
}

// readinessSourceRPC is the private Phase 2 source command seam. Keeping it
// narrower than RPC preserves existing Phase 1 test doubles and contracts.
type readinessSourceRPC interface {
	ApplyPolicy(context.Context, map[string]any) (map[string]any, error)
	WaitForPolicyApplied(context.Context, string, int64) (map[string]any, error)
	RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error)
	WaitForChildSnapshot(context.Context) (map[string]any, error)
}

// ManagedGoalEvaluationRPC is the narrow internal evaluator channel. It uses
// the existing policy command and custom receipt type, but accepts no model
// input beyond the router-fenced goal evaluation envelope.
type ManagedGoalEvaluationRPC interface {
	StartManagedGoalEvaluation(context.Context, store.ManagedRuntimeFence, store.ManagedGoalPolicy) error
	WaitForManagedGoalEvaluation(context.Context) (string, string, error)
}

// ManagedGoalEvaluationCleanupRPC is the evaluator-only Abort/reconcile seam.
// Public Abort remains side-effect free because lifecycle termination does not
// own an evaluator suppression lease.
type ManagedGoalEvaluationCleanupRPC interface {
	ReconcileManagedGoalEvaluation(context.Context, store.ManagedRuntimeFence, string, int64, int64) error
}

type managedGoalEvaluationCleanupStateRPC interface {
	ManagedGoalEvaluationCleanupPending() bool
}

// NativeModelRPC is stock Pi's in-place set_model command. It stays narrower
// than RPC so non-native test and legacy bindings remain source-compatible.
type NativeModelRPC interface {
	SetModel(context.Context, string, string) (Model, error)
}

// RestartTurnSettler is deliberately narrower than RPC. Existing launch-only
// callers remain source-compatible; controlled active restart requires this
// binding-scoped stdout capability.
type RestartTurnSettler interface {
	SettlePreparedRestartTurn(context.Context, store.ManagedRuntimeFence) error
}

// RestartTurnAdmissionRPC atomically records the source-turn watermark with
// its prompt write. The settlement waiter can then consume the exact decoded
// native boundary even if durable activity has not caught up yet.
type RestartTurnAdmissionRPC interface {
	PromptAdmittedRestartTurn(context.Context, string, store.ManagedRuntimeFence) error
}

// DirectMailboxRPC is private to managed delivery. It deliberately does not
// widen RPC, so launch and readiness callers cannot issue router mail.
type DirectMailboxRPC interface {
	PromptManagedMailbox(context.Context, store.ManagedDirectAttempt) (<-chan DirectMailboxEvent, error)
	RecoverManagedMailbox(context.Context, store.ManagedDirectAttempt) (DirectMailboxRecovery, error)
}

// RootControlEvent is emitted only for a complete hidden root request. The
// reader validates its closed transport envelope; manager admission performs
// the binding, manifest tuple, and schema checks before storage.
type RootControlEvent struct {
	RequestID, ToolCallID, CapabilityID, ToolName, DefinitionID, DefinitionSHA256, OperationID string
	ManifestSHA256, RuntimeGeneration                                                          string
	SessionEpoch                                                                               int64
	Arguments                                                                                  any
	Watermark                                                                                  uint64
}

type RootControlResultReceipt struct {
	Event     map[string]any
	Watermark uint64
}

type RootControlPersistedReceipt struct {
	ReceiptID, RequestID, ResultID, PayloadSHA256 string
	RuntimeGeneration, ManifestSHA256             string
	SessionEpoch                                  int64
}

type RootControlAppendToken interface {
	Watermark() uint64
	Release()
}

// RootControlRPC is a private S4 capability. Transcript authority stays in the
// extension and crosses this boundary only as one closed, fenced receipt.
type RootControlRPC interface {
	SetRootControlEventHandler(func(RootControlEvent))
	ArmRootControlResult(requestID, entryID string) (RootControlAppendToken, error)
	SubmitRootControlResult(context.Context, RootControlAppendToken, map[string]any) (<-chan RootControlResultReceipt, error)
	ReconcileRootControlResult(context.Context, map[string]any) (RootControlPersistedReceipt, error)
}

// ChildDeltaRPC receives post-readiness child lifecycle deltas. Keeping the
// callback separate from readiness receipts means delayed lifecycle events
// cannot satisfy or poison the launch gate.
type ChildDeltaRPC interface {
	SetChildDeltaEventHandler(func(map[string]any))
}

// ParentCompactionRPC receives the installed Pi's structured compaction
// lifecycle. It is separate from child state so parent failures cannot be
// mistaken for a nested child transition.
type ParentCompactionRPC interface {
	SetParentCompactionEventHandler(func(map[string]any))
}

// ParentCompactionNotificationRPC receives the persisted visible handoff Pi
// emits after a successful native compaction. It is separate from the root
// control response, which only proves that Pi accepted the hidden command.
type ParentCompactionNotificationRPC interface {
	SetParentCompactionNotificationHandler(func(map[string]any))
}

// NativeCompactionRPC is intentionally private to the managed root-control
// bridge. It invokes Pi's typed compact RPC command, which is the only path
// that can produce the native compaction lifecycle events used by the UI.
type NativeCompactionRPC interface {
	Compact(context.Context, string) error
	PostCompactionNotification(context.Context, map[string]any) error
}

// ParentActivityRPC receives Pi's native parent-turn start and settled events.
// The router uses it for the durable managed-agent activity projection; prompt
// acceptance itself is not evidence that Pi started work.
type ParentActivityRPC interface {
	SetParentActivityEventHandler(func(map[string]any))
}

// ParentActivitySequenceRPC exposes the reader's decoded stdout watermark for
// prompt admission. It is separate from the callback so queued semantic work
// cannot make admission mistake callback progress for stream progress.
type ParentActivitySequenceRPC interface {
	ParentActivitySequence() uint64
}

// BuiltinToolLifecycleEvent is the narrow native Pi lifecycle fact. Start
// events retain decoded input for Complete-history details; results, output,
// and diagnostics remain excluded.
type BuiltinToolLifecycleEvent struct {
	Phase      string
	ToolCallID string
	ToolName   string
	ToolInput  any
	IsError    bool
	Watermark  uint64
}

// BuiltinToolLifecycleRPC exposes only the ordered native builtin stream. It
// is not a root-control or custom-message channel.
type BuiltinToolLifecycleRPC interface {
	SetBuiltinToolLifecycleEventHandler(func(BuiltinToolLifecycleEvent))
}

// RPCReaderTerminalRPC reports a terminal stdout reader failure. It is not a
// process-exit signal: callers may reconcile telemetry without changing the
// process lifecycle.
type RPCReaderTerminalRPC interface {
	SetRPCReaderTerminalHandler(func(error))
}

// QualifiedGoalActivityRPC remains the existing general qualified-activity
// seam. Native builtin completion uses it only after its terminal lifecycle
// transition commits, with the durable Complete entry ID as its identity.
type QualifiedGoalActivityRPC interface {
	SetQualifiedGoalActivityEventHandler(func(store.ManagedGoalActivity))
}

type DirectMailboxReceipt struct{ EntryIdentity string }

// DirectMailboxReply is the final visible assistant text from one direct
// mailbox turn. Its entry identity makes downstream persistence idempotent.
type DirectMailboxReply struct {
	EntryIdentity string
	Body          string
}

type DirectMailboxEvent struct {
	Receipt        *DirectMailboxReceipt
	Reply          *DirectMailboxReply
	Settled        bool
	DiagnosticCode string
}

type DirectMailboxRecovery struct {
	Receipt     *DirectMailboxReceipt
	Diagnostics []string
}

type RPCFactory interface {
	Open(context.Context, Process) (RPC, error)
}
type RPCFactoryFunc func(context.Context, Process) (RPC, error)

func (f RPCFactoryFunc) Open(ctx context.Context, process Process) (RPC, error) {
	return f(ctx, process)
}

type receiptNotification struct {
	sequence uint64
	value    map[string]any
}

type restartSettlementToken struct {
	fence     store.ManagedRuntimeFence
	watermark uint64
	consumed  bool
}

type admittedRestartTurn struct {
	fence           store.ManagedRuntimeFence
	watermark       uint64
	settledSequence uint64
}
type managedGoalEvaluationToken struct {
	evaluationID    string
	goalRevision    int64
	settled         int64
	fence           store.ManagedRuntimeFence
	watermark       uint64
	settledObserved bool
	admitted        bool
}

type managedGoalEvaluationCleanup struct {
	token                 *managedGoalEvaluationToken
	abortBoundary         bool
	abortBoundarySequence uint64
	barrier               <-chan struct{}
}
type directRootControlResultStatus uint8

const (
	directRootControlResultSuccess directRootControlResultStatus = iota + 1
	directRootControlResultError
)

type directMailboxToken struct {
	messageID             string
	generation            string
	manifestHash          string
	epoch                 int64
	watermark             uint64
	receiptSeen           bool
	receipt               *DirectMailboxReceipt
	settledSequence       uint64
	settledReleased       bool
	acceptedRootFollowUps uint
	// Root controls are completed on a later Pi follow-up turn. Retain direct
	// ownership across the provisional settle so WebUI stays working until the
	// result has driven that turn's final response.
	pendingRootRequests map[string]struct{}
	reply               *DirectMailboxReply
	result              chan directMailboxResult
}
type directMailboxResult struct {
	receipt        *DirectMailboxReceipt
	reply          *DirectMailboxReply
	settled        bool
	diagnosticCode string
	err            error
}
type rootResultToken struct {
	client             *rpcClient
	requestID, entryID string
	watermark          uint64
	result             chan RootControlResultReceipt
	closed             bool
}

func (t *rootResultToken) Watermark() uint64 {
	if t == nil {
		return 0
	}
	return t.watermark
}
func (t *rootResultToken) Release() {
	if t == nil || t.client == nil {
		return
	}
	t.client.mu.Lock()
	if t.client.rootResult == t {
		t.client.rootResult = nil
	}
	t.closed = true
	t.client.mu.Unlock()
}

type rpcResponse struct {
	value map[string]any
	err   error
}

type rpcResponseHook func(map[string]any, error) error

const rpcSemanticQueueCapacity = 128

type rpcSemanticTask struct {
	fn func()
}

type rpcClient struct {
	stdin   io.Writer
	stdout  io.Reader
	closeFn func() error

	mu              sync.Mutex
	pending         map[string]chan rpcResponse
	pendingCommands map[string]string
	pendingHooks    map[string]rpcResponseHook
	nextID          int64
	activeTools     []string

	// The matcher is owned under mu. Responses never occupy this bounded FIFO.
	// A terminal error lives separately so a retained notification can never
	// satisfy a readiness wait after EOF, close, or process failure.
	eventSequence                       uint64
	backlog                             []receiptNotification
	terminal                            error
	notified                            chan struct{}
	bootstrapWatermark                  *uint64
	policyWatermark                     *uint64
	snapshotWatermark                   *uint64
	settlementToken                     *restartSettlementToken
	admittedRestartTurn                 *admittedRestartTurn
	consumedAdmittedRestartFence        *store.ManagedRuntimeFence
	goalEvaluationToken                 *managedGoalEvaluationToken
	goalEvaluationCleanup               *managedGoalEvaluationCleanup
	directMailbox                       *directMailboxToken
	rootControlHandler                  func(RootControlEvent)
	childDeltaHandler                   func(map[string]any)
	parentCompactionHandler             func(map[string]any)
	parentCompactionNotificationHandler func(map[string]any)
	parentActivityHandler               func(map[string]any)
	builtinToolLifecycleHandler         func(BuiltinToolLifecycleEvent)
	readerTerminalHandler               func(error)
	readerTerminalHandlerCalled         bool
	readerTerminal                      error
	suppressNextEvaluatorSettled        bool
	evaluatorSettledObserved            bool
	rootResult                          *rootResultToken

	// Semantic handlers run on one bounded FIFO. The stdout reader only parses,
	// classifies, and enqueues; a handler may wait for a later receipt without
	// preventing the reader from consuming that receipt. Queue admission and
	// shutdown both happen under mu so overflow and close cannot race a send.
	semanticQueue       chan rpcSemanticTask
	semanticQueueClosed bool
	semanticDone        chan struct{}

	readerDone          chan struct{}
	terminalHandlerDone chan struct{}
	stdoutCloseOnce     sync.Once

	closeOnce sync.Once
	closeErr  error
}

func NewRPCClient(stdin io.Writer, stdout io.Reader, closeFn func() error) RPC {
	terminalHandlerDone := make(chan struct{})
	close(terminalHandlerDone)
	client := &rpcClient{
		stdin: stdin, stdout: stdout, closeFn: closeFn, pending: map[string]chan rpcResponse{}, pendingCommands: map[string]string{}, pendingHooks: map[string]rpcResponseHook{}, notified: make(chan struct{}, 1),
		semanticQueue: make(chan rpcSemanticTask, rpcSemanticQueueCapacity), semanticDone: make(chan struct{}),
		readerDone: make(chan struct{}), terminalHandlerDone: terminalHandlerDone,
	}
	go client.semanticDispatchLoop()
	go client.readLoop()
	return client
}

// semanticDispatchLoop is the single execution lane for callbacks whose
// meaning depends on neighboring stdout records. Terminalization closes the
// queue only after failing waiters; every task already admitted is still
// drained in stdout order. A handler panic is reported through the same reader
// terminal path so the manager can reconcile the lost stream.
func (c *rpcClient) semanticDispatchLoop() {
	defer close(c.semanticDone)
	for task := range c.semanticQueue {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					c.readerTerminalize(fmt.Errorf("managed RPC semantic handler panicked: %v", recovered))
				}
			}()
			task.fn()
		}()
	}
}

func (c *rpcClient) enqueueSemantic(task func()) bool {
	if task == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enqueueSemanticLocked(task)
}

func (c *rpcClient) enqueueSemanticLocked(task func()) bool {
	return c.enqueueSemanticTaskLocked(rpcSemanticTask{fn: task})
}

func (c *rpcClient) enqueueSemanticTaskLocked(task rpcSemanticTask) bool {
	if task.fn == nil {
		return true
	}
	if c.terminal != nil || c.semanticQueueClosed {
		return false
	}
	select {
	case c.semanticQueue <- task:
		return true
	default:
		c.readerTerminalizeLocked(ErrSemanticQueueFull)
		return false
	}
}

func (c *rpcClient) readLoop() {
	defer close(c.readerDone)
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 4096), MaxRPCLineBytes+1)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > MaxRPCLineBytes {
			c.readerTerminalize(errors.New("RPC line exceeds 1 MiB"))
			return
		}
		decoded, err := strictJSON(line)
		raw, ok := decoded.(map[string]any)
		if err != nil || !ok {
			c.readerTerminalize(errors.New("Pi emitted malformed RPC JSON"))
			return
		}
		if c.routeMalformedAbortResponse(raw) {
			continue
		}
		if id, ok := raw["id"].(string); ok && validRPCResponseEnvelope(raw) {
			command := raw["command"].(string)
			c.mu.Lock()
			expectedCommand, hasExpectedCommand := c.pendingCommands[id]
			ch, hasPending := c.pending[id]
			if hasPending && hasExpectedCommand && expectedCommand == command {
				delete(c.pending, id)
				delete(c.pendingCommands, id)
				hook := c.pendingHooks[id]
				delete(c.pendingHooks, id)
				var responseErr error
				if value, hasError := raw["error"]; hasError && value != nil {
					responseErr = fmt.Errorf("Pi RPC error: %v", value)
				}
				if hook != nil {
					if hookErr := hook(raw, responseErr); hookErr != nil {
						responseErr = hookErr
					}
				}
				c.mu.Unlock()
				if ch != nil {
					ch <- rpcResponse{value: raw, err: responseErr}
				}
				// A response belongs only to its request-ID waiter. Timed-out and
				// unknown responses are never notifications, so they cannot consume
				// the bounded readiness receipt FIFO.
				continue
			}
			if !hasPending {
				// A valid response for an unknown request is discarded. A response
				// for a known request with the wrong command falls through to the
				// bounded backlog below as malformed traffic.
				c.mu.Unlock()
				continue
			}
			// The request ID remains the routing key, but a response for a
			// different command is malformed traffic, not its completion.
			c.mu.Unlock()
		}
		var rootCallback func(RootControlEvent)
		var rootEvent *RootControlEvent
		var childDeltaCallback func(map[string]any)
		var childDelta map[string]any
		var parentCompactionCallback func(map[string]any)
		var parentCompaction map[string]any
		var parentCompactionNotificationCallback func(map[string]any)
		var parentCompactionNotification map[string]any
		var parentActivityCallback func(map[string]any)
		var parentActivity map[string]any
		var parentActivityCandidate bool
		var builtinToolLifecycleCallback func(BuiltinToolLifecycleEvent)
		var builtinToolLifecycleEvent *BuiltinToolLifecycleEvent
		var evaluatorSettledSuppressed bool
		c.mu.Lock()
		terminal := c.terminal != nil
		if !terminal {
			c.eventSequence++
			if event, candidate := classifyBuiltinToolLifecycleEvent(raw, c.eventSequence); candidate {
				// Tool lifecycle is an anti-poison semantic channel, never a readiness
				// receipt. Invalid/future shapes are discarded without payload logging.
				if event != nil && c.builtinToolLifecycleHandler != nil {
					builtinToolLifecycleCallback, builtinToolLifecycleEvent = c.builtinToolLifecycleHandler, event
				}
				c.mu.Unlock()
				if builtinToolLifecycleCallback != nil && !c.enqueueSemantic(func() {
					builtinToolLifecycleCallback(*builtinToolLifecycleEvent)
				}) {
					return
				}
				continue
			}
			if event, candidate := classifyRootControlEvent(raw, c.eventSequence); candidate {
				// Root control is an anti-poison control-plane channel, not a readiness
				// receipt. Malformed/future candidates never enter that bounded FIFO.
				if event != nil {
					c.noteDirectRootControlRequestLocked(event)
					if c.rootControlHandler != nil {
						rootCallback, rootEvent = c.rootControlHandler, event
					}
				}
				c.mu.Unlock()
				if rootCallback != nil && !c.enqueueSemantic(func() {
					rootCallback(*rootEvent)
				}) {
					return
				}
				continue
			}
			if delta, candidate := classifyChildDeltaEvent(raw); candidate && delta != nil {
				// Valid lifecycle deltas stay outside the readiness receipt backlog.
				// A malformed candidate remains observable in the bounded alarm FIFO.
				if c.childDeltaHandler != nil {
					childDeltaCallback, childDelta = c.childDeltaHandler, delta
				}
				c.mu.Unlock()
				if childDeltaCallback != nil && !c.enqueueSemantic(func() {
					childDeltaCallback(childDelta)
				}) {
					return
				}
				continue
			}
			if event, candidate := classifyParentCompactionEvent(raw, c.eventSequence); candidate && event != nil {
				if c.parentCompactionHandler != nil {
					parentCompactionCallback, parentCompaction = c.parentCompactionHandler, event
				}
				c.mu.Unlock()
				if parentCompactionCallback != nil && !c.enqueueSemantic(func() {
					parentCompactionCallback(parentCompaction)
				}) {
					return
				}
				continue
			}
			if event, candidate := classifyParentCompactionNotificationEvent(raw, c.eventSequence); candidate && event != nil {
				if c.parentCompactionNotificationHandler != nil {
					parentCompactionNotificationCallback, parentCompactionNotification = c.parentCompactionNotificationHandler, event
				}
				c.mu.Unlock()
				if parentCompactionNotificationCallback != nil && !c.enqueueSemantic(func() {
					parentCompactionNotificationCallback(parentCompactionNotification)
				}) {
					return
				}
				continue
			}
			if isUnboundPostCompactionNotification(raw) {
				c.mu.Unlock()
				continue
			}
			admittedSettlementCaptured := false
			if agentSettledFrame(raw) && c.admittedRestartTurn != nil && c.eventSequence > c.admittedRestartTurn.watermark && c.admittedRestartTurn.settledSequence == 0 {
				// This is recorded in stdout order before the semantic callback can
				// be delayed behind durable activity projection. It is binding-scoped
				// by the full fence captured atomically with the prompt write.
				c.admittedRestartTurn.settledSequence = c.eventSequence
				admittedSettlementCaptured = true
				c.signalLocked()
			}
			if event, candidate := classifyParentActivityEvent(raw, c.eventSequence); candidate && event != nil {
				parentActivityCandidate = true
				cleanupBoundary := c.goalEvaluationCleanup != nil && c.goalEvaluationCleanup.abortBoundary
				if event != nil && event["state"] == "active" && !cleanupBoundary {
					if c.goalEvaluationToken != nil {
						c.goalEvaluationToken.admitted = true
					}
					if c.goalEvaluationCleanup != nil && c.goalEvaluationCleanup.token != nil {
						c.goalEvaluationCleanup.token.admitted = true
					}
				}
				if event != nil && event["state"] == "idle" && c.suppressNextEvaluatorSettled && !cleanupBoundary {
					// The hidden evaluator owns this model turn. Its settled boundary
					// must never become an ordinary goal/rearm trigger. Pi emits that
					// lifecycle frame before the evaluator's policy receipt, so arm this
					// suppression when the evaluator starts and remember that the frame
					// was consumed before the receipt waiter runs.
					c.suppressNextEvaluatorSettled = false
					c.evaluatorSettledObserved = true
					if c.goalEvaluationToken != nil {
						c.goalEvaluationToken.settledObserved = true
					}
					if c.goalEvaluationCleanup != nil && c.goalEvaluationCleanup.token != nil {
						c.goalEvaluationCleanup.token.settledObserved = true
					}
					parentActivityCandidate = false
					evaluatorSettledSuppressed = true
				} else if event != nil && c.parentActivityHandler != nil {
					parentActivityCallback, parentActivity = c.parentActivityHandler, event
				}
			}
			if token := c.rootResult; token != nil && classifyRootControlResultEvent(raw, token, c.eventSequence) {
				select {
				case token.result <- RootControlResultReceipt{Event: raw, Watermark: c.eventSequence}:
				default:
				}
				c.signalLocked()
				c.mu.Unlock()
				continue
			}
			if evaluatorSettledSuppressed {
				c.mu.Unlock()
				continue
			}
			if isRootControlResultEvent(raw) {
				// Exact entry lookup verifies the persisted result. The entry event
				// itself is not a general readiness receipt.
				c.mu.Unlock()
				continue
			}
			// Readiness receipts are consumed by their fenced waiters, even while a
			// semantic callback remains open for a root-control follow-up. The
			// reader never waits on that callback, so a later policy receipt can
			// still signal and unblock it.
			if token := c.directMailbox; token != nil && !knownReadinessReceipt(receiptCustomType(raw)) {
				// A lifecycle executor may arm settlement while direct delivery is
				// paused between the append receipt and its durable ACK. Let the
				// settlement waiter own the native boundary in that case; otherwise
				// the paused direct matcher would consume it and hide it from restart.
				if !(agentSettledType(raw) && c.settlementToken != nil) {
					if result, handled := c.classifyDirectMailboxEvent(raw, token); handled {
						// A native settled boundary is provisional until the matching
						// append receipt is observed and any root-control follow-up is
						// durable. Do not advertise parent idle early.
						deferParentIdle := agentSettledFrame(raw) && (!token.receiptSeen || len(token.pendingRootRequests) != 0)
						if result.settled && parentActivityCallback == nil && c.parentActivityHandler != nil {
							parentActivityCallback = c.parentActivityHandler
							parentActivity = map[string]any{"sequence": int64(token.settledSequence), "state": "idle"}
						}
						c.sendDirectMailboxResultLocked(token, result)
						c.signalLocked()
						c.mu.Unlock()
						if !deferParentIdle && parentActivityCallback != nil && !c.enqueueSemantic(func() {
							parentActivityCallback(parentActivity)
						}) {
							return
						}
						continue
					}
				}
			}
			if parentActivityCandidate && parentActivityCallback != nil && c.settlementToken == nil && !admittedSettlementCaptured {
				c.mu.Unlock()
				if !c.enqueueSemantic(func() {
					parentActivityCallback(parentActivity)
				}) {
					return
				}
				continue
			}
			if admittedSettlementCaptured {
				// The admitted record owns this exact boundary. Do not duplicate it
				// in backlog, but still fan the idle projection out once below.
				c.mu.Unlock()
				if parentActivityCallback != nil && !c.enqueueSemantic(func() {
					parentActivityCallback(parentActivity)
				}) {
					return
				}
				continue
			}
			// Ordinary Pi session events have no readiness consumer once the
			// semantic paths above declined them. Keep this allowlist exact so an
			// unfamiliar protocol frame still reaches the bounded alarm FIFO.
			// An active settlement waiter owns its frame. Without a parent
			// subscriber, an unowned settled frame stays as stale-boundary proof
			// for a later waiter. An admitted turn records its sequence above and
			// is deliberately not duplicated in backlog.
			settledOwned := agentSettledFrame(raw) && !admittedSettlementCaptured && (c.settlementToken != nil || (c.parentActivityHandler == nil && c.admittedRestartTurn == nil))
			// Wrapped custom messages are semantic receipt candidates. They need
			// the existing waiter/backlog path before ordinary message_end disposal;
			// this includes malformed-known and future custom types as bounded alarms.
			if !settledOwned && !semanticCustomMessageCandidate(raw) && ordinaryAgentSessionEvent(raw) {
				c.mu.Unlock()
				continue
			}
			// A controlled restart owns its agent_settled receipt. Keep that
			// native event in the bounded backlog until the settlement waiter
			// consumes it. A client without a parent-activity subscriber retains
			// the event too, preserving the restart waiter's stale-event proof.
			if len(c.backlog) == 128 {
				c.readerTerminalizeLocked(ErrReadinessBacklogFull)
				terminal = true
			} else {
				c.backlog = append(c.backlog, receiptNotification{sequence: c.eventSequence, value: raw})
				c.signalLocked()
			}
		}
		c.mu.Unlock()
		if terminal {
			return
		}
		if parentActivityCallback != nil && !c.enqueueSemantic(func() {
			parentActivityCallback(parentActivity)
		}) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		c.readerTerminalize(err)
	} else {
		c.readerTerminalize(io.EOF)
	}
}

// routeMalformedAbortResponse preserves the typed Abort validator's ability
// to distinguish a malformed or rejected Abort response. Other malformed
// response-shaped traffic must remain in the bounded readiness backlog.
func (c *rpcClient) routeMalformedAbortResponse(raw map[string]any) bool {
	id, ok := raw["id"].(string)
	if !ok || id == "" {
		return false
	}
	c.mu.Lock()
	expectedCommand, hasExpectedCommand := c.pendingCommands[id]
	_, hasHook := c.pendingHooks[id]
	ch, hasPending := c.pending[id]
	if !hasPending || !hasExpectedCommand || expectedCommand != "abort" || hasHook {
		c.mu.Unlock()
		return false
	}
	command, commandOK := raw["command"].(string)
	if validRPCResponseEnvelope(raw) && commandOK && command == "abort" {
		c.mu.Unlock()
		return false
	}
	delete(c.pending, id)
	delete(c.pendingCommands, id)
	delete(c.pendingHooks, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- rpcResponse{value: raw}
	}
	return true
}

func (c *rpcClient) signalLocked() {
	select {
	case c.notified <- struct{}{}:
	default:
	}
}
func (c *rpcClient) terminalize(err error) { c.mu.Lock(); c.terminalizeLocked(err); c.mu.Unlock() }

// readerTerminalize is deliberately separate from Close: EOF says the Pi
// telemetry stream ended, but an intentional client close is not evidence
// about the still-owned process.
func (c *rpcClient) readerTerminalize(err error) {
	c.mu.Lock()
	c.readerTerminalizeLocked(err)
	c.mu.Unlock()
}

// readerTerminalizeLocked records a reader-owned terminal boundary, schedules
// the one terminal callback, and fails all waiters before closing the semantic
// queue. The caller must hold mu.
func (c *rpcClient) readerTerminalizeLocked(err error) {
	if c.terminal != nil {
		return
	}
	c.readerTerminal = err
	if c.readerTerminalHandler != nil && !c.readerTerminalHandlerCalled {
		c.scheduleReaderTerminalNotificationLocked(c.readerTerminalHandler, err)
	}
	c.terminalizeLocked(err)
}

func (c *rpcClient) scheduleReaderTerminalNotificationLocked(handler func(error), err error) {
	if handler == nil || c.readerTerminalHandlerCalled {
		return
	}
	c.readerTerminalHandlerCalled = true
	c.terminalHandlerDone = make(chan struct{})
	done := c.terminalHandlerDone
	go func() {
		<-c.semanticDone
		handler(err)
		close(done)
	}()
}

func (c *rpcClient) terminalizeLocked(err error) {
	if c.terminal != nil {
		return
	}
	c.terminal = err
	// Fail every waiter before closing the semantic queue. A semantic callback
	// may be waiting for one of these receipts; it must be able to finish so the
	// dispatcher can drain and Close can join it.
	if token := c.directMailbox; token != nil {
		c.directMailbox = nil
		c.sendDirectMailboxResultLocked(token, directMailboxResult{err: err})
	}
	if token := c.rootResult; token != nil {
		c.rootResult = nil
		token.closed = true
		close(token.result)
	}
	for id, ch := range c.pending {
		delete(c.pending, id)
		delete(c.pendingCommands, id)
		delete(c.pendingHooks, id)
		ch <- rpcResponse{err: err}
	}
	for id := range c.pendingCommands {
		delete(c.pendingCommands, id)
	}
	for id := range c.pendingHooks {
		delete(c.pendingHooks, id)
	}
	if !c.semanticQueueClosed {
		c.semanticQueueClosed = true
		close(c.semanticQueue)
	}
	c.signalLocked()
}
func (c *rpcClient) terminalError() error { c.mu.Lock(); defer c.mu.Unlock(); return c.terminal }

func (c *rpcClient) call(ctx context.Context, typ string, values map[string]any) (map[string]any, error) {
	value, _, err := c.callSerialized(ctx, typ, values, false)
	return value, err
}

// callSerialized registers a response waiter and writes one JSONL request
// while holding the matcher lock. Private commands capture their receipt
// watermark in this same critical section, so a receipt cannot be appended
// between the watermark and the command write.
func (c *rpcClient) callSerialized(ctx context.Context, typ string, values map[string]any, captureWatermark bool) (map[string]any, uint64, error) {
	return c.callSerializedWithHook(ctx, typ, values, captureWatermark, nil)
}

func (c *rpcClient) callSerializedWithHook(ctx context.Context, typ string, values map[string]any, captureWatermark bool, hook rpcResponseHook) (map[string]any, uint64, error) {
	return c.callSerializedWithHookAndRestartAdmission(ctx, typ, values, captureWatermark, hook, nil)
}

func (c *rpcClient) callSerializedWithHookAndRestartAdmission(ctx context.Context, typ string, values map[string]any, captureWatermark bool, hook rpcResponseHook, admissionFence *store.ManagedRuntimeFence) (map[string]any, uint64, error) {
	c.mu.Lock()
	if c.terminal != nil {
		err := c.terminal
		c.mu.Unlock()
		return nil, 0, err
	}
	c.nextID++
	id := fmt.Sprintf("managed-%d", c.nextID)
	payload := map[string]any{"id": id, "type": typ}
	for key, value := range values {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		c.mu.Unlock()
		return nil, 0, err
	}
	response := make(chan rpcResponse, 1)
	c.pending[id] = response
	c.pendingCommands[id] = typ
	if admissionFence != nil {
		if admitted := c.admittedRestartTurn; admitted != nil {
			if admitted.fence != *admissionFence {
				delete(c.pending, id)
				delete(c.pendingCommands, id)
				c.mu.Unlock()
				return nil, 0, errors.New("restart turn admission fence does not match armed turn")
			}
			if admitted.settledSequence <= admitted.watermark {
				delete(c.pending, id)
				delete(c.pendingCommands, id)
				c.mu.Unlock()
				return nil, 0, errors.New("restart turn admission is already armed")
			}
		}
		// A decoded settled frame closes this exact prior admission even if its
		// semantic activity callback remains delayed. Replace it with a fresh
		// identity and current watermark so a later reset must drain the new turn.
		c.admittedRestartTurn = &admittedRestartTurn{fence: *admissionFence, watermark: c.eventSequence}
		c.consumedAdmittedRestartFence = nil
	}
	if hook != nil {
		c.pendingHooks[id] = hook
	}
	watermark := c.eventSequence
	if _, err = fmt.Fprintf(c.stdin, "%s\n", encoded); err != nil {
		delete(c.pending, id)
		delete(c.pendingCommands, id)
		delete(c.pendingHooks, id)
		if admissionFence != nil && c.admittedRestartTurn != nil && c.admittedRestartTurn.fence == *admissionFence {
			c.admittedRestartTurn = nil
		}
		c.terminalizeLocked(err)
		c.mu.Unlock()
		return nil, 0, err
	}
	c.mu.Unlock()
	if !captureWatermark {
		watermark = 0
	}
	select {
	case result := <-response:
		return result.value, watermark, result.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		delete(c.pendingCommands, id)
		delete(c.pendingHooks, id)
		c.mu.Unlock()
		return nil, watermark, ctx.Err()
	}
}
func unwrap(value map[string]any) map[string]any {
	if data, ok := value["data"].(map[string]any); ok {
		return data
	}
	return value
}
func (c *rpcClient) GetState(ctx context.Context) (map[string]any, error) {
	value, err := c.call(ctx, "get_state", nil)
	return unwrap(value), err
}

func (c *rpcClient) SetModel(ctx context.Context, provider, modelID string) (Model, error) {
	value, err := c.call(ctx, "set_model", map[string]any{"provider": provider, "modelId": modelID})
	if err != nil {
		return Model{}, err
	}
	data := unwrap(value)
	actualProvider, providerOK := data["provider"].(string)
	actualID, idOK := data["id"].(string)
	if !providerOK || !idOK || actualProvider != provider || actualID != modelID {
		return Model{}, errors.New("set_model response does not match requested model")
	}
	name, _ := data["name"].(string)
	return Model{Provider: actualProvider, ID: actualID, Name: name, Available: true}, nil
}

// Abort intentionally uses the public wire command rather than a prompt
// wrapper. The response is part of the ownership proof: only Pi's typed,
// successful Abort acknowledgement counts before the caller reaps its group.
func (c *rpcClient) Abort(ctx context.Context) error {
	c.mu.Lock()
	terminal := c.terminal
	c.mu.Unlock()
	if terminal != nil {
		// A readiness backlog overflow stops the stdout reader, but the exact
		// owned Pi process may still be alive and accepting commands. Send the
		// typed Abort before the lifecycle reaper takes over. Its response cannot
		// be observed on this terminated reader, so the caller must still prove
		// the exact process fence and reap the group before replacement.
		if !errors.Is(terminal, ErrReadinessBacklogFull) && !errors.Is(terminal, ErrSemanticQueueFull) {
			return terminal
		}
		return c.sendTerminalAbort(ctx)
	}
	value, err := c.call(ctx, "abort", nil)
	if err != nil {
		return err
	}
	return validateAbortResponse(value)
}

func (c *rpcClient) sendTerminalAbort(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	payload := map[string]any{"id": fmt.Sprintf("managed-%d", c.nextID), "type": "abort"}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.stdin, "%s\n", encoded); err != nil {
		return err
	}
	return ErrAbortResponseUnavailable
}

// Compact calls Pi's native RPC command instead of synthesizing a context
// transition. Pi emits compaction_start and compaction_end on the same stdout
// stream, so the manager can persist the genuine lifecycle cards.
func (c *rpcClient) Compact(ctx context.Context, customInstructions string) error {
	values := map[string]any{}
	if customInstructions != "" {
		values["customInstructions"] = customInstructions
	}
	value, err := c.call(ctx, "compact", values)
	if err != nil {
		return err
	}
	return validateSuccessfulCommandResponse(value, "compact")
}

func (c *rpcClient) PostCompactionNotification(ctx context.Context, envelope map[string]any) error {
	value, _, err := c.privateCommand(ctx, "mecha-post-compaction-notification-v1", envelope)
	if err != nil {
		return err
	}
	return validateSuccessfulCommandResponse(value, "prompt")
}

func validateAbortResponse(value map[string]any) error {
	return validateSuccessfulCommandResponse(value, "abort")
}

func validateSuccessfulCommandResponse(value map[string]any, commandName string) error {
	if value == nil {
		return ErrAbortResponseMalformed
	}
	typ, typeOK := value["type"].(string)
	command, commandOK := value["command"].(string)
	success, successOK := value["success"].(bool)
	if !typeOK || typ != "response" || !commandOK || command != commandName || !successOK {
		return ErrAbortResponseMalformed
	}
	if !success {
		return ErrAbortResponseRejected
	}
	if responseError, present := value["error"]; present && responseError != nil {
		return ErrAbortResponseRejected
	}
	return nil
}
func (c *rpcClient) GetCommands(ctx context.Context) ([]string, error) {
	value, err := c.call(ctx, "get_commands", nil)
	if err != nil {
		return nil, err
	}
	value = unwrap(value)
	raw := value["commands"]
	if raw == nil {
		return nil, errors.New("Pi get_commands response has no commands")
	}
	var result []string
	switch items := raw.(type) {
	case []any:
		for _, item := range items {
			switch entry := item.(type) {
			case string:
				result = append(result, entry)
			case map[string]any:
				if name, ok := entry["name"].(string); ok {
					result = append(result, name)
				}
			}
		}
	default:
		return nil, errors.New("Pi commands response is invalid")
	}
	if err := validateManagedCommandNames(result); err != nil {
		return nil, err
	}
	return result, nil
}
func encodeRPCEnvelope(value map[string]any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
func (c *rpcClient) privateCommand(ctx context.Context, name string, envelope map[string]any) (map[string]any, uint64, error) {
	encoded, err := encodeRPCEnvelope(envelope)
	if err != nil {
		return nil, 0, err
	}
	value, watermark, err := c.callSerialized(ctx, "prompt", map[string]any{"message": "/" + name + " " + encoded}, true)
	return unwrap(value), watermark, err
}
func (c *rpcClient) setReceiptWatermark(slot **uint64, watermark uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*slot = &watermark
}
func (c *rpcClient) Bootstrap(ctx context.Context, envelope map[string]any) error {
	_, watermark, err := c.privateCommand(ctx, "mecha-bootstrap-v1", envelope)
	if err != nil {
		return err
	}
	c.setReceiptWatermark(&c.bootstrapWatermark, watermark)
	return nil
}
func (c *rpcClient) ApplyPolicy(ctx context.Context, envelope map[string]any) (map[string]any, error) {
	value, watermark, err := c.privateCommand(ctx, "mecha-policy-apply-v1", envelope)
	if err != nil {
		return nil, err
	}
	c.setReceiptWatermark(&c.policyWatermark, watermark)
	return value, nil
}

func (c *rpcClient) StartManagedGoalEvaluation(ctx context.Context, fence store.ManagedRuntimeFence, policy store.ManagedGoalPolicy) error {
	if !validRestartFence(fence) || policy.EvaluatorID == "" || policy.EvaluatorGoalRevision < 1 || policy.EvaluatorSettledSequence < 1 || policy.Config.Goal == "" {
		return errors.New("managed goal evaluation is invalid")
	}
	token := &managedGoalEvaluationToken{evaluationID: policy.EvaluatorID, goalRevision: policy.EvaluatorGoalRevision, settled: policy.EvaluatorSettledSequence, fence: fence}
	c.mu.Lock()
	if c.terminal != nil {
		err := c.terminal
		c.mu.Unlock()
		return err
	}
	if c.goalEvaluationToken != nil || c.goalEvaluationCleanup != nil {
		c.mu.Unlock()
		return errors.New("managed goal evaluation is already armed")
	}
	// Keep cleanup ownership before the hidden command is written. A response
	// timeout (including one from Start itself) is ambiguous: Pi may already
	// have activated the evaluator, so suppression must remain owned until the
	// exact token is reconciled or the existing process-recovery path takes over.
	c.goalEvaluationToken = token
	c.goalEvaluationCleanup = &managedGoalEvaluationCleanup{token: token}
	// Pi emits the evaluator's native settled frame before the extension emits
	// the policy result receipt. Arm suppression before asking Pi to trigger the
	// hidden turn so that frame cannot start a second durable evaluation.
	c.suppressNextEvaluatorSettled = true
	c.evaluatorSettledObserved = false
	c.mu.Unlock()
	envelope := map[string]any{
		"protocol": "mecha.policy/v1", "policy": map[string]any{}, "runtime_generation": fence.RuntimeGeneration,
		"session_epoch": fence.SessionEpoch, "manifest_sha256": fence.ManifestHash,
		"evaluator": map[string]any{"evaluation_id": policy.EvaluatorID, "goal_revision": policy.EvaluatorGoalRevision,
			"settled_sequence": policy.EvaluatorSettledSequence, "goal": policy.Config.Goal},
	}
	_, watermark, err := c.privateCommand(ctx, "mecha-policy-apply-v1", envelope)
	if err != nil {
		c.mu.Lock()
		if c.goalEvaluationToken == token {
			// Start owns cleanup, but its response waiter is gone. Transfer the
			// token to the cleanup owner so Reconcile can issue the typed Abort.
			c.goalEvaluationToken = nil
		}
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.goalEvaluationToken != token || c.goalEvaluationCleanup == nil {
		return ErrManagedGoalEvaluationCleanupStale
	}
	// The command watermark belongs to the attempt even when Start's response
	// arrived after native activity frames were already decoded.
	token.watermark = watermark
	if c.terminal != nil {
		c.goalEvaluationToken = nil
		return c.terminal
	}
	return nil
}

func (c *rpcClient) WaitForManagedGoalEvaluation(ctx context.Context) (outcome, reason string, err error) {
	c.mu.Lock()
	token := c.goalEvaluationToken
	c.mu.Unlock()
	if token == nil {
		return "", "", errors.New("managed goal evaluation is not armed")
	}
	defer func() {
		c.mu.Lock()
		if c.goalEvaluationToken == token {
			c.goalEvaluationToken = nil
			if err == nil && c.goalEvaluationCleanup != nil && c.goalEvaluationCleanup.token == token {
				c.goalEvaluationCleanup = nil
			}
			if err != nil && c.goalEvaluationCleanup == nil {
				c.goalEvaluationCleanup = &managedGoalEvaluationCleanup{token: token}
			}
			// Start installs the cleanup owner before writing the command. Keep it
			// on every waiter/terminal error; only successful receipt validation
			// proves that evaluator ownership can be released.
		}
		c.mu.Unlock()
	}()
	for {
		c.mu.Lock()
		if c.terminal != nil {
			err := c.terminal
			c.mu.Unlock()
			return "", "", err
		}
		consumed := false
		for index, item := range c.backlog {
			if receiptCustomType(item.value) != "mecha.policy.applied.v1" {
				continue
			}
			details := receiptDetails(item.value)
			evaluator, isEvaluator := map[string]any(nil), false
			if details != nil {
				evaluator, isEvaluator = details["evaluator"].(map[string]any)
			}
			if !isEvaluator {
				continue
			}
			// Evaluator receipts are isolated from ordinary policy application.
			// A stale or mismatched result is consumed and cannot poison the next
			// evaluation on this runtime.
			c.backlog = append(c.backlog[:index], c.backlog[index+1:]...)
			consumed = true
			if item.sequence <= token.watermark || evaluator["evaluation_id"] != token.evaluationID || !sameEpoch(evaluator["goal_revision"], token.goalRevision) || !sameEpoch(evaluator["settled_sequence"], token.settled) {
				break
			}
			phase, _ := evaluator["phase"].(string)
			if phase == "requested" || phase != "result" || details["runtime_generation"] != token.fence.RuntimeGeneration || !sameEpoch(details["session_epoch"], token.fence.SessionEpoch) || details["manifest_sha256"] != token.fence.ManifestHash {
				break
			}
			outcome, outcomeOK := evaluator["outcome"].(string)
			reason, reasonOK := evaluator["reason"].(string)
			if !c.evaluatorSettledObserved {
				// Some Pi versions report the policy receipt before the native
				// settled frame. Keep suppression armed for that ordering only.
				c.suppressNextEvaluatorSettled = true
			}
			c.mu.Unlock()
			if !outcomeOK || !reasonOK || (outcome != "DONE" && outcome != store.ManagedGoalEvaluationNotDone) || len(reason) == 0 || len(reason) > 512 {
				return "", "", errors.New("managed goal evaluation result is invalid")
			}
			if outcome == "DONE" {
				outcome = store.ManagedGoalEvaluationCompleted
			}
			return outcome, reason, nil
		}
		c.mu.Unlock()
		if consumed {
			continue
		}
		select {
		case <-c.notified:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
}
func managedGoalEvaluationTokenMatches(token *managedGoalEvaluationToken, fence store.ManagedRuntimeFence, evaluationID string, goalRevision, settled int64) bool {
	return token != nil && token.fence == fence && token.evaluationID == evaluationID && token.goalRevision == goalRevision && token.settled == settled
}

func (c *rpcClient) ManagedGoalEvaluationCleanupPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.goalEvaluationCleanup != nil
}

func (c *rpcClient) finishManagedGoalEvaluationCleanup(ctx context.Context, owner *managedGoalEvaluationCleanup, fence store.ManagedRuntimeFence, evaluationID string, goalRevision, settled int64) error {
	c.mu.Lock()
	if c.goalEvaluationCleanup != owner || c.goalEvaluationToken != nil || owner == nil || !managedGoalEvaluationTokenMatches(owner.token, fence, evaluationID, goalRevision, settled) || !owner.abortBoundary || owner.barrier == nil {
		c.mu.Unlock()
		return ErrManagedGoalEvaluationCleanupStale
	}
	barrier := owner.barrier
	c.mu.Unlock()

	select {
	case <-barrier:
	case <-ctx.Done():
		return ctx.Err()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.goalEvaluationCleanup != owner || c.goalEvaluationToken != nil || !managedGoalEvaluationTokenMatches(owner.token, fence, evaluationID, goalRevision, settled) {
		return ErrManagedGoalEvaluationCleanupStale
	}
	if owner.token.admitted && !owner.token.settledObserved {
		return ErrManagedGoalEvaluationSettledNotObserved
	}
	if !owner.token.settledObserved {
		c.suppressNextEvaluatorSettled = false
		c.evaluatorSettledObserved = false
	}
	c.goalEvaluationCleanup = nil
	return nil
}

// ReconcileManagedGoalEvaluation sends the typed public Abort command with an
// evaluator-specific response hook. A successful response admits a signal-only
// task into the existing semantic FIFO before the response waiter is released.
// That boundary keeps later ordinary activity visible while preserving an
// already observed evaluator settled event.
func (c *rpcClient) ReconcileManagedGoalEvaluation(ctx context.Context, fence store.ManagedRuntimeFence, evaluationID string, goalRevision, settled int64) error {
	c.mu.Lock()
	owner := c.goalEvaluationCleanup
	if owner == nil || !managedGoalEvaluationTokenMatches(owner.token, fence, evaluationID, goalRevision, settled) {
		c.mu.Unlock()
		return ErrManagedGoalEvaluationCleanupStale
	}
	barrier := owner.barrier
	abortBoundary := owner.abortBoundary
	c.mu.Unlock()
	if abortBoundary {
		return c.finishManagedGoalEvaluationCleanup(ctx, owner, fence, evaluationID, goalRevision, settled)
	}
	if barrier != nil {
		return ErrManagedGoalEvaluationCleanupStale
	}

	responseHook := func(value map[string]any, responseErr error) error {
		if responseErr != nil {
			return responseErr
		}
		if err := validateAbortResponse(value); err != nil {
			return err
		}
		// The response hook runs under c.mu, so recording the boundary and
		// admitting the barrier are one matcher-locked operation.
		if c.goalEvaluationCleanup != owner || c.goalEvaluationToken != nil || !managedGoalEvaluationTokenMatches(owner.token, fence, evaluationID, goalRevision, settled) {
			return ErrManagedGoalEvaluationCleanupStale
		}
		completion := make(chan struct{})
		if !c.enqueueSemanticTaskLocked(rpcSemanticTask{fn: func() { close(completion) }}) {
			if c.terminal != nil {
				return c.terminal
			}
			return ErrSemanticQueueFull
		}
		owner.abortBoundary = true
		owner.abortBoundarySequence = c.eventSequence
		owner.barrier = completion
		return nil
	}
	_, _, err := c.callSerializedWithHook(ctx, "abort", nil, false, responseHook)
	if err != nil {
		return err
	}
	return c.finishManagedGoalEvaluationCleanup(ctx, owner, fence, evaluationID, goalRevision, settled)
}

func (c *rpcClient) RequestChildSnapshot(ctx context.Context, envelope map[string]any) (map[string]any, error) {
	value, watermark, err := c.privateCommand(ctx, "mecha-child-snapshot-v1", envelope)
	if err != nil {
		return nil, err
	}
	c.setReceiptWatermark(&c.snapshotWatermark, watermark)
	return value, nil
}

func receiptDetails(event map[string]any) map[string]any {
	if details, ok := event["details"].(map[string]any); ok {
		return details
	}
	if message, ok := event["message"].(map[string]any); ok {
		if details, ok := message["details"].(map[string]any); ok {
			return details
		}
	}
	return nil
}
func receiptCustomType(event map[string]any) string {
	// Pi reports a persisted custom message at both message_start and
	// message_end. Only the end frame is the durable receipt; treating start
	// as another notification would turn every fixed extension receipt into a
	// false duplicate at the next command watermark.
	if kind, _ := event["type"].(string); kind == "message_start" {
		return ""
	}
	if message, ok := event["message"].(map[string]any); ok {
		return receiptCustomType(message)
	}
	if custom, ok := event["customType"].(string); ok {
		return custom
	}
	return ""
}
func knownReadinessReceipt(custom string) bool {
	switch custom {
	case "mecha.tools.accepted.v1", "mecha.tools.rejected.v1", "mecha.policy.applied.v1", "mecha.child.snapshot.v1":
		return true
	default:
		return false
	}
}
func (c *rpcClient) waitForReceipt(ctx context.Context, watermark uint64, want string, validate func(map[string]any) error) (map[string]any, error) {
	for {
		c.mu.Lock()
		if c.terminal != nil {
			err := c.terminal
			c.mu.Unlock()
			return nil, err
		}
		for index, item := range c.backlog {
			custom := receiptCustomType(item.value)
			if !knownReadinessReceipt(custom) {
				continue
			}
			if item.sequence <= watermark {
				c.terminalizeLocked(fmt.Errorf("stale readiness receipt %s sequence %d at watermark %d", custom, item.sequence, watermark))
				err := c.terminal
				c.mu.Unlock()
				return nil, err
			}
			if custom != want {
				c.terminalizeLocked(fmt.Errorf("unexpected readiness receipt %s", custom))
				err := c.terminal
				c.mu.Unlock()
				return nil, err
			}
			// Remove before validating outside the matcher lock. Validators update
			// accepted tools and can safely take c.mu without deadlocking the read
			// loop; the per-binding serializer is the sole readiness consumer.
			c.backlog = append(c.backlog[:index], c.backlog[index+1:]...)
			c.mu.Unlock()
			if err := validate(item.value); err != nil {
				c.terminalize(err)
				return nil, err
			}
			return item.value, nil
		}
		c.mu.Unlock()
		select {
		case <-c.notified:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// SettlePreparedRestartTurn treats stock Pi's agent_settled frame as a drain
// boundary. The frame has no session or process fields, so the supplied fence
// is deliberately retained only as the caller's immutable binding proof; this
// client proves pipe ownership by observing the frame on this stdout reader.
func (c *rpcClient) SetBuiltinToolLifecycleEventHandler(handler func(BuiltinToolLifecycleEvent)) {
	c.mu.Lock()
	c.builtinToolLifecycleHandler = handler
	c.mu.Unlock()
}

func (c *rpcClient) SettlePreparedRestartTurn(ctx context.Context, fence store.ManagedRuntimeFence) error {
	if !validRestartFence(fence) {
		return errors.New("restart settlement fence is invalid")
	}
	c.mu.Lock()
	if c.terminal != nil {
		err := c.terminal
		c.mu.Unlock()
		return err
	}
	if c.settlementToken != nil {
		c.mu.Unlock()
		return errors.New("restart settlement is already armed")
	}
	if c.consumedAdmittedRestartFence != nil && *c.consumedAdmittedRestartFence == fence {
		c.mu.Unlock()
		return errors.New("admitted restart settlement was already consumed")
	}
	if admitted := c.admittedRestartTurn; admitted != nil {
		if admitted.fence != fence {
			c.mu.Unlock()
			return errors.New("restart settlement fence does not match admitted turn")
		}
		if admitted.settledSequence > admitted.watermark {
			// Consume the exact decoded settlement once. The event was captured
			// before semantic activity projection, so a durable active row cannot
			// make this boundary disappear by advancing the current watermark.
			c.admittedRestartTurn = nil
			consumedFence := fence
			c.consumedAdmittedRestartFence = &consumedFence
			c.mu.Unlock()
			return nil
		}
	}
	token := &restartSettlementToken{fence: fence, watermark: c.eventSequence}
	c.settlementToken = token
	// The direct matcher can receive Pi's settled frame while its owner is
	// blocked in the append-before-ack boundary. Transfer that already observed
	// frame to the lifecycle waiter instead of requiring the waiter to win the
	// race with stdout.
	if direct := c.directMailbox; direct != nil && direct.settledSequence > direct.watermark && direct.generation == fence.RuntimeGeneration && direct.epoch == fence.SessionEpoch {
		token.consumed = true
		c.settlementToken = nil
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	for {
		c.mu.Lock()
		if c.terminal != nil {
			err := c.terminal
			if c.settlementToken == token {
				c.settlementToken = nil
			}
			c.mu.Unlock()
			return err
		}
		admissionWatermark := uint64(0)
		if admitted := c.admittedRestartTurn; admitted != nil {
			if admitted.fence != fence {
				if c.settlementToken == token {
					c.settlementToken = nil
				}
				c.mu.Unlock()
				return errors.New("restart settlement fence does not match admitted turn")
			}
			if admitted.settledSequence > admitted.watermark {
				c.admittedRestartTurn = nil
				consumedFence := fence
				c.consumedAdmittedRestartFence = &consumedFence
				token.consumed = true
				if c.settlementToken == token {
					c.settlementToken = nil
				}
				c.mu.Unlock()
				return nil
			}
			admissionWatermark = admitted.watermark
		}
		settled := -1
		for index, item := range c.backlog {
			// A current admitted turn owns only frames decoded after its prompt
			// watermark. Retain an earlier turn's frame for normal processing, but
			// do not let it fail this new turn as a stale restart settlement.
			if admissionWatermark > 0 && item.sequence <= admissionWatermark {
				continue
			}
			if !agentSettledType(item.value) {
				continue
			}
			if !agentSettledFrame(item.value) {
				if c.settlementToken == token {
					c.settlementToken = nil
				}
				c.mu.Unlock()
				return errors.New("restart settlement received a malformed agent_settled event")
			}
			if item.sequence <= token.watermark {
				if c.settlementToken == token {
					c.settlementToken = nil
				}
				c.mu.Unlock()
				return errors.New("restart settlement received a stale agent_settled event")
			}
			if settled >= 0 {
				if c.settlementToken == token {
					c.settlementToken = nil
				}
				c.mu.Unlock()
				return errors.New("restart settlement received duplicate agent_settled events")
			}
			settled = index
		}
		if settled >= 0 {
			c.backlog = append(c.backlog[:settled], c.backlog[settled+1:]...)
			token.consumed = true
			if c.settlementToken == token {
				c.settlementToken = nil
			}
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		select {
		case <-c.notified:
		case <-ctx.Done():
			c.mu.Lock()
			if c.settlementToken == token {
				c.settlementToken = nil
			}
			c.mu.Unlock()
			return ctx.Err()
		}
	}
}

func validRestartFence(fence store.ManagedRuntimeFence) bool {
	return fence.AgentID != "" && fence.PiSessionID != "" && fence.SessionEpoch >= 1 && fence.ProcessPID > 0 && fence.ProcessStartMarker != "" && fence.ProcessIncarnation != "" && fence.RuntimeGeneration != "" && fence.ManifestRevision >= 1 && len(fence.ManifestHash) == 64
}

func semanticCustomMessageCandidate(value map[string]any) bool {
	return receiptCustomType(value) != ""
}

// ordinaryAgentSessionEvent recognizes only the JSON frames serialized by Pi's
// RPC mode. It validates each known discriminator's closed source-declared
// shape so a future field is retained as a bounded compatibility alarm.
func ordinaryAgentSessionEvent(value map[string]any) bool {
	typeName, ok := value["type"].(string)
	if !ok {
		return false
	}
	switch typeName {
	case "agent_start", "agent_settled", "turn_start", "summarization_retry_finished":
		return exactJSONKeys(value, "type")
	case "agent_end":
		return exactJSONKeys(value, "type", "messages") && jsonArray(value["messages"]) ||
			exactJSONKeys(value, "type", "messages", "willRetry") && jsonArray(value["messages"]) && jsonBool(value["willRetry"])
	case "turn_end":
		return exactJSONKeys(value, "type", "message", "toolResults") && jsonObject(value["message"]) && jsonArray(value["toolResults"])
	case "message_start", "message_end":
		return exactJSONKeys(value, "type", "message") && jsonObject(value["message"])
	case "message_update":
		return exactJSONKeys(value, "type", "message", "assistantMessageEvent") && jsonObject(value["message"]) && validAssistantMessageEvent(value["assistantMessageEvent"]) ||
			exactJSONKeys(value, "type", "usage", "assistantMessageEvent") && validUsage(value["usage"]) && validAssistantMessageEvent(value["assistantMessageEvent"])
	case "tool_execution_start":
		return exactJSONKeys(value, "type", "toolCallId", "toolName", "args") && jsonString(value["toolCallId"]) && jsonString(value["toolName"])
	case "tool_execution_update":
		return exactJSONKeys(value, "type", "toolCallId", "toolName", "args", "partialResult") && jsonString(value["toolCallId"]) && jsonString(value["toolName"])
	case "tool_execution_end":
		return exactJSONKeys(value, "type", "toolCallId", "toolName", "result", "isError") && jsonString(value["toolCallId"]) && jsonString(value["toolName"]) && jsonBool(value["isError"])
	case "queue_update":
		return exactJSONKeys(value, "type", "steering", "followUp") && jsonStringArray(value["steering"]) && jsonStringArray(value["followUp"])
	case "compaction_start":
		return exactJSONKeys(value, "type", "reason") && compactionReason(value["reason"])
	case "compaction_end":
		return validCompactionEnd(value)
	case "entry_appended":
		return exactJSONKeys(value, "type", "entry") && jsonObject(value["entry"])
	case "session_info_changed":
		return allowedJSONKeys(value, []string{"type"}, []string{"name"}) && optionalJSONString(value, "name")
	case "thinking_level_changed":
		return exactJSONKeys(value, "type", "level") && oneOfString(value["level"], "off", "minimal", "low", "medium", "high", "xhigh", "max")
	case "auto_retry_start", "summarization_retry_scheduled":
		return exactJSONKeys(value, "type", "attempt", "maxAttempts", "delayMs", "errorMessage") && jsonNumber(value["attempt"]) && jsonNumber(value["maxAttempts"]) && jsonNumber(value["delayMs"]) && jsonString(value["errorMessage"])
	case "auto_retry_end":
		return validAutoRetryEnd(value)
	case "summarization_retry_attempt_start":
		return validSummarizationRetryStart(value)
	case "bash_execution_update":
		return validBashExecutionUpdate(value)
	default:
		return false
	}
}

func exactJSONKeys(value map[string]any, keys ...string) bool {
	return allowedJSONKeys(value, keys, nil)
}

// allowedJSONKeys permits only source-declared optional fields. A new field
// remains an unknown protocol shape and reaches the bounded alarm backlog.
func allowedJSONKeys(value map[string]any, required, optional []string) bool {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = struct{}{}
		if _, ok := value[key]; !ok {
			return false
		}
	}
	for _, key := range optional {
		allowed[key] = struct{}{}
	}
	for key := range value {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

// validRPCResponseEnvelope is deliberately checked before removing a waiter:
// malformed response-shaped traffic must remain observable in the bounded FIFO.
func validRPCResponseEnvelope(value map[string]any) bool {
	id, idOK := value["id"].(string)
	command, commandOK := value["command"].(string)
	success, successOK := value["success"].(bool)
	if !idOK || id == "" || !commandOK || command == "" || !successOK || value["type"] != "response" {
		return false
	}
	if success {
		return allowedJSONKeys(value, []string{"id", "type", "command", "success"}, []string{"data"})
	}
	errorMessage, errorOK := value["error"].(string)
	return errorOK && errorMessage != "" && exactJSONKeys(value, "id", "type", "command", "success", "error")
}

func jsonObject(value any) bool { _, ok := value.(map[string]any); return ok }
func jsonArray(value any) bool  { _, ok := value.([]any); return ok }
func jsonString(value any) bool { _, ok := value.(string); return ok }
func jsonBool(value any) bool   { _, ok := value.(bool); return ok }
func jsonNumber(value any) bool {
	switch value.(type) {
	case float64, json.Number:
		return true
	default:
		return false
	}
}
func jsonStringArray(value any) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range values {
		if !jsonString(item) {
			return false
		}
	}
	return true
}
func oneOfString(value any, options ...string) bool {
	candidate, ok := value.(string)
	if !ok {
		return false
	}
	for _, option := range options {
		if candidate == option {
			return true
		}
	}
	return false
}
func compactionReason(value any) bool { return oneOfString(value, "manual", "threshold", "overflow") }

func validUsage(value any) bool {
	usage, ok := value.(map[string]any)
	if !ok || !allowedJSONKeys(usage, []string{"input", "output", "cacheRead", "cacheWrite", "totalTokens", "cost"}, []string{"cacheWrite1h", "reasoning"}) {
		return false
	}
	for _, key := range []string{"input", "output", "cacheRead", "cacheWrite", "totalTokens"} {
		if !jsonNumber(usage[key]) {
			return false
		}
	}
	for _, key := range []string{"cacheWrite1h", "reasoning"} {
		if optional, present := usage[key]; present && !jsonNumber(optional) {
			return false
		}
	}
	cost, ok := usage["cost"].(map[string]any)
	if !ok || !exactJSONKeys(cost, "input", "output", "cacheRead", "cacheWrite", "total") {
		return false
	}
	for _, key := range []string{"input", "output", "cacheRead", "cacheWrite", "total"} {
		if !jsonNumber(cost[key]) {
			return false
		}
	}
	return true
}

func validAssistantMessageEvent(value any) bool {
	event, ok := value.(map[string]any)
	if !ok {
		return false
	}
	switch event["type"] {
	case "start":
		return exactJSONKeys(event, "type")
	case "text_start", "thinking_start":
		return exactJSONKeys(event, "type", "contentIndex") && jsonNumber(event["contentIndex"])
	case "text_delta", "thinking_delta", "toolcall_delta":
		return exactJSONKeys(event, "type", "contentIndex", "delta") && jsonNumber(event["contentIndex"]) && jsonString(event["delta"])
	case "text_end", "thinking_end":
		return exactJSONKeys(event, "type", "contentIndex", "content") && jsonNumber(event["contentIndex"]) && jsonString(event["content"])
	case "toolcall_start":
		return exactJSONKeys(event, "type", "contentIndex", "id", "toolName") && jsonNumber(event["contentIndex"]) && jsonString(event["id"]) && jsonString(event["toolName"])
	case "toolcall_end":
		toolCall, ok := event["toolCall"].(map[string]any)
		return exactJSONKeys(event, "type", "contentIndex", "toolCall") && jsonNumber(event["contentIndex"]) && ok && allowedJSONKeys(toolCall, []string{"type", "id", "name", "arguments"}, []string{"thoughtSignature", "namespace"}) && oneOfString(toolCall["type"], "toolCall") && jsonString(toolCall["id"]) && jsonString(toolCall["name"]) && jsonObject(toolCall["arguments"]) && optionalJSONString(toolCall, "thoughtSignature") && optionalJSONString(toolCall, "namespace")
	case "done":
		return exactJSONKeys(event, "type", "reason", "message") && oneOfString(event["reason"], "stop", "length", "toolUse", "deferred") && jsonObject(event["message"])
	case "error":
		return exactJSONKeys(event, "type", "reason", "error") && oneOfString(event["reason"], "aborted", "error") && jsonObject(event["error"])
	default:
		return false
	}
}

func optionalJSONString(value map[string]any, key string) bool {
	optional, present := value[key]
	return !present || jsonString(optional)
}

func validCompactionEnd(value map[string]any) bool {
	// Ordinary disposal shares the semantic lifecycle contract: malformed and
	// future compaction frames remain bounded readiness-backlog alarms.
	return validParentCompactionWireEvent(value)
}

func validAutoRetryEnd(value map[string]any) bool {
	if !allowedJSONKeys(value, []string{"type", "success", "attempt"}, []string{"finalError"}) || !jsonBool(value["success"]) || !jsonNumber(value["attempt"]) {
		return false
	}
	if finalError, present := value["finalError"]; present {
		return jsonString(finalError)
	}
	return true
}
func validSummarizationRetryStart(value map[string]any) bool {
	if source, ok := value["source"].(string); !ok || (source != "branchSummary" && source != "compaction") {
		return false
	}
	if value["source"] == "branchSummary" {
		return exactJSONKeys(value, "type", "source")
	}
	return exactJSONKeys(value, "type", "source", "reason") && compactionReason(value["reason"])
}
func validBashExecutionUpdate(value map[string]any) bool {
	if !jsonString(value["delta"]) || (len(value) != 2 && len(value) != 3) {
		return false
	}
	for key := range value {
		if key != "type" && key != "delta" && key != "id" {
			return false
		}
	}
	if id, ok := value["id"]; ok {
		return jsonString(id)
	}
	return true
}

func agentSettledType(value map[string]any) bool {
	typeName, ok := value["type"].(string)
	return ok && typeName == "agent_settled"
}

func agentSettledFrame(value map[string]any) bool {
	return len(value) == 1 && agentSettledType(value)
}

func takeWatermark(c *rpcClient, which *uint64) (uint64, error) {
	if which == nil {
		return 0, errors.New("readiness receipt wait has no command watermark")
	}
	return *which, nil
}
func (c *rpcClient) WaitForReceipt(ctx context.Context, generation, manifestHash string, manifestRevision int, epoch int64, _ string) error {
	c.mu.Lock()
	watermark, err := takeWatermark(c, c.bootstrapWatermark)
	c.bootstrapWatermark = nil
	c.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = c.waitForReceipt(ctx, watermark, "mecha.tools.accepted.v1", func(event map[string]any) error {
		details := receiptDetails(event)
		if details == nil {
			return errors.New("managed tools receipt is malformed")
		}
		if details["runtime_generation"] != generation || !sameEpoch(details["session_epoch"], epoch) || !sameEpoch(details["manifest_revision"], int64(manifestRevision)) || details["manifest_sha256"] != manifestHash {
			return errors.New("managed tools receipt does not match runtime")
		}
		active, ok := stringSlice(details["active_tools"])
		if !ok {
			return errors.New("managed tools receipt has invalid active tools")
		}
		c.mu.Lock()
		c.activeTools = append([]string(nil), active...)
		c.mu.Unlock()
		return nil
	})
	return err
}
func (c *rpcClient) WaitForPolicyApplied(ctx context.Context, generation string, epoch int64) (map[string]any, error) {
	c.mu.Lock()
	watermark, err := takeWatermark(c, c.policyWatermark)
	c.policyWatermark = nil
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	event, err := c.waitForReceipt(ctx, watermark, "mecha.policy.applied.v1", func(event map[string]any) error {
		details := receiptDetails(event)
		if details == nil || details["protocol"] != "mecha.policy/v1" || details["runtime_generation"] != generation || !sameEpoch(details["session_epoch"], epoch) {
			return errors.New("policy receipt does not match runtime")
		}
		_, err := normalizeAppliedPolicyReceipt(details["applied"])
		return err
	})
	if err != nil {
		return nil, err
	}
	return receiptDetails(event), nil
}
func (c *rpcClient) WaitForChildSnapshot(ctx context.Context) (map[string]any, error) {
	c.mu.Lock()
	watermark, err := takeWatermark(c, c.snapshotWatermark)
	c.snapshotWatermark = nil
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	event, err := c.waitForReceipt(ctx, watermark, "mecha.child.snapshot.v1", func(event map[string]any) error {
		_, err := normalizeChildSnapshotReceipt(receiptDetails(event))
		return err
	})
	if err != nil {
		return nil, err
	}
	return receiptDetails(event), nil
}
func stringSlice(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		name, ok := item.(string)
		if !ok || name == "" {
			return nil, false
		}
		result = append(result, name)
	}
	return result, true
}
func sameEpoch(value any, want int64) bool {
	switch number := value.(type) {
	case float64:
		return int64(number) == want
	case int:
		return int64(number) == want
	case int64:
		return number == want
	case json.Number:
		parsed, err := number.Int64()
		return err == nil && parsed == want
	default:
		return false
	}
}
func (c *rpcClient) GetActiveTools(_ context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeTools == nil {
		return nil, errors.New("Pi did not report its active tool set")
	}
	return append([]string(nil), c.activeTools...), nil
}
func (c *rpcClient) Prompt(ctx context.Context, message string) error {
	_, err := c.call(ctx, "prompt", map[string]any{"message": message})
	return err
}

func (c *rpcClient) PromptAdmittedRestartTurn(ctx context.Context, message string, fence store.ManagedRuntimeFence) error {
	if !validRestartFence(fence) {
		return errors.New("restart turn admission fence is invalid")
	}
	_, _, err := c.callSerializedWithHookAndRestartAdmission(ctx, "prompt", map[string]any{"message": message}, false, nil, &fence)
	return err
}

func directMailboxReconciliationKey(attempt store.ManagedDirectAttempt) string {
	return fmt.Sprintf("stock-transcript-receipt-v1:%s:%s:%d", attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber)
}

// PromptManagedMailbox installs its receipt matcher while holding the same
// mutex that serializes the JSONL write. A prompt response is deliberately not
// a receipt; it only reports a submission failure to the retained matcher.
func (c *rpcClient) PromptManagedMailbox(ctx context.Context, attempt store.ManagedDirectAttempt) (<-chan DirectMailboxEvent, error) {
	if attempt.MessageID == "" || attempt.Fence.ManifestHash == "" || attempt.SessionEntryWatermark == "" {
		return nil, errors.New("managed direct attempt is incomplete")
	}
	envelope := map[string]any{
		"protocol": "mecha.mailbox-deliver/v1", "message_id": attempt.MessageID,
		"content": attempt.Message.Body, "runtime_generation": attempt.Fence.RuntimeGeneration,
		"session_epoch": attempt.Fence.SessionEpoch, "manifest_sha256": attempt.Fence.ManifestHash,
	}
	encoded, err := encodeRPCEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.terminal != nil {
		err := c.terminal
		c.mu.Unlock()
		return nil, err
	}
	if c.directMailbox != nil {
		c.mu.Unlock()
		return nil, errors.New("managed direct mailbox matcher is already armed")
	}
	c.nextID++
	id := fmt.Sprintf("managed-%d", c.nextID)
	payload, err := json.Marshal(map[string]any{"id": id, "type": "prompt", "message": "/mecha-mailbox-deliver-v1 " + encoded})
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	response := make(chan rpcResponse, 1)
	c.pending[id] = response
	c.pendingCommands[id] = "prompt"
	token := &directMailboxToken{messageID: attempt.MessageID, generation: attempt.Fence.RuntimeGeneration, manifestHash: attempt.Fence.ManifestHash, epoch: attempt.Fence.SessionEpoch, watermark: c.eventSequence, pendingRootRequests: map[string]struct{}{}, result: make(chan directMailboxResult, 128)}
	c.directMailbox = token
	c.noteStaleDirectMailboxEventsLocked(token)
	if _, err = fmt.Fprintf(c.stdin, "%s\n", payload); err != nil {
		delete(c.pending, id)
		delete(c.pendingCommands, id)
		c.directMailbox = nil
		c.terminalizeLocked(err)
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()
	out := make(chan DirectMailboxEvent, 128)
	go func() {
		defer close(out)
		defer c.releaseDirectMailboxToken(id, token)
		responseCh := response
		receiptDelivered := false
		for {
			select {
			case value := <-token.result:
				if value.err != nil {
					return
				}
				event := DirectMailboxEvent{Receipt: value.receipt, Reply: value.reply, Settled: value.settled, DiagnosticCode: value.diagnosticCode}
				if event.Receipt != nil {
					receiptDelivered = true
				}
				if event.Receipt != nil || event.Reply != nil || event.Settled || event.DiagnosticCode != "" {
					out <- event
				}
			case responseValue := <-responseCh:
				if responseValue.err != nil {
					return
				}
				// A successful prompt response is intentionally ignored. Wait for the
				// persisted append marker or a terminal stream condition.
				responseCh = nil
			case <-ctx.Done():
				// Cancellation after Pi has durably appended the mailbox entry is a
				// terminal direct-turn boundary. Release it exactly once so the
				// manager can clear durable activity and admit the next message. A
				// missing receipt or pending root-control work remains provisional;
				// the manager will retain/recover or interrupt it on stream close.
				var canceled DirectMailboxEvent
				var idleHandler func(map[string]any)
				var idleEvent map[string]any
				c.mu.Lock()
				canCancelSettle := token.settledSequence == 0 && !token.settledReleased
				canDrainCommittedSettle := token.settledSequence != 0 && token.settledReleased && !receiptDelivered
				if c.directMailbox == token && token.receiptSeen && len(token.pendingRootRequests) == 0 && token.acceptedRootFollowUps == 0 && (canCancelSettle || canDrainCommittedSettle) {
					// A native settled frame may have been observed before the
					// receipt. Receipt classification then queues the combined
					// receipt/settlement result and marks it released. Cancellation
					// must forward that committed result rather than treating the
					// nonzero boundary as a reason to drop it.
					if token.settledSequence == 0 {
						token.settledSequence = c.eventSequence
						token.settledReleased = true
						idleHandler = c.parentActivityHandler
						idleEvent = map[string]any{"sequence": int64(token.settledSequence), "state": "idle"}
					}
					if !receiptDelivered {
						canceled.Receipt = token.receipt
						receiptDelivered = canceled.Receipt != nil
					}
					canceled.Reply = token.reply
					canceled.Settled = true
				}
				c.mu.Unlock()
				if canceled.Settled {
					out <- canceled
					if idleHandler != nil {
						_ = c.enqueueSemantic(func() { idleHandler(idleEvent) })
					}
				}
				return
			}
		}
	}()
	return out, nil
}

func (c *rpcClient) releaseDirectMailboxToken(id string, token *directMailboxToken) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.directMailbox == token {
		c.directMailbox = nil
	}
	delete(c.pending, id)
	delete(c.pendingCommands, id)
}

func (c *rpcClient) sendDirectMailboxResultLocked(token *directMailboxToken, result directMailboxResult) {
	select {
	case token.result <- result:
	default:
	}
}

func (c *rpcClient) noteStaleDirectMailboxEventsLocked(token *directMailboxToken) {
	for _, item := range c.backlog {
		if receiptCustomType(item.value) == "mecha.mailbox.appended.v1" {
			c.sendDirectMailboxResultLocked(token, directMailboxResult{diagnosticCode: "stale_receipt"})
		}
	}
}

func (c *rpcClient) noteDirectRootControlRequestLocked(event *RootControlEvent) {
	token := c.directMailbox
	if event == nil || token == nil || !token.receiptSeen || !rootControlUUIDRE.MatchString(event.RequestID) || event.RuntimeGeneration != token.generation || event.SessionEpoch != token.epoch || event.ManifestSHA256 != token.manifestHash {
		return
	}
	if token.pendingRootRequests == nil {
		token.pendingRootRequests = map[string]struct{}{}
	}
	token.pendingRootRequests[event.RequestID] = struct{}{}
}

func (c *rpcClient) AcknowledgeDirectRootControlReceipt(requestID string, status directRootControlResultStatus) {
	c.mu.Lock()
	token := c.directMailbox
	if token == nil {
		c.mu.Unlock()
		return
	}
	if _, pending := token.pendingRootRequests[requestID]; !pending {
		c.mu.Unlock()
		return
	}
	if status != directRootControlResultSuccess && status != directRootControlResultError {
		// The status is an internal typed value selected from the durable row.
		// Fail closed if a future caller supplies an unknown value.
		c.mu.Unlock()
		return
	}
	delete(token.pendingRootRequests, requestID)
	if status == directRootControlResultSuccess {
		// A successful persisted result causes Pi to start one native follow-up
		// turn. Terminal errors do not, so they must not create this expectation.
		token.acceptedRootFollowUps++
	}
	released := status == directRootControlResultError && c.releaseDirectRootSettlementLocked(token)
	if released {
		c.sendDirectMailboxResultLocked(token, directMailboxResult{reply: token.reply, settled: true})
	}
	idleHandler := c.parentActivityHandler
	idleSequence := token.settledSequence
	c.signalLocked()
	c.mu.Unlock()
	// The provisional native settled frame was held back while the root request
	// was pending. A terminal error has no follow-up frame, so publish its idle
	// activity boundary when the durable result releases the direct turn.
	if released && idleHandler != nil {
		_ = c.enqueueSemantic(func() {
			idleHandler(map[string]any{"sequence": int64(idleSequence), "state": "idle"})
		})
	}
}

func (c *rpcClient) releaseDirectRootSettlementLocked(token *directMailboxToken) bool {
	if token == nil || len(token.pendingRootRequests) != 0 || token.acceptedRootFollowUps != 0 || token.settledSequence == 0 || token.settledReleased {
		return false
	}
	token.settledReleased = true
	return true
}

// RejectRootControlRequest releases only a manager-invalid request whose full
// runtime fence matches this direct turn. If Pi already emitted its provisional
// settled boundary, emit the deferred idle exactly once after the last root is
// rejected.
func (c *rpcClient) RejectRootControlRequest(requestID string, fence store.ManagedRuntimeFence) {
	c.mu.Lock()
	defer c.mu.Unlock()
	token := c.directMailbox
	if token == nil || !token.receiptSeen || requestID == "" || !rootControlUUIDRE.MatchString(requestID) || token.generation != fence.RuntimeGeneration || token.epoch != fence.SessionEpoch || token.manifestHash != fence.ManifestHash {
		return
	}
	if _, pending := token.pendingRootRequests[requestID]; !pending {
		return
	}
	delete(token.pendingRootRequests, requestID)
	if c.releaseDirectRootSettlementLocked(token) {
		c.sendDirectMailboxResultLocked(token, directMailboxResult{reply: token.reply, settled: true})
	}
	c.signalLocked()
}

func (c *rpcClient) classifyDirectMailboxEvent(event map[string]any, token *directMailboxToken) (directMailboxResult, bool) {
	kind, _ := event["type"].(string)
	if kind == "agent_settled" {
		if !agentSettledFrame(event) {
			return directMailboxResult{}, false
		}
		// A root tool completes on a triggerTurn follow-up. Its source result can
		// arrive after the model's provisional response, so only the follow-up's
		// settled event releases the direct delivery lane.
		if token.settledSequence == 0 {
			token.settledSequence = c.eventSequence
			if token.acceptedRootFollowUps != 0 {
				return directMailboxResult{}, true
			}
		}
		if token.acceptedRootFollowUps != 0 {
			token.acceptedRootFollowUps--
			if c.releaseDirectRootSettlementLocked(token) {
				return directMailboxResult{reply: token.reply, settled: true}, true
			}
			return directMailboxResult{}, true
		}
		if len(token.pendingRootRequests) != 0 || !token.receiptSeen || token.settledReleased {
			return directMailboxResult{}, true
		}
		token.settledReleased = true
		return directMailboxResult{reply: token.reply, settled: true}, true
	}
	if kind != "message_end" {
		// Unrelated stream traffic must continue to semantic/ordinary classification
		// or the bounded unknown alarm; direct delivery owns no generic frames.
		return directMailboxResult{}, false
	}
	if receiptCustomType(event) == "mecha.mailbox.appended.v1" {
		details := receiptDetails(event)
		if details == nil {
			return directMailboxResult{diagnosticCode: "malformed_receipt"}, true
		}
		if details["protocol"] != "mecha.mailbox/v1" || details["message_id"] != token.messageID || details["runtime_generation"] != token.generation || !sameEpoch(details["session_epoch"], token.epoch) {
			return directMailboxResult{diagnosticCode: "mismatched_receipt"}, true
		}
		if c.eventSequence <= token.watermark {
			return directMailboxResult{diagnosticCode: "stale_receipt"}, true
		}
		if token.receiptSeen {
			return directMailboxResult{diagnosticCode: "duplicate_receipt"}, true
		}
		token.receiptSeen = true
		identity := directMailboxEntryIdentity(event, c.eventSequence)
		token.receipt = &DirectMailboxReceipt{EntryIdentity: identity}
		result := directMailboxResult{receipt: token.receipt}
		if token.settledSequence != 0 && len(token.pendingRootRequests) == 0 && token.acceptedRootFollowUps == 0 && !token.settledReleased {
			token.settledReleased = true
			result.settled = true
		}
		return result, true
	}
	if !token.receiptSeen {
		return directMailboxResult{}, false
	}
	body, ok := directMailboxAssistantText(event)
	if !ok {
		return directMailboxResult{}, false
	}
	// A tool-using turn can emit more than one assistant message. Keep the last
	// visible text and publish it only with agent_settled.
	token.reply = &DirectMailboxReply{EntryIdentity: directMailboxEntryIdentity(event, c.eventSequence), Body: body}
	return directMailboxResult{}, true
}

func directMailboxAssistantText(event map[string]any) (string, bool) {
	message := nestedMessage(event)
	if message == nil || message["role"] != "assistant" {
		return "", false
	}
	content, ok := message["content"].([]any)
	if !ok {
		return "", false
	}
	var text strings.Builder
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok || block["type"] != "text" {
			continue
		}
		value, ok := block["text"].(string)
		if !ok {
			return "", false
		}
		text.WriteString(value)
		if text.Len() > 64*1024 {
			return "", false
		}
	}
	if text.Len() == 0 {
		return "", false
	}
	return text.String(), true
}

func directMailboxEntryIdentity(event map[string]any, sequence uint64) string {
	for _, candidate := range []map[string]any{event, nestedMessage(event)} {
		for _, key := range []string{"entry_id", "id"} {
			if value, ok := candidate[key].(string); ok && value != "" {
				return value
			}
		}
	}
	return fmt.Sprintf("event-%d", sequence)
}

func nestedMessage(event map[string]any) map[string]any {
	message, _ := event["message"].(map[string]any)
	return message
}

func rootControlUUID(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && rootControlUUIDRE.MatchString(text)
}
func rootControlSHA(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && manifestSHA256RE.MatchString(text)
}
func rootControlText(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && validManifestText(text, 256, false)
}
func rootControlEpoch(value any) (int64, bool) {
	switch item := value.(type) {
	case json.Number:
		parsed, err := item.Int64()
		return parsed, err == nil && parsed >= 1
	case int64:
		return item, item >= 1
	case int:
		return int64(item), item >= 1
	case float64:
		return int64(item), item >= 1 && item == float64(int64(item))
	default:
		return 0, false
	}
}

func validOrdinaryToolName(value string) bool {
	if value == "" || !utf8.ValidString(value) || len([]byte(value)) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '_' && char != '-' && char != '.' && char != ':' {
			return false
		}
	}
	return true
}

// classifyRootControlEvent identifies only the documented completed hidden
// request. It deliberately ignores partial frames and malformed candidates.
// classifyBuiltinToolLifecycleEvent accepts exact native ordered start and
// terminal frames for every safely named Pi tool. It carries start input to the
// store's pre-persistence redaction boundary while deliberately ignoring result
// and every update field.
func classifyBuiltinToolLifecycleEvent(event map[string]any, watermark uint64) (*BuiltinToolLifecycleEvent, bool) {
	kind, _ := event["type"].(string)
	if kind != "tool_execution_start" && kind != "tool_execution_end" {
		return nil, false
	}
	if kind == "tool_execution_start" {
		if !exactJSONKeys(event, "type", "toolCallId", "toolName", "args") {
			return nil, true
		}
	} else if !exactJSONKeys(event, "type", "toolCallId", "toolName", "result", "isError") {
		return nil, true
	}
	callID, callOK := event["toolCallId"].(string)
	toolName, toolOK := event["toolName"].(string)
	if !callOK || !validBuiltinToolCallID(callID) || !toolOK {
		return nil, true
	}
	if kind == "tool_execution_start" && !validOrdinaryToolName(toolName) {
		return nil, true
	}
	if kind == "tool_execution_end" && !validOrdinaryToolName(toolName) {
		return nil, true
	}
	result := &BuiltinToolLifecycleEvent{ToolCallID: callID, ToolName: toolName, Watermark: watermark}
	if kind == "tool_execution_start" {
		result.Phase = "start"
		result.ToolInput = cloneJSONValue(event["args"])
		return result, true
	}
	isError, errorOK := event["isError"].(bool)
	if !errorOK {
		return nil, true
	}
	result.Phase, result.IsError = "end", isError
	return result, true
}

func validBuiltinToolName(name string) bool {
	return name == "read" || name == "bash" || name == "edit" || name == "write"
}

func validBuiltinToolCallID(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}

func classifyRootControlEvent(event map[string]any, watermark uint64) (*RootControlEvent, bool) {
	// Pi's RPC stream uses message_end, while the native custom-message stream
	// exposes the same persisted entry directly. Both are durable completed
	// entries; accept only these two shapes and apply the identical closed
	// envelope validation below.
	message := nestedMessage(event)
	switch event["type"] {
	case "message_end":
		if message == nil || message["customType"] != "mecha.control.request.v1" {
			return nil, false
		}
		if !exactJSONKeys(event, "type", "message") || !exactJSONKeys(message, "role", "customType", "content", "display", "details", "timestamp") || message["role"] != "custom" {
			return nil, true
		}
		if !validWireNumber(message["timestamp"]) {
			return nil, true
		}
	case "custom_message":
		if event["customType"] != "mecha.control.request.v1" {
			return nil, false
		}
		// This direct route is a normalized transport event, not a raw persisted
		// session entry; its producer emits exactly these five fields.
		if !exactJSONKeys(event, "type", "customType", "content", "display", "details") {
			return nil, true
		}
		message = event
	default:
		return nil, false
	}
	if message["display"] != false || message["content"] != "Router control request queued." {
		return nil, true
	}
	details, ok := message["details"].(map[string]any)
	if !ok {
		return nil, true
	}
	if err := exactKeys(details, "protocol", "request_id", "tool_call_id", "capability_id", "tool_name", "definition_id", "definition_sha256", "operation_id", "manifest_sha256", "runtime_generation", "session_epoch", "context_ref", "arguments"); err != nil {
		return nil, true
	}
	requestID, idOK := rootControlUUID(details["request_id"])
	toolCallID, callOK := rootControlText(details["tool_call_id"])
	capabilityID, capabilityOK := rootControlText(details["capability_id"])
	toolName, nameOK := rootControlText(details["tool_name"])
	definitionID, definitionOK := rootControlText(details["definition_id"])
	definitionSHA, definitionSHAOK := rootControlSHA(details["definition_sha256"])
	operationID, operationOK := rootControlText(details["operation_id"])
	manifestSHA, manifestOK := rootControlSHA(details["manifest_sha256"])
	generation, generationOK := rootControlUUID(details["runtime_generation"])
	epoch, epochOK := rootControlEpoch(details["session_epoch"])
	arguments := details["arguments"]
	if details["protocol"] != "mecha.control/v1" || details["context_ref"] != "root" || !idOK || !callOK || !capabilityOK || !nameOK || !definitionOK || !definitionSHAOK || definitionID != "def_"+definitionSHA || !operationOK || !manifestOK || !generationOK || !epochOK || !boundedJSON(arguments, 1, &jsonBounds{}) {
		return nil, true
	}
	return &RootControlEvent{RequestID: requestID, ToolCallID: toolCallID, CapabilityID: capabilityID, ToolName: toolName, DefinitionID: definitionID, DefinitionSHA256: definitionSHA, OperationID: operationID, ManifestSHA256: manifestSHA, RuntimeGeneration: generation, SessionEpoch: epoch, Arguments: cloneJSONValue(arguments), Watermark: watermark}, true
}

// classifyChildDeltaEvent admits only the prescribed lifecycle delta. It is
// deliberately distinct from the readiness snapshot, whose watermark rules are
// stricter and whose receipt is consumed synchronously by launch.
func classifyChildDeltaEvent(event map[string]any) (map[string]any, bool) {
	if event["type"] != "message_end" {
		return nil, false
	}
	message := nestedMessage(event)
	if message == nil || message["customType"] != "mecha.child.delta.v1" {
		return nil, false
	}
	if !exactJSONKeys(event, "type", "message") {
		return nil, true
	}
	if message["display"] != false || message["content"] != "Managed child lifecycle updated." {
		return nil, true
	}
	details := receiptDetails(message)
	if details == nil {
		return nil, true
	}
	delta, err := normalizeChildDeltaReceipt(details)
	if err != nil {
		return nil, true
	}
	return delta, true
}

// classifyParentCompactionEvent admits only native Pi RPC lifecycle events.
// It never infers an outcome from transcript text, a disconnect, or absence.
func classifyParentCompactionEvent(event map[string]any, sequence uint64) (map[string]any, bool) {
	kind, _ := event["type"].(string)
	if kind != "compaction_start" && kind != "compaction_end" {
		return nil, false
	}
	if !validParentCompactionWireEvent(event) {
		return nil, true
	}
	normalized, err := normalizeParentCompactionEvent(event, sequence)
	if err != nil {
		return nil, true
	}
	return normalized, true
}

// classifyParentActivityEvent admits only Pi's native lifecycle boundary. The
// settled frame still reaches the direct-mail matcher below so its final reply
// is persisted before the serialized parent lane returns to idle.
func validParentCompactionWireEvent(event map[string]any) bool {
	reason, reasonOK := event["reason"].(string)
	if !reasonOK || (reason != "manual" && reason != "threshold" && reason != "overflow") {
		return false
	}
	if event["type"] == "compaction_start" {
		return exactJSONKeys(event, "type", "reason")
	}
	aborted, abortedOK := event["aborted"].(bool)
	willRetry, retryOK := event["willRetry"].(bool)
	if !abortedOK || !retryOK {
		return false
	}
	if result, hasResult := event["result"]; hasResult {
		// JSON.stringify omits undefined result; an admitted completion is the
		// sole end variant that carries a result on Pi's RPC wire.
		resultMap, resultOK := result.(map[string]any)
		_, summaryOK := resultMap["summary"].(string)
		_, firstKeptOK := resultMap["firstKeptEntryId"].(string)
		beforeOK := validWireNumber(resultMap["tokensBefore"])
		afterOK := validWireNumber(resultMap["estimatedTokensAfter"])
		usage, hasUsage := resultMap["usage"]
		usageOK := !hasUsage || validUsage(usage)
		return resultOK && summaryOK && firstKeptOK && beforeOK && afterOK && usageOK && !aborted && !willRetry && exactJSONKeys(event, "type", "reason", "result", "aborted", "willRetry") && allowedJSONKeys(resultMap, []string{"summary", "firstKeptEntryId", "tokensBefore", "estimatedTokensAfter"}, []string{"usage", "details"})
	}
	if aborted {
		return !willRetry && exactJSONKeys(event, "type", "reason", "aborted", "willRetry")
	}
	errorMessage, errorOK := event["errorMessage"].(string)
	return !willRetry && errorOK && errorMessage != "" && exactJSONKeys(event, "type", "reason", "aborted", "willRetry", "errorMessage")
}

func validWireNumber(value any) bool {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return false
		}
		number = parsed
	default:
		return false
	}
	return number >= 0 && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func classifyParentActivityEvent(event map[string]any, sequence uint64) (map[string]any, bool) {
	switch event["type"] {
	case "agent_start", "agent_settled":
		if !exactJSONKeys(event, "type") {
			return nil, true
		}
	case "agent_end":
		messages, ok := event["messages"].([]any)
		if !exactJSONKeys(event, "type", "messages") || !ok || !boundedJSON(messages, 1, &jsonBounds{}) {
			return nil, true
		}
	default:
		return nil, false
	}
	normalized, err := normalizeParentActivityEvent(event, sequence)
	if err != nil {
		return nil, true
	}
	return normalized, true
}

func isUnboundPostCompactionNotification(event map[string]any) bool {
	if !exactJSONKeys(event, "type", "message") || event["type"] != "message_end" {
		return false
	}
	message := nestedMessage(event)
	if message == nil || message["customType"] != "mecha.post_compaction.v1" || message["display"] != true || message["content"] != "COMPACTION-CERT-DONE" {
		return false
	}
	_, hasDetails := message["details"]
	return !hasDetails
}

// classifyParentCompactionNotificationEvent admits only Pi's durable visible
// handoff receipt. Its runtime binding fields are rechecked by the manager;
// content stays out of the event because it is user-authored notification text.
func classifyParentCompactionNotificationEvent(event map[string]any, sequence uint64) (map[string]any, bool) {
	if event["type"] != "message_end" {
		return nil, false
	}
	message := nestedMessage(event)
	if message == nil || message["customType"] != "mecha.post_compaction.v1" {
		return nil, false
	}
	if message["display"] != true || sequence == 0 || sequence > uint64(readinessMaxSafeInteger) {
		return nil, true
	}
	details := receiptDetails(message)
	if details == nil || exactKeys(details, "protocol", "runtime_generation", "session_epoch", "manifest_sha256") != nil || details["protocol"] != "mecha.post-compaction/v1" {
		return nil, true
	}
	generation, generationOK := readinessBoundedString(details["runtime_generation"], readinessMaxJSONStringBytes, false)
	epoch, epochOK := readinessInteger(details["session_epoch"])
	manifest, manifestOK := rootControlSHA(details["manifest_sha256"])
	if !generationOK || !epochOK || epoch < 1 || !manifestOK {
		return nil, true
	}
	return map[string]any{"sequence": int64(sequence), "runtime_generation": generation, "session_epoch": epoch, "manifest_sha256": manifest}, true
}

func isRootControlResultEvent(event map[string]any) bool {
	message := nestedMessage(event)
	// Pi's live stdout receipt is a message_end frame. Supported explicit-append
	// paths may expose the persisted custom entry directly. Both forms carry the
	// same closed custom type and are verified against the reserved ID before the
	// ledger can settle.
	if message == nil {
		message = event
	}
	return message["customType"] == "mecha.control.result.v1"
}
func classifyRootControlResultEvent(event map[string]any, token *rootResultToken, watermark uint64) bool {
	return token != nil && !token.closed && watermark > token.watermark && isRootControlResultEvent(event)
}

func (c *rpcClient) SetRootControlEventHandler(handler func(RootControlEvent)) {
	c.mu.Lock()
	c.rootControlHandler = handler
	c.mu.Unlock()
}

func (c *rpcClient) SetChildDeltaEventHandler(handler func(map[string]any)) {
	c.mu.Lock()
	c.childDeltaHandler = handler
	c.mu.Unlock()
}

func (c *rpcClient) SetParentCompactionEventHandler(handler func(map[string]any)) {
	c.mu.Lock()
	c.parentCompactionHandler = handler
	c.mu.Unlock()
}

func (c *rpcClient) SetParentCompactionNotificationHandler(handler func(map[string]any)) {
	c.mu.Lock()
	c.parentCompactionNotificationHandler = handler
	c.mu.Unlock()
}

func (c *rpcClient) SetParentActivityEventHandler(handler func(map[string]any)) {
	c.mu.Lock()
	c.parentActivityHandler = handler
	c.mu.Unlock()
}

func (c *rpcClient) ParentActivitySequence() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventSequence
}

func (c *rpcClient) SetRPCReaderTerminalHandler(handler func(error)) {
	c.mu.Lock()
	c.readerTerminalHandler = handler
	if handler != nil && c.readerTerminal != nil && !c.readerTerminalHandlerCalled {
		c.scheduleReaderTerminalNotificationLocked(handler, c.readerTerminal)
	}
	c.mu.Unlock()
}

func (c *rpcClient) ArmRootControlResult(requestID, entryID string) (RootControlAppendToken, error) {
	if !rootControlUUIDRE.MatchString(requestID) || !strings.HasPrefix(entryID, "mcr1_") || len(entryID) != len("mcr1_")+32 {
		return nil, errors.New("managed control result token is invalid")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminal != nil {
		return nil, c.terminal
	}
	if c.rootResult != nil {
		return nil, errors.New("managed control result matcher is already armed")
	}
	token := &rootResultToken{client: c, requestID: requestID, entryID: entryID, watermark: c.eventSequence, result: make(chan RootControlResultReceipt, 2)}
	c.rootResult = token
	return token, nil
}

func (c *rpcClient) SubmitRootControlResult(ctx context.Context, rawToken RootControlAppendToken, envelope map[string]any) (<-chan RootControlResultReceipt, error) {
	token, ok := rawToken.(*rootResultToken)
	if !ok || token.client != c {
		return nil, errors.New("managed control result token is invalid")
	}
	encoded, err := encodeRPCEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.terminal != nil {
		err := c.terminal
		c.mu.Unlock()
		return nil, err
	}
	if c.rootResult != token || token.closed {
		c.mu.Unlock()
		return nil, errors.New("managed control result token is stale")
	}
	c.nextID++
	id := fmt.Sprintf("managed-%d", c.nextID)
	payload, err := json.Marshal(map[string]any{"id": id, "type": "prompt", "message": "/mecha-control-result-v1 " + encoded})
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	response := make(chan rpcResponse, 1)
	c.pending[id] = response
	c.pendingCommands[id] = "prompt"
	if _, err = fmt.Fprintf(c.stdin, "%s\n", payload); err != nil {
		delete(c.pending, id)
		delete(c.pendingCommands, id)
		c.terminalizeLocked(err)
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()
	select {
	case value := <-response:
		if value.err != nil {
			return nil, value.err
		}
		return token.result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		delete(c.pendingCommands, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *rpcClient) ReconcileRootControlResult(ctx context.Context, payload map[string]any) (RootControlPersistedReceipt, error) {
	requestID, requestOK := payload["request_id"].(string)
	resultID, resultOK := payload["result_id"].(string)
	generation, generationOK := payload["runtime_generation"].(string)
	manifest, manifestOK := payload["manifest_sha256"].(string)
	epoch, epochOK := readinessInteger(payload["target_session_epoch"])
	canonical, err := canonicalJSON(payload)
	if err != nil || !requestOK || !resultOK || !generationOK || !manifestOK || !epochOK {
		return RootControlPersistedReceipt{}, errors.New("managed control reconciliation payload is invalid")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(canonical))
	value, _, err := c.privateCommand(ctx, "mecha-control-result-reconcile-v1", map[string]any{
		"protocol": "mecha.control-result-reconcile/v1", "request_id": requestID, "result_id": resultID,
		"payload_hash": hash, "runtime_generation": generation, "session_epoch": epoch, "manifest_sha256": manifest,
	})
	if err != nil {
		return RootControlPersistedReceipt{}, err
	}
	if exactKeys(value, "protocol", "receipt_id", "request_id", "result_id", "payload_hash", "runtime_generation", "session_epoch", "manifest_sha256") != nil || value["protocol"] != "mecha.control.result.persisted.v1" || value["request_id"] != requestID || value["result_id"] != resultID || value["payload_hash"] != hash || value["runtime_generation"] != generation || value["manifest_sha256"] != manifest || !sameEpoch(value["session_epoch"], epoch) {
		return RootControlPersistedReceipt{}, errors.New("managed control reconciliation receipt is invalid")
	}
	receiptID, ok := value["receipt_id"].(string)
	if !ok || receiptID == "" {
		return RootControlPersistedReceipt{}, errors.New("managed control reconciliation receipt is invalid")
	}
	return RootControlPersistedReceipt{ReceiptID: receiptID, RequestID: requestID, ResultID: resultID, PayloadSHA256: hash, RuntimeGeneration: generation, SessionEpoch: epoch, ManifestSHA256: manifest}, nil
}

// RecoverManagedMailbox delegates the bounded transcript scan to the extension
// and accepts only its closed receipt under the attempt's immutable fence.
func (c *rpcClient) RecoverManagedMailbox(ctx context.Context, attempt store.ManagedDirectAttempt) (DirectMailboxRecovery, error) {
	if attempt.SessionEntryWatermark != directMailboxReconciliationKey(attempt) {
		return DirectMailboxRecovery{Diagnostics: []string{"missing_reconciliation_key"}}, nil
	}
	value, _, err := c.privateCommand(ctx, "mecha-mailbox-reconcile-v1", map[string]any{
		"protocol": "mecha.mailbox-reconcile/v1", "message_id": attempt.MessageID, "content": attempt.Message.Body,
		"runtime_generation": attempt.Fence.RuntimeGeneration, "session_epoch": attempt.Fence.SessionEpoch, "manifest_sha256": attempt.Fence.ManifestHash,
	})
	if err != nil {
		return DirectMailboxRecovery{}, err
	}
	if exactKeys(value, "protocol", "receipt_id", "message_id", "runtime_generation", "session_epoch", "manifest_sha256") != nil || value["protocol"] != "mecha.mailbox.appended.persisted.v1" || value["message_id"] != attempt.MessageID || value["runtime_generation"] != attempt.Fence.RuntimeGeneration || value["manifest_sha256"] != attempt.Fence.ManifestHash || !sameEpoch(value["session_epoch"], attempt.Fence.SessionEpoch) {
		return DirectMailboxRecovery{}, errors.New("managed mailbox reconciliation receipt is invalid")
	}
	receiptID, ok := value["receipt_id"].(string)
	if !ok || receiptID == "" {
		return DirectMailboxRecovery{}, errors.New("managed mailbox reconciliation receipt is invalid")
	}
	return DirectMailboxRecovery{Receipt: &DirectMailboxReceipt{EntryIdentity: receiptID}}, nil
}
func (c *rpcClient) Close() error {
	c.closeOnce.Do(func() {
		c.terminalize(errors.New("managed RPC closed"))
		if closer, ok := c.stdin.(io.Closer); ok {
			_ = closer.Close()
		}
		c.stdoutCloseOnce.Do(func() {
			if closer, ok := c.stdout.(io.Closer); ok {
				_ = closer.Close()
			}
		})
		if c.closeFn != nil {
			c.closeErr = c.closeFn()
		}
	})
	return c.closeErr
}

// WaitForClose joins the reader, terminal notification, and semantic callbacks
// admitted before Close. It is separate from Close so a callback can close its
// own RPC without waiting on its dispatcher.
func (c *rpcClient) WaitForClose() {
	<-c.semanticDone
	c.mu.Lock()
	handlerDone := c.terminalHandlerDone
	c.mu.Unlock()
	<-handlerDone
	<-c.readerDone
}

// closeRPCAndWait is for external owners that are releasing an RPC binding.
// A callback that closes itself must call Close only; it must not join here.
func closeRPCAndWait(rpc RPC) error {
	if rpc == nil {
		return nil
	}
	err := rpc.Close()
	if waiter, ok := rpc.(interface{ WaitForClose() }); ok {
		waiter.WaitForClose()
	}
	return err
}

type processRPCFactory struct{}

func DefaultRPCFactory() RPCFactory { return processRPCFactory{} }
func (processRPCFactory) Open(_ context.Context, process Process) (RPC, error) {
	if process == nil || process.Stdin() == nil || process.Stdout() == nil {
		return nil, errors.New("managed process pipes are unavailable")
	}
	return NewRPCClient(process.Stdin(), process.Stdout(), func() error { return nil }), nil
}
func waitForRPC(ctx context.Context, rpc RPC, timeout time.Duration, fn func(context.Context) error) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return fn(child)
}
