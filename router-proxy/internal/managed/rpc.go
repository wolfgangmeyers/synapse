package managed

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var ErrReadinessBacklogFull = errors.New("managed readiness receipt backlog is full")
var ErrAbortResponseMalformed = errors.New("Pi Abort response is malformed")
var ErrAbortResponseRejected = errors.New("Pi Abort response was not successful")
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

// RestartTurnSettler is deliberately narrower than RPC. Existing launch-only
// callers remain source-compatible; controlled active restart requires this
// binding-scoped stdout capability.
type RestartTurnSettler interface {
	SettlePreparedRestartTurn(context.Context, store.ManagedRuntimeFence) error
}

// DirectMailboxRPC is private to managed delivery. It deliberately does not
// widen RPC, so launch and readiness callers cannot issue router mail.
type DirectMailboxRPC interface {
	PrepareManagedMailbox(context.Context) (string, error)
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

type RootControlAppendToken interface {
	Watermark() uint64
	Release()
}

// RootControlRPC is a private S4 capability. It deliberately exposes only
// result append, its receipt token, and exact entry lookup; no history method
// is part of this interface.
type RootControlRPC interface {
	SetRootControlEventHandler(func(RootControlEvent))
	ArmRootControlResult(requestID, entryID string) (RootControlAppendToken, error)
	SubmitRootControlResult(context.Context, RootControlAppendToken, map[string]any) (<-chan RootControlResultReceipt, error)
	GetRootControlEntry(context.Context, string) (map[string]any, error)
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

// ParentActivityRPC receives Pi's native parent-turn start and settled events.
// The router uses it for the durable managed-agent activity projection; prompt
// acceptance itself is not evidence that Pi started work.
type ParentActivityRPC interface {
	SetParentActivityEventHandler(func(map[string]any))
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
type directMailboxToken struct {
	messageID   string
	generation  string
	epoch       int64
	watermark   uint64
	receiptSeen bool
	reply       *DirectMailboxReply
	result      chan directMailboxResult
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

type rpcClient struct {
	stdin   io.Writer
	stdout  io.Reader
	closeFn func() error

	mu          sync.Mutex
	pending     map[string]chan rpcResponse
	nextID      int64
	activeTools []string

	// The matcher is owned under mu. Responses never occupy this bounded FIFO.
	// A terminal error lives separately so a retained notification can never
	// satisfy a readiness wait after EOF, close, or process failure.
	eventSequence           uint64
	backlog                 []receiptNotification
	terminal                error
	notified                chan struct{}
	bootstrapWatermark      *uint64
	policyWatermark         *uint64
	snapshotWatermark       *uint64
	settlementToken         *restartSettlementToken
	directMailbox           *directMailboxToken
	rootControlHandler      func(RootControlEvent)
	childDeltaHandler       func(map[string]any)
	parentCompactionHandler func(map[string]any)
	parentActivityHandler   func(map[string]any)
	rootResult              *rootResultToken

	closeOnce sync.Once
	closeErr  error
}

func NewRPCClient(stdin io.Writer, stdout io.Reader, closeFn func() error) RPC {
	client := &rpcClient{stdin: stdin, stdout: stdout, closeFn: closeFn, pending: map[string]chan rpcResponse{}, notified: make(chan struct{}, 1)}
	go client.readLoop()
	return client
}

func (c *rpcClient) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 4096), MaxRPCLineBytes+1)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > MaxRPCLineBytes {
			c.terminalize(errors.New("RPC line exceeds 1 MiB"))
			return
		}
		decoded, err := strictJSON(line)
		raw, ok := decoded.(map[string]any)
		if err != nil || !ok {
			c.terminalize(errors.New("Pi emitted malformed RPC JSON"))
			return
		}
		if id, ok := raw["id"].(string); ok {
			if typ, _ := raw["type"].(string); typ == "response" || raw["error"] != nil {
				c.mu.Lock()
				ch := c.pending[id]
				delete(c.pending, id)
				c.mu.Unlock()
				if ch != nil {
					if value, hasError := raw["error"]; hasError && value != nil {
						ch <- rpcResponse{err: fmt.Errorf("Pi RPC error: %v", value)}
					} else {
						ch <- rpcResponse{value: raw}
					}
				}
				// A response belongs only to its request-ID waiter. Timed-out and
				// unknown responses are never notifications, so they cannot consume
				// the bounded readiness receipt FIFO.
				continue
			}
		}
		var rootCallback func(RootControlEvent)
		var rootEvent *RootControlEvent
		var childDeltaCallback func(map[string]any)
		var childDelta map[string]any
		var parentCompactionCallback func(map[string]any)
		var parentCompaction map[string]any
		var parentActivityCallback func(map[string]any)
		var parentActivity map[string]any
		var parentActivityCandidate bool
		c.mu.Lock()
		terminal := c.terminal != nil
		if !terminal {
			c.eventSequence++
			if event, candidate := classifyRootControlEvent(raw, c.eventSequence); candidate {
				// Root requests are control-plane input, not readiness receipts. Bad
				// candidates are dropped locally and cannot fill the receipt backlog.
				if event != nil && c.rootControlHandler != nil {
					rootCallback, rootEvent = c.rootControlHandler, event
				}
				c.mu.Unlock()
				if rootCallback != nil {
					go rootCallback(*rootEvent)
				}
				continue
			}
			if delta, candidate := classifyChildDeltaEvent(raw); candidate {
				// Lifecycle deltas stay outside the readiness FIFO. Their consumer
				// merges only a current, monotonically newer durable projection.
				if delta != nil && c.childDeltaHandler != nil {
					childDeltaCallback, childDelta = c.childDeltaHandler, delta
				}
				c.mu.Unlock()
				if childDeltaCallback != nil {
					go childDeltaCallback(childDelta)
				}
				continue
			}
			if event, candidate := classifyParentCompactionEvent(raw, c.eventSequence); candidate {
				if event != nil && c.parentCompactionHandler != nil {
					parentCompactionCallback, parentCompaction = c.parentCompactionHandler, event
				}
				c.mu.Unlock()
				if parentCompactionCallback != nil {
					go parentCompactionCallback(parentCompaction)
				}
				continue
			}
			if event, candidate := classifyParentActivityEvent(raw, c.eventSequence); candidate {
				parentActivityCandidate = true
				if event != nil && c.parentActivityHandler != nil {
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
			if isRootControlResultEvent(raw) {
				// Exact entry lookup verifies the persisted result. The entry event
				// itself is not a general readiness receipt.
				c.mu.Unlock()
				continue
			}
			if token := c.directMailbox; token != nil {
				if result, handled := c.classifyDirectMailboxEvent(raw, token); handled {
					c.sendDirectMailboxResultLocked(token, result)
					c.signalLocked()
					c.mu.Unlock()
					if parentActivityCallback != nil {
						go parentActivityCallback(parentActivity)
					}
					continue
				}
			}
			if parentActivityCandidate {
				c.mu.Unlock()
				if parentActivityCallback != nil {
					go parentActivityCallback(parentActivity)
				}
				continue
			}
			if len(c.backlog) == 128 {
				c.terminalizeLocked(ErrReadinessBacklogFull)
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
	}
	if err := scanner.Err(); err != nil {
		c.terminalize(err)
	} else {
		c.terminalize(io.EOF)
	}
}

func (c *rpcClient) signalLocked() {
	select {
	case c.notified <- struct{}{}:
	default:
	}
}
func (c *rpcClient) terminalize(err error) { c.mu.Lock(); c.terminalizeLocked(err); c.mu.Unlock() }
func (c *rpcClient) terminalizeLocked(err error) {
	if c.terminal != nil {
		return
	}
	c.terminal = err
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
		ch <- rpcResponse{err: err}
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
	watermark := c.eventSequence
	if _, err = fmt.Fprintf(c.stdin, "%s\n", encoded); err != nil {
		delete(c.pending, id)
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

// Abort intentionally uses the public wire command rather than a prompt
// wrapper. The response is part of the ownership proof: only Pi's typed,
// successful Abort acknowledgement counts before the caller reaps its group.
func (c *rpcClient) Abort(ctx context.Context) error {
	value, err := c.call(ctx, "abort", nil)
	if err != nil {
		return err
	}
	return validateAbortResponse(value)
}

func validateAbortResponse(value map[string]any) error {
	if value == nil {
		return ErrAbortResponseMalformed
	}
	typ, typeOK := value["type"].(string)
	command, commandOK := value["command"].(string)
	success, successOK := value["success"].(bool)
	if !typeOK || typ != "response" || !commandOK || command != "abort" || !successOK {
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
	token := &restartSettlementToken{fence: fence, watermark: c.eventSequence}
	c.settlementToken = token
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
		settled := -1
		for index, item := range c.backlog {
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

// PrepareManagedMailbox records a persisted session cursor before the fixed
// command is written. Recovery uses that cursor with Pi's documented
// get_entries `since` boundary and never falls back to a whole-session scan.
func (c *rpcClient) PrepareManagedMailbox(ctx context.Context) (string, error) {
	value, err := c.call(ctx, "get_entries", nil)
	if err != nil {
		return "", err
	}
	data := unwrap(value)
	if _, ok := data["entries"].([]any); !ok {
		return "", errors.New("Pi get_entries response has invalid entries")
	}
	cursor, ok := data["leafId"].(string)
	if !ok || cursor == "" {
		return "", errors.New("Pi get_entries response has no recovery cursor")
	}
	return cursor, nil
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
	token := &directMailboxToken{messageID: attempt.MessageID, generation: attempt.Fence.RuntimeGeneration, epoch: attempt.Fence.SessionEpoch, watermark: c.eventSequence, result: make(chan directMailboxResult, 128)}
	c.directMailbox = token
	c.noteStaleDirectMailboxEventsLocked(token)
	if _, err = fmt.Fprintf(c.stdin, "%s\n", payload); err != nil {
		delete(c.pending, id)
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
		for {
			select {
			case value := <-token.result:
				if value.err != nil {
					return
				}
				event := DirectMailboxEvent{Receipt: value.receipt, Reply: value.reply, Settled: value.settled, DiagnosticCode: value.diagnosticCode}
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

func (c *rpcClient) classifyDirectMailboxEvent(event map[string]any, token *directMailboxToken) (directMailboxResult, bool) {
	kind, _ := event["type"].(string)
	if kind == "agent_settled" {
		// A direct turn owns the lane until it settles. This keeps a follow-up
		// command from racing the response that follows the persisted receipt and
		// returns only the final visible assistant text after tool turns finish.
		return directMailboxResult{reply: token.reply, settled: token.receiptSeen}, true
	}
	if kind != "message_end" {
		// Activity and streaming frames are expected while the model works. They
		// are not receipt failures and must not flood direct diagnostics.
		return directMailboxResult{}, true
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
		return directMailboxResult{receipt: &DirectMailboxReceipt{EntryIdentity: identity}}, true
	}
	if !token.receiptSeen {
		return directMailboxResult{}, true
	}
	body, ok := directMailboxAssistantText(event)
	if !ok {
		return directMailboxResult{}, true
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

// classifyRootControlEvent identifies only the documented completed hidden
// request. It deliberately ignores partial frames and malformed candidates.
func classifyRootControlEvent(event map[string]any, watermark uint64) (*RootControlEvent, bool) {
	if event["type"] != "message_end" {
		return nil, false
	}
	message := nestedMessage(event)
	if message == nil || message["customType"] != "mecha.control.request.v1" {
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
	if event["type"] != "compaction_start" && event["type"] != "compaction_end" {
		return nil, false
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
func classifyParentActivityEvent(event map[string]any, sequence uint64) (map[string]any, bool) {
	if event["type"] != "agent_start" && event["type"] != "agent_settled" {
		return nil, false
	}
	normalized, err := normalizeParentActivityEvent(event, sequence)
	if err != nil {
		return nil, true
	}
	return normalized, true
}

func isRootControlResultEvent(event map[string]any) bool {
	message := nestedMessage(event)
	// Pi's live stdout receipt is a message_end frame, whereas get_entry and
	// some supported explicit-append paths expose the persisted custom entry
	// directly. Both forms carry the same closed custom type and are verified
	// again against the reserved ID before the ledger can settle.
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

func (c *rpcClient) SetParentActivityEventHandler(handler func(map[string]any)) {
	c.mu.Lock()
	c.parentActivityHandler = handler
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
	if _, err = fmt.Fprintf(c.stdin, "%s\n", payload); err != nil {
		delete(c.pending, id)
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
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *rpcClient) GetRootControlEntry(ctx context.Context, entryID string) (map[string]any, error) {
	if !strings.HasPrefix(entryID, "mcr1_") || len(entryID) != len("mcr1_")+32 {
		return nil, errors.New("managed control entry ID is invalid")
	}
	value, err := c.call(ctx, "get_entry", map[string]any{"entryId": entryID})
	if err != nil {
		return nil, err
	}
	data := unwrap(value)
	raw, found := data["entry"]
	if !found || raw == nil {
		return nil, nil
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("Pi get_entry response is invalid")
	}
	encoded, err := json.Marshal(entry)
	if err != nil || len(encoded) > 64*1024 {
		return nil, errors.New("Pi get_entry response is oversized")
	}
	return cloneJSONRecord(entry), nil
}

// RecoverManagedMailbox accepts a persisted append only when the recorded
// get_entries cursor yields one exact hidden custom-message entry. It never
// scans from the beginning of a session and returns metadata-only diagnostics.
func (c *rpcClient) RecoverManagedMailbox(ctx context.Context, attempt store.ManagedDirectAttempt) (DirectMailboxRecovery, error) {
	if attempt.SessionEntryWatermark == "" {
		return DirectMailboxRecovery{Diagnostics: []string{"missing_recovery_cursor"}}, nil
	}
	value, err := c.call(ctx, "get_entries", map[string]any{"since": attempt.SessionEntryWatermark})
	if err != nil {
		return DirectMailboxRecovery{}, err
	}
	data := unwrap(value)
	entries, ok := data["entries"].([]any)
	if !ok {
		return DirectMailboxRecovery{}, errors.New("Pi get_entries response has invalid entries")
	}
	result := DirectMailboxRecovery{}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		customType, _ := entry["customType"].(string)
		if customType == "" {
			continue
		}
		if customType != "mecha.mailbox.appended.v1" {
			result.Diagnostics = append(result.Diagnostics, "wrong_custom_type")
			continue
		}
		identity, diagnostic := exactPersistedMailboxReceipt(entry, attempt)
		if diagnostic != "" {
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			continue
		}
		if result.Receipt == nil {
			result.Receipt = &DirectMailboxReceipt{EntryIdentity: identity}
		} else {
			result.Diagnostics = append(result.Diagnostics, "duplicate_receipt")
		}
	}
	return result, nil
}

func exactPersistedMailboxReceipt(entry map[string]any, attempt store.ManagedDirectAttempt) (string, string) {
	identity, _ := entry["id"].(string)
	entryType, typeOK := entry["type"].(string)
	content, contentOK := entry["content"].(string)
	display, displayOK := entry["display"].(bool)
	details, detailsOK := entry["details"].(map[string]any)
	if identity == "" || !typeOK || entryType != "custom_message" || !contentOK || !displayOK || display || !detailsOK || len(details) != 4 {
		return "", "malformed_receipt"
	}
	if content != attempt.Message.Body || details["protocol"] != "mecha.mailbox/v1" || details["message_id"] != attempt.MessageID || details["runtime_generation"] != attempt.Fence.RuntimeGeneration || !sameEpoch(details["session_epoch"], attempt.Fence.SessionEpoch) {
		return "", "mismatched_receipt"
	}
	return identity, ""
}
func (c *rpcClient) Close() error {
	c.closeOnce.Do(func() {
		c.terminalize(errors.New("managed RPC closed"))
		if closer, ok := c.stdin.(io.Closer); ok {
			_ = closer.Close()
		}
		if c.closeFn != nil {
			c.closeErr = c.closeFn()
		}
	})
	return c.closeErr
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
