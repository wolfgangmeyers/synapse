CREATE TABLE sentinel_occurrences (
  occurrence_id TEXT PRIMARY KEY,
  message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
  state TEXT NOT NULL CHECK (state IN ('claimed', 'launching', 'launched', 'exited', 'failed')),
  attempt_id TEXT NOT NULL,
  transition_sequence INTEGER NOT NULL DEFAULT 1,
  child_identity TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX sentinel_active_message ON sentinel_occurrences(message_id)
  WHERE state IN ('claimed', 'launching', 'launched');
