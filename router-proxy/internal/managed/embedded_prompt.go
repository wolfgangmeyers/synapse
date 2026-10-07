package managed

import (
	"context"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// AdmitPromptWithRequestID is the embedded public turn admission with a durable
// retry identity. Stock RPC admission retains its existing behavior.
func (m *Manager) AdmitPromptWithRequestID(ctx context.Context, agentID, message, requestID string) error {
	initial, err := m.store.ManagedAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if initial.Runtime != "embedded" {
		return m.AdmitPrompt(ctx, agentID, message)
	}
	lane := m.laneFor(agentID)
	lane.consecutiveContinuations = 0
	lane.consecutiveEmptyTurns = 0
	lane.goalAttempt = 0
	lane.admission.Lock()
	defer lane.admission.Unlock()
	lane.serial.Lock()
	defer lane.serial.Unlock()
	lane.selection.Lock()
	selectionHeld := true
	defer func() {
		if selectionHeld {
			lane.selection.Unlock()
		}
	}()
	saved, err := m.store.ManagedAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if saved.Runtime != "embedded" {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	if saved.LifecycleState != "running" || saved.DesiredState != "running" || !saved.AcceptingPrompts || !saved.SlotHeld {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	m.mu.Lock()
	owner, ok := m.embeddedOwners[agentID]
	closed := m.closed
	m.mu.Unlock()
	if !ok || closed || m.runCtx.Err() != nil || owner.binding.AgentID != agentID || owner.binding.SessionID != saved.PiSessionID || owner.binding.Epoch != saved.SessionEpoch || owner.binding.Generation != saved.RuntimeGeneration {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		return &store.ManagedAgentNotReadyError{AgentID: agentID}
	}
	replay, err := m.store.ReserveEmbeddedPrompt(ctx, owner.binding, requestID, message)
	lane.selection.Unlock()
	selectionHeld = false
	if err != nil {
		return err
	}
	if replay {
		return nil
	}
	turnCtx, cancel := context.WithCancel(m.runCtx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	runCtx := context.WithValue(turnCtx, embeddedOwnerContextKey{}, owner)
	if err := m.runEmbeddedSessionPrompt(runCtx, *saved, message); err != nil {
		return err
	}
	if err := m.store.CompleteEmbeddedPrompt(ctx, owner.binding, requestID); err != nil {
		return err
	}
	return nil
}
