package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ManagedAgentMonitor represents an automated System 1 monitor attached to a managed agent.
type ManagedAgentMonitor struct {
	ID                    string          `json:"id" db:"id"`
	AgentID               string          `json:"agent_id" db:"agent_id"`
	WorkflowStateID       *string         `json:"workflow_state_id,omitempty" db:"workflow_state_id"`
	Name                  string          `json:"name" db:"name"`
	Description           string          `json:"description" db:"description"`
	Enabled               bool            `json:"enabled" db:"enabled"`
	IntervalSeconds       int             `json:"interval_seconds" db:"interval_seconds"`
	NextEvalAt            string          `json:"next_eval_at" db:"next_eval_at"`
	LastEvaluatedAt       *string         `json:"last_evaluated_at,omitempty" db:"last_evaluated_at"`
	CooldownSeconds       int             `json:"cooldown_seconds" db:"cooldown_seconds"`
	LastFiredAt           *string         `json:"last_fired_at,omitempty" db:"last_fired_at"`
	ConsecutiveAlerts     int             `json:"consecutive_alerts" db:"consecutive_alerts"`
	MaxAlerts             int             `json:"max_alerts" db:"max_alerts"`
	TrailingMessagesCount int             `json:"trailing_messages_count" db:"trailing_messages_count"`
	QuestionType          string          `json:"question_type" db:"question_type"`
	QuestionInstructions  string          `json:"question_instructions" db:"question_instructions"`
	QuestionCriteria      json.RawMessage `json:"question_criteria" db:"question_criteria_json"`
	QuestionOptions       json.RawMessage `json:"question_options" db:"question_options_json"`
	ConditionOperator     string          `json:"condition_operator" db:"condition_operator"`
	ConditionThreshold    string          `json:"condition_threshold" db:"condition_threshold"`
	NudgeTemplate         string          `json:"nudge_template" db:"nudge_template"`
	FallbackMode          string          `json:"fallback_mode" db:"fallback_mode"`
	CreatedAt             string          `json:"created_at" db:"created_at"`
	UpdatedAt             string          `json:"updated_at" db:"updated_at"`
}

var ErrManagedAgentMonitorNotFound = errors.New("managed agent monitor not found")

func normalizeJSON(raw json.RawMessage, defaultVal string) string {
	str := strings.TrimSpace(string(raw))
	if str == "" || str == "null" {
		return defaultVal
	}
	return str
}

// CreateManagedAgentMonitor inserts a new monitor into managed_agent_monitors.
func (s *Store) CreateManagedAgentMonitor(ctx context.Context, monitor *ManagedAgentMonitor) error {
	if monitor == nil {
		return errors.New("monitor cannot be nil")
	}
	if monitor.ID == "" {
		id, err := s.ids.Next()
		if err != nil {
			return err
		}
		monitor.ID = id
	}
	now := formatTimestamp(s.clock.Now())
	if monitor.CreatedAt == "" {
		monitor.CreatedAt = now
	}
	if monitor.UpdatedAt == "" {
		monitor.UpdatedAt = now
	}
	if monitor.NextEvalAt == "" {
		monitor.NextEvalAt = now
	}
	if monitor.IntervalSeconds <= 0 {
		monitor.IntervalSeconds = 30
	}
	if monitor.CooldownSeconds <= 0 {
		monitor.CooldownSeconds = 120
	}
	if monitor.TrailingMessagesCount <= 0 {
		monitor.TrailingMessagesCount = 6
	}
	if monitor.QuestionType == "" {
		monitor.QuestionType = "noul"
	}
	if monitor.ConditionOperator == "" {
		monitor.ConditionOperator = "gte"
	}
	if monitor.ConditionThreshold == "" {
		monitor.ConditionThreshold = "0.8"
	}
	if monitor.FallbackMode == "" {
		monitor.FallbackMode = "skip"
	}

	enabledInt := 0
	if monitor.Enabled {
		enabledInt = 1
	}

	criteriaJSON := normalizeJSON(monitor.QuestionCriteria, "{}")
	optionsJSON := normalizeJSON(monitor.QuestionOptions, "[]")

	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `
INSERT INTO managed_agent_monitors (
  id, agent_id, workflow_state_id, name, description, enabled,
  interval_seconds, next_eval_at, last_evaluated_at, cooldown_seconds,
  last_fired_at, consecutive_alerts, max_alerts, trailing_messages_count,
  question_type, question_instructions, question_criteria_json, question_options_json,
  condition_operator, condition_threshold, nudge_template, fallback_mode,
  created_at, updated_at
) VALUES (
  ?, ?, ?, ?, ?, ?,
  ?, ?, ?, ?,
  ?, ?, ?, ?,
  ?, ?, ?, ?,
  ?, ?, ?, ?,
  ?, ?
)`,
			monitor.ID,
			monitor.AgentID,
			monitor.WorkflowStateID,
			monitor.Name,
			monitor.Description,
			enabledInt,
			monitor.IntervalSeconds,
			monitor.NextEvalAt,
			monitor.LastEvaluatedAt,
			monitor.CooldownSeconds,
			monitor.LastFiredAt,
			monitor.ConsecutiveAlerts,
			monitor.MaxAlerts,
			monitor.TrailingMessagesCount,
			monitor.QuestionType,
			monitor.QuestionInstructions,
			criteriaJSON,
			optionsJSON,
			monitor.ConditionOperator,
			monitor.ConditionThreshold,
			monitor.NudgeTemplate,
			monitor.FallbackMode,
			monitor.CreatedAt,
			monitor.UpdatedAt,
		)
		return err
	})
}

func scanMonitor(row interface {
	Scan(dest ...interface{}) error
}) (*ManagedAgentMonitor, error) {
	var m ManagedAgentMonitor
	var workflowStateID, lastEvaluatedAt, lastFiredAt sql.NullString
	var criteriaStr, optionsStr string
	var enabledInt int

	err := row.Scan(
		&m.ID,
		&m.AgentID,
		&workflowStateID,
		&m.Name,
		&m.Description,
		&enabledInt,
		&m.IntervalSeconds,
		&m.NextEvalAt,
		&lastEvaluatedAt,
		&m.CooldownSeconds,
		&lastFiredAt,
		&m.ConsecutiveAlerts,
		&m.MaxAlerts,
		&m.TrailingMessagesCount,
		&m.QuestionType,
		&m.QuestionInstructions,
		&criteriaStr,
		&optionsStr,
		&m.ConditionOperator,
		&m.ConditionThreshold,
		&m.NudgeTemplate,
		&m.FallbackMode,
		&m.CreatedAt,
		&m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	m.Enabled = (enabledInt == 1)
	if workflowStateID.Valid {
		m.WorkflowStateID = &workflowStateID.String
	}
	if lastEvaluatedAt.Valid {
		m.LastEvaluatedAt = &lastEvaluatedAt.String
	}
	if lastFiredAt.Valid {
		m.LastFiredAt = &lastFiredAt.String
	}
	m.QuestionCriteria = json.RawMessage(criteriaStr)
	m.QuestionOptions = json.RawMessage(optionsStr)

	return &m, nil
}

const monitorColumns = `
  id, agent_id, workflow_state_id, name, description, enabled,
  interval_seconds, next_eval_at, last_evaluated_at, cooldown_seconds,
  last_fired_at, consecutive_alerts, max_alerts, trailing_messages_count,
  question_type, question_instructions, question_criteria_json, question_options_json,
  condition_operator, condition_threshold, nudge_template, fallback_mode,
  created_at, updated_at
`

// GetManagedAgentMonitor retrieves a specific monitor by agentID and monitorID.
func (s *Store) GetManagedAgentMonitor(ctx context.Context, agentID, monitorID string) (*ManagedAgentMonitor, error) {
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM managed_agent_monitors WHERE agent_id = ? AND id = ?`, monitorColumns), agentID, monitorID)
	m, err := scanMonitor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ListManagedAgentMonitors lists all monitors for an agent.
func (s *Store) ListManagedAgentMonitors(ctx context.Context, agentID string) ([]*ManagedAgentMonitor, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM managed_agent_monitors WHERE agent_id = ? ORDER BY created_at ASC, id ASC`, monitorColumns), agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var monitors []*ManagedAgentMonitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		monitors = append(monitors, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if monitors == nil {
		monitors = []*ManagedAgentMonitor{}
	}
	return monitors, nil
}

// UpdateManagedAgentMonitor updates an existing monitor record.
func (s *Store) UpdateManagedAgentMonitor(ctx context.Context, monitor *ManagedAgentMonitor) error {
	if monitor == nil {
		return errors.New("monitor cannot be nil")
	}
	now := formatTimestamp(s.clock.Now())
	monitor.UpdatedAt = now

	enabledInt := 0
	if monitor.Enabled {
		enabledInt = 1
	}

	criteriaJSON := normalizeJSON(monitor.QuestionCriteria, "{}")
	optionsJSON := normalizeJSON(monitor.QuestionOptions, "[]")

	return s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `
UPDATE managed_agent_monitors SET
  workflow_state_id = ?,
  name = ?,
  description = ?,
  enabled = ?,
  interval_seconds = ?,
  next_eval_at = ?,
  last_evaluated_at = ?,
  cooldown_seconds = ?,
  last_fired_at = ?,
  consecutive_alerts = ?,
  max_alerts = ?,
  trailing_messages_count = ?,
  question_type = ?,
  question_instructions = ?,
  question_criteria_json = ?,
  question_options_json = ?,
  condition_operator = ?,
  condition_threshold = ?,
  nudge_template = ?,
  fallback_mode = ?,
  updated_at = ?
WHERE agent_id = ? AND id = ?
`,
			monitor.WorkflowStateID,
			monitor.Name,
			monitor.Description,
			enabledInt,
			monitor.IntervalSeconds,
			monitor.NextEvalAt,
			monitor.LastEvaluatedAt,
			monitor.CooldownSeconds,
			monitor.LastFiredAt,
			monitor.ConsecutiveAlerts,
			monitor.MaxAlerts,
			monitor.TrailingMessagesCount,
			monitor.QuestionType,
			monitor.QuestionInstructions,
			criteriaJSON,
			optionsJSON,
			monitor.ConditionOperator,
			monitor.ConditionThreshold,
			monitor.NudgeTemplate,
			monitor.FallbackMode,
			monitor.UpdatedAt,
			monitor.AgentID,
			monitor.ID,
		)
		if err != nil {
			return err
		}
		rowsAff, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAff == 0 {
			return ErrManagedAgentMonitorNotFound
		}
		return nil
	})
}

// DeleteManagedAgentMonitor deletes a monitor by agentID and monitorID.
func (s *Store) DeleteManagedAgentMonitor(ctx context.Context, agentID, monitorID string) error {
	return s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `DELETE FROM managed_agent_monitors WHERE agent_id = ? AND id = ?`, agentID, monitorID)
		if err != nil {
			return err
		}
		rowsAff, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAff == 0 {
			return ErrManagedAgentMonitorNotFound
		}
		return nil
	})
}

// QueryDueManagedAgentMonitors finds enabled monitors whose next_eval_at <= now, ordered by next_eval_at ASC.
func (s *Store) QueryDueManagedAgentMonitors(ctx context.Context, now time.Time, limit int) ([]*ManagedAgentMonitor, error) {
	if limit <= 0 {
		limit = 50
	}
	nowStr := formatTimestamp(now)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT %s FROM managed_agent_monitors
WHERE enabled = 1 AND next_eval_at <= ?
ORDER BY next_eval_at ASC, id ASC
LIMIT ?
`, monitorColumns), nowStr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var monitors []*ManagedAgentMonitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		monitors = append(monitors, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if monitors == nil {
		monitors = []*ManagedAgentMonitor{}
	}
	return monitors, nil
}

// RecordManagedAgentMonitorEvaluated records that an evaluation occurred (without firing an alert),
// resetting consecutive_alerts to 0, updating last_evaluated_at and scheduling next_eval_at.
func (s *Store) RecordManagedAgentMonitorEvaluated(ctx context.Context, monitorID string, evaluatedAt, nextEvalAt time.Time) error {
	evalStr := formatTimestamp(evaluatedAt)
	nextStr := formatTimestamp(nextEvalAt)
	nowStr := formatTimestamp(s.clock.Now())

	return s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `
UPDATE managed_agent_monitors SET
  last_evaluated_at = ?,
  next_eval_at = ?,
  consecutive_alerts = 0,
  updated_at = ?
WHERE id = ?
`, evalStr, nextStr, nowStr, monitorID)
		if err != nil {
			return err
		}
		rowsAff, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAff == 0 {
			return ErrManagedAgentMonitorNotFound
		}
		return nil
	})
}

// RecordManagedAgentMonitorFired records that an evaluation triggered an alert,
// incrementing consecutive_alerts, updating last_evaluated_at, last_fired_at, and scheduling next_eval_at.
func (s *Store) RecordManagedAgentMonitorFired(ctx context.Context, monitorID string, firedAt, nextEvalAt time.Time) error {
	firedStr := formatTimestamp(firedAt)
	nextStr := formatTimestamp(nextEvalAt)
	nowStr := formatTimestamp(s.clock.Now())

	return s.runImmediate(ctx, func() error {
		res, err := s.db.ExecContext(ctx, `
UPDATE managed_agent_monitors SET
  last_evaluated_at = ?,
  last_fired_at = ?,
  next_eval_at = ?,
  consecutive_alerts = consecutive_alerts + 1,
  updated_at = ?
WHERE id = ?
`, firedStr, firedStr, nextStr, nowStr, monitorID)
		if err != nil {
			return err
		}
		rowsAff, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAff == 0 {
			return ErrManagedAgentMonitorNotFound
		}
		return nil
	})
}

// CleanUpManagedAgentWorkflowMonitors removes all monitors associated with a workflow state for an agent.
func (s *Store) CleanUpManagedAgentWorkflowMonitors(ctx context.Context, agentID string) error {
	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `DELETE FROM managed_agent_monitors WHERE agent_id = ? AND workflow_state_id IS NOT NULL`, agentID)
		return err
	})
}
