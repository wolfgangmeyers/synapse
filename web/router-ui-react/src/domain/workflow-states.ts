export interface WorkflowState {
  id: string;
  name: string;
  description: string;
  entry_text: string;
  created_at: string;
  updated_at: string;
}

export interface WorkflowStateInput {
  name: string;
  description?: string;
  entry_text?: string;
}

export interface WorkflowStateResponse {
  workflow_state: WorkflowState;
}

export interface WorkflowStatesResponse {
  workflow_states: WorkflowState[];
}
