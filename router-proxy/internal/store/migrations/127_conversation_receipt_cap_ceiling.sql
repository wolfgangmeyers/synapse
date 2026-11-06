-- Migration 126 is already deployed on some installations. Clamp their
-- larger custom caps too, without discarding or rewriting existing receipts.
-- Smaller byte limits, the configured count limit, and usage stay unchanged.
UPDATE conversation_send_receipt_capacity
SET max_bytes=209715200
WHERE singleton=1 AND max_bytes>209715200;
