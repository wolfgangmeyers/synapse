ALTER TABLE managed_control_requests
  ADD COLUMN delivery_protocol TEXT CHECK (delivery_protocol IS NULL OR length(CAST(delivery_protocol AS BLOB)) BETWEEN 1 AND 128);
ALTER TABLE managed_control_requests
  ADD COLUMN delivery_payload_sha256 TEXT CHECK (delivery_payload_sha256 IS NULL OR length(delivery_payload_sha256) = 64);
ALTER TABLE managed_control_requests
  ADD COLUMN reconciliation_state TEXT CHECK (reconciliation_state IS NULL OR reconciliation_state IN ('append_pending','reconcile_pending','delivered','conflict','failed'));
ALTER TABLE managed_control_requests
  ADD COLUMN receipt_id TEXT CHECK (receipt_id IS NULL OR length(CAST(receipt_id AS BLOB)) BETWEEN 1 AND 256);
ALTER TABLE managed_control_requests
  ADD COLUMN receipt_runtime_generation TEXT CHECK (receipt_runtime_generation IS NULL OR length(CAST(receipt_runtime_generation AS BLOB)) BETWEEN 1 AND 256);
ALTER TABLE managed_control_requests
  ADD COLUMN receipt_session_epoch INTEGER CHECK (receipt_session_epoch IS NULL OR receipt_session_epoch >= 1);
ALTER TABLE managed_control_requests
  ADD COLUMN receipt_manifest_hash TEXT CHECK (receipt_manifest_hash IS NULL OR length(receipt_manifest_hash) = 64);
ALTER TABLE managed_control_requests
  ADD COLUMN receipt_at TEXT;

UPDATE managed_control_requests
SET delivery_protocol = 'legacy_explicit_entry_v1',
    delivery_payload_sha256 = result_payload_sha256,
    reconciliation_state = 'delivered'
WHERE state = 'delivered';

UPDATE managed_control_requests
SET delivery_protocol = 'stock_transcript_receipt_v2',
    delivery_payload_sha256 = result_payload_sha256,
    reconciliation_state = 'append_pending'
WHERE state NOT IN ('delivered','failed','cancelled');

ALTER TABLE managed_direct_delivery_attempts
  ADD COLUMN reconciliation_protocol TEXT CHECK (reconciliation_protocol IS NULL OR length(CAST(reconciliation_protocol AS BLOB)) BETWEEN 1 AND 128);
ALTER TABLE managed_direct_delivery_attempts
  ADD COLUMN receipt_payload_sha256 TEXT CHECK (receipt_payload_sha256 IS NULL OR length(receipt_payload_sha256) = 64);
ALTER TABLE managed_direct_delivery_attempts
  ADD COLUMN receipt_id TEXT CHECK (receipt_id IS NULL OR length(CAST(receipt_id AS BLOB)) BETWEEN 1 AND 256);
