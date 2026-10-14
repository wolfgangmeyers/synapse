package managed

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedCompactionSchema() *ai.Schema {
	closed := false
	schema := ai.Object(ai.Prop("action", ai.EnumOf("status")))
	schema.AdditionalAllowed = &closed
	return schema
}

// ManagedContextStatus provides a read-only view of context usage and reset boundaries.
type ManagedContextStatus struct {
	SessionID               string   `json:"session_id"`
	Epoch                   int64    `json:"epoch"`
	CurrentTokens           *int     `json:"current_tokens,omitempty"`
	ContextWindow           int      `json:"context_window"`
	Percent                 *float64 `json:"percent,omitempty"`
	WarningThresholdPercent int      `json:"warning_threshold_percent"`
	ResetThresholdPercent   int      `json:"reset_threshold_percent"`
	ScratchpadEntriesCount  int      `json:"scratchpad_entries_count"`
	System                  string   `json:"system"`
	Guidance                string   `json:"guidance"`
}

// embeddedCompactionTool exposes mecha_context for inspecting context usage and reset status.
// Compaction has been replaced by context reset + scratchpad.
func (m *Manager) embeddedCompactionTool(owner embeddedOwner, _ ...any) agent.AgentTool {
	return agent.AgentTool{
		Name:        "mecha_context",
		Description: "Read this session's context usage, threshold warnings, and context reset status.",
		Parameters:  embeddedCompactionSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if len(args) != 1 {
				return agent.AgentToolResult{}, errors.New("invalid context action: only action=status is supported")
			}
			action, ok := args["action"].(string)
			if !ok || action != "status" {
				return agent.AgentToolResult{}, errors.New("unsupported mecha_context action: compaction has been replaced by context reset and scratchpad; only action=status is supported")
			}

			warningPct := 70
			resetPct := 80
			status := ManagedContextStatus{
				SessionID:               owner.binding.SessionID,
				Epoch:                   owner.binding.Epoch,
				WarningThresholdPercent: warningPct,
				ResetThresholdPercent:   resetPct,
				System:                  "context_reset+scratchpad",
				Guidance:                "Context compaction is deprecated and has been replaced by context reset and scratchpad. Save key findings and plans to scratchpad before reaching the reset threshold. At the reset threshold, context history is automatically reset to fresh headroom while preserving scratchpad memory and recent conversation.",
			}

			if m.store != nil {
				if agentRec, err := m.store.ManagedAgent(ctx, owner.binding.AgentID); err == nil && agentRec != nil {
					status.CurrentTokens = agentRec.ContextTokens
					status.ContextWindow = agentRec.ContextWindow
					status.Percent = agentRec.ContextPercent
					if agentRec.ContextWindow >= 1_000_000 {
						status.WarningThresholdPercent = 40
						status.ResetThresholdPercent = 50
					}
				}

				if entries, totalCount, err := m.store.FetchScratchpadRecent(ctx, owner.binding.AgentID, 1); err == nil {
					if totalCount > 0 {
						status.ScratchpadEntriesCount = totalCount
					} else {
						status.ScratchpadEntriesCount = len(entries)
					}
				}
			}

			body, err := json.Marshal(status)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(body)}}}, nil
		},
	}
}
