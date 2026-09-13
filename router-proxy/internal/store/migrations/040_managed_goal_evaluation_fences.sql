-- Every model evaluator attempt is fenced to the exact managed runtime that
-- reached the settled boundary. The wakeup reservation survives a router crash
-- but can only be delivered by that original fence.
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_id TEXT;
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_goal_revision INTEGER;
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_settled_sequence INTEGER;
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_pi_session_id TEXT;
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_session_epoch INTEGER;
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_process_pid INTEGER;
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_process_start_marker TEXT;
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_process_incarnation TEXT;
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_runtime_generation TEXT;
ALTER TABLE managed_agent_policies ADD COLUMN evaluator_reason TEXT;
ALTER TABLE managed_agent_policies ADD COLUMN wakeup_message_id TEXT;

CREATE TABLE managed_goal_wakeups (
  message_id TEXT PRIMARY KEY,
  evaluation_id TEXT NOT NULL UNIQUE,
  agent_id TEXT NOT NULL,
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL,
  process_pid INTEGER NOT NULL,
  process_start_marker TEXT NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX managed_goal_wakeups_agent_idx ON managed_goal_wakeups(agent_id, created_at);
