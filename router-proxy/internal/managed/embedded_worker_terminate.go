package managed

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

type workerSelfTerminateRequest struct {
	reason          string
	cleanupWorktree bool
}

func embeddedWorkerTerminateSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Opt("reason", ai.String("Optional reason for worker self-termination.")),
		ai.Opt("cleanup_worktree", ai.Boolean("Whether to clean up the temporary git worktree assigned to this worker. Defaults to true.")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) requestWorkerSelfTermination(agentID string, req workerSelfTerminateRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.workerSelfTerminateRequested == nil {
		m.workerSelfTerminateRequested = make(map[string]workerSelfTerminateRequest)
	}
	m.workerSelfTerminateRequested[agentID] = req
}

func (m *Manager) isWorkerSelfTerminationPending(agentID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.workerSelfTerminateRequested[agentID]
	return ok
}

func (m *Manager) embeddedWorkerTerminateTool(owner embeddedOwner, requested func(workerSelfTerminateRequest)) agent.AgentTool {
	return agent.AgentTool{
		Name:        "mecha_worker_terminate",
		Description: "Terminate self and optionally remove assigned worktree upon completing assignments.",
		Parameters:  embeddedWorkerTerminateSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("store unavailable for worker termination")
			}
			role, err := m.store.EmbeddedSessionRole(ctx, owner.binding)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("embedded session role: %w", err)
			}
			if role != "worker" {
				return agent.AgentToolResult{}, errors.New("mecha_worker_terminate is only permitted for worker agents")
			}

			reason := "worker self-terminated"
			if r, ok := args["reason"].(string); ok && strings.TrimSpace(r) != "" {
				reason = strings.TrimSpace(r)
			}

			cleanupWorktree := true
			if cw, ok := args["cleanup_worktree"].(bool); ok {
				cleanupWorktree = cw
			}

			req := workerSelfTerminateRequest{
				reason:          reason,
				cleanupWorktree: cleanupWorktree,
			}
			if requested != nil {
				requested(req)
			}

			statusMsg := fmt.Sprintf("Worker self-termination scheduled (reason: %q, cleanup_worktree: %t). Halting session.", reason, cleanupWorktree)
			return agent.AgentToolResult{
				Content: ai.ContentList{
					ai.TextContent{Text: statusMsg},
				},
				Terminate: true,
			}, nil
		},
	}
}

// checkWorkerSelfTerminationAfterTurn checks whether a self-termination was requested
// by the agent during its turn and kicks off asynchronous termination outside lane locks.
func (m *Manager) checkWorkerSelfTerminationAfterTurn(ctx context.Context, agentID string) bool {
	m.mu.Lock()
	req, ok := m.workerSelfTerminateRequested[agentID]
	m.mu.Unlock()

	if !ok {
		return false
	}

	task := func() {
		m.mu.Lock()
		delete(m.workerSelfTerminateRequested, agentID)
		m.mu.Unlock()

		reqID := uuid.New().String()
		_, err := m.Terminate(context.Background(), TerminateRequest{
			AgentID:           agentID,
			Method:            "DELETE",
			CanonicalPath:     "/v1/managed-agents/" + agentID,
			RequestID:         reqID,
			CanonicalBodyHash: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
			CleanupWorktree:   req.cleanupWorktree,
		})
		if err != nil {
			log.Printf("[embedded:%s] worker self-termination failed: %v", agentID, err)
		}
	}

	if !m.startManagedTask(task) {
		go task()
	}
	return true
}
