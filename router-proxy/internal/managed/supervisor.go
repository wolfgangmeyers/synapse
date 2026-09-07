package managed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type LaunchRequest struct {
	Name                string
	Cwd                 string
	Model               *Model
	InitialInstruction  string
	SessionIdentityHash string
	Method              string
	CanonicalPath       string
	RequestID           string
	CanonicalBodyHash   string
}
type LaunchResult struct {
	Agent    *store.ManagedAgentRecord
	Replayed bool
}
type ManagerOptions struct {
	Store          *store.Store
	Config         Config
	Manifest       Manifest
	ProcessFactory ProcessFactory
	RPCFactory     RPCFactory
	CatalogSource  ModelSource
	Clock          deterministic.Clock
	IDSource       deterministic.IDSource
}

type Manager struct {
	store          *store.Store
	config         Config
	manifest       Manifest
	processFactory ProcessFactory
	rpcFactory     RPCFactory
	catalog        ModelSource
	clock          deterministic.Clock
	ids            deterministic.IDSource
	mu             sync.Mutex
	processes      map[string]Process
	rpcs           map[string]RPC
	operationIDs   map[string]string
	runCtx         context.Context
	cancel         context.CancelFunc
	startWG        sync.WaitGroup
	closed         bool
}

func NewManager(options ManagerOptions) (*Manager, error) {
	if options.Store == nil {
		return nil, errors.New("managed store is required")
	}
	config := options.Config
	config.normalize()
	if err := config.Validate(); err != nil {
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
		catalog = CatalogFromModels(Model{Provider: "openai", ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Available: true})
	}
	runCtx, cancel := context.WithCancel(context.Background())
	return &Manager{store: options.Store, config: config, manifest: manifest, processFactory: processFactory, rpcFactory: rpcFactory, catalog: catalog, clock: clock, ids: ids, processes: map[string]Process{}, rpcs: map[string]RPC{}, operationIDs: map[string]string{}, runCtx: runCtx, cancel: cancel}, nil
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
	op := store.ManagedOperation{OperationID: request.RequestID, SessionIdentityHash: request.SessionIdentityHash, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"X-Request-ID": request.RequestID, "Content-Type": "application/json; charset=utf-8"}, ResponseBody: body, CreatedAt: now}
	if err := m.store.ReserveManagedLaunchWithLimit(ctx, agent, op, m.config.Limits.MaxAgents); err != nil {
		return LaunchResult{}, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = m.store.MarkManagedFailed(context.Background(), agent.AgentID, "MANAGER_CLOSED", "managed manager closed")
		return LaunchResult{}, errors.New("managed manager is closed")
	}
	m.operationIDs[agent.AgentID] = request.RequestID
	m.startWG.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.startWG.Done()
		m.start(m.runCtx, agent, request)
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

func (m *Manager) start(ctx context.Context, agent store.ManagedAgentRecord, request LaunchRequest) {
	defer func() {
		m.mu.Lock()
		delete(m.processes, agent.AgentID)
		delete(m.rpcs, agent.AgentID)
		delete(m.operationIDs, agent.AgentID)
		m.mu.Unlock()
	}()
	spec := CommandSpec{PiExecutable: m.config.PiExecutable, SessionRoot: m.config.SessionRoot, SessionID: agent.PiSessionID, Cwd: agent.Cwd, Provider: agent.SelectedModel.Provider, ModelID: agent.SelectedModel.ID, MechaRPCExtension: m.config.MechaRPCExtension, PiSubagentsExtension: m.config.PiSubagentsExtension, CursorExtension: m.config.CursorExtension, Environment: ChildEnvironmentFor(m.config, agent.RuntimeGeneration, agent.SessionEpoch)}
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
	if err = m.store.MarkManagedReady(ctx, agent.AgentID); err != nil {
		_ = rpc.Close()
		_ = process.Stop(context.Background())
		m.fail(agent.AgentID, "STATE_UPDATE_FAILED", sanitizeError(err))
		return
	}
	if request.InitialInstruction != "" {
		if err = rpc.Prompt(ctx, request.InitialInstruction); err != nil {
			_ = rpc.Close()
			_ = process.Stop(context.Background())
			m.fail(agent.AgentID, "INITIAL_INSTRUCTION_FAILED", sanitizeError(err))
			return
		}
		_ = m.store.MarkManagedInitialInstructionSent(ctx, agent.AgentID)
	}
	_ = m.store.MarkManagedOperationTerminal(ctx, request.RequestID)
	select {
	case waitErr := <-waitDone:
		if ctx.Err() == nil {
			_ = m.store.MarkManagedFailed(context.Background(), agent.AgentID, "PROCESS_EXITED", sanitizeError(waitErr))
		} else {
			_ = m.store.ReleaseManagedSlot(context.Background(), agent.AgentID)
		}
	case <-ctx.Done():
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
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()
	processes := make([]Process, 0, len(m.processes))
	rpcs := make([]RPC, 0, len(m.rpcs))
	for _, process := range m.processes {
		processes = append(processes, process)
	}
	for _, rpc := range m.rpcs {
		rpcs = append(rpcs, rpc)
	}
	m.mu.Unlock()
	waitDone := make(chan struct{})
	go func() { m.startWG.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	m.mu.Lock()
	processes = processes[:0]
	rpcs = rpcs[:0]
	for _, process := range m.processes {
		processes = append(processes, process)
	}
	for _, rpc := range m.rpcs {
		rpcs = append(rpcs, rpc)
	}
	m.mu.Unlock()
	for _, rpc := range rpcs {
		_ = rpc.Close()
	}
	var first error
	for _, process := range processes {
		if err := process.Stop(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}
