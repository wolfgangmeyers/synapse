package managed

import (
	"context"
	"encoding/json"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (m *Manager) reserveEmbeddedReset(ctx context.Context, request ResetRequest, agent *store.ManagedAgentRecord) (ResetResult, error) {
	lane := m.laneFor(request.AgentID)
	lane.selection.Lock()
	defer lane.selection.Unlock()

	var err error
	agent, err = m.store.ManagedAgent(ctx, request.AgentID)
	if err != nil {
		return ResetResult{}, err
	}
	if agent.DesiredState != "running" || agent.LifecycleState != "running" || !agent.AcceptingPrompts || !agent.SlotHeld {
		return ResetResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	m.mu.Lock()
	owner, ok := m.embeddedOwners[request.AgentID]
	closed := m.closed
	m.mu.Unlock()
	if !ok || closed || m.runCtx.Err() != nil || owner.binding.SessionID != agent.PiSessionID || owner.binding.Epoch != agent.SessionEpoch || owner.binding.Generation != agent.RuntimeGeneration {
		return ResetResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		return ResetResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}

	body, err := json.Marshal(map[string]any{"operation_id": request.RequestID, "agent": PublicAgent(agent)})
	if err != nil {
		return ResetResult{}, err
	}
	operation := store.ManagedOperation{
		OperationID:       request.RequestID,
		Method:            request.Method,
		CanonicalPath:     request.CanonicalPath,
		RequestID:         request.RequestID,
		CanonicalBodyHash: request.CanonicalBodyHash,
		ResponseStatus:    202,
		ResponseHeaders:   map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID},
		ResponseBody:      body,
	}
	committed, replayed, err := m.store.ReserveEmbeddedReset(ctx, request.AgentID, operation)
	if err != nil {
		return ResetResult{}, err
	}
	if !replayed {
		resetCtx := m.runCtx
		if resetCtx == nil {
			resetCtx = context.Background()
		}
		m.startManagedTask(func() {
			_ = m.ExecuteContextReset(resetCtx, owner.binding, "gear_popup_requested")
		})
	}
	return ResetResult{Agent: agent, Operation: committed, Replayed: replayed}, nil
}
