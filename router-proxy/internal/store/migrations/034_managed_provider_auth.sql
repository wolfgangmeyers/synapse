-- Provider credentials stay in Pi's protected auth store. This migration keeps
-- only the small non-secret router snapshot needed for UI status and launch
-- validation. Each successful helper operation replaces a provider's rows.
ALTER TABLE managed_settings ADD COLUMN auth_revision INTEGER NOT NULL DEFAULT 0;

CREATE TABLE managed_provider_status (
  provider TEXT PRIMARY KEY CHECK (provider IN ('openai-codex','google','cursor')),
  label TEXT NOT NULL,
  authenticated INTEGER NOT NULL CHECK (authenticated IN (0,1)),
  status TEXT NOT NULL,
  account_label TEXT,
  updated_at TEXT NOT NULL,
  error TEXT
);

-- A signed-out row makes the shared settings surface explicit on first
-- startup without inferring an authenticated account from a static catalog.
INSERT INTO managed_provider_status (provider, label, authenticated, status, updated_at) VALUES
  ('openai-codex', 'ChatGPT', 0, 'signed_out', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  ('google', 'Google', 0, 'signed_out', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  ('cursor', 'Cursor', 0, 'signed_out', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE managed_provider_models (
  provider TEXT NOT NULL CHECK (provider IN ('openai-codex','google','cursor')),
  model_id TEXT NOT NULL,
  name TEXT NOT NULL,
  available INTEGER NOT NULL CHECK (available IN (0,1)),
  updated_at TEXT NOT NULL,
  PRIMARY KEY (provider, model_id),
  FOREIGN KEY (provider) REFERENCES managed_provider_status(provider) ON DELETE CASCADE
);
CREATE INDEX managed_provider_models_available_idx
  ON managed_provider_models (available, provider, model_id);
