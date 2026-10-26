package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// managedAgentToolsRoute matches /v1/managed-agents/{id}/tools or /v1/agents/{id}/tools
func managedAgentToolsRoute(path string) (agentID string, ok bool) {
	prefix := ""
	if strings.HasPrefix(path, "/v1/managed-agents/") {
		prefix = "/v1/managed-agents/"
	} else if strings.HasPrefix(path, "/v1/agents/") {
		prefix = "/v1/agents/"
	} else {
		return "", false
	}

	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "tools" {
		return "", false
	}

	var err error
	agentID, err = url.PathUnescape(parts[0])
	if err != nil || agentID == "" {
		return "", false
	}
	return agentID, true
}

type toolUpdateRequest struct {
	ToolName *string `json:"tool_name"`
	Category *string `json:"category"`
	Enabled  bool    `json:"enabled"`
}

type agentToolResponseItem struct {
	ToolName         string  `json:"tool_name"`
	Category         string  `json:"category"`
	Enabled          bool    `json:"enabled"`
	EffectiveEnabled bool    `json:"effective_enabled"`
	MCPServerID      *string `json:"mcp_server_id,omitempty"`
}

type agentToolsResponse struct {
	AgentID             string                  `json:"agent_id"`
	WorkflowOverride    bool                    `json:"workflow_override"`
	ActiveWorkflowState string                  `json:"active_workflow_state"`
	Tools               []agentToolResponseItem `json:"tools"`
}

func (r *Router) handleSettingsTools(w http.ResponseWriter, req *http.Request) {
	if r.store == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}

	switch req.Method {
	case http.MethodGet:
		tools, err := r.store.GetToolDefaults(req.Context())
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}
		if tools == nil {
			tools = []store.ToolDefinition{}
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"tools": tools})

	case http.MethodPut:
		var body toolUpdateRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		hasToolName := body.ToolName != nil && strings.TrimSpace(*body.ToolName) != ""
		hasCategory := body.Category != nil && strings.TrimSpace(*body.Category) != ""
		if !hasToolName && !hasCategory {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		if hasCategory {
			if err := r.store.SetCategoryDefaults(req.Context(), strings.TrimSpace(*body.Category), body.Enabled); err != nil {
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
		} else {
			if err := r.store.SetToolDefault(req.Context(), strings.TrimSpace(*body.ToolName), body.Enabled); err != nil {
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
		}

		r.writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
	}
}

func (r *Router) handleManagedAgentTools(w http.ResponseWriter, req *http.Request, agentID string) {
	if r.store == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}

	switch req.Method {
	case http.MethodGet:
		tools, err := r.store.GetAgentTools(req.Context(), agentID)
		if err != nil {
			var notFound *store.ManagedAgentNotFoundError
			if errors.As(err, &notFound) {
				r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
				return
			}
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		activeWorkflowState, err := r.store.GetManagedAgentActiveWorkflowState(req.Context(), agentID)
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
			return
		}

		workflowOverride := false
		activeWorkflowStateName := ""
		if activeWorkflowState != nil && len(activeWorkflowState.AllowedTools) > 0 {
			workflowOverride = true
			activeWorkflowStateName = activeWorkflowState.Name
		}

		respTools := make([]agentToolResponseItem, 0, len(tools))
		for _, td := range tools {
			effectiveEnabled := td.Enabled
			if workflowOverride {
				effectiveEnabled = store.IsToolAllowedInWorkflowState(td.ToolName, activeWorkflowState.AllowedTools)
			}
			respTools = append(respTools, agentToolResponseItem{
				ToolName:         td.ToolName,
				Category:         td.Category,
				Enabled:          td.Enabled,
				EffectiveEnabled: effectiveEnabled,
				MCPServerID:      td.MCPServerID,
			})
		}

		r.writeJSON(w, http.StatusOK, agentToolsResponse{
			AgentID:             agentID,
			WorkflowOverride:    workflowOverride,
			ActiveWorkflowState: activeWorkflowStateName,
			Tools:               respTools,
		})

	case http.MethodPut:
		var body toolUpdateRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		hasToolName := body.ToolName != nil && strings.TrimSpace(*body.ToolName) != ""
		hasCategory := body.Category != nil && strings.TrimSpace(*body.Category) != ""
		if !hasToolName && !hasCategory {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}

		if hasCategory {
			if err := r.store.SetAgentCategoryTools(req.Context(), agentID, strings.TrimSpace(*body.Category), body.Enabled); err != nil {
				var notFound *store.ManagedAgentNotFoundError
				if errors.As(err, &notFound) {
					r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
					return
				}
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
		} else {
			if err := r.store.SetAgentTool(req.Context(), agentID, strings.TrimSpace(*body.ToolName), body.Enabled); err != nil {
				var notFound *store.ManagedAgentNotFoundError
				if errors.As(err, &notFound) {
					r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
					return
				}
				r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
				return
			}
		}

		r.writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
	}
}

type CatalogToolItem struct {
	ToolName       string  `json:"tool_name"`
	Category       string  `json:"category"`
	Description    string  `json:"description"`
	DefaultEnabled bool    `json:"default_enabled"`
	IsAdmin        bool    `json:"is_admin"`
	MCPServerID    *string `json:"mcp_server_id,omitempty"`
}

func (r *Router) handleToolsCatalog(w http.ResponseWriter, req *http.Request) {
	if r.store == nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	defaults, err := r.store.GetToolDefaults(req.Context())
	if err != nil {
		r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "internal_error"})
		return
	}
	canonMap := make(map[string]string)
	for _, ct := range store.CanonicalWorkflowTools {
		canonMap[ct.ID] = ct.Description
		canonMap[ct.Name] = ct.Description
	}
	var catalogTools []CatalogToolItem
	categorySet := make(map[string]struct{})
	var categories []string
	for _, td := range defaults {
		desc := canonMap[td.ToolName]
		if desc == "" {
			desc = td.ToolName
		}
		isAdmin := td.Category == "Worker Administration" || strings.HasPrefix(td.ToolName, "worker_")
		catalogTools = append(catalogTools, CatalogToolItem{
			ToolName:       td.ToolName,
			Category:       td.Category,
			Description:    desc,
			DefaultEnabled: td.Enabled,
			IsAdmin:        isAdmin,
			MCPServerID:    td.MCPServerID,
		})
		if _, exists := categorySet[td.Category]; !exists {
			categorySet[td.Category] = struct{}{}
			categories = append(categories, td.Category)
		}
	}
	r.writeJSON(w, http.StatusOK, map[string]any{
		"tools":      catalogTools,
		"categories": categories,
	})
}

