package managed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// LaunchBackgroundWorkflow launches an embedded background workflow worker.
// It:
// 1. Fetches workflow definition from the store.
// 2. Validates required variables using store.ValidateWorkflowVariables.
// 3. Generates a deterministic agent ID (bg-wf-<workflowID>-<timestamp>).
// 4. Interpolates the workflow's entry state prompt/instructions with the input variables.
// 5. Launches the agent as a managed agent with IsBackground: true and initial instructions.
// 6. Sets the initial active workflow state and variables on the agent.
func (m *Manager) LaunchBackgroundWorkflow(
	ctx context.Context,
	workflowID string,
	inputVars map[string]string,
	targetCwd string,
	modelOverride *Model,
) (*store.ManagedAgentRecord, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("manager store is unavailable")
	}

	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, &store.WorkflowValidationError{Field: "workflow_id", Message: "workflow_id must not be empty"}
	}

	wf, err := m.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch workflow: %w", err)
	}
	if wf == nil {
		return nil, &store.WorkflowNotFoundError{ID: workflowID}
	}

	if inputVars == nil {
		inputVars = make(map[string]string)
	}

	// Validate required variables
	if err := store.ValidateWorkflowVariables(wf.Variables, inputVars); err != nil {
		return nil, err
	}

	if wf.EntryStateID == nil || strings.TrimSpace(*wf.EntryStateID) == "" {
		return nil, &store.WorkflowValidationError{
			Field:   "entry_state_id",
			Message: fmt.Sprintf("workflow %q has no configured entry state", workflowID),
		}
	}

	entryState, err := m.store.GetWorkflowState(ctx, *wf.EntryStateID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch entry state: %w", err)
	}
	if entryState == nil {
		return nil, &store.WorkflowValidationError{
			Field:   "entry_state_id",
			Message: fmt.Sprintf("entry state %q not found", *wf.EntryStateID),
		}
	}

	// Generate agent ID
	now := m.clock.Now()
	timestampStr := now.UTC().Format("20060102150405")
	cleanWfID := strings.ReplaceAll(workflowID, "_", "-")
	agentID := fmt.Sprintf("bg-wf-%s-%s", cleanWfID, timestampStr)

	// Format initial instructions from entry state and input variables
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Workflow Activated: %s]\n", entryState.Name))
	if strings.TrimSpace(entryState.Description) != "" {
		descText := m.store.SubstituteAndExpandText(ctx, strings.TrimSpace(entryState.Description), inputVars)
		sb.WriteString(fmt.Sprintf("%s\n\n", descText))
	} else {
		sb.WriteString("\n")
	}
	if strings.TrimSpace(entryState.EntryText) != "" {
		entryText := m.store.SubstituteAndExpandText(ctx, strings.TrimSpace(entryState.EntryText), inputVars)
		sb.WriteString(fmt.Sprintf("### Instructions\n%s\n\n", entryText))
	}
	sb.WriteString("### Allowed Transitions\n")
	if len(entryState.Transitions) == 0 {
		sb.WriteString("None. (No forward state transitions available from this state)\n\n")
	} else {
		for _, tr := range entryState.Transitions {
			targetName := tr.TargetStateID
			if target, err := m.store.GetWorkflowState(ctx, tr.TargetStateID); err == nil && target != nil {
				targetName = target.Name
			}
			desc := strings.TrimSpace(tr.Description)
			if desc != "" {
				desc = m.store.SubstituteAndExpandText(ctx, desc, inputVars)
				sb.WriteString(fmt.Sprintf("- %s: %s\n", targetName, desc))
			} else {
				sb.WriteString(fmt.Sprintf("- %s\n", targetName))
			}
		}
		sb.WriteString("(Invoke `mecha_workflow_transition` with action=\"transition\" and target_state=\"<StateName>\" to transition)\n\n")
	}
	sb.WriteString("### Workflow Exit\n")
	if entryState.AllowExit {
		sb.WriteString("Allowed: invoke `mecha_workflow_transition` with action=\"exit\" when work in this workflow is finished.\n")
	} else {
		sb.WriteString("Not allowed from this state. You must transition to an allowed exit state to exit the workflow.\n")
	}

	initialInstruction := strings.TrimSpace(sb.String())

	// Resolve target cwd
	cwd := strings.TrimSpace(targetCwd)
	if cwd == "" {
		roots := m.AllowedWorkingDirectoryRoots()
		if len(roots) > 0 {
			cwd = roots[0]
		} else {
			cwd = "."
		}
	}

	// Model selection
	model := modelOverride
	if model == nil && wf != nil {
		// Use default model if available
	}

	requestID, err := m.ids.Next()
	if err != nil {
		return nil, err
	}

	launchReq := LaunchRequest{
		Name:               agentID,
		Cwd:                cwd,
		Model:              model,
		InitialInstruction: initialInstruction,
		Runtime:            "embedded",
		RequestID:          requestID,
		IsBackground:       true,
		AllowInlineTools:   true,
	}

	launchRes, err := m.Launch(ctx, launchReq)
	if err != nil {
		return nil, fmt.Errorf("failed to launch background agent: %w", err)
	}

	agentRec := launchRes.Agent

	// Bind active workflow and variables
	if err := m.store.SetManagedAgentActiveWorkflowWithVariables(ctx, agentID, &workflowID, &entryState.ID, inputVars); err != nil {
		return nil, fmt.Errorf("failed to set active workflow state on launched agent: %w", err)
	}

	// Activate implicit workflow completion goal
	wfName := "active"
	snoozeDurationMS := int64(600000)
	if wf, err := m.store.GetWorkflow(ctx, workflowID); err == nil && wf != nil {
		wfName = wf.Name
		if wf.SnoozeDurationMS > 0 {
			snoozeDurationMS = int64(wf.SnoozeDurationMS)
		}
	}
	goalText := "Complete active workflow: " + wfName
	goalSettings := store.DefaultManagedGoalSettings()
	if policy, err := m.store.ManagedGoalPolicyFor(ctx, agentID); err == nil && policy != nil {
		goalSettings = policy.Config.Settings
	}
	goalSettings.SnoozeDurationMS = snoozeDurationMS
	_, _ = m.store.UpdateManagedGoalConfig(ctx, agentID, store.ManagedGoalConfig{
		Enabled:  true,
		Goal:     goalText,
		Settings: goalSettings,
	})

	_ = m.store.SpawnWorkflowStateReminders(ctx, agentID, entryState, inputVars)
	_ = m.SpawnWorkflowStateMonitors(ctx, agentID, entryState, inputVars)
	_ = m.SyncEmbeddedWorkflowTools(ctx, agentID)

	// Reload agent to reflect updated workflow state
	if updated, err := m.store.ManagedAgent(ctx, agentID); err == nil && updated != nil {
		agentRec = updated
	}

	return agentRec, nil
}
