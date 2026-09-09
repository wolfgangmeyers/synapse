-- Quiet eligibility is decided at the time an ordinary message is committed.
-- It is never inferred from a mutable display name or later configuration.
ALTER TABLE conversation_messages ADD COLUMN quiet_eligible INTEGER NOT NULL DEFAULT 0
  CHECK (quiet_eligible IN (0, 1));
CREATE INDEX conversation_messages_quiet_history_idx
  ON conversation_messages (quiet_eligible, conversation_sequence DESC);

-- Complete history holds short, redacted operational summaries separately from
-- ordinary messages, so its fixed cap cannot evict quiet communication.
CREATE TABLE managed_complete_history (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_id TEXT,
  category TEXT NOT NULL CHECK (length(CAST(category AS BLOB)) BETWEEN 1 AND 64),
  level TEXT NOT NULL CHECK (length(CAST(level AS BLOB)) BETWEEN 1 AND 32),
  message TEXT NOT NULL CHECK (length(CAST(message AS BLOB)) BETWEEN 1 AND 512),
  occurred_at TEXT NOT NULL
);
CREATE INDEX managed_complete_history_sequence_idx ON managed_complete_history (sequence DESC);
CREATE INDEX managed_complete_history_agent_idx ON managed_complete_history (agent_id, sequence DESC);
