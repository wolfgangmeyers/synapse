-- Add reminders_json to workflow_states
ALTER TABLE workflow_states ADD COLUMN reminders_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(reminders_json));

-- Track dynamically spawned workflow reminders for managed agents
CREATE TABLE IF NOT EXISTS managed_agent_workflow_reminders (
  agent_id TEXT NOT NULL,
  workflow_state_id TEXT NOT NULL,
  reminder_id TEXT NOT NULL PRIMARY KEY,
  created_at TEXT NOT NULL,
  FOREIGN KEY(agent_id) REFERENCES managed_agents(agent_id) ON DELETE CASCADE,
  FOREIGN KEY(workflow_state_id) REFERENCES workflow_states(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_wf_reminders_agent
  ON managed_agent_workflow_reminders(agent_id);
