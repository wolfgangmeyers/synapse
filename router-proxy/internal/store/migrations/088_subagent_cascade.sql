ALTER TABLE managed_agents ADD COLUMN parent_agent_id TEXT;
CREATE INDEX IF NOT EXISTS managed_agents_parent_idx ON managed_agents (parent_agent_id);
UPDATE managed_agents SET parent_agent_id = (SELECT parent_agent_id FROM managed_subagents WHERE managed_subagents.agent_id = managed_agents.agent_id) WHERE agent_id IN (SELECT agent_id FROM managed_subagents);
