package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// AdminToolsCategory is the category for worker administration tools.
const AdminToolsCategory = "Worker Administration"

// AdminToolNames lists the 5 administrative tools for worker lifecycle and configuration.
var AdminToolNames = []string{
	"worker_spawn",
	"worker_terminate",
	"worker_set_model",
	"worker_set_goal",
	"worker_status",
}

func embeddedWorkerSpawnSchema() *ai.Schema {
	closed := false
	model := ai.Object(
		ai.Prop("provider", ai.String("Model provider name, e.g. openai-codex, google-vertex, google.")),
		ai.Prop("id", ai.String("Model identifier string.")),
	)
	model.AdditionalAllowed = &closed
	schema := ai.Object(
		ai.Prop("name", ai.String("Unique worker agent name/identifier.")),
		ai.Prop("cwd", ai.String("Working directory path for the worker.")),
		ai.Prop("instruction", ai.String("Initial prompt or instruction for the worker.")),
		ai.Opt("model", model),
		ai.Opt("admin", ai.Boolean("Whether the spawned worker has administrative capabilities.")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkerSpawnTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "worker_spawn",
		Description: "Spawn a new independent managed worker agent with optional administrative capabilities.",
		Parameters:  embeddedWorkerSpawnSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}
			name, ok := args["name"].(string)
			if !ok || strings.TrimSpace(name) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or missing worker name")
			}
			cwd, ok := args["cwd"].(string)
			if !ok || strings.TrimSpace(cwd) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or missing worker cwd")
			}
			instruction, ok := args["instruction"].(string)
			if !ok || strings.TrimSpace(instruction) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or missing worker instruction")
			}

			var selectedModel *Model
			if rawModel, ok := args["model"].(map[string]any); ok && rawModel != nil {
				provider, _ := rawModel["provider"].(string)
				id, _ := rawModel["id"].(string)
				if strings.TrimSpace(provider) == "" || strings.TrimSpace(id) == "" {
					return agent.AgentToolResult{}, errors.New("invalid model provider or id")
				}
				selectedModel = &Model{Provider: strings.TrimSpace(provider), ID: strings.TrimSpace(id)}
			}

			var admin bool
			if a, ok := args["admin"].(bool); ok {
				admin = a
			}

			body, err := json.Marshal(args)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			requestID := embeddedWorkerRequestID(owner.binding, callID)

			launched, err := m.launchEmbeddedWorkerFromOwner(ctx, LaunchRequest{
				Name:               strings.TrimSpace(name),
				Cwd:                strings.TrimSpace(cwd),
				Model:              selectedModel,
				InitialInstruction: strings.TrimSpace(instruction),
				Runtime:            "embedded",
				RequestID:          requestID,
				CanonicalBodyHash:  hashString(string(body)),
			}, owner)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			if admin {
				for _, toolName := range AdminToolNames {
					if err := m.store.SetAgentTool(ctx, launched.Agent.AgentID, toolName, true); err != nil {
						return agent.AgentToolResult{}, fmt.Errorf("failed to enable admin tool %s: %w", toolName, err)
					}
				}
			}

			respBytes, err := json.Marshal(map[string]any{
				"status":    "accepted",
				"worker_id": launched.Agent.AgentID,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
		},
	}
}

func embeddedWorkerTerminateAdminSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Prop("worker_id", ai.String("Agent ID of the worker to terminate.")),
		ai.Opt("reason", ai.String("Optional reason for worker termination.")),
		ai.Opt("cleanup_worktree", ai.Boolean("Whether to clean up the temporary git worktree assigned to this worker.")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkerTerminateAdminTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "worker_terminate",
		Description: "Terminate a running managed worker agent.",
		Parameters:  embeddedWorkerTerminateAdminSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}
			workerID, ok := args["worker_id"].(string)
			if !ok || strings.TrimSpace(workerID) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or missing worker_id")
			}
			workerID = strings.TrimSpace(workerID)
			if workerID == owner.binding.AgentID {
				return agent.AgentToolResult{}, errors.New("cannot target calling lane")
			}

			var cleanupWorktree bool
			if cw, ok := args["cleanup_worktree"].(bool); ok {
				cleanupWorktree = cw
			}

			body, err := json.Marshal(args)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			requestID := embeddedWorkerRequestID(owner.binding, callID)

			_, err = m.terminateWithEmbeddedProducer(ctx, TerminateRequest{
				AgentID:           workerID,
				Method:            "DELETE",
				CanonicalPath:     "/v1/managed-agents/" + workerID,
				RequestID:         requestID,
				CanonicalBodyHash: hashString(string(body)),
				CleanupWorktree:   cleanupWorktree,
			}, nil)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			respBytes, err := json.Marshal(map[string]any{
				"status":    "accepted",
				"worker_id": workerID,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
		},
	}
}

func embeddedWorkerSetModelSchema() *ai.Schema {
	closed := false
	model := ai.Object(
		ai.Prop("provider", ai.String("Model provider name, e.g. openai-codex, google-vertex, google.")),
		ai.Prop("id", ai.String("Model identifier string.")),
	)
	model.AdditionalAllowed = &closed
	schema := ai.Object(
		ai.Prop("worker_id", ai.String("Agent ID of the worker.")),
		ai.Prop("model", model),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkerSetModelTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "worker_set_model",
		Description: "Change the model configuration of a managed worker agent.",
		Parameters:  embeddedWorkerSetModelSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}
			workerID, ok := args["worker_id"].(string)
			if !ok || strings.TrimSpace(workerID) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or missing worker_id")
			}
			workerID = strings.TrimSpace(workerID)

			rawModel, ok := args["model"].(map[string]any)
			if !ok || rawModel == nil {
				return agent.AgentToolResult{}, errors.New("invalid or missing model")
			}
			provider, _ := rawModel["provider"].(string)
			id, _ := rawModel["id"].(string)
			if strings.TrimSpace(provider) == "" || strings.TrimSpace(id) == "" {
				return agent.AgentToolResult{}, errors.New("invalid model provider or id")
			}
			selected := Model{Provider: strings.TrimSpace(provider), ID: strings.TrimSpace(id)}
			if err := ValidateSelectedModel(selected); err != nil {
				return agent.AgentToolResult{}, err
			}

			// Validate worker exists
			if _, err := m.store.ManagedAgent(ctx, workerID); err != nil {
				return agent.AgentToolResult{}, err
			}

			// Validate model in catalog if catalog source is present
			if selector, ok := m.catalog.(ModelSelector); ok {
				if _, err := selector.Select(ctx, &selected); err != nil {
					return agent.AgentToolResult{}, err
				}
			} else if m.catalog != nil {
				catalog, err := m.catalog.Catalog(ctx)
				if err == nil {
					if _, err := SelectModel(&selected, nil, catalog); err != nil {
						return agent.AgentToolResult{}, err
					}
				}
			}

			body, err := json.Marshal(args)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			requestID := embeddedWorkerRequestID(owner.binding, callID)

			_, err = m.ReserveRestart(ctx, RestartRequest{
				AgentID:           workerID,
				Method:            "POST",
				CanonicalPath:     "/v1/managed-agents/" + workerID + "/restart",
				RequestID:         requestID,
				CanonicalBodyHash: hashString(string(body)),
				Model:             &selected,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			respBytes, err := json.Marshal(map[string]any{
				"status":    "accepted",
				"worker_id": workerID,
				"model": map[string]any{
					"provider": selected.Provider,
					"id":       selected.ID,
				},
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
		},
	}
}

func embeddedWorkerSetGoalSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Prop("worker_id", ai.String("Agent ID of the target worker.")),
		ai.Prop("action", ai.EnumOf("set", "status", "off")),
		ai.Opt("goal", ai.String("Goal instruction for the worker (required for 'set').")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkerSetGoalTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "worker_set_goal",
		Description: "Set, inspect, or clear the goal of a managed worker agent.",
		Parameters:  embeddedWorkerSetGoalSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}
			workerID, ok := args["worker_id"].(string)
			if !ok || strings.TrimSpace(workerID) == "" {
				return agent.AgentToolResult{}, errors.New("invalid or missing worker_id")
			}
			workerID = strings.TrimSpace(workerID)

			action, ok := args["action"].(string)
			if !ok || (action != "set" && action != "status" && action != "off") {
				return agent.AgentToolResult{}, errors.New("invalid action, must be 'set', 'status', or 'off'")
			}
			goal, _ := args["goal"].(string)
			goal = strings.TrimSpace(goal)
			if action == "set" && goal == "" {
				return agent.AgentToolResult{}, errors.New("goal is required when action is 'set'")
			}

			// Validate worker exists
			if _, err := m.store.ManagedAgent(ctx, workerID); err != nil {
				return agent.AgentToolResult{}, err
			}

			requestID := embeddedWorkerRequestID(owner.binding, callID)
			ctrlArgs := map[string]any{"action": action}
			if action == "set" {
				ctrlArgs["goal"] = goal
			}

			resultMap := map[string]any{
				"status":    "accepted",
				"worker_id": workerID,
				"action":    action,
			}

			res, err := m.DirectRootControl(ctx, workerID, requestID, "goal.control", ctrlArgs)
			if err == nil {
				if res.Details != nil {
					for k, v := range res.Details {
						resultMap[k] = v
					}
				}
			} else {
				// Fallback to direct store update
				switch action {
				case "set":
					policy, pErr := m.store.SetManagedGoalDesiredState(ctx, workerID, true, goal)
					if pErr != nil {
						return agent.AgentToolResult{}, pErr
					}
					resultMap["goal_state"] = map[string]any{
						"enabled": policy.Config.Enabled,
						"goal":    policy.Config.Goal,
					}
				case "off":
					policy, pErr := m.store.SetManagedGoalDesiredState(ctx, workerID, false, "")
					if pErr != nil {
						return agent.AgentToolResult{}, pErr
					}
					resultMap["goal_state"] = map[string]any{
						"enabled": policy.Config.Enabled,
						"goal":    policy.Config.Goal,
					}
				case "status":
					policy, pErr := m.store.ManagedGoalPolicyFor(ctx, workerID)
					if pErr != nil && !errors.Is(pErr, sql.ErrNoRows) {
						return agent.AgentToolResult{}, pErr
					}
					if policy != nil {
						resultMap["goal_state"] = map[string]any{
							"enabled": policy.Config.Enabled,
							"goal":    policy.Config.Goal,
						}
					} else {
						resultMap["goal_state"] = map[string]any{
							"enabled": false,
							"goal":    "",
						}
					}
				}
			}

			respBytes, err := json.Marshal(resultMap)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
		},
	}
}

func embeddedWorkerStatusSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Opt("worker_id", ai.String("Optional agent ID of the worker to inspect. If omitted, lists all managed workers.")),
	)
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkerStatusTool(owner embeddedOwner) agent.AgentTool {
	return agent.AgentTool{
		Name:        "worker_status",
		Description: "Inspect detailed status, activity, goal, and subagents of managed workers.",
		Parameters:  embeddedWorkerStatusSchema(),
		Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if m == nil || m.store == nil {
				return agent.AgentToolResult{}, errors.New("manager store is unavailable")
			}
			if owner.binding.AgentID != "" && owner.identity != "" {
				if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
					return agent.AgentToolResult{}, err
				}
			}

			rawID, hasWorkerID := args["worker_id"]
			workerID, _ := rawID.(string)
			workerID = strings.TrimSpace(workerID)

			if hasWorkerID && workerID != "" {
				agentRecord, err := m.store.ManagedAgent(ctx, workerID)
				if err != nil {
					return agent.AgentToolResult{}, err
				}

				policy, _ := m.store.ManagedGoalPolicyFor(ctx, workerID)
				var goalInfo map[string]any
				if policy != nil {
					goalInfo = map[string]any{
						"enabled":          policy.Config.Enabled,
						"goal":             policy.Config.Goal,
						"projection_state": policy.ProjectionState,
						"desired_revision": policy.DesiredRevision,
						"applied_revision": policy.AppliedRevision,
					}
				}

				subagents, _ := m.store.ManagedSubagents(ctx, store.EmbeddedBinding{AgentID: workerID})
				activeSubagents := make([]map[string]any, 0)
				for _, sa := range subagents {
					if sa.Status == "running" || sa.Status == "queued" {
						activeSubagents = append(activeSubagents, map[string]any{
							"agent_id":      sa.AgentID,
							"description":   sa.Description,
							"subagent_type": sa.SubagentType,
							"status":        sa.Status,
							"turn_count":    sa.TurnCount,
						})
					}
				}

				respBytes, err := json.Marshal(map[string]any{
					"status":    "ok",
					"worker_id": workerID,
					"worker": map[string]any{
						"agent_id":        agentRecord.AgentID,
						"name":            agentRecord.Name,
						"lifecycle_state": agentRecord.LifecycleState,
						"activity_state":  agentRecord.ActivityState,
						"desired_state":   agentRecord.DesiredState,
						"cwd":             agentRecord.Cwd,
						"model": map[string]any{
							"provider": agentRecord.SelectedModel.Provider,
							"id":       agentRecord.SelectedModel.ID,
						},
						"goal":             goalInfo,
						"active_subagents": activeSubagents,
					},
				})
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
			}

			// List all managed agents summary
			agents, err := m.store.ManagedAgents(ctx)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			workersList := make([]map[string]any, 0, len(agents))
			for _, a := range agents {
				workersList = append(workersList, map[string]any{
					"agent_id":        a.AgentID,
					"name":            a.Name,
					"lifecycle_state": a.LifecycleState,
					"activity_state":  a.ActivityState,
					"desired_state":   a.DesiredState,
					"cwd":             a.Cwd,
					"model": map[string]any{
						"provider": a.SelectedModel.Provider,
						"id":       a.SelectedModel.ID,
					},
				})
			}
			respBytes, err := json.Marshal(map[string]any{
				"status":  "ok",
				"workers": workersList,
			})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(respBytes)}}}, nil
		},
	}
}

// embeddedAdminTools returns the 5 admin tools for an embedded session.
func (m *Manager) embeddedAdminTools(owner embeddedOwner) []agent.AgentTool {
	return []agent.AgentTool{
		m.embeddedWorkerSpawnTool(owner),
		m.embeddedWorkerTerminateAdminTool(owner),
		m.embeddedWorkerSetModelTool(owner),
		m.embeddedWorkerSetGoalTool(owner),
		m.embeddedWorkerStatusTool(owner),
	}
}

// embeddedSessionTools returns the administration tools for an embedded session.
func (m *Manager) embeddedSessionTools(owner embeddedOwner) []agent.AgentTool {
	return m.embeddedAdminTools(owner)
}
