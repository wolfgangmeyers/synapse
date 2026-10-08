-- Add active_workflow_state_id to managed_agents referencing workflow_states(id) ON DELETE SET NULL
ALTER TABLE managed_agents ADD COLUMN active_workflow_state_id TEXT REFERENCES workflow_states(id) ON DELETE SET NULL;
