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

const (
	InlineToolPoolCapacity            = 3
	InlineToolReplenishmentIntervalMS = 60_000
	InlineToolBlockReason             = "Inline tool budget exhausted: you have used all available inline tool credits. Inline tools are intended only for quick, occasional actions; repeated inline tool use is disallowed. You must delegate this work to subagents instead. (Credits replenish at 1 credit per minute up to a maximum of 3, or inline tools can be toggled on.)"
	InlineToolAllowanceToolName       = "mecha_request_inline_tool_allowance"
	InlineToolAllowanceToolDesc       = "Request a temporary exception for genuinely necessary inline use of a named tool. This is an exception to the rule; subagents remain mandatory by default."
)

type InlineAllowanceDetails struct {
	ToolName              string  `json:"toolName"`
	UsageCount            int     `json:"usageCount"`
	GrantedCount          int     `json:"grantedCount"`
	Remaining             int     `json:"remaining"`
	AvailableWholeCredits int     `json:"availableWholeCredits"`
	RemainingPool         float64 `json:"remainingPool"`
}

func normalizeInlineToolName(toolName string) (string, error) {
	trimmed := strings.TrimSpace(toolName)
	if trimmed == "" {
		return "", errors.New("toolName must be a non-empty string")
	}
	normalized := strings.TrimPrefix(trimmed, "functions.")
	if normalized == "bash" || normalized == "edit" || normalized == "write" {
		return normalized, nil
	}
	return "", fmt.Errorf("unsupported inline tool name: %s; expected bash, edit, or write", trimmed)
}

func normalizeInlineActionTool(toolName string) (string, bool) {
	trimmed := strings.TrimSpace(toolName)
	normalized := strings.TrimPrefix(trimmed, "functions.")
	if normalized == "bash" || normalized == "edit" || normalized == "write" {
		return normalized, true
	}
	return "", false
}

func (l *managedAgentLane) replenishInlinePool(now time.Time) {
	if l.inlinePoolUpdatedAt.IsZero() {
		l.inlinePoolRemainingMs = InlineToolPoolCapacity * InlineToolReplenishmentIntervalMS
		l.inlinePoolUpdatedAt = now
		return
	}
	elapsedMs := now.Sub(l.inlinePoolUpdatedAt).Milliseconds()
	if elapsedMs < 0 {
		elapsedMs = 0
	}
	maxPoolMs := int64(InlineToolPoolCapacity * InlineToolReplenishmentIntervalMS)
	l.inlinePoolRemainingMs = l.inlinePoolRemainingMs + elapsedMs
	if l.inlinePoolRemainingMs > maxPoolMs {
		l.inlinePoolRemainingMs = maxPoolMs
	}
	l.inlinePoolUpdatedAt = now
}

func (l *managedAgentLane) grantInlineAllowance(toolName string, usageCount int, now time.Time) (InlineAllowanceDetails, error) {
	l.activeMu.Lock()
	defer l.activeMu.Unlock()

	l.replenishInlinePool(now)
	availableWholeCredits := int(l.inlinePoolRemainingMs / InlineToolReplenishmentIntervalMS)
	remainingPool := float64(l.inlinePoolRemainingMs) / float64(InlineToolReplenishmentIntervalMS)

	if l.inlineAllowances == nil {
		l.inlineAllowances = make(map[string]int)
	}

	if availableWholeCredits < usageCount {
		exhaustedText := ""
		if availableWholeCredits == 0 && l.inlinePoolRemainingMs == 0 {
			exhaustedText = " The pool is exhausted."
		}
		errMsg := fmt.Sprintf("Insufficient inline tool credits: requested %d credit(s), but only %d whole credit(s) are available (pool currently holds %v credit(s)).%s No credits were granted. Credits replenish at 1 credit per minute up to a maximum of 3 credits.\nATTENTION: you are probably getting this error because you are ABUSING the inline allowance escape hatch tool. You need to be using subagents instead.\n\nRead that again: YOU SHOULD BE USING SUBAGENTS INSTEAD. Continue to ignore this mandatory policy and the inline tool allowance will be disabled.", usageCount, availableWholeCredits, remainingPool, exhaustedText)
		return InlineAllowanceDetails{
			ToolName:              toolName,
			UsageCount:            usageCount,
			GrantedCount:          0,
			Remaining:             l.inlineAllowances[toolName],
			AvailableWholeCredits: availableWholeCredits,
			RemainingPool:         remainingPool,
		}, errors.New(errMsg)
	}

	grantedCount := usageCount
	l.inlinePoolRemainingMs -= int64(grantedCount * InlineToolReplenishmentIntervalMS)
	l.inlineAllowances[toolName] += grantedCount
	remaining := l.inlineAllowances[toolName]

	return InlineAllowanceDetails{
		ToolName:              toolName,
		UsageCount:            usageCount,
		GrantedCount:          grantedCount,
		Remaining:             remaining,
		AvailableWholeCredits: int(l.inlinePoolRemainingMs / InlineToolReplenishmentIntervalMS),
		RemainingPool:         float64(l.inlinePoolRemainingMs) / float64(InlineToolReplenishmentIntervalMS),
	}, nil
}

func (l *managedAgentLane) consumeInlineAllowance(toolName string, now time.Time) bool {
	if l == nil {
		return false
	}
	l.activeMu.Lock()
	defer l.activeMu.Unlock()

	if l.inlineAllowances != nil && l.inlineAllowances[toolName] > 0 {
		l.inlineAllowances[toolName]--
		return true
	}

	l.replenishInlinePool(now)
	if l.inlinePoolRemainingMs >= InlineToolReplenishmentIntervalMS {
		l.inlinePoolRemainingMs -= InlineToolReplenishmentIntervalMS
		return true
	}

	return false
}

func (l *managedAgentLane) getInlineAllowance(toolName string) int {
	if l == nil {
		return 0
	}
	l.activeMu.RLock()
	defer l.activeMu.RUnlock()
	if l.inlineAllowances == nil {
		return 0
	}
	return l.inlineAllowances[toolName]
}

func embeddedRequestInlineToolAllowanceSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("toolName", ai.String("Canonical tool name (bash, edit, or write), or its functions.* alias.")),
		ai.Prop("usageCount", ai.Integer("Number of temporary uses to grant, from 1 through 3.")),
	)
	s.AdditionalAllowed = &closed
	return s
}

func (m *Manager) embeddedRequestInlineToolAllowanceTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        InlineToolAllowanceToolName,
		Description: InlineToolAllowanceToolDesc,
		Parameters:  embeddedRequestInlineToolAllowanceSchema(),
		PromptGuidelines: []string{
			"Use mecha_request_inline_tool_allowance only for genuinely necessary top-level bash/edit/write work.",
		},
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if owner.binding.AgentID != "" && owner.identity != "" && m != nil && m.store != nil {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}
			// Native subagents are exempt from the inline tool guard; no parent allowance was changed.
			if m != nil && m.store != nil && owner.binding.AgentID != "" {
				if storedAgent, err := m.store.ManagedAgent(ctx, owner.binding.AgentID); err == nil && storedAgent != nil {
					if storedAgent.ParentAgentID != "" {
						return agent.AgentToolResult{
							Content: ai.ContentList{
								ai.TextContent{Text: "Native subagents are exempt from the inline tool guard; no parent allowance was changed."},
							},
						}, nil
					}
				}
			}

			var rawToolName string
			if val, ok := args["toolName"].(string); ok && val != "" {
				rawToolName = val
			} else if val, ok := args["tool_name"].(string); ok && val != "" {
				rawToolName = val
			}

			var usageCount int
			hasUsageCount := false
			if val, ok := args["usageCount"]; ok && val != nil {
				usageCount, hasUsageCount = parseUsageCount(val)
			} else if val, ok := args["usage_count"]; ok && val != nil {
				usageCount, hasUsageCount = parseUsageCount(val)
			}

			if rawToolName == "" || !hasUsageCount {
				err := errors.New("toolName and usageCount are required")
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: err.Error()},
					},
				}, err
			}

			normalized, normErr := normalizeInlineToolName(rawToolName)
			if normErr != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: normErr.Error()},
					},
				}, normErr
			}

			if usageCount < 1 || usageCount > 3 {
				err := errors.New("usageCount must be an integer from 1 through 3")
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: err.Error()},
					},
				}, err
			}

			if m != nil && m.IsInlineToolsAllowed(ctx, owner.binding.AgentID) {
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: fmt.Sprintf("Inline tools are unrestricted for this agent; calls to %s are permitted freely without requiring an allowance.", normalized)},
					},
					Details: InlineAllowanceDetails{
						ToolName:              normalized,
						UsageCount:            usageCount,
						GrantedCount:          usageCount,
						Remaining:             999999,
						AvailableWholeCredits: InlineToolPoolCapacity,
						RemainingPool:         float64(InlineToolPoolCapacity),
					},
				}, nil
			}

			lane := m.laneFor(owner.binding.AgentID)
			grant, err := lane.grantInlineAllowance(normalized, usageCount, time.Now())
			if err != nil {
				return agent.AgentToolResult{
					Content: ai.ContentList{
						ai.TextContent{Text: err.Error()},
					},
					Details: grant,
				}, err
			}

			text := fmt.Sprintf("Granted %d temporary inline use(s) for %s. This allowance is an exception to the rule; subagents remain mandatory by default.", grant.GrantedCount, grant.ToolName)
			return agent.AgentToolResult{
				Content: ai.ContentList{
					ai.TextContent{Text: text},
				},
				Details: grant,
			}, nil
		},
	}
}

func parseUsageCount(val any) (int, bool) {
	switch v := val.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if v == float64(int(v)) {
			return int(v), true
		}
		return 0, false
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n), true
		}
		return 0, false
	default:
		return 0, false
	}
}
