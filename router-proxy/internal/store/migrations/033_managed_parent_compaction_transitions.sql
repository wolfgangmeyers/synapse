-- Parent compaction has its own source-fenced transition ledger. The live
-- managed-agent activity is only a projection; this table retains the exact
-- Pi event sequence and its one complete-history row across reconnects.
CREATE TABLE managed_parent_compaction_state (
  agent_id TEXT NOT NULL,
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  source_sequence INTEGER NOT NULL,
  compacting INTEGER NOT NULL CHECK (compacting IN (0,1)),
  updated_at TEXT NOT NULL,
  PRIMARY KEY (agent_id, pi_session_id, session_epoch, process_incarnation, runtime_generation)
);

CREATE TABLE managed_parent_compaction_transitions (
  event_key TEXT PRIMARY KEY,
  complete_sequence INTEGER NOT NULL,
  agent_id TEXT NOT NULL,
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  source_sequence INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('compacting','idle')),
  outcome TEXT CHECK (outcome IN ('completed','failed','aborted','reconciled_unknown')),
  occurred_at TEXT NOT NULL,
  FOREIGN KEY (complete_sequence) REFERENCES managed_complete_history(sequence) ON DELETE CASCADE
);
CREATE INDEX managed_parent_compaction_transitions_agent_sequence_idx
  ON managed_parent_compaction_transitions (agent_id, complete_sequence DESC);
