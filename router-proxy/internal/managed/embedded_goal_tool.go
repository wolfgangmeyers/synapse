package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// The SDK selects only an action and, for set, text. The saved owner supplies
// the subject and settings; model arguments cannot select another identity.
func embeddedGoalSchema() *ai.Schema {
	closed := false
	s := ai.Object(ai.Prop("action", ai.EnumOf("set", "status", "off")), ai.Opt("goal", ai.String()))
	s.AdditionalAllowed = &closed
	return s
}

func embeddedGoalCommand(args map[string]any) (store.EmbeddedGoalCommand, error) {
	var c store.EmbeddedGoalCommand
	if len(args) < 1 || len(args) > 2 {
		return c, errors.New("invalid goal arguments")
	}
	action, ok := args["action"].(string)
	if !ok {
		return c, errors.New("invalid goal action")
	}
	c.Action = action
	switch action {
	case "set":
		text, ok := args["goal"].(string)
		if !ok || len(args) != 2 || !utf8.ValidString(text) || len(text) > 4000 || strings.TrimSpace(text) == "" {
			return c, errors.New("invalid goal text")
		}
		c.Goal, c.Settings = text, store.DefaultManagedGoalSettings()
	case "status", "off":
		if len(args) != 1 {
			return c, errors.New("goal action takes no text")
		}
	default:
		return c, errors.New("invalid goal action")
	}
	return c, nil
}

func (m *Manager) embeddedGoalTool(owner embeddedOwner, admitted func(string) *store.EmbeddedGoalInvocation, completed func(string, *store.EmbeddedGoalInvocation, ai.ToolResultMessage)) agent.AgentTool {
	return agent.AgentTool{Name: "goal", Description: "Set, inspect or turn off this saved embedded session's own goal.", Parameters: embeddedGoalSchema(), Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		if m.embeddedAfterIntent != nil {
			m.embeddedAfterIntent(ctx) // test-only barrier after SDK intent, before policy transaction
		}
		if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return agent.AgentToolResult{}, err
		}
		command, err := embeddedGoalCommand(args)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if command.Action == "set" && m.store != nil {
			command.Goal = m.store.SubstituteAndExpandText(ctx, command.Goal, nil)
		}
		if command.Action == "status" {
			policy, _, err := m.store.ApplyEmbeddedGoalCall(ctx, owner.binding, id, command)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			payload, err := json.Marshal(policy)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			result := agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(payload)}}}
			completed(id, nil, ai.ToolResultMessage{ToolCallID: id, ToolName: "goal", Content: result.Content})
			return result, nil
		}
		invocation := admitted(id)
		if invocation == nil {
			return agent.AgentToolResult{}, errors.New("goal call was not freshly admitted")
		}
		activeWorkflowState, sErr := m.store.GetManagedAgentActiveWorkflowState(ctx, owner.binding.AgentID)
		if sErr == nil && activeWorkflowState != nil && activeWorkflowState.Goal != "" {
			return agent.AgentToolResult{}, fmt.Errorf("goal is locked by active workflow state %q", activeWorkflowState.Name)
		}
		if err := m.store.CheckEmbeddedGoalInvocation(ctx, invocation); err != nil {
			return agent.AgentToolResult{}, err
		}
		effect, err := m.store.RecoverEmbeddedEffect(ctx, owner.binding, id)
		if err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("goal intent unknown: %w", err)
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
		currentArgs, encodeErr := json.Marshal(args)
		if err != nil || encodeErr != nil || original.ID != id || original.Name != "goal" || !bytes.Equal(originalArgs, currentArgs) || effect.State != "unknown" {
			return agent.AgentToolResult{}, errors.New("goal call conflicts with admitted original intent")
		}
		policy, replay, err := m.store.ApplyCurrentEmbeddedGoalCall(ctx, owner.binding, id, command, invocation)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if command.Action == "set" {
			lane := m.laneFor(owner.binding.AgentID)
			lane.consecutiveEmptyTurns = 0
			lane.goalAttempt = 0
			lane.consecutiveContinuations = 0
		}
		if m.embeddedAfterGoalCommit != nil {
			m.embeddedAfterGoalCommit(ctx)
		}
		if replay {
			return agent.AgentToolResult{}, errors.New("goal outcome already recorded: SDK result unknown")
		}
		payload, err := json.Marshal(policy)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		result := agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(payload)}}}
		expected := ai.ToolResultMessage{ToolCallID: id, ToolName: "goal", Content: result.Content}
		if err := m.store.CompleteEmbeddedGoalInvocation(ctx, invocation, expected); err != nil {
			return agent.AgentToolResult{}, err
		}
		completed(id, invocation, expected)
		return result, nil
	}}
}
