package transport

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/sdui"
)

// handleSDUIHTTP dispatches Server-Driven UI (Block Kit) requests.
// Supported endpoints:
// - GET  /v1/ui/bootstrap
// - GET  /v1/ui/views/sidebar
// - GET  /v1/ui/views/agents/{id}/conversation
// - GET  /v1/ui/views/agents/{id}/config
// - POST /v1/ui/action
func (r *Router) handleSDUIHTTP(w http.ResponseWriter, req *http.Request) bool {
	path := req.URL.Path

	if !strings.HasPrefix(path, "/v1/ui/") && path != "/v1/ui/action" && path != "/v1/ui/bootstrap" {
		return false
	}

	switch {
	case path == "/v1/ui/bootstrap":
		if req.Method != http.MethodGet {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return true
		}
		r.handleSDUIBootstrap(w, req)
		return true

	case path == "/v1/ui/views/sidebar":
		if req.Method != http.MethodGet {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return true
		}
		r.handleSDUISidebar(w, req)
		return true

	case strings.HasPrefix(path, "/v1/ui/views/agents/") && strings.HasSuffix(path, "/conversation"):
		if req.Method != http.MethodGet {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return true
		}
		r.handleSDUIConversation(w, req)
		return true

	case strings.HasPrefix(path, "/v1/ui/views/agents/") && strings.HasSuffix(path, "/config"):
		if req.Method != http.MethodGet {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return true
		}
		r.handleSDUIConfig(w, req)
		return true

	case path == "/v1/ui/action":
		if req.Method != http.MethodPost {
			r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
			return true
		}
		r.handleSDUIAction(w, req)
		return true

	default:
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
		return true
	}
}

func (r *Router) handleSDUIBootstrap(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	baseURL := req.URL.Query().Get("base_url")
	surface := sdui.GenerateBootstrapView(baseURL)
	r.writeJSON(w, http.StatusOK, surface)
}

func (r *Router) handleSDUISidebar(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var peers []sdui.PeerInfo
	if r.store != nil {
		snapshot, err := r.store.ManagedAgentsProjectionSnapshot(req.Context())
		if err == nil {
			for _, a := range snapshot.Agents {
				peers = append(peers, sdui.PeerInfo{
					ID:     a.AgentID,
					Name:   a.Name,
					Status: a.ActivityState,
					Active: a.DesiredState == "running",
				})
			}
		}
	}
	surface := sdui.GenerateSidebarView(peers)
	r.writeJSON(w, http.StatusOK, surface)
}

func (r *Router) handleSDUIConversation(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	prefix := "/v1/ui/views/agents/"
	suffix := "/conversation"
	trimmed := strings.TrimPrefix(req.URL.Path, prefix)
	agentID := strings.TrimSuffix(trimmed, suffix)
	if agentID == "" || strings.Contains(agentID, "/") {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_agent_id"})
		return
	}

	var messages []sdui.MessageInfo
	if r.store != nil {
		events, _, err := r.store.GetConversationTimeline(req.Context(), agentID, 50, nil)
		if err == nil {
			for _, ev := range events {
				content := ""
				if ev.Content != nil {
					content = *ev.Content
				}
				role := "assistant"
				if ev.Role != nil {
					role = *ev.Role
				}
				sender := role
				isTool := ev.Kind == "tool_invocation" || ev.Kind == "tool_call" || strings.HasPrefix(ev.Kind, "tool")
				messages = append(messages, sdui.MessageInfo{
					ID:        ev.ID,
					Sender:    sender,
					Role:      role,
					Content:   content,
					IsTool:    isTool,
					ToolName:  ev.Kind,
					Timestamp: strconv.FormatInt(ev.CreatedAt, 10),
				})
			}
		}
	}
	surface := sdui.GenerateConversationView(agentID, messages)
	r.writeJSON(w, http.StatusOK, surface)
}

func (r *Router) handleSDUIConfig(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	prefix := "/v1/ui/views/agents/"
	suffix := "/config"
	trimmed := strings.TrimPrefix(req.URL.Path, prefix)
	agentID := strings.TrimSuffix(trimmed, suffix)
	if agentID == "" || strings.Contains(agentID, "/") {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_agent_id"})
		return
	}

	config := sdui.AgentConfig{
		Name:             agentID,
		ModelProvider:    "google-vertex",
		ModelID:          "gemini-3.8-flash",
		Temperature:      0.7,
		MaxTokens:        4096,
		AllowInlineTools: true,
		AutoRestart:      true,
	}

	if r.store != nil {
		if agent, err := r.store.ManagedAgent(req.Context(), agentID); err == nil && agent != nil {
			config.Name = agent.Name
			if agent.SelectedModel.Provider != "" {
				config.ModelProvider = agent.SelectedModel.Provider
			}
			if agent.SelectedModel.ID != "" {
				config.ModelID = agent.SelectedModel.ID
			}
			config.AllowInlineTools = agent.AllowInlineTools
		}
	}

	surface := sdui.GenerateConfigView(agentID, config)
	r.writeJSON(w, http.StatusOK, surface)
}

func (r *Router) handleSDUIAction(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var payload sdui.ActionPayload
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_json"})
		return
	}
	resp := sdui.HandleAction(payload)
	r.writeJSON(w, http.StatusOK, resp)
}
