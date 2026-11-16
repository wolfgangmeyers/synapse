CREATE TABLE IF NOT EXISTS custom_providers (
  id TEXT PRIMARY KEY CHECK (length(id) >= 1 AND length(id) <= 64),
  label TEXT NOT NULL CHECK (length(label) >= 1 AND length(label) <= 128),
  base_url TEXT NOT NULL CHECK (length(base_url) >= 1 AND length(base_url) <= 1024),
  protocol TEXT NOT NULL DEFAULT 'openai-compatible' CHECK (length(protocol) >= 1 AND length(protocol) <= 64),
  api_key_ciphertext TEXT,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS custom_provider_models (
  id TEXT NOT NULL,
  provider_id TEXT NOT NULL,
  display_name TEXT NOT NULL,
  context_window INTEGER NOT NULL DEFAULT 131072,
  max_tokens INTEGER NOT NULL DEFAULT 8192,
  supports_tools INTEGER NOT NULL DEFAULT 1 CHECK (supports_tools IN (0, 1)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (provider_id, id),
  FOREIGN KEY (provider_id) REFERENCES custom_providers(id) ON DELETE CASCADE
);

