package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedProgressSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("action", ai.EnumOf("set", "get", "clear")),
		ai.Opt("percent", ai.Integer("Progress percentage from 0 to 100 inclusive (required for set).")),
		ai.Opt("note", ai.String("Optional milestone note describing current progress (maximum 120 characters).")),
	)
	s.AdditionalAllowed = &closed
	return s
}

func (m *Manager) embeddedProgressTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "mecha_progress",
		Description: "Track and report measurable task progress. Supports 'set' (with percentage 0..100 and optional note up to 120 characters), 'get' (fetch current status and history count), and 'clear'.",
		Parameters:  embeddedProgressSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("store unavailable for progress")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}

			action, ok := args["action"].(string)
			if !ok || strings.TrimSpace(action) == "" {
				return agent.AgentToolResult{}, errors.New("action is required for progress (set, get, or clear)")
			}

			agentID := owner.binding.AgentID
			if strings.TrimSpace(agentID) == "" {
				return agent.AgentToolResult{}, errors.New("agent identity unavailable")
			}

			switch action {
			case "set":
				percentVal, exists := args["percent"]
				if !exists || percentVal == nil {
					return agent.AgentToolResult{}, errors.New("percent is required for action 'set'")
				}

				var pct int
				switch v := percentVal.(type) {
				case int:
					pct = v
				case int64:
					pct = int(v)
				case float64:
					if v != float64(int(v)) {
						return agent.AgentToolResult{}, fmt.Errorf("percent must be an integer between 0 and 100, got %v", v)
					}
					pct = int(v)
				case json.Number:
					parsed, err := v.Int64()
					if err != nil {
						return agent.AgentToolResult{}, fmt.Errorf("percent must be an integer between 0 and 100, got %v", v)
					}
					pct = int(parsed)
				default:
					return agent.AgentToolResult{}, errors.New("percent must be an integer between 0 and 100")
				}

				if pct < 0 || pct > 100 {
					return agent.AgentToolResult{}, fmt.Errorf("percent must be between 0 and 100 inclusive, got %d", pct)
				}

				note := ""
				if noteVal, ok := args["note"].(string); ok {
					note = noteVal
				}
				if utf8.RuneCountInString(note) > 120 {
					return agent.AgentToolResult{}, fmt.Errorf("note exceeds maximum length of 120 characters, got %d", utf8.RuneCountInString(note))
				}

				if err := m.store.SetAgentProgress(ctx, agentID, pct, note); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("set agent progress: %w", err)
				}
				m.publishProjection()

				// Fetch updated record to return exact timestamp
				pctPtr, noteOut, updatedAt, _, err := m.store.GetAgentProgress(ctx, agentID)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("read progress after set: %w", err)
				}
				resp := map[string]any{
					"ok":         true,
					"percent":    pct,
					"note":       noteOut,
					"updated_at": updatedAt,
				}
				if pctPtr != nil {
					resp["percent"] = *pctPtr
				}
				respJSON, _ := json.Marshal(resp)
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: string(respJSON)},
					},
				}, nil

			case "get":
				pct, note, updatedAt, count, err := m.store.GetAgentProgress(ctx, agentID)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("get agent progress: %w", err)
				}

				var resp map[string]any
				if pct == nil {
					resp = map[string]any{
						"ok":         true,
						"percent":    nil,
						"note":       nil,
						"updated_at": nil,
						"count":      count,
					}
				} else {
					resp = map[string]any{
						"ok":         true,
						"percent":    *pct,
						"note":       note,
						"updated_at": updatedAt,
						"count":      count,
					}
				}
				respJSON, _ := json.Marshal(resp)
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: string(respJSON)},
					},
				}, nil

			case "clear":
				if err := m.store.ClearAgentProgress(ctx, agentID); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("clear agent progress: %w", err)
				}
				m.publishProjection()
				resp := map[string]any{
					"ok":      true,
					"cleared": true,
				}
				respJSON, _ := json.Marshal(resp)
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: string(respJSON)},
					},
				}, nil

			default:
				return agent.AgentToolResult{}, fmt.Errorf("unsupported progress action %q (must be set, get, or clear)", action)
			}
		},
	}
}
