package transport

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// The prompt route is a direct managed control, not a mailbox send or an RPC
// prompt. Admission and durable retry identity are owned by the Manager/Store.
func managedPromptPath(path string) string {
	const prefix, suffix = "/v1/managed-agents/", "/prompt"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") || !managedName(id) {
		return ""
	}
	return id
}

func (r *Router) handleManagedPrompt(w http.ResponseWriter, req *http.Request, agentID string) {
	id := req.Header.Get("X-Request-ID")
	if !canonicalUUID(id) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 64*1024, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	if len(payload) != 1 {
		r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
		return
	}
	message, err := managedString(payload, "message", true)
	if err != nil || len(message) > 64*1024 {
		r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_PROMPT", "Prompt is invalid"})
		return
	}
	// Never fall through to AdmitPrompt's RPC branch: this ingress only admits
	// a saved embedded identity. The manager rechecks its binding under the lane.
	agent, err := r.store.ManagedAgent(req.Context(), agentID)
	if errors.Is(err, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
		return
	}
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read the managed agent"})
		return
	}
	if agent.Runtime != "embedded" {
		r.writeManagedError(w, req, &managedHTTPError{409, "PROMPT_UNAVAILABLE", "Embedded prompt is not available"})
		return
	}
	if err := r.managed.AdmitPromptWithRequestID(req.Context(), agentID, message, id); err != nil {
		// Unknown pending outcomes, stale owners, and conflicting IDs must all
		// fail closed. Do not leak provider or SQLite errors to the caller.
		r.writeManagedError(w, req, &managedHTTPError{409, "PROMPT_UNAVAILABLE", "Embedded prompt could not be admitted"})
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"request_id": id, "agent_id": agentID, "status": "complete"}, map[string]string{"X-Request-ID": id})
}
