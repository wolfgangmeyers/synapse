-- Rebuild managed_subagents to remove closed status check constraints per Postel's Law.
-- Clean up any lingering stuck running subagents and mark their managed agents terminated.
CREATE TABLE managed_subagents_096_new (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  parent_agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  parent_session_id TEXT NOT NULL,
  parent_epoch INTEGER NOT NULL CHECK(parent_epoch > 0),
  parent_generation TEXT NOT NULL,
  description TEXT NOT NULL,
  subagent_type TEXT NOT NULL,
  status TEXT NOT NULL,
  max_turns INTEGER NOT NULL CHECK(max_turns >= 0),
  turn_count INTEGER NOT NULL DEFAULT 0 CHECK(turn_count >= 0),
  tool_use_count INTEGER NOT NULL DEFAULT 0 CHECK(tool_use_count >= 0),
  total_tokens INTEGER NOT NULL DEFAULT 0 CHECK(total_tokens >= 0),
  result_text TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  terminal_at TEXT,
  attempt INTEGER NOT NULL DEFAULT 1 CHECK(attempt > 0),
  duration_seconds REAL NOT NULL DEFAULT 0 CHECK(duration_seconds >= 0),
  resume_prompt TEXT NOT NULL DEFAULT '',
  CHECK(agent_id <> parent_agent_id)
);

INSERT INTO managed_subagents_096_new (
  agent_id, parent_agent_id, parent_session_id, parent_epoch, parent_generation,
  description, subagent_type, status, max_turns, turn_count, tool_use_count,
  total_tokens, result_text, error, created_at, updated_at, terminal_at,
  attempt, duration_seconds, resume_prompt
) SELECT
  agent_id, parent_agent_id, parent_session_id, parent_epoch, parent_generation,
  description, subagent_type, status, max_turns, turn_count, tool_use_count,
  total_tokens, result_text, error, created_at, updated_at, terminal_at,
  attempt, duration_seconds, resume_prompt
FROM managed_subagents;

DROP TABLE managed_subagents;
ALTER TABLE managed_subagents_096_new RENAME TO managed_subagents;
CREATE INDEX managed_subagents_parent_idx ON managed_subagents(parent_agent_id, agent_id);
CREATE INDEX managed_subagents_retention_idx ON managed_subagents(terminal_at) WHERE terminal_at IS NOT NULL;

UPDATE managed_subagents SET status='stopped', terminal_at=CURRENT_TIMESTAMP WHERE status='running';

UPDATE managed_agents SET desired_state='terminated', lifecycle_state='terminated', activity_state='stopped', accepting_prompts=0, updated_at=CURRENT_TIMESTAMP
WHERE agent_id IN (SELECT agent_id FROM managed_subagents WHERE status='stopped') AND lifecycle_state='running';
