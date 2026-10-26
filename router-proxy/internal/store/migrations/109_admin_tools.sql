-- Migration 109: Admin Tools Seed
-- Seed the 5 admin tools in managed_tool_defaults with enabled = 0 (disabled by default) and category 'Worker Administration'

INSERT INTO managed_tool_defaults (tool_name, category, enabled, mcp_server_id, updated_at) VALUES
    ('worker_spawn', 'Worker Administration', 0, NULL, CURRENT_TIMESTAMP),
    ('worker_terminate', 'Worker Administration', 0, NULL, CURRENT_TIMESTAMP),
    ('worker_set_model', 'Worker Administration', 0, NULL, CURRENT_TIMESTAMP),
    ('worker_set_goal', 'Worker Administration', 0, NULL, CURRENT_TIMESTAMP),
    ('worker_status', 'Worker Administration', 0, NULL, CURRENT_TIMESTAMP)
ON CONFLICT(tool_name) DO NOTHING;
