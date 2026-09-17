package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const managedRootRecoveryWakeupMaxBodyBytes = 4096

// ManagedRootRecoveryWakeup identifies the one ordinary parent input emitted
// for a completed cold recovery action.
type ManagedRootRecoveryWakeup struct {
	ActionID, MessageID, Body, CreatedAt, ExpiresAt string
}

// QueueManagedRootRecoveryWakeup persists the restart notice and its
// idempotency marker in one SQLite transaction. A marker with the same action
// ID is the durable answer on every retry, including a retry after a router
// crash.
func (s *Store) QueueManagedRootRecoveryWakeup(ctx context.Context, actionID string) (*ManagedRootRecoveryWakeup, error) {
	if ctx == nil || actionID == "" {
		return nil, errors.New("managed root recovery wakeup is invalid")
	}
	var wakeup *ManagedRootRecoveryWakeup
	err := s.runImmediate(ctx, func() error {
		return s.queueManagedRootRecoveryWakeupTx(ctx, actionID, &wakeup)
	})
	return wakeup, err
}

// QueueManagedRootRecoveryWakeupForAgent repairs the crash window between a
// ready recovery transaction and the notice transaction. It selects only the
// ready action whose target fence is the agent's current fence.
func (s *Store) QueueManagedRootRecoveryWakeupForAgent(ctx context.Context, agentID string) (*ManagedRootRecoveryWakeup, error) {
	if ctx == nil || agentID == "" {
		return nil, errors.New("managed root recovery wakeup agent is invalid")
	}
	var wakeup *ManagedRootRecoveryWakeup
	err := s.runImmediate(ctx, func() error {
		var actionID string
		err := s.db.QueryRowContext(ctx, `SELECT recovery.action_id
			FROM managed_root_recovery_actions recovery
			JOIN managed_agents agent ON agent.agent_id=recovery.agent_id
			WHERE recovery.agent_id=? AND recovery.stage='ready'
				AND recovery.target_process_pid=agent.process_pid
				AND recovery.target_process_start_marker=agent.process_start_marker
				AND recovery.target_process_incarnation=agent.process_incarnation
				AND recovery.target_runtime_generation=agent.runtime_generation
				AND agent.desired_state='running' AND agent.lifecycle_state='running'
				AND agent.accepting_prompts=1 AND agent.slot_held=1
			ORDER BY recovery.updated_at DESC, recovery.action_id DESC LIMIT 1`, agentID).Scan(&actionID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.queueManagedRootRecoveryWakeupTx(ctx, actionID, &wakeup)
	})
	return wakeup, err
}

func (s *Store) queueManagedRootRecoveryWakeupTx(ctx context.Context, actionID string, result **ManagedRootRecoveryWakeup) error {
	marker, err := managedRootRecoveryWakeupForActionTx(ctx, s.db, actionID)
	if err == nil {
		*result = marker
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
	if err != nil {
		return err
	}
	if action.Stage != "ready" {
		return errors.New("managed root recovery wakeup requires a ready action")
	}
	if action.ExpiresAt == "" || action.ExpiresAt <= formatTimestamp(s.clock.Now()) {
		return nil
	}

	var current ManagedRuntimeFence
	var desiredState, lifecycleState string
	var accepting, slotHeld int
	err = s.db.QueryRowContext(ctx, `SELECT pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,desired_state,lifecycle_state,accepting_prompts,slot_held
		FROM managed_agents WHERE agent_id=?`, action.AgentID).Scan(
		&current.PiSessionID, &current.SessionEpoch, &current.ProcessPID, &current.ProcessStartMarker, &current.ProcessIncarnation, &current.RuntimeGeneration, &current.ManifestRevision, &current.ManifestHash,
		&desiredState, &lifecycleState, &accepting, &slotHeld)
	if err != nil {
		return err
	}
	current.AgentID = action.AgentID
	if desiredState != "running" || lifecycleState != "running" || accepting != 1 || slotHeld != 1 || current.ProcessPID != action.TargetProcessPID || current.ProcessStartMarker != action.TargetProcessStartMarker || current.ProcessIncarnation != action.TargetProcessIncarnation || current.RuntimeGeneration != action.TargetRuntimeGeneration {
		return errors.New("managed root recovery wakeup target is stale")
	}
	body, err := s.managedRootRecoveryWakeupBodyTx(ctx, action)
	if err != nil {
		return err
	}
	limit := s.maxBodyBytes
	if limit > managedRootRecoveryWakeupMaxBodyBytes {
		limit = managedRootRecoveryWakeupMaxBodyBytes
	}
	body, err = boundedManagedRootRecoveryWakeupBody(body, limit)
	if err != nil {
		return err
	}
	now := formatTimestamp(s.clock.Now())
	expires := formatTimestamp(s.clock.Now().Add(managedRootRecoveryRetention))
	messageID := managedRootRecoveryWakeupMessageID(actionID)
	incarnation := managedRootRecoveryWakeupIncarnation(actionID)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?) ON CONFLICT(agent_id) DO NOTHING`, action.AgentID, now); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO messages(id,sender,recipient,subject,body,created_at,expires_at,message_incarnation_id,sentinel_json,source_kind,state) VALUES(?,?,?,?,?,?,?,?,NULL,?,'queued')`, messageID, "system", action.AgentID, nil, body, now, expires, incarnation, "system"); err != nil {
		return err
	}
	target, err := s.resolveMessageTargetTx(ctx, messageID)
	if err != nil {
		return err
	}
	if target == nil {
		return errors.New("managed root recovery wakeup target is not active")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_root_recovery_wakeups(action_id,agent_id,old_pi_session_id,old_session_epoch,old_process_pid,old_process_start_marker,old_process_incarnation,old_runtime_generation,message_id,body,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		action.ActionID, action.AgentID, action.OldPiSessionID, action.OldSessionEpoch, action.OldProcessPID, action.OldProcessStartMarker, action.OldProcessIncarnation, action.OldRuntimeGeneration, messageID, body, now, expires)
	if err != nil {
		return err
	}
	// Target selection remains the normal router directory operation. Startup
	// runs its usual target reconciliation after managed recovery has completed.
	*result = &ManagedRootRecoveryWakeup{ActionID: action.ActionID, MessageID: messageID, Body: body, CreatedAt: now, ExpiresAt: expires}
	return nil
}

func managedRootRecoveryWakeupForActionTx(ctx context.Context, db *sql.DB, actionID string) (*ManagedRootRecoveryWakeup, error) {
	var wakeup ManagedRootRecoveryWakeup
	err := db.QueryRowContext(ctx, `SELECT action_id,message_id,body,created_at,expires_at FROM managed_root_recovery_wakeups WHERE action_id=?`, actionID).Scan(&wakeup.ActionID, &wakeup.MessageID, &wakeup.Body, &wakeup.CreatedAt, &wakeup.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &wakeup, nil
}

func managedRootRecoveryWakeupMessageID(actionID string) string {
	sum := sha256.Sum256([]byte("managed-root-recovery-wakeup:" + actionID))
	return "mrrw_" + fmt.Sprintf("%x", sum[:])
}

func managedRootRecoveryWakeupIncarnation(actionID string) string {
	sum := sha256.Sum256([]byte("managed-root-recovery-wakeup-incarnation:" + actionID))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

type managedRootRecoveryChildName struct {
	ParentEntryID string
	TaskID        string
	RunID         string
}

func (s *Store) managedRootRecoveryWakeupBodyTx(ctx context.Context, action *ManagedRootRecoveryAction) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT parent_entry_id,task_id,run_id
		FROM managed_child_recoveries
		WHERE agent_id=? AND prior_pi_session_id=? AND prior_session_epoch=? AND prior_process_pid=? AND prior_process_start_marker=? AND prior_process_incarnation=? AND prior_runtime_generation=? AND recovery_state='interrupted_unknown'
		ORDER BY task_id ASC,run_id ASC,parent_entry_id ASC`, action.AgentID, action.OldPiSessionID, action.OldSessionEpoch, action.OldProcessPID, action.OldProcessStartMarker, action.OldProcessIncarnation, action.OldRuntimeGeneration)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	children := make([]managedRootRecoveryChildName, 0)
	for rows.Next() {
		var child managedRootRecoveryChildName
		if err := rows.Scan(&child.ParentEntryID, &child.TaskID, &child.RunID); err != nil {
			return "", err
		}
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(children) == 0 {
		return "Router restarted. No child runs were recorded as interrupted.", nil
	}
	parts := make([]string, 0, len(children))
	for _, child := range children {
		parts = append(parts, fmt.Sprintf("%s (task %s)", sanitizeManagedRootRecoveryToken(child.RunID), sanitizeManagedRootRecoveryToken(child.TaskID)))
	}
	return "Router restarted. Interrupted child runs: " + strings.Join(parts, ", ") + ".", nil
}

func sanitizeManagedRootRecoveryToken(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' || char == ':' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
		if builder.Len() >= 64 {
			break
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func boundedManagedRootRecoveryWakeupBody(body string, limit int) (string, error) {
	if limit < 1 || !utf8.ValidString(body) {
		return "", errors.New("managed root recovery wakeup body is invalid")
	}
	if len([]byte(body)) <= limit {
		return body, nil
	}
	encoded := []byte(body)
	for len(encoded) > limit && !utf8.Valid(encoded) {
		encoded = encoded[:len(encoded)-1]
	}
	if len(encoded) > limit {
		encoded = encoded[:limit]
		for !utf8.Valid(encoded) {
			encoded = encoded[:len(encoded)-1]
		}
	}
	if len(encoded) == 0 {
		return "", errors.New("managed root recovery wakeup body is empty")
	}
	return string(encoded), nil
}

func (s *Store) pruneManagedRootRecoveryWakeupsTx(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM managed_root_recovery_wakeups WHERE expires_at <= ?`, formatTimestamp(s.clock.Now()))
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}
