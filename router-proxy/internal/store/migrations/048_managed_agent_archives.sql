-- Archive rows are the closed, redacted projection that survives removal of
-- managed operational state. They deliberately have no foreign keys: an
-- archive must remain readable after the live agent and lifecycle rows are
-- cleaned up.
CREATE TABLE managed_agent_archives (
  archive_key TEXT PRIMARY KEY NOT NULL CHECK (
    length(CAST(archive_key AS BLOB)) = 36
    AND archive_key = lower(archive_key)
    AND archive_key NOT GLOB '*[^0-9a-f-]*'
    AND length(replace(archive_key, '-', '')) = 32
    AND substr(archive_key, 9, 1) = '-'
    AND substr(archive_key, 14, 1) = '-'
    AND substr(archive_key, 19, 1) = '-'
    AND substr(archive_key, 24, 1) = '-'
  ),
  agent_id TEXT NOT NULL CHECK (length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 128),
  record_kind TEXT NOT NULL DEFAULT 'archived' CHECK (record_kind = 'archived'),
  lifecycle_state TEXT NOT NULL DEFAULT 'archived' CHECK (lifecycle_state = 'archived'),
  terminal_outcome TEXT NOT NULL CHECK (terminal_outcome IN ('terminated', 'cleanup_failed')),
  completed_at TEXT NOT NULL CHECK (length(CAST(completed_at AS BLOB)) BETWEEN 1 AND 64),
  disposition TEXT NOT NULL CHECK (disposition IN ('no_attempts', 'queued', 'completed', 'released_or_failed', 'unknown')),
  total INTEGER NOT NULL CHECK (total BETWEEN 0 AND 200),
  queued_never_attempted INTEGER NOT NULL CHECK (queued_never_attempted BETWEEN 0 AND 200),
  in_flight_or_unknown INTEGER NOT NULL CHECK (in_flight_or_unknown BETWEEN 0 AND 200),
  completed INTEGER NOT NULL CHECK (completed BETWEEN 0 AND 200),
  released_or_failed INTEGER NOT NULL CHECK (released_or_failed BETWEEN 0 AND 200),
  total_overflow INTEGER NOT NULL DEFAULT 0 CHECK (total_overflow IN (0, 1)),
  queued_never_attempted_overflow INTEGER NOT NULL DEFAULT 0 CHECK (queued_never_attempted_overflow IN (0, 1)),
  in_flight_or_unknown_overflow INTEGER NOT NULL DEFAULT 0 CHECK (in_flight_or_unknown_overflow IN (0, 1)),
  completed_overflow INTEGER NOT NULL DEFAULT 0 CHECK (completed_overflow IN (0, 1)),
  released_or_failed_overflow INTEGER NOT NULL DEFAULT 0 CHECK (released_or_failed_overflow IN (0, 1))
);
CREATE INDEX managed_agent_archives_recency_idx
  ON managed_agent_archives(completed_at DESC, archive_key DESC);
CREATE INDEX managed_agent_archives_agent_idx
  ON managed_agent_archives(agent_id, completed_at DESC, archive_key DESC);

-- Termination progress stays on the existing lifecycle action until the final
-- durable write. Every new field is nullable so databases opened at 047 keep
-- their existing actions valid and non-termination actions need no backfill.
ALTER TABLE managed_lifecycle_actions ADD COLUMN archive_key TEXT CHECK (
  archive_key IS NULL OR (
    length(CAST(archive_key AS BLOB)) = 36
    AND archive_key = lower(archive_key)
    AND archive_key NOT GLOB '*[^0-9a-f-]*'
    AND length(replace(archive_key, '-', '')) = 32
    AND substr(archive_key, 9, 1) = '-'
    AND substr(archive_key, 14, 1) = '-'
    AND substr(archive_key, 19, 1) = '-'
    AND substr(archive_key, 24, 1) = '-'
  )
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_cutoff_enqueue_sequence INTEGER CHECK (
  delivery_cutoff_enqueue_sequence IS NULL OR delivery_cutoff_enqueue_sequence >= 0
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_disposition TEXT CHECK (
  delivery_aggregate_disposition IS NULL OR delivery_aggregate_disposition IN ('no_attempts', 'queued', 'completed', 'released_or_failed', 'unknown')
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_total INTEGER CHECK (
  delivery_aggregate_total IS NULL OR delivery_aggregate_total BETWEEN 0 AND 200
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_queued_never_attempted INTEGER CHECK (
  delivery_aggregate_queued_never_attempted IS NULL OR delivery_aggregate_queued_never_attempted BETWEEN 0 AND 200
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_in_flight_or_unknown INTEGER CHECK (
  delivery_aggregate_in_flight_or_unknown IS NULL OR delivery_aggregate_in_flight_or_unknown BETWEEN 0 AND 200
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_completed INTEGER CHECK (
  delivery_aggregate_completed IS NULL OR delivery_aggregate_completed BETWEEN 0 AND 200
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_released_or_failed INTEGER CHECK (
  delivery_aggregate_released_or_failed IS NULL OR delivery_aggregate_released_or_failed BETWEEN 0 AND 200
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_total_overflow INTEGER CHECK (
  delivery_aggregate_total_overflow IS NULL OR delivery_aggregate_total_overflow IN (0, 1)
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_queued_never_attempted_overflow INTEGER CHECK (
  delivery_aggregate_queued_never_attempted_overflow IS NULL OR delivery_aggregate_queued_never_attempted_overflow IN (0, 1)
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_in_flight_or_unknown_overflow INTEGER CHECK (
  delivery_aggregate_in_flight_or_unknown_overflow IS NULL OR delivery_aggregate_in_flight_or_unknown_overflow IN (0, 1)
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_completed_overflow INTEGER CHECK (
  delivery_aggregate_completed_overflow IS NULL OR delivery_aggregate_completed_overflow IN (0, 1)
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_released_or_failed_overflow INTEGER CHECK (
  delivery_aggregate_released_or_failed_overflow IS NULL OR delivery_aggregate_released_or_failed_overflow IN (0, 1)
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_checkpoint_enqueue_sequence INTEGER CHECK (
  delivery_checkpoint_enqueue_sequence IS NULL OR delivery_checkpoint_enqueue_sequence >= 0
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_checkpoint_message_id TEXT CHECK (
  delivery_checkpoint_message_id IS NULL OR length(CAST(delivery_checkpoint_message_id AS BLOB)) BETWEEN 1 AND 128
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_aggregate_complete INTEGER CHECK (
  delivery_aggregate_complete IS NULL OR delivery_aggregate_complete IN (0, 1)
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN delivery_frozen_at TEXT CHECK (
  delivery_frozen_at IS NULL OR length(CAST(delivery_frozen_at AS BLOB)) BETWEEN 1 AND 64
);
ALTER TABLE managed_lifecycle_actions ADD COLUMN archive_outcome TEXT CHECK (
  archive_outcome IS NULL OR archive_outcome IN ('terminated', 'cleanup_failed')
);

CREATE INDEX managed_lifecycle_archive_key_idx
  ON managed_lifecycle_actions(archive_key)
  WHERE archive_key IS NOT NULL;
