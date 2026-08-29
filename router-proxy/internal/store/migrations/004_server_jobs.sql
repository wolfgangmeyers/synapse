CREATE TABLE server_jobs (
  job_key TEXT PRIMARY KEY,
  schedule_interval_hours INTEGER NOT NULL CHECK (
    schedule_interval_hours > 0 AND 24 % schedule_interval_hours = 0
  ),
  schedule_anchor_hour INTEGER NOT NULL CHECK (
    schedule_anchor_hour >= 0 AND schedule_anchor_hour < 24
  ),
  schedule_minute INTEGER NOT NULL CHECK (schedule_minute >= 0 AND schedule_minute < 60),
  schedule_time_zone TEXT NOT NULL,
  definition_fingerprint TEXT NOT NULL,
  next_due_at TEXT NOT NULL
);

CREATE TABLE server_job_runs (
  run_id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_key TEXT NOT NULL REFERENCES server_jobs(job_key) ON DELETE CASCADE,
  scheduled_for TEXT NOT NULL,
  attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
  outcome TEXT NOT NULL CHECK (outcome IN ('succeeded', 'failed', 'failed_terminal')),
  attempted_at TEXT NOT NULL,
  retry_at TEXT,
  error_code TEXT,
  message_id TEXT,
  CHECK (
    (outcome = 'succeeded' AND retry_at IS NULL AND error_code IS NULL AND message_id IS NOT NULL)
    OR (outcome = 'failed' AND retry_at IS NOT NULL AND error_code IS NOT NULL AND message_id IS NULL)
    OR (outcome = 'failed_terminal' AND retry_at IS NULL AND error_code IS NOT NULL AND message_id IS NULL)
  ),
  UNIQUE (job_key, scheduled_for, attempt_number)
);

CREATE INDEX server_jobs_due_idx ON server_jobs (next_due_at);
CREATE INDEX server_job_runs_occurrence_idx
  ON server_job_runs (job_key, scheduled_for, attempt_number DESC);
