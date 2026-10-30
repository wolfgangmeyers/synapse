package transport

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (r *Router) handleWorkflowSchedulesCollection(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		workflowID := req.URL.Query().Get("workflow_id")
		schedules, err := r.store.ListWorkflowSchedules(req.Context(), workflowID)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"schedules": schedules})

	case http.MethodPost:
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		var scheduleReq store.WorkflowScheduleRequest
		b, err := json.Marshal(payload)
		if err != nil {
			r.writeError(w, &httpError{status: 400, code: "invalid_request"})
			return
		}
		if err := json.Unmarshal(b, &scheduleReq); err != nil {
			r.writeError(w, &httpError{status: 400, code: "invalid_request"})
			return
		}
		created, err := r.store.CreateWorkflowSchedule(req.Context(), scheduleReq)
		if err != nil {
			if valErr, ok := err.(*store.WorkflowValidationError); ok {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{"error": valErr.Error(), "code": valErr.Code()})
				return
			}
			if notFound, ok := err.(*store.WorkflowNotFoundError); ok {
				r.writeError(w, &httpError{status: 404, code: notFound.Code()})
				return
			}
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusCreated, map[string]any{"schedule": created})

	default:
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
	}
}

func (r *Router) handleWorkflowScheduleItem(w http.ResponseWriter, req *http.Request, id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		r.writeError(w, &httpError{status: 400, code: "invalid_request"})
		return
	}

	switch req.Method {
	case http.MethodGet:
		schedule, err := r.store.GetWorkflowSchedule(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if schedule == nil {
			r.writeError(w, &httpError{status: 404, code: "schedule_not_found"})
			return
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"schedule": schedule})

	case http.MethodPatch, http.MethodPut:
		existing, err := r.store.GetWorkflowSchedule(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if existing == nil {
			r.writeError(w, &httpError{status: 404, code: "schedule_not_found"})
			return
		}
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}

		// Merge existing values
		updateReq := store.WorkflowScheduleRequest{
			WorkflowID:     existing.WorkflowID,
			Name:           existing.Name,
			Enabled:        &existing.Enabled,
			ScheduleType:   existing.ScheduleType,
			IntervalMS:     existing.IntervalMS,
			CronExpression: existing.CronExpression,
			Variables:      existing.Variables,
			TargetCwd:      existing.TargetCwd,
			ModelProvider:  existing.ModelProvider,
			ModelID:        existing.ModelID,
			NextRunAt:      existing.NextRunAt,
		}

		if name, err := rawString(payload, "name", false); err == nil && name != "" {
			updateReq.Name = name
		}
		if wfID, err := rawString(payload, "workflow_id", false); err == nil && wfID != "" {
			updateReq.WorkflowID = wfID
		}
		if enabled, exists := payload["enabled"]; exists {
			var b bool
			if err := json.Unmarshal(enabled, &b); err == nil {
				updateReq.Enabled = &b
			}
		}
		if schedType, err := rawString(payload, "schedule_type", false); err == nil && schedType != "" {
			updateReq.ScheduleType = schedType
		}
		if intervalRaw, exists := payload["interval_ms"]; exists {
			var ims int64
			if err := json.Unmarshal(intervalRaw, &ims); err == nil {
				updateReq.IntervalMS = &ims
			}
		}
		if cronExpr, err := rawString(payload, "cron_expression", false); err == nil && cronExpr != "" {
			updateReq.CronExpression = &cronExpr
		}
		if varsRaw, exists := payload["variables"]; exists && len(varsRaw) > 0 && string(varsRaw) != "null" {
			var vars map[string]string
			if err := json.Unmarshal(varsRaw, &vars); err == nil {
				updateReq.Variables = vars
			}
		}
		if cwd, err := rawString(payload, "target_cwd", false); err == nil && cwd != "" {
			updateReq.TargetCwd = cwd
		}
		if nextRun, err := rawString(payload, "next_run_at", false); err == nil && nextRun != "" {
			updateReq.NextRunAt = nextRun
		}

		updated, err := r.store.UpdateWorkflowSchedule(req.Context(), id, updateReq)
		if err != nil {
			if valErr, ok := err.(*store.WorkflowValidationError); ok {
				r.writeJSON(w, http.StatusBadRequest, map[string]any{"error": valErr.Error(), "code": valErr.Code()})
				return
			}
			if notFound, ok := err.(*store.WorkflowNotFoundError); ok {
				r.writeError(w, &httpError{status: 404, code: notFound.Code()})
				return
			}
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"schedule": updated})

	case http.MethodDelete:
		deleted, err := r.store.DeleteWorkflowSchedule(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if !deleted {
			r.writeError(w, &httpError{status: 404, code: "schedule_not_found"})
			return
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		r.writeError(w, &httpError{status: http.StatusMethodNotAllowed, code: "method_not_allowed"})
	}
}
