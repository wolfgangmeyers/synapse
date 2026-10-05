-- Restart reservation needs one bounded transition row per managed agent. SQLite
-- cannot widen the existing CHECK constraints in place, so copy the complete
-- 022 row before replacing the table inside the migration runner's transaction.
CREATE TABLE managed_agents_restart_new (
  agent_id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  slot_held INTEGER NOT NULL DEFAULT 0 CHECK (slot_held IN (0, 1)),
  cwd TEXT NOT NULL,
  selected_model_provider TEXT NOT NULL,
  selected_model_id TEXT NOT NULL,
  desired_state TEXT NOT NULL CHECK (desired_state IN ('running')),
  lifecycle_state TEXT NOT NULL CHECK (lifecycle_state IN ('starting', 'running', 'restarting', 'resetting', 'terminating', 'failed', 'terminated')),
  activity_state TEXT NOT NULL CHECK (activity_state IN ('starting', 'waiting', 'active', 'idle', 'compacting', 'stopped', 'failed')),
  accepting_prompts INTEGER NOT NULL DEFAULT 0 CHECK (accepting_prompts IN (0, 1)),
  pi_session_id TEXT,
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  process_pid INTEGER,
  process_start_marker TEXT,
  process_incarnation TEXT,
  runtime_generation TEXT,
  manifest_revision INTEGER NOT NULL CHECK (manifest_revision >= 1),
  manifest_hash TEXT NOT NULL,
  pending_initial_instruction TEXT,
  initial_instruction_sent INTEGER NOT NULL DEFAULT 0 CHECK (initial_instruction_sent IN (0, 1)),
  last_error_code TEXT,
  last_error_message TEXT,
  last_error_at TEXT,
  created_at TEXT NOT NULL,
  started_at TEXT,
  updated_at TEXT NOT NULL
);

INSERT INTO managed_agents_restart_new (
  agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,
  desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,
  session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,
  manifest_revision,manifest_hash,pending_initial_instruction,initial_instruction_sent,
  last_error_code,last_error_message,last_error_at,created_at,started_at,updated_at
)
SELECT
  agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,
  desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,
  session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,
  manifest_revision,manifest_hash,pending_initial_instruction,initial_instruction_sent,
  last_error_code,last_error_message,last_error_at,created_at,started_at,updated_at
FROM managed_agents;

DROP TABLE managed_agents;
ALTER TABLE managed_agents_restart_new RENAME TO managed_agents;
CREATE INDEX managed_agents_slot_idx ON managed_agents (slot_held, lifecycle_state);

-- A restart transition is bounded by the primary key and keeps the old live
-- fence long enough for later replacement or recovery owners to act safely.
CREATE TABLE managed_restart_transitions (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id),
  operation_id TEXT NOT NULL UNIQUE REFERENCES managed_http_operations(operation_id),
  old_pi_session_id TEXT NOT NULL,
  old_session_epoch INTEGER NOT NULL,
  old_process_pid INTEGER NOT NULL,
  old_process_start_marker TEXT NOT NULL,
  old_process_incarnation TEXT NOT NULL,
  old_runtime_generation TEXT NOT NULL,
  target_runtime_generation TEXT NOT NULL,
  old_desired_state TEXT NOT NULL,
  old_lifecycle_state TEXT NOT NULL,
  old_activity_state TEXT NOT NULL,
  old_accepting_prompts INTEGER NOT NULL CHECK (old_accepting_prompts IN (0, 1)),
  old_slot_held INTEGER NOT NULL CHECK (old_slot_held IN (0, 1)),
  reservation_activity_state TEXT NOT NULL,
  planned_stop INTEGER NOT NULL DEFAULT 0 CHECK (planned_stop IN (0, 1)),
  stage TEXT NOT NULL CHECK (stage IN ('reserved', 'prepared')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
