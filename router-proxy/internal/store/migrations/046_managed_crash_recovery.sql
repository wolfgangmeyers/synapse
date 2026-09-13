-- Keep one bounded retry row per managed agent. The row stores only the
-- first retry deadline; later crash-policy fields belong to later migrations.
CREATE TABLE managed_crash_recovery (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  retry_at TEXT,
  updated_at TEXT NOT NULL
);

INSERT INTO managed_crash_recovery(agent_id, updated_at)
  SELECT agent_id, updated_at FROM managed_agents;
CREATE INDEX managed_crash_recovery_retry_idx
  ON managed_crash_recovery(retry_at, agent_id) WHERE retry_at IS NOT NULL;
