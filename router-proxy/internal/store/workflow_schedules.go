package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxWorkflowScheduleNameBytes = 256
)

// IntervalSpec defines a repeating interval.
type IntervalSpec struct {
	Value int    `json:"value"`
	Unit  string `json:"unit"` // "minutes", "hours", "days"
}

// StructuredSchedule defines the schedule cadence and timing.
type StructuredSchedule struct {
	DaysMode string        `json:"days_mode"`           // "every_day" | "specific_days"
	Days     []int         `json:"days,omitempty"`      // 0 = Sunday, 1 = Monday, ..., 6 = Saturday
	TimeMode string        `json:"time_mode"`           // "specific_times" | "repeating_interval"
	Times    []string      `json:"times,omitempty"`     // 24-hr "HH:MM" chronologically sorted
	Interval *IntervalSpec `json:"interval,omitempty"`  // interval configuration
	Timezone string        `json:"timezone,omitempty"`  // IANA timezone identifier, default "UTC"
}

// WorkflowScheduleRecord represents a persistent workflow schedule.
type WorkflowScheduleRecord struct {
	ID            string             `json:"id"`
	WorkflowID    string             `json:"workflow_id"`
	Name          string             `json:"name"`
	Enabled       bool               `json:"enabled"`
	Schedule      StructuredSchedule `json:"schedule"`
	Variables     map[string]string  `json:"variables"`
	TargetCwd     string             `json:"target_cwd"`
	ModelProvider string             `json:"model_provider"`
	ModelID       string             `json:"model_id"`
	NextRunAt     string             `json:"next_run_at"`
	LastRunAt     *string            `json:"last_run_at,omitempty"`
	LastRunStatus *string            `json:"last_run_status,omitempty"`
	LastRunError  *string            `json:"last_run_error,omitempty"`
	CreatedAt     string             `json:"created_at"`
	UpdatedAt     string             `json:"updated_at"`
}

// WorkflowScheduleRequest holds creation or update parameters for a schedule.
type WorkflowScheduleRequest struct {
	WorkflowID    string             `json:"workflow_id"`
	Name          string             `json:"name"`
	Enabled       *bool              `json:"enabled,omitempty"`
	Schedule      StructuredSchedule `json:"schedule"`
	Variables     map[string]string  `json:"variables,omitempty"`
	TargetCwd     string             `json:"target_cwd"`
	ModelProvider string             `json:"model_provider"`
	ModelID       string             `json:"model_id"`
	NextRunAt     string             `json:"next_run_at,omitempty"`
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

// ValidateStructuredSchedule checks all constraints on a StructuredSchedule.
func ValidateStructuredSchedule(spec StructuredSchedule) error {
	// 1. Timezone
	tz := strings.TrimSpace(spec.Timezone)
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return &WorkflowValidationError{Field: "schedule.timezone", Message: fmt.Sprintf("invalid timezone %q: %v", tz, err)}
		}
	}

	// 2. DaysMode
	switch spec.DaysMode {
	case "every_day":
		// days array is optional / ignored
	case "specific_days":
		if len(spec.Days) == 0 {
			return &WorkflowValidationError{Field: "schedule.days", Message: "days array must not be empty when days_mode is specific_days"}
		}
		seen := make(map[int]bool)
		for _, d := range spec.Days {
			if d < 0 || d > 6 {
				return &WorkflowValidationError{Field: "schedule.days", Message: fmt.Sprintf("invalid day %d: must be 0 (Sunday) through 6 (Saturday)", d)}
			}
			if seen[d] {
				return &WorkflowValidationError{Field: "schedule.days", Message: fmt.Sprintf("duplicate day %d in days array", d)}
			}
			seen[d] = true
		}
	default:
		return &WorkflowValidationError{Field: "schedule.days_mode", Message: "days_mode must be 'every_day' or 'specific_days'"}
	}

	// 3. TimeMode
	switch spec.TimeMode {
	case "specific_times":
		if len(spec.Times) == 0 {
			return &WorkflowValidationError{Field: "schedule.times", Message: "times array must not be empty when time_mode is specific_times"}
		}
		seenTimes := make(map[string]bool)
		for _, tStr := range spec.Times {
			tTrimmed := strings.TrimSpace(tStr)
			if err := validateTimeString(tTrimmed); err != nil {
				return &WorkflowValidationError{Field: "schedule.times", Message: err.Error()}
			}
			if seenTimes[tTrimmed] {
				return &WorkflowValidationError{Field: "schedule.times", Message: fmt.Sprintf("duplicate time %q in times array", tTrimmed)}
			}
			seenTimes[tTrimmed] = true
		}
	case "repeating_interval":
		if spec.Interval == nil {
			return &WorkflowValidationError{Field: "schedule.interval", Message: "interval must not be nil when time_mode is repeating_interval"}
		}
		if spec.Interval.Value <= 0 {
			return &WorkflowValidationError{Field: "schedule.interval.value", Message: "interval value must be a positive integer"}
		}
		switch spec.Interval.Unit {
		case "minutes", "hours", "days":
			// valid
		default:
			return &WorkflowValidationError{Field: "schedule.interval.unit", Message: "interval unit must be 'minutes', 'hours', or 'days'"}
		}
	default:
		return &WorkflowValidationError{Field: "schedule.time_mode", Message: "time_mode must be 'specific_times' or 'repeating_interval'"}
	}

	return nil
}

func validateTimeString(t string) error {
	parts := strings.Split(t, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return fmt.Errorf("invalid time format %q, must be HH:MM (24-hour)", t)
	}
	hh, err := strconv.Atoi(parts[0])
	if err != nil || hh < 0 || hh > 23 {
		return fmt.Errorf("invalid hour %q in time %q (must be 00-23)", parts[0], t)
	}
	mm, err := strconv.Atoi(parts[1])
	if err != nil || mm < 0 || mm > 59 {
		return fmt.Errorf("invalid minute %q in time %q (must be 00-59)", parts[1], t)
	}
	return nil
}

func parseTimeOfDay(t string) (hour int, minute int) {
	parts := strings.Split(t, ":")
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h, m
}

func dayMatches(day int, daysMode string, days []int) bool {
	if daysMode == "every_day" {
		return true
	}
	for _, d := range days {
		if d == day {
			return true
		}
	}
	return false
}

// NextRunAt computes the next scheduled run timestamp after `now`.
func NextRunAt(spec StructuredSchedule, now time.Time) (time.Time, error) {
	if err := ValidateStructuredSchedule(spec); err != nil {
		return time.Time{}, err
	}

	loc := time.UTC
	if strings.TrimSpace(spec.Timezone) != "" {
		loadedLoc, err := time.LoadLocation(strings.TrimSpace(spec.Timezone))
		if err != nil {
			return time.Time{}, err
		}
		loc = loadedLoc
	}

	nowInLoc := now.In(loc)

	if spec.TimeMode == "specific_times" {
		// Clean and sort times
		var times []string
		for _, t := range spec.Times {
			times = append(times, strings.TrimSpace(t))
		}
		sort.Strings(times)

		// Search up to 366 days in the future
		for dayOffset := 0; dayOffset <= 366; dayOffset++ {
			candidateDate := nowInLoc.AddDate(0, 0, dayOffset)
			weekday := int(candidateDate.Weekday()) // 0 = Sunday, 1 = Monday, ...
			if !dayMatches(weekday, spec.DaysMode, spec.Days) {
				continue
			}

			year, month, day := candidateDate.Date()
			for _, tStr := range times {
				h, m := parseTimeOfDay(tStr)
				candTime := time.Date(year, month, day, h, m, 0, 0, loc)
				if candTime.After(nowInLoc) {
					return candTime.UTC(), nil
				}
			}
		}
		return time.Time{}, fmt.Errorf("no matching schedule run found within 1 year")
	}

	// spec.TimeMode == "repeating_interval"
	var d time.Duration
	switch spec.Interval.Unit {
	case "minutes":
		d = time.Duration(spec.Interval.Value) * time.Minute
	case "hours":
		d = time.Duration(spec.Interval.Value) * time.Hour
	case "days":
		d = time.Duration(spec.Interval.Value) * 24 * time.Hour
	}

	if spec.DaysMode == "every_day" {
		// Simple forward advancement by interval
		return now.Add(d).UTC(), nil
	}

	// Specific days with repeating interval:
	// Start from now + interval. If the landing day is an eligible day, that is the next run.
	// If the landing day is not eligible, jump forward to the start of the next eligible day (midnight loc).
	cand := nowInLoc.Add(d)
	for dayOffset := 0; dayOffset <= 366; dayOffset++ {
		checkDay := cand.AddDate(0, 0, dayOffset)
		weekday := int(checkDay.Weekday())
		if dayMatches(weekday, spec.DaysMode, spec.Days) {
			if dayOffset == 0 {
				return cand.UTC(), nil
			}
			// Snap to beginning of that eligible day
			year, month, day := checkDay.Date()
			return time.Date(year, month, day, 0, 0, 0, 0, loc).UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("no matching schedule run found within 1 year")
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

	if err := ValidateStructuredSchedule(req.Schedule); err != nil {
		return err
	}

	return nil
}

func marshalSchedule(spec StructuredSchedule) (string, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return "{}", err
	}
	return string(b), nil
}

func unmarshalSchedule(raw string) StructuredSchedule {
	var res StructuredSchedule
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return res
	}
	_ = json.Unmarshal([]byte(trimmed), &res)
	return res
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
		rec           WorkflowScheduleRecord
		enabledInt    int
		schedJSON     string
		varsJSON      string
		lastRunAt     sql.NullString
		lastRunStatus sql.NullString
		lastRunError  sql.NullString
	)

	err := row.Scan(
		&rec.ID,
		&rec.WorkflowID,
		&rec.Name,
		&enabledInt,
		&schedJSON,
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
	rec.Schedule = unmarshalSchedule(schedJSON)
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
	SELECT id, workflow_id, name, enabled, schedule_json,
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

		nowTime := s.clock.Now()
		now := formatTimestamp(nowTime)
		enabled := 1
		if req.Enabled != nil && !*req.Enabled {
			enabled = 0
		}

		nextRunAt := strings.TrimSpace(req.NextRunAt)
		if nextRunAt == "" {
			computed, err := NextRunAt(req.Schedule, nowTime)
			if err != nil {
				return &WorkflowValidationError{Field: "schedule", Message: fmt.Sprintf("failed to compute next_run_at: %v", err)}
			}
			nextRunAt = formatTimestamp(computed)
		}

		schedJSON, err := marshalSchedule(req.Schedule)
		if err != nil {
			return err
		}

		varsJSON, err := marshalScheduleVariables(req.Variables)
		if err != nil {
			return err
		}

		_, err = s.db.ExecContext(ctx, `
			INSERT INTO workflow_schedules (
				id, workflow_id, name, enabled, schedule_json,
				variables_json, target_cwd, model_provider, model_id, next_run_at,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			id,
			strings.TrimSpace(req.WorkflowID),
			strings.TrimSpace(req.Name),
			enabled,
			schedJSON,
			varsJSON,
			req.TargetCwd,
			req.ModelProvider,
			req.ModelID,
			nextRunAt,
			now,
			now,
		)
		if err != nil {
			return err
		}

		rec = &WorkflowScheduleRecord{
			ID:            id,
			WorkflowID:    strings.TrimSpace(req.WorkflowID),
			Name:          strings.TrimSpace(req.Name),
			Enabled:       enabled == 1,
			Schedule:      req.Schedule,
			Variables:     req.Variables,
			TargetCwd:     req.TargetCwd,
			ModelProvider: req.ModelProvider,
			ModelID:       req.ModelID,
			NextRunAt:     nextRunAt,
			CreatedAt:     now,
			UpdatedAt:     now,
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

		nowTime := s.clock.Now()
		now := formatTimestamp(nowTime)
		enabled := 1
		if req.Enabled != nil && !*req.Enabled {
			enabled = 0
		} else if req.Enabled == nil && !existing.Enabled {
			enabled = 0
		}

		nextRunAt := strings.TrimSpace(req.NextRunAt)
		if nextRunAt == "" {
			computed, err := NextRunAt(req.Schedule, nowTime)
			if err != nil {
				return &WorkflowValidationError{Field: "schedule", Message: fmt.Sprintf("failed to compute next_run_at: %v", err)}
			}
			nextRunAt = formatTimestamp(computed)
		}

		schedJSON, err := marshalSchedule(req.Schedule)
		if err != nil {
			return err
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
				schedule_json = ?,
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
			schedJSON,
			varsJSON,
			req.TargetCwd,
			req.ModelProvider,
			req.ModelID,
			nextRunAt,
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
