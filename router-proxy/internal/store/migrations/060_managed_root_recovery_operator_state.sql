-- Migration 059 already repairs managed lifecycle foreign keys. Keep the
-- failed-root operator reservation on the next migration number while adding
-- one bounded CAS owner to the existing 24-hour recovery ledger.
ALTER TABLE managed_root_recovery_actions
  ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1);
ALTER TABLE managed_root_recovery_actions
  ADD COLUMN operator_operation_id TEXT;
ALTER TABLE managed_root_recovery_actions
  ADD COLUMN operator_kind TEXT CHECK (operator_kind IS NULL OR operator_kind IN ('retry','terminate'));
ALTER TABLE managed_root_recovery_actions
  ADD COLUMN operator_stage TEXT CHECK (operator_stage IS NULL OR operator_stage IN ('reserved','identity_resolved','executing','terminal'));
ALTER TABLE managed_root_recovery_actions
  ADD COLUMN operator_owner TEXT CHECK (operator_owner IS NULL OR length(CAST(operator_owner AS BLOB)) BETWEEN 1 AND 128);
ALTER TABLE managed_root_recovery_actions
  ADD COLUMN operator_owner_epoch INTEGER CHECK (operator_owner_epoch IS NULL OR operator_owner_epoch >= 1);
ALTER TABLE managed_root_recovery_actions
  ADD COLUMN operator_agent_updated_at TEXT;

CREATE UNIQUE INDEX managed_root_recovery_operator_operation_idx
  ON managed_root_recovery_actions(operator_operation_id)
  WHERE operator_operation_id IS NOT NULL;
CREATE INDEX managed_root_recovery_operator_resume_idx
  ON managed_root_recovery_actions(operator_stage, updated_at, action_id)
  WHERE operator_stage IS NOT NULL AND operator_stage <> 'terminal';
