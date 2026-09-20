package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	workflowdomain "github.com/reflexivity/mecha-developer/router-proxy/internal/workflow"
)

// WorkflowDefinitionRecord is the mutable definition head. Validation belongs
// to the current immutable revision and is included for deterministic lists.
type WorkflowDefinitionRecord struct {
	DefinitionID    string
	Name            string
	Description     string
	Status          string
	Version         int64
	CurrentRevision int64
	ValidationValid bool
	ValidationJSON  json.RawMessage
	CreatedAt       string
	UpdatedAt       string
}

type WorkflowDefinitionRevisionRecord struct {
	DefinitionID    string
	Revision        int64
	SnapshotJSON    json.RawMessage
	ValidationValid bool
	ValidationJSON  json.RawMessage
	CreatedAt       string
}

type WorkflowDefinitionCreate struct {
	DefinitionID    string
	Name            string
	Description     string
	Status          string
	SnapshotJSON    json.RawMessage
	ValidationValid bool
	ValidationJSON  json.RawMessage
	CreatedAt       string
}

type WorkflowDefinitionUpdate struct {
	DefinitionID     string
	ExpectedVersion  int64
	ExpectedRevision int64
	Name             string
	Description      string
	Status           string
	SnapshotJSON     json.RawMessage
	ValidationValid  bool
	ValidationJSON   json.RawMessage
	UpdatedAt        string
}

// WorkflowMutation identifies and stores an HTTP mutation replay in the same
// immediate transaction as its definition effect.
type WorkflowMutation struct {
	Method            string
	RequestID         string
	CanonicalPath     string
	CanonicalBodyHash string
	ResponseStatus    int
	ResponseBody      []byte
}

type WorkflowMutationResult struct {
	Definition     *WorkflowDefinitionRecord
	Revision       *WorkflowDefinitionRevisionRecord
	Replayed       bool
	ResponseStatus int
	ResponseBody   []byte
}

type WorkflowIdempotencyConflictError struct{}

func (*WorkflowIdempotencyConflictError) Error() string {
	return "workflow mutation request identity was reused with different content"
}

type WorkflowDefinitionNameConflictError struct{ Name string }

func (e *WorkflowDefinitionNameConflictError) Error() string {
	return "workflow definition name is already in use"
}

type WorkflowDefinitionRevisionConflictError struct {
	DefinitionID     string
	ExpectedVersion  int64
	ExpectedRevision int64
}

func (e *WorkflowDefinitionRevisionConflictError) Error() string {
	return "workflow definition version or revision is stale"
}

type WorkflowDefinitionHasActiveRunError struct{ DefinitionID string }

func (e *WorkflowDefinitionHasActiveRunError) Error() string {
	return "workflow definition has an active run"
}

func validateWorkflowMutation(m WorkflowMutation) error {
	if m.Method != "POST" && m.Method != "PUT" && m.Method != "DELETE" {
		return errors.New("workflow mutation method is invalid")
	}
	if !managedCanonicalUUIDRE.MatchString(m.RequestID) || m.CanonicalPath == "" || !managedSHA256RE.MatchString(m.CanonicalBodyHash) {
		return errors.New("workflow mutation identity is invalid")
	}
	if m.ResponseStatus < 200 || m.ResponseStatus > 599 || len(m.ResponseBody) > 64*1024 {
		return errors.New("workflow mutation response is invalid")
	}
	return nil
}

func validateWorkflowDefinitionValues(id, name, description, status string, snapshot json.RawMessage) (json.RawMessage, json.RawMessage, bool, error) {
	if !managedCanonicalUUIDRE.MatchString(id) {
		return nil, nil, false, errors.New("workflow definition ID is invalid")
	}
	if !utf8.ValidString(name) || strings.IndexByte(name, 0) >= 0 || strings.TrimSpace(name) == "" || len([]byte(name)) > workflowdomain.MaxNameBytes {
		return nil, nil, false, errors.New("workflow definition name is invalid")
	}
	if !utf8.ValidString(description) || strings.IndexByte(description, 0) >= 0 || len([]byte(description)) > workflowdomain.MaxDescriptionBytes {
		return nil, nil, false, errors.New("workflow definition description is invalid")
	}
	if status != string(workflowdomain.StatusEnabled) && status != string(workflowdomain.StatusDisabled) && status != string(workflowdomain.StatusArchived) {
		return nil, nil, false, errors.New("workflow definition status is invalid")
	}
	if err := validateWorkflowJSONObject(snapshot, 64*1024, "snapshot"); err != nil {
		return nil, nil, false, err
	}
	decoded, err := workflowdomain.DecodeSnapshotJSON(snapshot)
	if err != nil {
		return nil, nil, false, err
	}
	validation := workflowdomain.ValidateDefinition(workflowdomain.Definition{
		Name: name, Description: description, Status: workflowdomain.DefinitionStatus(status), Snapshot: decoded,
	})
	canonicalSnapshot, err := json.Marshal(decoded)
	if err != nil {
		return nil, nil, false, err
	}
	canonicalValidation, err := json.Marshal(validation)
	if err != nil {
		return nil, nil, false, err
	}
	return canonicalSnapshot, canonicalValidation, validation.Valid, nil
}

func validateWorkflowJSONObject(value json.RawMessage, limit int, label string) error {
	if len(value) == 0 || len(value) > limit || !json.Valid(value) {
		return fmt.Errorf("workflow %s JSON is invalid", label)
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(value))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return fmt.Errorf("workflow %s must be a JSON object", label)
	}
	return nil
}

func findWorkflowMutationTx(ctx context.Context, db *sql.DB, mutation WorkflowMutation) (*WorkflowMutationResult, error) {
	var path, bodyHash string
	var status int
	var body []byte
	err := db.QueryRowContext(ctx, `SELECT canonical_path,canonical_body_hash,response_status,response_body
		FROM workflow_http_mutations WHERE method=? AND request_id=?`, mutation.Method, mutation.RequestID).
		Scan(&path, &bodyHash, &status, &body)
	if err != nil {
		return nil, err
	}
	if path != mutation.CanonicalPath || bodyHash != mutation.CanonicalBodyHash {
		return nil, &WorkflowIdempotencyConflictError{}
	}
	return &WorkflowMutationResult{Replayed: true, ResponseStatus: status, ResponseBody: append([]byte(nil), body...)}, nil
}

func insertWorkflowMutationTx(ctx context.Context, db *sql.DB, mutation WorkflowMutation, createdAt string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO workflow_http_mutations(method,request_id,canonical_path,canonical_body_hash,response_status,response_body,created_at)
		VALUES(?,?,?,?,?,?,?)`, mutation.Method, mutation.RequestID, mutation.CanonicalPath, mutation.CanonicalBodyHash, mutation.ResponseStatus, mutation.ResponseBody, createdAt)
	if err != nil && isConstraintError(err) {
		return &WorkflowIdempotencyConflictError{}
	}
	return err
}

func scanWorkflowDefinition(scanner interface{ Scan(...any) error }) (*WorkflowDefinitionRecord, error) {
	var record WorkflowDefinitionRecord
	var valid int
	var validation string
	if err := scanner.Scan(&record.DefinitionID, &record.Name, &record.Description, &record.Status, &record.Version, &record.CurrentRevision, &valid, &validation, &record.CreatedAt, &record.UpdatedAt); err != nil {
		return nil, err
	}
	record.ValidationValid = valid != 0
	record.ValidationJSON = json.RawMessage(validation)
	return &record, nil
}

const workflowDefinitionSelect = `SELECT d.definition_id,d.name,d.description,d.status,d.version,d.current_revision,r.validation_valid,r.validation_json,d.created_at,d.updated_at
	FROM workflow_definitions d JOIN workflow_definition_revisions r ON r.definition_id=d.definition_id AND r.revision=d.current_revision`

func scanWorkflowRevision(scanner interface{ Scan(...any) error }) (*WorkflowDefinitionRevisionRecord, error) {
	var record WorkflowDefinitionRevisionRecord
	var snapshot, validation string
	var valid int
	if err := scanner.Scan(&record.DefinitionID, &record.Revision, &snapshot, &valid, &validation, &record.CreatedAt); err != nil {
		return nil, err
	}
	record.SnapshotJSON = json.RawMessage(snapshot)
	record.ValidationValid = valid != 0
	record.ValidationJSON = json.RawMessage(validation)
	return &record, nil
}

func workflowRevisionTx(ctx context.Context, db *sql.DB, definitionID string, revision int64) (*WorkflowDefinitionRevisionRecord, error) {
	return scanWorkflowRevision(db.QueryRowContext(ctx, `SELECT definition_id,revision,snapshot_json,validation_valid,validation_json,created_at
		FROM workflow_definition_revisions WHERE definition_id=? AND revision=?`, definitionID, revision))
}

func (s *Store) CreateWorkflowDefinition(ctx context.Context, create WorkflowDefinitionCreate, mutation WorkflowMutation) (WorkflowMutationResult, error) {
	if ctx == nil {
		return WorkflowMutationResult{}, errors.New("context is required")
	}
	if err := validateWorkflowMutation(mutation); err != nil {
		return WorkflowMutationResult{}, err
	}
	var result WorkflowMutationResult
	err := s.runImmediate(ctx, func() error {
		replay, err := findWorkflowMutationTx(ctx, s.db, mutation)
		if err == nil {
			result = *replay
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if create.DefinitionID == "" {
			create.DefinitionID, err = s.ids.Next()
			if err != nil {
				return err
			}
		}
		if create.CreatedAt == "" {
			create.CreatedAt = formatTimestamp(s.clock.Now())
		}
		create.SnapshotJSON, create.ValidationJSON, create.ValidationValid, err = validateWorkflowDefinitionValues(create.DefinitionID, create.Name, create.Description, create.Status, create.SnapshotJSON)
		if err != nil {
			return err
		}
		var found string
		if err = s.db.QueryRowContext(ctx, `SELECT definition_id FROM workflow_definitions WHERE name=?`, create.Name).Scan(&found); err == nil {
			return &WorkflowDefinitionNameConflictError{Name: create.Name}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_definitions(definition_id,name,description,status,version,current_revision,created_at,updated_at) VALUES(?,?,?,?,1,1,?,?)`, create.DefinitionID, create.Name, create.Description, create.Status, create.CreatedAt, create.CreatedAt)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowDefinitionNameConflictError{Name: create.Name}
			}
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_definition_revisions(definition_id,revision,snapshot_json,validation_valid,validation_json,created_at) VALUES(?,1,?,?,?,?)`, create.DefinitionID, string(create.SnapshotJSON), boolInt(create.ValidationValid), string(create.ValidationJSON), create.CreatedAt)
		if err != nil {
			return err
		}
		if err = insertWorkflowMutationTx(ctx, s.db, mutation, create.CreatedAt); err != nil {
			return err
		}
		result.Definition, err = scanWorkflowDefinition(s.db.QueryRowContext(ctx, workflowDefinitionSelect+` WHERE d.definition_id=?`, create.DefinitionID))
		if err != nil {
			return err
		}
		result.Revision, err = workflowRevisionTx(ctx, s.db, create.DefinitionID, 1)
		if err != nil {
			return err
		}
		result.ResponseStatus = mutation.ResponseStatus
		result.ResponseBody = append([]byte(nil), mutation.ResponseBody...)
		return nil
	})
	return result, err
}

func (s *Store) UpdateWorkflowDefinition(ctx context.Context, update WorkflowDefinitionUpdate, mutation WorkflowMutation) (WorkflowMutationResult, error) {
	if ctx == nil {
		return WorkflowMutationResult{}, errors.New("context is required")
	}
	if update.ExpectedVersion < 1 || update.ExpectedRevision < 1 {
		return WorkflowMutationResult{}, errors.New("expected workflow version and revision are required")
	}
	if err := validateWorkflowMutation(mutation); err != nil {
		return WorkflowMutationResult{}, err
	}
	var result WorkflowMutationResult
	err := s.runImmediate(ctx, func() error {
		replay, err := findWorkflowMutationTx(ctx, s.db, mutation)
		if err == nil {
			result = *replay
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if update.UpdatedAt == "" {
			update.UpdatedAt = formatTimestamp(s.clock.Now())
		}
		update.SnapshotJSON, update.ValidationJSON, update.ValidationValid, err = validateWorkflowDefinitionValues(update.DefinitionID, update.Name, update.Description, update.Status, update.SnapshotJSON)
		if err != nil {
			return err
		}
		var version, revision int64
		if err = s.db.QueryRowContext(ctx, `SELECT version,current_revision FROM workflow_definitions WHERE definition_id=?`, update.DefinitionID).Scan(&version, &revision); err != nil {
			return err
		}
		if version != update.ExpectedVersion || revision != update.ExpectedRevision {
			return &WorkflowDefinitionRevisionConflictError{DefinitionID: update.DefinitionID, ExpectedVersion: update.ExpectedVersion, ExpectedRevision: update.ExpectedRevision}
		}
		var other string
		if err = s.db.QueryRowContext(ctx, `SELECT definition_id FROM workflow_definitions WHERE name=? AND definition_id<>?`, update.Name, update.DefinitionID).Scan(&other); err == nil {
			return &WorkflowDefinitionNameConflictError{Name: update.Name}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next := revision + 1
		if _, err = s.db.ExecContext(ctx, `INSERT INTO workflow_definition_revisions(definition_id,revision,snapshot_json,validation_valid,validation_json,created_at) VALUES(?,?,?,?,?,?)`, update.DefinitionID, next, string(update.SnapshotJSON), boolInt(update.ValidationValid), string(update.ValidationJSON), update.UpdatedAt); err != nil {
			return err
		}
		changed, err := s.db.ExecContext(ctx, `UPDATE workflow_definitions SET name=?,description=?,status=?,version=version+1,current_revision=?,updated_at=? WHERE definition_id=? AND version=? AND current_revision=?`, update.Name, update.Description, update.Status, next, update.UpdatedAt, update.DefinitionID, update.ExpectedVersion, update.ExpectedRevision)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowDefinitionNameConflictError{Name: update.Name}
			}
			return err
		}
		count, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return &WorkflowDefinitionRevisionConflictError{DefinitionID: update.DefinitionID, ExpectedVersion: update.ExpectedVersion, ExpectedRevision: update.ExpectedRevision}
		}
		if err = pruneWorkflowDefinitionRevisionsTx(ctx, s.db, update.DefinitionID, next); err != nil {
			return err
		}
		if err = insertWorkflowMutationTx(ctx, s.db, mutation, update.UpdatedAt); err != nil {
			return err
		}
		result.Definition, err = scanWorkflowDefinition(s.db.QueryRowContext(ctx, workflowDefinitionSelect+` WHERE d.definition_id=?`, update.DefinitionID))
		if err != nil {
			return err
		}
		result.Revision, err = workflowRevisionTx(ctx, s.db, update.DefinitionID, next)
		if err != nil {
			return err
		}
		result.ResponseStatus = mutation.ResponseStatus
		result.ResponseBody = append([]byte(nil), mutation.ResponseBody...)
		return nil
	})
	return result, err
}

func pruneWorkflowDefinitionRevisionsTx(ctx context.Context, db *sql.DB, definitionID string, current int64) error {
	if current <= 3 {
		return nil
	}
	_, err := db.ExecContext(ctx, `DELETE FROM workflow_definition_revisions AS revision
		WHERE revision.definition_id=? AND revision.revision<=?
		AND NOT EXISTS(SELECT 1 FROM workflow_runs run WHERE run.definition_id=revision.definition_id AND run.revision=revision.revision)`, definitionID, current-3)
	return err
}

func (s *Store) WorkflowDefinition(ctx context.Context, definitionID string) (*WorkflowDefinitionRecord, *WorkflowDefinitionRevisionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("store is closed")
	}
	definition, err := scanWorkflowDefinition(s.db.QueryRowContext(ctx, workflowDefinitionSelect+` WHERE d.definition_id=?`, definitionID))
	if err != nil {
		return nil, nil, err
	}
	revision, err := workflowRevisionTx(ctx, s.db, definitionID, definition.CurrentRevision)
	return definition, revision, err
}

func (s *Store) ListWorkflowDefinitions(ctx context.Context) ([]WorkflowDefinitionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, workflowDefinitionSelect+` ORDER BY d.name ASC,d.definition_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []WorkflowDefinitionRecord{}
	for rows.Next() {
		record, err := scanWorkflowDefinition(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *record)
	}
	return result, rows.Err()
}

func (s *Store) WorkflowDefinitionRevision(ctx context.Context, definitionID string, revision int64) (*WorkflowDefinitionRevisionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return workflowRevisionTx(ctx, s.db, definitionID, revision)
}

func (s *Store) ListWorkflowDefinitionRevisions(ctx context.Context, definitionID string) ([]WorkflowDefinitionRevisionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT definition_id,revision,snapshot_json,validation_valid,validation_json,created_at FROM workflow_definition_revisions WHERE definition_id=? ORDER BY revision DESC`, definitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []WorkflowDefinitionRevisionRecord{}
	for rows.Next() {
		record, err := scanWorkflowRevision(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *record)
	}
	return result, rows.Err()
}

func (s *Store) DeleteWorkflowDefinition(ctx context.Context, definitionID string, mutation WorkflowMutation) (WorkflowMutationResult, error) {
	if ctx == nil {
		return WorkflowMutationResult{}, errors.New("context is required")
	}
	if !managedCanonicalUUIDRE.MatchString(definitionID) {
		return WorkflowMutationResult{}, errors.New("workflow definition ID is invalid")
	}
	if err := validateWorkflowMutation(mutation); err != nil {
		return WorkflowMutationResult{}, err
	}
	var result WorkflowMutationResult
	err := s.runImmediate(ctx, func() error {
		replay, err := findWorkflowMutationTx(ctx, s.db, mutation)
		if err == nil {
			result = *replay
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var present int
		if err = s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_definitions WHERE definition_id=?`, definitionID).Scan(&present); err != nil {
			return err
		}
		var active int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_runs WHERE definition_id=? AND status='active'`, definitionID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return &WorkflowDefinitionHasActiveRunError{DefinitionID: definitionID}
		}
		// Terminal run-owned rows cascade from workflow_runs. Shared messages are
		// retained because notifications use ON DELETE SET NULL in that direction.
		if _, err = s.db.ExecContext(ctx, `DELETE FROM workflow_runs WHERE definition_id=?`, definitionID); err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `DELETE FROM workflow_definitions WHERE definition_id=?`, definitionID); err != nil {
			return err
		}
		createdAt := formatTimestamp(s.clock.Now())
		if err = insertWorkflowMutationTx(ctx, s.db, mutation, createdAt); err != nil {
			return err
		}
		result.ResponseStatus = mutation.ResponseStatus
		result.ResponseBody = append([]byte(nil), mutation.ResponseBody...)
		return nil
	})
	return result, err
}
