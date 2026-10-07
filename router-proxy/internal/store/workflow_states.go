package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	workflowStateIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

const (
	maxWorkflowStateNameBytes        = 256
	maxWorkflowStateDescriptionBytes = 4096
	maxWorkflowStateEntryTextBytes   = 65536
)

type WorkflowState struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	EntryText   string `json:"entry_text"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type WorkflowStateRequest struct {
	ID          *string `json:"id,omitempty"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	EntryText   string  `json:"entry_text"`
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
	return nil
}

func (s *Store) ListWorkflowStates(ctx context.Context) ([]WorkflowState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, entry_text, created_at, updated_at FROM workflow_states ORDER BY name ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []WorkflowState
	for rows.Next() {
		var state WorkflowState
		if err := rows.Scan(&state.ID, &state.Name, &state.Description, &state.EntryText, &state.CreatedAt, &state.UpdatedAt); err != nil {
			return nil, err
		}
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
	err := s.db.QueryRowContext(ctx, `SELECT id, name, description, entry_text, created_at, updated_at FROM workflow_states WHERE id = ?`, id).
		Scan(&state.ID, &state.Name, &state.Description, &state.EntryText, &state.CreatedAt, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
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

		_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_states (id, name, description, entry_text, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, name, desc, entryText, now, now)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowStateNameConflictError{Name: name}
			}
			return err
		}

		state = &WorkflowState{
			ID:          id,
			Name:        name,
			Description: desc,
			EntryText:   entryText,
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
		err := s.db.QueryRowContext(ctx, `SELECT id, name, description, entry_text, created_at, updated_at FROM workflow_states WHERE id = ?`, id).
			Scan(&existing.ID, &existing.Name, &existing.Description, &existing.EntryText, &existing.CreatedAt, &existing.UpdatedAt)
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

		_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET name = ?, description = ?, entry_text = ?, updated_at = ? WHERE id = ?`,
			name, desc, entryText, now, id)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowStateNameConflictError{Name: name}
			}
			return err
		}

		state = &WorkflowState{
			ID:          id,
			Name:        name,
			Description: desc,
			EntryText:   entryText,
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
