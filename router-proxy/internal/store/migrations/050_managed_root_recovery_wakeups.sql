-- A cold restart gets one durable parent input after the replacement is ready.
-- The marker is keyed to the exact recovery action so retries cannot enqueue a
-- second notice, while the message expires with the recovery action's bound.
CREATE TABLE managed_root_recovery_wakeups (
  action_id TEXT PRIMARY KEY REFERENCES managed_root_recovery_actions(action_id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  old_pi_session_id TEXT NOT NULL,
  old_session_epoch INTEGER NOT NULL CHECK (old_session_epoch >= 1),
  old_process_pid INTEGER NOT NULL CHECK (old_process_pid > 0),
  old_process_start_marker TEXT NOT NULL,
  old_process_incarnation TEXT NOT NULL,
  old_runtime_generation TEXT NOT NULL,
  message_id TEXT NOT NULL UNIQUE,
  body TEXT NOT NULL CHECK (length(CAST(body AS BLOB)) BETWEEN 1 AND 4096),
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX managed_root_recovery_wakeups_retention_idx
  ON managed_root_recovery_wakeups(expires_at, agent_id);
CREATE INDEX managed_root_recovery_wakeups_agent_idx
  ON managed_root_recovery_wakeups(agent_id, created_at DESC);
