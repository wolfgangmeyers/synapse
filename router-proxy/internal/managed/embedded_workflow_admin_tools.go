package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// WorkflowManagementCategory is the tool category for workflow management tools.
const WorkflowManagementCategory = "Workflow Management"

// WorkflowAdminToolNames lists the 5 administrative tools for workflow management.
var WorkflowAdminToolNames = []string{
	"workflow_list",
	"workflow_get",
	"workflow_save",
	"workflow_delete",
	"workflow_assign",
}

// ----------------------------------------------------------------------
// 1. workflow_list
// ----------------------------------------------------------------------

func embeddedWorkflowListSchema() *ai.Schema {
	closed := false
	schema := ai.Object()
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkflowListTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "workflow_list",
		Description: "List all workflows with summary statistics. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.",
		Parameters:  embeddedWorkflowListSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}

			workflows, err := m.store.ListWorkflows(ctx)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			list := make([]map[string]any, 0, len(workflows))
			for _, wf := range workflows {
				list = append(list, map[string]any{
					"id":             wf.ID,
					"name":           wf.Name,
					"description":    wf.Description,
					"entry_state_id": wf.EntryStateID,
					"state_count":    wf.StateCount,
					"variable_count": len(wf.Variables),
					"created_at":     wf.CreatedAt,
					"updated_at":     wf.UpdatedAt,
				})
			}

			respBytes, err := json.Marshal(map[string]any{
				"status":    "ok",
				"workflows": list,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
		},
	}
}

// ----------------------------------------------------------------------
// 2. workflow_get
// ----------------------------------------------------------------------

func embeddedWorkflowGetSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Prop("workflow_id", ai.String("Workflow ID or exact unique name.")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkflowGetTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "workflow_get",
		Description: "Retrieve full workflow details including states and export bundle. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.",
		Parameters:  embeddedWorkflowGetSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}

			idOrName, ok := args["workflow_id"].(string)
			if !ok || strings.TrimSpace(idOrName) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or missing workflow_id")
			}
			idOrName = strings.TrimSpace(idOrName)

			wf, err := m.store.FindWorkflowByIDOrName(ctx, idOrName)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if wf == nil {
				return agent.AgentToolResult{}, fmt.Errorf("workflow %q not found", idOrName)
			}

			states, err := m.store.ListWorkflowStatesByWorkflow(ctx, wf.ID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			bundle, err := m.store.ExportWorkflow(ctx, wf.ID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			respBytes, err := json.Marshal(map[string]any{
				"status":   "ok",
				"workflow": wf,
				"states":   states,
				"bundle":   bundle,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
		},
	}
}

// ----------------------------------------------------------------------
// 3. workflow_save
// ----------------------------------------------------------------------

func embeddedWorkflowSaveSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Opt("workflow_id", ai.String("Optional workflow ID or name when updating an existing workflow.")),
		ai.Opt("bundle", &ai.Schema{Description: "Export bundle as JSON string or object."}),
		ai.Opt("name", ai.String("Workflow name (when saving via fields).")),
		ai.Opt("description", ai.String("Workflow description (when saving via fields).")),
		ai.Opt("entry_state_id", ai.String("Entry state ID (when saving via fields).")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkflowSaveTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "workflow_save",
		Description: "Create or update a workflow from an export bundle or fields. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.",
		Parameters:  embeddedWorkflowSaveSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}

			rawBundle, hasBundle := args["bundle"]
			if hasBundle && rawBundle != nil {
				var bundle store.WorkflowExportBundle
				switch v := rawBundle.(type) {
				case string:
					if err := json.Unmarshal([]byte(v), &bundle); err != nil {
						return agent.AgentToolResult{}, fmt.Errorf("invalid bundle JSON: %w", err)
					}
				case map[string]any:
					bBytes, err := json.Marshal(v)
					if err != nil {
						return agent.AgentToolResult{}, fmt.Errorf("invalid bundle object: %w", err)
					}
					if err := json.Unmarshal(bBytes, &bundle); err != nil {
						return agent.AgentToolResult{}, fmt.Errorf("invalid bundle schema: %w", err)
					}
				default:
					return agent.AgentToolResult{}, errors.New("bundle must be a JSON string or object")
				}

				if err := m.store.ValidateExportBundle(bundle); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("invalid export bundle: %w", err)
				}

				createdWf, states, err := m.store.ImportWorkflow(ctx, bundle)
				if err != nil {
					return agent.AgentToolResult{}, err
				}

				respBytes, err := json.Marshal(map[string]any{
					"status":      "ok",
					"workflow_id": createdWf.ID,
					"workflow":    createdWf,
					"state_count": len(states),
				})
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
			}

			// Saving via fields
			name, _ := args["name"].(string)
			name = strings.TrimSpace(name)
			desc, _ := args["description"].(string)
			desc = strings.TrimSpace(desc)

			var entryStateID *string
			if rawEntry, ok := args["entry_state_id"].(string); ok && strings.TrimSpace(rawEntry) != "" {
				trimmed := strings.TrimSpace(rawEntry)
				entryStateID = &trimmed
			}

			var targetID string
			if rawID, ok := args["workflow_id"].(string); ok && strings.TrimSpace(rawID) != "" {
				targetID = strings.TrimSpace(rawID)
			}

			if targetID != "" {
				existingWf, err := m.store.FindWorkflowByIDOrName(ctx, targetID)
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				if existingWf == nil {
					return agent.AgentToolResult{}, fmt.Errorf("workflow %q not found", targetID)
				}
				if name == "" {
					name = existingWf.Name
				}
				if desc == "" && args["description"] == nil {
					desc = existingWf.Description
				}
				if entryStateID == nil && args["entry_state_id"] == nil {
					entryStateID = existingWf.EntryStateID
				}
				updatedWf, err := m.store.UpdateWorkflow(ctx, existingWf.ID, store.WorkflowRequest{
					Name:         name,
					Description:  desc,
					EntryStateID: entryStateID,
				})
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				respBytes, err := json.Marshal(map[string]any{
					"status":      "ok",
					"workflow_id": updatedWf.ID,
					"workflow":    updatedWf,
				})
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
			}

			// Create new workflow via fields
			if name == "" {
				return agent.AgentToolResult{}, errors.New("name is required when creating a workflow without bundle")
			}
			newWf, err := m.store.CreateWorkflow(ctx, store.WorkflowRequest{
				Name:         name,
				Description:  desc,
				EntryStateID: entryStateID,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			respBytes, err := json.Marshal(map[string]any{
				"status":      "ok",
				"workflow_id": newWf.ID,
				"workflow":    newWf,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
		},
	}
}

// ----------------------------------------------------------------------
// 4. workflow_delete
// ----------------------------------------------------------------------

func embeddedWorkflowDeleteSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Prop("workflow_id", ai.String("Workflow ID or exact unique name to delete.")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkflowDeleteTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "workflow_delete",
		Description: "Delete a workflow and its associated states. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.",
		Parameters:  embeddedWorkflowDeleteSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}

			idOrName, ok := args["workflow_id"].(string)
			if !ok || strings.TrimSpace(idOrName) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or missing workflow_id")
			}
			idOrName = strings.TrimSpace(idOrName)

			wf, err := m.store.FindWorkflowByIDOrName(ctx, idOrName)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if wf == nil {
				return agent.AgentToolResult{}, fmt.Errorf("workflow %q not found", idOrName)
			}

			deleted, err := m.store.DeleteWorkflow(ctx, wf.ID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			respBytes, err := json.Marshal(map[string]any{
				"status":      "ok",
				"deleted":     deleted,
				"workflow_id": wf.ID,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
		},
	}
}

// ----------------------------------------------------------------------
// 5. workflow_assign
// ----------------------------------------------------------------------

func embeddedWorkflowAssignSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Prop("agent_id", ai.String("Agent ID to assign or unassign the workflow.")),
		ai.Prop("action", ai.EnumOf("activate", "deactivate")),
		ai.Opt("workflow_id", ai.String("Workflow ID or exact unique name (required for 'activate').")),
		ai.Opt("workflow_state_id", ai.String("Optional specific entry state ID.")),
		ai.Opt("variables", &ai.Schema{Type: "object", Description: "Optional workflow variables key-value map."}),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkflowAssignTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "workflow_assign",
		Description: "Activate or deactivate a workflow on a managed agent. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.",
		Parameters:  embeddedWorkflowAssignSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}

			agentID, ok := args["agent_id"].(string)
			if !ok || strings.TrimSpace(agentID) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or missing agent_id")
			}
			agentID = strings.TrimSpace(agentID)

			action, ok := args["action"].(string)
			if !ok || (action != "activate" && action != "deactivate") {
				return agent.AgentToolResult{}, errors.New("invalid action, must be 'activate' or 'deactivate'")
			}

			targetAgent, err := m.store.ManagedAgent(ctx, agentID)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			switch action {
			case "activate":
				rawWfID, _ := args["workflow_id"].(string)
				rawWfID = strings.TrimSpace(rawWfID)

				var wfStateID string
				if rawStateID, ok := args["workflow_state_id"].(string); ok {
					wfStateID = strings.TrimSpace(rawStateID)
				}

				if rawWfID == "" && wfStateID == "" {
					return agent.AgentToolResult{}, errors.New("workflow_id or workflow_state_id is required for activate")
				}

				var wf *store.Workflow
				if rawWfID != "" {
					wf, err = m.store.FindWorkflowByIDOrName(ctx, rawWfID)
					if err != nil {
						return agent.AgentToolResult{}, err
					}
					if wf == nil {
						return agent.AgentToolResult{}, fmt.Errorf("workflow %q not found", rawWfID)
					}
					if wfStateID == "" {
						if wf.EntryStateID == nil || strings.TrimSpace(*wf.EntryStateID) == "" {
							return agent.AgentToolResult{}, errors.New("workflow has no entry state")
						}
						wfStateID = strings.TrimSpace(*wf.EntryStateID)
					}
				}

				wfState, err := m.store.GetWorkflowState(ctx, wfStateID)
				if err != nil || wfState == nil {
					return agent.AgentToolResult{}, fmt.Errorf("workflow state %q not found", wfStateID)
				}

				var workflowID *string
				if wf != nil {
					workflowID = &wf.ID
				} else if wfState.WorkflowID != nil && strings.TrimSpace(*wfState.WorkflowID) != "" {
					workflowID = wfState.WorkflowID
				}

				variables := make(map[string]string)
				if rawVars, ok := args["variables"].(map[string]any); ok && rawVars != nil {
					for k, v := range rawVars {
						if strVal, isStr := v.(string); isStr {
							variables[k] = strVal
						} else {
							variables[k] = fmt.Sprintf("%v", v)
						}
					}
				}

				_ = m.store.CleanUpManagedAgentWorkflowReminders(ctx, agentID)
				_ = m.store.CleanUpManagedAgentWorkflowMonitors(ctx, agentID)

				if err := m.store.SetManagedAgentActiveWorkflowWithVariables(ctx, agentID, workflowID, &wfState.ID, variables); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("failed to set active workflow: %w", err)
				}

				// Synchronize goal with workflow state, applying variable substitution
				if wfState.Goal != "" {
					goalText := m.store.SubstituteAndExpandText(ctx, wfState.Goal, variables)
					goalSettings := store.DefaultManagedGoalSettings()
					goalSettings.GoalLockEnabled = true
					_, _ = m.store.UpdateManagedGoalConfig(ctx, agentID, store.ManagedGoalConfig{
						Enabled:  true,
						Goal:     goalText,
						Settings: goalSettings,
					})
				} else {
					goalSettings := store.DefaultManagedGoalSettings()
					goalSettings.GoalLockEnabled = false
					_, _ = m.store.UpdateManagedGoalConfig(ctx, agentID, store.ManagedGoalConfig{
						Enabled:  false,
						Goal:     "",
						Settings: goalSettings,
					})
				}

				_ = m.store.SpawnWorkflowStateReminders(ctx, agentID, wfState, variables)
				_ = m.SpawnWorkflowStateMonitors(ctx, agentID, wfState, variables)
				_ = m.SyncEmbeddedWorkflowTools(ctx, agentID)

				// Inject guidance prompt if agent is embedded and running
				if targetAgent.Runtime == "embedded" && targetAgent.LifecycleState == "running" {
					var sb strings.Builder
					sb.WriteString(fmt.Sprintf("[Workflow Activated: %s]\n", wfState.Name))
					if strings.TrimSpace(wfState.Description) != "" {
						descText := m.store.SubstituteAndExpandText(ctx, strings.TrimSpace(wfState.Description), variables)
						sb.WriteString(fmt.Sprintf("%s\n\n", descText))
					} else {
						sb.WriteString("\n")
					}
					if strings.TrimSpace(wfState.EntryText) != "" {
						entryText := m.store.SubstituteAndExpandText(ctx, strings.TrimSpace(wfState.EntryText), variables)
						sb.WriteString(fmt.Sprintf("### Instructions\n%s\n\n", entryText))
					}
					sb.WriteString("### Allowed Transitions\n")
					if len(wfState.Transitions) == 0 {
						sb.WriteString("None. (No forward state transitions available from this state)\n\n")
					} else {
						for _, tr := range wfState.Transitions {
							targetName := tr.TargetStateID
							if target, err := m.store.GetWorkflowState(ctx, tr.TargetStateID); err == nil && target != nil {
								targetName = target.Name
							}
							desc := strings.TrimSpace(tr.Description)
							if desc != "" {
								desc = m.store.SubstituteAndExpandText(ctx, desc, variables)
								sb.WriteString(fmt.Sprintf("- %s: %s\n", targetName, desc))
							} else {
								sb.WriteString(fmt.Sprintf("- %s\n", targetName))
							}
						}
						sb.WriteString("(Invoke `mecha_workflow_transition` with action=\"transition\" and target_state=\"<StateName>\" to transition)\n\n")
					}
					sb.WriteString("### Workflow Exit\n")
					if wfState.AllowExit {
						sb.WriteString("Allowed: invoke `mecha_workflow_transition` with action=\"exit\" when work in this workflow is finished.\n")
					} else {
						sb.WriteString("Not allowed from this state. You must transition to an allowed exit state to exit the workflow.\n")
					}
					promptMsg := strings.TrimSpace(sb.String())
					promptReqID := embeddedWorkerRequestID(owner.binding, callID)
					go func() {
						_ = m.AdmitPromptWithRequestID(context.Background(), agentID, promptMsg, promptReqID)
					}()
				}

				respBytes, err := json.Marshal(map[string]any{
					"status":                   "ok",
					"agent_id":                 agentID,
					"active_workflow_id":       workflowID,
					"active_workflow_state_id": wfState.ID,
					"active_workflow_state":    wfState.Name,
					"variables":                variables,
				})
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil

			case "deactivate":
				_ = m.store.CleanUpManagedAgentWorkflowReminders(ctx, agentID)
				_ = m.store.CleanUpManagedAgentWorkflowMonitors(ctx, agentID)
				if err := m.store.SetManagedAgentActiveWorkflowWithVariables(ctx, agentID, nil, nil, nil); err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("failed to deactivate workflow: %w", err)
				}
				_ = m.SyncEmbeddedWorkflowTools(ctx, agentID)

				goalSettings := store.DefaultManagedGoalSettings()
				goalSettings.GoalLockEnabled = false
				_, _ = m.store.UpdateManagedGoalConfig(ctx, agentID, store.ManagedGoalConfig{
					Enabled:  false,
					Goal:     "",
					Settings: goalSettings,
				})

				respBytes, err := json.Marshal(map[string]any{
					"status":                   "ok",
					"agent_id":                 agentID,
					"active_workflow_id":       nil,
					"active_workflow_state_id": nil,
				})
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
			}

			return agent.AgentToolResult{}, errors.New("unsupported action")
		},
	}
}

// embeddedWorkflowManagementTools returns the 5 workflow management tools for an embedded session.
func (m *Manager) embeddedWorkflowManagementTools(owner embeddedOwner) []agent.AgentTool {
	return []agent.AgentTool{
		m.embeddedWorkflowListTool(owner),
		m.embeddedWorkflowGetTool(owner),
		m.embeddedWorkflowSaveTool(owner),
		m.embeddedWorkflowDeleteTool(owner),
		m.embeddedWorkflowAssignTool(owner),
	}
}
