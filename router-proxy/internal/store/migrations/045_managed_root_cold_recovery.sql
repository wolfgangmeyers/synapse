-- Cold recovery persists the fixed, non-secret launch recipe separately from
-- the public managed-agent projection. Existing agents may have no recipe and
-- fail closed until a later launch or explicit migration backfill supplies one.
CREATE TABLE managed_launch_recipes (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  pi_executable TEXT NOT NULL CHECK (length(CAST(pi_executable AS BLOB)) BETWEEN 1 AND 4096),
  session_root TEXT NOT NULL CHECK (length(CAST(session_root AS BLOB)) BETWEEN 1 AND 4096),
  cwd TEXT NOT NULL CHECK (length(CAST(cwd AS BLOB)) BETWEEN 1 AND 4096),
  provider TEXT NOT NULL CHECK (length(CAST(provider AS BLOB)) BETWEEN 1 AND 128),
  model_id TEXT NOT NULL CHECK (length(CAST(model_id AS BLOB)) BETWEEN 1 AND 256),
  mecha_rpc_extension TEXT NOT NULL CHECK (length(CAST(mecha_rpc_extension AS BLOB)) BETWEEN 1 AND 4096),
  pi_subagents_extension TEXT NOT NULL CHECK (length(CAST(pi_subagents_extension AS BLOB)) BETWEEN 1 AND 4096),
  cursor_extension TEXT NOT NULL DEFAULT '' CHECK (length(CAST(cursor_extension AS BLOB)) <= 4096),
  test_extensions_json TEXT NOT NULL DEFAULT '[]' CHECK (length(CAST(test_extensions_json AS BLOB)) <= 131072),
  environment_json TEXT NOT NULL DEFAULT '[]' CHECK (length(CAST(environment_json AS BLOB)) <= 131072),
  recipe_sha256 TEXT NOT NULL CHECK (length(recipe_sha256)=64 AND recipe_sha256=lower(recipe_sha256) AND recipe_sha256 NOT GLOB '*[^0-9a-f]*'),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- One action is keyed by the complete old runtime fence. The partial index
-- permits retained terminal history while allowing only one active owner per
-- agent. Target identity is write-once and remains durable across crashes.
CREATE TABLE managed_root_recovery_actions (
  action_id TEXT PRIMARY KEY CHECK (length(CAST(action_id AS BLOB)) BETWEEN 1 AND 128),
  agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  old_pi_session_id TEXT NOT NULL CHECK (length(CAST(old_pi_session_id AS BLOB)) BETWEEN 1 AND 128),
  old_session_epoch INTEGER NOT NULL CHECK (old_session_epoch >= 1),
  old_process_pid INTEGER NOT NULL CHECK (old_process_pid > 0),
  old_process_start_marker TEXT NOT NULL CHECK (length(CAST(old_process_start_marker AS BLOB)) BETWEEN 1 AND 256),
  old_process_incarnation TEXT NOT NULL CHECK (length(CAST(old_process_incarnation AS BLOB)) BETWEEN 1 AND 128),
  old_runtime_generation TEXT NOT NULL CHECK (length(CAST(old_runtime_generation AS BLOB)) BETWEEN 1 AND 128),
  old_manifest_revision INTEGER NOT NULL CHECK (old_manifest_revision >= 1),
  old_manifest_hash TEXT NOT NULL CHECK (length(old_manifest_hash)=64 AND old_manifest_hash=lower(old_manifest_hash) AND old_manifest_hash NOT GLOB '*[^0-9a-f]*'),
  recipe_sha256 TEXT NOT NULL CHECK (length(recipe_sha256)=64 AND recipe_sha256=lower(recipe_sha256) AND recipe_sha256 NOT GLOB '*[^0-9a-f]*'),
  target_runtime_generation TEXT NOT NULL CHECK (length(CAST(target_runtime_generation AS BLOB)) BETWEEN 1 AND 128),
  target_process_pid INTEGER,
  target_process_start_marker TEXT,
  target_process_incarnation TEXT,
  stage TEXT NOT NULL CHECK (stage IN ('claimed','reaping','stopped','starting','ready','failed')),
  unexpected_exit_count INTEGER NOT NULL DEFAULT 0 CHECK (unexpected_exit_count BETWEEN 0 AND 5),
  failure_code TEXT CHECK (failure_code IS NULL OR length(CAST(failure_code AS BLOB)) BETWEEN 1 AND 128),
  failure_message TEXT CHECK (failure_message IS NULL OR length(CAST(failure_message AS BLOB)) <= 512),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  expires_at TEXT,
  UNIQUE(agent_id, old_pi_session_id, old_session_epoch, old_process_pid, old_process_start_marker, old_process_incarnation, old_runtime_generation, old_manifest_revision, old_manifest_hash),
  CHECK ((target_process_pid IS NULL AND target_process_start_marker IS NULL AND target_process_incarnation IS NULL) OR (target_process_pid > 0 AND length(CAST(target_process_start_marker AS BLOB)) BETWEEN 1 AND 256 AND length(CAST(target_process_incarnation AS BLOB)) BETWEEN 1 AND 128)),
  CHECK ((stage='failed') = (failure_code IS NOT NULL AND failure_message IS NOT NULL)),
  CHECK ((stage IN ('ready','failed')) = (expires_at IS NOT NULL))
);
CREATE UNIQUE INDEX managed_root_recovery_active_agent_idx
  ON managed_root_recovery_actions(agent_id) WHERE stage NOT IN ('ready','failed');
CREATE INDEX managed_root_recovery_retention_idx
  ON managed_root_recovery_actions(expires_at, agent_id) WHERE expires_at IS NOT NULL;
CREATE INDEX managed_launch_recipes_hash_idx
  ON managed_launch_recipes(recipe_sha256);

-- Downgrade convention: remove the indexes, then the action table, then the
-- recipe table. No existing managed-agent columns or rows are rewritten.
