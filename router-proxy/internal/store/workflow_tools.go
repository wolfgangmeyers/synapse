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
	{ID: "mecha_mailbox_send", Name: "mecha_mailbox_send", Category: "lifecycle", Description: "Send async mailbox messages"},
	{ID: "goal", Name: "goal", Category: "lifecycle", Description: "Inspect or control goals"},
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
	{ID: "mecha_worker", Name: "mecha_worker", Category: "subagents", Description: "Launch independent workers"},
	{ID: "worker_spawn", Name: "worker_spawn", Category: "admin", Description: "Spawn a new independent managed worker"},
	{ID: "worker_terminate", Name: "worker_terminate", Category: "admin", Description: "Terminate a running managed worker"},
	{ID: "worker_set_model", Name: "worker_set_model", Category: "admin", Description: "Change the model of a managed worker"},
	{ID: "worker_set_goal", Name: "worker_set_goal", Category: "admin", Description: "Set, inspect, or clear the goal of a managed worker"},
	{ID: "worker_status", Name: "worker_status", Category: "admin", Description: "Inspect detailed state and active subagents of managed workers"},
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
