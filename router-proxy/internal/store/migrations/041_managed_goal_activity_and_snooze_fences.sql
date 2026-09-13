-- Snooze delivery and qualified work are facts of one exact runtime. A later
-- reset, restart, or same-name relaunch keeps the goal configuration but cannot
-- inherit a pending wakeup or replay an old activity event.
ALTER TABLE managed_agent_policies ADD COLUMN snooze_pi_session_id TEXT;
ALTER TABLE managed_agent_policies ADD COLUMN snooze_session_epoch INTEGER;
ALTER TABLE managed_agent_policies ADD COLUMN snooze_process_pid INTEGER;
ALTER TABLE managed_agent_policies ADD COLUMN snooze_process_start_marker TEXT;
ALTER TABLE managed_agent_policies ADD COLUMN snooze_process_incarnation TEXT;
ALTER TABLE managed_agent_policies ADD COLUMN snooze_runtime_generation TEXT;

CREATE TABLE managed_goal_activity (
  agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL,
  process_pid INTEGER NOT NULL,
  process_start_marker TEXT NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  activity_kind TEXT NOT NULL CHECK (activity_kind IN ('child','tool','builtin')),
  activity_id TEXT NOT NULL CHECK (length(activity_id) BETWEEN 1 AND 512),
  recorded_at TEXT NOT NULL,
  PRIMARY KEY (agent_id, pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, activity_kind, activity_id)
);
