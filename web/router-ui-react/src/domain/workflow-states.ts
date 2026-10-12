export type WorkflowReminderType = "one_shot" | "recurring";

export interface WorkflowTransitionConfig {
  target_state_id: string;
  description: string;
}

export interface WorkflowReminderConfig {
  id: string;
  type: WorkflowReminderType;
  subject?: string;
  body: string;
  delay_seconds: number;
  interval_seconds?: number;
}

export interface WorkflowState {
  id: string;
  workflow_id?: string | null;
  name: string;
  description: string;
  entry_text: string;
  goal?: string | undefined;
  reminders?: WorkflowReminderConfig[];
  allowed_tools?: string[];
  transitions?: WorkflowTransitionConfig[];
  allow_exit?: boolean;
  ui_pos_x?: number;
  ui_pos_y?: number;
  created_at: string;
  updated_at: string;
}

export interface WorkflowStatePosition {
  state_id: string;
  ui_pos_x: number;
  ui_pos_y: number;
}

export interface Workflow {
  id: string;
  name: string;
  description: string;
  entry_state_id: string | null;
  created_at: string;
  updated_at: string;
  state_count?: number;
}

export interface WorkflowWithStates {
  workflow: Workflow;
  states: WorkflowState[];
}

export interface WorkflowStateInput {
  workflow_id?: string | null;
  name: string;
  description?: string;
  entry_text?: string;
  goal?: string | undefined;
  reminders?: WorkflowReminderConfig[];
  allowed_tools?: string[];
  transitions?: WorkflowTransitionConfig[];
  allow_exit?: boolean;
  ui_pos_x?: number;
  ui_pos_y?: number;
}

export interface WorkflowStateResponse {
  workflow_state: WorkflowState;
}

export interface WorkflowStatesResponse {
  workflow_states: WorkflowState[];
}

export interface WorkflowToolsResponse {
  tools: KnownToolDefinition[];
}

export interface KnownToolDefinition {
  id: string;
  name: string;
  category: "system" | "lifecycle" | "subagents" | "memory";
  description: string;
}

export const KNOWN_WORKFLOW_TOOLS: KnownToolDefinition[] = [
  { id: "read", name: "read", category: "system", description: "Read file contents" },
  { id: "edit", name: "edit", category: "system", description: "Make targeted edits to files" },
  { id: "write", name: "write", category: "system", description: "Write or overwrite files" },
  { id: "bash", name: "bash", category: "system", description: "Execute shell commands" },
  { id: "google_search", name: "google_search", category: "system", description: "Search the web using Google Search via Grounding" },
  { id: "mecha_mailbox_send", name: "mecha_mailbox_send", category: "lifecycle", description: "Send async mailbox messages" },
  { id: "goal", name: "goal", category: "lifecycle", description: "Inspect or control goals" },
  { id: "goal_snooze", name: "goal_snooze", category: "lifecycle", description: "Snooze goal evaluations" },
  { id: "mecha_context", name: "mecha_context", category: "lifecycle", description: "Manage context compaction" },
  { id: "mecha_reminder", name: "mecha_reminder", category: "lifecycle", description: "Schedule reminders" },
  { id: "mecha_workflow_transition", name: "mecha_workflow_transition", category: "lifecycle", description: "Transition workflow state or exit workflow" },
  { id: "context_reset", name: "context_reset", category: "lifecycle", description: "Reset agent context" },
  { id: "scratchpad", name: "scratchpad", category: "memory", description: "Persistent scratchpad memory" },
  { id: "subagent", name: "subagent", category: "subagents", description: "Launch background subagents" },
  { id: "get_subagent_result", name: "get_subagent_result", category: "subagents", description: "Snapshot subagent outputs" },
  { id: "steer_subagent", name: "steer_subagent", category: "subagents", description: "Send steering messages to subagents" },
  { id: "mecha_worker", name: "mecha_worker", category: "subagents", description: "Launch independent workers" },
];
