package managedbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sync"
)

const (
	DefaultCursorCapacity = 128
	MaxCursorCapacity     = 4096
)

var (
	ErrCursorGap        = errors.New("managed RPC cursor gap")
	ErrCursorConflict   = errors.New("managed RPC cursor conflict")
	ErrReplayGap        = errors.New("managed RPC replay gap")
	ErrDurableAdmission = errors.New("managed RPC durable admission failed")
	ErrCursorInvalid    = errors.New("managed RPC cursor input is invalid")
)

type CursorDomain string

const (
	CursorCommands CursorDomain = DomainCommands
	CursorEvents   CursorDomain = DomainEvents
)

type cursorRecord struct{ id, hash string }

type cursor struct {
	mu       sync.Mutex
	capacity uint64
	next     uint64
	floor    uint64
	records  map[uint64]cursorRecord
}

// CursorResult describes admission without exposing payloads or internal
// records. AckSeq advances only after durable admission returns nil.
type CursorResult struct {
	Accepted  bool
	Duplicate bool
	AckSeq    uint64
	Expected  uint64
	Floor     uint64
}

// CursorEngine owns independent bounded command and event sequence domains.
type CursorEngine struct {
	commands cursor
	events   cursor
}

func NewCursorEngine(capacity int) (*CursorEngine, error) {
	if capacity <= 0 || capacity > MaxCursorCapacity {
		return nil, ErrCursorInvalid
	}
	return &CursorEngine{commands: newCursor(uint64(capacity)), events: newCursor(uint64(capacity))}, nil
}
func NewDefaultCursorEngine() *CursorEngine {
	engine, _ := NewCursorEngine(DefaultCursorCapacity)
	return engine
}
func newCursor(capacity uint64) cursor {
	return cursor{capacity: capacity, next: 1, floor: 1, records: make(map[uint64]cursorRecord, capacity)}
}

// Admit runs durableAdmission while the selected domain is serialized. A
// failed callback leaves next, floor, and retained records unchanged. Callers
// should keep the callback bounded and must not re-enter this engine.
func (e *CursorEngine) Admit(domain CursorDomain, seq uint64, id, hash string, durableAdmission func() error) (CursorResult, error) {
	if e == nil || (domain != CursorCommands && domain != CursorEvents) || seq == 0 || !validUUID(id) || !hex64RE.MatchString(hash) || durableAdmission == nil {
		return CursorResult{}, ErrCursorInvalid
	}
	selected := &e.commands
	if domain == CursorEvents {
		selected = &e.events
	}
	return selected.admit(seq, id, hash, durableAdmission)
}

func (c *cursor) admit(seq uint64, id, hash string, durableAdmission func() error) (CursorResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq < c.next {
		if seq < c.floor {
			return CursorResult{Expected: c.next, Floor: c.floor}, ErrReplayGap
		}
		record, ok := c.records[seq]
		if !ok {
			return CursorResult{Expected: c.next, Floor: c.floor}, ErrReplayGap
		}
		if record.id != id || record.hash != hash {
			return CursorResult{Expected: c.next, Floor: c.floor}, ErrCursorConflict
		}
		return CursorResult{Accepted: true, Duplicate: true, AckSeq: seq, Expected: c.next, Floor: c.floor}, nil
	}
	if seq > c.next {
		return CursorResult{Expected: c.next, Floor: c.floor}, ErrCursorGap
	}
	// There is no representable successor after the maximum uint64 sequence.
	// Reject it before invoking the durable callback so the cursor remains
	// usable and the callback cannot create an unacknowledgeable record.
	if c.next == math.MaxUint64 {
		return CursorResult{Expected: c.next, Floor: c.floor}, ErrCursorInvalid
	}
	if err := callDurableAdmission(durableAdmission); err != nil {
		return CursorResult{Expected: c.next, Floor: c.floor}, ErrDurableAdmission
	}
	c.records[seq] = cursorRecord{id: id, hash: hash}
	c.next++
	for uint64(len(c.records)) > c.capacity {
		delete(c.records, c.floor)
		c.floor++
	}
	return CursorResult{Accepted: true, AckSeq: seq, Expected: c.next, Floor: c.floor}, nil
}

// callDurableAdmission contains callback panics without retaining or exposing
// the recovered value. A panic is the same failed admission as an error.
func callDurableAdmission(callback func() error) (err error) {
	completed := false
	defer func() {
		if recover() != nil || !completed {
			err = ErrDurableAdmission
		}
	}()
	err = callback()
	completed = true
	return err
}

// Next returns the next sequence to admit in a domain.
func (e *CursorEngine) Next(domain CursorDomain) (uint64, error) {
	selected, err := e.selectCursor(domain)
	if err != nil {
		return 0, err
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	return selected.next, nil
}

// ReplayFloor returns the oldest sequence whose ID/hash duplicate can still be
// checked. Sequences below it require replay from a durable source.
func (e *CursorEngine) ReplayFloor(domain CursorDomain) (uint64, error) {
	selected, err := e.selectCursor(domain)
	if err != nil {
		return 0, err
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	return selected.floor, nil
}

// PruneThrough explicitly forgets retained duplicate identities up to seq.
// The next sequence is never changed by pruning.
func (e *CursorEngine) PruneThrough(domain CursorDomain, seq uint64) error {
	selected, err := e.selectCursor(domain)
	if err != nil || seq == 0 {
		return ErrCursorInvalid
	}
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if seq >= selected.next {
		seq = selected.next - 1
	}
	for selected.floor <= seq {
		delete(selected.records, selected.floor)
		selected.floor++
	}
	return nil
}

func (e *CursorEngine) selectCursor(domain CursorDomain) (*cursor, error) {
	if e == nil {
		return nil, ErrCursorInvalid
	}
	switch domain {
	case CursorCommands:
		return &e.commands, nil
	case CursorEvents:
		return &e.events, nil
	}
	return nil, ErrCursorInvalid
}

// HashPayload returns the fixed hash used in cursor identity checks. JSON
// whitespace is canonicalized because encoding/json canonicalizes RawMessage
// values while encoding a frame. It validates the payload bound before
// canonicalization so callers cannot create an unbounded admission record.
func HashPayload(payload []byte) (string, error) {
	if !validPayload(payload) {
		return "", ErrCursorInvalid
	}
	var canonical bytes.Buffer
	canonical.Grow(len(payload))
	if err := json.Compact(&canonical, payload); err != nil {
		return "", ErrCursorInvalid
	}
	sum := sha256.Sum256(canonical.Bytes())
	return hex.EncodeToString(sum[:]), nil
}
