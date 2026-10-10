package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedToolTimeoutSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Opt("duration_minutes", ai.Integer("Duration in minutes to temporarily extend the tool execution timeout (optional, defaults to 30, maximum 30).")),
		ai.Opt("reason", ai.String("Why the agent needs longer tool execution (optional).")),
	)
	s.AdditionalAllowed = &closed
	return s
}

func (m *Manager) embeddedToolTimeoutTool(owner embeddedOwner, toolName string) agent.AgentTool {
	return agent.AgentTool{
		Name:        toolName,
		Description: "Temporarily extend tool execution timeout up to 30 minutes for long-running commands. Reverts automatically to 5 minutes after the duration expires.",
		Parameters:  embeddedToolTimeoutSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if owner.binding.AgentID != "" && owner.identity != "" && m != nil && m.store != nil {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}
			durationMinutes := 30
			if val, ok := args["duration_minutes"]; ok && val != nil {
				switch v := val.(type) {
				case int:
					durationMinutes = v
				case float64:
					durationMinutes = int(v)
				case int64:
					durationMinutes = int(v)
				case json.Number:
					if n, err := v.Int64(); err == nil {
						durationMinutes = int(n)
					}
				}
			}
			lane := m.laneFor(owner.binding.AgentID)
			extendedUntil, durationMinutes := lane.extendToolTimeout(durationMinutes)
			output := fmt.Sprintf("Tool timeout temporarily extended to %d minutes (until %s). Limit will automatically revert to 5 minutes thereafter.", durationMinutes, extendedUntil.Format(time.RFC3339))
			return agent.AgentToolResult{
				Content: ai.ContentList{
					ai.TextContent{Text: output},
				},
			}, nil
		},
	}
}

func (m *Manager) wrapEmbeddedToolTimeout(agentID string, tool agent.AgentTool) agent.AgentTool {
	original := tool.Execute
	if original == nil {
		return tool
	}
	tool.Execute = func(callCtx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		timeout := 5 * time.Minute
		if m != nil {
			lane := m.laneFor(agentID)
			if lane != nil {
				timeout = lane.toolTimeout()
			}
		}
		timeoutCtx, cancel := context.WithTimeout(callCtx, timeout)
		defer cancel()

		res, err := original(timeoutCtx, id, args, update)
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return agent.AgentToolResult{}, fmt.Errorf("tool execution timed out (limit: %v)", timeout)
		}
		if timeoutCtx.Err() != nil && err == nil {
			return agent.AgentToolResult{}, timeoutCtx.Err()
		}
		return res, err
	}
	return tool
}
