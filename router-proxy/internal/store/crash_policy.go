package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RecordManagedUnexpectedExit applies the persisted rolling crash policy. The
// exact runtime fence makes callbacks idempotent and prevents old processes
// from changing a replacement's policy.
func (s *Store) RecordManagedUnexpectedExit(ctx context.Context, fence ManagedRuntimeFence, operationID, message string) (ManagedCrashRecoveryDecision, error) {
	if !validManagedRuntimeFence(fence) || message == "" {
		return ManagedCrashRecoveryDecision{}, errors.New("managed unexpected exit is invalid")
	}
	message = RedactedManagedDiagnostic(message)
	decision := ManagedCrashRecoveryDecision{}
	err := s.runImmediate(ctx, func() error {
		now := s.clock.Now()
		nowText := formatTimestamp(now)
		if err := ensureManagedCrashRecoveryTx(ctx, s.db, fence.AgentID, nowText); err != nil {
			if strings.Contains(err.Error(), "no such table: managed_crash_recovery") {
				return ErrManagedCrashRecoveryUnavailable
			}
			return err
		}
		var activity, lifecycle, retryAt, historyJSON, readySince string
		var lastCode sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT activity_state,lifecycle_state,last_error_code FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running'`, managedFenceValues(fence)...).Scan(&activity, &lifecycle, &lastCode)
		if errors.Is(err, sql.ErrNoRows) {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		if err != nil {
			return err
		}
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(retry_at,''),crash_history,COALESCE(ready_since,'') FROM managed_crash_recovery WHERE agent_id=?`, fence.AgentID).Scan(&retryAt, &historyJSON, &readySince); err != nil {
			return err
		}
		if lifecycle == "failed" && lastCode.Valid && lastCode.String == "CRASH_LOOP" {
			decision.CrashLoop = true
			return nil
		}
		// The first callback owns the stopped row. A duplicate callback sees the
		// persisted deadline and returns the same decision without another event.
		if activity == "stopped" && retryAt != "" {
			decision.RetryAt = retryAt
			var history []string
			if json.Unmarshal([]byte(historyJSON), &history) == nil {
				decision.CrashCount = len(history)
			}
			return nil
		}
		var history []string
		if err := json.Unmarshal([]byte(historyJSON), &history); err != nil {
			return errors.New("managed crash history is invalid")
		}
		if readySince != "" {
			readyAt := parseTimestamp(readySince)
			// The authority's 600.000-second boundary is inclusive.
			if !readyAt.IsZero() && !now.Before(readyAt.Add(managedHealthyReset)) {
				history = nil
			}
		}
		// Keep the bounded policy history until the ten-minute healthy reset.
		// The shorter 120-second window is only the authority for declaring a
		// crash loop; dropping older entries here would make the 600-second
		// healthy-reset boundary unobservable.
		retained := make([]string, 0, len(history)+1)
		qualifying := make([]string, 0, len(history)+1)
		for _, value := range history {
			at := parseTimestamp(value)
			if at.IsZero() || at.After(now) || now.Sub(at) >= managedHealthyReset {
				continue
			}
			retained = append(retained, value)
			if now.Sub(at) < managedCrashWindow {
				qualifying = append(qualifying, value)
			}
		}
		retained = append(retained, nowText)
		if len(retained) > 3 {
			retained = retained[len(retained)-3:]
		}
		qualifying = append(qualifying, nowText)
		encoded, err := json.Marshal(retained)
		if err != nil {
			return err
		}
		decision.CrashCount = len(retained)
		if len(qualifying) >= 3 {
			decision.CrashLoop = true
			if _, err := s.db.ExecContext(ctx, `UPDATE managed_crash_recovery SET retry_at=NULL,crash_history=?,ready_since=NULL,updated_at=? WHERE agent_id=?`, string(encoded), nowText, fence.AgentID); err != nil {
				return err
			}
			result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='failed',activity_state='failed',accepting_prompts=0,slot_held=0,last_error_code='CRASH_LOOP',last_error_message='managed agent entered crash loop',last_error_at=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state IN ('starting','running')`, append([]any{nowText, nowText}, managedFenceValues(fence)...)...)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				if err != nil {
					return err
				}
				return &ManagedRestartStaleError{AgentID: fence.AgentID}
			}
		} else {
			delay := time.Second
			if len(qualifying) == 2 {
				delay = 5 * time.Second
			}
			decision.RetryAt = formatTimestamp(now.Add(delay))
			if _, err := s.db.ExecContext(ctx, `UPDATE managed_crash_recovery SET retry_at=?,crash_history=?,ready_since=NULL,updated_at=? WHERE agent_id=?`, decision.RetryAt, string(encoded), nowText, fence.AgentID); err != nil {
				return err
			}
			result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='running',activity_state='stopped',accepting_prompts=0,slot_held=1,last_error_code='PROCESS_EXITED',last_error_message=?,last_error_at=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state IN ('starting','running')`, append([]any{message, nowText, nowText}, managedFenceValues(fence)...)...)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				if err != nil {
					return err
				}
				return &ManagedRestartStaleError{AgentID: fence.AgentID}
			}
		}
		if err := terminalizeManagedCrashOperationTx(ctx, s.db, operationID, nowText, "PROCESS_EXITED", message); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
	return decision, err
}

// RecordManagedStartupExit is the same policy boundary for a child that exits
// before readiness. A start error that proves no child was created remains a
// launch failure and does not call this method.
func (s *Store) RecordManagedStartupExit(ctx context.Context, fence ManagedRuntimeFence, operationID, message string) (ManagedCrashRecoveryDecision, error) {
	return s.RecordManagedUnexpectedExit(ctx, fence, operationID, message)
}
