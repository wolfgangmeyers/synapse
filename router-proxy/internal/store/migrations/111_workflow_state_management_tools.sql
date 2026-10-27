-- Migration 111: Workflow State Management Tools Seed
-- Seed the 4 workflow state management tools in managed_tool_defaults with enabled = 0 (disabled by default) and category 'Workflow Management'

INSERT INTO managed_tool_defaults (tool_name, category, enabled, mcp_server_id, updated_at) VALUES
    ('workflow_state_list', 'Workflow Management', 0, NULL, CURRENT_TIMESTAMP),
    ('workflow_state_get', 'Workflow Management', 0, NULL, CURRENT_TIMESTAMP),
    ('workflow_state_save', 'Workflow Management', 0, NULL, CURRENT_TIMESTAMP),
    ('workflow_state_delete', 'Workflow Management', 0, NULL, CURRENT_TIMESTAMP)
ON CONFLICT(tool_name) DO NOTHING;
