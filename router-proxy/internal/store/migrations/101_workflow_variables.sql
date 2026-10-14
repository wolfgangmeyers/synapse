ALTER TABLE workflows ADD COLUMN variables_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(variables_json));
ALTER TABLE managed_agents ADD COLUMN active_workflow_id TEXT REFERENCES workflows(id) ON DELETE SET NULL;
ALTER TABLE managed_agents ADD COLUMN workflow_variables_json TEXT;
