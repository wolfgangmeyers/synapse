-- Ordinary Pi tools now use the same start/end lifecycle table as the four
-- historical coding builtins so their invocation input can remain attached to
-- one mutable Complete row. Rebuild the table to widen only the tool-name
-- constraint while copying every existing builtin invocation unchanged.
CREATE TABLE managed_tool_invocations_next (
  invocation_key TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  process_pid INTEGER NOT NULL,
  process_start_marker TEXT NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  manifest_revision INTEGER NOT NULL CHECK (manifest_revision >= 1),
  manifest_sha256 TEXT NOT NULL,
  tool_call_id TEXT NOT NULL,
  tool_name TEXT NOT NULL CHECK (
    tool_name <> ''
    AND length(CAST(tool_name AS BLOB)) <= 128
    AND tool_name NOT GLOB '*[^A-Za-z0-9_.:-]*'
  ),
  state TEXT NOT NULL CHECK (state IN ('active','succeeded','failed','interrupted')),
  error_code TEXT,
  started_at TEXT NOT NULL,
  terminal_at TEXT,
  complete_sequence INTEGER NOT NULL UNIQUE REFERENCES managed_complete_history(sequence) ON DELETE CASCADE,
  UNIQUE(agent_id, pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, manifest_revision, manifest_sha256, tool_call_id)
);

INSERT INTO managed_tool_invocations_next(
  invocation_key,agent_id,pi_session_id,session_epoch,process_pid,
  process_start_marker,process_incarnation,runtime_generation,
  manifest_revision,manifest_sha256,tool_call_id,tool_name,state,error_code,
  started_at,terminal_at,complete_sequence
)
SELECT
  invocation_key,agent_id,pi_session_id,session_epoch,process_pid,
  process_start_marker,process_incarnation,runtime_generation,
  manifest_revision,manifest_sha256,tool_call_id,tool_name,state,error_code,
  started_at,terminal_at,complete_sequence
FROM managed_builtin_tool_invocations;

DROP TABLE managed_builtin_tool_invocations;
ALTER TABLE managed_tool_invocations_next RENAME TO managed_builtin_tool_invocations;
CREATE INDEX managed_builtin_tool_invocations_active_fence_idx
  ON managed_builtin_tool_invocations(agent_id, pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, manifest_revision, manifest_sha256, state);
