package managed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func embeddedWorkflowTransitionSchema() *ai.Schema {
	closed := false
	s := ai.Object(
		ai.Prop("action", ai.EnumOf("transition", "exit")),
		ai.Opt("target_state", ai.String("Target workflow state name or ID (required when action is 'transition').")),
		ai.Opt("reason", ai.String("Optional rationale or note describing why this transition or exit is occurring.")),
	)
	s.AdditionalAllowed = &closed
	return s
}

// formatWorkflowStateGuidance formats the context guidance for a given workflow state.
func formatWorkflowStateGuidance(ctx context.Context, s *store.Store, state *store.WorkflowState) string {
	if state == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Active Workflow State: %s\n", state.Name))
	if strings.TrimSpace(state.Description) != "" {
		sb.WriteString(fmt.Sprintf("%s\n\n", strings.TrimSpace(state.Description)))
	} else {
		sb.WriteString("\n")
	}

	if strings.TrimSpace(state.EntryText) != "" {
		sb.WriteString(fmt.Sprintf("### Instructions\n%s\n\n", strings.TrimSpace(state.EntryText)))
	}

	sb.WriteString("### Allowed Transitions\n")
	if len(state.Transitions) == 0 {
		sb.WriteString("None. (No forward state transitions available from this state)\n\n")
	} else {
		for _, tr := range state.Transitions {
			targetName := tr.TargetStateID
			if s != nil {
				if target, err := s.GetWorkflowState(ctx, tr.TargetStateID); err == nil && target != nil {
					targetName = target.Name
				}
			}
			desc := strings.TrimSpace(tr.Description)
			if desc != "" {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", targetName, desc))
			} else {
				sb.WriteString(fmt.Sprintf("- %s\n", targetName))
			}
		}
		sb.WriteString("(Invoke `mecha_workflow_transition` with action=\"transition\" and target_state=\"<StateName>\" to transition)\n\n")
	}

	sb.WriteString("### Workflow Exit\n")
	if state.AllowExit {
		sb.WriteString("Allowed: invoke `mecha_workflow_transition` with action=\"exit\" when work in this workflow is finished.\n")
	} else {
		sb.WriteString("Not allowed from this state. You must transition to an allowed exit state to exit the workflow.\n")
	}

	return strings.TrimSpace(sb.String())
}

func (m *Manager) embeddedWorkflowTransitionTool(owner embeddedOwner, toolName string) agent.AgentTool {
	desc := "Inspect and execute workflow state transitions or exit the active workflow state."
	return agent.AgentTool{
		Name:        toolName,
		Description: desc,
		Parameters:  embeddedWorkflowTransitionSchema(),
		Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("store unavailable for workflow transition")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				_ = m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL)
			}

			// Reject subagent callers
			agentRec, agentErr := m.store.ManagedAgent(ctx, owner.binding.AgentID)
			if agentErr == nil && agentRec != nil && agentRec.ParentAgentID != "" {
				return agent.AgentToolResult{}, errors.New("subagents cannot invoke workflow state transitions")
			}

			action, _ := args["action"].(string)
			if action != "transition" && action != "exit" {
				return agent.AgentToolResult{}, errors.New("invalid action: must be 'transition' or 'exit'")
			}

			currentState, err := m.store.GetManagedAgentActiveWorkflowState(ctx, owner.binding.AgentID)
			if err != nil || currentState == nil {
				return agent.AgentToolResult{}, errors.New("agent has no active workflow state")
			}

			reasonStr, _ := args["reason"].(string)

			switch action {
			case "transition":
				targetInput, _ := args["target_state"].(string)
				targetInput = strings.TrimSpace(targetInput)
				if targetInput == "" {
					return agent.AgentToolResult{}, errors.New("target_state is required for 'transition' action")
				}

				if len(currentState.Transitions) == 0 {
					return agent.AgentToolResult{}, fmt.Errorf("workflow state %q has no outgoing transitions", currentState.Name)
				}

				// Find matching transition by ID or Name (case-insensitive)
				var matchedTargetID string
				var allowedNames []string
				for _, tr := range currentState.Transitions {
					targetState, err := m.store.GetWorkflowState(ctx, tr.TargetStateID)
					targetName := tr.TargetStateID
					if err == nil && targetState != nil {
						targetName = targetState.Name
					}
					allowedNames = append(allowedNames, targetName)

					if tr.TargetStateID == targetInput || strings.EqualFold(targetName, targetInput) {
						matchedTargetID = tr.TargetStateID
						break
					}
				}

				if matchedTargetID == "" {
					return agent.AgentToolResult{}, fmt.Errorf("transition to %q is not permitted from state %q (allowed targets: %s)", targetInput, currentState.Name, strings.Join(allowedNames, ", "))
				}

				targetState, err := m.store.GetWorkflowState(ctx, matchedTargetID)
				if err != nil || targetState == nil {
					return agent.AgentToolResult{}, fmt.Errorf("target workflow state %q not found", matchedTargetID)
				}

				// Cleanup old reminders
				_ = m.store.CleanUpManagedAgentWorkflowReminders(ctx, owner.binding.AgentID)

				// Transition to new state
				if err := m.store.SetManagedAgentActiveWorkflowState(ctx, owner.binding.AgentID, &targetState.ID); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("failed to transition workflow state: %w", err)
				}

				// Spawn new reminders
				_ = m.store.SpawnWorkflowStateReminders(ctx, owner.binding.AgentID, targetState)

				// Record history presentation
				eventKey := fmt.Sprintf("workflow-transition:%s:%s:%s:%s", owner.binding.AgentID, currentState.ID, targetState.ID, id)
				pres := &store.ManagedHistoryPresentation{
					Kind:    "state",
					Outcome: "success",
					From:    currentState.Name,
					To:      targetState.Name,
					Summary: fmt.Sprintf("Workflow transitioned from %s to %s", currentState.Name, targetState.Name),
				}
				var details *string
				if trimmed := strings.TrimSpace(reasonStr); trimmed != "" {
					msg := fmt.Sprintf("Reason: %s", trimmed)
					details = &msg
				}
				entry, recErr := m.store.RecordManagedCompleteHistoryWithDetails(
					ctx,
					owner.binding.AgentID,
					"managed_message",
					"info",
					"Workflow state transitioned",
					eventKey,
					pres,
					args,
					details,
				)
				if recErr == nil {
					m.publishCompleteHistory(entry)
				}

				// Construct synchronous response with the new state's guidance
				guidance := formatWorkflowStateGuidance(ctx, m.store, targetState)
				resultText := fmt.Sprintf("Successfully transitioned to workflow state: %s.\n\n%s", targetState.Name, guidance)
				return agent.AgentToolResult{
					Content: []ai.Content{ai.TextContent{Text: resultText}},
				}, nil

			case "exit":
				if !currentState.AllowExit {
					return agent.AgentToolResult{}, fmt.Errorf("workflow exit is not permitted from state %q (locked state; must transition to an exit-eligible state first)", currentState.Name)
				}

				// Cleanup reminders
				_ = m.store.CleanUpManagedAgentWorkflowReminders(ctx, owner.binding.AgentID)

				// Deactivate state
				if err := m.store.SetManagedAgentActiveWorkflowState(ctx, owner.binding.AgentID, nil); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("failed to exit workflow state: %w", err)
				}

				// Record history presentation
				eventKey := fmt.Sprintf("workflow-exit:%s:%s:%s", owner.binding.AgentID, currentState.ID, id)
				pres := &store.ManagedHistoryPresentation{
					Kind:    "state",
					Outcome: "success",
					From:    currentState.Name,
					To:      "none",
					Summary: fmt.Sprintf("Workflow exited from %s", currentState.Name),
				}
				var details *string
				if trimmed := strings.TrimSpace(reasonStr); trimmed != "" {
					msg := fmt.Sprintf("Reason: %s", trimmed)
					details = &msg
				}
				entry, recErr := m.store.RecordManagedCompleteHistoryWithDetails(
					ctx,
					owner.binding.AgentID,
					"managed_message",
					"info",
					"Workflow exited",
					eventKey,
					pres,
					args,
					details,
				)
				if recErr == nil {
					m.publishCompleteHistory(entry)
				}

				resultText := fmt.Sprintf("Successfully exited workflow state %q. The agent is now in unrestricted default operation.", currentState.Name)
				return agent.AgentToolResult{
					Content: []ai.Content{ai.TextContent{Text: resultText}},
				}, nil
			}

			return agent.AgentToolResult{}, errors.New("unhandled action")
		},
	}
}

// EmbeddedWorkflowTransitionToolForTest exports the workflow transition tool for integration tests.
func (m *Manager) EmbeddedWorkflowTransitionToolForTest(agentID, identity, toolName string) agent.AgentTool {
	owner := embeddedOwner{
		binding:  store.EmbeddedBinding{AgentID: agentID},
		identity: identity,
	}
	return m.embeddedWorkflowTransitionTool(owner, toolName)
}

