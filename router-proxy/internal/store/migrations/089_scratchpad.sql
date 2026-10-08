CREATE TABLE IF NOT EXISTS scratchpad_entries (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    content TEXT NOT NULL,
    pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scratchpad_agent_created ON scratchpad_entries(agent_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_scratchpad_agent_pinned ON scratchpad_entries(agent_id, pinned, created_at DESC);
