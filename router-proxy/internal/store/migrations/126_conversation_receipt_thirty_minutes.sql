-- Upgrade only the old shipped default; other values are operator choices.
UPDATE conversation_send_receipt_capacity SET max_bytes=209715200 WHERE singleton=1 AND max_bytes=67108864;

-- Never extend a shorter deadline. Pre-082 receipts were baselined at upgrade,
-- so their saved recording time is also needed to avoid giving old sends a new
-- 30-minute lifetime. An unreadable acceptance time expires conservatively.
UPDATE conversation_send_receipts
SET deadline_at = MIN(
 COALESCE(deadline_at, '0001-01-01T00:00:00.000Z'),
 COALESCE(strftime('%Y-%m-%dT%H:%M:%fZ', accepted_at, '+30 minutes'), '0001-01-01T00:00:00.000Z'),
 COALESCE(CASE WHEN json_valid(conversation_json) THEN strftime('%Y-%m-%dT%H:%M:%fZ', json_extract(conversation_json, '$.recorded_at'), '+30 minutes') END, deadline_at, '0001-01-01T00:00:00.000Z')
);
