-- Phase 1: Tool Management
-- Create managed_tool_defaults and managed_agent_tools tables

CREATE TABLE IF NOT EXISTS managed_tool_defaults (
    tool_name TEXT PRIMARY KEY,
    category TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    mcp_server_id TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS managed_agent_tools (
    agent_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    enabled INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (agent_id, tool_name),
    FOREIGN KEY (agent_id) REFERENCES managed_agents(agent_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_managed_agent_tools_agent_id ON managed_agent_tools(agent_id);

-- Seed initial defaults for core native tools (all default enabled = 1)
INSERT INTO managed_tool_defaults (tool_name, category, enabled, mcp_server_id, updated_at) VALUES
    ('read', 'File Operations', 1, NULL, CURRENT_TIMESTAMP),
    ('write', 'File Operations', 1, NULL, CURRENT_TIMESTAMP),
    ('edit', 'File Operations', 1, NULL, CURRENT_TIMESTAMP),
    ('bash', 'System & Shell', 1, NULL, CURRENT_TIMESTAMP),
    ('subagent', 'Subagent Lifecycle', 1, NULL, CURRENT_TIMESTAMP),
    ('stop_subagent', 'Subagent Lifecycle', 1, NULL, CURRENT_TIMESTAMP),
    ('steer_subagent', 'Subagent Lifecycle', 1, NULL, CURRENT_TIMESTAMP),
    ('list_subagents', 'Subagent Lifecycle', 1, NULL, CURRENT_TIMESTAMP),
    ('get_subagent_result', 'Subagent Lifecycle', 1, NULL, CURRENT_TIMESTAMP),
    ('mecha_mailbox_send', 'Messaging & Routing', 1, NULL, CURRENT_TIMESTAMP),
    ('mecha_worker', 'Messaging & Routing', 1, NULL, CURRENT_TIMESTAMP),
    ('mecha_restart_router', 'Messaging & Routing', 1, NULL, CURRENT_TIMESTAMP),
    ('mecha_workflow_transition', 'Workflow Management', 1, NULL, CURRENT_TIMESTAMP),
    ('mecha_progress', 'Workflow Management', 1, NULL, CURRENT_TIMESTAMP),
    ('goal', 'Workflow Management', 1, NULL, CURRENT_TIMESTAMP),
    ('goal_snooze', 'Workflow Management', 1, NULL, CURRENT_TIMESTAMP),
    ('context_reset', 'Workflow Management', 1, NULL, CURRENT_TIMESTAMP),
    ('scratchpad', 'Workflow Management', 1, NULL, CURRENT_TIMESTAMP),
    ('mecha_reminder', 'Workflow Management', 1, NULL, CURRENT_TIMESTAMP),
    ('kb_search', 'Knowledge Base & Search', 1, NULL, CURRENT_TIMESTAMP),
    ('kb_read', 'Knowledge Base & Search', 1, NULL, CURRENT_TIMESTAMP),
    ('kb_write', 'Knowledge Base & Search', 1, NULL, CURRENT_TIMESTAMP),
    ('kb_list', 'Knowledge Base & Search', 1, NULL, CURRENT_TIMESTAMP),
    ('google_search', 'Knowledge Base & Search', 1, NULL, CURRENT_TIMESTAMP)
ON CONFLICT(tool_name) DO NOTHING;
