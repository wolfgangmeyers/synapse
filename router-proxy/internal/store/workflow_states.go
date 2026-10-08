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
)

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
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	EntryText   string                   `json:"entry_text"`
	Reminders   []WorkflowReminderConfig `json:"reminders"`
	CreatedAt   string                   `json:"created_at"`
	UpdatedAt   string                   `json:"updated_at"`
}

type WorkflowStateRequest struct {
	ID          *string                  `json:"id,omitempty"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	EntryText   string                   `json:"entry_text"`
	Reminders   []WorkflowReminderConfig `json:"reminders"`
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

	return nil
}

func (s *Store) ListWorkflowStates(ctx context.Context) ([]WorkflowState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, entry_text, reminders_json, created_at, updated_at FROM workflow_states ORDER BY name ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []WorkflowState
	for rows.Next() {
		var state WorkflowState
		var remindersJSON sql.NullString
		if err := rows.Scan(&state.ID, &state.Name, &state.Description, &state.EntryText, &remindersJSON, &state.CreatedAt, &state.UpdatedAt); err != nil {
			return nil, err
		}
		state.Reminders = unmarshalWorkflowReminders(remindersJSON.String)
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

func (s *Store) GetWorkflowState(ctx context.Context, id string) (*WorkflowState, error) {
	var state WorkflowState
	var remindersJSON sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, name, description, entry_text, reminders_json, created_at, updated_at FROM workflow_states WHERE id = ?`, id).
		Scan(&state.ID, &state.Name, &state.Description, &state.EntryText, &remindersJSON, &state.CreatedAt, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state.Reminders = unmarshalWorkflowReminders(remindersJSON.String)
	return &state, nil
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

		_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_states (id, name, description, entry_text, reminders_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, name, desc, entryText, remindersJSON, now, now)
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

		state = &WorkflowState{
			ID:          id,
			Name:        name,
			Description: desc,
			EntryText:   entryText,
			Reminders:   remindersCopy,
			CreatedAt:   now,
			UpdatedAt:   now,
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
		err := s.db.QueryRowContext(ctx, `SELECT id, name, description, entry_text, reminders_json, created_at, updated_at FROM workflow_states WHERE id = ?`, id).
			Scan(&existing.ID, &existing.Name, &existing.Description, &existing.EntryText, &existingReminders, &existing.CreatedAt, &existing.UpdatedAt)
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

		_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET name = ?, description = ?, entry_text = ?, reminders_json = ?, updated_at = ? WHERE id = ?`,
			name, desc, entryText, remindersJSON, now, id)
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

		state = &WorkflowState{
			ID:          id,
			Name:        name,
			Description: desc,
			EntryText:   entryText,
			Reminders:   remindersCopy,
			CreatedAt:   existing.CreatedAt,
			UpdatedAt:   now,
		}
		return nil
	})

	return state, err
}

func (s *Store) DeleteWorkflowState(ctx context.Context, id string) (bool, error) {
	var deleted bool
	err := s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `DELETE FROM workflow_states WHERE id = ?`, id)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		deleted = rows > 0
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
