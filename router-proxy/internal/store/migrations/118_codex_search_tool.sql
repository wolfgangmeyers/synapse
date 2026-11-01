-- Migration 118: Codex Search Tool Seed
-- Seed codex_search in managed_tool_defaults with enabled = 1 and category 'Knowledge Base & Search'

INSERT INTO managed_tool_defaults (tool_name, category, enabled, mcp_server_id, updated_at) VALUES
    ('codex_search', 'Knowledge Base & Search', 1, NULL, CURRENT_TIMESTAMP)
ON CONFLICT(tool_name) DO NOTHING;
