package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managedbridge"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	managedBridgeHandshakeTimeout = 5 * time.Second
	managedBridgeWriteTimeout     = 5 * time.Second
	managedBridgeHeartbeat        = 15 * time.Second
	managedBridgeMaxDrain         = 64
)

// ManagedBridgeHandler is an unregistered HTTP handler for the Phase 2B
// router-side WebSocket. The owner chooses where to mount it.
type ManagedBridgeHandler struct {
	store               *store.Store
	routerNonce         string
	registry            *managedBridgeRegistry
	mu                  sync.Mutex
	closed              bool
	sessions            map[*managedBridgeSession]struct{}
	heartbeat           time.Duration
	maxDrain            int
	afterEventAdmission func(*managedBridgeSession)
	afterActivate       func(*managedBridgeSession)
}

// ManagedBridgeHandlerOptions contains deterministic bounds for focused
// transport tests. It does not alter routing or process configuration.
type ManagedBridgeHandlerOptions struct {
	Heartbeat time.Duration
	MaxDrain  int

	// afterEventAdmission is a deterministic test seam for the exact window
	// between durable admission and the event ACK write.
	afterEventAdmission func(*managedBridgeSession)
	afterActivate       func(*managedBridgeSession)
}

// managedBridgeRegistry is process-local connection ownership. It is shared
// by handler instances so an atomic handler swap fences the old socket too.
type managedBridgeRegistry struct {
	mu      sync.Mutex
	current map[*store.Store]map[string]*managedBridgeSession
}

var phase2BRegistry = &managedBridgeRegistry{current: make(map[*store.Store]map[string]*managedBridgeSession)}

// NewManagedBridgeHandler creates a handler without registering a production
// route. The returned handler has no goroutines until a socket is accepted.
func NewManagedBridgeHandler(durable *store.Store, options ...ManagedBridgeHandlerOptions) *ManagedBridgeHandler {
	challenge, err := managedbridge.NewChallenge()
	if err != nil {
		// NewChallenge only fails when the system random source fails. A handler
		// with no valid nonce rejects every connection safely.
		challenge.RouterInstanceNonce = strings.Repeat("0", 64)
	}
	handler := &ManagedBridgeHandler{store: durable, routerNonce: challenge.RouterInstanceNonce, registry: phase2BRegistry, sessions: make(map[*managedBridgeSession]struct{}), heartbeat: managedBridgeHeartbeat, maxDrain: managedBridgeMaxDrain}
	if len(options) != 0 {
		handler.afterEventAdmission = options[0].afterEventAdmission
		handler.afterActivate = options[0].afterActivate
	}
	if len(options) != 0 {
		if options[0].Heartbeat > 0 {
			handler.heartbeat = options[0].Heartbeat
		}
		if options[0].MaxDrain > 0 {
			handler.maxDrain = options[0].MaxDrain
		}
	}
	return handler
}

// NewManagedBridgeServer is a descriptive alias for callers that mount the
// handler behind their own HTTP path.
func NewManagedBridgeServer(durable *store.Store, options ...ManagedBridgeHandlerOptions) *ManagedBridgeHandler {
	return NewManagedBridgeHandler(durable, options...)
}

// ServeHTTP implements the unregistered Phase 2B WebSocket endpoint.
func (h *ManagedBridgeHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if h == nil || h.store == nil || !managedBridgeRequestAllowed(req) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	conn.SetReadLimit(managedbridge.MaxFrameBytes)
	if err := h.handshake(req.Context(), conn); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "managed bridge handshake rejected")
		return
	}
	// handshake creates the session and runs it after the ownership swap.
}

func managedBridgeRequestAllowed(req *http.Request) bool {
	if req == nil || req.Method != http.MethodGet || req.URL == nil || req.URL.RawQuery != "" || req.URL.Fragment != "" || !websocketUpgrade(req) {
		return false
	}
	// Presence of Origin is rejected, including an empty value.
	if _, present := req.Header["Origin"]; present {
		return false
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil || strings.Contains(host, "%") {
		return false
	}
	// Compare the parsed host's exact spelling. IPv4-mapped IPv6 addresses
	// are a different transport identity for this endpoint.
	return host == "127.0.0.1" || host == "::1"
}

func (h *ManagedBridgeHandler) handshake(parent context.Context, conn *websocket.Conn) error {
	ctx, cancel := context.WithTimeout(parent, managedBridgeHandshakeTimeout)
	defer cancel()
	challenge, err := managedbridge.NewChallenge()
	if err != nil || len(h.routerNonce) != 64 {
		return managedbridge.ErrInvalidFrame
	}
	challenge.RouterInstanceNonce = h.routerNonce
	if err := h.writeHandshake(ctx, conn, challenge); err != nil {
		return err
	}
	frame, err := readManagedBridgeText(ctx, conn)
	if err != nil {
		return err
	}
	decoded, err := managedbridge.DecodeFrame(frame)
	if err != nil {
		return managedbridge.ErrInvalidFrame
	}
	auth, ok := decoded.(managedbridge.Authenticate)
	if !ok {
		return managedbridge.ErrInvalidFrame
	}
	accepted, err := h.store.AuthenticateAndProve(ctx, challenge, auth)
	if err != nil {
		return managedbridge.ErrInvalidFrame
	}
	if err := h.writeHandshake(ctx, conn, accepted.Accepted); err != nil {
		return err
	}
	frame, err = readManagedBridgeText(ctx, conn)
	if err != nil {
		return err
	}
	decoded, err = managedbridge.DecodeFrame(frame)
	if err != nil {
		return managedbridge.ErrInvalidFrame
	}
	resume, ok := decoded.(managedbridge.Resume)
	if !ok || resume.PostAuth != authPost(auth) {
		return managedbridge.ErrInvalidFrame
	}
	if err := h.reconcileResume(ctx, conn, resume, accepted.Cursors); err != nil {
		return err
	}
	session := &managedBridgeSession{handler: h, conn: conn, post: authPost(auth), commandCursor: resume.CommandCursor, eventCursor: resume.EventCursor, ctx: nil, cancel: nil, wake: make(chan struct{}, 1), control: make(chan managedBridgeWrite, h.maxDrain), done: make(chan struct{}), readDone: make(chan struct{}), writeDone: make(chan struct{}), sentCommands: make(map[uint64]struct{})}
	session.ctx, session.cancel = context.WithCancel(context.Background())
	old := h.activate(session)
	if old != nil {
		old.stop()
		old.wait()
	}
	h.mu.Lock()
	h.sessions[session] = struct{}{}
	h.mu.Unlock()
	if hook := h.afterActivate; hook != nil {
		hook(session)
	}
	session.run()
	h.mu.Lock()
	delete(h.sessions, session)
	h.mu.Unlock()
	h.deactivate(session)
	return nil
}

func authPost(auth managedbridge.Authenticate) managedbridge.PostAuth {
	return managedbridge.PostAuth{Agent: auth.Agent, BridgeInstance: auth.BridgeInstance, RouterInstanceNonce: auth.RouterInstanceNonce, ConnectionNonce: auth.ConnectionNonce, Fence: auth.Fence}
}

func (h *ManagedBridgeHandler) writeHandshake(ctx context.Context, conn *websocket.Conn, frame any) error {
	data, err := managedbridge.EncodeFrame(frame)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func readManagedBridgeText(ctx context.Context, conn *websocket.Conn) ([]byte, error) {
	kind, data, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageText || !utf8.Valid(data) {
		return nil, managedbridge.ErrInvalidFrame
	}
	return data, nil
}

func (h *ManagedBridgeHandler) reconcileResume(ctx context.Context, conn *websocket.Conn, resume managedbridge.Resume, cursors store.ManagedBridgeCursors) error {
	commandHead := cursorBefore(cursors.CommandNext)
	eventHead := cursorBefore(cursors.EventAppliedNext)
	if resume.CommandCursor > commandHead || resume.EventCursor > eventHead {
		return managedbridge.ErrInvalidFrame
	}
	if beforeCursor(resume.CommandCursor) < cursors.CommandFloor {
		return h.sendHandshakeGap(ctx, conn, resume.PostAuth, managedbridge.DomainCommands, resume.CommandCursor+1, cursors.CommandFloor)
	}
	if beforeCursor(resume.EventCursor) < cursors.EventAdmittedFloor {
		return h.sendHandshakeGap(ctx, conn, resume.PostAuth, managedbridge.DomainEvents, resume.EventCursor+1, cursors.EventAdmittedFloor)
	}
	return nil
}

func beforeCursor(cursor uint64) uint64 {
	if cursor == 0 {
		return 1
	}
	if cursor == ^uint64(0) {
		return 0
	}
	return cursor + 1
}

func cursorBefore(next uint64) uint64 {
	if next <= 1 {
		return 0
	}
	return next - 1
}

func (h *ManagedBridgeHandler) sendHandshakeGap(ctx context.Context, conn *websocket.Conn, post managedbridge.PostAuth, domain string, expected, floor uint64) error {
	if expected == 0 || floor == 0 || expected < floor {
		return managedbridge.ErrInvalidFrame
	}
	gap := managedbridge.ReplayGap{PostAuth: post, Protocol: managedbridge.ProtocolVersion, Type: string(managedbridge.FrameReplayGap), Domain: domain, Expected: expected, Floor: floor}
	if err := h.writeHandshake(ctx, conn, gap); err != nil {
		return err
	}
	return store.ErrManagedBridgeReplayGap
}

func (h *ManagedBridgeHandler) activate(session *managedBridgeSession) *managedBridgeSession {
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	byBridge := h.registry.current[h.store]
	if byBridge == nil {
		byBridge = make(map[string]*managedBridgeSession)
		h.registry.current[h.store] = byBridge
	}
	old := byBridge[session.post.BridgeInstance]
	byBridge[session.post.BridgeInstance] = session
	if old != nil {
		old.cancel()
		_ = old.conn.Close(websocket.StatusGoingAway, "managed bridge replaced")
	}
	return old
}

func (h *ManagedBridgeHandler) deactivate(session *managedBridgeSession) {
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	byBridge := h.registry.current[h.store]
	if byBridge != nil && byBridge[session.post.BridgeInstance] == session {
		delete(byBridge, session.post.BridgeInstance)
		if len(byBridge) == 0 {
			delete(h.registry.current, h.store)
		}
	}
}

func (h *ManagedBridgeHandler) current(session *managedBridgeSession) bool {
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	return h.currentLocked(session)
}

func (h *ManagedBridgeHandler) currentLocked(session *managedBridgeSession) bool {
	return h.registry.current[h.store][session.post.BridgeInstance] == session && !session.stopped()
}

func (h *ManagedBridgeHandler) wake(bridgeID string) {
	h.registry.mu.Lock()
	session := h.registry.current[h.store][bridgeID]
	h.registry.mu.Unlock()
	if session != nil {
		select {
		case session.wake <- struct{}{}:
		default:
		}
	}
}

// Wake notifies the authenticated bridge after a manager has durably queued a
// command. One argument may be a bridge ID or agent ID; two arguments are
// accepted as agent ID and bridge ID for manager call-site compatibility.
func (h *ManagedBridgeHandler) Wake(ids ...string) {
	bridgeID := ""
	if len(ids) > 1 {
		bridgeID = ids[1]
	} else if len(ids) == 1 {
		bridgeID = ids[0]
	}
	if bridgeID != "" {
		h.wake(bridgeID)
		return
	}
}

// Close stops and joins every session owned by this handler. It leaves the
// bridge process and store rows untouched.
func (h *ManagedBridgeHandler) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	sessions := make([]*managedBridgeSession, 0, len(h.sessions))
	for session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.mu.Unlock()
	for _, session := range sessions {
		session.stop()
	}
	for _, session := range sessions {
		select {
		case <-session.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type managedBridgeWrite struct {
	frame    any
	complete chan error
}

type managedBridgeSession struct {
	handler       *ManagedBridgeHandler
	conn          *websocket.Conn
	post          managedbridge.PostAuth
	commandCursor uint64
	eventCursor   uint64
	ctx           context.Context
	cancel        context.CancelFunc
	wake          chan struct{}
	control       chan managedBridgeWrite
	done          chan struct{}
	readDone      chan struct{}
	writeDone     chan struct{}
	sentCommands  map[uint64]struct{}
	mu            sync.Mutex
	stopOnce      sync.Once
}

func (s *managedBridgeSession) stopped() bool {
	select {
	case <-s.ctx.Done():
		return true
	default:
		return false
	}
}

func (s *managedBridgeSession) stop() {
	s.stopOnce.Do(func() {
		s.cancel()
		_ = s.conn.Close(websocket.StatusGoingAway, "managed bridge session stopped")
	})
}

func (s *managedBridgeSession) wait() {
	<-s.done
}

func (s *managedBridgeSession) run() {
	go s.writePump()
	go s.readPump()
	select {
	case <-s.ctx.Done():
	case <-s.readDone:
	case <-s.writeDone:
	}
	s.stop()
	<-s.readDone
	<-s.writeDone
	close(s.done)
}

func (s *managedBridgeSession) writePump() {
	defer close(s.writeDone)
	ticker := time.NewTicker(s.handler.heartbeat)
	defer ticker.Stop()
	initial := true
	for {
		if message, ok := tryManagedBridgeWrite(s.control); ok {
			if err := s.writeControl(message); err != nil {
				return
			}
			continue
		}
		if initial {
			initial = false
			more, err := s.drainCommands()
			if err != nil {
				return
			}
			if more {
				continue
			}
		}
		select {
		case <-s.ctx.Done():
			return
		case message := <-s.control:
			if err := s.writeControl(message); err != nil {
				return
			}
		case <-s.wake:
			more, err := s.drainCommands()
			if err != nil {
				return
			}
			if more {
				select {
				case s.wake <- struct{}{}:
				default:
				}
			}
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, managedBridgeWriteTimeout)
			err := s.withCurrent(func() error { return s.conn.Ping(ctx) })
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func tryManagedBridgeWrite(control <-chan managedBridgeWrite) (managedBridgeWrite, bool) {
	select {
	case message := <-control:
		return message, true
	default:
		return managedBridgeWrite{}, false
	}
}

func (s *managedBridgeSession) writeControl(message managedBridgeWrite) error {
	err := s.writeCurrent(message.frame)
	if message.complete != nil {
		message.complete <- err
	}
	return err
}

func (s *managedBridgeSession) drainCommands() (bool, error) {
	from := beforeCursor(s.commandCursor)
	if from == 0 {
		return false, managedbridge.ErrInvalidFrame
	}
	var commands []store.ManagedBridgeCommand
	err := s.withCurrent(func() error {
		var readErr error
		commands, readErr = s.handler.store.ReplayManagedBridgeCommands(s.ctx, s.post.BridgeInstance, from, managedBridgeFence(s.post.Fence))
		return readErr
	})
	if err != nil {
		if errors.Is(err, store.ErrManagedBridgeReplayGap) {
			gap, gapErr := s.replayGap(managedbridge.DomainCommands)
			if gapErr != nil {
				return false, gapErr
			}
			if gapErr = s.writeCurrent(gap); gapErr != nil {
				return false, gapErr
			}
			return false, store.ErrManagedBridgeReplayGap
		}
		return false, err
	}
	pending := 0
	for _, command := range commands {
		if command.State != "queued" {
			continue
		}
		s.mu.Lock()
		_, sent := s.sentCommands[command.Seq]
		s.mu.Unlock()
		if sent {
			continue
		}
		pending++
		if pending > s.handler.maxDrain {
			return true, nil
		}
		var payload []byte
		err := s.withCurrent(func() error {
			var readErr error
			payload, readErr = s.handler.store.ReadManagedBridgeCommandPayload(s.ctx, command.BridgeID, command.Seq, command.Fence)
			return readErr
		})
		if err != nil {
			return false, err
		}
		hash := command.PayloadHash
		frame := managedbridge.Command{PostAuth: s.post, Protocol: managedbridge.ProtocolVersion, Type: string(managedbridge.FrameCommand), Seq: command.Seq, ID: command.RequestID, Name: command.Name, Payload: payload, Hash: hash}
		if err := s.writeCurrent(frame); err != nil {
			return false, err
		}
		s.mu.Lock()
		s.sentCommands[command.Seq] = struct{}{}
		s.mu.Unlock()
	}
	return false, nil
}

func (s *managedBridgeSession) readPump() {
	defer close(s.readDone)
	for {
		kind, data, err := s.conn.Read(s.ctx)
		if err != nil || kind != websocket.MessageText || !utf8.Valid(data) {
			return
		}
		frame, err := managedbridge.DecodeFrame(data)
		if err != nil || !s.handler.current(s) {
			return
		}
		switch value := frame.(type) {
		case managedbridge.Event:
			if err := s.handleEvent(value); err != nil {
				return
			}
		case managedbridge.Ack:
			if err := s.handleAck(value); err != nil {
				return
			}
		default:
			return
		}
	}
}

func (s *managedBridgeSession) handleEvent(event managedbridge.Event) error {
	if event.PostAuth != s.post {
		return managedbridge.ErrStaleFrame
	}
	var admitErr error
	err := s.withCurrent(func() error {
		_, admitErr = s.handler.store.AdmitManagedBridgeEventFrame(s.ctx, event)
		return admitErr
	})
	if err != nil {
		if errors.Is(err, store.ErrManagedBridgeSequenceGap) || errors.Is(err, store.ErrManagedBridgeReplayGap) {
			return s.sendGap(managedbridge.DomainEvents)
		}
		return err
	}
	if hook := s.handler.afterEventAdmission; hook != nil {
		hook(s)
	}
	ack := managedbridge.Ack{PostAuth: s.post, Protocol: managedbridge.ProtocolVersion, Type: string(managedbridge.FrameAck), Domain: managedbridge.DomainEvents, Seq: event.Seq}
	if err := s.queueAndWait(ack); err != nil {
		return err
	}
	// Applied is recorded after the ACK write. If the socket disappears between
	// admission and ACK, the bridge resumes with its old cursor and resends the
	// exact event; admission then returns the duplicate and the ACK is repeated.
	return s.withCurrent(func() error {
		return s.handler.store.ApplyManagedBridgeEvent(s.ctx, event.BridgeInstance, event.Seq, managedBridgeFence(event.Fence))
	})
}

func (s *managedBridgeSession) handleAck(ack managedbridge.Ack) error {
	if ack.PostAuth != s.post || ack.Domain != managedbridge.DomainCommands {
		return managedbridge.ErrStaleFrame
	}
	err := s.withCurrent(func() error {
		return s.handler.store.AcknowledgeManagedBridgeCommand(s.ctx, ack.BridgeInstance, ack.Seq, managedBridgeFence(ack.Fence))
	})
	if errors.Is(err, store.ErrManagedBridgeSequenceGap) {
		return s.sendGap(managedbridge.DomainCommands)
	}
	return err
}

func (s *managedBridgeSession) queueAndWait(frame any) error {
	complete := make(chan error, 1)
	message := managedBridgeWrite{frame: frame, complete: complete}
	select {
	case s.control <- message:
	case <-s.ctx.Done():
		return managedbridge.ErrStaleFrame
	}
	select {
	case err := <-complete:
		return err
	case <-s.ctx.Done():
		return managedbridge.ErrStaleFrame
	}
}

func (s *managedBridgeSession) writeCurrent(frame any) error {
	return s.withCurrent(func() error {
		data, err := managedbridge.EncodeFrame(frame)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(s.ctx, managedBridgeWriteTimeout)
		defer cancel()
		return s.conn.Write(ctx, websocket.MessageText, data)
	})
}

func (s *managedBridgeSession) sendGap(domain string) error {
	gap, err := s.replayGap(domain)
	if err != nil {
		return err
	}
	if err := s.queueAndWait(gap); err != nil {
		return err
	}
	return store.ErrManagedBridgeReplayGap
}

func (s *managedBridgeSession) replayGap(domain string) (managedbridge.ReplayGap, error) {
	var cursors store.ManagedBridgeCursors
	err := s.withCurrent(func() error {
		var readErr error
		cursors, readErr = s.handler.store.ManagedBridgeCursors(s.ctx, s.post.BridgeInstance)
		return readErr
	})
	if err != nil {
		return managedbridge.ReplayGap{}, err
	}
	expected := cursorBefore(cursors.CommandNext)
	floor := cursors.CommandFloor
	if domain == managedbridge.DomainEvents {
		expected = cursorBefore(cursors.EventAdmittedNext)
		floor = cursors.EventAdmittedFloor
	}
	if expected < floor {
		expected = floor
	}
	if expected == 0 || floor == 0 {
		return managedbridge.ReplayGap{}, managedbridge.ErrInvalidFrame
	}
	return managedbridge.ReplayGap{PostAuth: s.post, Protocol: managedbridge.ProtocolVersion, Type: string(managedbridge.FrameReplayGap), Domain: domain, Expected: expected, Floor: floor}, nil
}

func (s *managedBridgeSession) withCurrent(operation func() error) error {
	s.handler.registry.mu.Lock()
	defer s.handler.registry.mu.Unlock()
	if !s.handler.currentLocked(s) {
		return managedbridge.ErrStaleFrame
	}
	return operation()
}

func managedBridgeFence(fence managedbridge.RuntimeFence) store.ManagedRuntimeFence {
	return store.ManagedRuntimeFence{AgentID: fence.Agent, PiSessionID: fence.PiSessionID, SessionEpoch: int64(fence.PiSessionEpoch), ProcessPID: int(fence.PiPID), ProcessStartMarker: fence.PiStartMarker, ProcessIncarnation: fence.PiIncarnation, RuntimeGeneration: formatUint(fence.RuntimeGeneration), ManifestRevision: int(fence.ManifestRevision), ManifestHash: fence.ManifestHash}
}

func formatUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatUint(value, 10)
}
