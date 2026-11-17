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
	maxWorkflowCollectionNameBytes        = 128
	maxWorkflowCollectionDescriptionBytes = 4096
	DefaultWorkflowCollectionID           = "default"
)

// WorkflowCollection represents a group of workflows.
type WorkflowCollection struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	WorkflowCount int    `json:"workflow_count"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// WorkflowCollectionRequest models create and update requests for collections.
type WorkflowCollectionRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// WorkflowCollectionValidationError represents validation failure on collection fields.
type WorkflowCollectionValidationError struct {
	Field   string
	Message string
}

func (e *WorkflowCollectionValidationError) Error() string {
	return fmt.Sprintf("invalid workflow collection (%s): %s", e.Field, e.Message)
}
func (e *WorkflowCollectionValidationError) Code() string { return "invalid_workflow_collection" }

// WorkflowCollectionNameConflictError represents name collision on collection names.
type WorkflowCollectionNameConflictError struct {
	Name string
}

func (e *WorkflowCollectionNameConflictError) Error() string {
	return fmt.Sprintf("workflow collection name %q already exists", e.Name)
}
func (e *WorkflowCollectionNameConflictError) Code() string { return "workflow_collection_name_conflict" }

// WorkflowCollectionNotFoundError indicates the requested collection does not exist.
type WorkflowCollectionNotFoundError struct {
	ID string
}

func (e *WorkflowCollectionNotFoundError) Error() string {
	return fmt.Sprintf("workflow collection %q not found", e.ID)
}
func (e *WorkflowCollectionNotFoundError) Code() string { return "workflow_collection_not_found" }

// CannotModifyDefaultCollectionError indicates an attempt to modify the default collection.
// The default collection is immutable; its name and properties cannot be modified.
type CannotModifyDefaultCollectionError struct{}

func (e *CannotModifyDefaultCollectionError) Error() string {
	return "cannot modify default workflow collection"
}
func (e *CannotModifyDefaultCollectionError) Code() string { return "cannot_modify_default_collection" }

// CannotDeleteDefaultCollectionError indicates an attempt to delete the default collection.
// The default collection acts as the root fallback collection and cannot be removed.
type CannotDeleteDefaultCollectionError struct{}

func (e *CannotDeleteDefaultCollectionError) Error() string {
	return "cannot delete default workflow collection"
}
func (e *CannotDeleteDefaultCollectionError) Code() string { return "cannot_delete_default_collection" }

// CollectionNotEmptyError indicates deletion failed because the collection contains workflows and cascade=false.
type CollectionNotEmptyError struct {
	ID    string
	Count int
}

func (e *CollectionNotEmptyError) Error() string {
	return fmt.Sprintf("workflow collection %q is not empty (%d workflows)", e.ID, e.Count)
}
func (e *CollectionNotEmptyError) Code() string { return "collection_not_empty" }

// Sentinel errors matching error types for convenient errors.Is checks if needed
var (
	ErrCannotModifyDefaultCollection = &CannotModifyDefaultCollectionError{}
	ErrCannotDeleteDefaultCollection = &CannotDeleteDefaultCollectionError{}
)

func validateWorkflowCollectionRequest(req WorkflowCollectionRequest) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return &WorkflowCollectionValidationError{Field: "name", Message: "name must not be empty"}
	}
	if len([]byte(name)) > maxWorkflowCollectionNameBytes {
		return &WorkflowCollectionValidationError{Field: "name", Message: "name exceeds maximum byte length"}
	}
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return &WorkflowCollectionValidationError{Field: "name", Message: "name contains invalid characters"}
	}

	if len([]byte(req.Description)) > maxWorkflowCollectionDescriptionBytes {
		return &WorkflowCollectionValidationError{Field: "description", Message: "description exceeds maximum byte length"}
	}
	if !utf8.ValidString(req.Description) || strings.ContainsRune(req.Description, 0) {
		return &WorkflowCollectionValidationError{Field: "description", Message: "description contains invalid characters"}
	}

	return nil
}

// CreateWorkflowCollection creates a new workflow collection.
func (s *Store) CreateWorkflowCollection(ctx context.Context, req WorkflowCollectionRequest) (*WorkflowCollection, error) {
	if err := validateWorkflowCollectionRequest(req); err != nil {
		return nil, err
	}

	var col *WorkflowCollection
	err := s.runImmediate(ctx, func() error {
		name := strings.TrimSpace(req.Name)
		var existingID string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM workflow_collections WHERE name = ?`, name).Scan(&existingID)
		if err == nil {
			return &WorkflowCollectionNameConflictError{Name: name}
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

		_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_collections (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			id, name, desc, now, now)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowCollectionNameConflictError{Name: name}
			}
			return err
		}

		col = &WorkflowCollection{
			ID:            id,
			Name:          name,
			Description:   desc,
			WorkflowCount: 0,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		return nil
	})

	return col, err
}

// ListWorkflowCollections returns all collections with workflow counts, ordered with 'default' first.
func (s *Store) ListWorkflowCollections(ctx context.Context) ([]WorkflowCollection, error) {
	query := `
		SELECT wc.id, wc.name, wc.description, wc.created_at, wc.updated_at,
		       (SELECT COUNT(*) FROM workflows w WHERE w.collection_id = wc.id) AS workflow_count
		FROM workflow_collections wc
		ORDER BY CASE WHEN wc.id = 'default' THEN 0 ELSE 1 END, wc.name ASC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var collections []WorkflowCollection
	for rows.Next() {
		var c WorkflowCollection
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt, &c.WorkflowCount); err != nil {
			return nil, err
		}
		collections = append(collections, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if collections == nil {
		collections = []WorkflowCollection{}
	}
	return collections, nil
}

// GetWorkflowCollection returns a single workflow collection by ID with workflow count.
func (s *Store) GetWorkflowCollection(ctx context.Context, id string) (*WorkflowCollection, error) {
	query := `
		SELECT wc.id, wc.name, wc.description, wc.created_at, wc.updated_at,
		       (SELECT COUNT(*) FROM workflows w WHERE w.collection_id = wc.id) AS workflow_count
		FROM workflow_collections wc
		WHERE wc.id = ?`

	var c WorkflowCollection
	err := s.db.QueryRowContext(ctx, query, id).Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt, &c.WorkflowCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateWorkflowCollection updates the name and description of a collection. Updating 'default' is rejected.
func (s *Store) UpdateWorkflowCollection(ctx context.Context, id string, req WorkflowCollectionRequest) (*WorkflowCollection, error) {
	if id == DefaultWorkflowCollectionID {
		return nil, &CannotModifyDefaultCollectionError{}
	}
	if err := validateWorkflowCollectionRequest(req); err != nil {
		return nil, err
	}

	var updated *WorkflowCollection
	err := s.runImmediate(ctx, func() error {
		var existing WorkflowCollection
		err := s.db.QueryRowContext(ctx, `SELECT id, name, description, created_at, updated_at FROM workflow_collections WHERE id = ?`, id).
			Scan(&existing.ID, &existing.Name, &existing.Description, &existing.CreatedAt, &existing.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return &WorkflowCollectionNotFoundError{ID: id}
		}
		if err != nil {
			return err
		}

		name := strings.TrimSpace(req.Name)
		var conflictID string
		err = s.db.QueryRowContext(ctx, `SELECT id FROM workflow_collections WHERE name = ? AND id != ?`, name, id).Scan(&conflictID)
		if err == nil {
			return &WorkflowCollectionNameConflictError{Name: name}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		desc := strings.TrimSpace(req.Description)

		_, err = s.db.ExecContext(ctx, `UPDATE workflow_collections SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
			name, desc, now, id)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowCollectionNameConflictError{Name: name}
			}
			return err
		}

		var count int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE collection_id = ?`, id).Scan(&count)

		updated = &WorkflowCollection{
			ID:            id,
			Name:          name,
			Description:   desc,
			WorkflowCount: count,
			CreatedAt:     existing.CreatedAt,
			UpdatedAt:     now,
		}
		return nil
	})

	return updated, err
}

// DeleteWorkflowCollection deletes a collection. Deleting 'default' is rejected.
// If cascade is false and the collection has workflows, returns *CollectionNotEmptyError.
// If cascade is true, deletes all workflows in that collection (and their states/schedules/journals) in transaction.
// Also cleans up any associated git sync target (e.g. "workflows:<id>") for the collection.
func (s *Store) DeleteWorkflowCollection(ctx context.Context, id string, cascade bool) error {
	if id == DefaultWorkflowCollectionID {
		return &CannotDeleteDefaultCollectionError{}
	}

	return s.runImmediate(ctx, func() error {
		var existingID string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM workflow_collections WHERE id = ?`, id).Scan(&existingID)
		if errors.Is(err, sql.ErrNoRows) {
			return &WorkflowCollectionNotFoundError{ID: id}
		}
		if err != nil {
			return err
		}

		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE collection_id = ?`, id).Scan(&count); err != nil {
			return err
		}

		// When non-cascade, prevent accidental orphan or bulk workflow deletion
		if count > 0 && !cascade {
			return &CollectionNotEmptyError{ID: id, Count: count}
		}

		before, err := s.adminBindingsTx(ctx)
		if err != nil {
			return err
		}

		if cascade && count > 0 {
			// Explicitly clean up child workflow_states first to ensure journal triggers
			// and referential integrity are respected across SQLite foreign key modes.
			rows, err := s.db.QueryContext(ctx, `SELECT id FROM workflows WHERE collection_id = ?`, id)
			if err != nil {
				return err
			}
			var wfIDs []string
			for rows.Next() {
				var wfID string
				if err := rows.Scan(&wfID); err != nil {
					rows.Close()
					return err
				}
				wfIDs = append(wfIDs, wfID)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}

			for _, wfID := range wfIDs {
				if _, err := s.db.ExecContext(ctx, `DELETE FROM workflow_states WHERE workflow_id = ?`, wfID); err != nil {
					return err
				}
			}

			if _, err := s.db.ExecContext(ctx, `DELETE FROM workflows WHERE collection_id = ?`, id); err != nil {
				return err
			}
		}

		// Clean up collection-specific git sync target if configured ("workflows:<id>")
		syncTarget := fmt.Sprintf("workflows:%s", id)
		_, _ = s.db.ExecContext(ctx, `DELETE FROM git_sync_targets WHERE target = ?`, syncTarget)

		// Delete collection record
		if _, err := s.db.ExecContext(ctx, `DELETE FROM workflow_collections WHERE id = ?`, id); err != nil {
			return err
		}

		if cascade && count > 0 {
			return s.journalAdminBindingsChangedTx(ctx, before)
		}
		return nil
	})
}

// ListWorkflowsByCollection lists workflows belonging to the given collection ID.
// If collectionID is empty, it returns all workflows.
func (s *Store) ListWorkflowsByCollection(ctx context.Context, collectionID string) ([]Workflow, error) {
	if strings.TrimSpace(collectionID) == "" {
		return s.ListWorkflows(ctx)
	}

	query := `
		SELECT w.id, w.collection_id, w.name, w.description, w.entry_state_id, w.snooze_duration_ms, w.variables_json, w.created_at, w.updated_at,
		       (SELECT COUNT(*) FROM workflow_states ws WHERE ws.workflow_id = w.id) AS state_count
		FROM workflows w
		WHERE w.collection_id = ?
		ORDER BY w.name ASC, w.id ASC`

	rows, err := s.db.QueryContext(ctx, query, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workflows []Workflow
	for rows.Next() {
		var wf Workflow
		var entryStateID sql.NullString
		var variablesJSON sql.NullString
		if err := rows.Scan(&wf.ID, &wf.CollectionID, &wf.Name, &wf.Description, &entryStateID, &wf.SnoozeDurationMS, &variablesJSON, &wf.CreatedAt, &wf.UpdatedAt, &wf.StateCount); err != nil {
			return nil, err
		}
		if entryStateID.Valid {
			wf.EntryStateID = &entryStateID.String
		}
		wf.Variables = unmarshalWorkflowVariables(variablesJSON.String)
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

// MoveWorkflow moves a workflow to another collection.
// Validates destination collection exists, enforces destination composite uniqueness
// on (collection_id, name), and updates the workflow record within an immediate transaction.
func (s *Store) MoveWorkflow(ctx context.Context, workflowID string, destCollectionID string) (*Workflow, error) {
	destCollectionID = strings.TrimSpace(destCollectionID)
	if destCollectionID == "" {
		destCollectionID = DefaultWorkflowCollectionID
	}

	var movedWf *Workflow
	err := s.runImmediate(ctx, func() error {
		// 1. Verify destination collection exists
		var destExists int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_collections WHERE id = ?`, destCollectionID).Scan(&destExists)
		if errors.Is(err, sql.ErrNoRows) {
			return &WorkflowCollectionNotFoundError{ID: destCollectionID}
		}
		if err != nil {
			return err
		}

		// 2. Fetch workflow
		var wf Workflow
		var entryStateID sql.NullString
		var variablesJSON sql.NullString
		err = s.db.QueryRowContext(ctx, `
			SELECT id, collection_id, name, description, entry_state_id, snooze_duration_ms, variables_json, created_at, updated_at,
			       (SELECT COUNT(*) FROM workflow_states ws WHERE ws.workflow_id = workflows.id)
			FROM workflows WHERE id = ?`, workflowID).
			Scan(&wf.ID, &wf.CollectionID, &wf.Name, &wf.Description, &entryStateID, &wf.SnoozeDurationMS, &variablesJSON, &wf.CreatedAt, &wf.UpdatedAt, &wf.StateCount)
		if errors.Is(err, sql.ErrNoRows) {
			return &WorkflowNotFoundError{ID: workflowID}
		}
		if err != nil {
			return err
		}
		if entryStateID.Valid {
			wf.EntryStateID = &entryStateID.String
		}
		wf.Variables = unmarshalWorkflowVariables(variablesJSON.String)

		if wf.CollectionID == destCollectionID {
			movedWf = &wf
			return nil
		}

		// 3. Check for name conflict in destination collection
		var conflictID string
		err = s.db.QueryRowContext(ctx, `SELECT id FROM workflows WHERE collection_id = ? AND name = ?`, destCollectionID, wf.Name).Scan(&conflictID)
		if err == nil {
			return &WorkflowNameConflictError{Name: wf.Name}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		_, err = s.db.ExecContext(ctx, `UPDATE workflows SET collection_id = ?, updated_at = ? WHERE id = ?`, destCollectionID, now, workflowID)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowNameConflictError{Name: wf.Name}
			}
			return err
		}

		wf.CollectionID = destCollectionID
		wf.UpdatedAt = now
		movedWf = &wf
		return nil
	})

	return movedWf, err
}

// ExportWorkflowCollection exports only workflows belonging to collectionID as portable bundles.
func (s *Store) ExportWorkflowCollection(ctx context.Context, collectionID string) (*WorkflowsExportPayload, error) {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		collectionID = DefaultWorkflowCollectionID
	}

	// Verify collection exists
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_collections WHERE id = ?`, collectionID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &WorkflowCollectionNotFoundError{ID: collectionID}
	}
	if err != nil {
		return nil, err
	}

	workflows, err := s.ListWorkflowsByCollection(ctx, collectionID)
	if err != nil {
		return nil, err
	}

	bundles := make([]WorkflowExportBundle, 0, len(workflows))
	for _, wf := range workflows {
		bundle, err := s.ExportWorkflow(ctx, wf.ID)
		if err != nil {
			return nil, err
		}
		if bundle != nil {
			bundles = append(bundles, *bundle)
		}
	}

	return &WorkflowsExportPayload{
		Format:     "mecha-workflows",
		Version:    1,
		ExportedAt: formatTimestamp(s.clock.Now()),
		Workflows:  bundles,
	}, nil
}

// ReplaceWorkflowsInCollection atomically wipes and replaces workflows inside that specific collection,
// leaving workflows in other collections untouched. It maps portable state keys to new UUIDs
// and remaps transitions before inserting records into the database.
func (s *Store) ReplaceWorkflowsInCollection(ctx context.Context, collectionID string, bundles []WorkflowExportBundle) error {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		collectionID = DefaultWorkflowCollectionID
	}

	// Verify collection exists
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_collections WHERE id = ?`, collectionID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return &WorkflowCollectionNotFoundError{ID: collectionID}
	}
	if err != nil {
		return err
	}

	// 1. Validate all bundles upfront before entering the transaction
	names := make(map[string]bool, len(bundles))
	for i, bundle := range bundles {
		if err := s.ValidateExportBundle(bundle); err != nil {
			return err
		}
		name := strings.TrimSpace(bundle.Workflow.Name)
		if names[name] {
			return &WorkflowValidationError{
				Field:   fmt.Sprintf("workflows[%d].name", i),
				Message: fmt.Sprintf("duplicate workflow name %q in bundle list", name),
			}
		}
		names[name] = true
	}

	// 2. Perform replacement in an immediate transaction
	return s.runImmediate(ctx, func() error {
		before, err := s.adminBindingsTx(ctx)
		if err != nil {
			return err
		}

		// Find existing workflow IDs in this collection to delete their states
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM workflows WHERE collection_id = ?`, collectionID)
		if err != nil {
			return err
		}
		var oldWfIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			oldWfIDs = append(oldWfIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, wfID := range oldWfIDs {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM workflow_states WHERE workflow_id = ?`, wfID); err != nil {
				return fmt.Errorf("delete workflow_states for workflow %s: %w", wfID, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM workflows WHERE collection_id = ?`, collectionID); err != nil {
			return fmt.Errorf("delete workflows in collection %s: %w", collectionID, err)
		}

		if err := s.journalAdminBindingsChangedTx(ctx, before); err != nil {
			return err
		}

		now := formatTimestamp(s.clock.Now())

		for _, bundle := range bundles {
			newWfID, err := s.ids.Next()
			if err != nil {
				return err
			}

			// Generate new UUIDs for each state
			keyToNewID := make(map[string]string, len(bundle.States))
			for _, st := range bundle.States {
				newID, err := s.ids.Next()
				if err != nil {
					return err
				}
				keyToNewID[st.Key] = newID
			}

			// Determine new entry_state_id
			var newEntryStateID *string
			if bundle.EntryStateKey != "" {
				if id, ok := keyToNewID[bundle.EntryStateKey]; ok {
					newEntryStateID = &id
				}
			}
			if newEntryStateID == nil {
				for _, st := range bundle.States {
					if st.IsEntryState {
						id := keyToNewID[st.Key]
						newEntryStateID = &id
						break
					}
				}
			}

			varsList := bundle.Workflow.Variables
			if varsList == nil {
				varsList = []WorkflowVariableDefinition{}
			}
			variablesJSON, err := marshalWorkflowVariables(varsList)
			if err != nil {
				return err
			}

			workflowName := strings.TrimSpace(bundle.Workflow.Name)
			workflowDesc := strings.TrimSpace(bundle.Workflow.Description)
			snoozeDurationMS := bundle.Workflow.SnoozeDurationMS
			if snoozeDurationMS <= 0 {
				snoozeDurationMS = 600000
			}

			// Insert workflow with collection_id
			_, err = s.db.ExecContext(ctx, `INSERT INTO workflows (id, collection_id, name, description, entry_state_id, snooze_duration_ms, variables_json, created_at, updated_at) VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?)`,
				newWfID, collectionID, workflowName, workflowDesc, snoozeDurationMS, variablesJSON, now, now)
			if err != nil {
				return fmt.Errorf("insert workflow %s: %w", workflowName, err)
			}

			// Insert all states
			for _, st := range bundle.States {
				newStateID := keyToNewID[st.Key]

				remappedTransitions := make([]WorkflowTransitionConfig, 0, len(st.Transitions))
				for _, tr := range st.Transitions {
					mappedTargetID, ok := keyToNewID[tr.TargetStateKey]
					if ok {
						remappedTransitions = append(remappedTransitions, WorkflowTransitionConfig{
							TargetStateID: mappedTargetID,
							Description:   tr.Description,
						})
					}
				}

				remindersJSON, err := marshalWorkflowReminders(st.Reminders)
				if err != nil {
					return err
				}

				monitorsCopy := st.Monitors
				if monitorsCopy == nil {
					monitorsCopy = []WorkflowMonitorConfig{}
				}
				monitorsJSON, err := marshalWorkflowMonitors(monitorsCopy)
				if err != nil {
					return err
				}

				sanitizedTools, err := sanitizeWorkflowAllowedTools(st.AllowedTools)
				if err != nil {
					return err
				}
				allowedToolsJSON, err := marshalWorkflowAllowedTools(sanitizedTools)
				if err != nil {
					return err
				}

				transitionsJSON, err := marshalWorkflowTransitions(remappedTransitions)
				if err != nil {
					return err
				}

				allowExitInt := 1
				if !st.AllowExit {
					allowExitInt = 0
				}

				_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_states (id, workflow_id, name, description, entry_text, reminders_json, monitors_json, allowed_tools_json, transitions_json, allow_exit, allow_implicit_return, ui_pos_x, ui_pos_y, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					newStateID, newWfID, st.Name, st.Description, st.EntryText, remindersJSON, monitorsJSON, allowedToolsJSON, transitionsJSON, allowExitInt, st.AllowImplicitReturn, st.UIPosX, st.UIPosY, now, now)
				if err != nil {
					return fmt.Errorf("insert workflow state %s: %w", st.Name, err)
				}
			}

			// Update entry_state_id if set
			if newEntryStateID != nil {
				_, err = s.db.ExecContext(ctx, `UPDATE workflows SET entry_state_id = ? WHERE id = ?`, *newEntryStateID, newWfID)
				if err != nil {
					return fmt.Errorf("update entry_state_id for workflow %s: %w", workflowName, err)
				}
			}
		}

		return nil
	})
}
