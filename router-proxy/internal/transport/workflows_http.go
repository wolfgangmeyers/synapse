package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var filenameSanitizeRegex = regexp.MustCompile(`[^a-zA-Z0-9_\-]+`)

func sanitizeFilename(name string) string {
	s := strings.TrimSpace(name)
	s = filenameSanitizeRegex.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return strings.ToLower(s)
}

func (r *Router) handleWorkflowImport(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
		return
	}
	var bundle store.WorkflowExportBundle
	if err := json.NewDecoder(req.Body).Decode(&bundle); err != nil {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
		return
	}
	createdWf, _, err := r.store.ImportWorkflow(req.Context(), bundle)
	if err != nil {
		r.writeError(w, storeError(err))
		return
	}
	r.writeJSON(w, http.StatusCreated, createdWf)
}

func (r *Router) handleWorkflowsCollection(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		workflows, err := r.store.ListWorkflows(req.Context())
		if err != nil {
			r.writeError(w, &httpError{status: 500, code: "internal_error"})
			return
		}
		r.writeJSON(w, http.StatusOK, workflows)
	case http.MethodPost:
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		reqData, err := parseWorkflowRequest(payload)
		if err != nil {
			r.writeError(w, err)
			return
		}
		wf, err := r.store.CreateWorkflow(req.Context(), reqData)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusCreated, wf)
	default:
		r.writeError(w, &httpError{status: 405, code: "method_not_allowed"})
	}
}

func (r *Router) handleWorkflowItem(w http.ResponseWriter, req *http.Request, id, action string) {
	if id == "" {
		r.writeError(w, &httpError{status: 400, code: "invalid_request"})
		return
	}

	switch action {
	case "":
		switch req.Method {
		case http.MethodGet:
			wf, err := r.store.GetWorkflow(req.Context(), id)
			if err != nil {
				r.writeError(w, storeError(err))
				return
			}
			if wf == nil {
				r.writeError(w, &httpError{status: 404, code: "workflow_not_found"})
				return
			}
			states, err := r.store.ListWorkflowStatesByWorkflow(req.Context(), id)
			if err != nil {
				r.writeError(w, storeError(err))
				return
			}
			r.writeJSON(w, http.StatusOK, map[string]any{
				"workflow": wf,
				"states":   states,
			})
		case http.MethodPut:
			payload, err := r.readJSON(req)
			if err != nil {
				r.writeError(w, err)
				return
			}
			reqData, err := parseWorkflowRequest(payload)
			if err != nil {
				r.writeError(w, err)
				return
			}

			// If name is omitted or empty in PUT payload, keep existing workflow's name and description if omitted
			if reqData.Name == "" {
				existingWf, err := r.store.GetWorkflow(req.Context(), id)
				if err != nil {
					r.writeError(w, storeError(err))
					return
				}
				if existingWf == nil {
					r.writeError(w, &httpError{status: 404, code: "workflow_not_found"})
					return
				}
				reqData.Name = existingWf.Name
				if reqData.Description == "" {
					reqData.Description = existingWf.Description
				}
			}

			// If entry_state_id is explicitly passed in payload (even if empty string or null),
			// handle setting or clearing it. Note: parseWorkflowRequest sets EntryStateID to nil
			// if omitted or explicit null; but if key was explicitly present as null or empty string,
			// we want to ensure EntryStateID can be explicitly cleared or set.
			if raw, exists := payload["entry_state_id"]; exists {
				if len(raw) == 0 || string(raw) == "null" {
					empty := ""
					reqData.EntryStateID = &empty
				}
			}

			wf, err := r.store.UpdateWorkflow(req.Context(), id, reqData)
			if err != nil {
				r.writeError(w, storeError(err))
				return
			}
			r.writeJSON(w, http.StatusOK, wf)
		case http.MethodDelete:
			deleted, err := r.store.DeleteWorkflow(req.Context(), id)
			if err != nil {
				r.writeError(w, storeError(err))
				return
			}
			if !deleted {
				r.writeError(w, &httpError{status: 404, code: "workflow_not_found"})
				return
			}
			r.writeNoContent(w)
		default:
			r.writeError(w, &httpError{status: 405, code: "method_not_allowed"})
		}

	case "duplicate":
		if req.Method != http.MethodPost {
			r.writeError(w, &httpError{status: 405, code: "method_not_allowed"})
			return
		}
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		var name string
		if raw, exists := payload["name"]; exists && len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &name); err != nil {
				r.writeError(w, &httpError{status: 400, code: "invalid_request"})
				return
			}
		}
		wf, err := r.store.DuplicateWorkflow(req.Context(), id, name)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusCreated, wf)

	case "layout":
		if req.Method != http.MethodPut {
			r.writeError(w, &httpError{status: 405, code: "method_not_allowed"})
			return
		}
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		positions, err := parseWorkflowStatePositions(payload)
		if err != nil {
			r.writeError(w, err)
			return
		}
		if err := r.store.UpdateWorkflowStatePositions(req.Context(), id, positions); err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "export":
		if req.Method != http.MethodGet {
			r.writeError(w, &httpError{status: 405, code: "method_not_allowed"})
			return
		}
		bundle, err := r.store.ExportWorkflow(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		sanitizedName := sanitizeFilename(bundle.Workflow.Name)
		if sanitizedName == "" {
			sanitizedName = "workflow"
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-workflow.json"`, sanitizedName))
		r.writeJSON(w, http.StatusOK, bundle)

	default:
		r.writeError(w, &httpError{status: 404, code: "not_found"})
	}
}

func parseWorkflowRequest(payload map[string]json.RawMessage) (store.WorkflowRequest, error) {
	name, err := rawString(payload, "name", false)
	if err != nil {
		return store.WorkflowRequest{}, &httpError{status: 400, code: "invalid_request"}
	}
	desc, _ := rawString(payload, "description", false)

	var entryStateID *string
	if raw, exists := payload["entry_state_id"]; exists && len(raw) > 0 && string(raw) != "null" {
		var entryID string
		if err := json.Unmarshal(raw, &entryID); err != nil {
			return store.WorkflowRequest{}, &httpError{status: 400, code: "invalid_request"}
		}
		entryStateID = &entryID
	}

	var variables *[]store.WorkflowVariableDefinition
	if raw, exists := payload["variables"]; exists && len(raw) > 0 && string(raw) != "null" {
		var vars []store.WorkflowVariableDefinition
		if err := json.Unmarshal(raw, &vars); err != nil {
			return store.WorkflowRequest{}, &httpError{status: 400, code: "invalid_request"}
		}
		variables = &vars
	}

	return store.WorkflowRequest{
		Name:         name,
		Description:  desc,
		EntryStateID: entryStateID,
		Variables:    variables,
	}, nil
}

func parseWorkflowStatePositions(payload map[string]json.RawMessage) ([]store.WorkflowStatePosition, error) {
	raw, ok := payload["positions"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, &httpError{status: 400, code: "invalid_request"}
	}

	var rawPositions []struct {
		StateID *string  `json:"state_id"`
		UIPosX  *float64 `json:"ui_pos_x"`
		UIPosY  *float64 `json:"ui_pos_y"`
	}
	if err := json.Unmarshal(raw, &rawPositions); err != nil {
		return nil, &httpError{status: 400, code: "invalid_request"}
	}

	positions := make([]store.WorkflowStatePosition, 0, len(rawPositions))
	for _, p := range rawPositions {
		if p.StateID == nil || p.UIPosX == nil || p.UIPosY == nil {
			return nil, &httpError{status: 400, code: "invalid_request"}
		}
		positions = append(positions, store.WorkflowStatePosition{
			StateID: *p.StateID,
			UIPosX:  *p.UIPosX,
			UIPosY:  *p.UIPosY,
		})
	}
	return positions, nil
}
