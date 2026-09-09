-- Quiet history may only record a mail.send invocation that the managed-control
-- ledger admitted.  The authorization is single-use and expires, so arbitrary
-- unauthenticated /v1/messages traffic cannot opt itself into the quiet view.
CREATE TABLE managed_quiet_send_authorizations (
  request_id TEXT PRIMARY KEY,
  sender_agent_id TEXT NOT NULL,
  recipient_agent_id TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX managed_quiet_send_authorizations_expiry_idx
  ON managed_quiet_send_authorizations (expires_at);

-- Complete-history producer retries use an idempotency key. Existing rows
-- predate it and remain readable; only non-null keys participate in uniqueness.
ALTER TABLE managed_complete_history ADD COLUMN event_key TEXT;
CREATE UNIQUE INDEX managed_complete_history_event_key_idx
  ON managed_complete_history (event_key) WHERE event_key IS NOT NULL;
