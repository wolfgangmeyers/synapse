// Package managed provides lifecycle, execution, and tooling management for embedded agents.
// The workflow_snooze tool allows agents running active workflows to snooze workflow
// continuation turns, defaulting to the workflow's configured snooze_duration_ms
// (or 10 minutes) when no duration is specified.
package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedWorkflowSnoozeSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Opt("duration_minutes", ai.Integer()),
		ai.Opt("duration_ms", ai.Integer()),
		ai.Opt("reason", ai.String()),
	)
	s.AdditionalAllowed = &closed
	return s
}

func embeddedWorkflowSnoozeArguments(args map[string]any, defaultMs int64) ([]byte, int64, error) {
	if args == nil {
		args = make(map[string]any)
	}

	// Validate float conversion at the SDK's map boundary before JSON encoding.
	for _, value := range args {
		if v, ok := value.(float64); ok && (math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < -9223372036854775808.0 || v >= 9223372036854775808.0) {
			return nil, 0, errors.New("duration must be a finite representable integer")
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, 0, err
	}
	return store.NormalizeEmbeddedWorkflowSnoozeArguments(raw, defaultMs)
}

// embeddedWorkflowSnoozeTool returns an agent tool allowing workers running an active workflow
// to snooze workflow continuation turns. It sets a delay preference on the agent's goal policy,
// defaulting to the workflow's configured snooze_duration_ms (or 10 minutes) when no duration is specified.
func (m *Manager) embeddedWorkflowSnoozeTool(owner embeddedOwner, turn func() store.EmbeddedGoalSnoozeTurn, admitted func(string) *store.EmbeddedGoalInvocation, completed func(string, *store.EmbeddedGoalInvocation, ai.ToolResultMessage)) agent.AgentTool {
	return agent.AgentTool{
		Name:        "workflow_snooze",
		Description: "Set a delay preference for active workflow continuation (up to 24 hours). Defaults to the active workflow's configured snooze duration.",
		Parameters:  embeddedWorkflowSnoozeSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m.embeddedAfterIntent != nil {
				m.embeddedAfterIntent(ctx)
			}
			invocation := admitted(id)
			if invocation == nil {
				return agent.AgentToolResult{}, errors.New("workflow snooze call was not freshly admitted")
			}
			if err := m.store.CheckEmbeddedGoalInvocation(ctx, invocation); err != nil {
				return agent.AgentToolResult{}, err
			}
			if args == nil {
				args = make(map[string]any)
			}

			raw, _, err := embeddedWorkflowSnoozeArguments(args, 600000)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
				return agent.AgentToolResult{}, err
			}
			effect, err := m.store.RecoverEmbeddedEffect(ctx, owner.binding, id)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("workflow snooze intent unknown: %w", err)
			}
			var original struct {
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(effect.Intent, &original); err != nil {
				return agent.AgentToolResult{}, err
			}
			originalArgs, _, err := store.NormalizeEmbeddedWorkflowSnoozeArguments(original.Arguments, 600000)
			if err != nil || original.ID != id || original.Name != "workflow_snooze" || !bytes.Equal(originalArgs, raw) || effect.State != "unknown" {
				return agent.AgentToolResult{}, errors.New("workflow snooze call conflicts with admitted original intent")
			}
			goal, err := m.store.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			initiatingTurn := turn()
			initiatingTurn.GoalRevision = goal.DesiredRevision

			policy, replay, err := m.store.SaveCurrentEmbeddedWorkflowSnoozePreference(ctx, owner.binding, initiatingTurn, id, raw, invocation)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if replay {
				return agent.AgentToolResult{}, errors.New("workflow snooze outcome already recorded: SDK result unknown")
			}
			if m.embeddedAfterGoalCommit != nil {
				m.embeddedAfterGoalCommit(ctx)
			}

			payload, err := policy.WorkflowResultJSON()
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			result := agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(payload)}}}
			expected := ai.ToolResultMessage{ToolCallID: id, ToolName: "workflow_snooze", Content: result.Content}
			if err := m.store.CompleteEmbeddedGoalInvocation(ctx, invocation, expected); err != nil {
				return agent.AgentToolResult{}, err
			}
			completed(id, invocation, expected)
			return result, nil
		},
	}
}
