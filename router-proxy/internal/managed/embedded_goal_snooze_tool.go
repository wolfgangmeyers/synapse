package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedGoalSnoozeSchema() *ai.Schema {
	closed := false
	s := ai.Object(ai.Prop("duration_ms", ai.Integer()))
	s.AdditionalAllowed = &closed
	return s
}

// The parser runs before SDK intent admission, using the original arguments.
func embeddedGoalSnoozeArguments(args map[string]any, limitMsOpt ...int64) ([]byte, error) {
	if args == nil {
		args = make(map[string]any)
	}
	limitMs := store.DefaultManagedGoalSettings().SnoozeDurationMS
	if len(limitMsOpt) > 0 && limitMsOpt[0] > 0 {
		limitMs = limitMsOpt[0]
	}
	if limitMs > store.EmbeddedGoalSnoozeMaxDurationMS {
		limitMs = store.EmbeddedGoalSnoozeMaxDurationMS
	}

	clampOrKeep := func(d int64) int64 {
		if d > limitMs {
			return limitMs
		}
		return d
	}

	if rawVal, ok := args["duration_ms"]; ok {
		if rawVal == nil {
			args["duration_ms"] = limitMs
		} else {
			switch v := rawVal.(type) {
			case float64:
				d := int64(v)
				if d > 0 {
					args["duration_ms"] = clampOrKeep(d)
				}
			case int:
				d := int64(v)
				if d > 0 {
					args["duration_ms"] = clampOrKeep(d)
				}
			case int64:
				if v > 0 {
					args["duration_ms"] = clampOrKeep(v)
				}
			case json.Number:
				if i, err := v.Int64(); err == nil && i > 0 {
					args["duration_ms"] = clampOrKeep(i)
				}
			}
		}
	} else if rawVal, ok := args["duration"]; ok {
		delete(args, "duration")
		if rawVal == nil {
			args["duration_ms"] = limitMs
		} else {
			switch v := rawVal.(type) {
			case float64:
				d := int64(v)
				if d > 0 {
					d = clampOrKeep(d)
				}
				args["duration_ms"] = d
			case int:
				d := int64(v)
				if d > 0 {
					d = clampOrKeep(d)
				}
				args["duration_ms"] = d
			case int64:
				d := v
				if d > 0 {
					d = clampOrKeep(d)
				}
				args["duration_ms"] = d
			case json.Number:
				if i, err := v.Int64(); err == nil {
					d := i
					if d > 0 {
						d = clampOrKeep(d)
					}
					args["duration_ms"] = d
				}
			}
		}
	} else {
		args["duration_ms"] = limitMs
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, &store.EmbeddedGoalSnoozeDenial{Reason: "invalid arguments"}
	}
	if _, err := store.ParseEmbeddedGoalSnoozeArguments(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (m *Manager) goalSnoozeLimitMS(ctx context.Context, agentID string) int64 {
	limitMs := store.DefaultManagedGoalSettings().SnoozeDurationMS
	if m != nil && m.store != nil && agentID != "" {
		policy, err := m.store.ManagedGoalPolicyFor(ctx, agentID)
		if err == nil && policy != nil && policy.Config.Settings.SnoozeDurationMS > 0 {
			limitMs = policy.Config.Settings.SnoozeDurationMS
		}
	}
	if limitMs > store.EmbeddedGoalSnoozeMaxDurationMS {
		limitMs = store.EmbeddedGoalSnoozeMaxDurationMS
	}
	return limitMs
}

func (m *Manager) embeddedGoalSnoozeTool(owner embeddedOwner, turn func() store.EmbeddedGoalSnoozeTurn, admitted func(string) *store.EmbeddedGoalInvocation, completed func(string, *store.EmbeddedGoalInvocation, ai.ToolResultMessage)) agent.AgentTool {
	return agent.AgentTool{Name: "goal_snooze", Description: "Set a delay preference for this embedded session's current goal and initiating turn (up to 24 hours).", Parameters: embeddedGoalSnoozeSchema(), Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		if m.embeddedAfterIntent != nil {
			m.embeddedAfterIntent(ctx)
		}
		invocation := admitted(id)
		if invocation == nil {
			return agent.AgentToolResult{}, errors.New("goal snooze call was not freshly admitted")
		}
		if err := m.store.CheckEmbeddedGoalInvocation(ctx, invocation); err != nil {
			return agent.AgentToolResult{}, err
		}
		if args == nil {
			args = make(map[string]any)
		}
		limitMs := m.goalSnoozeLimitMS(ctx, owner.binding.AgentID)
		raw, err := embeddedGoalSnoozeArguments(args, limitMs)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return agent.AgentToolResult{}, err
		}
		effect, err := m.store.RecoverEmbeddedEffect(ctx, owner.binding, id)
		if err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("goal snooze intent unknown: %w", err)
		}
		var original struct {
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(effect.Intent, &original); err != nil {
			return agent.AgentToolResult{}, err
		}
		originalArgs, err := json.Marshal(original.Arguments)
		if err != nil || original.ID != id || original.Name != "goal_snooze" || !bytes.Equal(originalArgs, raw) || effect.State != "unknown" {
			return agent.AgentToolResult{}, errors.New("goal snooze call conflicts with admitted original intent")
		}
		goal, err := m.store.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		initiatingTurn := turn()
		initiatingTurn.GoalRevision = goal.DesiredRevision
		policy, replay, err := m.store.SaveCurrentEmbeddedGoalSnoozePreference(ctx, owner.binding, initiatingTurn, id, raw, invocation)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if replay {
			return agent.AgentToolResult{}, errors.New("goal snooze outcome already recorded: SDK result unknown")
		}
		if policy.DueAt != "" {
			_ = m.store.SetManagedGoalSnoozeUntil(ctx, owner.binding.AgentID, policy.DueAt)
		}
		if m.embeddedAfterGoalCommit != nil {
			m.embeddedAfterGoalCommit(ctx)
		}
		payload, err := json.Marshal(policy)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		result := agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(payload)}}}
		expected := ai.ToolResultMessage{ToolCallID: id, ToolName: "goal_snooze", Content: result.Content}
		if err := m.store.CompleteEmbeddedGoalInvocation(ctx, invocation, expected); err != nil {
			return agent.AgentToolResult{}, err
		}
		completed(id, invocation, expected)
		return result, nil
	}}
}
