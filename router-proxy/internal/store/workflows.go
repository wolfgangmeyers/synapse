package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxWorkflowNameBytes        = 256
	maxWorkflowDescriptionBytes = 4096
)

type Workflow struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	EntryStateID *string `json:"entry_state_id,omitempty"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	StateCount   int     `json:"state_count"`
}

type WorkflowRequest struct {
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	EntryStateID *string `json:"entry_state_id,omitempty"`
}

type WorkflowStatePosition struct {
	StateID string  `json:"state_id"`
	UIPosX  float64 `json:"ui_pos_x"`
	UIPosY  float64 `json:"ui_pos_y"`
}

type WorkflowValidationError struct {
	Field   string
	Message string
}

func (e *WorkflowValidationError) Error() string {
	return fmt.Sprintf("invalid workflow (%s): %s", e.Field, e.Message)
}
func (e *WorkflowValidationError) Code() string { return "invalid_workflow" }

type WorkflowNameConflictError struct {
	Name string
}

func (e *WorkflowNameConflictError) Error() string {
	return fmt.Sprintf("workflow name %q already exists", e.Name)
}
func (e *WorkflowNameConflictError) Code() string { return "workflow_name_conflict" }

type WorkflowNotFoundError struct {
	ID string
}

func (e *WorkflowNotFoundError) Error() string {
	return fmt.Sprintf("workflow %q not found", e.ID)
}
func (e *WorkflowNotFoundError) Code() string { return "workflow_not_found" }

func validateWorkflowRequest(req WorkflowRequest) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return &WorkflowValidationError{Field: "name", Message: "name must not be empty"}
	}
	if len([]byte(name)) > maxWorkflowNameBytes {
		return &WorkflowValidationError{Field: "name", Message: "name exceeds maximum byte length"}
	}
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return &WorkflowValidationError{Field: "name", Message: "name contains invalid characters"}
	}

	if len([]byte(req.Description)) > maxWorkflowDescriptionBytes {
		return &WorkflowValidationError{Field: "description", Message: "description exceeds maximum byte length"}
	}
	if !utf8.ValidString(req.Description) || strings.ContainsRune(req.Description, 0) {
		return &WorkflowValidationError{Field: "description", Message: "description contains invalid characters"}
	}

	if req.EntryStateID != nil && strings.TrimSpace(*req.EntryStateID) != "" {
		entryID := strings.TrimSpace(*req.EntryStateID)
		if !workflowStateIDPattern.MatchString(entryID) {
			return &WorkflowValidationError{Field: "entry_state_id", Message: "entry_state_id must be a valid UUIDv4"}
		}
	}

	return nil
}

func (s *Store) CreateWorkflow(ctx context.Context, req WorkflowRequest) (*Workflow, error) {
	if err := validateWorkflowRequest(req); err != nil {
		return nil, err
	}

	var wf *Workflow
	err := s.runImmediate(ctx, func() error {
		name := strings.TrimSpace(req.Name)
		var existingID string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM workflows WHERE name = ?`, name).Scan(&existingID)
		if err == nil {
			return &WorkflowNameConflictError{Name: name}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		id, err := s.ids.Next()
		if err != nil {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		desc := strings.TrimSpace(req.Description)

		var entryStateIDVal *string
		if req.EntryStateID != nil && strings.TrimSpace(*req.EntryStateID) != "" {
			trimmed := strings.TrimSpace(*req.EntryStateID)
			var exists int
			err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_states WHERE id = ?`, trimmed).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				return &WorkflowValidationError{
					Field:   "entry_state_id",
					Message: fmt.Sprintf("entry state %q does not exist", trimmed),
				}
			}
			if err != nil {
				return err
			}
			entryStateIDVal = &trimmed
		}

		_, err = s.db.ExecContext(ctx, `INSERT INTO workflows (id, name, description, entry_state_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, name, desc, entryStateIDVal, now, now)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowNameConflictError{Name: name}
			}
			return err
		}

		wf = &Workflow{
			ID:           id,
			Name:         name,
			Description:  desc,
			EntryStateID: entryStateIDVal,
			CreatedAt:    now,
			UpdatedAt:    now,
			StateCount:   0,
		}
		return nil
	})

	return wf, err
}

func (s *Store) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	var wf Workflow
	var entryStateID sql.NullString
	query := `
		SELECT w.id, w.name, w.description, w.entry_state_id, w.created_at, w.updated_at,
		       (SELECT COUNT(*) FROM workflow_states ws WHERE ws.workflow_id = w.id) AS state_count
		FROM workflows w
		WHERE w.id = ?`
	err := s.db.QueryRowContext(ctx, query, id).
		Scan(&wf.ID, &wf.Name, &wf.Description, &entryStateID, &wf.CreatedAt, &wf.UpdatedAt, &wf.StateCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if entryStateID.Valid {
		wf.EntryStateID = &entryStateID.String
	}
	return &wf, nil
}

func (s *Store) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	query := `
		SELECT w.id, w.name, w.description, w.entry_state_id, w.created_at, w.updated_at,
		       (SELECT COUNT(*) FROM workflow_states ws WHERE ws.workflow_id = w.id) AS state_count
		FROM workflows w
		ORDER BY w.name ASC, w.id ASC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workflows []Workflow
	for rows.Next() {
		var wf Workflow
		var entryStateID sql.NullString
		if err := rows.Scan(&wf.ID, &wf.Name, &wf.Description, &entryStateID, &wf.CreatedAt, &wf.UpdatedAt, &wf.StateCount); err != nil {
			return nil, err
		}
		if entryStateID.Valid {
			wf.EntryStateID = &entryStateID.String
		}
		workflows = append(workflows, wf)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if workflows == nil {
		workflows = []Workflow{}
	}
	return workflows, nil
}

func (s *Store) UpdateWorkflow(ctx context.Context, id string, req WorkflowRequest) (*Workflow, error) {
	if err := validateWorkflowRequest(req); err != nil {
		return nil, err
	}

	var wf *Workflow
	err := s.runImmediate(ctx, func() error {
		var existing Workflow
		var existingEntryStateID sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT id, name, description, entry_state_id, created_at, updated_at FROM workflows WHERE id = ?`, id).
			Scan(&existing.ID, &existing.Name, &existing.Description, &existingEntryStateID, &existing.CreatedAt, &existing.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return &WorkflowNotFoundError{ID: id}
		}
		if err != nil {
			return err
		}

		name := strings.TrimSpace(req.Name)
		var conflictID string
		err = s.db.QueryRowContext(ctx, `SELECT id FROM workflows WHERE name = ? AND id != ?`, name, id).Scan(&conflictID)
		if err == nil {
			return &WorkflowNameConflictError{Name: name}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		desc := strings.TrimSpace(req.Description)

		var entryStateIDVal *string
		if req.EntryStateID != nil {
			if strings.TrimSpace(*req.EntryStateID) != "" {
				trimmed := strings.TrimSpace(*req.EntryStateID)
				var exists int
				err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_states WHERE id = ?`, trimmed).Scan(&exists)
				if errors.Is(err, sql.ErrNoRows) {
					return &WorkflowValidationError{
						Field:   "entry_state_id",
						Message: fmt.Sprintf("entry state %q does not exist", trimmed),
					}
				}
				if err != nil {
					return err
				}
				entryStateIDVal = &trimmed
			} else {
				entryStateIDVal = nil
			}
		} else if existingEntryStateID.Valid {
			entryStateIDVal = &existingEntryStateID.String
		}

		_, err = s.db.ExecContext(ctx, `UPDATE workflows SET name = ?, description = ?, entry_state_id = ?, updated_at = ? WHERE id = ?`,
			name, desc, entryStateIDVal, now, id)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowNameConflictError{Name: name}
			}
			return err
		}

		var stateCount int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_states WHERE workflow_id = ?`, id).Scan(&stateCount)

		wf = &Workflow{
			ID:           id,
			Name:         name,
			Description:  desc,
			EntryStateID: entryStateIDVal,
			CreatedAt:    existing.CreatedAt,
			UpdatedAt:    now,
			StateCount:   stateCount,
		}
		return nil
	})

	return wf, err
}

func (s *Store) DeleteWorkflow(ctx context.Context, id string) (bool, error) {
	var deleted bool
	err := s.runImmediate(ctx, func() error {
		// Explicitly delete child states to be certain child states are cleaned up regardless of foreign key pragmas
		_, err := s.db.ExecContext(ctx, `DELETE FROM workflow_states WHERE workflow_id = ?`, id)
		if err != nil {
			return err
		}

		res, err := s.db.ExecContext(ctx, `DELETE FROM workflows WHERE id = ?`, id)
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

func (s *Store) ListWorkflowStatesByWorkflow(ctx context.Context, workflowID string) ([]WorkflowState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workflow_id, name, description, entry_text, reminders_json, allowed_tools_json, transitions_json, allow_exit, ui_pos_x, ui_pos_y, created_at, updated_at FROM workflow_states WHERE workflow_id = ? ORDER BY name ASC, id ASC`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []WorkflowState
	for rows.Next() {
		var state WorkflowState
		var wfID sql.NullString
		var remindersJSON sql.NullString
		var allowedToolsJSON sql.NullString
		var transitionsJSON sql.NullString
		var allowExitInt int
		if err := rows.Scan(&state.ID, &wfID, &state.Name, &state.Description, &state.EntryText, &remindersJSON, &allowedToolsJSON, &transitionsJSON, &allowExitInt, &state.UIPosX, &state.UIPosY, &state.CreatedAt, &state.UpdatedAt); err != nil {
			return nil, err
		}
		if wfID.Valid {
			state.WorkflowID = &wfID.String
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

func (s *Store) UpdateWorkflowStatePositions(ctx context.Context, workflowID string, positions []WorkflowStatePosition) error {
	return s.runImmediate(ctx, func() error {
		// Verify workflow exists
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workflows WHERE id = ?`, workflowID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return &WorkflowNotFoundError{ID: workflowID}
		}
		if err != nil {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		stmt, err := s.db.PrepareContext(ctx, `UPDATE workflow_states SET ui_pos_x = ?, ui_pos_y = ?, updated_at = ? WHERE id = ? AND workflow_id = ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, pos := range positions {
			res, err := stmt.ExecContext(ctx, pos.UIPosX, pos.UIPosY, now, pos.StateID, workflowID)
			if err != nil {
				return err
			}
			aff, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if aff == 0 {
				return &WorkflowStateNotFoundError{ID: pos.StateID}
			}
		}
		return nil
	})
}

func (s *Store) DuplicateWorkflow(ctx context.Context, id string, newName string) (*Workflow, error) {
	trimmedName := strings.TrimSpace(newName)
	if trimmedName == "" {
		return nil, &WorkflowValidationError{Field: "name", Message: "name must not be empty"}
	}
	if len([]byte(trimmedName)) > maxWorkflowNameBytes {
		return nil, &WorkflowValidationError{Field: "name", Message: "name exceeds maximum byte length"}
	}
	if !utf8.ValidString(trimmedName) || strings.ContainsRune(trimmedName, 0) {
		return nil, &WorkflowValidationError{Field: "name", Message: "name contains invalid characters"}
	}

	var duplicated *Workflow
	err := s.runImmediate(ctx, func() error {
		// 1. Fetch original workflow
		var original Workflow
		var originalEntryStateID sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT id, name, description, entry_state_id, created_at, updated_at FROM workflows WHERE id = ?`, id).
			Scan(&original.ID, &original.Name, &original.Description, &originalEntryStateID, &original.CreatedAt, &original.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return &WorkflowNotFoundError{ID: id}
		}
		if err != nil {
			return err
		}

		// 2. Check name collision for new workflow
		var conflictID string
		err = s.db.QueryRowContext(ctx, `SELECT id FROM workflows WHERE name = ?`, trimmedName).Scan(&conflictID)
		if err == nil {
			return &WorkflowNameConflictError{Name: trimmedName}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		// 3. Generate new workflow ID
		newWfID, err := s.ids.Next()
		if err != nil {
			return err
		}

		now := formatTimestamp(s.clock.Now())

		// 4. Fetch all original child states
		states, err := s.ListWorkflowStatesByWorkflow(ctx, id)
		if err != nil {
			return err
		}

		// 5. Map old state UUID to new state UUID and unique name
		oldToNewID := make(map[string]string, len(states))
		for _, st := range states {
			newID, err := s.ids.Next()
			if err != nil {
				return err
			}
			oldToNewID[st.ID] = newID
		}

		// 6. Determine copied entry_state_id
		var newEntryStateID *string
		if originalEntryStateID.Valid {
			if mapped, ok := oldToNewID[originalEntryStateID.String]; ok {
				newEntryStateID = &mapped
			}
		}

		// 7. Insert new workflow first with NULL entry_state_id so workflow_states foreign key (workflow_id REFERENCES workflows(id)) is satisfied!
		_, err = s.db.ExecContext(ctx, `INSERT INTO workflows (id, name, description, entry_state_id, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?)`,
			newWfID, trimmedName, original.Description, now, now)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowNameConflictError{Name: trimmedName}
			}
			return err
		}

		// Check name conflicts for new states and make unique names if needed, or keep uniqueness scoped
		// Note: workflow_states table has a UNIQUE constraint on `name`.
		// Let's generate state names: e.g., fmt.Sprintf("%s (copy)", st.Name) or suffix with a unique string if needed.
		// If st.Name exists, find a unique name.
		for _, st := range states {
			newStateID := oldToNewID[st.ID]
			candidateName := st.Name
			var nameExists int
			err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_states WHERE name = ?`, candidateName).Scan(&nameExists)
			if err == nil {
				// Name exists, find a candidate
				candidateName = fmt.Sprintf("%s-copy-%s", st.Name, newStateID[:8])
			}

			// Remap transitions
			newTransitions := make([]WorkflowTransitionConfig, 0, len(st.Transitions))
			for _, tr := range st.Transitions {
				mappedTarget, ok := oldToNewID[tr.TargetStateID]
				if ok {
					newTransitions = append(newTransitions, WorkflowTransitionConfig{
						TargetStateID: mappedTarget,
						Description:   tr.Description,
					})
				} else {
					// If the target state wasn't in this workflow, keep original target
					newTransitions = append(newTransitions, tr)
				}
			}

			remindersJSON, err := marshalWorkflowReminders(st.Reminders)
			if err != nil {
				return err
			}
			allowedToolsJSON, err := marshalWorkflowAllowedTools(st.AllowedTools)
			if err != nil {
				return err
			}
			transitionsJSON, err := marshalWorkflowTransitions(newTransitions)
			if err != nil {
				return err
			}

			allowExitInt := 1
			if !st.AllowExit {
				allowExitInt = 0
			}

			uiPosX := st.UIPosX + 20
			uiPosY := st.UIPosY + 20

			_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_states (id, workflow_id, name, description, entry_text, reminders_json, allowed_tools_json, transitions_json, allow_exit, ui_pos_x, ui_pos_y, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				newStateID, newWfID, candidateName, st.Description, st.EntryText, remindersJSON, allowedToolsJSON, transitionsJSON, allowExitInt, uiPosX, uiPosY, now, now)
			if err != nil {
				return err
			}
		}

		// 8. Update workflow with copied entry_state_id if set
		if newEntryStateID != nil {
			_, err = s.db.ExecContext(ctx, `UPDATE workflows SET entry_state_id = ? WHERE id = ?`, *newEntryStateID, newWfID)
			if err != nil {
				return err
			}
		}

		duplicated = &Workflow{
			ID:           newWfID,
			Name:         trimmedName,
			Description:  original.Description,
			EntryStateID: newEntryStateID,
			CreatedAt:    now,
			UpdatedAt:    now,
			StateCount:   len(states),
		}
		return nil
	})

	return duplicated, err
}
