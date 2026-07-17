ALTER TABLE sentinel_occurrences ADD COLUMN terminal_at TEXT;
ALTER TABLE sentinel_occurrences ADD COLUMN recipient TEXT;
ALTER TABLE sentinel_occurrences ADD COLUMN claim_lease_id TEXT;
ALTER TABLE reminders ADD COLUMN sentinel_json TEXT;
UPDATE sentinel_occurrences
SET terminal_at = updated_at
WHERE state = 'failed' AND terminal_at IS NULL;
UPDATE sentinel_occurrences
SET recipient = (SELECT recipient FROM messages WHERE messages.id = sentinel_occurrences.message_id)
WHERE recipient IS NULL;
UPDATE delivery_attempts
SET state = 'timed_out', updated_at = '1970-01-01T00:00:00.000Z'
WHERE message_id IN (
  SELECT message_id FROM sentinel_occurrences
  WHERE claim_lease_id IS NULL AND state IN ('claimed', 'launching')
) AND state IN ('leased', 'injection_requested');
UPDATE reminders
SET state = 'failed', linked_message_id = NULL
WHERE linked_message_id IN (
  SELECT message_id FROM sentinel_occurrences
  WHERE claim_lease_id IS NULL AND state IN ('claimed', 'launching')
) AND interval_ms IS NULL;
UPDATE reminders
SET state = 'scheduled', linked_message_id = NULL
WHERE linked_message_id IN (
  SELECT message_id FROM sentinel_occurrences
  WHERE claim_lease_id IS NULL AND state IN ('claimed', 'launching')
) AND interval_ms IS NOT NULL;
UPDATE messages
SET body = '', state = 'dead_letter'
WHERE id IN (
  SELECT message_id FROM sentinel_occurrences
  WHERE claim_lease_id IS NULL AND state IN ('claimed', 'launching')
);
UPDATE sentinel_occurrences
SET state = 'failed', terminal_at = updated_at
WHERE claim_lease_id IS NULL AND state IN ('claimed', 'launching');
CREATE INDEX sentinel_tombstone_cleanup ON sentinel_occurrences(terminal_at) WHERE terminal_at IS NOT NULL;
