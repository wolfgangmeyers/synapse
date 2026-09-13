-- Native Pi builtin lifecycle rows link one mutable Complete entry to its full
-- managed-runtime fence. The foreign key makes global Complete retention remove
-- the dedupe record at the same time as the visible entry.
CREATE TABLE managed_builtin_tool_invocations (
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
  tool_name TEXT NOT NULL CHECK (tool_name IN ('read','bash','edit','write')),
  state TEXT NOT NULL CHECK (state IN ('active','succeeded','failed','interrupted')),
  error_code TEXT,
  started_at TEXT NOT NULL,
  terminal_at TEXT,
  complete_sequence INTEGER NOT NULL UNIQUE REFERENCES managed_complete_history(sequence) ON DELETE CASCADE,
  UNIQUE(agent_id, pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, manifest_revision, manifest_sha256, tool_call_id)
);
CREATE INDEX managed_builtin_tool_invocations_active_fence_idx
  ON managed_builtin_tool_invocations(agent_id, pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, manifest_revision, manifest_sha256, state);
