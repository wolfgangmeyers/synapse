-- Restart execution needs durable old and target fences. Rebuild only the
-- bounded transition table so 023 reservations, managed rows, operations,
-- readiness sources, and their indexes remain unchanged.
CREATE TABLE managed_restart_transitions_execution_new (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id),
  operation_id TEXT NOT NULL UNIQUE REFERENCES managed_http_operations(operation_id),
  old_pi_session_id TEXT NOT NULL,
  old_session_epoch INTEGER NOT NULL,
  old_process_pid INTEGER NOT NULL,
  old_process_start_marker TEXT NOT NULL,
  old_process_incarnation TEXT NOT NULL,
  old_runtime_generation TEXT NOT NULL,
  old_manifest_revision INTEGER NOT NULL CHECK (old_manifest_revision >= 1),
  old_manifest_hash TEXT NOT NULL,
  target_runtime_generation TEXT NOT NULL,
  target_pi_session_id TEXT,
  target_session_epoch INTEGER,
  target_process_pid INTEGER,
  target_process_start_marker TEXT,
  target_process_incarnation TEXT,
  target_manifest_revision INTEGER,
  target_manifest_hash TEXT,
  old_desired_state TEXT NOT NULL,
  old_lifecycle_state TEXT NOT NULL,
  old_activity_state TEXT NOT NULL,
  old_accepting_prompts INTEGER NOT NULL CHECK (old_accepting_prompts IN (0, 1)),
  old_slot_held INTEGER NOT NULL CHECK (old_slot_held IN (0, 1)),
  reservation_activity_state TEXT NOT NULL CHECK (reservation_activity_state IN ('active', 'idle')),
  planned_stop INTEGER NOT NULL DEFAULT 0 CHECK (planned_stop IN (0, 1)),
  turn_settled_at TEXT,
  stage TEXT NOT NULL CHECK (stage IN ('reserved', 'prepared', 'settling', 'stopping', 'stopped', 'starting')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (
    (target_pi_session_id IS NULL AND target_session_epoch IS NULL AND target_process_pid IS NULL
      AND target_process_start_marker IS NULL AND target_process_incarnation IS NULL
      AND target_manifest_revision IS NULL AND target_manifest_hash IS NULL)
    OR
    (length(CAST(target_pi_session_id AS BLOB)) > 0 AND target_session_epoch >= 1
      AND target_process_pid > 0 AND length(CAST(target_process_start_marker AS BLOB)) > 0
      AND length(CAST(target_process_incarnation AS BLOB)) > 0 AND target_manifest_revision >= 1
      AND length(target_manifest_hash) = 64 AND target_manifest_hash = lower(target_manifest_hash)
      AND target_manifest_hash NOT GLOB '*[^0-9a-f]*')
  ),
  CHECK ((stage != 'starting') OR target_pi_session_id IS NOT NULL),
  CHECK ((stage NOT IN ('stopping', 'stopped', 'starting')) OR planned_stop = 1),
  CHECK ((reservation_activity_state != 'active' OR stage NOT IN ('stopping', 'stopped', 'starting')) OR turn_settled_at IS NOT NULL),
  CHECK ((reservation_activity_state != 'idle') OR turn_settled_at IS NULL)
);

INSERT INTO managed_restart_transitions_execution_new (
  agent_id,operation_id,old_pi_session_id,old_session_epoch,old_process_pid,
  old_process_start_marker,old_process_incarnation,old_runtime_generation,
  old_manifest_revision,old_manifest_hash,target_runtime_generation,
  old_desired_state,old_lifecycle_state,old_activity_state,old_accepting_prompts,
  old_slot_held,reservation_activity_state,planned_stop,turn_settled_at,stage,created_at,updated_at
)
SELECT transition.agent_id,transition.operation_id,transition.old_pi_session_id,
  transition.old_session_epoch,transition.old_process_pid,transition.old_process_start_marker,
  transition.old_process_incarnation,transition.old_runtime_generation,
  agent.manifest_revision,agent.manifest_hash,transition.target_runtime_generation,
  transition.old_desired_state,transition.old_lifecycle_state,transition.old_activity_state,
  transition.old_accepting_prompts,transition.old_slot_held,transition.reservation_activity_state,
  transition.planned_stop,NULL,transition.stage,transition.created_at,transition.updated_at
FROM managed_restart_transitions transition
JOIN managed_agents agent ON agent.agent_id = transition.agent_id;

DROP TABLE managed_restart_transitions;
ALTER TABLE managed_restart_transitions_execution_new RENAME TO managed_restart_transitions;
