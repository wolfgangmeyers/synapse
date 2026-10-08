export type WorkflowReminderType = "one_shot" | "recurring";

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
  name: string;
  description: string;
  entry_text: string;
  reminders?: WorkflowReminderConfig[];
  created_at: string;
  updated_at: string;
}

export interface WorkflowStateInput {
  name: string;
  description?: string;
  entry_text?: string;
  reminders?: WorkflowReminderConfig[];
}

export interface WorkflowStateResponse {
  workflow_state: WorkflowState;
}

export interface WorkflowStatesResponse {
  workflow_states: WorkflowState[];
}

