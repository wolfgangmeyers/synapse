-- The retired RPC extension is optional for embedded launches. Rebuild only
-- this leaf table: it has no incoming foreign keys or schema-owned triggers.
-- Keep all legacy values, hashes and timestamps unchanged.
CREATE TABLE managed_launch_recipes_131 (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  pi_executable TEXT NOT NULL CHECK (length(CAST(pi_executable AS BLOB)) BETWEEN 1 AND 4096),
  session_root TEXT NOT NULL CHECK (length(CAST(session_root AS BLOB)) BETWEEN 1 AND 4096),
  cwd TEXT NOT NULL CHECK (length(CAST(cwd AS BLOB)) BETWEEN 1 AND 4096),
  provider TEXT NOT NULL CHECK (length(CAST(provider AS BLOB)) BETWEEN 1 AND 128),
  model_id TEXT NOT NULL CHECK (length(CAST(model_id AS BLOB)) BETWEEN 1 AND 256),
  mecha_rpc_extension TEXT NOT NULL CHECK (length(CAST(mecha_rpc_extension AS BLOB)) <= 4096),
  pi_subagents_extension TEXT NOT NULL CHECK (length(CAST(pi_subagents_extension AS BLOB)) BETWEEN 1 AND 4096),
  cursor_extension TEXT NOT NULL DEFAULT '' CHECK (length(CAST(cursor_extension AS BLOB)) <= 4096),
  test_extensions_json TEXT NOT NULL DEFAULT '[]' CHECK (length(CAST(test_extensions_json AS BLOB)) <= 131072),
  environment_json TEXT NOT NULL DEFAULT '[]' CHECK (length(CAST(environment_json AS BLOB)) <= 131072),
  recipe_sha256 TEXT NOT NULL CHECK (length(recipe_sha256)=64 AND recipe_sha256=lower(recipe_sha256) AND recipe_sha256 NOT GLOB '*[^0-9a-f]*'),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  antigravity_extension TEXT NOT NULL DEFAULT '' CHECK (length(CAST(antigravity_extension AS BLOB)) <= 4096)
);
INSERT INTO managed_launch_recipes_131 (
  agent_id, pi_executable, session_root, cwd, provider, model_id,
  mecha_rpc_extension, pi_subagents_extension, cursor_extension,
  test_extensions_json, environment_json, recipe_sha256, created_at, updated_at,
  antigravity_extension
) SELECT
  agent_id, pi_executable, session_root, cwd, provider, model_id,
  mecha_rpc_extension, pi_subagents_extension, cursor_extension,
  test_extensions_json, environment_json, recipe_sha256, created_at, updated_at,
  antigravity_extension
FROM managed_launch_recipes;
DROP TABLE managed_launch_recipes;
ALTER TABLE managed_launch_recipes_131 RENAME TO managed_launch_recipes;
CREATE INDEX managed_launch_recipes_hash_idx ON managed_launch_recipes(recipe_sha256);
