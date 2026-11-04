package managed

import (
	"context"
	"encoding/json"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"log"
)

// Embedded Claude has no RPC process binding. Restart only its engine resource;
// checked history, ownership and accepted input remain with the serving lane.
func (m *Manager) reserveClaudeCodeRestart(ctx context.Context, request RestartRequest, saved *store.ManagedAgentRecord) (RestartResult, error) {
	body, err := json.Marshal(map[string]interface{}{"operation_id": request.RequestID, "agent": PublicAgent(saved)})
	if err != nil {
		return RestartResult{}, err
	}
	operation := store.ManagedOperation{OperationID: request.RequestID, Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash, ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, ResponseBody: body}
	m.mu.Lock()
	owner, hasOwner := m.embeddedOwners[request.AgentID]
	m.mu.Unlock()
	lane := m.laneFor(request.AgentID)
	lane.selection.Lock()
	defer lane.selection.Unlock()
	active, ok := lane.beginEmbeddedReset()
	if !ok {
		return RestartResult{}, &store.ManagedAgentNotReadyError{AgentID: request.AgentID}
	}
	committed, replayed, err := m.store.ReserveEmbeddedReset(ctx, request.AgentID, operation)
	if err != nil || replayed {
		lane.endEmbeddedReset()
		return RestartResult{Operation: committed, Replayed: replayed}, err
	}
	if !m.startManagedTask(func() {
		stopped := false
		defer func() {
			lane.endEmbeddedReset()
			if stopped {
				m.startManagedTask(func() { m.dispatchEmbeddedMail(request.AgentID) })
			}
		}()
		if active != nil {
			active.Interrupt()
		}
		if err := m.claudeCodeProcesses.stop(request.AgentID); err != nil {
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
		if err := m.claudeCodeProcesses.stop(request.AgentID); err != nil {
			log.Printf("[embedded:%s] restart stop failed: %v", request.AgentID, m.recordClaudeRestartStopFailure(owner, hasOwner, err))
			return
		}
		stopped = true
	}) {
		lane.endEmbeddedReset()
	}
	return RestartResult{Agent: saved, Operation: committed}, nil
}
