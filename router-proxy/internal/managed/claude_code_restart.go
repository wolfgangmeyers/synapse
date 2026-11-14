package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"log"
)

// Embedded runtimes have no RPC process binding. Interrupt the active engine
// and retain checked history, ownership and accepted input in the serving lane.
func (m *Manager) reserveClaudeCodeRestart(ctx context.Context, request RestartRequest) (RestartResult, error) {
	lane := m.laneFor(request.AgentID)
	lane.selection.Lock()
	defer lane.selection.Unlock()
	if existing, err := m.store.FindManagedOperation(ctx, request.Method, request.RequestID); err == nil {
		return replayRestartOperation(request, existing)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return RestartResult{}, err
	}
	saved, err := m.store.ManagedAgent(ctx, request.AgentID)
	if err != nil {
		return RestartResult{}, err
	}
	m.mu.Lock()
	owner, hasOwner := m.embeddedOwners[request.AgentID]
	closed := m.closed
	m.mu.Unlock()
	if !hasOwner || closed || m.runCtx.Err() != nil || owner.binding.SessionID != saved.PiSessionID || owner.binding.Epoch != saved.SessionEpoch || owner.binding.Generation != saved.RuntimeGeneration {
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		if errors.Is(err, store.ErrStaleEmbeddedFence) {
			return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
		}
		return RestartResult{}, err
	}
	body, err := json.Marshal(map[string]interface{}{"operation_id": request.RequestID, "agent": PublicAgent(saved)})
	if err != nil {
		return RestartResult{}, err
	}
	operation := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, ResponseBody: body}
	active, ok := lane.beginEmbeddedReset(owner.binding)
	if !ok {
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	committed, replayed, err := m.store.ReserveEmbeddedReset(ctx, request.AgentID, operation)
	if err != nil || replayed {
		lane.endEmbeddedReset()
		return RestartResult{Operation: committed, Replayed: replayed}, err
	}
	// selection serializes the committed fence with the restart generation.
	lane.claudeRestartGeneration.Add(1)
	if !m.startManagedTask(func() {
		stopped := false
		defer func() {
			lane.endEmbeddedReset()
			if stopped {
				_ = restartMailTestBarrier(m.runCtx, request.AgentID, "restart-ready")
				m.startManagedTask(func() { m.dispatchEmbeddedMail(request.AgentID) })
			}
		}()
		if hasOwner && active != nil {
			active.Interrupt()
		}
		if err := m.claudeCodeProcesses.stopBinding(owner.binding); err != nil {
			log.Printf("[embedded:%s] restart stop failed: %v", request.AgentID, m.recordClaudeRestartStopFailure(owner, hasOwner, err))
			return
		}
		if err := restartMailTestBarrier(m.runCtx, request.AgentID, "restart"); err != nil {
			log.Printf("restart test barrier: %v", err)
			return
		}
		lane.serial.Lock()
		defer lane.serial.Unlock()
		// The old callbacks have settled. Future acquisition resumes this binding.
		if err := m.claudeCodeProcesses.stopBinding(owner.binding); err != nil {
			log.Printf("[embedded:%s] restart stop failed: %v", request.AgentID, m.recordClaudeRestartStopFailure(owner, hasOwner, err))
			return
		}
		stopped = true
	}) {
		lane.endEmbeddedReset()
	}
	return RestartResult{Agent: saved, Operation: committed}, nil
}
