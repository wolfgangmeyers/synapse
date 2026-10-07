-- Simple Workflow States
CREATE TABLE workflow_states (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 256),
  description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 4096),
  entry_text TEXT NOT NULL DEFAULT '' CHECK (length(entry_text) <= 65536),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX workflow_states_name_idx ON workflow_states(name);
