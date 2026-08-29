ALTER TABLE delivery_attempts ADD COLUMN delivery_batch_id TEXT;
ALTER TABLE messages ADD COLUMN retry_batch_id TEXT;

DROP INDEX messages_recipient_in_flight_idx;
CREATE INDEX messages_recipient_in_flight_idx
  ON messages (recipient)
  WHERE state IN ('leased', 'injection_requested');

-- Delivery replay now rebuilds the exact active batch instead of folding rows into one survivor.
-- These indexes keep reconnect adoption and timed-out retry-group claims from scanning the mailbox.
CREATE INDEX delivery_attempts_active_batch_idx
  ON delivery_attempts (delivery_batch_id)
  WHERE state IN ('leased', 'injection_requested');

CREATE INDEX messages_retry_batch_idx
  ON messages (recipient, retry_batch_id)
  WHERE state = 'queued' AND retry_batch_id IS NOT NULL;
