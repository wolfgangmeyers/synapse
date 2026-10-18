package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (r *Router) handleAgentMonitorHTTP(w http.ResponseWriter, req *http.Request, agentID, monitorID, action string) {
	if r.store == nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Store unavailable"})
		return
	}

	if monitorID == "" {
		// Collection: GET or POST /v1/agents/{id}/monitors
		r.handleManagedAgentMonitorsCollection(w, req, agentID)
		return
	}

	if action == "" {
		// Item: GET, PUT, or DELETE /v1/agents/{id}/monitors/{monitorId}
		r.handleManagedAgentMonitorItem(w, req, agentID, monitorID)
		return
	}

	if action == "evaluate" {
		if req.Method != http.MethodPost {
			r.writeManagedError(w, req, &managedHTTPError{405, "METHOD_NOT_ALLOWED", "Method not allowed"})
			return
		}
		r.handleManagedAgentMonitorEvaluate(w, req, agentID, monitorID)
		return
	}

	r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Resource not found"})
}

func (r *Router) handleManagedAgentMonitorEvaluate(w http.ResponseWriter, req *http.Request, agentID, monitorID string) {
	mon, err := r.store.GetManagedAgentMonitor(req.Context(), agentID, monitorID)
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to fetch agent monitor"})
		return
	}
	if mon == nil {
		r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Agent monitor not found"})
		return
	}

	if q := req.URL.Query().Get("since_sequence"); q != "" {
		if seq, err := strconv.ParseInt(q, 10, 64); err == nil {
			mon.LastEvaluatedSequence = seq
		}
	} else if req.Body != nil && req.ContentLength > 0 {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err == nil {
			if s, ok := body["since_sequence"]; ok {
				switch v := s.(type) {
				case float64:
					mon.LastEvaluatedSequence = int64(v)
				case string:
					if seq, err := strconv.ParseInt(v, 10, 64); err == nil {
						mon.LastEvaluatedSequence = seq
					}
				}
			}
		}
	}

	now := r.now()
	var fired bool
	if r.managed != nil {
		fired, err = r.managed.EvaluateSingleMonitor(req.Context(), mon, now)
		if err != nil {
			r.writeManagedError(w, req, &managedHTTPError{500, "EVALUATION_FAILED", err.Error()})
			return
		}
	} else {
		// No manager running
		r.writeManagedError(w, req, &managedHTTPError{503, "MANAGER_UNAVAILABLE", "Managed manager unavailable"})
		return
	}

	// Refetch updated monitor state
	updated, _ := r.store.GetManagedAgentMonitor(req.Context(), agentID, monitorID)
	if updated == nil {
		updated = mon
	}

	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{
		"evaluated": true,
		"fired":     fired,
		"monitor":   updated,
	}, nil)
}

// handleManagedAgentMonitorsCollection handles GET /v1/managed-agents/{agentID}/monitors and POST /v1/managed-agents/{agentID}/monitors
func (r *Router) handleManagedAgentMonitorsCollection(w http.ResponseWriter, req *http.Request, agentID string) {
	switch req.Method {
	case http.MethodGet:
		monitors, err := r.store.ListManagedAgentMonitors(req.Context(), agentID)
		if err != nil {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to list agent monitors"})
			return
		}
		r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"monitors": monitors}, nil)
	case http.MethodPost:
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", "Invalid request body"})
			return
		}
		mon, err := parseAgentMonitorPayload(agentID, "", payload)
		if err != nil {
			r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", err.Error()})
			return
		}
		if err := r.store.CreateManagedAgentMonitor(req.Context(), mon); err != nil {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to create agent monitor"})
			return
		}
		r.writeManagedJSON(w, req, http.StatusCreated, map[string]any{"monitor": mon}, nil)
	default:
		r.writeManagedError(w, req, &managedHTTPError{405, "METHOD_NOT_ALLOWED", "Method not allowed"})
	}
}

// handleManagedAgentMonitorItem handles GET, PUT, and DELETE /v1/managed-agents/{agentID}/monitors/{monitorID}
func (r *Router) handleManagedAgentMonitorItem(w http.ResponseWriter, req *http.Request, agentID, monitorID string) {
	switch req.Method {
	case http.MethodGet:
		mon, err := r.store.GetManagedAgentMonitor(req.Context(), agentID, monitorID)
		if err != nil {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to get agent monitor"})
			return
		}
		if mon == nil {
			r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Agent monitor not found"})
			return
		}
		r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"monitor": mon}, nil)
	case http.MethodPut:
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", "Invalid request body"})
			return
		}
		existing, err := r.store.GetManagedAgentMonitor(req.Context(), agentID, monitorID)
		if err != nil {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to fetch existing agent monitor"})
			return
		}
		if existing == nil {
			r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Agent monitor not found"})
			return
		}

		mon, err := parseAgentMonitorPayload(agentID, monitorID, payload)
		if err != nil {
			r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", err.Error()})
			return
		}
		// Preserve non-mutable or unpassed fields from existing
		mon.CreatedAt = existing.CreatedAt
		mon.NextEvalAt = existing.NextEvalAt
		mon.LastEvaluatedAt = existing.LastEvaluatedAt
		mon.LastFiredAt = existing.LastFiredAt
		mon.ConsecutiveAlerts = existing.ConsecutiveAlerts

		if err := r.store.UpdateManagedAgentMonitor(req.Context(), mon); err != nil {
			if errors.Is(err, store.ErrManagedAgentMonitorNotFound) {
				r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Agent monitor not found"})
				return
			}
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to update agent monitor"})
			return
		}
		r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"monitor": mon}, nil)
	case http.MethodDelete:
		if err := r.store.DeleteManagedAgentMonitor(req.Context(), agentID, monitorID); err != nil {
			if errors.Is(err, store.ErrManagedAgentMonitorNotFound) {
				r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Agent monitor not found"})
				return
			}
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to delete agent monitor"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		r.writeManagedError(w, req, &managedHTTPError{405, "METHOD_NOT_ALLOWED", "Method not allowed"})
	}
}

func parseAgentMonitorPayload(agentID, monitorID string, payload map[string]json.RawMessage) (*store.ManagedAgentMonitor, error) {
	mon := &store.ManagedAgentMonitor{
		AgentID: agentID,
		ID:      monitorID,
		Enabled: true,
	}

	if raw, ok := payload["id"]; ok && len(raw) > 0 && string(raw) != "null" && monitorID == "" {
		var id string
		if err := json.Unmarshal(raw, &id); err == nil && strings.TrimSpace(id) != "" {
			mon.ID = strings.TrimSpace(id)
		}
	}

	if raw, ok := payload["name"]; ok && len(raw) > 0 && string(raw) != "null" {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return nil, errors.New("name must be a string")
		}
		mon.Name = strings.TrimSpace(name)
	}
	if mon.Name == "" {
		return nil, errors.New("name is required")
	}

	if raw, ok := payload["description"]; ok && len(raw) > 0 && string(raw) != "null" {
		var desc string
		_ = json.Unmarshal(raw, &desc)
		mon.Description = desc
	}

	if raw, ok := payload["enabled"]; ok && len(raw) > 0 && string(raw) != "null" {
		var enabled bool
		if err := json.Unmarshal(raw, &enabled); err != nil {
			return nil, errors.New("enabled must be a boolean")
		}
		mon.Enabled = enabled
	}

	if raw, ok := payload["interval_seconds"]; ok && len(raw) > 0 && string(raw) != "null" {
		var interval int
		if err := json.Unmarshal(raw, &interval); err != nil {
			return nil, errors.New("interval_seconds must be an integer")
		}
		mon.IntervalSeconds = interval
	}

	if raw, ok := payload["cooldown_seconds"]; ok && len(raw) > 0 && string(raw) != "null" {
		var cooldown int
		if err := json.Unmarshal(raw, &cooldown); err != nil {
			return nil, errors.New("cooldown_seconds must be an integer")
		}
		mon.CooldownSeconds = cooldown
	}

	if raw, ok := payload["max_alerts"]; ok && len(raw) > 0 && string(raw) != "null" {
		var maxAlerts int
		if err := json.Unmarshal(raw, &maxAlerts); err != nil {
			return nil, errors.New("max_alerts must be an integer")
		}
		mon.MaxAlerts = maxAlerts
	}

	if raw, ok := payload["trailing_messages_count"]; ok && len(raw) > 0 && string(raw) != "null" {
		var trailing int
		if err := json.Unmarshal(raw, &trailing); err != nil {
			return nil, errors.New("trailing_messages_count must be an integer")
		}
		mon.TrailingMessagesCount = trailing
	}

	if raw, ok := payload["question_type"]; ok && len(raw) > 0 && string(raw) != "null" {
		var qType string
		if err := json.Unmarshal(raw, &qType); err != nil {
			return nil, errors.New("question_type must be a string")
		}
		mon.QuestionType = strings.TrimSpace(qType)
	}
	if mon.QuestionType == "" {
		mon.QuestionType = "noul"
	}

	if raw, ok := payload["question_instructions"]; ok && len(raw) > 0 && string(raw) != "null" {
		var qInst string
		if err := json.Unmarshal(raw, &qInst); err != nil {
			return nil, errors.New("question_instructions must be a string")
		}
		mon.QuestionInstructions = strings.TrimSpace(qInst)
	}
	if mon.QuestionInstructions == "" {
		return nil, errors.New("question_instructions is required")
	}

	if raw, ok := payload["question_criteria"]; ok && len(raw) > 0 && string(raw) != "null" {
		mon.QuestionCriteria = raw
	}
	if raw, ok := payload["question_options"]; ok && len(raw) > 0 && string(raw) != "null" {
		mon.QuestionOptions = raw
	}

	if raw, ok := payload["condition_operator"]; ok && len(raw) > 0 && string(raw) != "null" {
		var op string
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil, errors.New("condition_operator must be a string")
		}
		mon.ConditionOperator = strings.TrimSpace(op)
	}
	if mon.ConditionOperator == "" {
		mon.ConditionOperator = "gte"
	}

	if raw, ok := payload["condition_threshold"]; ok && len(raw) > 0 && string(raw) != "null" {
		var th string
		if err := json.Unmarshal(raw, &th); err != nil {
			// Try unmarshaling float/number as string
			var num float64
			if errNum := json.Unmarshal(raw, &num); errNum != nil {
				return nil, errors.New("condition_threshold must be a string or number")
			}
			th = strings.TrimSpace(string(raw))
		}
		mon.ConditionThreshold = strings.TrimSpace(th)
	}
	if mon.ConditionThreshold == "" {
		mon.ConditionThreshold = "0.8"
	}

	if raw, ok := payload["last_evaluated_sequence"]; ok && len(raw) > 0 && string(raw) != "null" {
		var seq int64
		if err := json.Unmarshal(raw, &seq); err != nil {
			return nil, errors.New("last_evaluated_sequence must be an integer")
		}
		mon.LastEvaluatedSequence = seq
	}

	if raw, ok := payload["nudge_template"]; ok && len(raw) > 0 && string(raw) != "null" {
		var nudge string
		if err := json.Unmarshal(raw, &nudge); err != nil {
			return nil, errors.New("nudge_template must be a string")
		}
		mon.NudgeTemplate = strings.TrimSpace(nudge)
	}
	if mon.NudgeTemplate == "" {
		return nil, errors.New("nudge_template is required")
	}

	if raw, ok := payload["fallback_mode"]; ok && len(raw) > 0 && string(raw) != "null" {
		var fb string
		if err := json.Unmarshal(raw, &fb); err != nil {
			return nil, errors.New("fallback_mode must be a string")
		}
		mon.FallbackMode = strings.TrimSpace(fb)
	}
	if mon.FallbackMode == "" {
		mon.FallbackMode = "skip"
	}

	if raw, ok := payload["workflow_state_id"]; ok && len(raw) > 0 && string(raw) != "null" {
		var wsID string
		if err := json.Unmarshal(raw, &wsID); err == nil && strings.TrimSpace(wsID) != "" {
			trimmed := strings.TrimSpace(wsID)
			mon.WorkflowStateID = &trimmed
		}
	}

	return mon, nil
}
