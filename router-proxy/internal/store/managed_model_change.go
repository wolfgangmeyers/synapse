package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// RecoverPendingManagedModelChanges treats every pre-mutation fence left by a
// prior router process as ambiguous. The agent and replay response close in the
// same transaction before ordinary cold-start recovery can inspect the row.
func (s *Store) RecoverPendingManagedModelChanges(ctx context.Context) error {
	return s.runImmediate(ctx, func() error {
		rows, err := s.db.QueryContext(ctx, managedOperationSelect+` WHERE state='pending' AND method='PUT' AND canonical_path LIKE '/v1/managed-agents/%/model' ORDER BY created_at,operation_id`)
		if err != nil {
			return err
		}
		var operations []*ManagedOperation
		for rows.Next() {
			operation, scanErr := scanManagedOperation(rows)
			if scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
			operations = append(operations, operation)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		for _, operation := range operations {
			agentID, validPath := managedModelAgentFromPath(operation.CanonicalPath)
			code := "MODEL_CHANGE_RECOVERY_FAILED"
			message := "managed model change was interrupted and its runtime model is unknown"
			status := 500
			body, _ := json.Marshal(map[string]any{
				"error":      map[string]any{"code": code, "message": message},
				"request_id": operation.RequestID,
			})
			headers := map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": operation.RequestID}

			var agent *ManagedAgentRecord
			if validPath {
				agent, err = scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, agentID), nil)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
			now := s.clock.Now()
			if agent != nil {
				fence := restartFenceFromAgent(agent)
				if validManagedRuntimeFence(fence) {
					result, updateErr := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='failed',activity_state='failed',accepting_prompts=0,slot_held=0,last_error_code=?,last_error_message=?,last_error_at=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running'`, append([]any{code, message, formatTimestamp(now), formatTimestamp(now)}, managedFenceValues(fence)...)...)
					if updateErr != nil {
						return updateErr
					}
					changed, updateErr := result.RowsAffected()
					if updateErr != nil {
						return updateErr
					}
					if changed != 1 {
						return &ManagedRestartStaleError{AgentID: agentID}
					}
				}
			}
			encodedHeaders, err := json.Marshal(headers)
			if err != nil {
				return err
			}
			result, err := s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',response_status=?,response_headers_json=?,response_body=?,terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, status, string(encodedHeaders), string(body), formatTimestamp(now), formatTimestamp(now.Add(24*time.Hour)), code, message, operation.OperationID)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return errors.New("pending managed model operation changed during recovery")
			}
			if agent != nil {
				if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
