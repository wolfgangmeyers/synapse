-- Managed direct delivery keeps transport selection, attempts, and diagnostics
-- in router-owned SQLite state.  The ordinary messages table remains the source
-- of truth and cascades snapshots and attempts only after terminal settlement.
CREATE TABLE recipient_sources (
  recipient_name TEXT PRIMARY KEY,
  source_kind TEXT NOT NULL CHECK (source_kind IN ('managed', 'tmux')),
  owner_identity TEXT NOT NULL CHECK (length(CAST(owner_identity AS BLOB)) > 0),
  state TEXT NOT NULL CHECK (state IN ('reserved', 'active', 'conflicted', 'retired')),
  conflict_source_kind TEXT,
  conflict_owner_identity TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (
    (state = 'conflicted'
      AND conflict_source_kind IN ('managed', 'tmux')
      AND conflict_source_kind <> source_kind
      AND length(CAST(conflict_owner_identity AS BLOB)) > 0)
    OR
    (state <> 'conflicted' AND conflict_source_kind IS NULL AND conflict_owner_identity IS NULL)
  )
);

CREATE TABLE message_target_snapshots (
  snapshot_id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
  message_incarnation_id TEXT NOT NULL,
  recipient_name TEXT NOT NULL,
  source_kind TEXT NOT NULL CHECK (source_kind IN ('managed', 'tmux')),
  owner_identity TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY (recipient_name) REFERENCES recipient_sources(recipient_name) ON DELETE RESTRICT
);

CREATE TABLE managed_direct_delivery_attempts (
  message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  message_incarnation_id TEXT NOT NULL,
  attempt_number INTEGER NOT NULL CHECK (attempt_number BETWEEN 1 AND 5),
  snapshot_id TEXT NOT NULL REFERENCES message_target_snapshots(snapshot_id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id),
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  process_pid INTEGER NOT NULL CHECK (process_pid > 0),
  process_start_marker TEXT NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  manifest_revision INTEGER NOT NULL CHECK (manifest_revision >= 1),
  manifest_hash TEXT NOT NULL CHECK (length(manifest_hash) = 64 AND manifest_hash = lower(manifest_hash) AND manifest_hash NOT GLOB '*[^0-9a-f]*'),
  state TEXT NOT NULL CHECK (state IN ('direct_pending', 'direct_prompted', 'acknowledged', 'released', 'failed')),
  session_entry_watermark TEXT,
  receipt_entry_identity TEXT,
  created_at TEXT NOT NULL,
  prompted_at TEXT,
  acknowledged_at TEXT,
  released_at TEXT,
  expires_at TEXT NOT NULL,
  next_attempt_at TEXT,
  failure_code TEXT,
  failure_detail TEXT,
  PRIMARY KEY (message_id, message_incarnation_id, attempt_number),
  CHECK ((state = 'released' AND next_attempt_at IS NOT NULL) OR (state <> 'released' AND next_attempt_at IS NULL)),
  CHECK (failure_code IS NULL OR (length(CAST(failure_code AS BLOB)) BETWEEN 1 AND 128)),
  CHECK (failure_detail IS NULL OR length(CAST(failure_detail AS BLOB)) <= 1024)
);

CREATE UNIQUE INDEX managed_direct_delivery_active_attempt_idx
  ON managed_direct_delivery_attempts(message_id, message_incarnation_id)
  WHERE state IN ('direct_pending', 'direct_prompted');
CREATE INDEX managed_direct_delivery_retry_idx
  ON managed_direct_delivery_attempts(agent_id, next_attempt_at, message_id)
  WHERE state = 'released' AND next_attempt_at IS NOT NULL;
CREATE INDEX recipient_sources_lookup_idx
  ON recipient_sources(source_kind, state, owner_identity);
CREATE INDEX message_target_snapshots_owner_idx
  ON message_target_snapshots(source_kind, owner_identity, message_id);
CREATE INDEX managed_direct_delivery_message_idx
  ON managed_direct_delivery_attempts(message_id, attempt_number DESC);

CREATE TABLE managed_direct_delivery_diagnostics (
  diagnostic_id INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_id TEXT NOT NULL REFERENCES managed_agents(agent_id),
  message_id TEXT,
  message_incarnation_id TEXT,
  attempt_number INTEGER,
  snapshot_id TEXT,
  code TEXT NOT NULL CHECK (length(CAST(code AS BLOB)) BETWEEN 1 AND 128),
  detail TEXT CHECK (detail IS NULL OR length(CAST(detail AS BLOB)) <= 1024),
  observed_at TEXT NOT NULL
);
CREATE INDEX managed_direct_delivery_diagnostics_recency_idx
  ON managed_direct_delivery_diagnostics(agent_id, observed_at DESC, diagnostic_id DESC);
