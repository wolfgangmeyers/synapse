-- Keep the complete crash policy in the existing one-row-per-agent record.
-- The bounded JSON history needs three timestamps at most; ready_since tracks
-- the continuous healthy interval across router restarts.
ALTER TABLE managed_crash_recovery ADD COLUMN crash_history TEXT NOT NULL DEFAULT '[]';
ALTER TABLE managed_crash_recovery ADD COLUMN ready_since TEXT;

-- Migration 046 persisted the first unexpected exit only as retry_at. Its
-- one-second retry is derived from the exit time, so retain that event when
-- adding the bounded history. Restrict the conversion to the exact pending
-- unexpected-exit projection; planned stops and idle rows must not become
-- crashes merely because they have a stale retry value.
UPDATE managed_crash_recovery
SET crash_history = json_array(strftime('%Y-%m-%dT%H:%M:%fZ', retry_at, '-1 second'))
WHERE retry_at IS NOT NULL
  AND retry_at <> ''
  AND retry_at = strftime('%Y-%m-%dT%H:%M:%fZ', retry_at)
  AND EXISTS (
    SELECT 1 FROM managed_agents
    WHERE managed_agents.agent_id = managed_crash_recovery.agent_id
      AND managed_agents.desired_state = 'running'
      AND managed_agents.lifecycle_state = 'running'
      AND managed_agents.activity_state = 'stopped'
      AND managed_agents.last_error_code = 'PROCESS_EXITED'
  );
