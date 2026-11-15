CREATE TABLE workflow_interruption_markers (
 id INTEGER PRIMARY KEY,
 agent_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 epoch INTEGER NOT NULL,
 generation TEXT NOT NULL,
 user_seq INTEGER NOT NULL,
 user_digest TEXT NOT NULL,
 goal_revision INTEGER NOT NULL,
 final_seq INTEGER NOT NULL,
 final_digest TEXT NOT NULL,
 owner_token INTEGER NOT NULL,
 execution_token INTEGER NOT NULL,
 recorded_at_ms INTEGER NOT NULL,
 UNIQUE(agent_id,session_id,epoch,generation,owner_token,execution_token),
 FOREIGN KEY(agent_id) REFERENCES managed_agents(agent_id) ON DELETE CASCADE
);
CREATE INDEX workflow_interruption_markers_agent ON workflow_interruption_markers(agent_id,recorded_at_ms DESC,id DESC);
