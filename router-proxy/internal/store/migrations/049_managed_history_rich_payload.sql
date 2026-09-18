-- Preserve the canonical managed-control action payload so Complete can explain
-- an action after reconnect/restart. Existing records intentionally remain sparse.
ALTER TABLE managed_control_requests ADD COLUMN canonical_arguments_json TEXT;
