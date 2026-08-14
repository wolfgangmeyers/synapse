-- Durable lifecycle state deliberately outlives agent leases and presence records.
-- A parent can disconnect while its child tombstones still fence delayed frames.
CREATE TABLE subagent_lifecycle_producers (
  parent_agent_id TEXT PRIMARY KEY CHECK (length(CAST(parent_agent_id AS BLOB)) BETWEEN 1 AND 128),
  instance_id TEXT NOT NULL CHECK (length(CAST(instance_id AS BLOB)) BETWEEN 1 AND 128),
  parent_session_id TEXT NOT NULL CHECK (length(CAST(parent_session_id AS BLOB)) BETWEEN 1 AND 128),
  producer_epoch TEXT NOT NULL CHECK (length(CAST(producer_epoch AS BLOB)) = 36),
  lease_id TEXT NOT NULL CHECK (length(CAST(lease_id AS BLOB)) BETWEEN 1 AND 128),
  highest_client_sequence INTEGER NOT NULL DEFAULT 0 CHECK (highest_client_sequence >= 0),
  last_event_id TEXT,
  last_event_hash TEXT,
  last_snapshot_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_snapshot_sequence >= 0),
  authority TEXT NOT NULL DEFAULT 'legacy' CHECK (authority IN ('legacy', 'v2')),
  last_seen_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE subagent_lifecycle_state (
  parent_agent_id TEXT NOT NULL CHECK (length(CAST(parent_agent_id AS BLOB)) BETWEEN 1 AND 128),
  subagent_id TEXT NOT NULL CHECK (length(CAST(subagent_id AS BLOB)) BETWEEN 1 AND 128),
  instance_id TEXT NOT NULL CHECK (length(CAST(instance_id AS BLOB)) BETWEEN 1 AND 128),
  parent_session_id TEXT NOT NULL CHECK (length(CAST(parent_session_id AS BLOB)) BETWEEN 1 AND 128),
  producer_epoch TEXT NOT NULL CHECK (length(CAST(producer_epoch AS BLOB)) = 36),
  type TEXT CHECK (type IS NULL OR length(CAST(type AS BLOB)) <= 64),
  description TEXT CHECK (description IS NULL OR length(CAST(description AS BLOB)) <= 512),
  state TEXT NOT NULL CHECK (state IN ('running', 'terminal')),
  started_at TEXT,
  updated_at TEXT NOT NULL,
  terminal_at TEXT,
  terminal_reason TEXT CHECK (terminal_reason IS NULL OR length(CAST(terminal_reason AS BLOB)) <= 64),
  router_version INTEGER NOT NULL CHECK (router_version >= 0),
  PRIMARY KEY (parent_agent_id, subagent_id)
);

CREATE TABLE subagent_lifecycle_events (
  router_version INTEGER PRIMARY KEY AUTOINCREMENT,
  parent_agent_id TEXT NOT NULL CHECK (length(CAST(parent_agent_id AS BLOB)) BETWEEN 1 AND 128),
  instance_id TEXT NOT NULL CHECK (length(CAST(instance_id AS BLOB)) BETWEEN 1 AND 128),
  parent_session_id TEXT NOT NULL CHECK (length(CAST(parent_session_id AS BLOB)) BETWEEN 1 AND 128),
  producer_epoch TEXT NOT NULL CHECK (length(CAST(producer_epoch AS BLOB)) = 36),
  subagent_id TEXT NOT NULL CHECK (length(CAST(subagent_id AS BLOB)) BETWEEN 1 AND 128),
  event_id TEXT NOT NULL CHECK (length(CAST(event_id AS BLOB)) BETWEEN 1 AND 128),
  client_sequence INTEGER NOT NULL CHECK (client_sequence > 0),
  event_kind TEXT NOT NULL CHECK (event_kind IN ('started', 'terminal', 'snapshot')),
  state TEXT NOT NULL CHECK (state IN ('running', 'terminal')),
  type TEXT,
  description TEXT,
  terminal_reason TEXT,
  payload_hash TEXT NOT NULL,
  received_at TEXT NOT NULL,
  UNIQUE (parent_agent_id, producer_epoch, event_id),
  UNIQUE (parent_agent_id, producer_epoch, client_sequence, subagent_id)
);

CREATE TABLE subagent_lifecycle_diagnostics (
  parent_agent_id TEXT NOT NULL,
  reason_code TEXT NOT NULL CHECK (length(CAST(reason_code AS BLOB)) <= 64),
  count INTEGER NOT NULL DEFAULT 0 CHECK (count >= 0 AND count <= 10000),
  last_router_version INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (parent_agent_id, reason_code)
);

CREATE INDEX subagent_lifecycle_state_parent_running_idx
  ON subagent_lifecycle_state (parent_agent_id, router_version)
  WHERE state = 'running';
CREATE INDEX subagent_lifecycle_state_terminal_prune_idx
  ON subagent_lifecycle_state (terminal_at)
  WHERE state = 'terminal';
CREATE INDEX subagent_lifecycle_events_parent_version_idx
  ON subagent_lifecycle_events (parent_agent_id, router_version);
CREATE INDEX subagent_lifecycle_events_received_idx
  ON subagent_lifecycle_events (received_at, router_version);
CREATE INDEX subagent_lifecycle_events_identity_idx
  ON subagent_lifecycle_events (parent_agent_id, producer_epoch, event_id);
CREATE INDEX subagent_lifecycle_producers_last_seen_idx
  ON subagent_lifecycle_producers (last_seen_at);
