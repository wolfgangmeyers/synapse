-- A binding replacement (including a same-value assignment) starts a new
-- generation. Normal transitions restore the predecessor inside their transaction.
ALTER TABLE managed_agents ADD COLUMN workflow_predecessor_state_id TEXT REFERENCES workflow_states(id) ON DELETE SET NULL;
ALTER TABLE managed_agents ADD COLUMN workflow_binding_generation INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER managed_workflow_binding_replaced
AFTER UPDATE OF active_workflow_state_id, active_workflow_id ON managed_agents
BEGIN
  UPDATE managed_agents SET workflow_predecessor_state_id = NULL,
    workflow_binding_generation = workflow_binding_generation + 1
  WHERE agent_id = NEW.agent_id;
END;
