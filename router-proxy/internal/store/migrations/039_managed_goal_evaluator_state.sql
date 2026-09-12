-- Keep evaluator outcomes and the bounded meaningful-activity rearm counter
-- durable across router restart. 038 is already applied in existing stores.
ALTER TABLE managed_agent_policies
  ADD COLUMN last_outcome TEXT CHECK (last_outcome IN ('COMPLETED','NOT_DONE'));
ALTER TABLE managed_agent_policies
  ADD COLUMN rearm_activity_count INTEGER NOT NULL DEFAULT 0 CHECK (rearm_activity_count >= 0 AND rearm_activity_count <= 3);
