package transport

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// workflowJournalPath accepts exactly the agent-scoped collection or one run.
func workflowJournalPath(path string) (agentID, runID string, ok bool) {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] != "workflow-journal" {
		return "", "", false
	}
	if len(parts) == 3 {
		if parts[2] == "" {
			return "", "", false
		}
		runID = parts[2]
	}
	return parts[0], runID, true
}

func workflowJournalRouteMatched(path string) bool {
	_, _, ok := workflowJournalPath(path)
	return ok
}

func (r *Router) handleWorkflowJournal(w http.ResponseWriter, req *http.Request, agentID, runID string) {
	fail := func(status int, code, message string) {
		r.writeManagedError(w, req, &managedHTTPError{status, code, message})
	}
	limit := 0
	if raw := req.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			fail(http.StatusBadRequest, "INVALID_REQUEST", "Invalid page limit")
			return
		}
		limit = parsed
	}
	writeErr := func(err error) {
		switch {
		case errors.Is(err, store.ErrWorkflowJournalNotFound):
			fail(http.StatusNotFound, "NOT_FOUND", "Workflow journal was not found")
		case errors.Is(err, store.ErrWorkflowJournalActive):
			fail(http.StatusConflict, "WORKFLOW_JOURNAL_ACTIVE", "Active runs cannot be deleted")
		case errors.Is(err, store.ErrWorkflowJournalCursor):
			fail(http.StatusBadRequest, "INVALID_CURSOR", "Invalid workflow journal cursor")
		default:
			fail(http.StatusInternalServerError, "INTERNAL", "Workflow journal request failed")
		}
	}
	switch {
	case req.Method == http.MethodGet && runID == "":
		q := req.URL.Query()
		status := q.Get("status")
		if status != "" && status != "active" && status != "terminal" {
			fail(http.StatusBadRequest, "INVALID_REQUEST", "Invalid workflow journal status")
			return
		}
		page, err := r.store.ListWorkflowJournalRuns(req.Context(), agentID, store.WorkflowJournalFilter{WorkflowID: q.Get("workflow_id"), Status: status, Cursor: q.Get("cursor"), Limit: limit})
		if err != nil {
			writeErr(err)
			return
		}
		r.writeJSON(w, http.StatusOK, page)
	case req.Method == http.MethodGet && runID != "":
		detail, err := r.store.GetWorkflowJournalRun(req.Context(), agentID, runID, store.WorkflowJournalTimelineFilter{Cursor: req.URL.Query().Get("cursor"), Limit: limit})
		if err != nil {
			writeErr(err)
			return
		}
		r.writeJSON(w, http.StatusOK, detail)
	case req.Method == http.MethodDelete && runID != "":
		if err := r.store.DeleteWorkflowJournalRun(req.Context(), agentID, runID); err != nil {
			writeErr(err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		fail(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
	}
}
