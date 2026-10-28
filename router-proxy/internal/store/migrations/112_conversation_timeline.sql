CREATE TABLE IF NOT EXISTS conversation_timeline (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL,
  role TEXT,
  content TEXT,
  metadata TEXT,
  created_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_conversation_timeline_agent_seq
  ON conversation_timeline (agent_id, seq);

CREATE INDEX IF NOT EXISTS idx_conversation_timeline_agent_created
  ON conversation_timeline (agent_id, created_at ASC);
