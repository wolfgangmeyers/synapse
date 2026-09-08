-- Restart All parents may share a pending direct restart through coalescing.
-- Keep parent-scoped snapshot uniqueness while removing the old global child
-- reference uniqueness that made the second parent roll back.

CREATE TEMP TABLE managed_lifecycle_batch_items_028_snapshot AS
  SELECT parent_action_id,snapshot_position,agent_id,status,child_action_id,
         child_operation_id,error_code,error_message,created_at
  FROM managed_lifecycle_batch_items;

CREATE TABLE managed_lifecycle_batch_items_028_new (
  parent_action_id TEXT NOT NULL REFERENCES managed_lifecycle_actions(action_id) ON DELETE CASCADE,
  snapshot_position INTEGER NOT NULL CHECK (snapshot_position BETWEEN 0 AND 31),
  agent_id TEXT NOT NULL CHECK (length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  status TEXT NOT NULL CHECK (status IN ('accepted','coalesced','rejected')),
  child_action_id TEXT REFERENCES managed_lifecycle_actions(action_id) ON DELETE SET NULL,
  child_operation_id TEXT REFERENCES managed_http_operations(operation_id) ON DELETE SET NULL,
  error_code TEXT,
  error_message TEXT,
  created_at TEXT NOT NULL,
  CHECK ((status IN ('accepted','coalesced')) = (child_action_id IS NOT NULL)),
  CHECK ((status IN ('accepted','coalesced')) = (child_operation_id IS NOT NULL)),
  CHECK ((status='rejected') = (error_code IS NOT NULL)),
  CHECK ((status='rejected') = (error_message IS NOT NULL)),
  UNIQUE(parent_action_id, snapshot_position),
  UNIQUE(parent_action_id, agent_id)
);

INSERT INTO managed_lifecycle_batch_items_028_new (
  parent_action_id,snapshot_position,agent_id,status,child_action_id,
  child_operation_id,error_code,error_message,created_at
) SELECT
  parent_action_id,snapshot_position,agent_id,status,child_action_id,
  child_operation_id,error_code,error_message,created_at
FROM managed_lifecycle_batch_items_028_snapshot;

DROP TABLE managed_lifecycle_batch_items;
ALTER TABLE managed_lifecycle_batch_items_028_new RENAME TO managed_lifecycle_batch_items;
CREATE INDEX managed_lifecycle_batch_parent_idx
  ON managed_lifecycle_batch_items(parent_action_id, snapshot_position);
CREATE INDEX managed_lifecycle_batch_child_action_idx
  ON managed_lifecycle_batch_items(child_action_id)
  WHERE child_action_id IS NOT NULL;
CREATE INDEX managed_lifecycle_batch_child_operation_idx
  ON managed_lifecycle_batch_items(child_operation_id)
  WHERE child_operation_id IS NOT NULL;
