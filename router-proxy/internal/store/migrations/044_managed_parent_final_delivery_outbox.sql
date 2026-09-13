-- Final replies use the existing deterministic conversation message identity.
-- These fields make every router-side publish attempt bounded and recoverable.
-- The source message is deleted after Pi accepts it, so retain its recipient
-- with parent work for a restart that has a final reply but no original row.
ALTER TABLE managed_parent_work ADD COLUMN reply_recipient TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_parent_final_deliveries RENAME TO managed_parent_final_deliveries_043;
CREATE TABLE managed_parent_final_deliveries (
  delivery_id TEXT PRIMARY KEY,
  work_id TEXT NOT NULL REFERENCES managed_parent_work(work_id) ON DELETE CASCADE,
  source_entry_identity TEXT NOT NULL,
  body TEXT NOT NULL CHECK (length(CAST(body AS BLOB)) BETWEEN 1 AND 65536),
  state TEXT NOT NULL CHECK (state IN ('pending','delivering','delivered','failed','interrupted')),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0 AND attempt_count <= 5),
  next_attempt_at TEXT,
  last_attempt_at TEXT,
  failure_code TEXT,
  created_at TEXT NOT NULL,
  delivered_at TEXT,
  terminal_at TEXT,
  UNIQUE(work_id, source_entry_identity)
);
INSERT INTO managed_parent_final_deliveries(delivery_id,work_id,source_entry_identity,body,state,created_at,delivered_at)
  SELECT delivery_id,work_id,source_entry_identity,body,state,created_at,delivered_at
  FROM managed_parent_final_deliveries_043;
DROP TABLE managed_parent_final_deliveries_043;
-- Pre-044 pending replies were missing the original sender after its direct
-- message had been acknowledged and deleted. There is no safe recipient to
-- infer, so terminalize those rows instead of publishing to an empty target.
UPDATE managed_parent_final_deliveries
  SET state='failed', failure_code='reply_recipient_unavailable', terminal_at=created_at
  WHERE state='pending' AND work_id IN (SELECT work_id FROM managed_parent_work WHERE reply_recipient='');
UPDATE managed_parent_work
  SET state='final_delivery_failed', terminal_at=COALESCE(terminal_at, created_at)
  WHERE state='final_delivery_pending' AND reply_recipient='';
CREATE INDEX managed_parent_final_deliveries_pending_idx
  ON managed_parent_final_deliveries(state, next_attempt_at, work_id)
  WHERE state IN ('pending','delivering');
