package managedbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	DefaultRPCStreamCapacity = 128
	MaxRPCStreamRecordBytes  = MaxPayloadBytes
)

var (
	ErrRPCStreamClosed       = errors.New("managed RPC stream is closed")
	ErrRPCStreamInvalid      = errors.New("managed RPC stream record is invalid")
	ErrRPCStreamBackpressure = errors.New("managed RPC stream event queue is full")
	ErrRPCStreamOutOfOrder   = errors.New("managed RPC stream event is out of order")
	ErrRPCStreamStale        = errors.New("managed RPC stream fence is stale")
)

// RPCCommand is the cycle-free command admission contract. Queue must return
// only after the command is durable; Wake is called after Queue succeeds.
type RPCCommand struct {
	RequestID   string
	Name        string
	Payload     []byte
	PayloadHash string
	Fence       RuntimeFence
}

type RPCCommandQueue func(RPCCommand) error
type RPCStreamWake func()

// BridgeStreamFactory is the narrow seam a future Manager can use to bind its
// existing RPC client to the managed bridge. It carries no store or transport
// types.
type BridgeStreamFactory interface {
	Open(context.Context, RuntimeFence) (io.ReadWriteCloser, error)
	Attach(context.Context, RuntimeFence) (io.ReadWriteCloser, error)
}

type BridgeStreamFactoryFunc func(context.Context, RuntimeFence) (io.ReadWriteCloser, error)

func (f BridgeStreamFactoryFunc) Open(ctx context.Context, fence RuntimeFence) (io.ReadWriteCloser, error) {
	return f(ctx, fence)
}
func (f BridgeStreamFactoryFunc) Attach(ctx context.Context, fence RuntimeFence) (io.ReadWriteCloser, error) {
	return f(ctx, fence)
}

// RPCStreamOptions supplies the bounded callbacks used by the router/store
// adapter. The stream package deliberately knows neither store nor transport.
type RPCStreamOptions struct {
	Fence          RuntimeFence
	Queue          RPCCommandQueue
	Wake           RPCStreamWake
	Capacity       int
	MaxRecordBytes int
	NextEventSeq   uint64
}

// RPCStreamEvent is one already-admitted durable event. Applied is invoked
// after the complete canonical line has been returned from Read.
type RPCStreamEvent struct {
	Seq     uint64
	Name    string
	Payload []byte
	Fence   RuntimeFence
	Applied func() error
}

type rpcStreamEvent struct {
	seq    uint64
	line   []byte
	onDone func() error
}

// RPCStream adapts newline-delimited JSON-RPC records to the managed bridge
// command/event ledger. It implements io.ReadWriteCloser.
type RPCStream struct {
	fence          RuntimeFence
	queue          RPCCommandQueue
	wake           RPCStreamWake
	capacity       int
	maxRecordBytes int
	nextSeq        uint64

	mu      sync.Mutex
	write   []byte
	events  []rpcStreamEvent
	pending map[uint64]struct{}
	current *rpcStreamEvent
	offset  int
	closed  bool
	err     error
	notify  chan struct{}
}

func NewRPCStream(options RPCStreamOptions) (*RPCStream, error) {
	if options.Fence.Validate() != nil || options.Queue == nil {
		return nil, ErrRPCStreamInvalid
	}
	capacity := options.Capacity
	if capacity == 0 {
		capacity = DefaultRPCStreamCapacity
	}
	maxRecordBytes := options.MaxRecordBytes
	if maxRecordBytes == 0 {
		maxRecordBytes = MaxRPCStreamRecordBytes
	}
	next := options.NextEventSeq
	if next == 0 {
		next = 1
	}
	if capacity < 1 || capacity > MaxCursorCapacity || maxRecordBytes < 1 || maxRecordBytes > MaxPayloadBytes {
		return nil, ErrRPCStreamInvalid
	}
	return &RPCStream{
		fence: options.Fence, queue: options.Queue, wake: options.Wake,
		capacity: capacity, maxRecordBytes: maxRecordBytes, nextSeq: next,
		pending: make(map[uint64]struct{}), notify: make(chan struct{}, 1),
	}, nil
}

// NewRPCStreamWithCallbacks is a compact constructor for later Manager wiring.
func NewRPCStreamWithCallbacks(fence RuntimeFence, queue RPCCommandQueue, wake RPCStreamWake) (*RPCStream, error) {
	return NewRPCStream(RPCStreamOptions{Fence: fence, Queue: queue, Wake: wake})
}

func (s *RPCStream) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Write buffers partial input and admits exactly one newline-terminated JSON-
// RPC object. A successful completed write means the durable queue callback
// accepted the command.
func (s *RPCStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrRPCStreamClosed
	}
	if bytes.Count(p, []byte{'\n'}) > 1 || bytes.IndexByte(p, '\r') >= 0 {
		s.write = nil
		return 0, ErrRPCStreamInvalid
	}
	if len(s.write)+len(p) > s.maxRecordBytes+1 {
		s.write = nil
		return 0, ErrRPCStreamInvalid
	}
	candidate := append(append([]byte(nil), s.write...), p...)
	newline := bytes.IndexByte(candidate, '\n')
	if newline < 0 {
		s.write = candidate
		return len(p), nil
	}
	if newline != len(candidate)-1 || newline == 0 || newline > s.maxRecordBytes {
		s.write = nil
		return 0, ErrRPCStreamInvalid
	}
	_, command, err := decodeRPCCommand(candidate[:newline], s.fence)
	if err != nil {
		s.write = nil
		return 0, err
	}
	if err := s.queue(command); err != nil {
		s.write = nil
		return 0, err
	}
	s.write = nil
	if s.wake != nil {
		s.wake()
	}
	return len(p), nil
}

// PushEvent admits one event already stored by the durable event applier. It
// is intentionally non-blocking: callers retain the durable row on overflow.
func (s *RPCStream) PushEvent(event RPCStreamEvent) error {
	if event.Name != EventRPC || event.Seq == 0 || event.Fence != s.fence || event.Applied == nil {
		return ErrRPCStreamInvalid
	}
	canonical, err := canonicalJSONObject(event.Payload, s.maxRecordBytes-1)
	if err != nil {
		return err
	}
	line := append(canonical, '\n')
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrRPCStreamClosed
	}
	if event.Seq < s.nextSeq {
		if _, ok := s.pending[event.Seq]; ok {
			s.mu.Unlock()
			return nil
		}
		// The durable applier already completed this sequence.
		s.mu.Unlock()
		return nil
	}
	if event.Seq != s.nextSeq {
		s.mu.Unlock()
		return ErrRPCStreamOutOfOrder
	}
	if len(s.events) >= s.capacity {
		s.mu.Unlock()
		return ErrRPCStreamBackpressure
	}
	s.events = append(s.events, rpcStreamEvent{seq: event.Seq, line: line, onDone: event.Applied})
	s.pending[event.Seq] = struct{}{}
	if s.nextSeq < ^uint64(0) {
		s.nextSeq++
	}
	s.mu.Unlock()
	s.signal()
	return nil
}

// Read returns the exact canonical event line. Applied runs after the final
// byte of a line has been copied, so a store cursor cannot lead the reader.
func (s *RPCStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		s.mu.Lock()
		if s.current == nil && len(s.events) != 0 {
			event := s.events[0]
			s.events = s.events[1:]
			s.current = &event
			s.offset = 0
		}
		if s.current != nil {
			n := copy(p, s.current.line[s.offset:])
			s.offset += n
			if s.offset != len(s.current.line) {
				s.mu.Unlock()
				return n, nil
			}
			done := s.current.onDone
			seq := s.current.seq
			s.current = nil
			s.offset = 0
			// The sequence was advanced at enqueue time. Removing the pending
			// marker here keeps duplicate replay idempotent after delivery.
			// The callback is the durable applied-cursor boundary.
			delete(s.pending, seq)
			s.mu.Unlock()
			if err := done(); err != nil {
				s.mu.Lock()
				if s.err == nil {
					s.err = err
				}
				s.mu.Unlock()
				return n, err
			}
			return n, nil
		}
		if s.err != nil {
			err := s.err
			s.mu.Unlock()
			return 0, err
		}
		if s.closed {
			s.mu.Unlock()
			return 0, io.EOF
		}
		s.mu.Unlock()
		<-s.notify
	}
}

func (s *RPCStream) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.write = nil
	}
	s.mu.Unlock()
	s.signal()
	return nil
}

func decodeRPCCommand(data []byte, fence RuntimeFence) ([]byte, RPCCommand, error) {
	canonical, err := canonicalJSONObject(data, MaxRPCStreamRecordBytes)
	if err != nil {
		return nil, RPCCommand{}, err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &value); err != nil || value == nil {
		return nil, RPCCommand{}, ErrRPCStreamInvalid
	}
	var version, method string
	if err := json.Unmarshal(value["jsonrpc"], &version); err != nil || version != "2.0" {
		return nil, RPCCommand{}, ErrRPCStreamInvalid
	}
	if err := json.Unmarshal(value["method"], &method); err != nil || !validString(method) {
		return nil, RPCCommand{}, ErrRPCStreamInvalid
	}
	id, ok := value["id"]
	if !ok || !validRPCScalarID(id) {
		return nil, RPCCommand{}, ErrRPCStreamInvalid
	}
	hash, err := HashPayload(canonical)
	if err != nil {
		return nil, RPCCommand{}, ErrRPCStreamInvalid
	}
	requestID := deterministicRPCRequestID(fence, id, hash)
	return canonical, RPCCommand{RequestID: requestID, Name: CommandRPC, Payload: canonical, PayloadHash: hash, Fence: fence}, nil
}

func validRPCScalarID(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > MaxStringBytes {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	if trimmed[0] == '"' {
		var value string
		return json.Unmarshal(trimmed, &value) == nil && utf8.ValidString(value) && len([]byte(value)) <= MaxStringBytes
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return false
	}
	return number.String() == string(trimmed)
}

func deterministicRPCRequestID(fence RuntimeFence, id json.RawMessage, payloadHash string) string {
	fenceData, _ := json.Marshal(fence)
	hash := sha256.New()
	for _, value := range [][]byte{fenceData, id, []byte(payloadHash)} {
		_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write(value)
	}
	digest := hash.Sum(nil)
	// Store request IDs are canonical UUIDs. The version/variant bits retain
	// UUID shape while the complete digest remains the identity source.
	digest[6] = (digest[6] & 0x0f) | 0x50
	digest[8] = (digest[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(digest[:16])
	return strings.Join([]string{encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:]}, "-")
}

func canonicalJSONObject(data []byte, limit int) ([]byte, error) {
	if len(data) == 0 || len(data) > limit || rejectDuplicateKeys(data) != nil {
		return nil, ErrRPCStreamInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrRPCStreamInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, ErrRPCStreamInvalid
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, ErrRPCStreamInvalid
	}
	canonical, err := canonicalRPCJSON(object)
	if err != nil || len(canonical) > limit {
		return nil, ErrRPCStreamInvalid
	}
	return canonical, nil
}

func canonicalRPCJSON(value any) ([]byte, error) {
	switch item := value.(type) {
	case nil:
		return []byte("null"), nil
	case bool:
		if item {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return json.Marshal(item)
	case json.Number:
		return []byte(item.String()), nil
	case []any:
		parts := make([][]byte, len(item))
		for i, element := range item {
			part, err := canonicalRPCJSON(element)
			if err != nil {
				return nil, err
			}
			parts[i] = part
		}
		return []byte("[" + joinRPCBytes(parts, ",") + "]"), nil
	case map[string]any:
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([][]byte, 0, len(keys))
		for _, key := range keys {
			encoded, err := json.Marshal(key)
			if err != nil {
				return nil, err
			}
			part, err := canonicalRPCJSON(item[key])
			if err != nil {
				return nil, err
			}
			parts = append(parts, append(append(encoded, ':'), part...))
		}
		return []byte("{" + joinRPCBytes(parts, ",") + "}"), nil
	default:
		return nil, ErrRPCStreamInvalid
	}
}

func joinRPCBytes(values [][]byte, separator string) string {
	var result strings.Builder
	for i, value := range values {
		if i > 0 {
			result.WriteString(separator)
		}
		result.Write(value)
	}
	return result.String()
}
