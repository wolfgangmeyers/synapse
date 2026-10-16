-- Drop triggers referencing managed_agents from other tables so that dropping managed_agents
-- does not fire foreign key cascade deletes or trigger validation errors.
DROP TRIGGER IF EXISTS embedded_goal_continuation_no_delete;
DROP TRIGGER IF EXISTS embedded_goal_snooze_call_no_delete;
DROP TRIGGER IF EXISTS embedded_goal_snooze_preference_no_delete;
DROP TRIGGER IF EXISTS embedded_goal_receipt_immutable_delete;
DROP TRIGGER IF EXISTS embedded_goal_evaluation_immutable_delete;

CREATE TABLE managed_agents_104_new (
  agent_id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  slot_held INTEGER NOT NULL DEFAULT 0 CHECK (slot_held IN (0,1)),
  cwd TEXT NOT NULL,
  selected_model_provider TEXT NOT NULL,
  selected_model_id TEXT NOT NULL,
  desired_state TEXT NOT NULL CHECK (desired_state IN ('running','terminated')),
  lifecycle_state TEXT NOT NULL CHECK (lifecycle_state IN ('starting','running','restarting','resetting','terminating','failed','terminated')),
  activity_state TEXT NOT NULL CHECK (activity_state IN ('starting','waiting','active','idle','compacting','stopped','failed')),
  accepting_prompts INTEGER NOT NULL DEFAULT 0 CHECK (accepting_prompts IN (0,1)),
  pi_session_id TEXT,
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  process_pid INTEGER,
  process_start_marker TEXT,
  process_incarnation TEXT,
  runtime_generation TEXT,
  manifest_revision INTEGER NOT NULL CHECK (manifest_revision >= 1),
  manifest_hash TEXT NOT NULL,
  pending_initial_instruction TEXT,
  initial_instruction_sent INTEGER NOT NULL DEFAULT 0 CHECK (initial_instruction_sent IN (0,1)),
  last_error_code TEXT,
  last_error_message TEXT,
  last_error_at TEXT,
  created_at TEXT NOT NULL,
  started_at TEXT,
  updated_at TEXT NOT NULL,
  launch_operation_id TEXT REFERENCES managed_http_operations(operation_id) ON DELETE RESTRICT,
  runtime TEXT NOT NULL DEFAULT 'rpc' CHECK(runtime IN ('rpc','embedded')),
  parent_agent_id TEXT,
  active_workflow_state_id TEXT REFERENCES workflow_states(id) ON DELETE SET NULL,
  allow_inline_tools INTEGER NOT NULL DEFAULT 0,
  worktree INTEGER NOT NULL DEFAULT 0,
  worktree_base_path TEXT,
  worktree_branch TEXT,
  context_tokens INTEGER,
  context_window INTEGER,
  context_percent REAL,
  active_workflow_id TEXT REFERENCES workflows(id) ON DELETE SET NULL,
  workflow_variables_json TEXT
);

INSERT INTO managed_agents_104_new (
  agent_id, name, slot_held, cwd, selected_model_provider, selected_model_id,
  desired_state, lifecycle_state, activity_state, accepting_prompts, pi_session_id,
  session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation,
  manifest_revision, manifest_hash, pending_initial_instruction, initial_instruction_sent,
  last_error_code, last_error_message, last_error_at, created_at, started_at, updated_at,
  launch_operation_id, runtime, parent_agent_id, active_workflow_state_id,
  allow_inline_tools, worktree, worktree_base_path, worktree_branch,
  context_tokens, context_window, context_percent, active_workflow_id, workflow_variables_json
) SELECT
  agent_id, name, slot_held, cwd, selected_model_provider, selected_model_id,
  desired_state, lifecycle_state, activity_state, accepting_prompts, pi_session_id,
  session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation,
  manifest_revision, manifest_hash, pending_initial_instruction, initial_instruction_sent,
  last_error_code, last_error_message, last_error_at, created_at, started_at, updated_at,
  launch_operation_id, runtime, parent_agent_id, active_workflow_state_id,
  allow_inline_tools, worktree, worktree_base_path, worktree_branch,
  context_tokens, context_window, context_percent, active_workflow_id, workflow_variables_json
FROM managed_agents;

DROP TABLE managed_agents;
ALTER TABLE managed_agents_104_new RENAME TO managed_agents;

CREATE INDEX managed_agents_slot_idx ON managed_agents (slot_held, lifecycle_state);
CREATE INDEX managed_agents_parent_idx ON managed_agents (parent_agent_id);

CREATE TRIGGER managed_runtime_immutable BEFORE UPDATE OF runtime ON managed_agents
WHEN NEW.runtime <> OLD.runtime BEGIN SELECT RAISE(ABORT,'managed runtime is immutable'); END;

CREATE TRIGGER embedded_goal_continuation_epoch_invalidate AFTER UPDATE OF session_epoch,runtime_generation,pi_session_id ON managed_agents
WHEN OLD.session_epoch<>NEW.session_epoch OR OLD.runtime_generation<>NEW.runtime_generation OR OLD.pi_session_id<>NEW.pi_session_id
BEGIN
 UPDATE embedded_goal_continuations SET state='invalidated',disposition='invalidated'
 WHERE agent_id=OLD.agent_id AND state='reserved';
END;

CREATE TRIGGER embedded_goal_snooze_preference_epoch_invalidate AFTER UPDATE OF session_epoch,runtime_generation,pi_session_id ON managed_agents
WHEN OLD.session_epoch<>NEW.session_epoch OR OLD.runtime_generation<>NEW.runtime_generation OR OLD.pi_session_id<>NEW.pi_session_id
BEGIN
 UPDATE embedded_goal_snooze_preferences SET active=0 WHERE agent_id=OLD.agent_id AND active=1;
END;

CREATE TRIGGER embedded_goal_continuation_no_delete BEFORE DELETE ON embedded_goal_continuations
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'continuation is immutable'); END;

CREATE TRIGGER embedded_goal_snooze_call_no_delete BEFORE DELETE ON embedded_goal_snooze_calls
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'snooze call is immutable'); END;

CREATE TRIGGER embedded_goal_snooze_preference_no_delete BEFORE DELETE ON embedded_goal_snooze_preferences
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'snooze preference is immutable history'); END;

CREATE TRIGGER embedded_goal_receipt_immutable_delete BEFORE DELETE ON embedded_goal_applied_receipts
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'embedded goal receipt is immutable'); END;

CREATE TRIGGER embedded_goal_evaluation_immutable_delete BEFORE DELETE ON embedded_goal_evaluations
WHEN EXISTS (SELECT 1 FROM managed_agents WHERE agent_id=OLD.agent_id)
BEGIN SELECT RAISE(ABORT,'embedded goal evaluation is immutable'); END;
