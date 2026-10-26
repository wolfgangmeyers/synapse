-- Migration 110: Workflow Management Tools Seed
-- Seed the 5 workflow management tools in managed_tool_defaults with enabled = 0 (disabled by default) and category 'Workflow Management'

INSERT INTO managed_tool_defaults (tool_name, category, enabled, mcp_server_id, updated_at) VALUES
    ('workflow_list', 'Workflow Management', 0, NULL, CURRENT_TIMESTAMP),
    ('workflow_get', 'Workflow Management', 0, NULL, CURRENT_TIMESTAMP),
    ('workflow_save', 'Workflow Management', 0, NULL, CURRENT_TIMESTAMP),
    ('workflow_delete', 'Workflow Management', 0, NULL, CURRENT_TIMESTAMP),
    ('workflow_assign', 'Workflow Management', 0, NULL, CURRENT_TIMESTAMP)
ON CONFLICT(tool_name) DO NOTHING;
