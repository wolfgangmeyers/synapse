-- Child recovery rows must retain the complete old process fence. Session and
-- generation alone are not enough: a reused session can contain a different
-- process, and its children must not appear in a later restart notice.
ALTER TABLE managed_child_recoveries ADD COLUMN prior_process_pid INTEGER;
ALTER TABLE managed_child_recoveries ADD COLUMN prior_process_start_marker TEXT;

-- Rows written before this migration are backfilled only when the retained
-- recovery action carries the exact old process identity. Rows without that
-- evidence remain in the ledger but are intentionally excluded from notices.
UPDATE managed_child_recoveries
SET prior_process_pid = COALESCE(
      (
        SELECT old_process_pid
        FROM managed_root_recovery_actions
        WHERE agent_id=managed_child_recoveries.agent_id
          AND old_pi_session_id=managed_child_recoveries.prior_pi_session_id
          AND old_session_epoch=managed_child_recoveries.prior_session_epoch
          AND old_process_incarnation=managed_child_recoveries.prior_process_incarnation
          AND old_runtime_generation=managed_child_recoveries.prior_runtime_generation
        ORDER BY updated_at DESC, action_id DESC LIMIT 1
      ),
      (
        SELECT process_pid
        FROM managed_agents
        WHERE agent_id=managed_child_recoveries.agent_id
          AND pi_session_id=managed_child_recoveries.prior_pi_session_id
          AND session_epoch=managed_child_recoveries.prior_session_epoch
          AND process_incarnation=managed_child_recoveries.prior_process_incarnation
          AND runtime_generation=managed_child_recoveries.prior_runtime_generation
        LIMIT 1
      )
    ),
    prior_process_start_marker = COALESCE(
      (
        SELECT old_process_start_marker
        FROM managed_root_recovery_actions
        WHERE agent_id=managed_child_recoveries.agent_id
          AND old_pi_session_id=managed_child_recoveries.prior_pi_session_id
          AND old_session_epoch=managed_child_recoveries.prior_session_epoch
          AND old_process_incarnation=managed_child_recoveries.prior_process_incarnation
          AND old_runtime_generation=managed_child_recoveries.prior_runtime_generation
        ORDER BY updated_at DESC, action_id DESC LIMIT 1
      ),
      (
        SELECT process_start_marker
        FROM managed_agents
        WHERE agent_id=managed_child_recoveries.agent_id
          AND pi_session_id=managed_child_recoveries.prior_pi_session_id
          AND session_epoch=managed_child_recoveries.prior_session_epoch
          AND process_incarnation=managed_child_recoveries.prior_process_incarnation
          AND runtime_generation=managed_child_recoveries.prior_runtime_generation
        LIMIT 1
      )
    )
WHERE prior_process_pid IS NULL;

CREATE INDEX managed_child_recoveries_exact_fence_idx
  ON managed_child_recoveries(agent_id, prior_pi_session_id, prior_session_epoch,
    prior_process_pid, prior_process_start_marker, prior_process_incarnation,
    prior_runtime_generation, recovery_state);
