-- The clock baseline is filled by Store.Open inside this migration's BEGIN IMMEDIATE.
ALTER TABLE conversation_send_receipts ADD COLUMN accepted_at TEXT;
ALTER TABLE conversation_send_receipts ADD COLUMN deadline_at TEXT;
CREATE INDEX conversation_send_receipts_deadline_idx ON conversation_send_receipts(deadline_at, message_id);
-- Historical rows that had a receipt are not pre-078 receipt-less history.
ALTER TABLE conversation_messages ADD COLUMN receipt_backed INTEGER NOT NULL DEFAULT 0 CHECK(receipt_backed IN (0,1));
UPDATE conversation_messages SET receipt_backed=1 WHERE EXISTS (
 SELECT 1 FROM conversation_send_receipts r WHERE r.message_id=conversation_messages.message_id
);
CREATE INDEX conversation_messages_legacy_id_idx ON conversation_messages(message_id, conversation_sequence DESC) WHERE receipt_backed=0;
