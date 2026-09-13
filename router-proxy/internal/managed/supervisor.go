package managed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type LaunchRequest struct {
	Name               string
	Cwd                string
	Model              *Model
	InitialInstruction string
	Method             string
	CanonicalPath      string
	RequestID          string
	CanonicalBodyHash  string
}
type LaunchResult struct {
	Agent    *store.ManagedAgentRecord
	Replayed bool
}
type RestartPreparationDeadlineFactory func(context.Context, time.Duration) (context.Context, context.CancelFunc)
type RestartPreparationHook func(context.Context, string) error
type ProcessGroupLeaderVerifier func(ProcessIdentity) bool

type ManagerOptions struct {
	Store                             *store.Store
	Config                            Config
	Manifest                          Manifest
	ProcessFactory                    ProcessFactory
	RPCFactory                        RPCFactory
	CatalogSource                     ModelSource
	Clock                             deterministic.Clock
	IDSource                          deterministic.IDSource
	ProcessGroupLeaderVerifier        ProcessGroupLeaderVerifier
	ProcessGroupOps                   ProcessGroupOps
	RestartPreparationDeadlineFactory RestartPreparationDeadlineFactory
	RestartPreparationHook            RestartPreparationHook
	// BeforeManagedDirectAcknowledge is a test seam for the durable
	// append-before-ack crash boundary. Production leaves it nil.
	BeforeManagedDirectAcknowledge func(context.Context, store.ManagedDirectAttempt) error
	// BeforeManagedDirectRecovery pauses the lane-owned recovery scan in focused
	// tests. Production leaves it nil.
	BeforeManagedDirectRecovery func(context.Context, string)
	// The target-start hooks model bounded reset/restart crash boundaries in
	// focused/native tests. Production leaves them nil; they add no runtime
	// control surface.
	BeforeManagedResetOldArtifactCleanup              func(context.Context, string) error
	AfterManagedResetTargetStartBeforeBind            func(context.Context, string, Process) error
	AfterManagedLifecycleRestartTargetStartBeforeBind func(context.Context, string, Process) error
	// BeforeManagedLaunchStart lets focused tests hold the post-reservation
	// handoff. Production leaves it nil; it creates no runtime control surface.
	BeforeManagedLaunchStart func(context.Context, string)
	// BeforeManagedTerminationArtifactCleanup models a durable retry boundary
	// after an exact group has been reaped but before session files are removed.
	// Production leaves it nil.
	BeforeManagedTerminationArtifactCleanup func(context.Context, string) error
	// RestartAllDispatcher is a post-commit handoff seam. Test doubles record
	// durable IDs only; production leaves it nil and uses the S3 restart bridge.
	RestartAllDispatcher              RestartAllDispatcher
	RestartAllDispatchFailureRecorder RestartAllDispatchFailureRecorder
	// RootControlHandlers is the concrete owner set for the closed 16-operation
	// registry. Nil retains safe correlated unavailable results until the host
	// wires its real supervisor-service adapters.
	RootControlHandlers []RootControlHandler
}

// RestartRequest already contains canonical operation metadata. A future HTTP
// route owns parsing and canonicalization; it must not bypass this internal
// reservation boundary.
type RestartRequest struct {
	AgentID           string
	Method            string
	CanonicalPath     string
	RequestID         string
	CanonicalBodyHash string
	// Model is applied atomically with the restart so the replacement process
	// cannot begin with a model other than the one shown to the user.
	Model *Model
}

type RestartResult struct {
	Agent     *store.ManagedAgentRecord
	Operation *store.ManagedOperation
	Replayed  bool
}

// ResetRequest contains only canonical transport metadata. The reset executor
// owns live binding checks, fresh session allocation, and all process work.
type ResetRequest struct {
	AgentID           string
	Method            string
	CanonicalPath     string
	RequestID         string
	CanonicalBodyHash string
}

type ResetResult struct {
	Agent     *store.ManagedAgentRecord
	Operation *store.ManagedOperation
	Replayed  bool
}

// managedAgentLane survives a binding replacement. Restart execution, recovery,
// close, and exit handling all acquire this one owner before they can touch the
// durable transition or either process group.
type managedAgentLane struct{ serial sync.Mutex }

const (
	maxQueuedManagedChildDeltas          = 64
	maxQueuedManagedBuiltinToolLifecycle = 64
)

type managedBinding struct {
	agentID string
	lane    *managedAgentLane
	serial  sync.Mutex
	// Root controls can resolve an active direct turn. Keep their append path
	// ordered without blocking the direct-delivery lane that needs that result.
	rootControlSerial       sync.Mutex
	childDeltaSerial        sync.Mutex
	childDeltaQueue         []map[string]any
	childDeltaDraining      bool
	builtinToolQueueSerial  sync.Mutex
	builtinToolQueue        []builtinToolLifecycleQueueItem
	builtinToolDraining     bool
	builtinToolOverflowed   bool
	builtinToolQueueStopped bool
	builtinToolSerial       sync.Mutex
	builtinToolOrphanEnds   map[string]builtinToolOrphanEnd
	preparationWG           sync.WaitGroup
	process                 Process
	rpc                     RPC
	operationID             string
	lifecycleAction         string
	identity                ProcessIdentity
	sessionID               string
	sessionEpoch            int64
	runtimeGeneration       string
	manifestRevision        int
	manifestHash            string
	parentCompacting        *bool
	reaped                  bool
	token                   string
	waitDone                <-chan error
	waitSignal              <-chan struct{}
	waitErr                 error
	restartExecutionDone    bool
	restartShutdownDone     bool
	restartShutdownError    error
	// retainBinding keeps a failed EOF cleanup owner available for retry.
	retainBinding bool
}

type Manager struct {
	store                                      *store.Store
	config                                     Config
	manifest                                   Manifest
	processFactory                             ProcessFactory
	rpcFactory                                 RPCFactory
	catalog                                    ModelSource
	clock                                      deterministic.Clock
	ids                                        deterministic.IDSource
	mu                                         sync.Mutex
	processes                                  map[string]Process
	rpcs                                       map[string]RPC
	operationIDs                               map[string]string
	bindings                                   map[string]*managedBinding
	lanes                                      map[string]*managedAgentLane
	groupLeader                                ProcessGroupLeaderVerifier
	processOps                                 ProcessGroupOps
	preparationDeadline                        RestartPreparationDeadlineFactory
	preparationHook                            RestartPreparationHook
	beforeDirectAcknowledge                    func(context.Context, store.ManagedDirectAttempt) error
	beforeManagedDirectRecovery                func(context.Context, string)
	beforeResetOldArtifactCleanup              func(context.Context, string) error
	afterResetTargetStartBeforeBind            func(context.Context, string, Process) error
	afterLifecycleRestartTargetStartBeforeBind func(context.Context, string, Process) error
	beforeManagedLaunchStart                   func(context.Context, string)
	beforeTerminationArtifactCleanup           func(context.Context, string) error
	restartAllDispatcher                       RestartAllDispatcher
	restartAllDispatchFailureRecorder          RestartAllDispatchFailureRecorder
	rootControlHandlers                        *RootControlHandlerRegistry
	projectionPublisher                        func()
	completeHistoryPublisher                   CompleteHistoryPublisher
	directReplyPublisher                       DirectReplyPublisher
	artifacts                                  *sessionArtifactOwner
	runCtx                                     context.Context
	cancel                                     context.CancelFunc
	startWG                                    sync.WaitGroup
	closed                                     bool
	closeErr                                   error
}

// AllowedWorkingDirectoryRoots returns a copy of the startup allowlist for the
// direct folder browser. It exposes no process or auth state.
func (m *Manager) AllowedWorkingDirectoryRoots() []string {
	return append([]string(nil), m.config.AllowedWorkingDirectoryRoots...)
}

// SetRouterURL records the exact listener URL for future managed launches.
// Existing managed processes retain their inherited environment.
func (m *Manager) SetRouterURL(raw string) error {
	if err := ValidateManagedRouterURL(raw); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.RouterURL = raw
	return nil
}

func NewManager(options ManagerOptions) (*Manager, error) {
	if options.Store == nil {
		return nil, errors.New("managed store is required")
	}
	config := options.Config
	config.normalize()
	if err := ValidateManagedLaunchConfig(config); err != nil {
		return nil, err
	}
	manifest := options.Manifest
	if manifest.Protocol == "" {
		loaded, _, err := LoadManifest(config.ManifestPath)
		if err != nil {
			return nil, err
		}
		manifest = loaded
	}
	if len(manifest.Tools) != len(managedManifestOperations) {
		return nil, fmt.Errorf("managed manifest must have %d tools", len(managedManifestOperations))
	}
	rootHandlers, err := NewRootControlHandlerRegistry(manifest, options.RootControlHandlers)
	if err != nil {
		return nil, err
	}
	clock := options.Clock
	if clock == nil {
		clock = managerClock{}
	}
	ids := options.IDSource
	if ids == nil {
		ids = managerIDs{}
	}
	processFactory := options.ProcessFactory
	if processFactory == nil {
		processFactory = DefaultProcessFactory()
	}
	rpcFactory := options.RPCFactory
	if rpcFactory == nil {
		rpcFactory = DefaultRPCFactory()
	}
	catalog := options.CatalogSource
	if catalog == nil {
		catalog = CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Available: true})
	}
	groupLeader := options.ProcessGroupLeaderVerifier
	if groupLeader == nil {
		groupLeader = defaultProcessGroupLeaderVerifier
	}
	processOps := options.ProcessGroupOps
	if processOps == nil {
		processOps = DefaultProcessGroupOps()
	}
	deadlineFactory := options.RestartPreparationDeadlineFactory
	if deadlineFactory == nil {
		deadlineFactory = context.WithTimeout
	}
	runCtx, cancel := context.WithCancel(context.Background())
	manager := &Manager{store: options.Store, config: config, manifest: manifest, processFactory: processFactory, rpcFactory: rpcFactory, catalog: catalog, clock: clock, ids: ids, processes: map[string]Process{}, rpcs: map[string]RPC{}, operationIDs: map[string]string{}, bindings: map[string]*managedBinding{}, lanes: map[string]*managedAgentLane{}, groupLeader: groupLeader, processOps: processOps, preparationDeadline: deadlineFactory, preparationHook: options.RestartPreparationHook, beforeDirectAcknowledge: options.BeforeManagedDirectAcknowledge, beforeManagedDirectRecovery: options.BeforeManagedDirectRecovery, beforeResetOldArtifactCleanup: options.BeforeManagedResetOldArtifactCleanup, afterResetTargetStartBeforeBind: options.AfterManagedResetTargetStartBeforeBind, afterLifecycleRestartTargetStartBeforeBind: options.AfterManagedLifecycleRestartTargetStartBeforeBind, beforeManagedLaunchStart: options.BeforeManagedLaunchStart, beforeTerminationArtifactCleanup: options.BeforeManagedTerminationArtifactCleanup, restartAllDispatcher: options.RestartAllDispatcher, restartAllDispatchFailureRecorder: options.RestartAllDispatchFailureRecorder, rootControlHandlers: rootHandlers, runCtx: runCtx, cancel: cancel}
	manager.artifacts = newSessionArtifactOwner(options.Store, config.SessionRoot, ids, clock.Now)
	// A router restart cannot reconstruct gaps in a prior Pi stdout pipe. Close
	// every surviving active builtin row before any manager can expose history.
	if err := options.Store.InterruptAllManagedBuiltinInvocations(context.Background()); err != nil {
		return nil, err
	}
	return manager, nil
}

type managerClock struct{}

func (managerClock) Now() time.Time { return time.Now() }

type managerIDs struct{}

func (managerIDs) Next() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 15) | 64
	value[8] = (value[8] & 63) | 128
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func (m *Manager) Launch(ctx context.Context, request LaunchRequest) (LaunchResult, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return LaunchResult{}, errors.New("managed manager is closed")
	}
	if !managedNameRE.MatchString(request.Name) {
		return LaunchResult{}, errors.New("invalid managed agent name")
	}
	if len([]byte(request.InitialInstruction)) > m.config.Limits.MaxInitialInstructionBytes {
		return LaunchResult{}, errors.New("initial instruction is too large")
	}
	cwd, err := ValidateWorkingDirectory(request.Cwd, m.config.AllowedWorkingDirectoryRoots)
	if err != nil {
		return LaunchResult{}, err
	}
	var selected Model
	if selector, ok := m.catalog.(ModelSelector); ok {
		selected, err = selector.Select(ctx, request.Model)
	} else {
		var catalog Catalog
		catalog, err = m.catalog.Catalog(ctx)
		if err == nil {
			var saved *store.ManagedModel
			saved, err = m.store.GetDefaultModel(ctx)
			if err == nil {
				selected, err = SelectModel(request.Model, saved, catalog)
			}
		}
	}
	if err != nil {
		return LaunchResult{}, err
	}
	sessionID, err := m.ids.Next()
	if err != nil {
		return LaunchResult{}, err
	}
	runtimeGeneration, err := m.ids.Next()
	if err != nil {
		return LaunchResult{}, err
	}
	if request.RequestID == "" {
		request.RequestID, err = m.ids.Next()
		if err != nil {
			return LaunchResult{}, err
		}
	}
	if request.Method == "" {
		request.Method = "POST"
	}
	if request.CanonicalPath == "" {
		request.CanonicalPath = "/v1/managed-agents"
	}
	if request.CanonicalBodyHash == "" {
		request.CanonicalBodyHash = hashString(request.Name + "\x00" + cwd + "\x00" + selected.Provider + "\x00" + selected.ID + "\x00" + request.InitialInstruction)
	}
	now := formatManagedTime(m.clock.Now())
	agent := store.ManagedAgentRecord{AgentID: request.Name, Name: request.Name, SlotHeld: true, Cwd: cwd, SelectedModel: store.ManagedModel{Provider: selected.Provider, ID: selected.ID, Name: selected.Name}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", SessionEpoch: 1, PiSessionID: sessionID, RuntimeGeneration: runtimeGeneration, ManifestRevision: manifestRevision(m.manifest), ManifestHash: m.manifest.CanonicalSHA256, PendingInitialInstruction: request.InitialInstruction, CreatedAt: now, UpdatedAt: now}
	if m.config.Enabled {
		recipe, recipeErr := m.managedLaunchRecipe(agent)
		if recipeErr != nil {
			return LaunchResult{}, recipeErr
		}
		agent.LaunchRecipe = &recipe
	}
	public := PublicAgent(&agent)
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": public})
	if err != nil {
		return LaunchResult{}, err
	}
	op := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"X-Request-ID": request.RequestID, "Content-Type": "application/json; charset=utf-8"}, ResponseBody: body, CreatedAt: now}
	// Launch admission and every launch callback use this same lane. A
	// termination that wins the handoff can tombstone the durable row before the
	// start task reaches ProcessFactory.Start, never after it has escaped.
	lane := m.laneFor(agent.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	if err := m.store.ReserveManagedLaunchWithLimit(ctx, agent, op, m.config.Limits.MaxAgents); err != nil {
		return LaunchResult{}, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = m.store.MarkManagedFailed(context.Background(), agent.AgentID, "MANAGER_CLOSED", "managed manager closed")
		return LaunchResult{}, errors.New("managed manager is closed")
	}
	binding := &managedBinding{agentID: agent.AgentID, lane: lane, operationID: request.RequestID, lifecycleAction: "launch", token: agent.RuntimeGeneration, sessionID: agent.PiSessionID, sessionEpoch: agent.SessionEpoch, runtimeGeneration: agent.RuntimeGeneration, manifestRevision: agent.ManifestRevision, manifestHash: agent.ManifestHash}
	m.bindings[agent.AgentID] = binding
	m.operationIDs[agent.AgentID] = request.RequestID
	m.startWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.startWG.Done()
		m.start(m.runCtx, agent, request, binding)
	}()
	return LaunchResult{Agent: &agent}, nil
}
func manifestRevision(value Manifest) int {
	if value.Revision < 1 {
		return 1
	}
	return value.Revision
}
func formatManagedTime(value time.Time) string { return value.UTC().Format("2006-01-02T15:04:05.000Z") }
func hashString(value string) string {
	sum := sha256Bytes([]byte(value))
	return hex.EncodeToString(sum[:])
}
func sha256Bytes(value []byte) [32]byte { return sha256.Sum256(value) }

func (m *Manager) start(ctx context.Context, agent store.ManagedAgentRecord, request LaunchRequest, binding *managedBinding) {
	// Close cancels ctx to stop process and RPC work. This goroutine owns its
	// durable launch transitions, and Close waits for them before returning.
	durableCtx := context.Background()
	lane := binding.lane
	if lane == nil {
		lane = m.laneFor(agent.AgentID)
		binding.lane = lane
	}
	if m.beforeManagedLaunchStart != nil {
		m.beforeManagedLaunchStart(ctx, agent.AgentID)
	}
	lane.serial.Lock()
	launchLaneHeld := true
	defer func() {
		if launchLaneHeld {
			lane.serial.Unlock()
		}
		binding.serial.Lock()
		reaped, retain := binding.reaped, binding.retainBinding
		binding.serial.Unlock()
		if reaped || !retain {
			lane.serial.Lock()
			m.clearBinding(binding)
			lane.serial.Unlock()
		}
	}()
	spec := CommandSpec{PiExecutable: m.config.PiExecutable, SessionRoot: m.config.SessionRoot, SessionID: agent.PiSessionID, LaunchMode: CreateNew, Cwd: agent.Cwd, Provider: agent.SelectedModel.Provider, ModelID: agent.SelectedModel.ID, MechaRPCExtension: m.config.MechaRPCExtension, PiSubagentsExtension: m.config.PiSubagentsExtension, CursorExtension: m.config.CursorExtension, TestOnlyExtensions: m.config.TestOnlyExtensions, Environment: ChildEnvironmentFor(m.config, agent.RuntimeGeneration, agent.SessionEpoch)}
	pending, pendingErr := m.store.ManagedLaunchStillPending(durableCtx, agent.AgentID, request.RequestID)
	if pendingErr != nil {
		m.fail(agent.AgentID, "STATE_UPDATE_FAILED", sanitizeError(pendingErr))
		return
	}
	if !pending {
		// A lifecycle tombstone won before this goroutine reached its only
		// external start boundary. It already terminalized the launch replay row.
		return
	}
	process, err := m.processFactory.Start(ctx, spec)
	if err != nil {
		m.fail(agent.AgentID, "PROCESS_START_FAILED", sanitizeError(err))
		return
	}
	identity := process.Identity()
	if err = ValidateIdentity(identity); err != nil {
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "PROCESS_IDENTITY_INVALID", sanitizeError(err))
		return
	}
	binding.serial.Lock()
	binding.process = process
	binding.identity = identity
	binding.serial.Unlock()
	m.mu.Lock()
	m.processes[agent.AgentID] = process
	m.mu.Unlock()
	waitDone := make(chan error, 1)
	waitSignal := make(chan struct{})
	go func() {
		waitErr := process.Wait()
		// waitSignal publishes waitErr before readiness may classify an EOF.
		// It must not wait on binding.serial because readiness owns that lock
		// while an RPC call can return the EOF that triggered this path.
		binding.waitErr = waitErr
		close(waitSignal)
		waitDone <- waitErr
	}()
	binding.serial.Lock()
	binding.waitDone = waitDone
	binding.waitSignal = waitSignal
	binding.serial.Unlock()
	if err = m.store.UpdateManagedAgentStarting(durableCtx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: identity.PID, StartMarker: identity.StartMarker, Incarnation: identity.Incarnation}, agent.RuntimeGeneration); err != nil {
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "STATE_UPDATE_FAILED", sanitizeError(err))
		return
	}
	rpc, err := m.rpcFactory.Open(ctx, process)
	if err != nil {
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "RPC_OPEN_FAILED", sanitizeError(err))
		return
	}
	binding.serial.Lock()
	binding.rpc = rpc
	binding.serial.Unlock()
	m.mu.Lock()
	m.rpcs[agent.AgentID] = rpc
	m.mu.Unlock()
	fence := bindingFence(agent.AgentID, binding)
	if err = m.readinessGate(ctx, binding, fence); err != nil {
		// Only a result published before cleanup began describes a child exit.
		// A result released by Stop belongs to manager cleanup and remains EOF.
		if errors.Is(err, io.EOF) {
			if waitErr, exited := completedBindingWait(binding); exited {
				_ = rpc.Close() // diagnostic only; RPC closure does not decide ownership.
				m.recordPreReadinessProcessExit(binding, process, identity, fence, request.RequestID, waitErr)
				return
			}
			_ = rpc.Close() // diagnostic only; RPC closure does not decide ownership.
			limit := m.config.Limits.GracefulStop
			if limit <= 0 {
				limit = DefaultLimits().GracefulStop
			}
			stopCtx, cancel := context.WithTimeout(context.Background(), limit)
			stopErr := process.Stop(stopCtx)
			cancel()
			if stopErr == nil {
				if m.store.MarkManagedFailedForFenceAndOperation(context.Background(), fence, request.RequestID, "READINESS_FAILED", sanitizeError(err)) == nil {
					binding.serial.Lock()
					binding.reaped = true
					binding.serial.Unlock()
				} else {
					binding.serial.Lock()
					binding.retainBinding = true
					binding.serial.Unlock()
				}
				return
			} else {
				// Process absence, when proven, still permits the binding to be
				// cleared. An RPC close error is never a stop failure.
				if m.processOps.Probe(identity).State == ProcessAbsent {
					if m.store.MarkManagedFailedForFenceAndOperation(context.Background(), fence, request.RequestID, "READINESS_FAILED", sanitizeError(err)) == nil {
						binding.serial.Lock()
						binding.reaped = true
						binding.serial.Unlock()
					} else {
						binding.serial.Lock()
						binding.retainBinding = true
						binding.serial.Unlock()
					}
					return
				}
				message := "readiness: " + sanitizeError(err) + "; cleanup: " + sanitizeError(stopErr)
				binding.serial.Lock()
				binding.retainBinding = true
				binding.serial.Unlock()
				_ = m.store.MarkManagedFailedForFenceAndOperation(context.Background(), fence, request.RequestID, "PROCESS_STOP_FAILED", message)
				return
			}
		}
		_ = rpc.Close()
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "READINESS_FAILED", sanitizeError(err))
		return
	}
	if waitErr, exited := completedBindingWait(binding); exited {
		_ = rpc.Close() // diagnostic only; RPC closure does not decide ownership.
		m.recordPreReadinessProcessExit(binding, process, identity, fence, request.RequestID, waitErr)
		return
	}
	binding.serial.Lock()
	if !m.liveBindingForFence(durableCtx, binding, fence) {
		binding.serial.Unlock()
		_ = rpc.Close()
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "READINESS_FAILED", "managed runtime binding changed before ready")
		return
	}
	if err = m.store.MarkManagedReadyFromSources(durableCtx, fence); err != nil {
		binding.serial.Unlock()
		_ = rpc.Close()
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "STATE_UPDATE_FAILED", sanitizeError(err))
		return
	}
	// The readiness get_state response is Pi's only authoritative reconnect
	// repair for a parent compaction that began before this router observed its
	// native event. It was captured inside the same serialized readiness gate.
	if binding.parentCompacting != nil {
		if _, reconcileErr := m.store.ReconcileManagedParentCompaction(durableCtx, fence, *binding.parentCompacting); reconcileErr != nil {
			binding.serial.Unlock()
			_ = rpc.Close()
			_ = process.Stop(context.Background())
			m.fail(agent.AgentID, "STATE_UPDATE_FAILED", sanitizeError(reconcileErr))
			return
		}
	}
	binding.serial.Unlock()
	m.installRootControlReceiver(binding)
	m.installChildDeltaReceiver(binding)
	m.installParentCompactionReceiver(binding)
	m.installParentCompactionNotificationReceiver(binding)
	m.installParentActivityReceiver(binding)
	m.installQualifiedGoalActivityReceiver(binding)
	m.installBuiltinToolLifecycleReceiver(binding)
	binding.serial.Lock()
	if !m.liveBindingForFence(durableCtx, binding, fence) {
		binding.serial.Unlock()
		_ = rpc.Close()
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "READINESS_FAILED", "managed runtime binding changed before root control activation")
		return
	}
	if request.InitialInstruction != "" {
		if err = rpc.Prompt(ctx, request.InitialInstruction); err != nil {
			binding.serial.Unlock()
			_ = rpc.Close()
			_ = process.Stop(context.Background())
			m.fail(agent.AgentID, "INITIAL_INSTRUCTION_FAILED", sanitizeError(err))
			return
		}
		_ = m.store.MarkManagedInitialInstructionSent(durableCtx, agent.AgentID)
	}
	_ = m.store.MarkManagedOperationTerminal(durableCtx, request.RequestID)
	binding.lifecycleAction = ""
	binding.serial.Unlock()
	m.recoverManagedDirectAttemptsAsync(agent.AgentID)
	m.recoverManagedGoalProjection(binding)
	// Readiness is the end of launch's synchronous ownership. Process exit and
	// shutdown callbacks reacquire this lane before they inspect or mutate the
	// binding, so termination cannot interleave a late launch callback.
	lane.serial.Unlock()
	launchLaneHeld = false
	select {
	case <-waitSignal:
		lane.serial.Lock()
		defer lane.serial.Unlock()
		waitErr, _ := completedBindingWait(binding)
		m.recordReadyBindingProcessExitInLane(binding, fence, sanitizeError(waitErr))
	case <-ctx.Done():
		// Lifecycle-specific shutdown owners acquire the lane themselves. The
		// generic fallback below acquires it only after they decline ownership.
		handled, shutdownErr := m.shutdownTerminationBinding(context.Background(), binding)
		if handled {
			m.recordCloseError(shutdownErr)
			return
		}
		handled, shutdownErr = m.shutdownLifecycleRestartBinding(context.Background(), binding)
		if handled {
			m.recordCloseError(shutdownErr)
			return
		}
		handled, shutdownErr = m.shutdownRestartBinding(context.Background(), binding)
		if handled {
			m.recordCloseError(shutdownErr)
			return
		}
		lane.serial.Lock()
		defer lane.serial.Unlock()
		_ = rpc.Close()
		if stopErr := process.Stop(context.Background()); stopErr != nil {
			m.fail(agent.AgentID, "PROCESS_STOP_FAILED", sanitizeError(stopErr))
			return
		}
		if err := m.store.MarkManagedStopped(context.Background(), agent.AgentID); err != nil {
			m.fail(agent.AgentID, "STATE_UPDATE_FAILED", sanitizeError(err))
		}
	}
}

// runningBindingCurrent confirms the immutable binding owns the currently
// running durable row without using a process liveness check. It is used only
// after a caller has independently proved a process-exit boundary.
func (m *Manager) runningBindingCurrent(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence) bool {
	if binding == nil || binding.agentID != fence.AgentID || !sameBindingFence(bindingFence(fence.AgentID, binding), fence) {
		return false
	}
	binding.serial.Lock()
	usable := !binding.reaped && binding.process != nil && binding.rpc != nil && binding.process.Identity() == binding.identity
	binding.serial.Unlock()
	if !usable {
		return false
	}
	m.mu.Lock()
	current := m.bindings[fence.AgentID] == binding
	m.mu.Unlock()
	if !current {
		return false
	}
	agent, err := m.store.ManagedAgent(ctx, fence.AgentID)
	return err == nil && agent.DesiredState == "running" && agent.LifecycleState == "running" && sameBindingFence(storedAgentFence(agent), fence)
}

// completedBindingWait reads the wait result only after the one waiter has
// completed. It never calls Process.Wait itself, preserving the single-reap
// ownership boundary during readiness failures.
func completedBindingWait(binding *managedBinding) (error, bool) {
	if binding == nil {
		return nil, false
	}
	binding.serial.Lock()
	waitSignal := binding.waitSignal
	binding.serial.Unlock()
	if waitSignal == nil {
		return nil, false
	}
	select {
	case <-waitSignal:
		return binding.waitErr, true
	default:
		return nil, false
	}
}

// recordPreReadinessProcessExit keeps an exit found before readiness on the
// launch's exact durable fence. The bounded stop also proves that a natural
// leader exit did not leave descendants behind before PROCESS_EXITED is saved.
func (m *Manager) recordPreReadinessProcessExit(binding *managedBinding, process Process, identity ProcessIdentity, fence store.ManagedRuntimeFence, operationID string, waitErr error) {
	diagnostic := ManagedProcessExitDiagnostic(waitErr)
	cleanupErr := m.stopPreReadinessProcess(process, identity)
	if cleanupErr == nil {
		if err := m.store.RecordManagedPreReadinessProcessExit(context.Background(), fence, operationID, diagnostic); err == nil {
			binding.serial.Lock()
			binding.reaped = true
			binding.serial.Unlock()
			m.publishProjection()
			return
		}
		binding.serial.Lock()
		binding.retainBinding = true
		binding.serial.Unlock()
		return
	}
	message := "exit: " + diagnostic + "; cleanup: " + sanitizeError(cleanupErr)
	binding.serial.Lock()
	binding.retainBinding = true
	binding.serial.Unlock()
	if err := m.store.MarkManagedFailedForFenceAndOperation(context.Background(), fence, operationID, "PROCESS_STOP_FAILED", message); err == nil {
		m.publishProjection()
	}
}

// stopPreReadinessProcess uses the same bounded Process.Stop contract as other
// managed cleanup, then checks the injected ownership probe for full-group absence.
func (m *Manager) stopPreReadinessProcess(process Process, identity ProcessIdentity) error {
	limit := m.config.Limits.GracefulStop
	if limit <= 0 {
		limit = DefaultLimits().GracefulStop
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), limit)
	stopErr := process.Stop(stopCtx)
	cancel()
	probe := m.processOps.Probe(identity)
	if probe.State == ProcessAbsent {
		return nil
	}
	if stopErr != nil {
		return stopErr
	}
	return newProcessCleanupError("managed process group remains present after stop", probe.State, nil)
}

// recordReadyBindingProcessExitInLane is shared by the process waiter and the
// control-plane reconciliation path. The lane serializes them, so only the
// first proof of a hard process loss can archive child state, fail the row,
// clear the binding, and notify the UI.
func (m *Manager) recordReadyBindingProcessExitInLane(binding *managedBinding, fence store.ManagedRuntimeFence, message string) {
	if binding == nil || !sameBindingFence(bindingFence(fence.AgentID, binding), fence) {
		return
	}
	binding.serial.Lock()
	terminating := binding.lifecycleAction == "terminate"
	binding.serial.Unlock()
	if terminating {
		// Termination owns the durable tombstone and exact group proof. A
		// cooperative Abort exit must not rewrite it as a generic failure.
		return
	}
	if m.recordLifecycleRestartExit(binding) || m.recordRestartExit(binding) {
		return
	}
	if !m.runningBindingCurrent(context.Background(), binding, fence) {
		return
	}
	if m.isClosed() {
		_ = m.store.MarkManagedStopped(context.Background(), binding.agentID)
		return
	}
	// A hard parent exit cannot carry a trustworthy child terminal event.
	// Reconcile an in-flight native parent compaction before persisting the
	// bounded retry, then archive every live child under this exact fence.
	_, _ = m.store.ReconcileManagedParentCompaction(context.Background(), fence, false)
	m.interruptBuiltinToolInvocations(binding, fence)
	_ = m.store.InterruptManagedParentWork(context.Background(), fence)
	_, _ = m.store.RecordManagedParentProcessLost(context.Background(), fence)
	if _, err := m.store.RecordManagedUnexpectedExit(context.Background(), fence, binding.operationID, message); err != nil {
		// Old focused schemas have no crash-recovery table. Keep their existing
		// terminal behavior while current production schemas use the retry row.
		if errors.Is(err, store.ErrManagedCrashRecoveryUnavailable) {
			_ = m.store.MarkManagedFailed(context.Background(), binding.agentID, "PROCESS_EXITED", message)
		} else {
			// Preserve the exact binding when a current-schema write fails. A
			// later reconciliation can retry without losing the runtime fence.
			binding.serial.Lock()
			binding.retainBinding = true
			binding.serial.Unlock()
			return
		}
	}
	binding.serial.Lock()
	binding.reaped = true
	binding.serial.Unlock()
	m.clearBinding(binding)
	m.publishProjection()
}

func (m *Manager) fail(agentID, code, message string) {
	_ = m.store.MarkManagedFailed(context.Background(), agentID, code, message)
	if operation, err := m.findOperationForAgent(agentID); err == nil && operation != nil {
		_ = m.store.MarkManagedOperationTerminal(context.Background(), operation.OperationID)
	}
}
func (m *Manager) findOperationForAgent(agentID string) (*store.ManagedOperation, error) {
	m.mu.Lock()
	operationID := m.operationIDs[agentID]
	m.mu.Unlock()
	if operationID == "" {
		return nil, nil
	}
	return m.store.ManagedOperation(context.Background(), operationID)
}

func defaultProcessGroupLeaderVerifier(identity ProcessIdentity) bool {
	if ValidateIdentity(identity) != nil {
		return false
	}
	marker, err := processStartMarker(identity.PID)
	if err != nil || marker != identity.StartMarker {
		return false
	}
	groupID, err := syscall.Getpgid(identity.PID)
	return err == nil && groupID == identity.PID
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// startManagedTask closes the WaitGroup admission race with Close. Every
// background task that is not registered while Manager.mu still proves the
// manager open must enter through this boundary.
func (m *Manager) startManagedTask(task func()) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	m.startWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.startWG.Done()
		task()
	}()
	return true
}

func (m *Manager) laneLocked(agentID string) *managedAgentLane {
	lane := m.lanes[agentID]
	if lane == nil {
		lane = &managedAgentLane{}
		m.lanes[agentID] = lane
	}
	return lane
}

func (m *Manager) laneFor(agentID string) *managedAgentLane {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.laneLocked(agentID)
}

func (m *Manager) bindingFor(agentID string) (*managedBinding, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	binding := m.bindings[agentID]
	return binding, !m.closed && binding != nil
}

// clearBinding only removes the runtime maps when this immutable binding still
// owns them. An old exit may arrive after a target has been installed.
func (m *Manager) clearBinding(binding *managedBinding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bindings[binding.agentID] != binding {
		return
	}
	delete(m.processes, binding.agentID)
	delete(m.rpcs, binding.agentID)
	delete(m.operationIDs, binding.agentID)
	delete(m.bindings, binding.agentID)
}

func (m *Manager) installBinding(binding *managedBinding) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	if binding.lane == nil {
		binding.lane = m.laneLocked(binding.agentID)
	}
	m.bindings[binding.agentID] = binding
	m.processes[binding.agentID] = binding.process
	m.rpcs[binding.agentID] = binding.rpc
	m.operationIDs[binding.agentID] = binding.operationID
	m.mu.Unlock()
	return true
}

func bindingFence(agentID string, binding *managedBinding) store.ManagedRuntimeFence {
	if agentID == "" {
		agentID = binding.agentID
	}
	return store.ManagedRuntimeFence{AgentID: agentID, PiSessionID: binding.sessionID, SessionEpoch: binding.sessionEpoch, ProcessPID: binding.identity.PID, ProcessStartMarker: binding.identity.StartMarker, ProcessIncarnation: binding.identity.Incarnation, RuntimeGeneration: binding.runtimeGeneration, ManifestRevision: binding.manifestRevision, ManifestHash: binding.manifestHash}
}

func storedAgentFence(agent *store.ManagedAgentRecord) store.ManagedRuntimeFence {
	if agent == nil {
		return store.ManagedRuntimeFence{}
	}
	return store.ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}
}

func sameBindingFence(left, right store.ManagedRuntimeFence) bool {
	return left.AgentID == right.AgentID && left.PiSessionID == right.PiSessionID && left.SessionEpoch == right.SessionEpoch && left.ProcessPID == right.ProcessPID && left.ProcessStartMarker == right.ProcessStartMarker && left.ProcessIncarnation == right.ProcessIncarnation && left.RuntimeGeneration == right.RuntimeGeneration && left.ManifestRevision == right.ManifestRevision && left.ManifestHash == right.ManifestHash
}

func (m *Manager) liveBinding(agent *store.ManagedAgentRecord, binding *managedBinding) bool {
	if agent == nil || binding.reaped || binding.process == nil || binding.rpc == nil || ValidateIdentity(binding.identity) != nil || !m.groupLeader(binding.identity) {
		return false
	}
	if binding.process.Identity() != binding.identity {
		return false
	}
	fence := bindingFence(agent.AgentID, binding)
	return agent.PiSessionID == fence.PiSessionID && agent.SessionEpoch == fence.SessionEpoch && agent.ProcessPID == fence.ProcessPID && agent.ProcessStartMarker == fence.ProcessStartMarker && agent.ProcessIncarnation == fence.ProcessIncarnation && agent.RuntimeGeneration == fence.RuntimeGeneration && agent.ManifestRevision == fence.ManifestRevision && agent.ManifestHash == fence.ManifestHash
}

func (m *Manager) liveBindingForFence(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence) bool {
	if !sameBindingFence(bindingFence(fence.AgentID, binding), fence) {
		return false
	}
	agent, err := m.store.ManagedAgent(ctx, fence.AgentID)
	return err == nil && m.liveBinding(agent, binding) && sameBindingFence(store.ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}, fence)
}

func replayRestartOperation(request RestartRequest, operation *store.ManagedOperation) (RestartResult, error) {
	if operation.CanonicalPath != request.CanonicalPath || operation.CanonicalBodyHash != request.CanonicalBodyHash {
		return RestartResult{}, &store.ManagedIdempotencyConflictError{}
	}
	return RestartResult{Operation: operation, Replayed: true}, nil
}

// ReserveRestart admits new direct restarts into the lifecycle ledger. Legacy
// 025 transitions remain recoverable, but this path never creates one.
func (m *Manager) ReserveRestart(ctx context.Context, request RestartRequest) (RestartResult, error) {
	if request.AgentID == "" || request.Method == "" || request.CanonicalPath == "" || request.RequestID == "" || request.CanonicalBodyHash == "" {
		return RestartResult{}, errors.New("managed restart request is incomplete")
	}
	if existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); err == nil {
		return replayRestartOperation(request, existing)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return RestartResult{}, err
	}

	lane := m.laneFor(request.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	// Replay is checked again after waiting for the stable agent lane. This is
	// before any binding, process, or generation read.
	if existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); err == nil {
		return replayRestartOperation(request, existing)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return RestartResult{}, err
	}
	agent, err := m.store.ManagedAgent(ctx, request.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return RestartResult{}, &store.ManagedAgentNotFoundError{AgentID: request.AgentID}
	}
	if err != nil {
		return RestartResult{}, err
	}
	binding, ok := m.bindingFor(request.AgentID)
	if !ok {
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	binding.serial.Lock()
	defer binding.serial.Unlock()
	if !m.liveBinding(agent, binding) {
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	selectedModel := agent.SelectedModel
	if request.Model != nil {
		var selected Model
		if selector, ok := m.catalog.(ModelSelector); ok {
			selected, err = selector.Select(ctx, request.Model)
		} else {
			var catalog Catalog
			catalog, err = m.catalog.Catalog(ctx)
			if err == nil {
				selected, err = SelectModel(request.Model, nil, catalog)
			}
		}
		if err != nil {
			return RestartResult{}, err
		}
		selectedModel = store.ManagedModel{Provider: selected.Provider, ID: selected.ID, Name: selected.Name}
	}
	current, err := m.store.ManagedOperation(ctx, binding.operationID)
	if err != nil || current.State != "terminal" || restartActionHigherPriority(binding.lifecycleAction) {
		return RestartResult{}, &store.ManagedLifecycleConflictError{AgentID: request.AgentID}
	}
	targetGeneration, err := m.ids.Next()
	if err != nil {
		return RestartResult{}, err
	}
	responseAgent := *agent
	responseAgent.SelectedModel = selectedModel
	responseAgent.LifecycleState, responseAgent.AcceptingPrompts = "restarting", false
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": PublicAgent(&responseAgent)})
	if err != nil {
		return RestartResult{}, err
	}
	operation := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, ResponseBody: body, CreatedAt: formatManagedTime(m.clock.Now())}
	source := bindingFence(request.AgentID, binding)
	admission, err := m.store.ReserveManagedLifecycleRestart(ctx, store.ManagedLifecycleRestartReservation{AgentID: request.AgentID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, Source: "direct", CurrentOperationID: binding.operationID, SourceFence: source, PlannedTargetRuntimeGeneration: targetGeneration, SelectedModel: selectedModel, Operation: operation})
	if err != nil {
		return RestartResult{}, err
	}
	if admission.Replayed {
		return RestartResult{Operation: admission.Operation, Replayed: true}, nil
	}
	binding.operationID, binding.lifecycleAction = admission.Action.ActionID, "restart"
	m.mu.Lock()
	if m.bindings[request.AgentID] == binding {
		m.operationIDs[request.AgentID] = admission.Action.ActionID
	}
	m.mu.Unlock()
	m.startLifecycleRestart(binding, admission.Action.ActionID, source)
	return RestartResult{Agent: &responseAgent, Operation: admission.Operation}, nil
}

func restartActionHigherPriority(action string) bool {
	switch action {
	case "reset", "resetting", "terminate", "terminating", "stop", "stopping":
		return true
	default:
		return false
	}
}

func (m *Manager) startRestartPreparation(binding *managedBinding, fence store.ManagedRestartFence, operationID string) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	binding.preparationWG.Add(1)
	m.startWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.startWG.Done()
		defer binding.preparationWG.Done()
		m.prepareRestart(binding, fence, operationID)
	}()
}

func (m *Manager) prepareRestart(binding *managedBinding, fence store.ManagedRestartFence, operationID string) {
	if m.preparationHook != nil {
		if err := m.preparationHook(m.runCtx, "reserved"); err != nil {
			if m.runCtx.Err() != nil {
				return
			}
			binding.serial.Lock()
			defer binding.serial.Unlock()
			if m.runCtx.Err() == nil && binding.operationID == operationID && sameBindingFence(bindingFence(fence.AgentID, binding), fence) {
				_ = m.store.RollbackManagedRestartPreparation(context.Background(), fence, operationID, "RESTART_PREPARATION_FAILED", "restart preparation failed")
				binding.lifecycleAction = ""
			}
			return
		}
	}
	binding.serial.Lock()
	rpc := binding.rpc
	binding.serial.Unlock()
	if rpc == nil {
		return
	}
	deadlineCtx, cancel := m.preparationDeadline(m.runCtx, m.config.Limits.GracefulStop)
	if cancel == nil {
		cancel = func() {}
	}
	state, readErr := rpc.GetState(deadlineCtx)
	cancel()
	binding.serial.Lock()
	if m.runCtx.Err() != nil || binding.reaped || binding.operationID != operationID || !sameBindingFence(bindingFence(fence.AgentID, binding), fence) {
		binding.serial.Unlock()
		return
	}
	if readErr != nil || !stateSessionMatches(state, fence.PiSessionID) {
		if err := m.store.RollbackManagedRestartPreparation(context.Background(), fence, operationID, "RESTART_PREPARATION_FAILED", "restart preparation failed"); err == nil {
			binding.lifecycleAction = ""
		}
		binding.serial.Unlock()
		return
	}
	if err := m.store.MarkManagedRestartPrepared(context.Background(), fence, operationID); err != nil {
		binding.serial.Unlock()
		return
	}
	binding.serial.Unlock()
	if m.preparationHook != nil {
		_ = m.preparationHook(m.runCtx, "prepared")
	}
	// Older launch-only RPC doubles remain valid for the closed reservation
	// slice. A real active replacement cannot continue without the narrow
	// binding-scoped settlement capability.
	_, transitionErr := m.store.ManagedRestartTransition(context.Background(), fence.AgentID)
	if transitionErr == nil && supportsRestartSettlement(rpc) {
		m.startRestartExecution(binding, fence, operationID)
	}
}

func supportsRestartSettlement(rpc RPC) bool {
	_, ok := rpc.(RestartTurnSettler)
	return ok
}

func (m *Manager) startRestartExecution(binding *managedBinding, fence store.ManagedRestartFence, operationID string) {
	m.mu.Lock()
	if m.closed || binding.restartExecutionDone {
		m.mu.Unlock()
		return
	}
	binding.restartExecutionDone = true
	m.startWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.startWG.Done()
		m.ExecutePreparedRestart(binding, fence, operationID)
	}()
}

// AdmitPrompt is intentionally internal. It shares restart's serializer so a
// message cannot cross the durable acceptance change into the old Pi process.
// RestartAfterAuthChange reuses the existing per-agent lifecycle owner. It
// never touches tmux state. A pending manual restart already owns the same
// lane and is allowed to finish; it therefore coalesces the auth refresh.
func (m *Manager) RestartAfterAuthChange(ctx context.Context, operationID string) {
	catalog, catalogErr := m.catalog.Catalog(ctx)
	if catalogErr != nil {
		return
	}
	available := func(model store.ManagedModel) bool {
		for _, candidate := range catalog.Models {
			if candidate.Provider == model.Provider && candidate.ID == model.ID && candidate.Available {
				return true
			}
		}
		return false
	}
	agents, err := m.store.ManagedAgents(ctx)
	if err != nil {
		return
	}
	for _, agent := range agents {
		if agent.DesiredState != "running" {
			continue
		}
		if !available(agent.SelectedModel) {
			// An auth refresh must never silently choose a replacement model for an
			// existing session. The durable failure also closes prompt admission.
			_ = m.store.MarkManagedFailed(context.Background(), agent.AgentID, "MODEL_UNAVAILABLE", "selected model is unavailable after provider refresh")
			continue
		}
		requestID, idErr := m.ids.Next()
		if idErr != nil {
			continue
		}
		_, _ = m.ReserveRestart(ctx, RestartRequest{AgentID: agent.AgentID, Method: "POST", CanonicalPath: "/v1/provider-auth/restart/" + operationID, RequestID: requestID, CanonicalBodyHash: hashString("provider-auth:" + operationID)})
	}
}

func (m *Manager) AdmitPrompt(ctx context.Context, agentID, message string) error {
	binding, ok := m.bindingFor(agentID)
	if !ok {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	binding.serial.Lock()
	defer binding.serial.Unlock()
	agent, err := m.store.ManagedAgent(ctx, agentID)
	if err != nil || agent.DesiredState != "running" || agent.LifecycleState != "running" || !agent.AcceptingPrompts || !agent.SlotHeld || !m.liveBinding(agent, binding) {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	if err := binding.rpc.Prompt(ctx, message); err != nil {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	return nil
}

// ExecutePreparedRestart is the single controlled replacement owner. The
// per-agent lane survives a binding replacement, so Close, recovery, and an
// old process exit cannot race a target install or its readiness gate.
func (m *Manager) ExecutePreparedRestart(binding *managedBinding, oldFence store.ManagedRestartFence, operationID string) {
	if binding.lane == nil {
		return
	}
	binding.lane.serial.Lock()
	defer binding.lane.serial.Unlock()
	m.executePreparedRestartInLane(binding, oldFence, operationID)
}

func (m *Manager) executePreparedRestartInLane(binding *managedBinding, oldFence store.ManagedRestartFence, operationID string) {
	transition, err := m.store.ManagedRestartTransition(context.Background(), oldFence.AgentID)
	if err != nil || transition.OperationID != operationID || !sameBindingFence(transition.Fence, oldFence) || transition.Stage != "prepared" {
		return
	}

	binding.serial.Lock()
	if !m.restartBindingCurrent(binding, oldFence, operationID) {
		binding.serial.Unlock()
		return
	}
	oldRPC, oldProcess, oldWait := binding.rpc, binding.process, binding.waitDone
	binding.serial.Unlock()
	if oldRPC == nil || oldProcess == nil || oldWait == nil {
		_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_IDENTITY_UNSAFE", "restart process identity could not be verified")
		return
	}

	if transition.ReservationActivityState == "active" {
		if err := m.store.MarkManagedRestartSettling(context.Background(), oldFence, operationID); err != nil {
			return
		}
		settler, ok := oldRPC.(RestartTurnSettler)
		if !ok {
			_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_SETTLE_FAILED", "restart turn did not settle")
			return
		}
		settleCtx, cancel := context.WithTimeout(m.runCtx, m.config.Limits.GracefulStop)
		err = settler.SettlePreparedRestartTurn(settleCtx, oldFence)
		cancel()
		binding.serial.Lock()
		current := m.restartBindingCurrent(binding, oldFence, operationID)
		binding.serial.Unlock()
		if err != nil || !current || m.store.MarkManagedRestartTurnSettled(context.Background(), oldFence, operationID) != nil {
			_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_SETTLE_FAILED", "restart turn did not settle")
			return
		}
	}

	if err := m.store.MarkManagedRestartStopping(context.Background(), oldFence, operationID); err != nil {
		return
	}
	if m.processOps.Probe(oldFenceIdentity(oldFence)).State != ProcessOwned {
		_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_IDENTITY_UNSAFE", "restart process identity could not be verified")
		return
	}
	if err := m.processOps.CloseProcessInput(oldProcess); err != nil {
		_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_STOP_FAILED", "restart process stop did not complete")
		return
	}
	if !m.reapRestartGroup(m.runCtx, oldFenceIdentity(oldFence), oldWait) {
		_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_STOP_FAILED", "restart process stop did not complete")
		return
	}
	m.interruptBuiltinToolInvocations(binding, oldFence)
	_ = m.store.InterruptManagedParentWork(context.Background(), oldFence)
	if err := m.store.MarkManagedRestartOldStopped(context.Background(), oldFence, operationID); err != nil {
		return
	}
	// The old pipe is closed only after the stopped commit. A continuation that
	// Pi begins after agent_settled stays on this old binding and cannot reach
	// the replacement's readiness or prompt path.
	_ = oldRPC.Close()
	stopped, err := m.store.ManagedRestartTransition(context.Background(), oldFence.AgentID)
	if err == nil {
		m.startRestartTargetInLane(stopped)
	}
}

// startRestartTargetInLane starts exactly one replacement from the retained
// stopped state. Both direct execution and cold recovery use this path.
func (m *Manager) startRestartTargetInLane(transition *store.ManagedRestartTransition) {
	if transition == nil || transition.Stage != "stopped" {
		return
	}
	oldFence, operationID := transition.Fence, transition.OperationID
	current, err := m.store.ManagedRestartTransition(context.Background(), oldFence.AgentID)
	if err != nil || current.OperationID != operationID || current.Stage != "stopped" || !sameBindingFence(current.Fence, oldFence) {
		return
	}
	agent, err := m.store.ManagedAgent(context.Background(), oldFence.AgentID)
	if err != nil || !sameBindingFence(storedAgentFence(agent), oldFence) || agent.ActivityState != "stopped" || agent.LifecycleState != "restarting" || agent.AcceptingPrompts {
		_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_STATE_WRITE_FAILED", "restart state write failed")
		return
	}
	if m.manifest.CanonicalSHA256 != oldFence.ManifestHash || manifestRevision(m.manifest) != oldFence.ManifestRevision {
		_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_REPLACEMENT_FAILED", "replacement process did not become ready")
		return
	}
	spec := CommandSpec{PiExecutable: m.config.PiExecutable, SessionRoot: m.config.SessionRoot, SessionID: oldFence.PiSessionID, LaunchMode: ResumeExisting, Cwd: agent.Cwd, Provider: agent.SelectedModel.Provider, ModelID: agent.SelectedModel.ID, MechaRPCExtension: m.config.MechaRPCExtension, PiSubagentsExtension: m.config.PiSubagentsExtension, CursorExtension: m.config.CursorExtension, TestOnlyExtensions: m.config.TestOnlyExtensions, Environment: ChildEnvironmentFor(m.config, current.TargetRuntimeGeneration, oldFence.SessionEpoch)}
	targetProcess, err := m.processFactory.Start(m.runCtx, spec)
	if err != nil {
		_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_REPLACEMENT_FAILED", "replacement process did not become ready")
		return
	}
	targetIdentity := targetProcess.Identity()
	targetFence := store.ManagedRuntimeFence{AgentID: oldFence.AgentID, PiSessionID: oldFence.PiSessionID, SessionEpoch: oldFence.SessionEpoch, ProcessPID: targetIdentity.PID, ProcessStartMarker: targetIdentity.StartMarker, ProcessIncarnation: targetIdentity.Incarnation, RuntimeGeneration: current.TargetRuntimeGeneration, ManifestRevision: oldFence.ManifestRevision, ManifestHash: oldFence.ManifestHash}
	target := &managedBinding{agentID: oldFence.AgentID, lane: m.laneFor(oldFence.AgentID), token: targetFence.RuntimeGeneration, process: targetProcess, operationID: operationID, lifecycleAction: "restart", identity: targetIdentity, sessionID: targetFence.PiSessionID, sessionEpoch: targetFence.SessionEpoch, runtimeGeneration: targetFence.RuntimeGeneration, manifestRevision: targetFence.ManifestRevision, manifestHash: targetFence.ManifestHash}
	m.watchRestartProcess(target)
	if ValidateIdentity(targetIdentity) != nil || targetIdentity.Incarnation == oldFence.ProcessIncarnation || m.processOps.Probe(targetIdentity).State != ProcessOwned {
		if !m.cleanupUninstalledTarget(targetProcess, targetIdentity, target.waitDone) {
			_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_TARGET_REAP_FAILED", "replacement process cleanup did not complete")
			return
		}
		_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_IDENTITY_UNSAFE", "restart process identity could not be verified")
		return
	}
	if err := m.store.InstallManagedRestartTarget(context.Background(), oldFence, targetFence, operationID); err != nil {
		if !m.cleanupUninstalledTarget(targetProcess, targetIdentity, target.waitDone) {
			_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_TARGET_REAP_FAILED", "replacement process cleanup did not complete")
			return
		}
		_ = m.store.RecordManagedRestartFailure(context.Background(), oldFence, operationID, "RESTART_REPLACEMENT_FAILED", "replacement process did not become ready")
		return
	}
	targetRPC, err := m.rpcFactory.Open(m.runCtx, targetProcess)
	if err != nil {
		m.cleanupInstalledTarget(target, targetFence, operationID)
		return
	}
	target.serial.Lock()
	target.rpc = targetRPC
	target.serial.Unlock()
	if !m.installBinding(target) {
		_ = targetRPC.Close()
		m.cleanupInstalledTarget(target, targetFence, operationID)
		return
	}
	if err := m.readinessGate(m.runCtx, target, targetFence); err != nil {
		_ = targetRPC.Close()
		if m.cleanupInstalledTarget(target, targetFence, operationID) {
			m.clearBinding(target)
		}
		return
	}
	select {
	case <-target.waitSignal:
		_ = targetRPC.Close()
		if m.cleanupInstalledTarget(target, targetFence, operationID) {
			m.clearBinding(target)
		}
		return
	default:
	}
	if err := m.store.CompleteManagedRestartReady(context.Background(), targetFence, operationID); err != nil {
		_ = targetRPC.Close()
		if m.cleanupInstalledTarget(target, targetFence, operationID) {
			m.clearBinding(target)
		}
		return
	}
	m.installRootControlReceiver(target)
	m.installChildDeltaReceiver(target)
	m.installParentCompactionReceiver(target)
	m.installParentCompactionNotificationReceiver(target)
	m.installParentActivityReceiver(target)
	m.installQualifiedGoalActivityReceiver(target)
	m.installBuiltinToolLifecycleReceiver(target)
	target.serial.Lock()
	target.lifecycleAction = ""
	target.serial.Unlock()
	m.recoverManagedDirectAttemptsAsync(target.agentID)
	m.watchReadyRestartTarget(target)
}

func (m *Manager) watchRestartProcess(binding *managedBinding) {
	waitDone := make(chan error, 1)
	waitSignal := make(chan struct{})
	binding.waitDone, binding.waitSignal = waitDone, waitSignal
	go func() {
		waitErr := binding.process.Wait()
		binding.waitErr = waitErr
		close(waitSignal)
		waitDone <- waitErr
	}()
}

func (m *Manager) watchReadyRestartTarget(target *managedBinding) {
	m.startManagedTask(func() {
		select {
		case <-target.waitSignal:
			waitErr, _ := completedBindingWait(target)
			target.serial.Lock()
			lifecycleOwned := target.lifecycleAction != "" || target.reaped
			target.serial.Unlock()
			// A reset/restart reaper owns this exit and advances its exact durable
			// stage. The ordinary ready-target watcher must not overwrite that
			// fenced lifecycle work with an unfenced PROCESS_EXITED failure.
			if !m.isClosed() && !lifecycleOwned {
				_ = m.store.InterruptManagedParentWork(context.Background(), bindingFence(target.agentID, target))
				_ = m.store.MarkManagedFailed(context.Background(), target.agentID, "PROCESS_EXITED", sanitizeError(waitErr))
			}
			m.clearBinding(target)
		case <-m.runCtx.Done():
		}
	})
}

func oldFenceIdentity(fence store.ManagedRuntimeFence) ProcessIdentity {
	return ProcessIdentity{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}
}

func (m *Manager) restartBindingCurrent(binding *managedBinding, fence store.ManagedRuntimeFence, operationID string) bool {
	return m.restartBindingMatches(binding, fence, operationID, false)
}

// restartBindingMatches keeps the immutable binding/fence check during Close.
// Shutdown cancels runCtx before it settles an active source, but that must not
// turn its still-owned lifecycle action into a stale callback.
func (m *Manager) restartBindingMatches(binding *managedBinding, fence store.ManagedRuntimeFence, operationID string, allowClosed bool) bool {
	if binding.reaped || binding.operationID != operationID || binding.process == nil || binding.rpc == nil || !sameBindingFence(bindingFence(fence.AgentID, binding), fence) {
		return false
	}
	m.mu.Lock()
	current := m.bindings[fence.AgentID] == binding && (allowClosed || !m.closed)
	m.mu.Unlock()
	return current
}

// reapRestartGroup is the only process-group reaper used by execution,
// shutdown, and cold recovery. A leader exit is insufficient: the entire
// owned group must be absent before the durable stage can advance.
func (m *Manager) reapRestartGroup(parent context.Context, identity ProcessIdentity, waitDone <-chan error) bool {
	if parent == nil {
		parent = context.Background()
	}
	if m.processOps.Probe(identity).State == ProcessAbsent {
		return true
	}
	graceful, cancel := context.WithTimeout(parent, m.config.Limits.GracefulStop)
	probe := m.processOps.WaitForProcessGroupEmpty(graceful, identity, waitDone)
	cancel()
	if probe.State == ProcessAbsent {
		return true
	}
	// This local proof belongs to this reaping attempt. It is the only
	// authorization that permits a leaderless continuation after TERM.
	termSentUnderOwnedProof := false
	if m.processOps.Probe(identity).State != ProcessOwned || m.processOps.SignalVerifiedProcessGroup(identity, syscall.SIGTERM) != nil {
		return false
	}
	termSentUnderOwnedProof = true
	term, cancel := context.WithTimeout(context.Background(), m.config.Limits.TermStop)
	probe = m.processOps.WaitForProcessGroupEmpty(term, identity, waitDone)
	cancel()
	if probe.State == ProcessAbsent {
		return true
	}
	if !termSentUnderOwnedProof || probe.State != ProcessPresent || m.processOps.SignalVerifiedProcessGroupAfterLeaderExit(identity) != nil {
		return false
	}
	kill, cancel := context.WithTimeout(context.Background(), m.config.Limits.TermStop)
	probe = m.processOps.WaitForProcessGroupEmpty(kill, identity, waitDone)
	cancel()
	return probe.State == ProcessAbsent
}

// cleanupUninstalledTarget does not report success until the owned group is
// gone. There is no durable target fence before installation, so returning
// early here would make a failed install orphan a child process.
func (m *Manager) cleanupUninstalledTarget(process Process, identity ProcessIdentity, waitDone <-chan error) bool {
	if process == nil {
		return false
	}
	probe := m.processOps.Probe(identity)
	if probe.State == ProcessAbsent {
		return true
	}
	if probe.State != ProcessOwned {
		return false
	}
	if err := m.processOps.CloseProcessInput(process); err != nil {
		return false
	}
	return m.reapRestartGroup(context.Background(), identity, waitDone)
}

// cleanupInstalledTarget retains the target fence until a controlled reaper
// proves the group absent. Only then may the store restore the old stopped
// fence so a later recovery owner can create one replacement.
func (m *Manager) cleanupInstalledTarget(binding *managedBinding, fence store.ManagedRuntimeFence, operationID string) bool {
	if binding == nil || !m.cleanupUninstalledTarget(binding.process, binding.identity, binding.waitDone) {
		_ = m.store.RecordManagedRestartFailure(context.Background(), fence, operationID, "RESTART_TARGET_REAP_FAILED", "replacement process cleanup did not complete")
		return false
	}
	if err := m.store.ResetManagedRestartTargetToStopped(context.Background(), fence, operationID, "RESTART_REPLACEMENT_FAILED", "replacement process did not become ready"); err != nil {
		_ = m.store.RecordManagedRestartFailure(context.Background(), fence, operationID, "RESTART_TARGET_REAP_FAILED", "replacement process cleanup did not complete")
		return false
	}
	return true
}

// shutdownRestartBinding arbitrates Close with restart execution through the
// persistent lane. It never calls Process.Stop: every owned group follows the
// same fenced reaper as execution and recovery.
func (m *Manager) shutdownRestartBinding(ctx context.Context, binding *managedBinding) (bool, error) {
	binding.preparationWG.Wait()
	if binding.lane == nil {
		return false, nil
	}
	binding.lane.serial.Lock()
	defer binding.lane.serial.Unlock()
	binding.serial.Lock()
	if binding.restartShutdownDone {
		err := binding.restartShutdownError
		binding.serial.Unlock()
		return true, err
	}
	if binding.lifecycleAction != "restart" {
		binding.serial.Unlock()
		return false, nil
	}
	fence, process, rpc, waitDone := bindingFence(binding.agentID, binding), binding.process, binding.rpc, binding.waitDone
	binding.serial.Unlock()
	finish := func(err error) (bool, error) {
		binding.serial.Lock()
		binding.restartShutdownDone = true
		binding.restartShutdownError = err
		binding.serial.Unlock()
		return true, err
	}
	transition, err := m.store.ManagedRestartTransition(context.Background(), binding.agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || transition.OperationID != binding.operationID {
		return finish(err)
	}
	if transition.TargetFence != nil && sameBindingFence(*transition.TargetFence, fence) && transition.Stage == "starting" {
		if process == nil || process.Identity() != binding.identity || !m.reapRestartGroup(context.Background(), oldFenceIdentity(fence), waitDone) {
			return finish(m.store.RecordManagedRestartFailure(context.Background(), fence, transition.OperationID, "RESTART_TARGET_REAP_FAILED", "replacement process cleanup did not complete"))
		}
		if rpc != nil {
			_ = rpc.Close()
		}
		binding.serial.Lock()
		binding.reaped = true
		binding.serial.Unlock()
		err := m.store.ResetManagedRestartTargetToStopped(context.Background(), fence, transition.OperationID, "RESTART_REPLACEMENT_FAILED", "replacement process did not become ready")
		if err == nil {
			m.clearBinding(binding)
		}
		return finish(err)
	}
	if !sameBindingFence(transition.Fence, fence) {
		// A target cleanup or replacement handoff already removed this immutable
		// binding. It must never fall through to generic raw Process.Stop.
		return finish(nil)
	}
	if process == nil || process.Identity() != binding.identity {
		return finish(&store.ManagedRestartStaleError{AgentID: binding.agentID})
	}
	if transition.Stage == "reserved" || transition.Stage == "prepared" || transition.Stage == "settling" {
		if err := m.store.MarkManagedRestartStoppingForShutdown(context.Background(), transition.Fence, transition.OperationID); err != nil {
			return finish(err)
		}
		transition.Stage = "stopping"
	}
	if transition.Stage == "stopping" {
		if m.processOps.Probe(oldFenceIdentity(fence)).State == ProcessOwned {
			if err := m.processOps.CloseProcessInput(process); err != nil {
				return finish(m.store.RecordManagedRestartFailure(context.Background(), fence, transition.OperationID, "RESTART_STOP_FAILED", "restart process stop did not complete"))
			}
		}
		if !m.reapRestartGroup(context.Background(), oldFenceIdentity(fence), waitDone) {
			return finish(m.store.RecordManagedRestartFailure(context.Background(), fence, transition.OperationID, "RESTART_STOP_FAILED", "restart process stop did not complete"))
		}
		m.interruptBuiltinToolInvocations(binding, fence)
		_ = m.store.InterruptManagedParentWork(context.Background(), fence)
		if err := m.store.MarkManagedRestartOldStopped(context.Background(), fence, transition.OperationID); err != nil {
			return finish(err)
		}
	}
	if transition.Stage != "stopped" && transition.Stage != "stopping" {
		return finish(&store.ManagedRestartStaleError{AgentID: binding.agentID})
	}
	if rpc != nil {
		_ = rpc.Close()
	}
	binding.serial.Lock()
	binding.reaped = true
	binding.serial.Unlock()
	m.clearBinding(binding)
	return finish(nil)
}

// recordRestartExit leaves restart transitions to their lane owner. A leader
// exit alone does not prove the group is empty, so this callback cannot write a
// stopped stage or race a target install.
func (m *Manager) recordRestartExit(binding *managedBinding) bool {
	binding.serial.Lock()
	defer binding.serial.Unlock()
	m.mu.Lock()
	current := m.bindings[binding.agentID] == binding
	m.mu.Unlock()
	if !current {
		return true
	}
	// Reset deliberately stops the source before its replacement exists. Its
	// process watcher must not turn that expected exit into PROCESS_EXITED while
	// the reset lane still owns the durable lifecycle action.
	if binding.lifecycleAction == "reset" {
		return true
	}
	if binding.lifecycleAction != "restart" {
		return false
	}
	transition, err := m.store.ManagedRestartTransition(context.Background(), binding.agentID)
	if err != nil || transition.OperationID != binding.operationID {
		return true
	}
	fence := bindingFence(binding.agentID, binding)
	if sameBindingFence(transition.Fence, fence) || (transition.TargetFence != nil && sameBindingFence(*transition.TargetFence, fence)) {
		binding.reaped = true
		return true
	}
	return true
}

func sanitizeError(err error) string {
	if err == nil {
		return "managed Pi exited before readiness"
	}
	return RedactedStderr(err.Error())
}

var managedNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var hiddenCommands = []string{"mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1", "mecha-post-compaction-notification-v1"}
var legacyCommands = []string{"goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"}
var fixedSubagentsTools = []string{"subagent", "get_subagent_result", "steer_subagent"}
var fixedManagedCodingTools = []string{"read", "bash", "edit", "write"}

func expectedManagedActiveTools(manifest Manifest) []string {
	result := append([]string{}, fixedManagedCodingTools...)
	result = append(result, fixedSubagentsTools...)
	for _, tool := range manifest.Tools {
		result = append(result, tool.Name)
	}
	sort.Strings(result)
	return result
}

func readinessGate(ctx context.Context, rpc RPC, manifest Manifest, sessionID string, epoch int64, generation string, restoredRequestIDs []any) error {
	_, err := readinessGateState(ctx, rpc, manifest, sessionID, epoch, generation, restoredRequestIDs)
	return err
}

// readinessGateState keeps the initial authoritative get_state response for
// the immediate post-ready parent-compaction reconciliation. The compatibility
// wrapper above preserves the existing Phase 1 readiness contract.
func readinessGateState(ctx context.Context, rpc RPC, manifest Manifest, sessionID string, epoch int64, generation string, restoredRequestIDs []any) (map[string]any, error) {
	state, err := rpc.GetState(ctx)
	if err != nil {
		return nil, err
	}
	if !stateSessionMatches(state, sessionID) {
		return nil, errors.New("Pi session identity does not match managed record")
	}
	commands, err := rpc.GetCommands(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireManagedCommands(commands); err != nil {
		return nil, err
	}
	envelope := map[string]any{"protocol": "mecha.bootstrap/v1", "delivery_id": generation, "runtime_generation": generation, "session_epoch": epoch, "desired_manifest": manifest.Raw, "manifest_sha256": manifest.CanonicalSHA256, "restored_request_ids": restoredRequestIDs, "inline_policy": map[string]any{"enabled": false, "credits": map[string]any{}}, "recall_projection": map[string]any{"content": ""}}
	if err := rpc.Bootstrap(ctx, envelope); err != nil {
		return nil, err
	}
	if err := rpc.WaitForReceipt(ctx, generation, manifest.CanonicalSHA256, manifest.Revision, epoch, "mecha.tools.accepted.v1"); err != nil {
		return nil, err
	}
	active, err := rpc.GetActiveTools(ctx)
	if err != nil {
		return nil, err
	}
	want := expectedManagedActiveTools(manifest)
	sort.Strings(active)
	if len(active) != len(want) {
		return nil, errors.New("Pi active tool set does not match manifest")
	}
	for i := range want {
		if active[i] != want[i] {
			return nil, errors.New("Pi active tool set does not match manifest")
		}
	}
	return state, nil
}

// readinessGate owns the serialized source confirmation phase. The fixed
// Phase 1 gates stay in the compatibility helper above; this method keeps the
// binding lock from the first durable/live check through both source commits.
func (m *Manager) readinessGate(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence) error {
	binding.serial.Lock()
	defer binding.serial.Unlock()
	if !m.liveBindingForFence(ctx, binding, fence) {
		return errors.New("managed runtime binding is not live")
	}
	restorable, err := m.store.ListRestorableManagedControlRequests(ctx, fence)
	if err != nil {
		return err
	}
	restoredRequestIDs := make([]any, 0, len(restorable))
	for _, request := range restorable {
		restoredRequestIDs = append(restoredRequestIDs, request.RequestID)
	}
	state, err := readinessGateState(ctx, binding.rpc, m.manifest, fence.PiSessionID, fence.SessionEpoch, fence.RuntimeGeneration, restoredRequestIDs)
	if err != nil {
		return err
	}
	if compacting, present := state["isCompacting"].(bool); present {
		binding.parentCompacting = &compacting
	} else {
		binding.parentCompacting = nil
	}

	sourceRPC, ok := binding.rpc.(readinessSourceRPC)
	if !ok {
		return errors.New("managed RPC does not support readiness sources")
	}
	if !m.liveBindingForFence(ctx, binding, fence) {
		return errors.New("managed runtime binding changed before policy")
	}
	desired, err := m.store.LoadManagedDesiredPolicy(ctx, fence.AgentID)
	if err != nil {
		return err
	}
	desiredValue, err := readinessPolicyValue(desired)
	if err != nil {
		return err
	}
	policyEnvelope := map[string]any{"protocol": "mecha.policy/v1", "policy": desiredValue, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "manifest_sha256": fence.ManifestHash}
	policyResponse, err := sourceRPC.ApplyPolicy(ctx, policyEnvelope)
	if err != nil {
		return err
	}
	// Installed Pi acknowledges prompt submission before the hidden command has
	// a return payload. Fakes and future protocol versions can expose that
	// payload directly; when present it must match the desired projection.
	if !privateCommandAcknowledgement(policyResponse) {
		responsePolicy, err := normalizePolicyResponse(policyResponse)
		if err != nil {
			return err
		}
		responseCanonical, responseHash, err := HashReadinessCanonicalJSON(responsePolicy)
		if err != nil || string(responseCanonical) != string(desired.Canonical) || responseHash != desired.SHA256 {
			return errors.New("policy command response does not match desired policy")
		}
	}
	policyReceipt, err := sourceRPC.WaitForPolicyApplied(ctx, fence.RuntimeGeneration, fence.SessionEpoch)
	if err != nil {
		return err
	}
	receiptPolicy, err := normalizePolicyReceiptEnvelope(policyReceipt, fence)
	if err != nil {
		return err
	}
	receiptCanonical, receiptHash, err := HashReadinessCanonicalJSON(receiptPolicy)
	if err != nil || string(receiptCanonical) != string(desired.Canonical) || receiptHash != desired.SHA256 {
		return errors.New("policy receipt does not match desired policy")
	}
	if !m.liveBindingForFence(ctx, binding, fence) {
		return errors.New("managed runtime binding changed before policy confirmation")
	}
	if err := m.store.ConfirmManagedPolicyApplied(context.Background(), fence, store.ManagedDesiredPolicy{Canonical: receiptCanonical, SHA256: receiptHash}); err != nil {
		return err
	}

	if !m.liveBindingForFence(ctx, binding, fence) {
		return errors.New("managed runtime binding changed before child snapshot")
	}
	snapshotEnvelope := map[string]any{"protocol": "mecha.child-snapshot/v1", "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "manifest_sha256": fence.ManifestHash}
	snapshotResponse, err := sourceRPC.RequestChildSnapshot(ctx, snapshotEnvelope)
	if err != nil {
		return err
	}
	var responseSnapshot map[string]any
	var responseSnapshotCanonical []byte
	var responseSnapshotHash string
	if !privateCommandAcknowledgement(snapshotResponse) {
		responseSnapshot, err = normalizeChildSnapshotReceipt(snapshotResponse)
		if err != nil {
			return err
		}
		responseSnapshotCanonical, responseSnapshotHash, err = HashReadinessCanonicalJSON(responseSnapshot)
		if err != nil {
			return err
		}
	}
	snapshotReceipt, err := sourceRPC.WaitForChildSnapshot(ctx)
	if err != nil {
		return err
	}
	receiptSnapshot, err := normalizeChildSnapshotReceipt(snapshotReceipt)
	if err != nil {
		return err
	}
	receiptSnapshotCanonical, receiptSnapshotHash, err := HashReadinessCanonicalJSON(receiptSnapshot)
	if err != nil {
		return err
	}
	if receiptSnapshot["owner_session_id"] != fence.PiSessionID {
		return errors.New("child snapshot owner does not match managed session")
	}
	if responseSnapshot != nil && (receiptSnapshot["snapshot_id"] != responseSnapshot["snapshot_id"] || string(receiptSnapshotCanonical) != string(responseSnapshotCanonical) || receiptSnapshotHash != responseSnapshotHash) {
		return errors.New("child snapshot response and receipt do not match")
	}
	if !m.liveBindingForFence(ctx, binding, fence) {
		return errors.New("managed runtime binding changed before child snapshot confirmation")
	}
	sequence, ok := readinessInteger(receiptSnapshot["sequence"])
	if !ok {
		return errors.New("child snapshot sequence is invalid")
	}
	if err := m.store.ConfirmManagedChildSnapshot(context.Background(), fence, store.ManagedChildSnapshot{SnapshotID: receiptSnapshot["snapshot_id"].(string), OwnerSessionID: receiptSnapshot["owner_session_id"].(string), Sequence: sequence, Canonical: receiptSnapshotCanonical, SHA256: receiptSnapshotHash}); err != nil {
		return err
	}
	return nil
}

func readinessPolicyValue(policy store.ManagedDesiredPolicy) (map[string]any, error) {
	value, err := parseReadinessJSON(policy.Canonical)
	if err != nil {
		return nil, err
	}
	return normalizeDesiredPolicy(value)
}
func privateCommandAcknowledgement(value map[string]any) bool {
	return value["success"] == true && value["command"] == "prompt" && len(value) <= 4
}
func normalizePolicyResponse(value map[string]any) (map[string]any, error) {
	if _, present := value["inline_enabled"]; present {
		return normalizeAppliedPolicyReceipt(value)
	}
	return normalizeAppliedPolicyReceipt(map[string]any{
		"inline_enabled": value["inlineEnabled"], "inline_credits": value["inlineCredits"], "recall_content": value["recallContent"],
		"settled_enabled": value["settledEnabled"], "goal_lock_enabled": value["goalLockEnabled"], "compaction_deferred": value["compactionDeferred"],
	})
}
func normalizePolicyReceiptEnvelope(value map[string]any, fence store.ManagedRuntimeFence) (map[string]any, error) {
	if value["protocol"] != "mecha.policy/v1" || value["runtime_generation"] != fence.RuntimeGeneration || !sameEpoch(value["session_epoch"], fence.SessionEpoch) {
		return nil, errors.New("policy receipt does not match runtime")
	}
	return normalizeAppliedPolicyReceipt(value["applied"])
}

func stateSessionMatches(state map[string]any, want string) bool {
	for _, key := range []string{"session_id", "sessionId", "session"} {
		if value, ok := state[key].(string); ok {
			return value == want
		}
	}
	return want == ""
}
func managedCommandSet() map[string]struct{} {
	wanted := append(append([]string(nil), hiddenCommands...), legacyCommands...)
	wantedSet := map[string]struct{}{}
	for _, value := range wanted {
		wantedSet[value] = struct{}{}
	}
	return wantedSet
}

func validateManagedCommandNames(commands []string) error {
	wantedSet := managedCommandSet()
	counts := map[string]int{}
	for _, value := range commands {
		// Check the raw name first. A normalized view is only useful for
		// identifying a collision; it must never make a non-canonical managed
		// command look like the required exact name.
		if _, canonical := wantedSet[value]; canonical {
			counts[value]++
			if counts[value] > 1 {
				return fmt.Errorf("managed command inventory requires exactly one %s", value)
			}
			continue
		}
		normalized := normalizeCommandName(value)
		if _, managedName := wantedSet[normalized]; managedName {
			return fmt.Errorf("managed command inventory contains non-canonical command %q", value)
		}
	}
	return nil
}

func requireManagedCommands(commands []string) error {
	if err := validateManagedCommandNames(commands); err != nil {
		return err
	}
	wanted := append(append([]string(nil), hiddenCommands...), legacyCommands...)
	wantedSet := managedCommandSet()
	counts := map[string]int{}
	for _, value := range commands {
		if _, managedName := wantedSet[value]; managedName {
			counts[value]++
		}
	}
	for _, want := range wanted {
		if counts[want] != 1 {
			return fmt.Errorf("managed command inventory requires exactly one %s", want)
		}
	}
	return nil
}
func normalizeCommandName(value string) string {
	value = strings.TrimPrefix(value, "/")
	if index := strings.LastIndex(value, ":"); index >= 0 {
		suffix := value[index+1:]
		if suffix != "" {
			all := true
			for _, char := range suffix {
				if char < '0' || char > '9' {
					all = false
				}
			}
			if all {
				value = value[:index]
			}
		}
	}
	return value
}

// RecoverManagedRestartTransitions continues retained work through the same
// per-agent lane as direct execution. It never attaches to a pre-restart RPC
// pipe; live prepared turns remain retained until their original owner can
// settle them, while absent or planned groups can continue safely.
func (m *Manager) RecoverManagedRestartTransitions(ctx context.Context) error {
	transitions, err := m.store.ListManagedRestartTransitions(ctx)
	if err != nil {
		return err
	}
	for _, listed := range transitions {
		lane := m.laneFor(listed.Fence.AgentID)
		lane.serial.Lock()
		m.recoverManagedRestartTransitionInLane(ctx, listed.Fence.AgentID)
		lane.serial.Unlock()
	}
	return nil
}

func (m *Manager) recoverManagedRestartTransitionInLane(ctx context.Context, agentID string) {
	transition, err := m.store.RecoverableManagedRestartTransition(ctx, agentID)
	if err != nil {
		return
	}
	switch transition.Stage {
	case "reserved":
		// Preparation owns reservation completion. A router that stopped before
		// preparation leaves this bounded record for its normal retry path.
		return
	case "prepared", "settling":
		probe := m.processOps.Probe(oldFenceIdentity(transition.Fence))
		switch probe.State {
		case ProcessAbsent:
			_ = m.store.InterruptManagedParentWork(context.Background(), transition.Fence)
			if m.store.RecoverManagedRestartOldGone(context.Background(), transition.Fence, transition.OperationID) == nil {
				stopped, readErr := m.store.ManagedRestartTransition(context.Background(), agentID)
				if readErr == nil {
					m.startRestartTargetInLane(stopped)
				}
			}
		case ProcessOwned, ProcessPresent:
			_ = m.store.RecordManagedRestartFailure(context.Background(), transition.Fence, transition.OperationID, "RESTART_RECOVERY_NEEDS_SETTLE", "restart requires a live turn settle")
		case ProcessMismatch, ProcessUnknown:
			_ = m.store.RecordManagedRestartFailure(context.Background(), transition.Fence, transition.OperationID, "RESTART_IDENTITY_UNSAFE", "restart process identity could not be verified")
		}
	case "stopping":
		if m.reapRestartGroup(context.Background(), oldFenceIdentity(transition.Fence), nil) {
			_ = m.store.InterruptManagedParentWork(context.Background(), transition.Fence)
			if m.store.MarkManagedRestartOldStopped(context.Background(), transition.Fence, transition.OperationID) == nil {
				stopped, readErr := m.store.ManagedRestartTransition(context.Background(), agentID)
				if readErr == nil {
					m.startRestartTargetInLane(stopped)
				}
			}
		} else {
			_ = m.store.RecordManagedRestartFailure(context.Background(), transition.Fence, transition.OperationID, "RESTART_STOP_FAILED", "restart process stop did not complete")
		}
	case "stopped":
		m.startRestartTargetInLane(transition)
	case "starting":
		if transition.TargetFence == nil {
			return
		}
		probe := m.processOps.Probe(oldFenceIdentity(*transition.TargetFence))
		switch probe.State {
		case ProcessAbsent:
			if m.store.ResetManagedRestartTargetToStopped(context.Background(), *transition.TargetFence, transition.OperationID, "RESTART_REPLACEMENT_FAILED", "replacement process did not become ready") == nil {
				stopped, readErr := m.store.ManagedRestartTransition(context.Background(), agentID)
				if readErr == nil {
					m.startRestartTargetInLane(stopped)
				}
			}
		case ProcessOwned:
			if m.reapRestartGroup(context.Background(), oldFenceIdentity(*transition.TargetFence), nil) {
				if m.store.ResetManagedRestartTargetToStopped(context.Background(), *transition.TargetFence, transition.OperationID, "RESTART_REPLACEMENT_FAILED", "replacement process did not become ready") == nil {
					stopped, readErr := m.store.ManagedRestartTransition(context.Background(), agentID)
					if readErr == nil {
						m.startRestartTargetInLane(stopped)
					}
				}
			} else {
				_ = m.store.RecordManagedRestartFailure(context.Background(), *transition.TargetFence, transition.OperationID, "RESTART_TARGET_REAP_FAILED", "replacement process cleanup did not complete")
			}
		case ProcessMismatch, ProcessUnknown, ProcessPresent:
			_ = m.store.RecordManagedRestartFailure(context.Background(), *transition.TargetFence, transition.OperationID, "RESTART_TARGET_REAP_FAILED", "replacement process cleanup did not complete")
		}
	}
}

const managedDirectPersistenceRetry = 50 * time.Millisecond

// OfferDirect wakes the managed agent's existing lane. Direct mailbox delivery
// has no WebSocket registration, lease, tmux frame, or shared batch worker.
func (m *Manager) OfferDirect(agentID string) {
	if agentID == "" {
		return
	}
	lane := m.laneFor(agentID)
	m.startManagedTask(func() {
		lane.serial.Lock()
		defer lane.serial.Unlock()
		m.dispatchDirectInLane(agentID)
	})
}

func (m *Manager) recordManagedDeliveryFailure(attempt store.ManagedDirectAttempt, code string) {
	entry, err := m.store.RecordManagedCompleteHistoryWithPresentation(context.Background(), attempt.Fence.AgentID, "managed_delivery", "error", "Message delivery failed", "managed-delivery:"+attempt.MessageID+":"+fmt.Sprint(attempt.AttemptNumber)+":"+code, &store.ManagedHistoryPresentation{Kind: "error", Summary: "The message could not be delivered.", Error: &store.ManagedHistoryError{Code: code, Retryable: false, DetailAvailable: false}})
	if err == nil {
		m.publishCompleteHistory(entry)
	}
}

// releaseManagedDirect records an unsuccessful pre-acknowledgment delivery.
// A transient SQLite failure must not leave the active attempt blocking every
// later allocation, so it is retried until it reaches released or failed.
func (m *Manager) releaseManagedDirect(attempt store.ManagedDirectAttempt, code, detail string) {
	terminal, err := m.store.ReleaseManagedDirect(context.Background(), attempt, code, detail)
	if err == nil {
		m.recordManagedDeliveryFailure(attempt, code)
		if !terminal {
			m.scheduleDirectRetry(attempt.Fence.AgentID)
		}
		return
	}
	var inactive *store.ManagedDirectNotActiveError
	if errors.As(err, &inactive) {
		return
	}
	var notReady *store.ManagedDirectNotReadyError
	if errors.As(err, &notReady) {
		m.releaseManagedDirectRecovery(attempt, code)
		return
	}
	_ = m.store.RecordManagedDirectDiagnostic(context.Background(), attempt.Fence.AgentID, attempt.MessageID, attempt.AttemptNumber, "release_persistence_failed", "")
	m.retryManagedDirectRelease(attempt, code, detail, false)
}

// releaseManagedDirectRecovery uses immutable target ownership after a
// replacement changes the original runtime fence. It has the same retry rule
// as the live path: a persistence error cannot retain an active attempt.
func (m *Manager) releaseManagedDirectRecovery(attempt store.ManagedDirectAttempt, code string) {
	terminal, err := m.store.ReleaseManagedDirectRecovery(context.Background(), attempt, code)
	if err == nil {
		m.recordManagedDeliveryFailure(attempt, code)
		if !terminal {
			m.scheduleDirectRetry(attempt.Fence.AgentID)
		}
		return
	}
	var inactive *store.ManagedDirectNotActiveError
	if errors.As(err, &inactive) {
		return
	}
	_ = m.store.RecordManagedDirectDiagnostic(context.Background(), attempt.Fence.AgentID, attempt.MessageID, attempt.AttemptNumber, "release_persistence_failed", "")
	m.retryManagedDirectRelease(attempt, code, "", true)
}

func (m *Manager) retryManagedDirectRelease(attempt store.ManagedDirectAttempt, code, detail string, recovery bool) {
	m.startManagedTask(func() {
		for {
			timer := time.NewTimer(managedDirectPersistenceRetry)
			select {
			case <-timer.C:
			case <-m.runCtx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}

			var terminal bool
			var err error
			if recovery {
				terminal, err = m.store.ReleaseManagedDirectRecovery(context.Background(), attempt, code)
			} else {
				terminal, err = m.store.ReleaseManagedDirect(context.Background(), attempt, code, detail)
			}
			if err == nil {
				m.recordManagedDeliveryFailure(attempt, code)
				if !terminal {
					m.scheduleDirectRetry(attempt.Fence.AgentID)
				}
				return
			}
			var inactive *store.ManagedDirectNotActiveError
			if errors.As(err, &inactive) {
				return
			}
			var notReady *store.ManagedDirectNotReadyError
			if !recovery && errors.As(err, &notReady) {
				recovery = true
				continue
			}
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), attempt.Fence.AgentID, attempt.MessageID, attempt.AttemptNumber, "release_persistence_failed", "")
		}
	})
}

// acknowledgeManagedDirect retries the durable settlement after an exact
// append receipt. Releasing at this point would risk another write, so retries
// preserve the receipt identity until the original ordinary row is settled.
func (m *Manager) acknowledgeManagedDirect(attempt store.ManagedDirectAttempt, receiptEntry string) bool {
	err := m.store.AcknowledgeManagedDirect(context.Background(), attempt, receiptEntry, true)
	if err == nil {
		return true
	}
	var inactive *store.ManagedDirectNotActiveError
	if errors.As(err, &inactive) {
		return false
	}
	var notReady *store.ManagedDirectNotReadyError
	if errors.As(err, &notReady) {
		m.acknowledgeManagedDirectRecovered(attempt, receiptEntry)
		return false
	}
	_ = m.store.RecordManagedDirectDiagnostic(context.Background(), attempt.Fence.AgentID, attempt.MessageID, attempt.AttemptNumber, "receipt_settlement_failed", "")
	m.retryManagedDirectAcknowledgment(attempt, receiptEntry, false)
	return false
}

func (m *Manager) acknowledgeManagedDirectRecovered(attempt store.ManagedDirectAttempt, receiptEntry string) bool {
	err := m.store.AcknowledgeManagedDirectRecovered(context.Background(), attempt, receiptEntry)
	if err == nil {
		return true
	}
	var inactive *store.ManagedDirectNotActiveError
	if errors.As(err, &inactive) {
		return false
	}
	_ = m.store.RecordManagedDirectDiagnostic(context.Background(), attempt.Fence.AgentID, attempt.MessageID, attempt.AttemptNumber, "recovery_settlement_failed", "")
	m.retryManagedDirectAcknowledgment(attempt, receiptEntry, true)
	return false
}

func (m *Manager) retryManagedDirectAcknowledgment(attempt store.ManagedDirectAttempt, receiptEntry string, recovered bool) {
	m.startManagedTask(func() {
		for {
			timer := time.NewTimer(managedDirectPersistenceRetry)
			select {
			case <-timer.C:
			case <-m.runCtx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}

			var err error
			if recovered {
				err = m.store.AcknowledgeManagedDirectRecovered(context.Background(), attempt, receiptEntry)
			} else {
				err = m.store.AcknowledgeManagedDirect(context.Background(), attempt, receiptEntry, true)
			}
			if err == nil {
				m.OfferDirect(attempt.Fence.AgentID)
				return
			}
			var inactive *store.ManagedDirectNotActiveError
			if errors.As(err, &inactive) {
				return
			}
			var notReady *store.ManagedDirectNotReadyError
			if !recovered && errors.As(err, &notReady) {
				recovered = true
				continue
			}
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), attempt.Fence.AgentID, attempt.MessageID, attempt.AttemptNumber, "receipt_settlement_failed", "")
		}
	})
}

func (m *Manager) dispatchDirectInLane(agentID string) {
	for {
		binding, ok := m.bindingFor(agentID)
		if !ok {
			return
		}
		binding.serial.Lock()
		agent, err := m.store.ManagedAgent(context.Background(), agentID)
		if err != nil || !m.liveBinding(agent, binding) || !agent.AcceptingPrompts {
			binding.serial.Unlock()
			return
		}
		fence := bindingFence(agentID, binding)
		rpc, supported := binding.rpc.(DirectMailboxRPC)
		binding.serial.Unlock()
		if !supported {
			return
		}
		attempt, err := m.store.AllocateManagedDirectAttempt(context.Background(), fence)
		if err != nil || attempt == nil {
			m.scheduleDirectRetry(agentID)
			return
		}
		// The persisted cursor is captured before the matcher is armed and before
		// the command is written. Recovery reads only entries after this boundary.
		prepareCtx, prepareCancel := context.WithTimeout(m.runCtx, 10*time.Second)
		cursor, err := rpc.PrepareManagedMailbox(prepareCtx)
		prepareCancel()
		if err != nil {
			m.releaseManagedDirect(*attempt, "prompt_prepare_failed", sanitizeError(err))
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "prompt_prepare_failed", "")
			return
		}
		attempt.SessionEntryWatermark = cursor
		if err := m.store.MarkManagedDirectPrompted(context.Background(), *attempt, cursor); err != nil {
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "prompt_state_persistence_failed", "")
			m.releaseManagedDirect(*attempt, "prompt_state_persistence_failed", sanitizeError(err))
			return
		}
		// The append receipt has a bounded delivery deadline. Once it is durable,
		// keep this lane with the turn until Pi settles so a second direct command
		// cannot overtake the first response.
		receiptCtx, cancelReceipt := context.WithTimeout(m.runCtx, 30*time.Second)
		// Keep the context ownership local to this turn. Explicit early cancels
		// below stop Pi promptly; the defers cover every retry/startup exit.
		defer cancelReceipt()
		receiptDone := receiptCtx.Done()
		turnCtx, cancelTurn := context.WithCancel(m.runCtx)
		defer cancelTurn()
		events, err := rpc.PromptManagedMailbox(turnCtx, *attempt)
		if err != nil {
			cancelTurn()
			cancelReceipt()
			m.releaseManagedDirect(*attempt, "prompt_failed", sanitizeError(err))
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "prompt_failed", "")
			return
		}
		// The command has been accepted by the managed RPC writer. Show the same
		// immediate working affordance as tmux while Pi begins its turn; native
		// parent events will take over as soon as they arrive.
		directPromptActive := false
		if _, activityErr := m.store.SetManagedDirectPromptActivity(context.Background(), fence, true); activityErr != nil {
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "prompt_activity_persistence_failed", "")
		} else {
			directPromptActive = true
			// A direct mailbox command has no tmux activity frame to wake WebUI.
			// Publish the committed projection now, before waiting for Pi's native
			// turn event, so the working indicator changes with accepted delivery.
			m.publishProjection()
		}
		clearDirectPromptActivity := func() {
			if !directPromptActive {
				return
			}
			directPromptActive = false
			if _, activityErr := m.store.SetManagedDirectPromptActivity(context.Background(), fence, false); activityErr != nil {
				_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "prompt_activity_clear_failed", "")
			} else {
				m.publishProjection()
			}
		}
		var acknowledged bool
		for {
			select {
			case event, open := <-events:
				if !open {
					clearDirectPromptActivity()
					cancelTurn()
					cancelReceipt()
					if !acknowledged {
						m.releaseManagedDirect(*attempt, "receipt_stream_closed", "")
						_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "receipt_stream_closed", "")
					} else {
						_ = m.store.InterruptManagedParentTurn(context.Background(), *attempt)
					}
					return
				}
				if event.DiagnosticCode != "" {
					_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, event.DiagnosticCode, "")
				}
				if event.Receipt != nil && !acknowledged {
					binding.serial.Lock()
					current := m.liveBindingForFence(context.Background(), binding, fence)
					binding.serial.Unlock()
					if !current {
						clearDirectPromptActivity()
						cancelTurn()
						cancelReceipt()
						m.releaseManagedDirect(*attempt, "stale_fence", "")
						return
					}
					if m.beforeDirectAcknowledge != nil {
						if err := m.beforeDirectAcknowledge(context.Background(), *attempt); err != nil {
							// Receipt delivery proves that Pi appended the exact entry. Keep
							// the prompted attempt for cursor-based recovery; releasing here
							// could send the same message a second time.
							clearDirectPromptActivity()
							cancelTurn()
							cancelReceipt()
							_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "acknowledgment_interrupted", sanitizeError(err))
							return
						}
					}
					if !m.acknowledgeManagedDirect(*attempt, event.Receipt.EntryIdentity) {
						clearDirectPromptActivity()
						cancelTurn()
						cancelReceipt()
						return
					}
					acknowledged = true
					cancelReceipt()
					receiptDone = nil
				}
				if acknowledged && event.Reply != nil {
					// A reply can arrive before its native settled boundary. Keep the
					// direct-prompt affordance visible until the actual turn settles.
					m.publishDirectReply(*attempt, *event.Reply)
				}
				if acknowledged && event.Settled {
					_ = m.store.SettleManagedParentDirectTurn(context.Background(), *attempt)
					clearDirectPromptActivity()
					cancelTurn()
					for range events {
					}
					return
				}
			case <-receiptDone:
				if !acknowledged {
					clearDirectPromptActivity()
					cancelTurn()
					m.releaseManagedDirect(*attempt, "receipt_timeout", "")
					_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "receipt_timeout", "")
				}
				return
			case <-m.runCtx.Done():
				clearDirectPromptActivity()
				cancelTurn()
				cancelReceipt()
				return
			}
		}
		// The lane is intentionally one-at-a-time. A burst gets one fixed Pi
		// command per original ordinary row, then the next oldest row.
	}
}

func (m *Manager) scheduleManagedFinalDeliveryRetry(agentID string) {
	due, ok, err := m.store.NextManagedFinalDeliveryRetry(context.Background(), agentID)
	if err != nil || !ok {
		return
	}
	delay := due.Sub(m.clock.Now())
	if delay < 0 {
		delay = 0
	}
	m.startManagedTask(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			if binding, ok := m.bindingFor(agentID); ok {
				binding.serial.Lock()
				fence := bindingFence(agentID, binding)
				live := m.liveBindingForFence(context.Background(), binding, fence)
				binding.serial.Unlock()
				if live {
					deliveries, _ := m.store.PendingManagedFinalDeliveries(context.Background(), fence)
					for _, delivery := range deliveries {
						m.publishManagedFinalDelivery(delivery)
					}
				}
			}
		case <-m.runCtx.Done():
		}
	})
}

func (m *Manager) scheduleDirectRetry(agentID string) {
	due, ok, err := m.store.NextManagedDirectRetry(context.Background(), agentID)
	if err != nil || !ok {
		return
	}
	delay := due.Sub(m.clock.Now())
	if delay < 0 {
		delay = 0
	}
	m.startManagedTask(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			m.OfferDirect(agentID)
		case <-m.runCtx.Done():
		}
	})
}

// RecoverManagedDirectAttempts is called only after managed restart recovery
// has supplied its live lane. Pending writes receive a durable retry release;
// prompted attempts stay fenced to their original ordinary row and are never
// moved to a tmux lease.
func (m *Manager) RecoverManagedDirectAttempts(ctx context.Context) error {
	m.mu.Lock()
	ids := make([]string, 0, len(m.bindings))
	for id := range m.bindings {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		if err := m.recoverManagedDirectAttemptsForAgent(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) recoverManagedDirectAttemptsAsync(agentID string) {
	m.startManagedTask(func() { _ = m.recoverManagedDirectAttemptsForAgent(context.Background(), agentID) })
}

// recoverManagedDirectAttemptsForAgent runs only after a live lane exists.
// Pending rows are released without I/O. Prompted rows first reconcile their
// recorded Pi cursor; an exact persisted marker settles the original row and
// never emits another slash command.
func (m *Manager) recoverManagedDirectAttemptsForAgent(ctx context.Context, agentID string) error {
	lane := m.laneFor(agentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	if m.beforeManagedDirectRecovery != nil {
		m.beforeManagedDirectRecovery(ctx, agentID)
	}
	attempts, err := m.store.ManagedActiveDirectAttempts(ctx, agentID)
	if err != nil {
		return err
	}
	binding, live := m.bindingFor(agentID)
	if !live {
		return nil
	}
	binding.serial.Lock()
	currentFence := bindingFence(agentID, binding)
	rpc, supported := binding.rpc.(DirectMailboxRPC)
	bindingLive := m.liveBindingForFence(ctx, binding, currentFence)
	binding.serial.Unlock()
	if !bindingLive || !supported {
		return nil
	}
	// A router restart can happen after source persistence but before the
	// conversation enqueue acknowledgement. Replay the deterministic source
	// through the ordinary publisher; it remains pending on any new failure.
	if m.store.RecoverManagedFinalDeliveries(ctx, currentFence) == nil {
		if deliveries, deliveryErr := m.store.PendingManagedFinalDeliveries(ctx, currentFence); deliveryErr == nil {
			for _, delivery := range deliveries {
				m.publishManagedFinalDelivery(delivery)
			}
		}
	}
	for _, attempt := range attempts {
		if attempt.State == "direct_pending" {
			m.releaseManagedDirectRecovery(attempt, "stale_fence")
			continue
		}
		if attempt.State != "direct_prompted" {
			continue
		}
		if attempt.Fence.PiSessionID != currentFence.PiSessionID {
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "recovery_session_mismatch", "")
			m.releaseManagedDirectRecovery(attempt, "recovery_session_mismatch")
			continue
		}
		recovery, recoveryErr := rpc.RecoverManagedMailbox(ctx, attempt)
		if recoveryErr != nil {
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, "recovery_query_failed", "")
			m.releaseManagedDirectRecovery(attempt, "recovery_query_failed")
			continue
		}
		for _, code := range recovery.Diagnostics {
			_ = m.store.RecordManagedDirectDiagnostic(context.Background(), agentID, attempt.MessageID, attempt.AttemptNumber, code, "")
		}
		if recovery.Receipt != nil {
			_ = m.acknowledgeManagedDirectRecovered(attempt, recovery.Receipt.EntryIdentity)
			continue
		}
		m.releaseManagedDirectRecovery(attempt, "recovery_marker_absent")
	}
	// The offer takes the same lane after this recovery owner releases it, so no
	// new allocation can overtake the persisted entry scan above.
	m.OfferDirect(agentID)
	return nil
}

func (m *Manager) Store() *store.Store { return m.store }

func (m *Manager) Agent(ctx context.Context, id string) (*store.ManagedAgentRecord, error) {
	return m.store.ManagedAgent(ctx, id)
}

// Models returns the current launch catalog and provider-auth revision without
// exposing helper internals or credentials.
func (m *Manager) Models(ctx context.Context) (Catalog, error) {
	return m.catalog.Catalog(ctx)
}

func (m *Manager) Agents(ctx context.Context) ([]*store.ManagedAgentRecord, error) {
	return m.store.ManagedAgents(ctx)
}
func (m *Manager) recordCloseError(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closeErr == nil {
		m.closeErr = err
	}
}

// AbortStartup releases bindings created during a failed Router.Listen attempt
// without closing the manager. Recovery bindings are made retryable after their
// exact target groups have been reaped, so the same Router can listen again.
func (m *Manager) AbortStartup(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	bindings := make([]*managedBinding, 0, len(m.bindings))
	for _, binding := range m.bindings {
		bindings = append(bindings, binding)
	}
	orphanProcesses := make(map[string]Process)
	for agentID, process := range m.processes {
		if m.bindings[agentID] == nil {
			orphanProcesses[agentID] = process
		}
	}
	m.mu.Unlock()
	var firstErr error
	for _, binding := range bindings {
		lane := binding.lane
		if lane != nil {
			lane.serial.Lock()
		}
		binding.serial.Lock()
		process, rpc, identity, waitDone := binding.process, binding.rpc, binding.identity, binding.waitDone
		actionID, lifecycleAction := binding.operationID, binding.lifecycleAction
		binding.reaped = true
		binding.serial.Unlock()
		m.stopBuiltinToolLifecycleQueue(binding)
		if rpc != nil {
			if err := rpc.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if process != nil {
			// Stop the owned handle first so its waiter can finish; the group
			// reaper below still proves the complete exact identity is absent.
			if m.processOps.Probe(identity).State == ProcessOwned {
				if err := process.Stop(ctx); err != nil && firstErr == nil {
					firstErr = err
				}
			}
			cleaned := m.cleanupUninstalledTarget(process, identity, waitDone)
			if !cleaned && firstErr == nil {
				firstErr = errors.New("managed startup binding cleanup failed")
			}
		}
		if lifecycleAction == "root-recovery" && actionID != "" && process != nil {
			if err := m.store.SupersedeManagedRootRecovery(context.Background(), actionID); err == nil {
				if err := m.store.ClearManagedRootRecoveryTargetAfterVerifiedAbsence(context.Background(), actionID, store.ManagedProcessIdentity{PID: identity.PID, StartMarker: identity.StartMarker, Incarnation: identity.Incarnation}); err != nil && firstErr == nil {
					firstErr = err
				}
			} else if firstErr == nil {
				firstErr = err
			}
		}
		m.clearBinding(binding)
		if lane != nil {
			lane.serial.Unlock()
		}
	}
	for _, process := range orphanProcesses {
		if process != nil {
			if err := process.Stop(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()
	bindings := make([]*managedBinding, 0, len(m.bindings))
	for _, binding := range m.bindings {
		bindings = append(bindings, binding)
	}
	m.mu.Unlock()
	genericAgentIDs := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		handled, err := m.shutdownLifecycleRestartBinding(ctx, binding)
		if handled {
			m.recordCloseError(err)
			continue
		}
		handled, err = m.shutdownResetBinding(ctx, binding)
		if handled {
			m.recordCloseError(err)
			continue
		}
		handled, err = m.shutdownTerminationBinding(ctx, binding)
		if handled {
			m.recordCloseError(err)
			continue
		}
		handled, err = m.shutdownRestartBinding(ctx, binding)
		if handled {
			m.recordCloseError(err)
			continue
		}
		lane := binding.lane
		if lane != nil {
			lane.serial.Lock()
		}
		binding.serial.Lock()
		rpc, process := binding.rpc, binding.process
		fence := bindingFence(binding.agentID, binding)
		binding.serial.Unlock()
		m.stopBuiltinToolLifecycleQueue(binding)
		m.interruptBuiltinToolInvocations(binding, fence)
		_ = m.store.InterruptManagedParentWork(context.Background(), fence)
		if rpc != nil {
			_ = rpc.Close()
		}
		if process != nil {
			m.recordCloseError(process.Stop(ctx))
		}
		if lane != nil {
			lane.serial.Unlock()
		}
		m.clearBinding(binding)
		genericAgentIDs = append(genericAgentIDs, binding.agentID)
	}
	waitDone := make(chan struct{})
	go func() { m.startWG.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
		for _, agentID := range genericAgentIDs {
			m.recordCloseError(m.store.MarkManagedStopped(context.Background(), agentID))
		}
		m.mu.Lock()
		err := m.closeErr
		m.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
