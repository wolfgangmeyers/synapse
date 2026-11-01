-- Migration 119: Structured workflow schedules
-- Cleanly recreate workflow_schedules to drop legacy schedule_type CHECK, interval_ms, and cron_expression,
-- and introduce schedule_json.

CREATE TABLE workflow_schedules_new (
  id TEXT PRIMARY KEY,
  workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 256),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  schedule_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(schedule_json)),
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

-- Migrate existing rows if present
INSERT INTO workflow_schedules_new (
  id, workflow_id, name, enabled, schedule_json, variables_json,
  target_cwd, model_provider, model_id, next_run_at,
  last_run_at, last_run_status, last_run_error, created_at, updated_at
)
SELECT
  id, workflow_id, name, enabled,
  CASE
    WHEN schedule_type = 'interval' AND interval_ms IS NOT NULL THEN
      json_object('days_mode', 'every_day', 'time_mode', 'repeating_interval', 'interval', json_object('value', MAX(1, interval_ms / 60000), 'unit', 'minutes'), 'timezone', 'UTC')
    ELSE
      json_object('days_mode', 'every_day', 'time_mode', 'repeating_interval', 'interval', json_object('value', 60, 'unit', 'minutes'), 'timezone', 'UTC')
  END,
  variables_json, target_cwd, model_provider, model_id, next_run_at,
  last_run_at, last_run_status, last_run_error, created_at, updated_at
FROM workflow_schedules;

DROP TABLE workflow_schedules;

ALTER TABLE workflow_schedules_new RENAME TO workflow_schedules;

CREATE INDEX IF NOT EXISTS idx_workflow_schedules_enabled_next_run ON workflow_schedules(enabled, next_run_at);
CREATE INDEX IF NOT EXISTS idx_workflow_schedules_workflow_id ON workflow_schedules(workflow_id);
