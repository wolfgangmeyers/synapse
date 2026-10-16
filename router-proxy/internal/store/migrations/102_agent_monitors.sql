-- Agent Monitors for System 1 monitoring
ALTER TABLE workflow_states ADD COLUMN monitors_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(monitors_json));

CREATE TABLE IF NOT EXISTS managed_agent_monitors (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  workflow_state_id TEXT,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  interval_seconds INTEGER NOT NULL DEFAULT 30,
  next_eval_at TEXT NOT NULL,
  last_evaluated_at TEXT,
  cooldown_seconds INTEGER NOT NULL DEFAULT 120,
  last_fired_at TEXT,
  consecutive_alerts INTEGER NOT NULL DEFAULT 0,
  max_alerts INTEGER NOT NULL DEFAULT 0,
  trailing_messages_count INTEGER NOT NULL DEFAULT 6,
  question_type TEXT NOT NULL DEFAULT 'noul',
  question_instructions TEXT NOT NULL,
  question_criteria_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(question_criteria_json)),
  question_options_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(question_options_json)),
  condition_operator TEXT NOT NULL DEFAULT 'gte',
  condition_threshold TEXT NOT NULL DEFAULT '0.8',
  nudge_template TEXT NOT NULL,
  fallback_mode TEXT NOT NULL DEFAULT 'skip',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(agent_id) REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  FOREIGN KEY(workflow_state_id) REFERENCES workflow_states(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_agent_monitors_agent ON managed_agent_monitors(agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_monitors_due ON managed_agent_monitors(enabled, next_eval_at);
