CREATE TABLE sentinel_relay_capabilities (
  occurrence_id TEXT PRIMARY KEY,
  relay_token TEXT NOT NULL UNIQUE,
  sender TEXT NOT NULL,
  recipient TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at TEXT
);
