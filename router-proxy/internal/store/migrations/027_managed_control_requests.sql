-- Phase 2 lifecycle-control ledger. The populated managed_agents rebuild and
-- legacy 025 transition backfill run only through store.go's dedicated 027
-- runner because they must temporarily suspend SQLite foreign-key enforcement
-- on the store's single connection.

CREATE TABLE managed_session_artifacts (
  artifact_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  pi_session_id TEXT NOT NULL CHECK (length(CAST(pi_session_id AS BLOB)) BETWEEN 1 AND 128),
  root_relative_path TEXT NOT NULL
    CHECK (length(CAST(root_relative_path AS BLOB)) BETWEEN 1 AND 4096)
    CHECK (root_relative_path NOT LIKE '/%')
    CHECK (root_relative_path <> '..')
    CHECK (root_relative_path NOT LIKE '../%')
    CHECK (root_relative_path NOT LIKE '%/../%')
    CHECK (root_relative_path NOT LIKE '%/..'),
  path_sha256 TEXT NOT NULL CHECK (length(path_sha256)=64 AND path_sha256=lower(path_sha256) AND path_sha256 NOT GLOB '*[^0-9a-f]*'),
  discovery_state TEXT NOT NULL CHECK (discovery_state IN ('materialized','not_materialized','ambiguous','missing')),
  file_device TEXT,
  file_inode TEXT,
  cleanup_state TEXT NOT NULL CHECK (cleanup_state IN ('retained','pending','unlinked')),
  discovered_at TEXT NOT NULL,
  cleanup_updated_at TEXT NOT NULL,
  CHECK ((discovery_state='materialized') = (file_device IS NOT NULL AND file_inode IS NOT NULL)),
  CHECK ((discovery_state<>'materialized') = (file_device IS NULL AND file_inode IS NULL)),
  CHECK (discovery_state<>'ambiguous' OR cleanup_state='retained'),
  CHECK (cleanup_state<>'unlinked' OR discovery_state<>'ambiguous'),
  UNIQUE(agent_id, session_epoch),
  UNIQUE(path_sha256)
);
CREATE INDEX managed_session_artifacts_cleanup_idx
  ON managed_session_artifacts(cleanup_state, agent_id, session_epoch);

CREATE TABLE managed_lifecycle_actions (
  action_id TEXT PRIMARY KEY,
  operation_id TEXT NOT NULL UNIQUE REFERENCES managed_http_operations(operation_id) ON DELETE CASCADE,
  agent_id TEXT,
  kind TEXT NOT NULL CHECK (kind IN ('restart','reset','terminate','restart_all')),
  source TEXT NOT NULL CHECK (source IN ('direct','restart_all_child')),
  parent_action_id TEXT REFERENCES managed_lifecycle_actions(action_id) ON DELETE CASCADE,
  retry_of TEXT UNIQUE REFERENCES managed_lifecycle_actions(action_id) ON DELETE RESTRICT,
  execution_owner TEXT NOT NULL DEFAULT 'lifecycle' CHECK (execution_owner IN ('legacy025','lifecycle')),
  state TEXT NOT NULL CHECK (state IN ('admitted','awaiting_settle','stopping_source','starting_target','awaiting_target_ready','old_artifact_cleanup','terminating','cleanup','succeeded','failed','cancelled','superseded')),
  stage TEXT NOT NULL CHECK (stage IN ('admission','source_settle','source_stop','target_start','target_ready','old_artifact_cleanup','abort','terminate_reap','direct_mail_retire','agent_cleanup','durable_write','complete')),
  source_pi_session_id TEXT,
  source_session_epoch INTEGER,
  source_process_pid INTEGER,
  source_process_start_marker TEXT,
  source_process_incarnation TEXT,
  source_runtime_generation TEXT,
  source_manifest_revision INTEGER,
  source_manifest_hash TEXT,
  target_pi_session_id TEXT,
  target_session_epoch INTEGER,
  target_process_pid INTEGER,
  target_process_start_marker TEXT,
  target_process_incarnation TEXT,
  target_runtime_generation TEXT,
  target_manifest_revision INTEGER,
  target_manifest_hash TEXT,
  planned_target_runtime_generation TEXT,
  target_artifact_id TEXT REFERENCES managed_session_artifacts(artifact_id) ON DELETE SET NULL,
  terminal_code TEXT,
  terminal_message TEXT,
  retryable INTEGER,
  completed_at TEXT,
  recovery_owner TEXT,
  recovery_claimed_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK ((kind='restart_all') = (agent_id IS NULL)),
  CHECK ((source='restart_all_child') = (parent_action_id IS NOT NULL)),
  CHECK (kind <> 'restart_all' OR source='direct'),
  CHECK (source <> 'restart_all_child' OR kind='restart'),
  CHECK ((kind='restart_all') OR length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  CHECK ((kind IN ('restart','reset')) = (planned_target_runtime_generation IS NOT NULL)),
  CHECK (planned_target_runtime_generation IS NULL OR length(CAST(planned_target_runtime_generation AS BLOB)) BETWEEN 1 AND 128),
  CHECK (target_runtime_generation IS NULL OR target_runtime_generation = planned_target_runtime_generation),
  CHECK ((source_pi_session_id IS NULL AND source_session_epoch IS NULL AND source_process_pid IS NULL AND source_process_start_marker IS NULL AND source_process_incarnation IS NULL AND source_runtime_generation IS NULL AND source_manifest_revision IS NULL AND source_manifest_hash IS NULL) OR (length(CAST(source_pi_session_id AS BLOB)) > 0 AND source_session_epoch >= 1 AND source_process_pid > 0 AND length(CAST(source_process_start_marker AS BLOB)) > 0 AND length(CAST(source_process_incarnation AS BLOB)) > 0 AND length(CAST(source_runtime_generation AS BLOB)) > 0 AND source_manifest_revision >= 1 AND length(source_manifest_hash)=64 AND source_manifest_hash=lower(source_manifest_hash) AND source_manifest_hash NOT GLOB '*[^0-9a-f]*')),
  CHECK ((target_pi_session_id IS NULL AND target_session_epoch IS NULL AND target_process_pid IS NULL AND target_process_start_marker IS NULL AND target_process_incarnation IS NULL AND target_runtime_generation IS NULL AND target_manifest_revision IS NULL AND target_manifest_hash IS NULL) OR (length(CAST(target_pi_session_id AS BLOB)) > 0 AND target_session_epoch >= 1 AND target_process_pid > 0 AND length(CAST(target_process_start_marker AS BLOB)) > 0 AND length(CAST(target_process_incarnation AS BLOB)) > 0 AND length(CAST(target_runtime_generation AS BLOB)) > 0 AND target_manifest_revision >= 1 AND length(target_manifest_hash)=64 AND target_manifest_hash=lower(target_manifest_hash) AND target_manifest_hash NOT GLOB '*[^0-9a-f]*')),
  CHECK ((state IN ('succeeded','failed','cancelled','superseded')) = (completed_at IS NOT NULL)),
  CHECK ((state IN ('succeeded','failed','cancelled','superseded')) = (terminal_code IS NOT NULL)),
  CHECK ((state IN ('succeeded','failed','cancelled','superseded')) = (terminal_message IS NOT NULL)),
  CHECK ((state IN ('succeeded','failed','cancelled','superseded')) = (retryable IS NOT NULL)),
  CHECK (retryable IS NULL OR retryable IN (0,1)),
  CHECK (retry_of IS NULL OR kind IN ('reset','terminate')),
  CHECK (execution_owner <> 'legacy025' OR (kind='restart' AND source='direct' AND retry_of IS NULL)),
  CHECK ((recovery_owner IS NULL) = (recovery_claimed_at IS NULL))
);
CREATE INDEX managed_lifecycle_recovery_idx ON managed_lifecycle_actions(state, recovery_claimed_at, created_at);
CREATE INDEX managed_lifecycle_agent_terminal_idx ON managed_lifecycle_actions(agent_id, completed_at DESC, action_id DESC);

CREATE TABLE managed_lifecycle_batch_items (
  parent_action_id TEXT NOT NULL REFERENCES managed_lifecycle_actions(action_id) ON DELETE CASCADE,
  snapshot_position INTEGER NOT NULL CHECK (snapshot_position BETWEEN 0 AND 31),
  agent_id TEXT NOT NULL CHECK (length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  status TEXT NOT NULL CHECK (status IN ('accepted','coalesced','rejected')),
  child_action_id TEXT UNIQUE REFERENCES managed_lifecycle_actions(action_id) ON DELETE SET NULL,
  child_operation_id TEXT UNIQUE REFERENCES managed_http_operations(operation_id) ON DELETE SET NULL,
  error_code TEXT,
  error_message TEXT,
  created_at TEXT NOT NULL,
  CHECK ((status IN ('accepted','coalesced')) = (child_action_id IS NOT NULL)),
  CHECK ((status IN ('accepted','coalesced')) = (child_operation_id IS NOT NULL)),
  CHECK ((status='rejected') = (error_code IS NOT NULL)),
  CHECK ((status='rejected') = (error_message IS NOT NULL)),
  UNIQUE(parent_action_id, snapshot_position),
  UNIQUE(parent_action_id, agent_id)
);
CREATE INDEX managed_lifecycle_batch_parent_idx ON managed_lifecycle_batch_items(parent_action_id, snapshot_position);

CREATE TABLE managed_control_requests (
  request_id TEXT PRIMARY KEY,
  result_id TEXT NOT NULL UNIQUE,
  agent_id TEXT NOT NULL CHECK (length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  tool_call_id TEXT NOT NULL CHECK (length(CAST(tool_call_id AS BLOB)) BETWEEN 1 AND 128),
  capability_id TEXT NOT NULL CHECK (length(CAST(capability_id AS BLOB)) BETWEEN 1 AND 128),
  tool_name TEXT NOT NULL CHECK (length(CAST(tool_name AS BLOB)) BETWEEN 1 AND 128),
  definition_id TEXT NOT NULL CHECK (length(CAST(definition_id AS BLOB)) BETWEEN 1 AND 128),
  operation_id TEXT NOT NULL CHECK (length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
  context_kind TEXT NOT NULL CHECK (context_kind='root'),
  canonical_arguments_sha256 TEXT NOT NULL CHECK (length(canonical_arguments_sha256)=64 AND canonical_arguments_sha256=lower(canonical_arguments_sha256) AND canonical_arguments_sha256 NOT GLOB '*[^0-9a-f]*'),
  request_fingerprint TEXT NOT NULL CHECK (length(request_fingerprint)=64 AND request_fingerprint=lower(request_fingerprint) AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
  state TEXT NOT NULL CHECK (state IN ('received','executing','side_effect_committed','completed','append_pending','awaiting_append','delivered','failed','cancelled')),
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  process_pid INTEGER NOT NULL CHECK (process_pid > 0),
  process_start_marker TEXT NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  manifest_revision INTEGER NOT NULL CHECK (manifest_revision >= 1),
  manifest_hash TEXT NOT NULL CHECK (length(manifest_hash)=64 AND manifest_hash=lower(manifest_hash) AND manifest_hash NOT GLOB '*[^0-9a-f]*'),
  result_status TEXT,
  result_content TEXT,
  result_details_json TEXT,
  result_payload_sha256 TEXT,
  decoded_payload_bytes INTEGER CHECK (decoded_payload_bytes BETWEEN 1 AND 65536),
  result_entry_id TEXT UNIQUE,
  append_attempt INTEGER NOT NULL DEFAULT 0 CHECK (append_attempt BETWEEN 0 AND 5),
  next_append_at TEXT,
  received_at TEXT NOT NULL,
  handler_owner TEXT,
  handler_started_at TEXT,
  handler_deadline_at TEXT,
  side_effect_committed_at TEXT,
  handler_completed_at TEXT,
  append_owner TEXT,
  append_started_at TEXT,
  append_watermark TEXT,
  append_submitted_at TEXT,
  receipt_entry_identity TEXT,
  delivered_at TEXT,
  terminal_at TEXT,
  error_code TEXT,
  error_detail TEXT,
  retryable INTEGER,
  UNIQUE(agent_id, request_id),
  CHECK ((state IN ('delivered','failed','cancelled')) = (terminal_at IS NOT NULL)),
  CHECK ((state IN ('delivered','failed','cancelled')) = (retryable IS NOT NULL)),
  CHECK (retryable IS NULL OR retryable IN (0,1)),
  CHECK ((result_status IS NULL) = (result_content IS NULL)),
  CHECK (result_content IS NULL OR length(CAST(result_content AS BLOB)) <= 16384),
  CHECK (result_details_json IS NULL OR length(CAST(result_details_json AS BLOB)) <= 16384),
  CHECK ((result_content IS NULL AND result_details_json IS NULL) OR length(CAST(result_content AS BLOB)) + length(CAST(result_details_json AS BLOB)) <= 65536),
  CHECK (result_payload_sha256 IS NULL OR (length(result_payload_sha256)=64 AND result_payload_sha256=lower(result_payload_sha256) AND result_payload_sha256 NOT GLOB '*[^0-9a-f]*')),
  CHECK ((result_payload_sha256 IS NULL) = (decoded_payload_bytes IS NULL)),
  CHECK (error_code IS NULL OR length(CAST(error_code AS BLOB)) BETWEEN 1 AND 128),
  CHECK (error_detail IS NULL OR length(CAST(error_detail AS BLOB)) <= 16384),
  CHECK (state NOT IN ('completed','append_pending','awaiting_append','delivered') OR result_entry_id IS NOT NULL),
  CHECK ((result_entry_id IS NULL) OR result_entry_id = 'mcr1_' || replace(result_id,'-','')),
  CHECK (append_attempt=0 OR result_entry_id IS NOT NULL),
  CHECK (state<>'append_pending' OR (append_attempt BETWEEN 1 AND 5 AND append_owner IS NOT NULL AND append_started_at IS NOT NULL AND append_watermark IS NOT NULL)),
  CHECK (state<>'awaiting_append' OR (append_attempt BETWEEN 1 AND 5 AND append_owner IS NOT NULL AND append_started_at IS NOT NULL AND append_watermark IS NOT NULL AND append_submitted_at IS NOT NULL)),
  CHECK (next_append_at IS NULL OR (state='completed' AND append_attempt BETWEEN 1 AND 4)),
  CHECK (state<>'delivered' OR (delivered_at IS NOT NULL AND receipt_entry_identity IS NOT NULL))
);
CREATE UNIQUE INDEX managed_control_request_dedup_idx ON managed_control_requests(agent_id, request_id);
CREATE INDEX managed_control_nonterminal_agent_idx ON managed_control_requests(agent_id, received_at) WHERE state NOT IN ('delivered','failed','cancelled');
CREATE INDEX managed_control_nonterminal_global_idx ON managed_control_requests(received_at) WHERE state NOT IN ('delivered','failed','cancelled');
CREATE INDEX managed_control_append_due_idx ON managed_control_requests(next_append_at, request_id) WHERE state='completed' AND next_append_at IS NOT NULL;
CREATE INDEX managed_control_terminal_retention_idx ON managed_control_requests(agent_id, terminal_at DESC, request_id DESC) WHERE state IN ('delivered','failed','cancelled');
