package store

// WorkflowToolDefinition represents metadata about a tool available in workflows.
type WorkflowToolDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

// CanonicalWorkflowTools defines the single source of truth for workflow tools catalog.
var CanonicalWorkflowTools = []WorkflowToolDefinition{
	{ID: "read", Name: "read", Category: "system", Description: "Read file contents"},
	{ID: "edit", Name: "edit", Category: "system", Description: "Make targeted edits to files"},
	{ID: "write", Name: "write", Category: "system", Description: "Write or overwrite files"},
	{ID: "bash", Name: "bash", Category: "system", Description: "Execute shell commands"},
	{ID: "google_search", Name: "google_search", Category: "system", Description: "Search the web using Google Search via Grounding"},
	{ID: "codex_search", Name: "codex_search", Category: "system", Description: "Search the web using OpenAI Codex web search"},
	{ID: "mecha_mailbox_send", Name: "mecha_mailbox_send", Category: "lifecycle", Description: "Send async mailbox messages"},
	{ID: "goal", Name: "goal", Category: "lifecycle", Description: "Inspect or control goals"},
	{ID: "workflow_snooze", Name: "workflow_snooze", Category: "lifecycle", Description: "Snooze active workflow continuation"},
	{ID: "goal_snooze", Name: "goal_snooze", Category: "lifecycle", Description: "Snooze goal evaluations"},
	{ID: "mecha_context", Name: "mecha_context", Category: "lifecycle", Description: "Inspect context usage and reset status"},
	{ID: "mecha_reminder", Name: "mecha_reminder", Category: "lifecycle", Description: "Schedule reminders"},
	{ID: "mecha_workflow_transition", Name: "mecha_workflow_transition", Category: "lifecycle", Description: "Transition workflow state or exit workflow"},
	{ID: "context_reset", Name: "context_reset", Category: "lifecycle", Description: "Reset agent context"},
	{ID: "mecha_worker_terminate", Name: "mecha_worker_terminate", Category: "lifecycle", Description: "Terminate self and optionally remove assigned worktree"},
	{ID: "scratchpad", Name: "scratchpad", Category: "memory", Description: "Persistent scratchpad memory"},
	{ID: "subagent", Name: "subagent", Category: "subagents", Description: "Launch background subagents"},
	{ID: "get_subagent_result", Name: "get_subagent_result", Category: "subagents", Description: "Snapshot subagent outputs"},
	{ID: "steer_subagent", Name: "steer_subagent", Category: "subagents", Description: "Send steering messages to subagents"},
	{ID: "list_subagents", Name: "list_subagents", Category: "subagents", Description: "List background subagents"},
	{ID: "mecha_worker", Name: "mecha_worker", Category: "subagents", Description: "List, send mail to or clean up independent workers"},
	{ID: "mecha_request_inline_tool_allowance", Name: "mecha_request_inline_tool_allowance", Category: "system", Description: "Request a temporary exception for genuinely necessary inline use of a named tool"},
	{ID: "mecha_tool_timeout", Name: "mecha_tool_timeout", Category: "system", Description: "Temporarily extend tool execution timeout"},
	{ID: "mecha_restart_router", Name: "mecha_restart_router", Category: "system", Description: "Request a controlled restart of the mailbox router"},
	{ID: "mecha_send_image", Name: "mecha_send_image", Category: "lifecycle", Description: "Send an image to the user or another recipient"},
	{ID: "mecha_progress", Name: "mecha_progress", Category: "lifecycle", Description: "Track and report measurable task progress"},
	{ID: "mecha_blocked", Name: "mecha_blocked", Category: "lifecycle", Description: "Set or clear blocked execution state when external assistance is required"},
	{ID: "stop_subagent", Name: "stop_subagent", Category: "subagents", Description: "Stop a running background subagent"},
	{ID: "kb_search", Name: "kb_search", Category: "memory", Description: "Search the persistent knowledge base"},
	{ID: "kb_read", Name: "kb_read", Category: "memory", Description: "Read the content of a topic or markdown document"},
	{ID: "kb_write", Name: "kb_write", Category: "memory", Description: "Write or update a markdown document in the knowledge base"},
	{ID: "kb_delete", Name: "kb_delete", Category: "memory", Description: "Delete a markdown document from the knowledge base"},
	{ID: "kb_list", Name: "kb_list", Category: "memory", Description: "List topics available in the knowledge base"},
	{ID: "worker_spawn", Name: "worker_spawn", Category: "admin", Description: "Spawn a new independent managed worker"},
	{ID: "worker_terminate", Name: "worker_terminate", Category: "admin", Description: "Terminate a running managed worker"},
	{ID: "worker_set_model", Name: "worker_set_model", Category: "admin", Description: "Change the model of a managed worker"},
	{ID: "worker_set_goal", Name: "worker_set_goal", Category: "admin", Description: "Set, inspect, or clear the goal of a managed worker"},
	{ID: "worker_status", Name: "worker_status", Category: "admin", Description: "Inspect detailed state and active subagents of managed workers"},
	{ID: "workflow_list", Name: "workflow_list", Category: "admin", Description: "List all workflows with summary statistics"},
	{ID: "workflow_get", Name: "workflow_get", Category: "admin", Description: "Retrieve full workflow details including states and export bundle"},
	{ID: "workflow_save", Name: "workflow_save", Category: "admin", Description: "Create or update a workflow from an export bundle or fields"},
	{ID: "workflow_delete", Name: "workflow_delete", Category: "admin", Description: "Delete a workflow and its associated states"},
	{ID: "workflow_assign", Name: "workflow_assign", Category: "admin", Description: "Activate or deactivate a workflow on a managed agent"},
	{ID: "workflow_state_list", Name: "workflow_state_list", Category: "admin", Description: "List states for a specific workflow as summaries"},
	{ID: "workflow_state_get", Name: "workflow_state_get", Category: "admin", Description: "Retrieve full details for an individual workflow state"},
	{ID: "workflow_state_save", Name: "workflow_state_save", Category: "admin", Description: "Create or update an individual workflow state"},
	{ID: "workflow_state_delete", Name: "workflow_state_delete", Category: "admin", Description: "Delete an individual workflow state"},
}

// ValidWorkflowTools is derived directly from CanonicalWorkflowTools, including interchangeable aliases.
var ValidWorkflowTools = func() map[string]bool {
	m := make(map[string]bool, len(CanonicalWorkflowTools)+2)
	for _, tool := range CanonicalWorkflowTools {
		m[tool.ID] = true
	}
	// Add aliases
	m["reset"] = true
	m["workflow_transition"] = true
	return m
}()
