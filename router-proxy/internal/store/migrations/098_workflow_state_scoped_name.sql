-- Relax global uniqueness of workflow_states.name to be unique per workflow.
-- States without a workflow_id remain unique across legacy/unassigned states.

CREATE TABLE workflow_states_new (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 256),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 4096),
  entry_text TEXT NOT NULL DEFAULT '' CHECK (length(entry_text) <= 65536),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  reminders_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(reminders_json)),
  allowed_tools_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(allowed_tools_json)),
  transitions_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(transitions_json)),
  allow_exit INTEGER NOT NULL DEFAULT 1 CHECK (allow_exit IN (0, 1)),
  workflow_id TEXT REFERENCES workflows(id) ON DELETE CASCADE,
  ui_pos_x REAL NOT NULL DEFAULT 0,
  ui_pos_y REAL NOT NULL DEFAULT 0
);

INSERT INTO workflow_states_new (id, name, description, entry_text, created_at, updated_at, reminders_json, allowed_tools_json, transitions_json, allow_exit, workflow_id, ui_pos_x, ui_pos_y)
  SELECT id, name, description, entry_text, created_at, updated_at, reminders_json, allowed_tools_json, transitions_json, allow_exit, workflow_id, ui_pos_x, ui_pos_y
  FROM workflow_states;

DROP TABLE workflow_states;

ALTER TABLE workflow_states_new RENAME TO workflow_states;

-- Scoped uniqueness: state names are unique per workflow (when workflow_id is not null)
CREATE UNIQUE INDEX idx_workflow_states_workflow_name ON workflow_states(workflow_id, name) WHERE workflow_id IS NOT NULL;
-- Legacy/global state names without workflow_id remain unique among unassigned states
CREATE UNIQUE INDEX idx_workflow_states_null_workflow_name ON workflow_states(name) WHERE workflow_id IS NULL;
CREATE INDEX idx_workflow_states_workflow_id ON workflow_states(workflow_id);
