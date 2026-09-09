-- Child lifecycle evidence has a separate durable identity from the browser
-- projection.  A later snapshot can replace the projection, but it cannot
-- silently erase the exact fenced transition or recovery decision that led to
-- it.  Details are normalized, bounded source data only.
CREATE TABLE managed_child_transitions (
  event_key TEXT PRIMARY KEY,
  complete_sequence INTEGER NOT NULL,
  agent_id TEXT NOT NULL,
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  parent_entry_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  source_sequence INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('transition', 'recovery')),
  lifecycle_state TEXT NOT NULL,
  recovery_state TEXT NOT NULL CHECK (recovery_state IN ('confirmed', 'interrupted_unknown')),
  recovery_cause TEXT CHECK (recovery_cause IN ('snapshot_omitted', 'parent_process_lost', 'session_changed')),
  details_json TEXT NOT NULL CHECK (length(CAST(details_json AS BLOB)) BETWEEN 2 AND 8192),
  occurred_at TEXT NOT NULL,
  FOREIGN KEY (complete_sequence) REFERENCES managed_complete_history(sequence) ON DELETE CASCADE
);
CREATE INDEX managed_child_transitions_agent_sequence_idx
  ON managed_child_transitions (agent_id, complete_sequence DESC);

-- Recovery rows preserve a prior active child until a current snapshot either
-- confirms it or explicitly omits it.  They contain no raw session/process
-- output and use the same 200-record bound as complete history.
CREATE TABLE managed_child_recoveries (
  agent_id TEXT NOT NULL,
  prior_pi_session_id TEXT NOT NULL,
  prior_session_epoch INTEGER NOT NULL,
  prior_process_incarnation TEXT NOT NULL,
  prior_runtime_generation TEXT NOT NULL,
  parent_entry_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  source_sequence INTEGER NOT NULL,
  run_json TEXT NOT NULL CHECK (length(CAST(run_json AS BLOB)) BETWEEN 2 AND 8192),
  recovery_state TEXT NOT NULL CHECK (recovery_state = 'interrupted_unknown'),
  recovery_cause TEXT NOT NULL CHECK (recovery_cause IN ('snapshot_omitted', 'parent_process_lost', 'session_changed')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (agent_id, prior_pi_session_id, prior_session_epoch, prior_process_incarnation, parent_entry_id, task_id, run_id)
);
CREATE INDEX managed_child_recoveries_agent_updated_idx
  ON managed_child_recoveries (agent_id, updated_at DESC);
