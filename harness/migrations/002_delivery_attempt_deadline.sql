ALTER TABLE messages ADD COLUMN claimed_at TEXT;

ALTER TABLE delivery_attempts
  ADD COLUMN owner_lease_id TEXT NOT NULL DEFAULT '';

ALTER TABLE delivery_attempts
  ADD COLUMN delivery_expires_at TEXT NOT NULL DEFAULT '1970-01-01T00:00:00.000Z';

ALTER TABLE delivery_attempts ADD COLUMN failure_reason TEXT;

UPDATE delivery_attempts
SET owner_lease_id = COALESCE(lease_id, '')
WHERE owner_lease_id = '';

CREATE UNIQUE INDEX messages_recipient_in_flight_idx
  ON messages (recipient)
  WHERE state IN ('leased', 'injection_requested');

CREATE INDEX delivery_attempts_active_deadline_idx
  ON delivery_attempts (delivery_expires_at)
  WHERE state IN ('leased', 'injection_requested');
