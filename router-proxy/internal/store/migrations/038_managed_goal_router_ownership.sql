-- Router-owned managed goal state survives process and router replacement. The
-- row is deliberately scoped to the current launch history; terminate cascades
-- it, while reset retains it for the same managed-agent lifetime.
CREATE TABLE managed_agent_policies (
  agent_id TEXT PRIMARY KEY REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  history_scope_id TEXT NOT NULL REFERENCES managed_history_incarnations(incarnation_id) ON DELETE CASCADE,
  desired_revision INTEGER NOT NULL DEFAULT 0 CHECK (desired_revision >= 0),
  applied_revision INTEGER NOT NULL DEFAULT 0 CHECK (applied_revision >= 0 AND applied_revision <= desired_revision),
  goal_enabled INTEGER NOT NULL DEFAULT 0 CHECK (goal_enabled IN (0,1)),
  goal_text TEXT NOT NULL DEFAULT '',
  settings_json TEXT NOT NULL,
  projection_state TEXT NOT NULL DEFAULT 'applied' CHECK (projection_state IN ('pending','applied','failed')),
  projection_attempts INTEGER NOT NULL DEFAULT 0 CHECK (projection_attempts >= 0 AND projection_attempts <= 8),
  next_projection_at TEXT,
  last_error_code TEXT,
  last_error_message TEXT,
  last_error_at TEXT,
  evaluator_state TEXT NOT NULL DEFAULT 'idle' CHECK (evaluator_state IN ('idle','scheduled','evaluating','snoozed','done')),
  evaluator_attempts INTEGER NOT NULL DEFAULT 0 CHECK (evaluator_attempts >= 0 AND evaluator_attempts <= 1000),
  not_done_wakeups INTEGER NOT NULL DEFAULT 0 CHECK (not_done_wakeups >= 0 AND not_done_wakeups <= 1000),
  next_wakeup_at TEXT,
  snoozed_until TEXT,
  last_settled_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_settled_sequence >= 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX managed_agent_policies_projection_retry_idx
  ON managed_agent_policies (projection_state, next_projection_at);
CREATE INDEX managed_agent_policies_goal_wakeup_idx
  ON managed_agent_policies (evaluator_state, next_wakeup_at);
CREATE INDEX managed_agent_policies_history_scope_idx
  ON managed_agent_policies (history_scope_id, updated_at DESC);
