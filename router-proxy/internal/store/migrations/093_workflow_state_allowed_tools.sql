-- Add allowed_tools_json to workflow_states
ALTER TABLE workflow_states ADD COLUMN allowed_tools_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(allowed_tools_json));
