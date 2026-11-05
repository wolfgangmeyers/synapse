package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (m *Manager) reserveEmbeddedReset(ctx context.Context, request ResetRequest, agent *store.ManagedAgentRecord) (ResetResult, error) {
	lane := m.laneFor(request.AgentID)
	lane.selection.Lock()
	defer lane.selection.Unlock()

	if existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); err == nil {
		if existing.CanonicalPath != request.CanonicalPath || existing.CanonicalBodyHash != request.CanonicalBodyHash {
			return ResetResult{}, &store.ManagedIdempotencyConflictError{}
		}
		return ResetResult{Operation: existing, Replayed: true}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ResetResult{}, err
	}

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
	active, ok := lane.beginEmbeddedReset()
	if !ok {
		return ResetResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	if m.embeddedResetReservationTestHook != nil {
		m.embeddedResetReservationTestHook()
	}
	if err := restartMailTestBarrier(ctx, request.AgentID, "reset"); err != nil {
		log.Printf("reset test barrier: %v", err)
	}
	committed, replayed, err := m.store.ReserveEmbeddedReset(ctx, request.AgentID, operation)
	if err != nil || replayed {
		m.endEmbeddedReset(request.AgentID, lane)
		return ResetResult{Operation: committed, Replayed: replayed}, err
	}
	resetCtx := m.runCtx
	if resetCtx == nil {
		resetCtx = context.Background()
	}
	if !m.startManagedTask(func() {
		if err := m.executePendingEmbeddedReset(resetCtx, owner.binding, "gear_popup_requested", active); err != nil {
			log.Printf("[embedded:%s] context reset failed: %v", request.AgentID, err)
		}
	}) {
		m.endEmbeddedReset(request.AgentID, lane)
	}
	return ResetResult{Agent: agent, Operation: committed, Replayed: replayed}, nil
}

// beginEmbeddedReset closes the short admission gate before interrupting. The
// caller must release admission before waiting on serial or engine callbacks.
func (lane *managedAgentLane) beginEmbeddedReset() (embeddedRuntime, bool) {
	lane.admission.Lock()
	defer lane.admission.Unlock()
	if lane.resetRunning {
		return nil, false
	}
	lane.resetPending = true
	lane.resetRunning = true
	lane.resetDone = make(chan struct{})
	lane.activeMu.RLock()
	defer lane.activeMu.RUnlock()
	return lane.activeSession, true
}

func (lane *managedAgentLane) endEmbeddedReset() {
	lane.admission.Lock()
	lane.resetPending = false
	lane.resetRunning = false
	if lane.resetDone != nil {
		close(lane.resetDone)
		lane.resetDone = nil
	}
	lane.admission.Unlock()
}

func (lane *managedAgentLane) embeddedResetPending() bool {
	lane.admission.Lock()
	defer lane.admission.Unlock()
	return (lane.resetPending || len(lane.resetToolGates) > 0) || lane.terminationPending > 0
}

// installEmbeddedRuntime closes the race between turn preparation and reset's
// active-handle capture. A denied runtime must be closed without running it.
func (lane *managedAgentLane) installEmbeddedRuntime(runtime embeddedRuntime, p *embeddedPersistence) bool {
	lane.admission.Lock()
	defer lane.admission.Unlock()
	if (lane.resetPending || len(lane.resetToolGates) > 0) || lane.terminationPending > 0 {
		return false
	}
	lane.activeMu.Lock()
	defer lane.activeMu.Unlock()
	lane.activeSession, lane.activePersistence = runtime, p
	return true
}

func (lane *managedAgentLane) removeEmbeddedRuntime(runtime embeddedRuntime) {
	lane.activeMu.Lock()
	defer lane.activeMu.Unlock()
	if lane.activeSession == runtime {
		lane.activeSession, lane.activePersistence = nil, nil
	}
}

// Transfer only deferred gates belonging to the binding just rotated. A fresh
// binding's tool request must remain independently gated.
func (lane *managedAgentLane) reopenEmbeddedResetAdmissionsForBinding(binding store.EmbeddedBinding) {
	lane.admission.Lock()
	for req := range lane.resetToolGates {
		if req.handoffBinding != nil && *req.handoffBinding == binding {
			delete(lane.resetToolGates, req)
		}
	}
	lane.resetPending = false
	lane.admission.Unlock()
}

func (lane *managedAgentLane) embeddedResetRunning() bool {
	lane.admission.Lock()
	defer lane.admission.Unlock()
	return lane.resetRunning
}

// Re-offer after releasing our gate. Dispatch rechecks every other gate and
// durable runnable state; no admission or serial lock is acquired here.
func (m *Manager) endEmbeddedReset(agentID string, lane *managedAgentLane) {
	lane.endEmbeddedReset()
	m.offerEmbeddedMail(agentID)
}

func (m *Manager) offerEmbeddedMail(agentID string) {
	m.startManagedTask(func() { m.dispatchEmbeddedMail(agentID) })
}
