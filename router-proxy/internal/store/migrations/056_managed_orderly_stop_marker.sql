-- Persist router-owned orderly shutdown intent so a restart can distinguish it
-- from an unexpected process loss without trusting the stopped projection alone.
CREATE TABLE managed_orderly_stop_markers (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL,
  process_pid INTEGER NOT NULL,
  process_start_marker TEXT NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  manifest_revision INTEGER NOT NULL,
  manifest_hash TEXT NOT NULL,
  marked_at TEXT NOT NULL
);
