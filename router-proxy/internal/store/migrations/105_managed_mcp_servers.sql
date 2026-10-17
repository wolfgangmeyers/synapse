CREATE TABLE IF NOT EXISTS managed_mcp_servers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  url TEXT NOT NULL UNIQUE,
  auth_type TEXT NOT NULL,
  client_id TEXT,
  client_secret TEXT,
  token_endpoint TEXT,
  auth_endpoint TEXT,
  registration_endpoint TEXT,
  revocation_endpoint TEXT,
  scopes TEXT,
  status TEXT NOT NULL,
  last_error TEXT,
  tools_count INTEGER DEFAULT 0,
  last_sync_at TIMESTAMP,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS managed_mcp_tokens (
  server_id TEXT PRIMARY KEY REFERENCES managed_mcp_servers(id) ON DELETE CASCADE,
  encrypted_access_token TEXT,
  encrypted_refresh_token TEXT,
  token_type TEXT DEFAULT 'Bearer',
  expires_at TIMESTAMP,
  scopes TEXT,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS managed_mcp_tools (
  id TEXT PRIMARY KEY,
  server_id TEXT NOT NULL REFERENCES managed_mcp_servers(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefixed_name TEXT NOT NULL UNIQUE,
  description TEXT,
  input_schema_json TEXT NOT NULL,
  output_schema_json TEXT,
  definition_sha256 TEXT NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_managed_mcp_servers_url ON managed_mcp_servers(url);
CREATE INDEX IF NOT EXISTS idx_managed_mcp_servers_status ON managed_mcp_servers(status);
CREATE INDEX IF NOT EXISTS idx_managed_mcp_tools_server_id ON managed_mcp_tools(server_id);
