package managed

import (
	"context"
	"errors"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// The router owns one ordering boundary for REST and checked native writes.
// The mutation returns committed output plus its one-use publication claim.
func (m *Manager) SetEmbeddedUrgencyCoordinator(coordinate func(func() (store.AgentUrgencyResult, bool, error)) (store.AgentUrgencyResult, error)) {
	m.mu.Lock()
	m.embeddedUrgencyCoordinator = coordinate
	m.mu.Unlock()
}

func (m *Manager) embeddedAgentUrgencyTool(owner embeddedOwner, admitted func(string) *store.EmbeddedToolInvocation, completed func(string, *store.EmbeddedToolInvocation)) agent.AgentTool {
	targetSchema := embeddedBoundedString(128)
	minimum := 1
	targetSchema.MinLength = &minimum
	return agent.AgentTool{Name: "mecha_agent_urgency", Description: "Explicitly set or clear attention metadata for self or an exact agent_id when directed. Not priority or blocked state; do not infer urgency from failures.", Parameters: embeddedClosedObject(ai.Prop("urgent", ai.Boolean()), ai.Opt("agent_id", targetSchema)), Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		current, ok := ctx.Value(embeddedOwnerContextKey{}).(embeddedOwner)
		if !ok || current != owner {
			return agent.AgentToolResult{}, errors.New("urgency requires current runner context owner")
		}
		if _, err := store.AgentUrgencyArguments(args, owner.binding.AgentID); err != nil {
			return agent.AgentToolResult{}, err
		}
		w := admitted(id)
		if err := m.store.StartEmbeddedToolInvocation(ctx, w, ai.ToolCall{ID: id, Name: "mecha_agent_urgency", Arguments: args}); err != nil {
			return agent.AgentToolResult{}, err
		}
		if m.embeddedAfterIntent != nil {
			m.embeddedAfterIntent(ctx)
		}
		if err := m.authorizeEmbeddedWorkerLaunch(ctx, owner); err != nil {
			return agent.AgentToolResult{}, err
		}
		m.mu.Lock()
		coordinate := m.embeddedUrgencyCoordinator
		m.mu.Unlock()
		mutate := func() (store.AgentUrgencyResult, bool, error) {
			if m.embeddedBeforeToolDomain != nil {
				m.embeddedBeforeToolDomain()
			}
			result, err := m.store.SetEmbeddedAgentUrgency(ctx, w)
			return result, m.claimEmbeddedToolHandoff(w), err
		}
		var err error
		if coordinate != nil {
			_, err = coordinate(mutate)
		} else {
			var committed bool
			_, committed, err = mutate()
			if committed {
				m.publishProjection()
			}
		}
		if err != nil {
			store.RecordEmbeddedToolHandoffError(w, err)
			return agent.AgentToolResult{}, err
		}
		payload, err := store.EmbeddedToolResultContent(w)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		result := agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(payload)}}}
		if err := m.store.CompleteEmbeddedToolInvocation(ctx, w, ai.ToolResultMessage{ToolCallID: id, ToolName: "mecha_agent_urgency", Content: result.Content}); err != nil {
			return agent.AgentToolResult{}, err
		}
		completed(id, w)
		return result, nil
	}}
}
