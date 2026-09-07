-- Router-managed launch state is durable so names, slots, defaults, failed
-- launch diagnostics, and replay survive a router restart. Terminal operation
-- rows are swept after 24 hours; failed launch rows are never swept so a
-- failed name cannot silently become reusable.
CREATE TABLE managed_settings (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  default_model_provider TEXT,
  default_model_id TEXT,
  canonical_quiet_recipient_id TEXT NOT NULL DEFAULT 'supervisor'
    CHECK (canonical_quiet_recipient_id GLOB '[A-Za-z0-9]*'
      AND length(canonical_quiet_recipient_id) BETWEEN 1 AND 64),
  projection_version INTEGER NOT NULL DEFAULT 0 CHECK (projection_version >= 0),
  updated_at TEXT NOT NULL
);

INSERT INTO managed_settings (
  singleton_id, canonical_quiet_recipient_id, projection_version, updated_at
) VALUES (1, 'supervisor', 0, '1970-01-01T00:00:00.000Z');

CREATE TABLE managed_agents (
  agent_id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  slot_held INTEGER NOT NULL DEFAULT 0 CHECK (slot_held IN (0, 1)),
  cwd TEXT NOT NULL,
  selected_model_provider TEXT NOT NULL,
  selected_model_id TEXT NOT NULL,
  desired_state TEXT NOT NULL CHECK (desired_state IN ('running')),
  lifecycle_state TEXT NOT NULL CHECK (lifecycle_state IN ('starting', 'running', 'failed')),
  activity_state TEXT NOT NULL CHECK (activity_state IN ('starting', 'idle', 'stopped', 'failed')),
  accepting_prompts INTEGER NOT NULL DEFAULT 0 CHECK (accepting_prompts IN (0, 1)),
  pi_session_id TEXT,
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  process_pid INTEGER,
  process_start_marker TEXT,
  process_incarnation TEXT,
  runtime_generation TEXT,
  manifest_revision INTEGER NOT NULL CHECK (manifest_revision >= 1),
  manifest_hash TEXT NOT NULL,
  pending_initial_instruction TEXT,
  initial_instruction_sent INTEGER NOT NULL DEFAULT 0 CHECK (initial_instruction_sent IN (0, 1)),
  last_error_code TEXT,
  last_error_message TEXT,
  last_error_at TEXT,
  created_at TEXT NOT NULL,
  started_at TEXT,
  updated_at TEXT NOT NULL
);
CREATE INDEX managed_agents_slot_idx ON managed_agents (slot_held, lifecycle_state);

CREATE TABLE managed_http_operations (
  operation_id TEXT PRIMARY KEY,
  session_identity_hash TEXT NOT NULL,
  method TEXT NOT NULL,
  canonical_path TEXT NOT NULL,
  request_id TEXT NOT NULL,
  canonical_body_hash TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending', 'terminal')),
  response_status INTEGER NOT NULL,
  response_headers_json TEXT NOT NULL,
  response_body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  terminal_at TEXT,
  expires_at TEXT,
  UNIQUE (session_identity_hash, method, request_id)
);
CREATE INDEX managed_http_operations_expiry_idx
  ON managed_http_operations (expires_at) WHERE state = 'terminal';
CREATE INDEX managed_http_operations_lookup_idx
  ON managed_http_operations (session_identity_hash, method, request_id);
