// Package transport implements the Mailbox V2 HTTP and WebSocket boundary.
// State transitions stay in store; this package owns wire validation,
// connection lifetime, fan-out, and the small amount of live projection state.
package transport

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	DefaultHost                  = "127.0.0.1"
	DefaultPort                  = 8765
	DefaultPath                  = "/v1/agents"
	DefaultMaxFrameBytes         = 64 * 1024
	DefaultMaxRequestBytes       = 6 * 1024 * 1024
	DefaultLeaseDuration         = 30 * time.Second
	DefaultDeliveryDuration      = 30 * time.Second
	DefaultMaxDeliveryTries      = 3
	DefaultActivityTTL           = 15 * time.Second
	DefaultPendingControlTTL     = 30 * time.Second
	DefaultDeliveryOfferCoalesce = 25 * time.Millisecond
	DefaultSweepHeartbeat        = time.Hour
	DefaultPolicyTimeout         = 5 * time.Second
	DefaultTmuxRouterAuditPeriod = 30 * time.Minute
)

var (
	allowedHosts = map[string]bool{"127.0.0.1": true, "::1": true, "0.0.0.0": true}
	frameIDRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	agentIDRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	messageIDRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	failureRE    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// MemoryCatalog is the optional HTTP memory capability. Implementations may
// be backed by the local knowledge-base catalog.
type MemoryCatalog interface {
	Rebuild(context.Context) (any, error)
	Lookup(context.Context, map[string]any) (any, error)
	Expand(context.Context, map[string]any) (any, error)
	Apply(context.Context, map[string]any) (map[string]any, error)
	Touch(context.Context, map[string]any) (string, error)
	Close() error
}

// ErrTmuxUnavailable tells the HTTP boundary that tmux has no selected session
// or that the selected session disappeared. The launcher treats this as a hard
// failure because provisioning into an unverified inventory can target a
// different terminal server.
var ErrTmuxUnavailable = errors.New("tmux unavailable")

// TmuxWindow is the router-owned representation consumed by worker lifecycle
// clients. Target remains present because existing clients use it for display.
type TmuxWindow struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

// TmuxAdapter keeps process control outside the HTTP boundary. The transport
// owns this interface because it decides which operations and result shape the
// router needs.
type TmuxAdapter interface {
	Sessions(context.Context) ([]string, error)
	ProvisionSession(context.Context, string, string) error
	Windows(context.Context, string) ([]TmuxWindow, error)
	Provision(context.Context, string, string) error
	Remove(context.Context, string, string) error
	Rename(context.Context, string, string, string) error
	SendKeys(context.Context, string, string, string, string) error
	Start(context.Context, string, string, string) (string, string, error)
	Stop(context.Context, string, string, string) (string, string, error)
	Restart(context.Context, string, string, string) (string, string, error)
	Resume(context.Context, string, string, string) (string, string, error)
}

// Options configures the transport. Store is required and must be opened with
// the same persistent SQLite path used by the launcher.
type Options struct {
	Store                  *store.Store
	Host                   string
	Port                   int
	Path                   string
	Clock                  deterministic.Clock
	IDSource               deterministic.IDSource
	Attachments            *attachments.Store
	Memory                 MemoryCatalog
	Tmux                   TmuxAdapter
	MaxFrameBytes          int
	MaxRequestBytes        int
	LeaseDuration          time.Duration
	DeliveryLeaseDuration  time.Duration
	MaxDeliveryAttempts    int
	ActivityTTL            time.Duration
	PendingControlTTL      time.Duration
	SweepInterval          time.Duration
	SweepHeartbeatInterval time.Duration
	MaxPendingControls     int
	MaxPendingPerSource    int
	GoalControlAvailable   bool
	LifecycleAvailable     bool
	EventLogger            func(string, map[string]any)
	NativePolicies         []NativePolicy
	EnableTmuxRouterAudit  bool
	AuditSupervisorTarget  string
	AuditTmuxSession       string
	PolicyTimeout          time.Duration
	PolicySender           PolicySender
	Managed                *managed.Manager
	// StaticUIRoot is an optional, router-owned built React asset directory.
	// It affects serving only and never establishes caller authority.
	StaticUIRoot string
}

type Router struct {
	store          *store.Store
	clock          deterministic.Clock
	ids            deterministic.IDSource
	host           string
	port           int
	path           string
	maxFrame       int
	maxRequest     int
	lease          time.Duration
	delivery       time.Duration
	maxAttempts    int
	activityTTL    time.Duration
	controlTTL     time.Duration
	sweepInterval  time.Duration
	sweepHeartbeat time.Duration
	maxControls    int
	maxPerSrc      int
	memory         MemoryCatalog
	tmux           TmuxAdapter
	attachments    *attachments.Store
	log            func(string, map[string]any)
	policies       []NativePolicy
	policyTimeout  time.Duration
	policySender   PolicySender
	policyMu       sync.Mutex
	policyRunning  map[string]bool
	policyWG       sync.WaitGroup
	policyRunWG    sync.WaitGroup
	policyLastRun  map[string]time.Time
	policiesClosed bool
	policyContext  context.Context
	policyCancel   context.CancelFunc

	mu sync.RWMutex
	// lifecycleMu serializes listener startup with the transition to closed.
	// This keeps a startup sweep from touching the store after Close returns.
	lifecycleMu           sync.Mutex
	deliveryOffersContext context.Context
	// sweepMu is the lifecycle boundary: startup, periodic, and shutdown
	// sweeps cannot overlap one another or live-state cleanup.
	sweepMu   sync.Mutex
	closeDone chan struct{}
	closeErr  error
	// conversationLocks serializes each conversation's durable write and live
	// broadcast together. This is the synchronous Node handler boundary; a
	// separate lock per participant pair keeps unrelated conversations moving.
	conversationLocksMu sync.Mutex
	conversationLocks   map[string]*conversationLock
	// ownershipMu protects the small per-agent transition table. A transition
	// reserves one agent while its SQLite and socket work runs, but this mutex
	// is only held while reading or changing that table.
	ownershipMu         sync.Mutex
	ownership           map[string]*ownershipTransition
	ownershipGeneration uint64
	// deliveryOfferMu protects one coalescing timer per agent. Close marks the
	// timer set closed before waiting so no callback can be added concurrently.
	deliveryOfferMu          sync.Mutex
	pendingDeliveryOffers    map[string]*pendingDeliveryOffer
	deliveryOfferWG          sync.WaitGroup
	deliveryStoreWG          sync.WaitGroup
	deliveryOffersClosed     bool
	deliveryOffersCancel     context.CancelFunc
	server                   *http.Server
	listener                 net.Listener
	serveDone                chan struct{}
	sweepCancel              context.CancelFunc
	sweepDone                chan struct{}
	closed                   bool
	sessions                 map[string]*session
	convos                   map[string]map[string]*session
	conversationOrder        map[string][]string
	activities               map[string]activity
	models                   map[string]modelProjection
	goals                    map[string]goalState
	inlineToolStates         map[string]inlineToolState
	latestGoalRevision       map[string]int64
	lastSweepRecord          time.Time
	pendingMod               map[string]*pendingControl
	pendingGoal              map[string]*pendingControl
	pendingLock              map[string]*pendingControl
	pendingInlineTool        map[string]*pendingControl
	lifecycleState           map[string]lifecycleAgent
	nextVersion              int64
	rates                    map[string][]time.Time
	selectedTmux             string
	windowIDs                map[string]string
	tmuxProvisionals         map[string]*tmuxProvisioning
	tmuxSessionProvisioning  *tmuxProvisioning
	goalAvailable            bool
	lifecycleAvailable       bool
	managed                  *managed.Manager
	managedPathCanonicalizer func(*http.Request) string
	staticUI                 *staticUI
	uiLive                   *uiLiveHub
}

type session struct {
	router            *Router
	conn              *websocket.Conn
	agentID           string
	instanceID        string
	deliveryInstance  string
	leaseID           string
	role              string
	clientType        string
	ackConfirmation   string
	conversationSync  string
	deliveryReceipts  string
	inlineToolControl bool
	// tmuxSourceID is durable across this socket and primary replacements.
	tmuxSourceID     string
	paused           bool
	lifecycleEnabled bool
	writeMu          sync.Mutex
	stateMu          sync.RWMutex
	closeOnce        sync.Once
	sendOverride     func(map[string]any) error
	closeOverride    func(websocket.StatusCode, string) error
}

type activity struct {
	state      string
	instanceID string
	leaseID    string
	until      time.Time
	expires    time.Time
	count      int
	subagents  []map[string]any
	goal       map[string]any
	model      map[string]any
}
type modelProjection struct {
	instanceID string
	leaseID    string
	current    map[string]any
	available  []map[string]any
	total      int
	truncated  bool
}
type inlineToolState struct {
	instanceID string
	leaseID    string
	enabled    bool
}
type goalState struct {
	instanceID string
	leaseID    string
	projection map[string]any
	state      map[string]any
}
type pendingControl struct {
	controlID string
	requestID string
	source    *session
	target    *session
	expires   time.Time
	revision  int64
	reminder  string
}
type lifecycleAgent struct {
	version   int64
	subagents []map[string]any
}

type conversationLock struct {
	mu   sync.Mutex
	refs int
}

type ownershipTransition struct {
	agentID    string
	generation uint64
	done       chan struct{}
}

type pendingDeliveryOffer struct {
	timer *time.Timer
	done  chan struct{}
}

type tmuxProvisioning struct {
	done      chan struct{}
	err       error
	sessionID string
}

func wallClock() time.Time { return time.Now() }

type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

type randomIDs struct{}

func (randomIDs) Next() (string, error) {
	value := fmt.Sprintf("%d", time.Now().UnixNano())
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:16]), nil
}

// New constructs an HTTP/WebSocket transport over an already-open Store.
func New(options Options) (*Router, error) {
	if options.Store == nil {
		return nil, errors.New("store is required")
	}
	if options.Managed != nil && options.Managed.Store() != options.Store {
		return nil, errors.New("managed manager must use the router store")
	}
	static, err := newStaticUI(options.StaticUIRoot)
	if err != nil {
		return nil, err
	}
	host := options.Host
	if host == "" {
		host = DefaultHost
	}
	if !allowedHosts[host] {
		return nil, errors.New("host must be 127.0.0.1, ::1, or 0.0.0.0")
	}
	port := options.Port
	if port < 0 || port > 65535 {
		return nil, errors.New("port must be an integer from 0 through 65535")
	}
	path := options.Path
	if path == "" {
		path = DefaultPath
	}
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("WebSocket path must begin with /")
	}
	clock := options.Clock
	if clock == nil {
		clock = clockFunc(wallClock)
	}
	ids := options.IDSource
	if ids == nil {
		ids = randomIDs{}
	}
	maxFrame := options.MaxFrameBytes
	if maxFrame == 0 {
		maxFrame = DefaultMaxFrameBytes
	}
	maxRequest := options.MaxRequestBytes
	if maxRequest == 0 {
		maxRequest = DefaultMaxRequestBytes
	}
	lease := options.LeaseDuration
	if lease == 0 {
		lease = DefaultLeaseDuration
	}
	delivery := options.DeliveryLeaseDuration
	if delivery == 0 {
		delivery = DefaultDeliveryDuration
	}
	attempts := options.MaxDeliveryAttempts
	if attempts == 0 {
		attempts = DefaultMaxDeliveryTries
	}
	activityTTL := options.ActivityTTL
	if activityTTL == 0 {
		activityTTL = DefaultActivityTTL
	}
	controlTTL := options.PendingControlTTL
	if controlTTL == 0 {
		controlTTL = DefaultPendingControlTTL
	}
	sweepInterval := options.SweepInterval
	if sweepInterval == 0 {
		sweepInterval = time.Second
	}
	sweepHeartbeat := options.SweepHeartbeatInterval
	if sweepHeartbeat == 0 {
		sweepHeartbeat = DefaultSweepHeartbeat
	}
	maxControls := options.MaxPendingControls
	if maxControls == 0 {
		maxControls = 256
	}
	maxPerSrc := options.MaxPendingPerSource
	if maxPerSrc == 0 {
		maxPerSrc = 16
	}
	for name, value := range map[string]time.Duration{"lease": lease, "delivery": delivery, "activity": activityTTL, "control": controlTTL, "sweep": sweepInterval, "sweep heartbeat": sweepHeartbeat} {
		if value <= 0 {
			return nil, fmt.Errorf("%s duration must be positive", name)
		}
	}
	if maxFrame <= 0 || maxFrame > 64*1024 || maxRequest <= 0 || maxRequest > DefaultMaxRequestBytes || attempts <= 0 || maxPerSrc <= 0 || maxControls < maxPerSrc {
		return nil, errors.New("invalid router limits")
	}
	policyTimeout := options.PolicyTimeout
	if policyTimeout == 0 {
		policyTimeout = DefaultPolicyTimeout
	}
	if policyTimeout <= 0 {
		return nil, errors.New("policy timeout must be positive")
	}
	auditTarget := options.AuditSupervisorTarget
	if auditTarget == "" {
		auditTarget = "supervisor"
	}
	if !agentIDRE.MatchString(auditTarget) {
		return nil, errors.New("audit supervisor target is invalid")
	}
	auditTmux := options.AuditTmuxSession
	if auditTmux == "" {
		auditTmux = "mecha"
	}
	if strings.TrimSpace(auditTmux) == "" {
		return nil, errors.New("audit tmux session is required")
	}
	policies := append([]NativePolicy(nil), options.NativePolicies...)
	if options.EnableTmuxRouterAudit {
		policies = append(policies, NewTmuxRouterAuditPolicy(auditTarget, auditTmux))
	}
	policies, err = nativePolicies(policies)
	if err != nil {
		return nil, err
	}
	logger := options.EventLogger
	if logger == nil {
		logger = func(string, map[string]any) {}
	}
	lifecycle := options.LifecycleAvailable
	if !options.LifecycleAvailable {
		lifecycle = contains(options.Store.MigrationIDs(), "017_subagent_lifecycle")
	}
	goal := options.GoalControlAvailable
	if !options.GoalControlAvailable {
		goal = contains(options.Store.MigrationIDs(), "019_goal_control_desired_state") && contains(options.Store.MigrationIDs(), "021_goal_control_revision_history")
	}
	deliveryOffersContext, deliveryOffersCancel := context.WithCancel(context.Background())
	policyContext, policyCancel := context.WithCancel(context.Background())
	r := &Router{store: options.Store, clock: clock, ids: ids, host: host, port: port, path: path, maxFrame: maxFrame, maxRequest: maxRequest, lease: lease, delivery: delivery, maxAttempts: attempts, activityTTL: activityTTL, controlTTL: controlTTL, sweepInterval: sweepInterval, sweepHeartbeat: sweepHeartbeat, maxControls: maxControls, maxPerSrc: maxPerSrc, memory: options.Memory, tmux: options.Tmux, attachments: options.Attachments, log: logger, policies: policies, policyTimeout: policyTimeout, policySender: options.PolicySender, policyRunning: map[string]bool{}, policyLastRun: map[string]time.Time{}, policyContext: policyContext, policyCancel: policyCancel, sessions: map[string]*session{}, convos: map[string]map[string]*session{}, conversationLocks: map[string]*conversationLock{}, conversationOrder: map[string][]string{}, activities: map[string]activity{}, models: map[string]modelProjection{}, goals: map[string]goalState{}, inlineToolStates: map[string]inlineToolState{}, latestGoalRevision: map[string]int64{}, pendingMod: map[string]*pendingControl{}, pendingGoal: map[string]*pendingControl{}, pendingLock: map[string]*pendingControl{}, pendingInlineTool: map[string]*pendingControl{}, lifecycleState: map[string]lifecycleAgent{}, rates: map[string][]time.Time{}, windowIDs: map[string]string{}, tmuxProvisionals: map[string]*tmuxProvisioning{}, ownership: map[string]*ownershipTransition{}, pendingDeliveryOffers: map[string]*pendingDeliveryOffer{}, deliveryOffersContext: deliveryOffersContext, deliveryOffersCancel: deliveryOffersCancel, managed: options.Managed, staticUI: static}
	r.uiLive = newUILiveHub(r)
	if options.Managed != nil {
		// Managed lifecycle snapshots commit in the supervisor, outside an HTTP
		// handler. Wake UI-live only after that durable commit; the hub reads its
		// own projection and never trusts a manager-supplied browser payload.
		options.Managed.SetProjectionPublisher(func() { r.uiLive.publish(context.Background()) })
	}
	r.goalAvailable, r.lifecycleAvailable = goal, lifecycle
	if r.policySender == nil {
		r.policySender = r.sendPolicyMessage
	}
	if lifecycle {
		projection, err := options.Store.LifecycleProjection(context.Background())
		if err != nil {
			return nil, err
		}
		r.hydrateLifecycleProjection(projection)
	}
	return r, nil
}

// NewWithStore is a convenience for callers that prefer the store as the first
// argument.
func NewWithStore(s *store.Store, options Options) (*Router, error) {
	options.Store = s
	return New(options)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool { return contains(values, wanted) }

// URLs returns the bound HTTP and WebSocket URLs. It is valid after Listen.
func (r *Router) URLs() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	port := r.port
	if r.listener != nil {
		if address, ok := r.listener.Addr().(*net.TCPAddr); ok {
			port = address.Port
		}
	}
	host := r.host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d", host, port), fmt.Sprintf("ws://%s:%d%s", host, port, r.path)
}

// Listen performs the startup lifecycle sweep before opening the listener.
// A router only becomes reachable after its durable state is clean.
func (r *Router) Listen(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("router is closed")
	}
	if r.listener != nil {
		r.mu.Unlock()
		return errors.New("router is already listening")
	}
	r.mu.Unlock()
	if r.managed != nil {
		if err := r.managed.RecoverPendingManagedLaunches(ctx); err != nil {
			return err
		}
		if err := r.managed.RecoverManagedRestartTransitions(ctx); err != nil {
			return err
		}
		if err := r.managed.RecoverManagedLifecycleControls(ctx); err != nil {
			return err
		}
	}
	if err := r.store.ReconcileManagedRecipientSources(ctx); err != nil {
		return err
	}
	if err := r.store.ReconcileMessageTargets(ctx); err != nil {
		return err
	}
	if r.managed != nil {
		if err := r.managed.RecoverManagedDirectAttempts(ctx); err != nil {
			return err
		}
	}
	if err := r.runSweep(ctx); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(r.host, fmt.Sprint(r.port)))
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = listener.Close()
		return errors.New("router is closed")
	}
	r.listener = listener
	r.serveDone = make(chan struct{})
	r.server = &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second}
	sweepCtx, sweepCancel := context.WithCancel(context.Background())
	r.sweepCancel = sweepCancel
	r.sweepDone = make(chan struct{})
	done := r.serveDone
	sweepDone := r.sweepDone
	server := r.server
	r.mu.Unlock()
	_, websocketURL := r.URLs()
	r.log("router_started", map[string]any{"host": r.host, "port": listener.Addr().(*net.TCPAddr).Port, "router_url": websocketURL})
	go func() { _ = server.Serve(listener); close(done) }()
	go r.sweepLoop(sweepCtx, sweepDone)
	return nil
}

// ServeHTTP exposes the router for net/http and httptest callers.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/v1/ui-live" {
		r.serveUILive(w, req)
		return
	}
	if req.URL.Path == r.path && websocketUpgrade(req) {
		r.serveWebSocket(w, req)
		return
	}
	if r.serveStaticUI(w, req) {
		return
	}
	r.handleHTTP(w, req)
}

// Close stops HTTP and all connected WebSockets. It does not close the Store.
// The caller keeps the Store open until this method returns so the final
// attachment sweep can still check conversation references.
func (r *Router) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.lifecycleMu.Lock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.lifecycleMu.Unlock()
		done := r.closeDone
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		r.mu.RLock()
		err := r.closeErr
		r.mu.RUnlock()
		return err
	}
	r.closed = true
	r.closeDone = make(chan struct{})
	r.lifecycleMu.Unlock()
	done := r.closeDone
	server := r.server
	sweepCancel := r.sweepCancel
	sweepDone := r.sweepDone
	sessions := make([]*session, 0, len(r.sessions))
	for _, candidate := range r.sessions {
		sessions = append(sessions, candidate)
	}
	for _, subscribers := range r.convos {
		for _, candidate := range subscribers {
			if !containsSession(sessions, candidate) {
				sessions = append(sessions, candidate)
			}
		}
	}
	r.mu.Unlock()
	if r.deliveryOffersCancel != nil {
		r.deliveryOffersCancel()
	}
	if r.uiLive != nil {
		r.uiLive.close()
	}
	var err error
	if r.managed != nil {
		if managedErr := r.managed.Close(ctx); managedErr != nil {
			err = managedErr
		}
	}
	if policyErr := r.stopPolicies(ctx); policyErr != nil {
		err = policyErr
	}
	for _, candidate := range sessions {
		_ = candidate.close(websocket.StatusGoingAway, "router shutdown")
	}
	if offerErr := r.stopPendingDeliveryOffers(ctx); err == nil {
		err = offerErr
	}
	if sweepCancel != nil && sweepDone != nil {
		sweepCancel()
		select {
		case <-sweepDone:
		case <-ctx.Done():
			if err == nil {
				err = ctx.Err()
			}
		}
	}
	if server != nil {
		if shutdownErr := server.Shutdown(ctx); err == nil {
			err = shutdownErr
		}
	}
	r.sweepAttachmentsBestEffort()
	if r.memory != nil {
		if closeErr := r.memory.Close(); err == nil {
			err = closeErr
		}
	}
	// The stopped record is part of the Close completion contract. Write it
	// before releasing waiters from concurrent/idempotent Close calls.
	r.log("router_stopped", nil)
	r.mu.Lock()
	r.closeErr = err
	close(done)
	r.mu.Unlock()
	return err
}

func containsSession(values []*session, wanted *session) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
func (s *session) setPaused(value bool) {
	s.stateMu.Lock()
	s.paused = value
	s.stateMu.Unlock()
}
func (s *session) isPaused() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.paused
}
func (s *session) setLifecycleEnabled(value bool) {
	s.stateMu.Lock()
	s.lifecycleEnabled = value
	s.stateMu.Unlock()
}
func (s *session) lifecycleEnabledNow() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.lifecycleEnabled
}

func (r *Router) nextID() (string, error)                  { return r.ids.Next() }
func (r *Router) now() time.Time                           { return r.clock.Now().UTC() }
func timestamp(value time.Time) string                     { return value.UTC().Format("2006-01-02T15:04:05.000Z") }
func (r *Router) emit(event string, fields map[string]any) { r.log(event, fields) }
