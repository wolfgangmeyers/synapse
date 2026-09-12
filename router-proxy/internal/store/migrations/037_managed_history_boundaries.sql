-- A reused managed-agent name gets a new active history window. Complete rows
-- stay bounded for audit, but the normal agent endpoint never crosses from a
-- new launch into an earlier lifetime.
CREATE TABLE managed_history_incarnations (
  incarnation_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  start_sequence INTEGER NOT NULL CHECK (start_sequence >= 0),
  created_at TEXT NOT NULL,
  closed_at TEXT
);
CREATE INDEX managed_history_incarnations_agent_created_idx
  ON managed_history_incarnations (agent_id, created_at DESC);

CREATE TABLE managed_history_boundaries (
  agent_id TEXT PRIMARY KEY,
  incarnation_id TEXT NOT NULL UNIQUE,
  start_sequence INTEGER NOT NULL CHECK (start_sequence >= 0),
  created_at TEXT NOT NULL,
  FOREIGN KEY (agent_id) REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  FOREIGN KEY (incarnation_id) REFERENCES managed_history_incarnations(incarnation_id)
);

-- Existing live agents retain their current timeline under a distinct legacy
-- incarnation. Agents already terminated before this migration have no active
-- boundary, so the default endpoint cannot reveal their old rows.
INSERT INTO managed_history_incarnations (incarnation_id, agent_id, start_sequence, created_at)
  SELECT 'legacy:' || agent_id, agent_id, 0, created_at FROM managed_agents;
INSERT INTO managed_history_boundaries (agent_id, incarnation_id, start_sequence, created_at)
  SELECT agent_id, 'legacy:' || agent_id, 0, created_at FROM managed_agents;

