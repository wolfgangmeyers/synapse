ALTER TABLE workflow_states ADD COLUMN goal TEXT NOT NULL DEFAULT '' CHECK (length(goal) <= 4000);
