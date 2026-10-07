package transport

import (
	"encoding/json"
	"net/http"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (r *Router) handleWorkflowStatesCollection(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		states, err := r.store.ListWorkflowStates(req.Context())
		if err != nil {
			r.writeError(w, &httpError{status: 500, code: "internal_error"})
			return
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"workflow_states": states})
	case http.MethodPost:
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		reqData, err := parseWorkflowStateRequest(payload)
		if err != nil {
			r.writeError(w, err)
			return
		}
		state, err := r.store.CreateWorkflowState(req.Context(), reqData)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusCreated, map[string]any{"workflow_state": state})
	default:
		r.writeError(w, &httpError{status: 405, code: "method_not_allowed"})
	}
}

func (r *Router) handleWorkflowStateItem(w http.ResponseWriter, req *http.Request, id string) {
	if id == "" {
		r.writeError(w, &httpError{status: 400, code: "invalid_request"})
		return
	}
	switch req.Method {
	case http.MethodGet:
		state, err := r.store.GetWorkflowState(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if state == nil {
			r.writeError(w, &httpError{status: 404, code: "not_found"})
			return
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"workflow_state": state})
	case http.MethodPut:
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		reqData, err := parseWorkflowStateRequest(payload)
		if err != nil {
			r.writeError(w, err)
			return
		}
		state, err := r.store.UpdateWorkflowState(req.Context(), id, reqData)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"workflow_state": state})
	case http.MethodDelete:
		deleted, err := r.store.DeleteWorkflowState(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if !deleted {
			r.writeError(w, &httpError{status: 404, code: "not_found"})
			return
		}
		r.writeNoContent(w)
	default:
		r.writeError(w, &httpError{status: 405, code: "method_not_allowed"})
	}
}

func parseWorkflowStateRequest(payload map[string]json.RawMessage) (store.WorkflowStateRequest, error) {
	name, err := rawString(payload, "name", true)
	if err != nil {
		return store.WorkflowStateRequest{}, &httpError{status: 400, code: "invalid_request"}
	}
	desc, _ := rawString(payload, "description", false)
	entryText, _ := rawString(payload, "entry_text", false)

	return store.WorkflowStateRequest{
		Name:        name,
		Description: desc,
		EntryText:   entryText,
	}, nil
}
