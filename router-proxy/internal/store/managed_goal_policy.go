package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	managedGoalProjectionPending     = "pending"
	managedGoalProjectionApplied     = "applied"
	managedGoalProjectionFailed      = "failed"
	managedGoalMaxProjectionAttempts = 8
)

// ManagedGoalSettings is the shared Goal Editor shape. The router stores this
// exact, validated input so a restarted managed Pi receives the same policy.
type ManagedGoalSettings struct {
	GoalLockEnabled       bool   `json:"goal_lock_enabled"`
	SnoozeEnabled         bool   `json:"snooze_enabled"`
	SnoozeDurationMS      int64  `json:"snooze_duration_ms"`
	SnoozeTurnLimit       *int64 `json:"snooze_turn_limit"`
	SnoozeBudgetCapMS     *int64 `json:"snooze_budget_cap_ms"`
	SnoozeRefillYieldMS   int64  `json:"snooze_refill_yield_ms"`
	SnoozeRefillElapsedMS int64  `json:"snooze_refill_elapsed_ms"`
}

type ManagedGoalConfig struct {
	Enabled  bool
	Goal     string
	Settings ManagedGoalSettings
}

// ManagedGoalPolicy is the sole durable managed-goal authority. The extension
// only receives its fenced projection and reports lifecycle receipts.
type ManagedGoalPolicy struct {
	AgentID, HistoryScopeID                                string
	DesiredRevision, AppliedRevision                       int64
	Config                                                 ManagedGoalConfig
	ProjectionState                                        string
	ProjectionAttempts                                     int
	NextProjectionAt                                       string
	LastErrorCode, LastErrorMessage, LastErrorAt           string
	EvaluatorState                                         string
	EvaluatorAttempts, NotDoneWakeups, LastSettledSequence int64
	NextWakeupAt, SnoozedUntil                             string
	CreatedAt, UpdatedAt                                   string
}

func DefaultManagedGoalSettings() ManagedGoalSettings {
	return ManagedGoalSettings{SnoozeEnabled: true, SnoozeDurationMS: 60_000, SnoozeRefillYieldMS: 30_000, SnoozeRefillElapsedMS: 60_000}
}

func validManagedGoalSettings(value ManagedGoalSettings) bool {
	if value.SnoozeDurationMS < 1 || value.SnoozeRefillYieldMS < 1 || value.SnoozeRefillElapsedMS < 1 {
		return false
	}
	if value.SnoozeTurnLimit != nil && (*value.SnoozeTurnLimit < 1 || *value.SnoozeTurnLimit > 20) {
		return false
	}
	return value.SnoozeBudgetCapMS == nil || *value.SnoozeBudgetCapMS > 0
}

func validManagedGoalConfig(value ManagedGoalConfig) bool {
	if !validManagedGoalSettings(value.Settings) || !utf8.ValidString(value.Goal) || len([]byte(value.Goal)) > 4_000 {
		return false
	}
	return !value.Enabled || strings.TrimSpace(value.Goal) != ""
}

func managedGoalSettingsJSON(value ManagedGoalSettings) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > 4_096 {
		return "", errors.New("managed goal settings are invalid")
	}
	return string(encoded), nil
}

func scanManagedGoalPolicy(scanner interface{ Scan(...any) error }) (*ManagedGoalPolicy, error) {
	var result ManagedGoalPolicy
	var enabled int
	var settingsJSON string
	var nextProjection, errorCode, errorMessage, errorAt, nextWakeup, snoozed sql.NullString
	err := scanner.Scan(&result.AgentID, &result.HistoryScopeID, &result.DesiredRevision, &result.AppliedRevision,
		&enabled, &result.Config.Goal, &settingsJSON, &result.ProjectionState, &result.ProjectionAttempts,
		&nextProjection, &errorCode, &errorMessage, &errorAt, &result.EvaluatorState, &result.EvaluatorAttempts,
		&result.NotDoneWakeups, &nextWakeup, &snoozed, &result.LastSettledSequence, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if enabled != 0 && enabled != 1 || !json.Valid([]byte(settingsJSON)) || json.Unmarshal([]byte(settingsJSON), &result.Config.Settings) != nil || !validManagedGoalSettings(result.Config.Settings) {
		return nil, errors.New("stored managed goal policy is invalid")
	}
	result.Config.Enabled = enabled == 1
	result.NextProjectionAt, result.LastErrorCode, result.LastErrorMessage, result.LastErrorAt = nextProjection.String, errorCode.String, errorMessage.String, errorAt.String
	result.NextWakeupAt, result.SnoozedUntil = nextWakeup.String, snoozed.String
	return &result, nil
}

const managedGoalPolicyColumns = `agent_id,history_scope_id,desired_revision,applied_revision,goal_enabled,goal_text,settings_json,projection_state,projection_attempts,next_projection_at,last_error_code,last_error_message,last_error_at,evaluator_state,evaluator_attempts,not_done_wakeups,next_wakeup_at,snoozed_until,last_settled_sequence,created_at,updated_at`

func (s *Store) ManagedGoalPolicyFor(ctx context.Context, agentID string) (*ManagedGoalPolicy, error) {
	if agentID == "" {
		return nil, errors.New("managed goal agent is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, agentID))
}

func (s *Store) managedGoalHistoryScopeTx(ctx context.Context, agentID string) (string, error) {
	var scope string
	err := s.db.QueryRowContext(ctx, `SELECT incarnation_id FROM managed_history_boundaries WHERE agent_id=?`, agentID).Scan(&scope)
	if err != nil {
		return "", err
	}
	return scope, nil
}

func (s *Store) ensureManagedGoalPolicyTx(ctx context.Context, agentID, createdAt string) error {
	// Historical migration fixtures intentionally end before 038. Check the
	// owned table first so callers can preserve those earlier schemas.
	var present int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type='table' AND name='managed_agent_policies'`).Scan(&present); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("no such table: managed_agent_policies")
		}
		return err
	}
	scope, err := s.managedGoalHistoryScopeTx(ctx, agentID)
	if err != nil {
		return err
	}
	settings, err := managedGoalSettingsJSON(DefaultManagedGoalSettings())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_agent_policies(agent_id,history_scope_id,settings_json,created_at,updated_at)
		VALUES (?,?,?,?,?) ON CONFLICT(agent_id) DO NOTHING`, agentID, scope, settings, createdAt, createdAt)
	return err
}

// UpdateManagedGoalPolicy is the atomic desired-state transition. Pi delivery
// happens only after it commits; a failed delivery cannot erase the pending
// policy or cause a stale acknowledgement to apply a superseded revision.
func (s *Store) UpdateManagedGoalPolicy(ctx context.Context, fence ManagedRuntimeFence, config ManagedGoalConfig) (*ManagedGoalPolicy, error) {
	if !validManagedRuntimeFence(fence) || !validManagedGoalConfig(config) {
		return nil, errors.New("managed goal policy is invalid")
	}
	settings, err := managedGoalSettingsJSON(config.Settings)
	if err != nil {
		return nil, err
	}
	var result *ManagedGoalPolicy
	err = s.runImmediate(ctx, func() error {
		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
			}
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if err := s.ensureManagedGoalPolicyTx(ctx, fence.AgentID, now); err != nil {
			return err
		}
		update, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET desired_revision=desired_revision+1,goal_enabled=?,goal_text=?,settings_json=?,projection_state='pending',projection_attempts=0,next_projection_at=?,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,evaluator_state=CASE WHEN ?=1 THEN 'idle' ELSE 'done' END,evaluator_attempts=0,not_done_wakeups=0,next_wakeup_at=NULL,snoozed_until=NULL,updated_at=? WHERE agent_id=?`, boolInt(config.Enabled), config.Goal, settings, now, boolInt(config.Enabled), now, fence.AgentID)
		if err != nil {
			return err
		}
		count, err := update.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
		}
		result, err = scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		return err
	})
	return result, err
}

// ConfirmManagedGoalPolicyApplied accepts only the current revision and fence.
// Old receipts are safe no-ops because the desired revision remains pending.
func (s *Store) ConfirmManagedGoalPolicyApplied(ctx context.Context, fence ManagedRuntimeFence, revision int64) (bool, error) {
	if !validManagedRuntimeFence(fence) || revision < 1 {
		return false, errors.New("managed goal policy acknowledgement is invalid")
	}
	updated := false
	err := s.runImmediate(ctx, func() error {
		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET applied_revision=desired_revision,projection_state='applied',projection_attempts=0,next_projection_at=NULL,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,updated_at=? WHERE agent_id=? AND desired_revision=? AND applied_revision<desired_revision AND projection_state='pending'`, formatTimestamp(s.clock.Now()), fence.AgentID, revision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		updated = count == 1
		return nil
	})
	return updated, err
}

func (s *Store) MarkManagedGoalPolicyProjectionFailure(ctx context.Context, fence ManagedRuntimeFence, revision int64, code, message, retryAt string) (*ManagedGoalPolicy, error) {
	if !validManagedRuntimeFence(fence) || revision < 1 || code == "" || len(code) > 128 || len(message) > 2_000 {
		return nil, errors.New("managed goal projection failure is invalid")
	}
	var result *ManagedGoalPolicy
	err := s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		args := []any{managedGoalMaxProjectionAttempts, managedGoalMaxProjectionAttempts, nullIfEmpty(retryAt), code, message, now, now, fence.AgentID, revision, managedGoalMaxProjectionAttempts}
		args = append(args, managedFenceValues(fence)...)
		update, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET projection_attempts=projection_attempts+1,projection_state=CASE WHEN projection_attempts+1>=? THEN 'failed' ELSE 'pending' END,next_projection_at=CASE WHEN projection_attempts+1>=? THEN NULL ELSE ? END,last_error_code=?,last_error_message=?,last_error_at=?,updated_at=? WHERE agent_id=? AND desired_revision=? AND applied_revision<desired_revision AND projection_state IN ('pending','failed') AND projection_attempts<? AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running')`, args...)
		if err != nil {
			return err
		}
		count, err := update.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		result, err = scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		return err
	})
	return result, err
}

func (s *Store) PendingManagedGoalPolicyForFence(ctx context.Context, fence ManagedRuntimeFence) (*ManagedGoalPolicy, error) {
	if !validManagedRuntimeFence(fence) {
		return nil, errors.New("managed goal policy fence is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=? AND desired_revision>applied_revision AND projection_state IN ('pending','failed') AND projection_attempts<? AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running')`, append([]any{fence.AgentID, managedGoalMaxProjectionAttempts}, managedFenceValues(fence)...)...))
}
