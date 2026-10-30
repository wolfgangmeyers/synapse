package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxWorkflowScheduleNameBytes = 256
)

// WorkflowScheduleRecord represents a persistent workflow schedule.
type WorkflowScheduleRecord struct {
	ID             string            `json:"id"`
	WorkflowID     string            `json:"workflow_id"`
	Name           string            `json:"name"`
	Enabled        bool              `json:"enabled"`
	ScheduleType   string            `json:"schedule_type"` // "interval" or "cron"
	IntervalMS     *int64            `json:"interval_ms,omitempty"`
	CronExpression *string           `json:"cron_expression,omitempty"`
	Variables      map[string]string `json:"variables"`
	TargetCwd      string            `json:"target_cwd"`
	ModelProvider  string            `json:"model_provider"`
	ModelID        string            `json:"model_id"`
	NextRunAt      string            `json:"next_run_at"`
	LastRunAt      *string           `json:"last_run_at,omitempty"`
	LastRunStatus  *string           `json:"last_run_status,omitempty"`
	LastRunError   *string           `json:"last_run_error,omitempty"`
	CreatedAt      string            `json:"created_at"`
	UpdatedAt      string            `json:"updated_at"`
}

// WorkflowScheduleRequest holds creation or update parameters for a schedule.
type WorkflowScheduleRequest struct {
	WorkflowID     string            `json:"workflow_id"`
	Name           string            `json:"name"`
	Enabled        *bool             `json:"enabled,omitempty"`
	ScheduleType   string            `json:"schedule_type"` // "interval" or "cron"
	IntervalMS     *int64            `json:"interval_ms,omitempty"`
	CronExpression *string           `json:"cron_expression,omitempty"`
	Variables      map[string]string `json:"variables,omitempty"`
	TargetCwd      string            `json:"target_cwd"`
	ModelProvider  string            `json:"model_provider"`
	ModelID        string            `json:"model_id"`
	NextRunAt      string            `json:"next_run_at"`
}

// ValidateWorkflowVariables verifies that all required variables defined in workflowVars
// are present in inputVars and non-empty (not blank / whitespace-only).
func ValidateWorkflowVariables(workflowVars []WorkflowVariableDefinition, inputVars map[string]string) error {
	var missing []string
	for _, v := range workflowVars {
		if !v.Required {
			continue
		}
		val, ok := inputVars[v.Name]
		if !ok || strings.TrimSpace(val) == "" {
			missing = append(missing, v.Name)
		}
	}
	if len(missing) > 0 {
		return &WorkflowValidationError{
			Field:   "variables",
			Message: fmt.Sprintf("missing required workflow parameter(s): %s", strings.Join(missing, ", ")),
		}
	}
	return nil
}

func validateWorkflowScheduleRequest(req WorkflowScheduleRequest) error {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return &WorkflowValidationError{Field: "name", Message: "schedule name must not be empty"}
	}
	if len([]byte(name)) > maxWorkflowScheduleNameBytes {
		return &WorkflowValidationError{Field: "name", Message: "schedule name exceeds maximum byte length"}
	}
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return &WorkflowValidationError{Field: "name", Message: "schedule name contains invalid characters"}
	}

	workflowID := strings.TrimSpace(req.WorkflowID)
	if workflowID == "" {
		return &WorkflowValidationError{Field: "workflow_id", Message: "workflow_id must not be empty"}
	}

	switch req.ScheduleType {
	case "interval":
		if req.IntervalMS == nil || *req.IntervalMS <= 0 {
			return &WorkflowValidationError{Field: "interval_ms", Message: "interval_ms must be positive for interval schedule"}
		}
	case "cron":
		if req.CronExpression == nil || strings.TrimSpace(*req.CronExpression) == "" {
			return &WorkflowValidationError{Field: "cron_expression", Message: "cron_expression must not be empty for cron schedule"}
		}
	default:
		return &WorkflowValidationError{Field: "schedule_type", Message: "schedule_type must be 'interval' or 'cron'"}
	}

	if strings.TrimSpace(req.NextRunAt) == "" {
		return &WorkflowValidationError{Field: "next_run_at", Message: "next_run_at must not be empty"}
	}

	return nil
}

func marshalScheduleVariables(vars map[string]string) (string, error) {
	if vars == nil {
		return "{}", nil
	}
	b, err := json.Marshal(vars)
	if err != nil {
		return "{}", err
	}
	return string(b), nil
}

func unmarshalScheduleVariables(raw string) map[string]string {
	res := make(map[string]string)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return res
	}
	_ = json.Unmarshal([]byte(trimmed), &res)
	return res
}

func scanWorkflowSchedule(row interface {
	Scan(dest ...interface{}) error
}) (*WorkflowScheduleRecord, error) {
	var (
		rec            WorkflowScheduleRecord
		enabledInt     int
		intervalMS     sql.NullInt64
		cronExpr       sql.NullString
		varsJSON       string
		lastRunAt      sql.NullString
		lastRunStatus  sql.NullString
		lastRunError   sql.NullString
	)

	err := row.Scan(
		&rec.ID,
		&rec.WorkflowID,
		&rec.Name,
		&enabledInt,
		&rec.ScheduleType,
		&intervalMS,
		&cronExpr,
		&varsJSON,
		&rec.TargetCwd,
		&rec.ModelProvider,
		&rec.ModelID,
		&rec.NextRunAt,
		&lastRunAt,
		&lastRunStatus,
		&lastRunError,
		&rec.CreatedAt,
		&rec.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	rec.Enabled = enabledInt != 0
	if intervalMS.Valid {
		rec.IntervalMS = &intervalMS.Int64
	}
	if cronExpr.Valid {
		rec.CronExpression = &cronExpr.String
	}
	if lastRunAt.Valid {
		rec.LastRunAt = &lastRunAt.String
	}
	if lastRunStatus.Valid {
		rec.LastRunStatus = &lastRunStatus.String
	}
	if lastRunError.Valid {
		rec.LastRunError = &lastRunError.String
	}
	rec.Variables = unmarshalScheduleVariables(varsJSON)

	return &rec, nil
}

const workflowScheduleSelect = `
	SELECT id, workflow_id, name, enabled, schedule_type, interval_ms, cron_expression,
	       variables_json, target_cwd, model_provider, model_id, next_run_at,
	       last_run_at, last_run_status, last_run_error, created_at, updated_at
	FROM workflow_schedules
`

// CreateWorkflowSchedule inserts a new workflow schedule after validating required parameters against the workflow definition.
func (s *Store) CreateWorkflowSchedule(ctx context.Context, req WorkflowScheduleRequest) (*WorkflowScheduleRecord, error) {
	if err := validateWorkflowScheduleRequest(req); err != nil {
		return nil, err
	}

	var rec *WorkflowScheduleRecord
	err := s.runImmediate(ctx, func() error {
		wf, err := s.GetWorkflow(ctx, req.WorkflowID)
		if err != nil {
			return err
		}
		if wf == nil {
			return &WorkflowNotFoundError{ID: req.WorkflowID}
		}

		if err := ValidateWorkflowVariables(wf.Variables, req.Variables); err != nil {
			return err
		}

		id, err := s.ids.Next()
		if err != nil {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		enabled := 1
		if req.Enabled != nil && !*req.Enabled {
			enabled = 0
		}

		varsJSON, err := marshalScheduleVariables(req.Variables)
		if err != nil {
			return err
		}

		_, err = s.db.ExecContext(ctx, `
			INSERT INTO workflow_schedules (
				id, workflow_id, name, enabled, schedule_type, interval_ms, cron_expression,
				variables_json, target_cwd, model_provider, model_id, next_run_at,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			id,
			strings.TrimSpace(req.WorkflowID),
			strings.TrimSpace(req.Name),
			enabled,
			req.ScheduleType,
			req.IntervalMS,
			req.CronExpression,
			varsJSON,
			req.TargetCwd,
			req.ModelProvider,
			req.ModelID,
			req.NextRunAt,
			now,
			now,
		)
		if err != nil {
			return err
		}

		rec = &WorkflowScheduleRecord{
			ID:             id,
			WorkflowID:     strings.TrimSpace(req.WorkflowID),
			Name:           strings.TrimSpace(req.Name),
			Enabled:        enabled == 1,
			ScheduleType:   req.ScheduleType,
			IntervalMS:     req.IntervalMS,
			CronExpression: req.CronExpression,
			Variables:      req.Variables,
			TargetCwd:      req.TargetCwd,
			ModelProvider:  req.ModelProvider,
			ModelID:        req.ModelID,
			NextRunAt:      req.NextRunAt,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if rec.Variables == nil {
			rec.Variables = make(map[string]string)
		}
		return nil
	})

	return rec, err
}

// GetWorkflowSchedule retrieves a schedule by ID.
func (s *Store) GetWorkflowSchedule(ctx context.Context, id string) (*WorkflowScheduleRecord, error) {
	row := s.db.QueryRowContext(ctx, workflowScheduleSelect+` WHERE id = ?`, id)
	rec, err := scanWorkflowSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return rec, err
}

// ListWorkflowSchedules lists schedules, optionally filtered by workflowID.
func (s *Store) ListWorkflowSchedules(ctx context.Context, workflowID string) ([]WorkflowScheduleRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if strings.TrimSpace(workflowID) != "" {
		rows, err = s.db.QueryContext(ctx, workflowScheduleSelect+` WHERE workflow_id = ? ORDER BY created_at ASC, id ASC`, strings.TrimSpace(workflowID))
	} else {
		rows, err = s.db.QueryContext(ctx, workflowScheduleSelect+` ORDER BY created_at ASC, id ASC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []WorkflowScheduleRecord
	for rows.Next() {
		rec, err := scanWorkflowSchedule(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []WorkflowScheduleRecord{}
	}
	return list, nil
}

// UpdateWorkflowSchedule updates an existing schedule.
func (s *Store) UpdateWorkflowSchedule(ctx context.Context, id string, req WorkflowScheduleRequest) (*WorkflowScheduleRecord, error) {
	if err := validateWorkflowScheduleRequest(req); err != nil {
		return nil, err
	}

	var rec *WorkflowScheduleRecord
	err := s.runImmediate(ctx, func() error {
		existing, err := s.GetWorkflowSchedule(ctx, id)
		if err != nil {
			return err
		}
		if existing == nil {
			return &WorkflowValidationError{Field: "id", Message: fmt.Sprintf("schedule %q not found", id)}
		}

		wf, err := s.GetWorkflow(ctx, req.WorkflowID)
		if err != nil {
			return err
		}
		if wf == nil {
			return &WorkflowNotFoundError{ID: req.WorkflowID}
		}

		if err := ValidateWorkflowVariables(wf.Variables, req.Variables); err != nil {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		enabled := 1
		if req.Enabled != nil && !*req.Enabled {
			enabled = 0
		} else if req.Enabled == nil && !existing.Enabled {
			enabled = 0
		}

		varsJSON, err := marshalScheduleVariables(req.Variables)
		if err != nil {
			return err
		}

		_, err = s.db.ExecContext(ctx, `
			UPDATE workflow_schedules SET
				workflow_id = ?,
				name = ?,
				enabled = ?,
				schedule_type = ?,
				interval_ms = ?,
				cron_expression = ?,
				variables_json = ?,
				target_cwd = ?,
				model_provider = ?,
				model_id = ?,
				next_run_at = ?,
				updated_at = ?
			WHERE id = ?
		`,
			strings.TrimSpace(req.WorkflowID),
			strings.TrimSpace(req.Name),
			enabled,
			req.ScheduleType,
			req.IntervalMS,
			req.CronExpression,
			varsJSON,
			req.TargetCwd,
			req.ModelProvider,
			req.ModelID,
			req.NextRunAt,
			now,
			id,
		)
		if err != nil {
			return err
		}

		rec, err = s.GetWorkflowSchedule(ctx, id)
		return err
	})

	return rec, err
}

// DeleteWorkflowSchedule deletes a workflow schedule by ID.
func (s *Store) DeleteWorkflowSchedule(ctx context.Context, id string) (bool, error) {
	var deleted bool
	err := s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `DELETE FROM workflow_schedules WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		deleted = n > 0
		return nil
	})
	return deleted, err
}

// GetDueWorkflowSchedules returns enabled schedules where next_run_at <= now ordered by next_run_at.
func (s *Store) GetDueWorkflowSchedules(ctx context.Context, now time.Time) ([]WorkflowScheduleRecord, error) {
	nowStr := formatTimestamp(now)
	rows, err := s.db.QueryContext(ctx, workflowScheduleSelect+` WHERE enabled = 1 AND next_run_at <= ? ORDER BY next_run_at ASC, id ASC`, nowStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []WorkflowScheduleRecord
	for rows.Next() {
		rec, err := scanWorkflowSchedule(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if list == nil {
		list = []WorkflowScheduleRecord{}
	}
	return list, nil
}

// AdvanceWorkflowSchedule updates next_run_at, last_run_at, last_run_status, and last_run_error for a schedule.
func (s *Store) AdvanceWorkflowSchedule(ctx context.Context, id string, nextRunAt string, lastStatus string, lastError string) error {
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		var errVal *string
		if strings.TrimSpace(lastError) != "" {
			trimmed := strings.TrimSpace(lastError)
			errVal = &trimmed
		}
		var statusVal *string
		if strings.TrimSpace(lastStatus) != "" {
			trimmed := strings.TrimSpace(lastStatus)
			statusVal = &trimmed
		}

		res, err := s.db.ExecContext(ctx, `
			UPDATE workflow_schedules SET
				next_run_at = ?,
				last_run_at = ?,
				last_run_status = ?,
				last_run_error = ?,
				updated_at = ?
			WHERE id = ?
		`, nextRunAt, now, statusVal, errVal, now, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return &WorkflowValidationError{Field: "id", Message: fmt.Sprintf("schedule %q not found", id)}
		}
		return nil
	})
}
