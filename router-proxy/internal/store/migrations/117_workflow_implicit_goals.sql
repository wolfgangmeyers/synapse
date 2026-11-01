ALTER TABLE workflows ADD COLUMN snooze_duration_ms INTEGER NOT NULL DEFAULT 600000 CHECK (snooze_duration_ms > 0);
ALTER TABLE workflow_states DROP COLUMN goal;
