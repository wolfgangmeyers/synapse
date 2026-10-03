-- Add Antigravity extension column to launch recipes for durable cold recovery
ALTER TABLE managed_launch_recipes ADD COLUMN antigravity_extension TEXT NOT NULL DEFAULT '' CHECK (length(CAST(antigravity_extension AS BLOB)) <= 4096);

-- Snapshot existing provider status (all 7 columns) and models data
CREATE TABLE _temp_status_snapshot AS
  SELECT provider, label, authenticated, status, account_label, updated_at, error
  FROM managed_provider_status;

CREATE TABLE _temp_models_snapshot AS
  SELECT provider, model_id, name, available, updated_at
  FROM managed_provider_models;

-- Drop child then drop parent table to maintain referential integrity without turning off foreign keys
DROP TABLE managed_provider_models;
DROP TABLE managed_provider_status;

-- Recreate parent table with expanded CHECK constraint including google-vertex and antigravity
CREATE TABLE managed_provider_status (
  provider TEXT PRIMARY KEY CHECK (provider IN ('openai-codex','google','cursor','google-vertex','antigravity')),
  label TEXT NOT NULL,
  authenticated INTEGER NOT NULL CHECK (authenticated IN (0,1)),
  status TEXT NOT NULL,
  account_label TEXT,
  updated_at TEXT NOT NULL,
  error TEXT
);

-- Recreate child table with expanded CHECK constraint and ON DELETE CASCADE
CREATE TABLE managed_provider_models (
  provider TEXT NOT NULL CHECK (provider IN ('openai-codex','google','cursor','google-vertex','antigravity')),
  model_id TEXT NOT NULL,
  name TEXT NOT NULL,
  available INTEGER NOT NULL CHECK (available IN (0,1)),
  updated_at TEXT NOT NULL,
  PRIMARY KEY (provider, model_id),
  FOREIGN KEY (provider) REFERENCES managed_provider_status(provider) ON DELETE CASCADE
);

-- Restore parent rows first, then child rows
INSERT INTO managed_provider_status (provider, label, authenticated, status, account_label, updated_at, error)
  SELECT provider, label, authenticated, status, account_label, updated_at, error
  FROM _temp_status_snapshot;

INSERT INTO managed_provider_models (provider, model_id, name, available, updated_at)
  SELECT provider, model_id, name, available, updated_at
  FROM _temp_models_snapshot;

-- Clean up temporary snapshot tables
DROP TABLE _temp_status_snapshot;
DROP TABLE _temp_models_snapshot;

-- Recreate models index
CREATE INDEX managed_provider_models_available_idx
  ON managed_provider_models (available, provider, model_id);

-- Insert default signed-out rows for newly supported Google authentication providers
INSERT INTO managed_provider_status (provider, label, authenticated, status, updated_at) VALUES
  ('google-vertex', 'Google Vertex AI', 0, 'signed_out', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  ('antigravity', 'Google Antigravity', 0, 'signed_out', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
