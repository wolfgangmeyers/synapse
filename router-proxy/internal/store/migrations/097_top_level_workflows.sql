-- Top-level workflows table and state associations
CREATE TABLE IF NOT EXISTS workflows (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  entry_state_id TEXT REFERENCES workflow_states(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workflows_name ON workflows(name);

ALTER TABLE workflow_states ADD COLUMN workflow_id TEXT REFERENCES workflows(id) ON DELETE CASCADE;
ALTER TABLE workflow_states ADD COLUMN ui_pos_x REAL NOT NULL DEFAULT 0;
ALTER TABLE workflow_states ADD COLUMN ui_pos_y REAL NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_workflow_states_workflow_id ON workflow_states(workflow_id);
