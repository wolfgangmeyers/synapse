package transport

import (
	"net/http"
	"strings"
)

// managedErrorPayload is the one public error shape for direct managed routes.
// It carries stable, local troubleshooting context without exposing process
// identity, session data, credentials, request bodies, or filesystem details.
func (r *Router) managedErrorPayload(req *http.Request, candidate *managedHTTPError) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"code":    candidate.code,
			"message": candidate.message,
			"details": managedErrorDetails(req, candidate.code),
		},
		"request_id": requestIDFromRequest(req),
	}
}

func managedErrorDetails(req *http.Request, code string) map[string]any {
	details := map[string]any{
		"operation": managedOperationName(req),
		"state":     managedDiagnosticState(code),
		"fence":     managedDiagnosticFence(code),
		"cause":     managedDiagnosticCause(code),
	}
	if requestID := requestIDFromRequest(req); requestID != nil {
		details["request_id"] = requestID
	}
	if agentID := managedDiagnosticAgentID(req); agentID != "" {
		details["agent_id"] = agentID
	}
	if runID := managedDiagnosticRunID(req); runID != "" {
		details["run_id"] = runID
	}
	return details
}

func managedOperationName(req *http.Request) string {
	path := req.URL.Path
	switch {
	case path == "/v1/ui-live":
		return "ui_live_connect"
	case path == "/v1/managed-agents" && req.Method == http.MethodPost:
		return "managed_agent_launch"
	case path == "/v1/managed-agents":
		return "managed_agent_list"
	case path == "/v1/managed-agents/restart-all":
		return "managed_agent_restart_all"
	case strings.HasSuffix(path, "/restart"):
		return "managed_agent_restart"
	case strings.HasSuffix(path, "/reset"):
		return "managed_agent_reset"
	case strings.HasSuffix(path, "/children"):
		return "managed_children_read"
	case strings.HasPrefix(path, "/v1/managed-agents/") && req.Method == http.MethodDelete:
		return "managed_agent_terminate"
	case strings.HasPrefix(path, "/v1/managed-agents/"):
		return "managed_agent_read"
	case path == "/v1/managed-directories":
		return "managed_directory_read"
	case path == "/v1/history":
		return "managed_history_read"
	case path == "/v1/models":
		return "managed_model_catalog_read"
	case path == "/v1/settings/default-model":
		return "managed_default_model_update"
	case strings.HasPrefix(path, "/v1/provider-auth/operations/"):
		return "provider_auth_response"
	case strings.HasPrefix(path, "/v1/provider-auth/"):
		return "provider_auth_action"
	case path == "/v1/provider-auth":
		return "provider_auth_read"
	default:
		return "managed_route_request"
	}
}

func managedDiagnosticAgentID(req *http.Request) string {
	if req.URL.Path == "/v1/history" {
		if id := req.URL.Query().Get("agent_id"); managedName(id) {
			return id
		}
		return ""
	}
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(req.URL.Path, prefix) {
		return ""
	}
	remainder := strings.TrimPrefix(req.URL.Path, prefix)
	if remainder == "restart-all" {
		return ""
	}
	id, _, _ := strings.Cut(remainder, "/")
	if managedName(id) {
		return id
	}
	return ""
}

func managedDiagnosticRunID(req *http.Request) string {
	const prefix = "/v1/provider-auth/operations/"
	if !strings.HasPrefix(req.URL.Path, prefix) {
		return ""
	}
	value := strings.TrimPrefix(req.URL.Path, prefix)
	operationID, remainder, found := strings.Cut(value, "/")
	if found && remainder == "response" && canonicalUUID(operationID) {
		return operationID
	}
	return ""
}

func managedDiagnosticState(code string) string {
	switch code {
	case "MANAGED_DISABLED":
		return "disabled"
	case "AGENT_NOT_FOUND":
		return "missing"
	case "AGENT_NOT_READY":
		return "not_ready"
	case "CONFLICT", "PROVIDER_AUTH_BUSY":
		return "operation_in_progress"
	case "INTERNAL", "MODEL_CATALOG_UNAVAILABLE", "PROVIDER_AUTH_UNAVAILABLE":
		return "unavailable"
	default:
		return "rejected"
	}
}

func managedDiagnosticFence(code string) string {
	switch code {
	case "MANAGED_DISABLED":
		return "unavailable"
	case "AGENT_NOT_FOUND":
		return "absent"
	case "CONFLICT", "PROVIDER_AUTH_BUSY":
		return "preserved"
	default:
		return "not_checked"
	}
}

func managedDiagnosticCause(code string) string {
	switch code {
	case "INTERNAL":
		return "managed_router_internal_error"
	case "MANAGED_DISABLED":
		return "managed_surface_disabled"
	case "CONFLICT":
		return "managed_lifecycle_operation_in_progress"
	case "AGENT_NOT_READY":
		return "managed_agent_not_ready"
	case "AGENT_NOT_FOUND":
		return "managed_agent_not_found"
	case "MODEL_CATALOG_UNAVAILABLE":
		return "managed_model_catalog_unavailable"
	case "PROVIDER_AUTH_UNAVAILABLE":
		return "provider_auth_unavailable"
	default:
		return strings.ToLower(code)
	}
}
