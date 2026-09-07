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
	RestartPreparationDeadlineFactory RestartPreparationDeadlineFactory
	RestartPreparationHook            RestartPreparationHook
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
}

type RestartResult struct {
	Agent     *store.ManagedAgentRecord
	Operation *store.ManagedOperation
	Replayed  bool
}

type managedBinding struct {
	agentID              string
	serial               sync.Mutex
	preparationWG        sync.WaitGroup
	process              Process
	rpc                  RPC
	operationID          string
	lifecycleAction      string
	identity             ProcessIdentity
	sessionID            string
	sessionEpoch         int64
	runtimeGeneration    string
	reaped               bool
	restartShutdownDone  bool
	restartShutdownError error
}

type Manager struct {
	store               *store.Store
	config              Config
	manifest            Manifest
	processFactory      ProcessFactory
	rpcFactory          RPCFactory
	catalog             ModelSource
	clock               deterministic.Clock
	ids                 deterministic.IDSource
	mu                  sync.Mutex
	processes           map[string]Process
	rpcs                map[string]RPC
	operationIDs        map[string]string
	bindings            map[string]*managedBinding
	groupLeader         ProcessGroupLeaderVerifier
	preparationDeadline RestartPreparationDeadlineFactory
	preparationHook     RestartPreparationHook
	runCtx              context.Context
	cancel              context.CancelFunc
	startWG             sync.WaitGroup
	closed              bool
	closeErr            error
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
	if len(manifest.Tools) != 16 {
		return nil, errors.New("managed manifest must have 16 tools")
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
	deadlineFactory := options.RestartPreparationDeadlineFactory
	if deadlineFactory == nil {
		deadlineFactory = context.WithTimeout
	}
	runCtx, cancel := context.WithCancel(context.Background())
	return &Manager{store: options.Store, config: config, manifest: manifest, processFactory: processFactory, rpcFactory: rpcFactory, catalog: catalog, clock: clock, ids: ids, processes: map[string]Process{}, rpcs: map[string]RPC{}, operationIDs: map[string]string{}, bindings: map[string]*managedBinding{}, groupLeader: groupLeader, preparationDeadline: deadlineFactory, preparationHook: options.RestartPreparationHook, runCtx: runCtx, cancel: cancel}, nil
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
	public := PublicAgent(&agent)
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": public})
	if err != nil {
		return LaunchResult{}, err
	}
	op := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"X-Request-ID": request.RequestID, "Content-Type": "application/json; charset=utf-8"}, ResponseBody: body, CreatedAt: now}
	if err := m.store.ReserveManagedLaunchWithLimit(ctx, agent, op, m.config.Limits.MaxAgents); err != nil {
		return LaunchResult{}, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = m.store.MarkManagedFailed(context.Background(), agent.AgentID, "MANAGER_CLOSED", "managed manager closed")
		return LaunchResult{}, errors.New("managed manager is closed")
	}
	binding := &managedBinding{agentID: agent.AgentID, operationID: request.RequestID, lifecycleAction: "launch", sessionID: agent.PiSessionID, sessionEpoch: agent.SessionEpoch, runtimeGeneration: agent.RuntimeGeneration}
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
	defer func() {
		m.mu.Lock()
		delete(m.processes, agent.AgentID)
		delete(m.rpcs, agent.AgentID)
		delete(m.operationIDs, agent.AgentID)
		if m.bindings[agent.AgentID] == binding {
			delete(m.bindings, agent.AgentID)
		}
		m.mu.Unlock()
	}()
	spec := CommandSpec{PiExecutable: m.config.PiExecutable, SessionRoot: m.config.SessionRoot, SessionID: agent.PiSessionID, Cwd: agent.Cwd, Provider: agent.SelectedModel.Provider, ModelID: agent.SelectedModel.ID, MechaRPCExtension: m.config.MechaRPCExtension, PiSubagentsExtension: m.config.PiSubagentsExtension, Environment: ChildEnvironmentFor(m.config, agent.RuntimeGeneration, agent.SessionEpoch)}
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
	go func() { waitDone <- process.Wait() }()
	if err = m.store.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: identity.PID, StartMarker: identity.StartMarker, Incarnation: identity.Incarnation}, agent.RuntimeGeneration); err != nil {
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
	if err = readinessGate(ctx, rpc, m.manifest, agent.PiSessionID, agent.SessionEpoch, agent.RuntimeGeneration); err != nil {
		_ = rpc.Close()
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "READINESS_FAILED", sanitizeError(err))
		return
	}
	select {
	case waitErr := <-waitDone:
		_ = rpc.Close()
		m.fail(agent.AgentID, "PROCESS_EXITED", sanitizeError(waitErr))
		return
	default:
	}
	binding.serial.Lock()
	if err = m.store.MarkManagedReady(ctx, agent.AgentID); err != nil {
		binding.serial.Unlock()
		_ = rpc.Close()
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "STATE_UPDATE_FAILED", sanitizeError(err))
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
		_ = m.store.MarkManagedInitialInstructionSent(ctx, agent.AgentID)
	}
	_ = m.store.MarkManagedOperationTerminal(ctx, request.RequestID)
	binding.lifecycleAction = ""
	binding.serial.Unlock()
	select {
	case waitErr := <-waitDone:
		if m.recordRestartExit(binding) {
			return
		}
		if m.isClosed() {
			_ = m.store.MarkManagedStopped(context.Background(), agent.AgentID)
		} else {
			_ = m.store.MarkManagedFailed(context.Background(), agent.AgentID, "PROCESS_EXITED", sanitizeError(waitErr))
		}
	case <-ctx.Done():
		handled, shutdownErr := m.shutdownRestartBinding(context.Background(), binding)
		if handled {
			m.recordCloseError(shutdownErr)
			return
		}
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
	if ValidateIdentity(identity) != nil || processStartMarker(identity.PID) != identity.StartMarker {
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

func (m *Manager) bindingFor(agentID string) (*managedBinding, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	binding := m.bindings[agentID]
	return binding, !m.closed && binding != nil
}

func bindingFence(agentID string, binding *managedBinding) store.ManagedRestartFence {
	if agentID == "" {
		agentID = binding.agentID
	}
	return store.ManagedRestartFence{AgentID: agentID, PiSessionID: binding.sessionID, SessionEpoch: binding.sessionEpoch, ProcessPID: binding.identity.PID, ProcessStartMarker: binding.identity.StartMarker, ProcessIncarnation: binding.identity.Incarnation, RuntimeGeneration: binding.runtimeGeneration}
}

func sameBindingFence(left, right store.ManagedRestartFence) bool {
	return left.AgentID == right.AgentID && left.PiSessionID == right.PiSessionID && left.SessionEpoch == right.SessionEpoch && left.ProcessPID == right.ProcessPID && left.ProcessStartMarker == right.ProcessStartMarker && left.ProcessIncarnation == right.ProcessIncarnation && left.RuntimeGeneration == right.RuntimeGeneration
}

func (m *Manager) liveBinding(agent *store.ManagedAgentRecord, binding *managedBinding) bool {
	if agent == nil || binding.reaped || binding.process == nil || binding.rpc == nil || ValidateIdentity(binding.identity) != nil || !m.groupLeader(binding.identity) {
		return false
	}
	if binding.process.Identity() != binding.identity {
		return false
	}
	fence := bindingFence(agent.AgentID, binding)
	return agent.PiSessionID == fence.PiSessionID && agent.SessionEpoch == fence.SessionEpoch && agent.ProcessPID == fence.ProcessPID && agent.ProcessStartMarker == fence.ProcessStartMarker && agent.ProcessIncarnation == fence.ProcessIncarnation && agent.RuntimeGeneration == fence.RuntimeGeneration
}

func replayRestartOperation(request RestartRequest, operation *store.ManagedOperation) (RestartResult, error) {
	if operation.CanonicalPath != request.CanonicalPath || operation.CanonicalBodyHash != request.CanonicalBodyHash {
		return RestartResult{}, &store.ManagedIdempotencyConflictError{}
	}
	return RestartResult{Operation: operation, Replayed: true}, nil
}

// ReserveRestart is an internal-only admission boundary for a future route.
// It resolves the worker-wide durable replay before touching the live binding,
// then closes prompt admission atomically before its state read starts.
func (m *Manager) ReserveRestart(ctx context.Context, request RestartRequest) (RestartResult, error) {
	if request.AgentID == "" || request.Method == "" || request.CanonicalPath == "" || request.RequestID == "" || request.CanonicalBodyHash == "" {
		return RestartResult{}, errors.New("managed restart request is incomplete")
	}
	existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID)
	if err == nil {
		return replayRestartOperation(request, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RestartResult{}, err
	}
	binding, ok := m.bindingFor(request.AgentID)
	if !ok {
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	binding.serial.Lock()
	agent, err := m.store.ManagedAgent(ctx, request.AgentID)
	if err != nil {
		binding.serial.Unlock()
		if errors.Is(err, sql.ErrNoRows) {
			return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
		}
		return RestartResult{}, err
	}
	if !m.liveBinding(agent, binding) {
		binding.serial.Unlock()
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	current, err := m.store.ManagedOperation(ctx, binding.operationID)
	if err != nil || current.State != "terminal" || restartActionHigherPriority(binding.lifecycleAction) {
		binding.serial.Unlock()
		if err != nil {
			return RestartResult{}, &store.ManagedRestartConflictError{AgentID: request.AgentID}
		}
		return RestartResult{}, &store.ManagedRestartConflictError{AgentID: request.AgentID}
	}
	targetGeneration, err := m.ids.Next()
	if err != nil {
		binding.serial.Unlock()
		return RestartResult{}, err
	}
	responseAgent := *agent
	responseAgent.LifecycleState = "restarting"
	responseAgent.AcceptingPrompts = false
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": PublicAgent(&responseAgent)})
	if err != nil {
		binding.serial.Unlock()
		return RestartResult{}, err
	}
	operation := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, ResponseBody: body, CreatedAt: formatManagedTime(m.clock.Now())}
	fence := bindingFence(request.AgentID, binding)
	reserved, replayed, err := m.store.ReserveManagedRestart(ctx, store.ManagedRestartReservation{Fence: fence, CurrentOperationID: binding.operationID, CurrentLifecycleAction: binding.lifecycleAction, Transition: store.ManagedRestartTransition{TargetRuntimeGeneration: targetGeneration}, Operation: operation})
	if err != nil {
		binding.serial.Unlock()
		if _, conflict := err.(*store.ManagedIdempotencyConflictError); conflict {
			existing, lookupErr := m.store.FindManagedOperation(ctx, request.Method, request.RequestID)
			if lookupErr == nil {
				return replayRestartOperation(request, existing)
			}
		}
		return RestartResult{}, err
	}
	if replayed {
		binding.serial.Unlock()
		return RestartResult{Operation: reserved, Replayed: true}, nil
	}
	binding.operationID = request.RequestID
	binding.lifecycleAction = "restart"
	binding.serial.Unlock()
	m.mu.Lock()
	if m.bindings[request.AgentID] == binding {
		m.operationIDs[request.AgentID] = request.RequestID
	}
	m.mu.Unlock()
	m.startRestartPreparation(binding, fence, request.RequestID)
	return RestartResult{Agent: &responseAgent, Operation: reserved}, nil
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
}

// AdmitPrompt is intentionally internal. It shares restart's serializer so a
// message cannot cross the durable acceptance change into the old Pi process.
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

// shutdownRestartBinding is the only close path for a pending restart. It
// waits for preparation to leave its RPC read, then retains the transition for
// router recovery instead of routing through generic launch cleanup.
func (m *Manager) shutdownRestartBinding(ctx context.Context, binding *managedBinding) (bool, error) {
	binding.preparationWG.Wait()
	binding.serial.Lock()
	defer binding.serial.Unlock()
	if binding.restartShutdownDone {
		return true, binding.restartShutdownError
	}
	if binding.lifecycleAction != "restart" {
		return false, nil
	}
	finish := func(err error) (bool, error) {
		binding.restartShutdownDone = true
		binding.restartShutdownError = err
		return true, err
	}
	fence := bindingFence(binding.agentID, binding)
	transition, err := m.store.ManagedRestartTransition(context.Background(), binding.agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return finish(err)
	}
	if transition.OperationID != binding.operationID || !sameBindingFence(transition.Fence, fence) || binding.process == nil || binding.process.Identity() != binding.identity || !m.groupLeader(binding.identity) {
		return finish(&store.ManagedRestartStaleError{AgentID: binding.agentID})
	}
	if err := binding.process.Stop(ctx); err != nil {
		return finish(err)
	}
	binding.reaped = true
	if binding.rpc != nil {
		_ = binding.rpc.Close()
	}
	return finish(m.store.RecordManagedRestartShutdown(context.Background(), fence, transition.OperationID))
}

// recordRestartExit keeps a restart recovery record when the owned process
// exits unexpectedly. Generic failed/stopped writes are unsafe once restart
// owns the transition, so recovery receives the fenced stopped observation.
func (m *Manager) recordRestartExit(binding *managedBinding) bool {
	binding.serial.Lock()
	defer binding.serial.Unlock()
	if binding.lifecycleAction != "restart" {
		return false
	}
	transition, err := m.store.ManagedRestartTransition(context.Background(), binding.agentID)
	if err != nil {
		return false
	}
	fence := bindingFence(binding.agentID, binding)
	binding.reaped = true
	if transition.OperationID != binding.operationID || !sameBindingFence(transition.Fence, fence) {
		return true
	}
	_ = m.store.RecordManagedRestartShutdown(context.Background(), fence, transition.OperationID)
	return true
}

func sanitizeError(err error) string {
	if err == nil {
		return "managed Pi exited before readiness"
	}
	return RedactedStderr(err.Error())
}

var managedNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var hiddenCommands = []string{"mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1"}
var legacyCommands = []string{"goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"}
var fixedSubagentsTools = []string{"subagent", "get_subagent_result", "steer_subagent"}

func expectedManagedActiveTools(manifest Manifest) []string {
	result := append([]string{}, fixedSubagentsTools...)
	for _, tool := range manifest.Tools {
		result = append(result, tool.Name)
	}
	sort.Strings(result)
	return result
}

func readinessGate(ctx context.Context, rpc RPC, manifest Manifest, sessionID string, epoch int64, generation string) error {
	state, err := rpc.GetState(ctx)
	if err != nil {
		return err
	}
	if !stateSessionMatches(state, sessionID) {
		return errors.New("Pi session identity does not match managed record")
	}
	commands, err := rpc.GetCommands(ctx)
	if err != nil {
		return err
	}
	if err := requireManagedCommands(commands); err != nil {
		return err
	}
	envelope := map[string]any{"protocol": "mecha.bootstrap/v1", "delivery_id": generation, "runtime_generation": generation, "session_epoch": epoch, "desired_manifest": manifest.Raw, "manifest_sha256": manifest.CanonicalSHA256, "restored_request_ids": []any{}, "inline_policy": map[string]any{"enabled": false, "credits": map[string]any{}}, "recall_projection": map[string]any{"content": ""}}
	if err := rpc.Bootstrap(ctx, envelope); err != nil {
		return err
	}
	if err := rpc.WaitForReceipt(ctx, generation, manifest.CanonicalSHA256, manifest.Revision, epoch, "mecha.tools.accepted.v1"); err != nil {
		return err
	}
	active, err := rpc.GetActiveTools(ctx)
	if err != nil {
		return err
	}
	want := expectedManagedActiveTools(manifest)
	sort.Strings(active)
	if len(active) != len(want) {
		return errors.New("Pi active tool set does not match manifest")
	}
	for i := range want {
		if active[i] != want[i] {
			return errors.New("Pi active tool set does not match manifest")
		}
	}
	return nil
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

func (m *Manager) Store() *store.Store { return m.store }

func (m *Manager) Agent(ctx context.Context, id string) (*store.ManagedAgentRecord, error) {
	return m.store.ManagedAgent(ctx, id)
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
		handled, err := m.shutdownRestartBinding(ctx, binding)
		if handled {
			m.recordCloseError(err)
			continue
		}
		binding.serial.Lock()
		rpc, process := binding.rpc, binding.process
		binding.serial.Unlock()
		if rpc != nil {
			_ = rpc.Close()
		}
		if process != nil {
			m.recordCloseError(process.Stop(ctx))
		}
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
