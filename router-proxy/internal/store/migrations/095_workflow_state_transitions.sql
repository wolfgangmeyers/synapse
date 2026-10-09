-- Add transitions_json and allow_exit to workflow_states
ALTER TABLE workflow_states ADD COLUMN transitions_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(transitions_json));
ALTER TABLE workflow_states ADD COLUMN allow_exit INTEGER NOT NULL DEFAULT 1 CHECK (allow_exit IN (0, 1));
