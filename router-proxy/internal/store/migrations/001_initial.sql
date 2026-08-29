CREATE TABLE agents (
  agent_id TEXT PRIMARY KEY,
  first_seen_at TEXT NOT NULL,
  last_connected_at TEXT,
  disconnected_at TEXT
);

CREATE TABLE messages (
  enqueue_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  sender TEXT NOT NULL,
  recipient TEXT NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
  subject TEXT,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN (
    'queued',
    'leased',
    'injection_requested',
    'acknowledged',
    'dead_letter'
  ))
);

CREATE TABLE agent_leases (
  lease_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL UNIQUE REFERENCES agents(agent_id) ON DELETE CASCADE,
  instance_id TEXT NOT NULL,
  acquired_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE TABLE delivery_attempts (
  attempt_id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
  lease_id TEXT REFERENCES agent_leases(lease_id) ON DELETE SET NULL,
  state TEXT NOT NULL CHECK (state IN (
    'leased',
    'injection_requested',
    'acknowledged',
    'nacked',
    'timed_out'
  )),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (message_id, attempt_number)
);

CREATE INDEX messages_recipient_state_sequence_idx
  ON messages (recipient, state, enqueue_sequence);

CREATE INDEX messages_queued_expiry_idx
  ON messages (expires_at)
  WHERE state = 'queued';

CREATE INDEX delivery_attempts_message_idx
  ON delivery_attempts (message_id, attempt_number);
