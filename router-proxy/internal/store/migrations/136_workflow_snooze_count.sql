-- 136_workflow_snooze_count.sql: Track cumulative workflow snooze calls per active workflow journal run.
ALTER TABLE workflow_journal_runs ADD COLUMN snooze_count INTEGER NOT NULL DEFAULT 0;
