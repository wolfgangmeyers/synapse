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

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
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
	AgentID, HistoryScopeID                                                    string
	DesiredRevision, AppliedRevision                                           int64
	Config                                                                     ManagedGoalConfig
	ProjectionState                                                            string
	ProjectionAttempts                                                         int
	NextProjectionAt                                                           string
	LastErrorCode, LastErrorMessage, LastErrorAt                               string
	EvaluatorState, LastOutcome                                                string
	EvaluatorAttempts, NotDoneWakeups, RearmActivityCount, LastSettledSequence int64
	NextWakeupAt, SnoozedUntil, EvaluatorLeaseUntil                            string
	EvaluatorID, EvaluatorReason, WakeupMessageID                              string
	EvaluatorGoalRevision, EvaluatorSettledSequence                            int64
	EvaluatorPiSessionID, EvaluatorProcessStartMarker                          string
	EvaluatorSessionEpoch, EvaluatorProcessPID                                 int64
	EvaluatorProcessIncarnation, EvaluatorRuntimeGeneration                    string
	SnoozePiSessionID, SnoozeProcessStartMarker                                string
	SnoozeSessionEpoch, SnoozeProcessPID                                       int64
	SnoozeProcessIncarnation, SnoozeRuntimeGeneration                          string
	CreatedAt, UpdatedAt                                                       string
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
	var nextProjection, errorCode, errorMessage, errorAt, evaluatorLease, lastOutcome, nextWakeup, snoozed, evaluatorID, evaluatorPiSessionID, evaluatorProcessStartMarker, evaluatorProcessIncarnation, evaluatorRuntimeGeneration, evaluatorReason, wakeupMessageID, snoozePiSessionID, snoozeProcessStartMarker, snoozeProcessIncarnation, snoozeRuntimeGeneration sql.NullString
	var evaluatorGoalRevision, evaluatorSettledSequence, evaluatorSessionEpoch, evaluatorProcessPID, snoozeSessionEpoch, snoozeProcessPID sql.NullInt64
	err := scanner.Scan(&result.AgentID, &result.HistoryScopeID, &result.DesiredRevision, &result.AppliedRevision,
		&enabled, &result.Config.Goal, &settingsJSON, &result.ProjectionState, &result.ProjectionAttempts,
		&nextProjection, &errorCode, &errorMessage, &errorAt, &result.EvaluatorState, &evaluatorLease, &lastOutcome, &result.EvaluatorAttempts,
		&result.NotDoneWakeups, &result.RearmActivityCount, &nextWakeup, &snoozed, &result.LastSettledSequence, &evaluatorID, &evaluatorGoalRevision, &evaluatorSettledSequence, &evaluatorPiSessionID, &evaluatorSessionEpoch, &evaluatorProcessPID, &evaluatorProcessStartMarker, &evaluatorProcessIncarnation, &evaluatorRuntimeGeneration, &evaluatorReason, &wakeupMessageID, &snoozePiSessionID, &snoozeSessionEpoch, &snoozeProcessPID, &snoozeProcessStartMarker, &snoozeProcessIncarnation, &snoozeRuntimeGeneration, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if enabled != 0 && enabled != 1 || !json.Valid([]byte(settingsJSON)) || json.Unmarshal([]byte(settingsJSON), &result.Config.Settings) != nil || !validManagedGoalSettings(result.Config.Settings) {
		return nil, errors.New("stored managed goal policy is invalid")
	}
	result.Config.Enabled = enabled == 1
	result.NextProjectionAt, result.LastErrorCode, result.LastErrorMessage, result.LastErrorAt = nextProjection.String, errorCode.String, errorMessage.String, errorAt.String
	result.EvaluatorLeaseUntil, result.LastOutcome = evaluatorLease.String, lastOutcome.String
	result.NextWakeupAt, result.SnoozedUntil = nextWakeup.String, snoozed.String
	result.EvaluatorID, result.EvaluatorPiSessionID, result.EvaluatorProcessStartMarker = evaluatorID.String, evaluatorPiSessionID.String, evaluatorProcessStartMarker.String
	result.EvaluatorProcessIncarnation, result.EvaluatorRuntimeGeneration = evaluatorProcessIncarnation.String, evaluatorRuntimeGeneration.String
	result.EvaluatorGoalRevision, result.EvaluatorSettledSequence = evaluatorGoalRevision.Int64, evaluatorSettledSequence.Int64
	result.EvaluatorSessionEpoch, result.EvaluatorProcessPID = evaluatorSessionEpoch.Int64, evaluatorProcessPID.Int64
	result.EvaluatorReason, result.WakeupMessageID = evaluatorReason.String, wakeupMessageID.String
	result.SnoozePiSessionID, result.SnoozeProcessStartMarker = snoozePiSessionID.String, snoozeProcessStartMarker.String
	result.SnoozeProcessIncarnation, result.SnoozeRuntimeGeneration = snoozeProcessIncarnation.String, snoozeRuntimeGeneration.String
	result.SnoozeSessionEpoch, result.SnoozeProcessPID = snoozeSessionEpoch.Int64, snoozeProcessPID.Int64
	return &result, nil
}

const managedGoalPolicyColumns = `agent_id,history_scope_id,desired_revision,applied_revision,goal_enabled,goal_text,settings_json,projection_state,projection_attempts,next_projection_at,last_error_code,last_error_message,last_error_at,evaluator_state,evaluator_lease_until,last_outcome,evaluator_attempts,not_done_wakeups,rearm_activity_count,next_wakeup_at,snoozed_until,last_settled_sequence,evaluator_id,evaluator_goal_revision,evaluator_settled_sequence,evaluator_pi_session_id,evaluator_session_epoch,evaluator_process_pid,evaluator_process_start_marker,evaluator_process_incarnation,evaluator_runtime_generation,evaluator_reason,wakeup_message_id,snooze_pi_session_id,snooze_session_epoch,snooze_process_pid,snooze_process_start_marker,snooze_process_incarnation,snooze_runtime_generation,created_at,updated_at`

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
		update, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET desired_revision=desired_revision+1,goal_enabled=?,goal_text=?,settings_json=?,projection_state='pending',projection_attempts=0,next_projection_at=?,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,evaluator_state=CASE WHEN ?=1 THEN 'idle' ELSE 'done' END,last_outcome=NULL,evaluator_attempts=0,not_done_wakeups=0,rearm_activity_count=0,next_wakeup_at=NULL,snoozed_until=NULL,snooze_pi_session_id=NULL,snooze_session_epoch=NULL,snooze_process_pid=NULL,snooze_process_start_marker=NULL,snooze_process_incarnation=NULL,snooze_runtime_generation=NULL,evaluator_lease_until=NULL,evaluator_id=NULL,evaluator_goal_revision=NULL,evaluator_settled_sequence=NULL,evaluator_pi_session_id=NULL,evaluator_session_epoch=NULL,evaluator_process_pid=NULL,evaluator_process_start_marker=NULL,evaluator_process_incarnation=NULL,evaluator_runtime_generation=NULL,evaluator_reason=NULL,wakeup_message_id=NULL,updated_at=? WHERE agent_id=?`, boolInt(config.Enabled), config.Goal, settings, now, boolInt(config.Enabled), now, fence.AgentID)
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
		if _, err = s.db.ExecContext(ctx, `DELETE FROM managed_goal_activity WHERE agent_id=?`, fence.AgentID); err != nil {
			return err
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

const (
	ManagedGoalEvaluationCompleted = "COMPLETED"
	ManagedGoalEvaluationNotDone   = "NOT_DONE"
	managedGoalRearmEvents         = 3
	managedGoalMaxNotDoneWakeups   = 1000
	managedGoalEvaluationLease     = 5 * time.Minute
)

// ClaimManagedGoalEvaluation admits one evaluator run from one native settled
// boundary. The gate reads the parent compaction state and current child
// snapshot in the same transaction, so the evaluator cannot wake a parent
// while the router knows it is still doing child work or compaction.
func (s *Store) ClaimManagedGoalEvaluation(ctx context.Context, fence ManagedRuntimeFence, settledSequence int64) (*ManagedGoalPolicy, bool, error) {
	if !validManagedRuntimeFence(fence) || settledSequence < 1 {
		return nil, false, errors.New("managed goal evaluation claim is invalid")
	}
	var policy *ManagedGoalPolicy
	claimed := false
	err := s.runImmediate(ctx, func() error {
		now := s.clock.Now()
		nowText := formatTimestamp(now)
		current, err := scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.EvaluatorState == "evaluating" {
			// A crash can leave an admitted evaluator without a completion receipt.
			// The lease is durable, so a later settled boundary can recover it safely.
			args := append([]any{nowText, fence.AgentID, nowText}, managedFenceValues(fence)...)
			result, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET evaluator_state='idle',evaluator_lease_until=NULL,updated_at=? WHERE agent_id=? AND evaluator_state='evaluating' AND (evaluator_lease_until IS NULL OR evaluator_lease_until<=?) AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running')`, args...)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return nil
			}
			current, err = scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
			if err != nil {
				return err
			}
		}
		if !current.Config.Enabled || current.ProjectionState != managedGoalProjectionApplied || current.DesiredRevision != current.AppliedRevision || current.EvaluatorState != "idle" || settledSequence <= current.LastSettledSequence {
			return nil
		}
		if current.NotDoneWakeups >= managedGoalMaxNotDoneWakeups {
			args := append([]any{nowText, fence.AgentID, managedGoalMaxNotDoneWakeups}, managedFenceValues(fence)...)
			_, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET evaluator_state='scheduled',updated_at=? WHERE agent_id=? AND evaluator_state='idle' AND not_done_wakeups>=? AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running')`, args...)
			return err
		}
		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running' AND activity_state='idle'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		var compacting int
		err = s.db.QueryRowContext(ctx, `SELECT compacting FROM managed_parent_compaction_state WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&compacting)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if compacting != 0 {
			return nil
		}
		var snapshot []byte
		err = s.db.QueryRowContext(ctx, `SELECT snapshot_canonical FROM managed_readiness_sources WHERE `+readinessSourceFenceWhere(), readinessSourceFenceArgs(fence)...).Scan(&snapshot)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if managedGoalSnapshotHasActiveChild(snapshot) {
			return nil
		}
		evaluationID, err := s.ids.Next()
		if err != nil {
			return err
		}
		leaseUntil := formatTimestamp(now.Add(managedGoalEvaluationLease))
		args := append([]any{leaseUntil, settledSequence, evaluationID, current.DesiredRevision, settledSequence, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, nowText, fence.AgentID, settledSequence, managedGoalMaxNotDoneWakeups}, managedFenceValues(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET evaluator_state='evaluating',evaluator_lease_until=?,evaluator_attempts=evaluator_attempts+1,last_settled_sequence=?,evaluator_id=?,evaluator_goal_revision=?,evaluator_settled_sequence=?,evaluator_pi_session_id=?,evaluator_session_epoch=?,evaluator_process_pid=?,evaluator_process_start_marker=?,evaluator_process_incarnation=?,evaluator_runtime_generation=?,evaluator_reason=NULL,wakeup_message_id=NULL,updated_at=? WHERE agent_id=? AND evaluator_state='idle' AND last_settled_sequence<? AND not_done_wakeups<? AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running' AND activity_state='idle')`, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return nil
		}
		policy, err = scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		claimed = err == nil
		return err
	})
	return policy, claimed, err
}

// RecoverManagedGoalEvaluationForFence releases an evaluator owned by a prior
// process after the router has made a new runtime ready. Its old result stays
// fenced and is discarded; the next native settled boundary may claim again.
func (s *Store) RecoverManagedGoalEvaluationForFence(ctx context.Context, fence ManagedRuntimeFence) (bool, error) {
	if !validManagedRuntimeFence(fence) {
		return false, errors.New("managed goal recovery fence is invalid")
	}
	changed := false
	err := s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		args := append([]any{now, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration}, managedFenceValues(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET evaluator_state='idle',evaluator_lease_until=NULL,evaluator_reason='evaluation interrupted',updated_at=?
			WHERE agent_id=? AND evaluator_state='evaluating' AND evaluator_id IS NOT NULL
			AND NOT (evaluator_pi_session_id=? AND evaluator_session_epoch=? AND evaluator_process_pid=? AND evaluator_process_start_marker=? AND evaluator_process_incarnation=? AND evaluator_runtime_generation=?)
			AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running')`, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = count == 1
		args = append([]any{now, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration}, managedFenceValues(fence)...)
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET evaluator_state='idle',next_wakeup_at=NULL,snoozed_until=NULL,wakeup_message_id=NULL,snooze_pi_session_id=NULL,snooze_session_epoch=NULL,snooze_process_pid=NULL,snooze_process_start_marker=NULL,snooze_process_incarnation=NULL,snooze_runtime_generation=NULL,updated_at=?
			WHERE agent_id=? AND evaluator_state='snoozed'
			AND NOT (snooze_pi_session_id=? AND snooze_session_epoch=? AND snooze_process_pid=? AND snooze_process_start_marker=? AND snooze_process_incarnation=? AND snooze_runtime_generation=?)
			AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running')`, args...)
		if err != nil {
			return err
		}
		count, err = result.RowsAffected()
		if err != nil {
			return err
		}
		changed = changed || count == 1
		_, err = s.db.ExecContext(ctx, `DELETE FROM managed_goal_activity WHERE agent_id=? AND NOT (pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=?)`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration)
		return err
	})
	return changed, err
}

func ManagedGoalSnoozeFenceMatches(policy *ManagedGoalPolicy, fence ManagedRuntimeFence) bool {
	return policy != nil && policy.SnoozePiSessionID == fence.PiSessionID && policy.SnoozeSessionEpoch == fence.SessionEpoch && policy.SnoozeProcessPID == int64(fence.ProcessPID) && policy.SnoozeProcessStartMarker == fence.ProcessStartMarker && policy.SnoozeProcessIncarnation == fence.ProcessIncarnation && policy.SnoozeRuntimeGeneration == fence.RuntimeGeneration
}

func managedGoalSnapshotHasActiveChild(canonical []byte) bool {
	var snapshot struct {
		Runs []struct {
			LifecycleState string `json:"lifecycle_state"`
		} `json:"runs"`
	}
	if len(canonical) == 0 || json.Unmarshal(canonical, &snapshot) != nil {
		// A missing or unreadable snapshot is not evidence that child work ended.
		return true
	}
	for _, run := range snapshot.Runs {
		if run.LifecycleState == "queued" || run.LifecycleState == "running" {
			return true
		}
	}
	return false
}

// CompleteManagedGoalEvaluation records a verdict under the evaluation ID
// claimed at the settled boundary. NOT_DONE reserves its message in this same
// transaction, so a replacement cannot claim or receive that continuation.
func (s *Store) CompleteManagedGoalEvaluation(ctx context.Context, fence ManagedRuntimeFence, settledSequence int64, outcome string) (*ManagedGoalPolicy, bool, error) {
	current, err := s.ManagedGoalPolicyFor(ctx, fence.AgentID)
	if err != nil {
		return nil, false, err
	}
	wakeupMessageID := ""
	if outcome == ManagedGoalEvaluationNotDone {
		wakeupMessageID, err = s.ids.Next()
		if err != nil {
			return nil, false, err
		}
	}
	return s.CompleteManagedGoalEvaluationWithWakeup(ctx, fence, current.EvaluatorID, settledSequence, outcome, "", wakeupMessageID)
}

func (s *Store) CompleteManagedGoalEvaluationWithWakeup(ctx context.Context, fence ManagedRuntimeFence, evaluationID string, settledSequence int64, outcome, reason, wakeupMessageID string) (*ManagedGoalPolicy, bool, error) {
	if !validManagedRuntimeFence(fence) || settledSequence < 1 || (outcome != ManagedGoalEvaluationCompleted && outcome != ManagedGoalEvaluationNotDone) || len(reason) > 512 || (outcome == ManagedGoalEvaluationNotDone && wakeupMessageID == "") {
		return nil, false, errors.New("managed goal evaluation completion is invalid")
	}
	var policy *ManagedGoalPolicy
	changed := false
	err := s.runImmediate(ctx, func() error {
		nowTime := s.clock.Now()
		now := formatTimestamp(nowTime)
		state := "done"
		if outcome == ManagedGoalEvaluationNotDone {
			state = "idle"
		}
		// An empty ID preserves the prior store seam while production callers bind
		// the completion to the UUID generated by ClaimManagedGoalEvaluation.
		idPredicate := ""
		args := []any{outcome, managedGoalMaxNotDoneWakeups, state, outcome, outcome, reason, nullIfEmpty(wakeupMessageID), now, fence.AgentID, settledSequence}
		if evaluationID != "" {
			idPredicate = " AND evaluator_id=?"
			args = append(args, evaluationID)
		}
		args = append(args, managedFenceValues(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET evaluator_state=CASE WHEN ?='NOT_DONE' AND not_done_wakeups+1>=? THEN 'scheduled' ELSE ? END,last_outcome=?,not_done_wakeups=not_done_wakeups+CASE WHEN ?='NOT_DONE' THEN 1 ELSE 0 END,rearm_activity_count=0,next_wakeup_at=NULL,snoozed_until=NULL,snooze_pi_session_id=NULL,snooze_session_epoch=NULL,snooze_process_pid=NULL,snooze_process_start_marker=NULL,snooze_process_incarnation=NULL,snooze_runtime_generation=NULL,evaluator_lease_until=NULL,evaluator_reason=?,wakeup_message_id=?,updated_at=? WHERE agent_id=? AND evaluator_state='evaluating' AND last_settled_sequence=?`+idPredicate+` AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running')`, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return nil
		}
		policy, err = scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		if err != nil {
			return err
		}
		if outcome == ManagedGoalEvaluationNotDone {
			// The verdict, ordinary source-marked row, immutable recipient snapshot,
			// and exact delivery reservation commit together. A crash leaves either
			// the whole wakeup or none of it; a replacement cannot retarget it.
			if err = s.reserveManagedGoalWakeupTx(ctx, fence, evaluationID, wakeupMessageID, policy.Config.Goal, nowTime); err != nil {
				return err
			}
		}
		changed = err == nil
		return err
	})
	return policy, changed, err
}

// reserveManagedGoalWakeupTx creates the ordinary wakeup and its immutable
// direct-delivery fence in the same transaction that makes it available.
func (s *Store) reserveManagedGoalWakeupTx(ctx context.Context, fence ManagedRuntimeFence, evaluationID, messageID, goal string, now time.Time) error {
	expires, err := protocol.TimestampAfter(formatTimestamp(now), time.Hour)
	if err != nil {
		return err
	}
	incarnation := "goal-wakeup-v1:" + messageID
	body := fmt.Sprintf("NOT_DONE: Review %s and take the next concrete action now.", strings.TrimSpace(goal))
	if _, err = s.db.ExecContext(ctx, `INSERT INTO agents(agent_id,first_seen_at) VALUES (?,?) ON CONFLICT(agent_id) DO NOTHING`, fence.AgentID, now); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO messages(id,sender,recipient,body,created_at,expires_at,message_incarnation_id,source_kind,state) VALUES (?,?,?,?,?,?,?,?, 'queued')`, messageID, "router", fence.AgentID, body, now, expires, incarnation, "system"); err != nil {
		return err
	}
	var sourceKind, owner string
	if err = s.db.QueryRowContext(ctx, `SELECT source_kind,owner_identity FROM recipient_sources WHERE recipient_name=? AND source_kind='managed' AND owner_identity=? AND state='active'`, fence.AgentID, fence.AgentID).Scan(&sourceKind, &owner); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO message_target_snapshots(snapshot_id,message_id,message_incarnation_id,recipient_name,source_kind,owner_identity,created_at) VALUES (?,?,?,?,?,?,?)`, "target-v1:"+incarnation, messageID, incarnation, fence.AgentID, sourceKind, owner, now); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_goal_wakeups(message_id,evaluation_id,agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, messageID, evaluationID, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, now)
	return err
}

// SnoozeManagedGoalPolicy keeps its wakeup in router SQLite so a process
// replacement can re-register it without trusting a local extension timer.
func (s *Store) SnoozeManagedGoalPolicy(ctx context.Context, fence ManagedRuntimeFence) (*ManagedGoalPolicy, bool, error) {
	if !validManagedRuntimeFence(fence) {
		return nil, false, errors.New("managed goal snooze is invalid")
	}
	var policy *ManagedGoalPolicy
	changed := false
	err := s.runImmediate(ctx, func() error {
		current, err := scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !current.Config.Enabled || !current.Config.Settings.SnoozeEnabled || current.EvaluatorState == "done" || current.ProjectionState != managedGoalProjectionApplied || current.DesiredRevision != current.AppliedRevision {
			return nil
		}
		now := s.clock.Now()
		wakeupMessageID, idErr := s.ids.Next()
		if idErr != nil {
			return idErr
		}
		wakeAt := formatTimestamp(now.Add(time.Duration(current.Config.Settings.SnoozeDurationMS) * time.Millisecond))
		args := append([]any{wakeAt, wakeAt, wakeupMessageID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, formatTimestamp(now), fence.AgentID}, managedFenceValues(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET evaluator_state='snoozed',next_wakeup_at=?,snoozed_until=?,wakeup_message_id=?,snooze_pi_session_id=?,snooze_session_epoch=?,snooze_process_pid=?,snooze_process_start_marker=?,snooze_process_incarnation=?,snooze_runtime_generation=?,evaluator_lease_until=NULL,updated_at=? WHERE agent_id=? AND evaluator_state='idle' AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running' AND activity_state='idle')`, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return err
		}
		policy, err = scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		changed = err == nil
		return err
	})
	return policy, changed, err
}

// WakeSnoozedManagedGoalPolicy consumes only a due snooze for the current
// fence. A stale timer cannot wake a reset, terminated, or reconfigured run.
func (s *Store) WakeSnoozedManagedGoalPolicy(ctx context.Context, fence ManagedRuntimeFence) (*ManagedGoalPolicy, bool, error) {
	if !validManagedRuntimeFence(fence) {
		return nil, false, errors.New("managed goal wakeup is invalid")
	}
	var policy *ManagedGoalPolicy
	changed := false
	err := s.runImmediate(ctx, func() error {
		now := s.clock.Now()
		nowText := formatTimestamp(now)
		current, err := scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || current.EvaluatorState != "snoozed" || current.SnoozedUntil == "" || current.WakeupMessageID == "" || !ManagedGoalSnoozeFenceMatches(current, fence) {
			return err
		}
		var present int
		if err = s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running' AND activity_state='idle'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		var compacting int
		err = s.db.QueryRowContext(ctx, `SELECT compacting FROM managed_parent_compaction_state WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&compacting)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if compacting != 0 {
			return nil
		}
		var snapshot []byte
		if err = s.db.QueryRowContext(ctx, `SELECT snapshot_canonical FROM managed_readiness_sources WHERE `+readinessSourceFenceWhere(), readinessSourceFenceArgs(fence)...).Scan(&snapshot); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if managedGoalSnapshotHasActiveChild(snapshot) {
			return nil
		}
		args := append([]any{nowText, fence.AgentID, nowText, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration}, managedFenceValues(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET evaluator_state='idle',next_wakeup_at=NULL,snoozed_until=NULL,snooze_pi_session_id=NULL,snooze_session_epoch=NULL,snooze_process_pid=NULL,snooze_process_start_marker=NULL,snooze_process_incarnation=NULL,snooze_runtime_generation=NULL,updated_at=? WHERE agent_id=? AND evaluator_state='snoozed' AND snoozed_until<=? AND snooze_pi_session_id=? AND snooze_session_epoch=? AND snooze_process_pid=? AND snooze_process_start_marker=? AND snooze_process_incarnation=? AND snooze_runtime_generation=? AND goal_enabled=1 AND projection_state='applied' AND desired_revision=applied_revision AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running' AND activity_state='idle')`, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return err
		}
		policy, err = scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		if err != nil {
			return err
		}
		if err = s.reserveManagedGoalWakeupTx(ctx, fence, "snooze:"+policy.WakeupMessageID, policy.WakeupMessageID, policy.Config.Goal, now); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return policy, changed, err
}

// ManagedGoalActivity is a router-observed fact that may rearm a capped goal
// only once for its exact runtime fence. The activity ID comes from durable
// child/control evidence, never from a model-visible tool argument.
type ManagedGoalActivity struct {
	Kind string
	ID   string
}

// RecordManagedGoalMeaningfulActivity opens a capped NOT_DONE policy only
// after three distinct qualified events. The durable identity ledger rejects
// replayed child/tool notifications before they can change the rearm counter.
func (s *Store) RecordManagedGoalMeaningfulActivity(ctx context.Context, fence ManagedRuntimeFence, activity ManagedGoalActivity) (*ManagedGoalPolicy, bool, error) {
	if !validManagedRuntimeFence(fence) || (activity.Kind != "child" && activity.Kind != "tool" && activity.Kind != "builtin") || activity.ID == "" || len(activity.ID) > 512 {
		return nil, false, errors.New("managed goal activity is invalid")
	}
	var policy *ManagedGoalPolicy
	rearmed := false
	err := s.runImmediate(ctx, func() error {
		current, err := scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !current.Config.Enabled || current.EvaluatorState != "scheduled" || current.NotDoneWakeups < managedGoalMaxNotDoneWakeups {
			return nil
		}
		var present int
		if err = s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `INSERT INTO managed_goal_activity(agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,activity_kind,activity_id,recorded_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, activity.Kind, activity.ID, now)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil || inserted != 1 {
			return err
		}
		args := []any{managedGoalRearmEvents, managedGoalRearmEvents, managedGoalRearmEvents, managedGoalRearmEvents, now, fence.AgentID, managedGoalMaxNotDoneWakeups}
		args = append(args, managedFenceValues(fence)...)
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET
			evaluator_state=CASE WHEN rearm_activity_count+1>=? THEN 'idle' ELSE 'scheduled' END,
			evaluator_attempts=CASE WHEN rearm_activity_count+1>=? THEN 0 ELSE evaluator_attempts END,
			not_done_wakeups=CASE WHEN rearm_activity_count+1>=? THEN 0 ELSE not_done_wakeups END,
			rearm_activity_count=CASE WHEN rearm_activity_count+1>=? THEN 0 ELSE rearm_activity_count+1 END,
			evaluator_lease_until=NULL,updated_at=?
			WHERE agent_id=? AND goal_enabled=1 AND evaluator_state='scheduled' AND not_done_wakeups>=?
			AND EXISTS(SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("managed_agents.")+` AND desired_state='running' AND lifecycle_state='running')`, args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return err
		}
		policy, err = scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, fence.AgentID))
		if err == nil {
			rearmed = policy.EvaluatorState == "idle"
		}
		return err
	})
	return policy, rearmed, err
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
