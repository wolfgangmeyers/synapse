-- One managed identity per child. This table is its authoritative parentage
-- marker and mutable subagent state; existing embedded session/history tables
-- remain the only conversation store. The subagent embedded role is deferred
-- until its session binding/runtime seam (065's role CHECK needs a rebuild).
CREATE TABLE managed_subagents (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  parent_agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  parent_session_id TEXT NOT NULL,
  parent_epoch INTEGER NOT NULL CHECK(parent_epoch > 0),
  parent_generation TEXT NOT NULL,
  description TEXT NOT NULL,
  subagent_type TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('queued','running','completed','failed','steered','aborted','stopped','error','interrupted','cancelled')),
  max_turns INTEGER NOT NULL CHECK(max_turns >= 0),
  turn_count INTEGER NOT NULL DEFAULT 0 CHECK(turn_count >= 0),
  tool_use_count INTEGER NOT NULL DEFAULT 0 CHECK(tool_use_count >= 0),
  total_tokens INTEGER NOT NULL DEFAULT 0 CHECK(total_tokens >= 0),
  result_text TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  terminal_at TEXT,
  CHECK(agent_id <> parent_agent_id),
  CHECK((status IN ('queued','running') AND terminal_at IS NULL) OR
        (status NOT IN ('queued','running')))
);
CREATE INDEX managed_subagents_parent_idx ON managed_subagents(parent_agent_id, agent_id);
CREATE INDEX managed_subagents_retention_idx ON managed_subagents(terminal_at) WHERE terminal_at IS NOT NULL;
