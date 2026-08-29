ALTER TABLE sentinel_occurrences ADD COLUMN launch_deadline_at TEXT;
CREATE INDEX sentinel_launch_deadline ON sentinel_occurrences(launch_deadline_at)
  WHERE state IN ('claimed', 'launching');
