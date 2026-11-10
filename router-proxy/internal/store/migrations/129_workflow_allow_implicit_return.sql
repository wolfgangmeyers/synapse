ALTER TABLE workflow_states ADD COLUMN allow_implicit_return INTEGER NOT NULL DEFAULT 0 CHECK (allow_implicit_return IN (0, 1));
