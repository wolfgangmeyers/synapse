-- Migration 116: Background workflows and schedules
ALTER TABLE managed_agents ADD COLUMN is_background INTEGER NOT NULL DEFAULT 0 CHECK (is_background IN (0, 1));
CREATE INDEX IF NOT EXISTS idx_managed_agents_background_lifecycle ON managed_agents(is_background, lifecycle_state);

CREATE TABLE IF NOT EXISTS workflow_schedules (
  id TEXT PRIMARY KEY,
  workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 256),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  schedule_type TEXT NOT NULL CHECK (schedule_type IN ('interval', 'cron')),
  interval_ms INTEGER,
  cron_expression TEXT,
  variables_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(variables_json)),
  target_cwd TEXT NOT NULL DEFAULT '',
  model_provider TEXT NOT NULL DEFAULT '',
  model_id TEXT NOT NULL DEFAULT '',
  next_run_at TEXT NOT NULL,
  last_run_at TEXT,
  last_run_status TEXT,
  last_run_error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workflow_schedules_enabled_next_run ON workflow_schedules(enabled, next_run_at);
CREATE INDEX IF NOT EXISTS idx_workflow_schedules_workflow_id ON workflow_schedules(workflow_id);
