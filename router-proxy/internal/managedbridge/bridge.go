package managedbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Dialer and Clock are injectable seams for deterministic reconnect tests.
type Dialer func(context.Context, string) (*websocket.Conn, error)
type Sleeper func(context.Context, time.Duration) error
type Random func(int64) int64

type Options struct {
	Dial   Dialer
	Sleep  Sleeper
	Now    func() time.Time
	Random Random
}

type EventInfo struct {
	Seq  uint64
	ID   string
	Name string
	Hash string
}
type Diagnostics struct {
	State               string
	Connected           bool
	Degraded            bool
	CommandCursor       uint64
	EventCursor         uint64
	ReplayFloor         uint64
	PiPID               int
	NeedsReconstruction bool
}

type queuedEvent struct {
	frame Event
	raw   []byte
}
type eventBuffer struct {
	capacity int
	next     uint64
	floor    uint64
	items    []queuedEvent
}

func newEventBuffer(capacity int) *eventBuffer {
	return &eventBuffer{capacity: capacity, next: 1, floor: 1}
}
func (o *eventBuffer) appendEvent(frame Event, raw []byte) bool {
	o.items = append(o.items, queuedEvent{frame: frame, raw: append([]byte(nil), raw...)})
	if frame.Seq < ^uint64(0) {
		o.next = frame.Seq + 1
	} else {
		o.next = 0
	}
	if len(o.items) > o.capacity {
		o.items = o.items[1:]
		o.floor = o.items[0].frame.Seq
		return true
	}
	return false
}
func (o *eventBuffer) prune(seq uint64) {
	index := 0
	for index < len(o.items) && o.items[index].frame.Seq <= seq {
		index++
	}
	if index != 0 {
		o.items = append([]queuedEvent(nil), o.items[index:]...)
	}
	if len(o.items) != 0 {
		o.floor = o.items[0].frame.Seq
	} else if seq < ^uint64(0) && seq+1 > o.floor {
		o.floor = seq + 1
	}
}

// Bridge is a standalone disabled-by-default managed RPC client. It owns one
// Pi process and one current router socket; it has no router integration.
type Bridge struct {
	config   Config
	verifier Verifier
	fence    RuntimeFence
	process  *childProcess
	state    *StateMachine

	mu             sync.Mutex
	current        *bridgeSocket
	generation     uint64
	commandCursor  *CursorEngine
	commandStarted bool
	unknown        map[uint64]cursorRecord
	events         *eventBuffer
	degraded       bool
	connected      bool
	terminal       bool
	terminalErr    error
	reconstruction *NeedsReconstruction
	options        Options
	runStarted     bool
	stopCancel     context.CancelFunc
	notify         chan struct{}
	done           chan struct{}
}

type controlMessage struct {
	frame    any
	complete chan error
}

type bridgeSocket struct {
	conn            *websocket.Conn
	post            PostAuth
	acceptedCommand uint64
	acceptedEvent   uint64
	generation      uint64
	ctx             context.Context
	cancel          context.CancelFunc
	control         chan controlMessage
	done            chan error
	readDone        chan struct{}
	writeDone       chan struct{}
	eventNext       uint64
	lastEventSent   uint64
	mu              sync.Mutex
	pumpsStarted    bool
	meaningful      bool
}

func NewBridge(config Config, verifier Verifier, options Options) (*Bridge, error) {
	if config.validate() != nil || !verifier.initialized {
		return nil, ErrInvalidConfig
	}
	bridgeID, err := newUUID()
	if err != nil {
		return nil, ErrInvalidConfig
	}
	sessionID, err := newUUID()
	if err != nil {
		return nil, ErrInvalidConfig
	}
	config = defaultConfig(config)
	b := &Bridge{
		config:   config,
		verifier: verifier,
		state:    NewStateMachine(),
		commandCursor: func() *CursorEngine {
			engine, _ := NewCursorEngine(config.CommandWindow)
			return engine
		}(),
		unknown: make(map[uint64]cursorRecord),
		events:  newEventBuffer(config.OutboxCapacity),
		notify:  make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	b.fence = RuntimeFence{Agent: config.Agent, BridgeInstance: bridgeID, PiSessionID: sessionID, PiSessionEpoch: 1, RuntimeGeneration: 1, PiPID: 1, PiStartMarker: "pending", PiIncarnation: "00000000-0000-4000-8000-000000000000", ManifestRevision: 1, ManifestHash: manifestHash()}
	b.options = options
	if b.options.Dial == nil {
		b.options.Dial = func(ctx context.Context, url string) (*websocket.Conn, error) {
			conn, _, err := websocket.Dial(ctx, url, nil)
			return conn, err
		}
	}
	if b.options.Sleep == nil {
		b.options.Sleep = defaultSleep
	}
	if b.options.Now == nil {
		b.options.Now = time.Now
	}
	if b.options.Random == nil {
		b.options.Random = func(n int64) int64 {
			if n <= 0 {
				return 0
			}
			return b.options.Now().UnixNano() % n
		}
	}
	return b, nil
}

func defaultSleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// options is kept private so diagnostics cannot expose dial URLs or config.
func (b *Bridge) optionsValue() Options { return b.options }

func (b *Bridge) String() string   { return "managed bridge [redacted]" }
func (b *Bridge) GoString() string { return "managed bridge [redacted]" }

func (b *Bridge) Start(ctx context.Context) error { return b.Run(ctx) }

// Stop requests a graceful stop. Run owns process cleanup and waits for the
// verified process group termination path.
func (b *Bridge) Stop(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidConfig
	}
	b.mu.Lock()
	cancel := b.stopCancel
	runStarted := b.runStarted
	done := b.done
	b.mu.Unlock()
	if !runStarted || cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Bridge) Run(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidConfig
	}
	b.mu.Lock()
	if b.runStarted {
		b.mu.Unlock()
		return ErrBridgeStarted
	}
	b.runStarted = true
	runCtx, cancel := context.WithCancel(ctx)
	b.stopCancel = cancel
	b.mu.Unlock()
	defer close(b.done)
	defer cancel()

	config := b.config
	process, err := startChild(runCtx, config)
	if err != nil {
		b.setTerminal(ErrProcessStart)
		return b.resultError(ctx)
	}
	b.mu.Lock()
	b.process = process
	b.fence.PiPID = int64(process.identity.PID)
	b.fence.PiStartMarker = process.identity.StartMarker
	b.fence.PiIncarnation = process.identity.Incarnation
	b.mu.Unlock()

	stderrDone := make(chan struct{})
	go drainStderr(process.stderr, stderrDone)
	processDone := make(chan error, 1)
	go func() { processDone <- process.wait() }()
	stdoutDone := make(chan error, 1)
	go func() { stdoutDone <- readStdout(process.stdout, config.MaxStdoutLine, b.acceptPiLine) }()
	networkDone := make(chan struct{})
	go func() { b.connectionLoop(runCtx); close(networkDone) }()

	var runErr error
	for {
		select {
		case err := <-stdoutDone:
			stdoutDone = nil
			if err != nil {
				b.setTerminal(err)
				runErr = err
				cancel()
			}
		case <-processDone:
			processDone = nil
			if runErr == nil {
				runErr = ErrProcessExit
			}
			cancel()
		case <-networkDone:
			networkDone = nil
			if runErr == nil {
				runErr = ErrProcessExit
			}
			cancel()
		case <-runCtx.Done():
			if runErr == nil {
				runErr = runCtx.Err()
			}
			cancel()
		}
		if processDone == nil || (runCtx.Err() != nil && networkDone == nil) {
			break
		}
	}
	cancel()
	if networkDone != nil {
		<-networkDone
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), processStopInternalGrace)
	cleanupErr := process.stop(cleanupCtx)
	cleanupCancel()
	joinCtx, joinCancel := context.WithTimeout(context.Background(), time.Second)
	process.joinOutputs(joinCtx, stdoutDoneAsDone(stdoutDone), stderrDone)
	joinCancel()
	if cleanupErr != nil && runErr == nil {
		runErr = cleanupErr
	}
	_ = b.state.Transition(StateStopped)
	b.acceptTerminalEvent()
	b.setTerminal(runErr)
	return b.resultError(ctx)
}

func stdoutDoneAsDone(done <-chan error) <-chan struct{} {
	closed := make(chan struct{})
	if done == nil {
		close(closed)
		return closed
	}
	go func() {
		<-done
		close(closed)
	}()
	return closed
}

func (b *Bridge) resultError(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	return b.terminalErr
}

func (b *Bridge) setTerminal(err error) {
	b.mu.Lock()
	if !b.terminal {
		b.terminal = true
		b.terminalErr = err
	}
	b.mu.Unlock()
}

func (b *Bridge) acceptTerminalEvent() {
	payload := json.RawMessage(`{"status":"stopped"}`)
	id, err := newUUID()
	if err != nil {
		return
	}
	_ = b.addEvent(EventStopped, id, payload)
}

func (b *Bridge) acceptPiLine(line []byte) error {
	if len(line) == 0 {
		return ErrInvalidFrame
	}
	id, err := newUUID()
	if err != nil {
		return ErrInvalidFrame
	}
	if _, err := HashPayload(line); err != nil {
		return ErrInvalidFrame
	}
	return b.addEvent(EventMessage, id, json.RawMessage(append([]byte(nil), line...)))
}

func (b *Bridge) addEvent(name, id string, payload json.RawMessage) error {
	hash, err := HashPayload(payload)
	if err != nil {
		return ErrInvalidFrame
	}
	b.mu.Lock()
	if b.events.next == 0 {
		b.mu.Unlock()
		return ErrInvalidFrame
	}
	seq := b.events.next
	frame := Event{Protocol: ProtocolVersion, Type: string(FrameEvent), Seq: seq, ID: id, Name: name, Payload: append(json.RawMessage(nil), payload...), Hash: hash}
	if b.events.appendEvent(frame, payload) {
		b.degraded = true
	}
	b.mu.Unlock()
	select {
	case b.notify <- struct{}{}:
	default:
	}
	return nil
}

func (b *Bridge) connectionLoop(ctx context.Context) {
	delay := b.config.ReconnectInitial
	for ctx.Err() == nil {
		if b.state.BeginConnect() != nil {
			_ = b.state.MarkStale()
			if b.state.BeginConnect() != nil {
				return
			}
		}
		conn, err := b.options.Dial(ctx, b.config.RouterURL)
		if err != nil {
			if !b.waitReconnect(ctx, &delay) {
				return
			}
			continue
		}
		socket, err := b.handshake(ctx, conn)
		if err != nil {
			_ = conn.Close(websocket.StatusPolicyViolation, "")
			if b.state.State() != StateStopped {
				_ = b.state.MarkStale()
			}
			if !b.waitReconnect(ctx, &delay) {
				return
			}
			continue
		}
		b.installSocket(socket)
		err = b.runSocket(ctx, socket)
		b.removeSocket(socket)
		if ctx.Err() != nil {
			return
		}
		if socket.isMeaningful() {
			delay = b.config.ReconnectInitial
		}
		if err != nil {
			_ = b.state.MarkStale()
		}
		if !b.waitReconnect(ctx, &delay) {
			return
		}
	}
}

func (b *Bridge) waitReconnect(ctx context.Context, delay *time.Duration) bool {
	if b.options.Sleep(ctx, b.jitter(*delay)) != nil {
		return false
	}
	if *delay < b.config.ReconnectMax {
		next := *delay * 2
		if next <= 0 || next > b.config.ReconnectMax {
			next = b.config.ReconnectMax
		}
		*delay = next
	}
	return true
}

func (b *Bridge) jitter(delay time.Duration) time.Duration {
	if delay <= 1 {
		return delay
	}
	n := b.options.Random(int64(delay))
	if n < 0 {
		n = -n
	}
	return time.Duration(n % int64(delay))
}

func readText(conn *websocket.Conn, ctx context.Context) ([]byte, error) {
	messageType, data, err := conn.Read(ctx)
	if err != nil || messageType != websocket.MessageText {
		return nil, ErrInvalidFrame
	}
	return data, nil
}

func (b *Bridge) handshake(ctx context.Context, conn *websocket.Conn) (*bridgeSocket, error) {
	conn.SetReadLimit(MaxFrameBytes)
	data, err := readText(conn, ctx)
	if err != nil {
		return nil, ErrInvalidFrame
	}
	frame, err := DecodeFrame(data)
	if err != nil {
		return nil, ErrInvalidFrame
	}
	challenge, ok := frame.(Challenge)
	if !ok {
		return nil, ErrInvalidFrame
	}
	if b.state.AcceptChallenge(challenge) != nil {
		return nil, ErrInvalidFrame
	}
	b.mu.Lock()
	fence := b.fence
	b.mu.Unlock()
	auth, err := AuthenticateFor(b.verifier, challenge, fence)
	if err != nil {
		return nil, ErrInvalidFrame
	}
	encoded, err := EncodeFrame(auth)
	if err != nil {
		return nil, ErrInvalidFrame
	}
	if err := conn.Write(ctx, websocket.MessageText, encoded); err != nil {
		return nil, ErrInvalidFrame
	}
	data, err = readText(conn, ctx)
	if err != nil {
		return nil, ErrInvalidFrame
	}
	acceptedFrame, err := DecodeFrame(data)
	if err != nil {
		return nil, ErrInvalidFrame
	}
	accepted, ok := acceptedFrame.(Authenticated)
	if !ok || VerifyAuthenticated(b.verifier, challenge, auth, accepted) != nil {
		return nil, ErrInvalidFrame
	}
	b.mu.Lock()
	commandHead := b.commandHeadLocked()
	eventHead := b.events.next - 1
	validCursors := accepted.CommandCursor <= commandHead && accepted.EventCursor <= eventHead
	if validCursors && !b.commandStarted {
		if accepted.CommandCursor == ^uint64(0) {
			validCursors = false
		} else {
			cursor := &b.commandCursor.commands
			cursor.mu.Lock()
			cursor.next = accepted.CommandCursor + 1
			cursor.floor = cursor.next
			cursor.mu.Unlock()
			b.commandStarted = true
		}
	}
	b.mu.Unlock()
	if !validCursors {
		return nil, ErrInvalidFrame
	}
	if b.state.Authenticate(b.verifier, auth) != nil {
		return nil, ErrInvalidFrame
	}
	socketCtx, cancel := context.WithCancel(ctx)
	config := b.config
	socket := &bridgeSocket{
		conn:            conn,
		post:            authPost(auth),
		acceptedCommand: accepted.CommandCursor,
		acceptedEvent:   accepted.EventCursor,
		ctx:             socketCtx,
		cancel:          cancel,
		control:         make(chan controlMessage, config.OutboxCapacity),
		done:            make(chan error, 1),
		readDone:        make(chan struct{}),
		writeDone:       make(chan struct{}),
		eventNext:       nextSequence(accepted.EventCursor),
	}
	if b.state.BeginResume(Resume{PostAuth: socket.post, Protocol: ProtocolVersion, Type: string(FrameResume), CommandCursor: accepted.CommandCursor, EventCursor: accepted.EventCursor}) != nil {
		cancel()
		return nil, ErrInvalidFrame
	}
	resume := Resume{PostAuth: socket.post, Protocol: ProtocolVersion, Type: string(FrameResume), CommandCursor: accepted.CommandCursor, EventCursor: accepted.EventCursor}
	encoded, err = EncodeFrame(resume)
	if err != nil || conn.Write(ctx, websocket.MessageText, encoded) != nil {
		cancel()
		return nil, ErrInvalidFrame
	}
	if b.state.Activate() != nil {
		cancel()
		return nil, ErrInvalidFrame
	}
	return socket, nil
}

func nextSequence(seq uint64) uint64 {
	if seq == ^uint64(0) {
		return 0
	}
	return seq + 1
}

func authPost(auth Authenticate) PostAuth {
	return PostAuth{Agent: auth.Agent, BridgeInstance: auth.BridgeInstance, RouterInstanceNonce: auth.RouterInstanceNonce, ConnectionNonce: auth.ConnectionNonce, Fence: auth.Fence}
}

// installSocket is a lease handoff. The old pumps are stopped and joined
// before the new socket becomes current, so a stale pump cannot outlive auth.
func (b *Bridge) installSocket(socket *bridgeSocket) {
	b.mu.Lock()
	old := b.current
	if old != nil {
		b.current = nil
		b.connected = false
	}
	b.mu.Unlock()
	if old != nil {
		b.stopSocket(old)
	}
	b.mu.Lock()
	b.generation++
	socket.generation = b.generation
	b.current = socket
	b.connected = true
	b.mu.Unlock()
}

func (b *Bridge) removeSocket(socket *bridgeSocket) {
	b.mu.Lock()
	if b.current == socket {
		b.current = nil
		b.connected = false
	}
	b.mu.Unlock()
	b.stopSocket(socket)
}

func (b *Bridge) stopSocket(socket *bridgeSocket) {
	socket.cancel()
	_ = socket.conn.Close(websocket.StatusNormalClosure, "")
	socket.mu.Lock()
	started := socket.pumpsStarted
	socket.mu.Unlock()
	if started {
		<-socket.readDone
		<-socket.writeDone
	}
}

func (b *Bridge) runSocket(ctx context.Context, socket *bridgeSocket) error {
	socket.mu.Lock()
	socket.pumpsStarted = true
	socket.mu.Unlock()
	go b.writePump(socket)
	go b.readPump(socket)
	var runErr error
	select {
	case <-ctx.Done():
		runErr = ctx.Err()
	case runErr = <-socket.done:
	}
	b.stopSocket(socket)
	if runErr == nil {
		runErr = ErrInvalidFrame
	}
	return runErr
}

func (s *bridgeSocket) report(err error) {
	select {
	case s.done <- err:
	default:
	}
}

func (s *bridgeSocket) isMeaningful() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meaningful
}

func (s *bridgeSocket) markMeaningful() {
	s.mu.Lock()
	s.meaningful = true
	s.mu.Unlock()
}

func (b *Bridge) writePump(socket *bridgeSocket) {
	defer close(socket.writeDone)
	for {
		if message, ok := tryControl(socket.control); ok {
			err := b.writeFrame(socket, message.frame)
			if message.complete != nil {
				message.complete <- err
			}
			if err != nil {
				socket.report(err)
				return
			}
			continue
		}
		if frame, seq, gapFloor, ok := b.nextEvent(socket); ok {
			if err := b.writeFrame(socket, frame); err != nil {
				socket.report(err)
				return
			}
			if gapFloor != 0 {
				socket.mu.Lock()
				if socket.eventNext < gapFloor {
					socket.eventNext = gapFloor
				}
				socket.mu.Unlock()
			} else {
				socket.mu.Lock()
				if socket.eventNext == seq {
					socket.eventNext = nextSequence(seq)
				}
				if seq > socket.lastEventSent {
					socket.lastEventSent = seq
					socket.meaningful = true
				}
				socket.mu.Unlock()
			}
			continue
		}
		select {
		case <-socket.ctx.Done():
			return
		case message := <-socket.control:
			err := b.writeFrame(socket, message.frame)
			if message.complete != nil {
				message.complete <- err
			}
			if err != nil {
				socket.report(err)
				return
			}
		case <-b.notify:
		}
	}
}

func tryControl(control <-chan controlMessage) (controlMessage, bool) {
	select {
	case message := <-control:
		return message, true
	default:
		return controlMessage{}, false
	}
}

func (b *Bridge) writeFrame(socket *bridgeSocket, frame any) error {
	data, err := EncodeFrame(frame)
	if err != nil {
		return ErrInvalidFrame
	}
	if err := socket.conn.Write(socket.ctx, websocket.MessageText, data); err != nil {
		return ErrInvalidFrame
	}
	if event, ok := frame.(Event); ok {
		socket.mu.Lock()
		if event.Seq > socket.lastEventSent {
			socket.lastEventSent = event.Seq
		}
		socket.meaningful = true
		socket.mu.Unlock()
	}
	return nil
}

func (b *Bridge) readPump(socket *bridgeSocket) {
	defer close(socket.readDone)
	for {
		messageType, data, err := socket.conn.Read(socket.ctx)
		if err != nil || messageType != websocket.MessageText {
			socket.report(ErrInvalidFrame)
			return
		}
		frame, err := DecodeFrame(data)
		if err != nil {
			socket.report(ErrInvalidFrame)
			return
		}
		if !b.isCurrent(socket) {
			socket.report(ErrStaleSocket)
			return
		}
		if err := b.handleFrame(socket, frame); err != nil {
			socket.report(err)
			return
		}
		socket.markMeaningful()
	}
}

// NeedsReconstruction is returned when the durable peer has discarded the
// cursor history required to resume. The bridge does not advance any cursor
// or invent a snapshot in this state.
type NeedsReconstruction struct {
	Domain   string
	Expected uint64
	Floor    uint64
}

func (*NeedsReconstruction) Error() string { return "managed bridge needs reconstruction" }

var (
	ErrStaleSocket      = errors.New("managed bridge socket is stale")
	ErrAmbiguousCommand = errors.New("managed bridge command admission is ambiguous")
	ErrCommandUnknown   = errors.New("managed bridge command result is unknown")
	ErrPartialWrite     = errors.New("managed bridge command write was partial")
)

func (b *Bridge) isCurrent(socket *bridgeSocket) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.isCurrentLocked(socket)
}

func (b *Bridge) isCurrentLocked(socket *bridgeSocket) bool {
	return b.current == socket && b.connected && socket.generation == b.generation
}

func (b *Bridge) queue(socket *bridgeSocket, frame any) error {
	return b.queueControl(socket, controlMessage{frame: frame})
}

func (b *Bridge) queueControl(socket *bridgeSocket, message controlMessage) error {
	select {
	case socket.control <- message:
		return nil
	case <-socket.ctx.Done():
		return ErrStaleSocket
	default:
		return ErrInvalidFrame
	}
}

func (b *Bridge) queueAndWait(socket *bridgeSocket, frame any) error {
	complete := make(chan error, 1)
	if err := b.queueControl(socket, controlMessage{frame: frame, complete: complete}); err != nil {
		return err
	}
	select {
	case err := <-complete:
		return err
	case <-socket.ctx.Done():
		return ErrStaleSocket
	}
}

func (b *Bridge) handleFrame(socket *bridgeSocket, frame any) error {
	switch value := frame.(type) {
	case Command:
		if b.state.CheckFrame(value) != nil || !samePost(value.PostAuth, socket.post) {
			return ErrStaleFrame
		}
		err := b.admitCommand(socket, value)
		if errors.Is(err, ErrCursorGap) || errors.Is(err, ErrReplayGap) {
			_ = b.sendCommandReplayGap(socket)
		}
		return err
	case Ack:
		if b.state.CheckFrame(value) != nil || !samePost(value.PostAuth, socket.post) {
			return ErrStaleFrame
		}
		return b.handleAck(socket, value)
	case ReplayGap:
		if b.state.CheckFrame(value) != nil || !samePost(value.PostAuth, socket.post) || value.Expected == 0 || value.Floor == 0 {
			return ErrStaleFrame
		}
		gap := &NeedsReconstruction{Domain: value.Domain, Expected: value.Expected, Floor: value.Floor}
		b.mu.Lock()
		b.reconstruction = gap
		b.degraded = true
		b.mu.Unlock()
		_ = b.state.Transition(StateNeedsReconstruction)
		return gap
	default:
		return ErrInvalidFrame
	}
}

func samePost(a, b PostAuth) bool {
	return a.Agent == b.Agent && a.BridgeInstance == b.BridgeInstance && a.RouterInstanceNonce == b.RouterInstanceNonce && a.ConnectionNonce == b.ConnectionNonce && a.Fence == b.Fence
}

func (b *Bridge) sendCommandReplayGap(socket *bridgeSocket) error {
	expected, err := b.commandCursor.Next(CursorCommands)
	if err != nil {
		return err
	}
	floor, err := b.commandCursor.ReplayFloor(CursorCommands)
	if err != nil {
		return err
	}
	return b.queueAndWait(socket, ReplayGap{PostAuth: socket.post, Protocol: ProtocolVersion, Type: string(FrameReplayGap), Domain: DomainCommands, Expected: expected, Floor: floor})
}

func (b *Bridge) handleAck(socket *bridgeSocket, ack Ack) error {
	if ack.Seq == 0 || (ack.Domain != DomainEvents && ack.Domain != DomainCommands) {
		return ErrInvalidFrame
	}
	socket.mu.Lock()
	highestSent := socket.lastEventSent
	socket.mu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.isCurrentLocked(socket) {
		return ErrStaleSocket
	}
	switch ack.Domain {
	case DomainEvents:
		if ack.Seq > highestSent {
			return ErrCursorGap
		}
		b.events.prune(ack.Seq)
	case DomainCommands:
		if ack.Seq > b.commandHeadLocked() {
			return ErrCursorGap
		}
	}
	return nil
}

func (b *Bridge) admitCommand(socket *bridgeSocket, command Command) error {
	canonical, err := compactCommandPayload(command.Payload)
	if err != nil {
		return ErrInvalidFrame
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// The lease covers the final fence check and the complete stdin side
	// effect. Socket replacement waits for this critical section.
	if !b.isCurrentLocked(socket) || b.process == nil {
		return ErrStaleSocket
	}
	identity := cursorRecord{id: command.ID, hash: command.Hash}
	if record, ok := b.unknown[command.Seq]; ok {
		if record != identity {
			return ErrCursorConflict
		}
		return ErrCommandUnknown
	}
	partial := false
	result, err := b.commandCursor.Admit(CursorCommands, command.Seq, command.ID, command.Hash, func() error {
		writeErr := writeComplete(b.process.stdin, canonical)
		if errors.Is(writeErr, ErrPartialWrite) {
			partial = true
		}
		return writeErr
	})
	if partial {
		b.unknown[command.Seq] = identity
		return ErrAmbiguousCommand
	}
	if err != nil {
		return err
	}
	if !result.Accepted {
		return ErrDurableAdmission
	}
	return b.queue(socket, Ack{PostAuth: socket.post, Protocol: ProtocolVersion, Type: string(FrameAck), Domain: DomainCommands, Seq: command.Seq})
}

func compactCommandPayload(payload []byte) ([]byte, error) {
	if !validPayload(payload) {
		return nil, ErrInvalidFrame
	}
	var compact bytes.Buffer
	compact.Grow(len(payload))
	if err := json.Compact(&compact, payload); err != nil || compact.Len() > MaxPayloadBytes {
		return nil, ErrInvalidFrame
	}
	return compact.Bytes(), nil
}

func writeComplete(writer io.Writer, payload []byte) error {
	canonical, err := compactCommandPayload(payload)
	if err != nil {
		return err
	}
	data := make([]byte, 0, len(canonical)+1)
	data = append(data, canonical...)
	data = append(data, '\n')
	written := 0
	for len(data) > 0 {
		n, writeErr := writer.Write(data)
		if n < 0 || n > len(data) {
			return ErrInvalidFrame
		}
		if n > 0 {
			written += n
			data = data[n:]
		}
		if writeErr != nil {
			if written > 0 {
				return ErrPartialWrite
			}
			return writeErr
		}
		if n == 0 {
			if written > 0 {
				return ErrPartialWrite
			}
			return io.ErrShortWrite
		}
	}
	return nil
}

// nextEvent reserves no state. The single writer advances eventNext only after
// its websocket Write succeeds, so ACK validation reflects sent frames only.
func (b *Bridge) nextEvent(socket *bridgeSocket) (any, uint64, uint64, bool) {
	socket.mu.Lock()
	next := socket.eventNext
	socket.mu.Unlock()
	if next == 0 {
		return nil, 0, 0, false
	}
	b.mu.Lock()
	floor := b.events.floor
	head := b.events.next - 1
	if next < floor {
		gap := ReplayGap{PostAuth: socket.post, Protocol: ProtocolVersion, Type: string(FrameReplayGap), Domain: DomainEvents, Expected: next, Floor: floor}
		b.degraded = true
		b.mu.Unlock()
		return gap, 0, floor, true
	}
	if next == 0 || next > head || len(b.events.items) == 0 {
		b.mu.Unlock()
		return nil, 0, 0, false
	}
	index := next - floor
	if index >= uint64(len(b.events.items)) {
		b.mu.Unlock()
		return nil, 0, 0, false
	}
	item := b.events.items[index]
	frame := item.frame
	frame.PostAuth = socket.post
	b.mu.Unlock()
	return frame, item.frame.Seq, 0, true
}

func (b *Bridge) commandHeadLocked() uint64 {
	next, err := b.commandCursor.Next(CursorCommands)
	if err != nil || next == 0 {
		return 0
	}
	return next - 1
}

func (b *Bridge) Diagnostics() Diagnostics {
	b.mu.Lock()
	defer b.mu.Unlock()
	cursor := b.commandHeadLocked()
	event := uint64(0)
	if b.events.next > 0 {
		event = b.events.next - 1
	}
	return Diagnostics{State: string(b.state.State()), Connected: b.connected, Degraded: b.degraded, CommandCursor: cursor, EventCursor: event, ReplayFloor: b.events.floor, PiPID: int(b.fence.PiPID), NeedsReconstruction: b.reconstruction != nil}
}
func (b *Bridge) Events() []EventInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]EventInfo, 0, len(b.events.items))
	for _, item := range b.events.items {
		result = append(result, EventInfo{Seq: item.frame.Seq, ID: item.frame.ID, Name: item.frame.Name, Hash: item.frame.Hash})
	}
	return result
}
func (b *Bridge) Fence() RuntimeFence { b.mu.Lock(); defer b.mu.Unlock(); return b.fence }

func (b *Bridge) Reconstruction() (NeedsReconstruction, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reconstruction == nil {
		return NeedsReconstruction{}, false
	}
	return *b.reconstruction, true
}
