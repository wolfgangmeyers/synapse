-- Migration 137: Admin Worker List Tool Seed
-- Seed worker_list in managed_tool_defaults with enabled = 0 (disabled by default) and category 'Worker Administration'

INSERT INTO managed_tool_defaults (tool_name, category, enabled, mcp_server_id, updated_at) VALUES
    ('worker_list', 'Worker Administration', 0, NULL, CURRENT_TIMESTAMP)
ON CONFLICT(tool_name) DO NOTHING;
