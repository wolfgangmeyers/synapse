package store

type NativeToolDef struct {
	ToolName       string
	Category       string
	Description    string
	DefaultEnabled bool
	IsAdmin        bool
}

var NativeToolCatalog = []NativeToolDef{
	// File Operations
	{ToolName: "read", Category: "File Operations", Description: "Read file contents", DefaultEnabled: true},
	{ToolName: "write", Category: "File Operations", Description: "Write content to a file", DefaultEnabled: true},
	{ToolName: "edit", Category: "File Operations", Description: "Edit a single file using exact text replacement", DefaultEnabled: true},

	// System & Shell
	{ToolName: "bash", Category: "System & Shell", Description: "Run a bash command in the current working directory", DefaultEnabled: true},
	{ToolName: "mecha_tool_timeout", Category: "System & Shell", Description: "Temporarily extend tool execution timeout", DefaultEnabled: true},
	{ToolName: "mecha_restart_router", Category: "System & Shell", Description: "Request a controlled restart of the mailbox router", DefaultEnabled: true},
	{ToolName: "mecha_request_inline_tool_allowance", Category: "System & Shell", Description: "Request an inline tool allowance for a named tool (bash, edit, or write).", DefaultEnabled: true},

	// Subagent Lifecycle
	{ToolName: "subagent", Category: "Subagent Lifecycle", Description: "Launch a specialized agent for complex tasks", DefaultEnabled: true},
	{ToolName: "get_subagent_result", Category: "Subagent Lifecycle", Description: "Retrieve subagent execution results", DefaultEnabled: true},
	{ToolName: "steer_subagent", Category: "Subagent Lifecycle", Description: "Send a mid-run message to redirect a running background agent", DefaultEnabled: true},
	{ToolName: "stop_subagent", Category: "Subagent Lifecycle", Description: "Stop a running background subagent", DefaultEnabled: true},
	{ToolName: "list_subagents", Category: "Subagent Lifecycle", Description: "List background subagents launched by this agent", DefaultEnabled: true},

	// Messaging & Routing
	{ToolName: "mecha_agent_urgency", Category: "Messaging & Routing", Description: "Set explicit attention metadata, not priority or blocked state", DefaultEnabled: true},
	{ToolName: "mecha_mailbox_send", Category: "Messaging & Routing", Description: "Queue an independently addressed mailbox message", DefaultEnabled: true},
	{ToolName: "mecha_worker_terminate", Category: "Messaging & Routing", Description: "Terminate self and optionally remove assigned worktree", DefaultEnabled: true},
	{ToolName: "mecha_reminder", Category: "Messaging & Routing", Description: "Create, list, update or delete reminders", DefaultEnabled: true},
	{ToolName: "mecha_send_image", Category: "Messaging & Routing", Description: "Send an image to the user or another recipient", DefaultEnabled: true},

	// Workflow Management
	{ToolName: "goal", Category: "Workflow Management", Description: "Set, inspect or turn off goal", DefaultEnabled: true},
	{ToolName: "goal_snooze", Category: "Workflow Management", Description: "Set a delay preference for current goal", DefaultEnabled: true},
	{ToolName: "workflow_snooze", Category: "Workflow Management", Description: "Set a delay preference for active workflow continuation while awaiting active external asynchronous processes", DefaultEnabled: true},
	{ToolName: "scratchpad", Category: "Workflow Management", Description: "Persistent scratchpad memory for notes and milestones", DefaultEnabled: true},
	{ToolName: "mecha_progress", Category: "Workflow Management", Description: "Track and report measurable task progress", DefaultEnabled: true},
	{ToolName: "mecha_blocked", Category: "Execution Control", Description: "Set or clear blocked execution state when external assistance is required", DefaultEnabled: true},
	{ToolName: "workflow_journal_note", Category: "Workflow Management", Description: "Add a workflow journal note or entry", DefaultEnabled: true},
	{ToolName: "mecha_workflow_transition", Category: "Workflow Management", Description: "Inspect and execute workflow state transitions", DefaultEnabled: true},
	{ToolName: "context_reset", Category: "Workflow Management", Description: "Trigger a clean context reset", DefaultEnabled: true},
	{ToolName: "mecha_context", Category: "Workflow Management", Description: "Read session context usage and threshold warnings", DefaultEnabled: true},
	{ToolName: "workflow_list", Category: "Workflow Management", Description: "List all workflows with summary statistics. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "workflow_get", Category: "Workflow Management", Description: "Retrieve full workflow details including states and export bundle. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "workflow_save", Category: "Workflow Management", Description: "Create or update a workflow from an export bundle or fields. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "workflow_delete", Category: "Workflow Management", Description: "Delete a workflow and its associated states. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "workflow_assign", Category: "Workflow Management", Description: "Activate or deactivate a workflow on a managed agent. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "workflow_state_list", Category: "Workflow Management", Description: "List states for a specific workflow as summaries. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "workflow_state_get", Category: "Workflow Management", Description: "Retrieve full details for an individual workflow state. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "workflow_state_save", Category: "Workflow Management", Description: "Create or update an individual workflow state. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "workflow_state_delete", Category: "Workflow Management", Description: "Delete an individual workflow state. For workflow system architecture, concepts, and bundle format details, see the workflow-management skill.", DefaultEnabled: false, IsAdmin: true},

	// Knowledge Base & Search
	{ToolName: "kb_search", Category: "Knowledge Base & Search", Description: "Search the persistent knowledge base", DefaultEnabled: true},
	{ToolName: "kb_read", Category: "Knowledge Base & Search", Description: "Read the content of a topic or markdown document", DefaultEnabled: true},
	{ToolName: "kb_write", Category: "Knowledge Base & Search", Description: "Write or update a markdown document in the knowledge base", DefaultEnabled: true},
	{ToolName: "kb_delete", Category: "Knowledge Base & Search", Description: "Delete a markdown document from the knowledge base", DefaultEnabled: true},
	{ToolName: "kb_list", Category: "Knowledge Base & Search", Description: "List topics available in the knowledge base", DefaultEnabled: true},
	{ToolName: "google_search", Category: "Knowledge Base & Search", Description: "Search the web using Google Search", DefaultEnabled: true},
	{ToolName: "codex_search", Category: "Knowledge Base & Search", Description: "Search the web using OpenAI Codex web search", DefaultEnabled: true},

	// Worker Administration (disabled by default)
	{ToolName: "worker_spawn", Category: "Worker Administration", Description: "Spawn a new managed worker", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "worker_terminate", Category: "Worker Administration", Description: "Terminate a managed worker", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "worker_set_model", Category: "Worker Administration", Description: "Change the model assigned to a worker", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "worker_set_goal", Category: "Worker Administration", Description: "Set, inspect, or clear the goal of a worker", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "worker_status", Category: "Worker Administration", Description: "Query detailed status of workers", DefaultEnabled: false, IsAdmin: true},
	{ToolName: "worker_list", Category: "Worker Administration", Description: "List managed workers with status filtering, goal policy, worktree metadata, and uptime", DefaultEnabled: false, IsAdmin: true},
}
