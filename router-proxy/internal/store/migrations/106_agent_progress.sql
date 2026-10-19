-- Add progress columns to managed_agents
ALTER TABLE managed_agents ADD COLUMN progress_percent INTEGER;
ALTER TABLE managed_agents ADD COLUMN progress_note TEXT;
ALTER TABLE managed_agents ADD COLUMN progress_updated_at TIMESTAMP;

-- Create agent_progress_history table for auditing and time-series progress tracking
CREATE TABLE IF NOT EXISTS agent_progress_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
    percent INTEGER NOT NULL,
    note TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_agent_progress_history_agent_id ON agent_progress_history(agent_id, created_at ASC);
