package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	workflowStateIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

const (
	maxWorkflowStateNameBytes        = 256
	maxWorkflowStateDescriptionBytes = 4096
	maxWorkflowStateEntryTextBytes   = 65536
	maxWorkflowReminderBodyBytes     = 16384
	maxWorkflowReminderDelaySeconds  = 2592000
	maxWorkflowTransitionDescriptionBytes = 1024
)

type WorkflowTransitionConfig struct {
	TargetStateID string `json:"target_state_id"`
	Description   string `json:"description"`
}

type WorkflowReminderType string

const (
	WorkflowReminderOneShot   WorkflowReminderType = "one_shot"
	WorkflowReminderRecurring WorkflowReminderType = "recurring"
)

type WorkflowReminderConfig struct {
	ID              string               `json:"id"`
	Type            WorkflowReminderType `json:"type"`
	Subject         *string              `json:"subject,omitempty"`
	Body            string               `json:"body"`
	DelaySeconds    int64                `json:"delay_seconds"`
	IntervalSeconds *int64               `json:"interval_seconds,omitempty"`
}

type WorkflowState struct {
	ID           string                   `json:"id"`
	Name         string                   `json:"name"`
	Description  string                   `json:"description"`
	EntryText    string                   `json:"entry_text"`
	Reminders    []WorkflowReminderConfig `json:"reminders"`
	AllowedTools []string                 `json:"allowed_tools"`
	Transitions  []WorkflowTransitionConfig `json:"transitions"`
	AllowExit    bool                     `json:"allow_exit"`
	CreatedAt    string                   `json:"created_at"`
	UpdatedAt    string                   `json:"updated_at"`
}

type WorkflowStateRequest struct {
	ID           *string                  `json:"id,omitempty"`
	Name         string                   `json:"name"`
	Description  string                   `json:"description"`
	EntryText    string                   `json:"entry_text"`
	Reminders    []WorkflowReminderConfig `json:"reminders"`
	AllowedTools []string                 `json:"allowed_tools"`
	Transitions  []WorkflowTransitionConfig `json:"transitions"`
	AllowExit    *bool                    `json:"allow_exit,omitempty"`
}

type WorkflowStateValidationError struct {
	Field   string
	Message string
}

func (e *WorkflowStateValidationError) Error() string {
	return fmt.Sprintf("invalid workflow state (%s): %s", e.Field, e.Message)
}
func (e *WorkflowStateValidationError) Code() string { return "invalid_workflow_state" }

type WorkflowStateNameConflictError struct {
	Name string
}

func (e *WorkflowStateNameConflictError) Error() string {
	return fmt.Sprintf("workflow state name %q already exists", e.Name)
}
func (e *WorkflowStateNameConflictError) Code() string { return "workflow_state_name_conflict" }

type WorkflowStateNotFoundError struct {
	ID string
}

func (e *WorkflowStateNotFoundError) Error() string {
	return fmt.Sprintf("workflow state %q not found", e.ID)
}
func (e *WorkflowStateNotFoundError) Code() string { return "workflow_state_not_found" }

type WorkflowStateInUseError struct {
	ID      string
	ReferrerState string
}

func (e *WorkflowStateInUseError) Error() string {
	if e.ReferrerState != "" {
		return fmt.Sprintf("workflow state %q is in use as a transition target by state %q", e.ID, e.ReferrerState)
	}
	return fmt.Sprintf("workflow state %q is in use as a transition target", e.ID)
}
func (e *WorkflowStateInUseError) Code() string { return "workflow_state_in_use" }

var ValidWorkflowTools = map[string]bool{
	"mecha_mailbox_send":  true,
	"goal":                true,
	"goal_snooze":         true,
	"mecha_context":       true,
	"mecha_reminder":      true,
	"context_reset":       true,
	"reset":               true,
	"scratchpad":          true,
	"bash":                true,
	"read":                true,
	"edit":                true,
	"write":               true,
	"subagent":            true,
	"get_subagent_result": true,
	"steer_subagent":      true,
	"mecha_worker":        true,
	"mecha_workflow_transition": true,
	"workflow_transition":       true,
}

// IsToolAllowedInWorkflowState reports whether toolName is permitted under allowedTools.
// If allowedTools is empty, all tools are permitted.
// If allowedTools is non-empty, only tools in allowedTools are permitted,
// treating "context_reset" and "reset" as interchangeable aliases.
// mecha_workflow_transition and workflow_transition are implicitly allowed control tools.
func IsToolAllowedInWorkflowState(toolName string, allowedTools []string) bool {
	if toolName == "mecha_workflow_transition" || toolName == "workflow_transition" {
		return true
	}
	if len(allowedTools) == 0 {
		return true
	}
	for _, allowed := range allowedTools {
		if allowed == toolName {
			return true
		}
		if (allowed == "context_reset" || allowed == "reset") && (toolName == "context_reset" || toolName == "reset") {
			return true
		}
		if (allowed == "mecha_workflow_transition" || allowed == "workflow_transition") && (toolName == "mecha_workflow_transition" || toolName == "workflow_transition") {
			return true
		}
	}
	return false
}

func sanitizeWorkflowAllowedTools(tools []string) ([]string, error) {
	if len(tools) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]bool, len(tools))
	result := make([]string, 0, len(tools))
	for _, raw := range tools {
		t := strings.TrimSpace(raw)
		if !ValidWorkflowTools[t] {
			return nil, &WorkflowStateValidationError{Field: "allowed_tools", Message: fmt.Sprintf("invalid tool %q", t)}
		}
		if !seen[t] {
			seen[t] = true
			result = append(result, t)
		}
	}
	return result, nil
}

func validateWorkflowStateRequest(req WorkflowStateRequest) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return &WorkflowStateValidationError{Field: "name", Message: "name must not be empty"}
	}
	if len([]byte(name)) > maxWorkflowStateNameBytes {
		return &WorkflowStateValidationError{Field: "name", Message: "name exceeds maximum byte length"}
	}
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return &WorkflowStateValidationError{Field: "name", Message: "name contains invalid characters"}
	}

	if len([]byte(req.Description)) > maxWorkflowStateDescriptionBytes {
		return &WorkflowStateValidationError{Field: "description", Message: "description exceeds maximum byte length"}
	}
	if !utf8.ValidString(req.Description) || strings.ContainsRune(req.Description, 0) {
		return &WorkflowStateValidationError{Field: "description", Message: "description contains invalid characters"}
	}

	if len([]byte(req.EntryText)) > maxWorkflowStateEntryTextBytes {
		return &WorkflowStateValidationError{Field: "entry_text", Message: "entry text exceeds maximum byte length"}
	}
	if !utf8.ValidString(req.EntryText) || strings.ContainsRune(req.EntryText, 0) {
		return &WorkflowStateValidationError{Field: "entry_text", Message: "entry text contains invalid characters"}
	}

	if req.ID != nil {
		id := strings.TrimSpace(*req.ID)
		if !workflowStateIDPattern.MatchString(id) {
			return &WorkflowStateValidationError{Field: "id", Message: "id must be a valid UUIDv4"}
		}
	}

	for i, rem := range req.Reminders {
		remID := strings.TrimSpace(rem.ID)
		if remID == "" {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("reminders[%d].id", i), Message: "id must be a non-empty string"}
		}
		if rem.Type != WorkflowReminderOneShot && rem.Type != WorkflowReminderRecurring {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("reminders[%d].type", i), Message: "type must be 'one_shot' or 'recurring'"}
		}
		if rem.Body == "" {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("reminders[%d].body", i), Message: "body must not be empty"}
		}
		if len([]byte(rem.Body)) > maxWorkflowReminderBodyBytes {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("reminders[%d].body", i), Message: "body exceeds maximum byte length"}
		}
		if rem.DelaySeconds < 1 || rem.DelaySeconds > maxWorkflowReminderDelaySeconds {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("reminders[%d].delay_seconds", i), Message: "delay_seconds must be between 1 and 2592000"}
		}
		if rem.Type == WorkflowReminderRecurring {
			if rem.IntervalSeconds == nil || *rem.IntervalSeconds < 1 {
				return &WorkflowStateValidationError{Field: fmt.Sprintf("reminders[%d].interval_seconds", i), Message: "interval_seconds must be >= 1 for recurring reminders"}
			}
		}
	}

	if _, err := sanitizeWorkflowAllowedTools(req.AllowedTools); err != nil {
		return err
	}

	selfID := ""
	if req.ID != nil {
		selfID = strings.TrimSpace(*req.ID)
	}

	seenTargets := make(map[string]bool, len(req.Transitions))
	for i, tr := range req.Transitions {
		targetID := strings.TrimSpace(tr.TargetStateID)
		if targetID == "" {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("transitions[%d].target_state_id", i), Message: "target_state_id must not be empty"}
		}
		if !workflowStateIDPattern.MatchString(targetID) {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("transitions[%d].target_state_id", i), Message: "target_state_id must be a valid UUIDv4"}
		}
		if selfID != "" && targetID == selfID {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("transitions[%d].target_state_id", i), Message: "self-transition is not permitted"}
		}
		if seenTargets[targetID] {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("transitions[%d].target_state_id", i), Message: fmt.Sprintf("duplicate transition target %q", targetID)}
		}
		seenTargets[targetID] = true

		desc := strings.TrimSpace(tr.Description)
		if desc == "" {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("transitions[%d].description", i), Message: "description must not be empty"}
		}
		if len([]byte(desc)) > maxWorkflowTransitionDescriptionBytes {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("transitions[%d].description", i), Message: "description exceeds maximum byte length"}
		}
		if !utf8.ValidString(desc) || strings.ContainsRune(desc, 0) {
			return &WorkflowStateValidationError{Field: fmt.Sprintf("transitions[%d].description", i), Message: "description contains invalid characters"}
		}
	}

	return nil
}

func (s *Store) ListWorkflowStates(ctx context.Context) ([]WorkflowState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, entry_text, reminders_json, allowed_tools_json, transitions_json, allow_exit, created_at, updated_at FROM workflow_states ORDER BY name ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []WorkflowState
	for rows.Next() {
		var state WorkflowState
		var remindersJSON sql.NullString
		var allowedToolsJSON sql.NullString
		var transitionsJSON sql.NullString
		var allowExitInt int
		if err := rows.Scan(&state.ID, &state.Name, &state.Description, &state.EntryText, &remindersJSON, &allowedToolsJSON, &transitionsJSON, &allowExitInt, &state.CreatedAt, &state.UpdatedAt); err != nil {
			return nil, err
		}
		state.Reminders = unmarshalWorkflowReminders(remindersJSON.String)
		state.AllowedTools = unmarshalWorkflowAllowedTools(allowedToolsJSON.String)
		state.Transitions = unmarshalWorkflowTransitions(transitionsJSON.String)
		state.AllowExit = (allowExitInt == 1)
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if states == nil {
		states = []WorkflowState{}
	}
	return states, nil
}

// GetManagedAgentActiveWorkflowState returns the active workflow state for an agent, or nil if none is set.
func (s *Store) GetManagedAgentActiveWorkflowState(ctx context.Context, agentID string) (*WorkflowState, error) {
	agent, err := s.ManagedAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil || agent.ActiveWorkflowStateID == "" {
		return nil, nil
	}
	return s.GetWorkflowState(ctx, agent.ActiveWorkflowStateID)
}

func (s *Store) GetWorkflowState(ctx context.Context, id string) (*WorkflowState, error) {
	var state WorkflowState
	var remindersJSON sql.NullString
	var allowedToolsJSON sql.NullString
	var transitionsJSON sql.NullString
	var allowExitInt int
	err := s.db.QueryRowContext(ctx, `SELECT id, name, description, entry_text, reminders_json, allowed_tools_json, transitions_json, allow_exit, created_at, updated_at FROM workflow_states WHERE id = ?`, id).
		Scan(&state.ID, &state.Name, &state.Description, &state.EntryText, &remindersJSON, &allowedToolsJSON, &transitionsJSON, &allowExitInt, &state.CreatedAt, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state.Reminders = unmarshalWorkflowReminders(remindersJSON.String)
	state.AllowedTools = unmarshalWorkflowAllowedTools(allowedToolsJSON.String)
	state.Transitions = unmarshalWorkflowTransitions(transitionsJSON.String)
	state.AllowExit = (allowExitInt == 1)
	return &state, nil
}

func marshalWorkflowTransitions(transitions []WorkflowTransitionConfig) (string, error) {
	if transitions == nil {
		return "[]", nil
	}
	data, err := json.Marshal(transitions)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalWorkflowTransitions(raw string) []WorkflowTransitionConfig {
	if strings.TrimSpace(raw) == "" {
		return []WorkflowTransitionConfig{}
	}
	var transitions []WorkflowTransitionConfig
	if err := json.Unmarshal([]byte(raw), &transitions); err != nil || transitions == nil {
		return []WorkflowTransitionConfig{}
	}
	return transitions
}

func marshalWorkflowAllowedTools(tools []string) (string, error) {
	if tools == nil {
		return "[]", nil
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalWorkflowAllowedTools(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var tools []string
	if err := json.Unmarshal([]byte(raw), &tools); err != nil || tools == nil {
		return []string{}
	}
	return tools
}

func marshalWorkflowReminders(reminders []WorkflowReminderConfig) (string, error) {
	if reminders == nil {
		return "[]", nil
	}
	data, err := json.Marshal(reminders)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalWorkflowReminders(raw string) []WorkflowReminderConfig {
	if strings.TrimSpace(raw) == "" {
		return []WorkflowReminderConfig{}
	}
	var reminders []WorkflowReminderConfig
	if err := json.Unmarshal([]byte(raw), &reminders); err != nil || reminders == nil {
		return []WorkflowReminderConfig{}
	}
	return reminders
}

func (s *Store) CreateWorkflowState(ctx context.Context, req WorkflowStateRequest) (*WorkflowState, error) {
	if err := validateWorkflowStateRequest(req); err != nil {
		return nil, err
	}

	var state *WorkflowState
	err := s.runImmediate(ctx, func() error {
		name := strings.TrimSpace(req.Name)
		var existingID string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM workflow_states WHERE name = ?`, name).Scan(&existingID)
		if err == nil {
			return &WorkflowStateNameConflictError{Name: name}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var id string
		if req.ID != nil && strings.TrimSpace(*req.ID) != "" {
			id = strings.TrimSpace(*req.ID)
		} else {
			generated, err := s.ids.Next()
			if err != nil {
				return err
			}
			id = generated
		}

		now := formatTimestamp(s.clock.Now())
		desc := strings.TrimSpace(req.Description)
		entryText := req.EntryText

		remindersJSON, err := marshalWorkflowReminders(req.Reminders)
		if err != nil {
			return err
		}

		sanitizedTools, err := sanitizeWorkflowAllowedTools(req.AllowedTools)
		if err != nil {
			return err
		}
		allowedToolsJSON, err := marshalWorkflowAllowedTools(sanitizedTools)
		if err != nil {
			return err
		}

		transitionsJSON, err := marshalWorkflowTransitions(req.Transitions)
		if err != nil {
			return err
		}

		// Verify that all transition target states exist
		for _, tr := range req.Transitions {
			var targetExists int
			err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_states WHERE id = ?`, tr.TargetStateID).Scan(&targetExists)
			if errors.Is(err, sql.ErrNoRows) {
				return &WorkflowStateValidationError{
					Field:   "transitions",
					Message: fmt.Sprintf("target state %q does not exist", tr.TargetStateID),
				}
			}
			if err != nil {
				return err
			}
		}

		allowExitInt := 1
		if req.AllowExit != nil && !*req.AllowExit {
			allowExitInt = 0
		}

		_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_states (id, name, description, entry_text, reminders_json, allowed_tools_json, transitions_json, allow_exit, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, name, desc, entryText, remindersJSON, allowedToolsJSON, transitionsJSON, allowExitInt, now, now)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowStateNameConflictError{Name: name}
			}
			return err
		}

		remindersCopy := req.Reminders
		if remindersCopy == nil {
			remindersCopy = []WorkflowReminderConfig{}
		}
		transitionsCopy := req.Transitions
		if transitionsCopy == nil {
			transitionsCopy = []WorkflowTransitionConfig{}
		}

		state = &WorkflowState{
			ID:           id,
			Name:         name,
			Description:  desc,
			EntryText:    entryText,
			Reminders:    remindersCopy,
			AllowedTools: sanitizedTools,
			Transitions:  transitionsCopy,
			AllowExit:    (allowExitInt == 1),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		return nil
	})

	return state, err
}

func (s *Store) UpdateWorkflowState(ctx context.Context, id string, req WorkflowStateRequest) (*WorkflowState, error) {
	if err := validateWorkflowStateRequest(req); err != nil {
		return nil, err
	}

	var state *WorkflowState
	err := s.runImmediate(ctx, func() error {
		var existing WorkflowState
		var existingReminders sql.NullString
		var existingAllowedTools sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT id, name, description, entry_text, reminders_json, allowed_tools_json, created_at, updated_at FROM workflow_states WHERE id = ?`, id).
			Scan(&existing.ID, &existing.Name, &existing.Description, &existing.EntryText, &existingReminders, &existingAllowedTools, &existing.CreatedAt, &existing.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return &WorkflowStateNotFoundError{ID: id}
		}
		if err != nil {
			return err
		}

		name := strings.TrimSpace(req.Name)
		var conflictID string
		err = s.db.QueryRowContext(ctx, `SELECT id FROM workflow_states WHERE name = ? AND id != ?`, name, id).Scan(&conflictID)
		if err == nil {
			return &WorkflowStateNameConflictError{Name: name}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		desc := strings.TrimSpace(req.Description)
		entryText := req.EntryText

		remindersJSON, err := marshalWorkflowReminders(req.Reminders)
		if err != nil {
			return err
		}

		sanitizedTools, err := sanitizeWorkflowAllowedTools(req.AllowedTools)
		if err != nil {
			return err
		}
		allowedToolsJSON, err := marshalWorkflowAllowedTools(sanitizedTools)
		if err != nil {
			return err
		}

		transitionsJSON, err := marshalWorkflowTransitions(req.Transitions)
		if err != nil {
			return err
		}

		// Verify that all transition target states exist and none point to self
		for _, tr := range req.Transitions {
			if tr.TargetStateID == id {
				return &WorkflowStateValidationError{
					Field:   "transitions",
					Message: "self-transition is not permitted",
				}
			}
			var targetExists int
			err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_states WHERE id = ?`, tr.TargetStateID).Scan(&targetExists)
			if errors.Is(err, sql.ErrNoRows) {
				return &WorkflowStateValidationError{
					Field:   "transitions",
					Message: fmt.Sprintf("target state %q does not exist", tr.TargetStateID),
				}
			}
			if err != nil {
				return err
			}
		}

		allowExitInt := 1
		if req.AllowExit != nil {
			if !*req.AllowExit {
				allowExitInt = 0
			}
		}

		_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET name = ?, description = ?, entry_text = ?, reminders_json = ?, allowed_tools_json = ?, transitions_json = ?, allow_exit = ?, updated_at = ? WHERE id = ?`,
			name, desc, entryText, remindersJSON, allowedToolsJSON, transitionsJSON, allowExitInt, now, id)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowStateNameConflictError{Name: name}
			}
			return err
		}

		remindersCopy := req.Reminders
		if remindersCopy == nil {
			remindersCopy = []WorkflowReminderConfig{}
		}
		transitionsCopy := req.Transitions
		if transitionsCopy == nil {
			transitionsCopy = []WorkflowTransitionConfig{}
		}

		state = &WorkflowState{
			ID:           id,
			Name:         name,
			Description:  desc,
			EntryText:    entryText,
			Reminders:    remindersCopy,
			AllowedTools: sanitizedTools,
			Transitions:  transitionsCopy,
			AllowExit:    (allowExitInt == 1),
			CreatedAt:    existing.CreatedAt,
			UpdatedAt:    now,
		}
		return nil
	})

	return state, err
}

func (s *Store) DeleteWorkflowState(ctx context.Context, id string) (bool, error) {
	var deleted bool
	err := s.runImmediate(ctx, func() error {
		// Check if any workflow state references this ID in transitions_json
		rows, err := s.db.QueryContext(ctx, `SELECT id, name, transitions_json FROM workflow_states WHERE id != ?`, id)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var otherID, otherName string
			var transitionsJSON sql.NullString
			if err := rows.Scan(&otherID, &otherName, &transitionsJSON); err != nil {
				return err
			}
			transitions := unmarshalWorkflowTransitions(transitionsJSON.String)
			for _, tr := range transitions {
				if tr.TargetStateID == id {
					return &WorkflowStateInUseError{
						ID:            id,
						ReferrerState: otherName,
					}
				}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}

		res, err := s.db.ExecContext(ctx, `DELETE FROM workflow_states WHERE id = ?`, id)
		if err != nil {
			return err
		}
		rowsAff, err := res.RowsAffected()
		if err != nil {
			return err
		}
		deleted = rowsAff > 0
		return nil
	})
	return deleted, err
}

func truncateSafe(s string, maxLen int) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			if b.Len() >= maxLen {
				break
			}
		}
	}
	res := b.String()
	if res == "" {
		return "x"
	}
	return res
}

func (s *Store) CleanUpManagedAgentWorkflowReminders(ctx context.Context, agentID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT reminder_id FROM managed_agent_workflow_reminders WHERE agent_id = ?`, agentID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var reminderIDs []string
	for rows.Next() {
		var remID string
		if err := rows.Scan(&remID); err != nil {
			return err
		}
		reminderIDs = append(reminderIDs, remID)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, remID := range reminderIDs {
		_, _ = s.CancelReminder(ctx, remID)
	}

	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `DELETE FROM managed_agent_workflow_reminders WHERE agent_id = ?`, agentID)
		return err
	})
}

func (s *Store) SpawnWorkflowStateReminders(ctx context.Context, agentID string, state *WorkflowState) error {
	if state == nil || len(state.Reminders) == 0 {
		return nil
	}

	nowTime := s.clock.Now()
	now := formatTimestamp(nowTime)

	for _, cfg := range state.Reminders {
		remID := fmt.Sprintf("wfr-%s-%s-%d", truncateSafe(agentID, 12), truncateSafe(cfg.ID, 8), nowTime.UnixNano()%1000000)
		if len(remID) > 99 {
			remID = remID[:99]
		}
		dueAt := formatTimestamp(nowTime.Add(time.Duration(cfg.DelaySeconds) * time.Second))
		var intervalMS *int64
		if cfg.Type == WorkflowReminderRecurring && cfg.IntervalSeconds != nil {
			ms := *cfg.IntervalSeconds * 1000
			intervalMS = &ms
		}

		created, _, err := s.CreateReminder(ctx, ReminderRequest{
			ID:         remID,
			Target:     agentID,
			Creator:    "system",
			Subject:    cfg.Subject,
			Body:       cfg.Body,
			CreatedAt:  now,
			NextDueAt:  dueAt,
			IntervalMS: intervalMS,
		})
		if err != nil {
			return err
		}
		if created {
			err = s.runImmediate(ctx, func() error {
				_, insertErr := s.db.ExecContext(ctx, `INSERT INTO managed_agent_workflow_reminders (agent_id, workflow_state_id, reminder_id, created_at) VALUES (?, ?, ?, ?)`,
					agentID, state.ID, remID, now)
				return insertErr
			})
			if err != nil {
				return err
			}
		}
	}

	return nil
}
