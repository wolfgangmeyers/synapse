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

type RPC interface {
	GetState(context.Context) (map[string]any, error)
	GetCommands(context.Context) ([]string, error)
	Bootstrap(context.Context, map[string]any) error
	WaitForReceipt(context.Context, string, string, int, int64, string) error
	GetActiveTools(context.Context) ([]string, error)
	Prompt(context.Context, string) error
	Close() error
}

type RPCFactory interface {
	Open(context.Context, Process) (RPC, error)
}
type RPCFactoryFunc func(context.Context, Process) (RPC, error)

func (f RPCFactoryFunc) Open(ctx context.Context, process Process) (RPC, error) {
	return f(ctx, process)
}

type rpcClient struct {
	stdin       io.Writer
	stdout      io.Reader
	closeFn     func() error
	mu          sync.Mutex
	pending     map[string]chan rpcResponse
	events      chan map[string]any
	nextID      int64
	activeTools []string
	closeOnce   sync.Once
	closeErr    error
}
type rpcResponse struct {
	value map[string]any
	err   error
}

func NewRPCClient(stdin io.Writer, stdout io.Reader, closeFn func() error) RPC {
	client := &rpcClient{stdin: stdin, stdout: stdout, closeFn: closeFn, pending: map[string]chan rpcResponse{}, events: make(chan map[string]any, 128)}
	go client.readLoop()
	return client
}
func (c *rpcClient) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 4096), MaxRPCLineBytes+1)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > MaxRPCLineBytes {
			c.failAll(errors.New("RPC line exceeds 1 MiB"))
			return
		}
		var raw map[string]any
		if json.Unmarshal(line, &raw) != nil {
			c.failAll(errors.New("Pi emitted malformed RPC JSON"))
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
						returnErr(ch, fmt.Errorf("Pi RPC error: %v", value))
					} else {
						ch <- rpcResponse{value: raw}
					}
					continue
				}
			}
		}
		select {
		case c.events <- raw:
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		c.failAll(err)
	} else {
		c.failAll(io.EOF)
	}
}
func returnErr(ch chan rpcResponse, err error) { ch <- rpcResponse{err: err} }
func (c *rpcClient) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcResponse{err: err}
	}
}
func (c *rpcClient) call(ctx context.Context, typ string, values map[string]any) (map[string]any, error) {
	c.mu.Lock()
	c.nextID++
	id := fmt.Sprintf("managed-%d", c.nextID)
	response := make(chan rpcResponse, 1)
	c.pending[id] = response
	c.mu.Unlock()
	payload := map[string]any{"id": id, "type": typ}
	for key, value := range values {
		payload[key] = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	if _, err = fmt.Fprintf(c.stdin, "%s\n", encoded); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case result := <-response:
		return result.value, result.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}
func unwrap(value map[string]any) map[string]any {
	if data, ok := value["data"].(map[string]any); ok {
		return data
	}
	return value
}
func (c *rpcClient) GetState(ctx context.Context) (map[string]any, error) {
	v, e := c.call(ctx, "get_state", nil)
	return unwrap(v), e
}
func (c *rpcClient) GetCommands(ctx context.Context) ([]string, error) {
	v, e := c.call(ctx, "get_commands", nil)
	if e != nil {
		return nil, e
	}
	v = unwrap(v)
	raw := v["commands"]
	if raw == nil {
		return nil, errors.New("Pi get_commands response has no commands")
	}
	var result []string
	switch items := raw.(type) {
	case []any:
		for _, item := range items {
			switch value := item.(type) {
			case string:
				result = append(result, value)
			case map[string]any:
				if name, ok := value["name"].(string); ok {
					result = append(result, name)
				}
			}
		}
	default:
		return nil, errors.New("Pi commands response is invalid")
	}
	// Keep the raw names through the readiness boundary. Normalizing here can
	// turn a versioned or slash-prefixed managed command into a valid-looking
	// canonical name and hide a collision.
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
func (c *rpcClient) Bootstrap(ctx context.Context, envelope map[string]any) error {
	encoded, err := encodeRPCEnvelope(envelope)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, "prompt", map[string]any{"message": "/mecha-bootstrap-v1 " + encoded})
	if err != nil {
		return err
	}
	return nil
}
func (c *rpcClient) WaitForReceipt(ctx context.Context, generation, manifestHash string, manifestRevision int, epoch int64, _ string) error {
	for {
		select {
		case event := <-c.events:
			if receiptMatches(event, "mecha.tools.accepted.v1", generation, manifestHash, manifestRevision, epoch) {
				details := receiptDetails(event)
				if active, ok := stringSlice(details["active_tools"]); ok {
					c.mu.Lock()
					c.activeTools = append([]string(nil), active...)
					c.mu.Unlock()
				}
				return nil
			}
			if receiptMatches(event, "mecha.tools.rejected.v1", generation, "", 0, epoch) {
				return errors.New("managed tool manifest was rejected")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
func receiptMatches(event map[string]any, want, generation, hash string, revision int, epoch int64) bool {
	if custom, ok := event["customType"].(string); ok && custom == want {
		details := receiptDetails(event)
		if details == nil {
			return false
		}
		if details["runtime_generation"] != generation {
			return false
		}
		if !sameEpoch(details["session_epoch"], epoch) {
			return false
		}
		if revision > 0 && !sameEpoch(details["manifest_revision"], int64(revision)) {
			return false
		}
		return hash == "" || details["manifest_sha256"] == hash
	}
	if message, ok := event["message"].(map[string]any); ok {
		return receiptMatches(message, want, generation, hash, revision, epoch)
	}
	return false
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
