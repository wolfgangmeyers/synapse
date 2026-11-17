-- Migration 136: workflow collections
-- Adds workflow_collections table and upgrades workflows to reference a collection_id.
-- Default collection 'default' is automatically seeded for pre-existing workflows.
CREATE TABLE IF NOT EXISTS workflow_collections (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT OR IGNORE INTO workflow_collections (id, name, description, created_at, updated_at)
VALUES ('default', 'Default', 'Default workflow collection', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

-- SQLite does not support ALTER TABLE ADD CONSTRAINT or modifying foreign keys on existing tables.
-- A table rebuild is required to add collection_id with REFERENCES workflow_collections(id)
-- and composite unique index (collection_id, name).
-- Foreign keys are temporarily disabled during table rebuild, followed by PRAGMA foreign_key_check.
PRAGMA foreign_keys = OFF;

CREATE TABLE IF NOT EXISTS workflows_new (
    id TEXT PRIMARY KEY,
    collection_id TEXT NOT NULL DEFAULT 'default' REFERENCES workflow_collections(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    entry_state_id TEXT REFERENCES workflow_states(id) ON DELETE SET NULL,
    snooze_duration_ms INTEGER NOT NULL DEFAULT 600000,
    variables_json TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (collection_id, name)
);

-- Backfill pre-existing workflows into the 'default' collection
INSERT INTO workflows_new (id, collection_id, name, description, entry_state_id, snooze_duration_ms, variables_json, created_at, updated_at)
SELECT id, 'default', name, description, entry_state_id, snooze_duration_ms, variables_json, created_at, updated_at
FROM workflows;

DROP TABLE workflows;
ALTER TABLE workflows_new RENAME TO workflows;

CREATE INDEX IF NOT EXISTS idx_workflows_collection_id ON workflows(collection_id);
CREATE INDEX IF NOT EXISTS idx_workflows_name ON workflows(name);

-- Validate foreign key integrity after table swap before re-enabling enforcement
PRAGMA foreign_key_check;
PRAGMA foreign_keys = ON;

