-- Preserve the native live status alongside the router-owned lifecycle state.
-- The Web UI needs to distinguish queued work from a child that has started.
ALTER TABLE subagent_lifecycle_state ADD COLUMN status TEXT
  CHECK (status IS NULL OR status IN ('queued', 'running', 'completed', 'steered', 'aborted', 'stopped', 'error'));
ALTER TABLE subagent_lifecycle_events ADD COLUMN status TEXT
  CHECK (status IS NULL OR status IN ('queued', 'running', 'completed', 'steered', 'aborted', 'stopped', 'error'));

UPDATE subagent_lifecycle_state
SET status = CASE
  WHEN state = 'running' THEN 'running'
  WHEN terminal_reason IN ('completed', 'steered', 'aborted', 'stopped', 'error') THEN terminal_reason
END;

UPDATE subagent_lifecycle_events
SET status = CASE
  WHEN state = 'running' THEN 'running'
  WHEN terminal_reason IN ('completed', 'steered', 'aborted', 'stopped', 'error') THEN terminal_reason
END;
