package managed

import (
	"context"
	"encoding/json"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (m *Manager) reserveEmbeddedModelSelection(ctx context.Context, request RestartRequest, agent *store.ManagedAgentRecord) (RestartResult, error) {
	lane := m.laneFor(request.AgentID)
	lane.selection.Lock()
	defer lane.selection.Unlock()
	// Refresh after taking the short admission fence, not after waiting on a provider.
	var err error
	agent, err = m.store.ManagedAgent(ctx, request.AgentID)
	if err != nil {
		return RestartResult{}, err
	}
	if agent.DesiredState != "running" || agent.LifecycleState != "running" || !agent.AcceptingPrompts || !agent.SlotHeld {
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	m.mu.Lock()
	owner, ok := m.embeddedOwners[request.AgentID]
	closed := m.closed
	m.mu.Unlock()
	if !ok || closed || m.runCtx.Err() != nil || owner.binding.SessionID != agent.PiSessionID || owner.binding.Epoch != agent.SessionEpoch || owner.binding.Generation != agent.RuntimeGeneration {
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	if request.Model != nil && engineOf(agent.SelectedModel.Provider) != engineOf(request.Model.Provider) {
		return RestartResult{}, &EngineSelectionError{Reason: "Cross-engine model changes are not supported; launch a new worker"}
	}
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
	if request.Model != nil && request.Model.Provider == claudeCodeProvider {
		selected, err = claudeCodeModel(request.Model.ID)
	} else if request.Model != nil && request.Model.Provider == antigravityProvider {
		selected, err = antigravityModel(request.Model.ID)
	}
	if err != nil {
		return RestartResult{}, err
	}
	selected, err = m.admitEmbeddedModel(ctx, selected)
	if err != nil {
		return RestartResult{}, err
	}
	if err := m.validateEmbeddedWorkingDirectory(agent.Cwd); err != nil {
		return RestartResult{}, err
	}
	model := store.ManagedModel{Provider: selected.Provider, ID: selected.ID, Name: selected.Name}
	responseAgent := *agent
	responseAgent.SelectedModel = model
	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": PublicAgent(&responseAgent)})
	if err != nil {
		return RestartResult{}, err
	}
	operation := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, ResponseBody: body}
	committed, replayed, err := m.store.ReserveEmbeddedModelSelection(ctx, request.AgentID, model, operation)
	if err != nil {
		return RestartResult{}, err
	}
	return RestartResult{Agent: &responseAgent, Operation: committed, Replayed: replayed}, nil
}
