package transport

import (
	"net/http"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// handleWorkflowCollections handles collection listing and creation on /v1/workflow-collections.
func (r *Router) handleWorkflowCollections(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		collections, err := r.store.ListWorkflowCollections(req.Context())
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusOK, collections)
	case http.MethodPost:
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		name, err := rawString(payload, "name", true)
		if err != nil {
			r.writeError(w, &httpError{status: 400, code: "invalid_request"})
			return
		}
		desc, _ := rawString(payload, "description", false)

		col, err := r.store.CreateWorkflowCollection(req.Context(), store.WorkflowCollectionRequest{
			Name:        name,
			Description: desc,
		})
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusCreated, col)
	default:
		r.writeError(w, &httpError{status: 405, code: "method_not_allowed"})
	}
}

// handleWorkflowCollectionItem handles collection retrieval, modification, and deletion on /v1/workflow-collections/:id.
// It enforces default collection immutability (modifications and deletions of 'default' return HTTP 400).
func (r *Router) handleWorkflowCollectionItem(w http.ResponseWriter, req *http.Request, id string) {
	if id == "" {
		r.writeError(w, &httpError{status: 400, code: "invalid_request"})
		return
	}

	switch req.Method {
	case http.MethodGet:
		col, err := r.store.GetWorkflowCollection(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if col == nil {
			r.writeError(w, &httpError{status: 404, code: "workflow_collection_not_found"})
			return
		}
		r.writeJSON(w, http.StatusOK, col)

	case http.MethodPut:
		// Default collection name and description are immutable
		if id == store.DefaultWorkflowCollectionID {
			r.writeError(w, &httpError{status: 400, code: "cannot_modify_default_collection"})
			return
		}
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		name, err := rawString(payload, "name", true)
		if err != nil {
			r.writeError(w, &httpError{status: 400, code: "invalid_request"})
			return
		}
		desc, _ := rawString(payload, "description", false)

		col, err := r.store.UpdateWorkflowCollection(req.Context(), id, store.WorkflowCollectionRequest{
			Name:        name,
			Description: desc,
		})
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusOK, col)

	case http.MethodDelete:
		// Default collection cannot be deleted as it is the system fallback
		if id == store.DefaultWorkflowCollectionID {
			r.writeError(w, &httpError{status: 400, code: "cannot_delete_default_collection"})
			return
		}
		cascade := strings.EqualFold(req.URL.Query().Get("cascade"), "true")
		if err := r.store.DeleteWorkflowCollection(req.Context(), id, cascade); err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeNoContent(w)

	default:
		r.writeError(w, &httpError{status: 405, code: "method_not_allowed"})
	}
}
