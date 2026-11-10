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
func formatWorkflowStateGuidance(ctx context.Context, s *store.Store, state *store.WorkflowState, vars ...map[string]string) string {
	if state == nil {
		return ""
	}
	var varMap map[string]string
	if len(vars) > 0 {
		varMap = vars[0]
	}

	expand := func(text string) string {
		if s != nil {
			return s.SubstituteAndExpandText(ctx, text, varMap)
		}
		if varMap != nil {
			return store.SubstituteVariables(text, varMap)
		}
		return text
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Active Workflow State: %s\n", state.Name))
	if strings.TrimSpace(state.Description) != "" {
		desc := expand(strings.TrimSpace(state.Description))
		sb.WriteString(fmt.Sprintf("%s\n\n", desc))
	} else {
		sb.WriteString("\n")
	}

	if strings.TrimSpace(state.EntryText) != "" {
		entryText := expand(strings.TrimSpace(state.EntryText))
		sb.WriteString(fmt.Sprintf("### Instructions\n%s\n\n", entryText))
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
				desc = expand(desc)
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

			action, _ := args["action"].(string)
			reasonStr, _ := args["reason"].(string)

			// Reject subagent callers
			agentRec, agentErr := m.store.ManagedAgent(ctx, owner.binding.AgentID)
			if agentErr == nil && agentRec != nil && agentRec.ParentAgentID != "" {
				return agent.AgentToolResult{}, errors.New("subagents cannot invoke workflow state transitions")
			}

			if action != "transition" && action != "exit" {
				return agent.AgentToolResult{}, errors.New("invalid action: must be 'transition' or 'exit'")
			}

			snapshot, err := m.store.WorkflowBindingVersion(ctx, owner.binding.AgentID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if m.workflowTransitionCapturedTestHook != nil {
				m.workflowTransitionCapturedTestHook()
			}
			if snapshot.StateID == "" {
				return agent.AgentToolResult{}, errors.New("agent has no active workflow state")
			}
			currentState, err := m.store.GetWorkflowState(ctx, snapshot.StateID)
			if err != nil || currentState == nil {
				return agent.AgentToolResult{}, errors.New("agent has no active workflow state")
			}

			var workflowVars map[string]string
			if agentRec != nil {
				workflowVars = agentRec.WorkflowVariables
			}

			switch action {
			case "transition":
				targetInput, _ := args["target_state"].(string)
				targetInput = strings.TrimSpace(targetInput)
				if targetInput == "" {
					return agent.AgentToolResult{}, errors.New("target_state is required for 'transition' action")
				}

				targetState, err := m.store.AuthorizeWorkflowTransition(ctx, owner.binding.AgentID, snapshot.Generation, currentState.ID, targetInput)
				if err != nil {
					var rejection *store.WorkflowTransitionRejection
					if errors.As(err, &rejection) {
						return agent.AgentToolResult{}, err
					}
					return agent.AgentToolResult{}, fmt.Errorf("failed to transition workflow state: %w", err)
				}

				// Effects only run after the authorized durable transition succeeds.
				_ = m.store.CleanUpManagedAgentWorkflowReminders(ctx, owner.binding.AgentID)
				_ = m.store.CleanUpManagedAgentWorkflowMonitors(ctx, owner.binding.AgentID)
				m.retireEmbeddedGoalTimer(owner.binding.AgentID)
				_ = m.SyncEmbeddedWorkflowTools(ctx, owner.binding.AgentID)
				_ = m.store.SpawnWorkflowStateReminders(ctx, owner.binding.AgentID, targetState, workflowVars)
				_ = m.SpawnWorkflowStateMonitors(ctx, owner.binding.AgentID, targetState, workflowVars)

				// On state transition: do NOT wipe, rewrite, or deactivate the goal.
				// The implicit workflow goal remains active and locked.

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
				guidanceState, guidanceErr := m.store.WorkflowStateForGuidance(ctx, owner.binding.AgentID, targetState)
				if guidanceErr != nil {
					guidanceState = targetState
				}
				guidance := formatWorkflowStateGuidance(ctx, m.store, guidanceState, workflowVars)
				resultText := fmt.Sprintf("Successfully transitioned to workflow state: %s.\n\n%s", targetState.Name, guidance)
				return agent.AgentToolResult{
					Content: []ai.Content{ai.TextContent{Text: resultText}},
				}, nil

			case "exit":
				if !currentState.AllowExit {
					return agent.AgentToolResult{}, fmt.Errorf("workflow exit is not permitted from state %q (locked state; must transition to an exit-eligible state first)", currentState.Name)
				}
				_ = m.store.CleanUpManagedAgentWorkflowReminders(ctx, owner.binding.AgentID)
				_ = m.store.CleanUpManagedAgentWorkflowMonitors(ctx, owner.binding.AgentID)
				if err := m.store.SetManagedAgentActiveWorkflowWithVariables(ctx, owner.binding.AgentID, nil, nil, nil); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("failed to exit workflow state: %w", err)
				}
				m.retireEmbeddedGoalTimer(owner.binding.AgentID)
				_ = m.SyncEmbeddedWorkflowTools(ctx, owner.binding.AgentID)

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

// isSystemEscapeTool returns true if the tool should always be allowed through
// regardless of worker tool disabled flags, such as workflow transitions and mailbox messaging/replies.
func isSystemEscapeTool(toolName string) bool {
	switch toolName {
	case "mecha_workflow_transition", "workflow_transition":
		return true
	case "mecha_mailbox_send", "mecha_parent_message":
		return true
	case "mecha_tool_timeout", "extend_tool_timeout":
		return true
	default:
		return false
	}
}

// filterToolsForAgent filters baseTools based on:
//  1. If an active workflow state with AllowedTools exists, filter baseTools using store.IsToolAllowedInWorkflowState.
//  2. Otherwise, fetch agent tools from store.GetAgentTools(ctx, agentID), and filter baseTools to include only tools
//     where tool.Enabled == true, or system escape tools (workflow transition, mailbox replies, timeouts).
func (m *Manager) filterToolsForAgent(ctx context.Context, agentID string, baseTools []agent.AgentTool) ([]agent.AgentTool, error) {
	if m == nil || len(baseTools) == 0 {
		return baseTools, nil
	}

	activeWorkflowState, err := m.store.GetManagedAgentActiveWorkflowState(ctx, agentID)
	if err != nil {
		return nil, err
	}

	if activeWorkflowState != nil && len(activeWorkflowState.AllowedTools) > 0 {
		var filteredTools []agent.AgentTool
		for _, tool := range baseTools {
			if store.IsToolAllowedInWorkflowState(tool.Name, activeWorkflowState.AllowedTools) {
				filteredTools = append(filteredTools, tool)
			}
		}
		return filteredTools, nil
	}

	agentTools, err := m.store.GetAgentTools(ctx, agentID)
	if err != nil {
		// If agent tools cannot be retrieved (e.g. agent not found or DB error), default to baseTools or return err
		var notFound *store.ManagedAgentNotFoundError
		if errors.As(err, &notFound) {
			return baseTools, nil
		}
		return nil, err
	}

	enabledMap := make(map[string]bool, len(agentTools))
	for _, at := range agentTools {
		enabledMap[at.ToolName] = at.Enabled
	}

	var filteredTools []agent.AgentTool
	for _, tool := range baseTools {
		if isSystemEscapeTool(tool.Name) {
			filteredTools = append(filteredTools, tool)
			continue
		}
		if enabled, exists := enabledMap[tool.Name]; exists {
			if enabled {
				filteredTools = append(filteredTools, tool)
			}
		} else {
			// If not explicitly configured, default to enabled (keep in filteredTools)
			filteredTools = append(filteredTools, tool)
		}
	}
	return filteredTools, nil
}

// SyncEmbeddedAgentTools updates the active tools on the agent's lane and active session
// according to active workflow state restrictions or configured agent tools.
func (m *Manager) SyncEmbeddedAgentTools(ctx context.Context, agentID string) error {
	if m == nil || agentID == "" {
		return nil
	}
	lane := m.laneFor(agentID)
	if lane == nil {
		return nil
	}

	baseTools := lane.getBaseTools()
	if len(baseTools) == 0 {
		return nil
	}

	filteredTools, err := m.filterToolsForAgent(ctx, agentID, baseTools)
	if err != nil {
		return err
	}

	lane.activeMu.Lock()
	lane.activeTools = append([]agent.AgentTool(nil), filteredTools...)
	session := lane.activeSession
	lane.activeMu.Unlock()

	refreshPiEmbeddedTools(session, filteredTools)

	return nil
}

// SyncEmbeddedWorkflowTools updates the active tools on the agent's lane and active session
// according to the current active workflow state tool restrictions.
func (m *Manager) SyncEmbeddedWorkflowTools(ctx context.Context, agentID string) error {
	return m.SyncEmbeddedAgentTools(ctx, agentID)
}

// SyncEmbeddedAllActiveLanes iterates over all active agent lanes and synchronizes their active tools.
func (m *Manager) SyncEmbeddedAllActiveLanes(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	laneIDs := make([]string, 0, len(m.lanes))
	for id := range m.lanes {
		laneIDs = append(laneIDs, id)
	}
	m.mu.Unlock()

	for _, id := range laneIDs {
		_ = m.SyncEmbeddedAgentTools(ctx, id)
	}
	return nil
}
