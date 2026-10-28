package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedBlockedSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("action", ai.EnumOf("set", "clear")),
		ai.Opt("reason", ai.String("Explanation of why the worker is blocked and what assistance is required. Required when action is 'set'.")),
	)
	s.AdditionalAllowed = &closed
	return s
}

func (m *Manager) embeddedBlockedTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name: "mecha_blocked",
		Description: "Set or clear the agent's blocked execution state. " +
			"IMPORTANT: 'blocked' is NOT a workflow state; it is an agent execution state indicating that external human/user assistance is required in order to make any more progress towards a goal. " +
			"Never invoke mecha_blocked for ordinary workflow transitions, waiting on subagents, or debugging test failures. " +
			"It is reserved exclusively for conditions where autonomous progress is impossible without external input (e.g. missing credentials, ambiguous product decisions, broken external environment). " +
			"While blocked, automatic goal continuation loops are suspended until unblocked.",
		Parameters: embeddedBlockedSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("store unavailable for blocked state")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}

			action, ok := args["action"].(string)
			if !ok || strings.TrimSpace(action) == "" {
				return agent.AgentToolResult{}, errors.New("action is required for mecha_blocked ('set' or 'clear')")
			}

			agentID := owner.binding.AgentID
			if strings.TrimSpace(agentID) == "" {
				return agent.AgentToolResult{}, errors.New("agent identity unavailable")
			}

			switch strings.TrimSpace(action) {
			case "set":
				reasonVal, _ := args["reason"].(string)
				reason := strings.TrimSpace(reasonVal)
				if reason == "" {
					return agent.AgentToolResult{}, errors.New("reason is required when action is 'set'")
				}

				if err := m.store.SetAgentBlocked(ctx, agentID, true, reason); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("set agent blocked: %w", err)
				}
				m.publishProjection()

				_, _, blockedAt, err := m.store.GetAgentBlocked(ctx, agentID)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("read blocked state after set: %w", err)
				}

				resp := map[string]any{
					"ok":         true,
					"blocked":    true,
					"reason":     reason,
					"blocked_at": blockedAt,
					"warning":    "⚠️ Agent execution is now BLOCKED. Goal continuation loops are suspended until cleared. Note: 'blocked' is NOT a workflow state; it indicates external assistance is required to make further progress.",
				}
				respJSON, _ := json.Marshal(resp)
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: string(respJSON)},
					},
				}, nil

			case "clear":
				if err := m.store.SetAgentBlocked(ctx, agentID, false, ""); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("clear agent blocked: %w", err)
				}
				m.publishProjection()

				now := time.Now()
				if m.clock != nil {
					now = m.clock.Now()
				}
				nowStr := formatManagedTime(now)
				resp := map[string]any{
					"ok":         true,
					"blocked":    false,
					"cleared_at": nowStr,
					"message":    "Worker is unblocked. Autonomous goal evaluations and continuations have resumed.",
				}
				respJSON, _ := json.Marshal(resp)
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: string(respJSON)},
					},
				}, nil

			default:
				return agent.AgentToolResult{}, fmt.Errorf("unsupported mecha_blocked action %q (must be 'set' or 'clear')", action)
			}
		},
	}
}
