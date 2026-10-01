-- Accepted IDs never expire. These caps bound receipt growth, not availability:
-- once full, new conversation sends stop until an operator archives the DB or
-- an explicit availability contract replaces this policy. Never silently prune.
CREATE TABLE conversation_send_receipt_capacity (
 singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
 used_count INTEGER NOT NULL CHECK (used_count >= 0),
 used_bytes INTEGER NOT NULL CHECK (used_bytes >= 0),
 max_count INTEGER NOT NULL CHECK (max_count > 0),
 max_bytes INTEGER NOT NULL CHECK (max_bytes > 0)
);
INSERT INTO conversation_send_receipt_capacity(singleton, used_count, used_bytes, max_count, max_bytes)
 SELECT 1, count(*), coalesce(sum(length(CAST(message_id AS BLOB)) + length(CAST(conversation_json AS BLOB)) + length(CAST(delivery_json AS BLOB))), 0), 100000, 67108864
 FROM conversation_send_receipts;
