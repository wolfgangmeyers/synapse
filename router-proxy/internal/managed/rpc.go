package managed

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var ErrReadinessBacklogFull = errors.New("managed readiness receipt backlog is full")

type RPC interface {
	GetState(context.Context) (map[string]any, error)
	GetCommands(context.Context) ([]string, error)
	Bootstrap(context.Context, map[string]any) error
	WaitForReceipt(context.Context, string, string, int, int64, string) error
	GetActiveTools(context.Context) ([]string, error)
	Prompt(context.Context, string) error
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

type DirectMailboxReceipt struct{ EntryIdentity string }

type DirectMailboxEvent struct {
	Receipt        *DirectMailboxReceipt
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
	result      chan directMailboxResult
}
type directMailboxResult struct {
	receipt        *DirectMailboxReceipt
	diagnosticCode string
	err            error
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
	eventSequence      uint64
	backlog            []receiptNotification
	terminal           error
	notified           chan struct{}
	bootstrapWatermark *uint64
	policyWatermark    *uint64
	snapshotWatermark  *uint64
	settlementToken    *restartSettlementToken
	directMailbox      *directMailboxToken

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
		var raw map[string]any
		if json.Unmarshal(line, &raw) != nil {
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
		c.mu.Lock()
		terminal := c.terminal != nil
		if !terminal {
			c.eventSequence++
			if token := c.directMailbox; token != nil {
				if result, handled := c.classifyDirectMailboxEvent(raw, token); handled {
					c.sendDirectMailboxResultLocked(token, result)
					c.signalLocked()
					c.mu.Unlock()
					continue
				}
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
				event := DirectMailboxEvent{Receipt: value.receipt, DiagnosticCode: value.diagnosticCode}
				if event.Receipt != nil || event.DiagnosticCode != "" {
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
	if kind, _ := event["type"].(string); kind == "message_start" {
		return directMailboxResult{}, false
	}
	if kind, _ := event["type"].(string); kind != "message_end" {
		return directMailboxResult{diagnosticCode: "unrelated_receipt"}, true
	}
	if receiptCustomType(event) != "mecha.mailbox.appended.v1" {
		return directMailboxResult{diagnosticCode: "wrong_custom_type"}, true
	}
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
