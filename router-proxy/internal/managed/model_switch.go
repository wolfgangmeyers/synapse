package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type ModelChangeRequest struct {
	AgentID           string
	Method            string
	CanonicalPath     string
	RequestID         string
	CanonicalBodyHash string
	Model             Model
}

type ModelChangeResult struct {
	Agent     *store.ManagedAgentRecord
	Operation *store.ManagedOperation
	Replayed  bool
}

type ModelSwitchFailedError struct{ AgentID string }

func (e *ModelSwitchFailedError) Error() string { return "managed model switch could not be confirmed" }
func (e *ModelSwitchFailedError) Code() string  { return "model_switch_failed" }

// ModelStatePersistenceError means the runtime may have changed model while
// the durable completion transaction failed. The pending operation remains the
// cold-recovery fence and the current runtime is terminated.
type ModelStatePersistenceError struct {
	AgentID        string
	CommitErr      error
	TerminationErr error
}

func (e *ModelStatePersistenceError) Error() string {
	return "managed model result could not be persisted; the pending recovery fence was retained"
}
func (e *ModelStatePersistenceError) Code() string { return "model_state_unconfirmed" }
func (e *ModelStatePersistenceError) Unwrap() []error {
	errs := []error{e.CommitErr}
	if e.TerminationErr != nil {
		errs = append(errs, e.TerminationErr)
	}
	return errs
}

func replayModelChange(request ModelChangeRequest, operation *store.ManagedOperation) (ModelChangeResult, error) {
	if operation.CanonicalPath != request.CanonicalPath || operation.CanonicalBodyHash != request.CanonicalBodyHash {
		return ModelChangeResult{}, &store.ManagedIdempotencyConflictError{}
	}
	return ModelChangeResult{Operation: operation, Replayed: true}, nil
}

func modelFromRPCState(state map[string]any) (Model, bool) {
	value, ok := state["model"].(map[string]any)
	if !ok {
		return Model{}, false
	}
	provider, providerOK := value["provider"].(string)
	id, idOK := value["id"].(string)
	if !providerOK || !idOK || provider == "" || id == "" {
		return Model{}, false
	}
	name, _ := value["name"].(string)
	return Model{Provider: provider, ID: id, Name: name, Available: true}, true
}

func sameModel(left Model, right Model) bool {
	return left.Provider == right.Provider && left.ID == right.ID
}

func modelOperationHeaders(requestID string) map[string]string {
	return map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": requestID}
}

func modelFailureBody(requestID, code, message string) []byte {
	body, _ := json.Marshal(map[string]any{
		"error":      map[string]any{"code": code, "message": message},
		"request_id": requestID,
	})
	return body
}

// terminateUnpersistedModelRuntime removes the only surviving source of model
// truth when SQLite cannot record the result. Process.Stop is synchronous and
// owns full process-group cleanup.
func (m *Manager) terminateUnpersistedModelRuntime(binding *managedBinding) error {
	binding.serial.Lock()
	binding.expectedStop.Store(true)
	rpc, process := binding.rpc, binding.process
	binding.serial.Unlock()

	limit := m.config.Limits.GracefulStop
	if limit <= 0 {
		limit = DefaultLimits().GracefulStop
	}
	abortCtx, cancelAbort := context.WithTimeout(context.Background(), limit)
	if rpc != nil {
		_ = rpc.Abort(abortCtx)
	}
	cancelAbort()
	if rpc != nil {
		_ = closeRPCAndWait(rpc)
	}
	if process == nil {
		m.clearBinding(binding)
		return nil
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), limit)
	stopErr := process.Stop(stopCtx)
	cancelStop()
	if stopErr != nil {
		return stopErr
	}
	binding.serial.Lock()
	binding.reaped = true
	binding.serial.Unlock()
	m.clearBinding(binding)
	return nil
}

// ChangeModel reserves the HTTP operation before issuing stock Pi's set_model.
// The same row owns replay, crash recovery, and the atomic completion.
func (m *Manager) ChangeModel(ctx context.Context, request ModelChangeRequest) (ModelChangeResult, error) {
	if request.AgentID == "" || request.Method == "" || request.CanonicalPath == "" || request.RequestID == "" || request.CanonicalBodyHash == "" {
		return ModelChangeResult{}, errors.New("managed model change request is incomplete")
	}
	if err := ValidateSelectedModel(request.Model); err != nil {
		return ModelChangeResult{}, err
	}

	lane := m.laneFor(request.AgentID)
	lane.admission.Lock()
	defer lane.admission.Unlock()

	if existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); err == nil {
		return replayModelChange(request, existing)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ModelChangeResult{}, err
	}
	agent, err := m.store.ManagedAgent(ctx, request.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelChangeResult{}, &store.ManagedAgentNotFoundError{AgentID: request.AgentID}
	}
	if err != nil {
		return ModelChangeResult{}, err
	}
	binding, ok := m.bindingFor(request.AgentID)
	if !ok {
		return ModelChangeResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	binding.serial.Lock()
	if binding.initialReplayPending || binding.modelTruthUnknown || !m.liveBinding(agent, binding) {
		binding.serial.Unlock()
		return ModelChangeResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	fence := bindingFence(request.AgentID, binding)
	rpc := binding.rpc
	binding.serial.Unlock()

	var selected Model
	if selector, ok := m.catalog.(ModelSelector); ok {
		selected, err = selector.Select(ctx, &request.Model)
	} else {
		var catalog Catalog
		catalog, err = m.catalog.Catalog(ctx)
		if err == nil {
			selected, err = SelectModel(&request.Model, nil, catalog)
		}
	}
	if err != nil {
		return ModelChangeResult{}, err
	}

	pendingBody, _ := json.Marshal(map[string]any{"operation_id": request.RequestID, "status": "pending"})
	pending := store.ManagedOperation{
		OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath,
		RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "pending",
		ResponseStatus: 202, ResponseHeaders: modelOperationHeaders(request.RequestID), ResponseBody: pendingBody,
	}
	reserved, replayed, err := m.store.ReserveManagedModelChange(ctx, store.ManagedModelChangeReservation{Fence: fence, Operation: pending})
	if err != nil {
		return ModelChangeResult{}, err
	}
	if replayed {
		return ModelChangeResult{Operation: reserved, Replayed: true}, nil
	}

	native, ok := rpc.(NativeModelRPC)
	if !ok {
		return m.rejectManagedModelChange(fence, request, "MODEL_SWITCH_FAILED", "Managed model change was rejected")
	}
	actual, switchErr := native.SetModel(ctx, selected.Provider, selected.ID)
	if switchErr != nil || !sameModel(actual, selected) {
		state, stateErr := rpc.GetState(ctx)
		observed, observedOK := modelFromRPCState(state)
		if stateErr == nil && observedOK && sameModel(observed, agentModel(agent)) {
			return m.rejectManagedModelChange(fence, request, "MODEL_SWITCH_FAILED", "Managed model change was rejected")
		}
		return m.failAmbiguousManagedModelChange(binding, fence, request)
	}

	responseAgent := *agent
	responseAgent.SelectedModel = store.ManagedModel{Provider: actual.Provider, ID: actual.ID, Name: actual.Name}
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": PublicAgent(&responseAgent)})
	if err != nil {
		return ModelChangeResult{}, err
	}
	headers := modelOperationHeaders(request.RequestID)
	if err := m.store.CompleteManagedModelChange(ctx, fence, responseAgent.SelectedModel, request.RequestID, 200, headers, body); err != nil {
		binding.serial.Lock()
		binding.modelTruthUnknown = true
		binding.serial.Unlock()
		terminationErr := m.terminateUnpersistedModelRuntime(binding)
		m.publishProjection()
		return ModelChangeResult{}, &ModelStatePersistenceError{AgentID: request.AgentID, CommitErr: err, TerminationErr: terminationErr}
	}
	m.publishProjection()
	operation := &store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "terminal", ResponseStatus: 200, ResponseHeaders: headers, ResponseBody: body}
	return ModelChangeResult{Agent: &responseAgent, Operation: operation}, nil
}

func agentModel(agent *store.ManagedAgentRecord) Model {
	return Model{Provider: agent.SelectedModel.Provider, ID: agent.SelectedModel.ID, Name: agent.SelectedModel.Name}
}

func (m *Manager) rejectManagedModelChange(fence store.ManagedRuntimeFence, request ModelChangeRequest, code, message string) (ModelChangeResult, error) {
	body := modelFailureBody(request.RequestID, code, message)
	headers := modelOperationHeaders(request.RequestID)
	if err := m.store.RejectManagedModelChange(context.Background(), fence, request.RequestID, 409, headers, body); err != nil {
		binding, _ := m.bindingFor(request.AgentID)
		if binding != nil {
			binding.serial.Lock()
			binding.modelTruthUnknown = true
			binding.serial.Unlock()
			terminationErr := m.terminateUnpersistedModelRuntime(binding)
			return ModelChangeResult{}, &ModelStatePersistenceError{AgentID: request.AgentID, CommitErr: err, TerminationErr: terminationErr}
		}
		return ModelChangeResult{}, err
	}
	operation := &store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "terminal", ResponseStatus: 409, ResponseHeaders: headers, ResponseBody: body}
	return ModelChangeResult{Operation: operation}, nil
}

func (m *Manager) failAmbiguousManagedModelChange(binding *managedBinding, fence store.ManagedRuntimeFence, request ModelChangeRequest) (ModelChangeResult, error) {
	const code = "MODEL_SWITCH_UNCONFIRMED"
	const message = "Managed model change outcome could not be confirmed"
	body := modelFailureBody(request.RequestID, code, message)
	headers := modelOperationHeaders(request.RequestID)
	binding.serial.Lock()
	binding.modelTruthUnknown = true
	binding.serial.Unlock()
	if err := m.store.FailManagedModelChange(context.Background(), fence, request.RequestID, code, message, 500, headers, body); err != nil {
		terminationErr := m.terminateUnpersistedModelRuntime(binding)
		m.publishProjection()
		return ModelChangeResult{}, &ModelStatePersistenceError{AgentID: request.AgentID, CommitErr: err, TerminationErr: terminationErr}
	}
	_ = m.terminateUnpersistedModelRuntime(binding)
	m.publishProjection()
	operation := &store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, State: "terminal", ResponseStatus: 500, ResponseHeaders: headers, ResponseBody: body}
	return ModelChangeResult{Operation: operation}, nil
}

// RecoverPendingManagedModelChanges runs before every ordinary cold-start pass.
func (m *Manager) RecoverPendingManagedModelChanges(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := m.store.RecoverPendingManagedModelChanges(ctx); err != nil {
		return err
	}
	m.publishProjection()
	return nil
}
