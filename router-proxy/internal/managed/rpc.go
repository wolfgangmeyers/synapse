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
