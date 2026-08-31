export type Version = "v1";
export type FrameId = string;
export type AgentId = string;
export type InstanceId = string;
export type LeaseId = string;
export type MessageId = string;
export type ReminderId = string;
export type AttachmentId = string;
export type MessageIncarnationId = string;
export type UtcMillis = string;

export type AgentRole = "agent" | "user_interaction";
export type ClientActivityState = "active" | "settled" | "compacting";
export type ObservableActivityState = "active" | "idle" | "compacting";
export type MessageSourceKind = "agent" | "web_ui" | "android" | "discord" | "reminder" | "system" | "unknown";
export type DeliveryReceiptMode = "delivery_items_v1";
export type LifecycleChildStatus = "queued" | "running" | "completed" | "steered" | "aborted" | "stopped" | "error";
export type FailureReason = string;

export interface MessageSource {
  kind: MessageSourceKind;
}

export interface SentinelDescriptor {
  model: string;
}

export interface PromptTemplate {
  id: string;
  name: string;
  body: string;
  created_at: UtcMillis;
  updated_at: UtcMillis;
}

export type ReminderState = "scheduled" | "due" | "queued" | "leased" | "injection_requested" | "acknowledged" | "failed" | "dead_letter";

export interface Reminder {
  id: ReminderId;
  target: AgentId;
  creator: AgentId;
  body: string;
  created_at: UtcMillis;
  next_due_at: UtcMillis;
  interval_ms: number | null;
  state: ReminderState;
  linked_message_id: MessageId | null;
  linked_message_state?: string | null;
  occurrence_number: number;
  attempt_count: number;
  last_attempt_at: UtcMillis | null;
  subject?: string | null;
  sentinel?: SentinelDescriptor;
}

export interface Agent {
  agent_id: AgentId;
  instance_id: InstanceId;
  agent_role: AgentRole;
  connected_at: UtcMillis;
  lease_expires_at: UtcMillis;
  activity_state: ObservableActivityState;
  subagent_count: number;
  subagents: SubagentSummary[];
  available_models: ModelSummary[];
  model_catalog_total_count: number;
  model_catalog_truncated: boolean;
  current_model?: ModelSummary;
  goal?: GoalProjection;
  goal_state?: GoalState;
  subagent_version?: number;
}

export interface Envelope {
  id: MessageId;
  from: string;
  to: AgentId;
  body: string;
  created_at: UtcMillis;
  subject?: string;
  sentinel?: SentinelDescriptor;
  source?: MessageSource;
}

export type AttachmentMimeType = "image/png" | "image/jpeg" | "image/webp" | "image/gif";

export interface AttachmentReference {
  id: AttachmentId;
  url: string;
  mime_type: AttachmentMimeType;
  size_bytes: number;
  name?: string;
}

export interface ConversationMessage {
  sequence: number;
  id: MessageId;
  message_incarnation_id: MessageIncarnationId;
  from: string;
  to: AgentId;
  body: string;
  created_at: UtcMillis;
  recorded_at: UtcMillis;
  attachments: AttachmentReference[];
  subject?: string;
  source?: MessageSource;
}

export interface SubagentSummary {
  id: string;
  description: string;
  type?: string;
  status?: LifecycleChildStatus;
}

export interface ModelSummary {
  provider: string;
  id: string;
  name?: string;
}

export interface GoalProjection {
  enabled: boolean;
  goal: string;
}

export interface GoalOutcomeCounts {
  COMPLETED: number;
  NOT_DONE: number;
}

export interface GoalControlSettings {
  goal_lock_enabled: boolean;
  snooze_enabled: boolean;
  snooze_duration_ms: number;
  snooze_turn_limit: number | null;
  snooze_budget_cap_ms: number | null;
  snooze_refill_yield_ms: number;
  snooze_refill_elapsed_ms: number;
}

export interface GoalState extends GoalControlSettings {
  enabled: boolean;
  goal: string;
  outcome_counts: GoalOutcomeCounts;
}

export interface DeliveryItem {
  message_id: MessageId;
  message_incarnation_id: MessageIncarnationId;
  attempt_number: number;
  delivery_expires_at: UtcMillis;
}

export interface RegisterFrame {
  version: "v1";
  frame_id: FrameId;
  type: "register";
  agent_id: AgentId;
  instance_id: InstanceId;
  agent_role?: AgentRole;
  client_type?: string;
  ack_confirmation?: "correlated_v1";
  conversation_sync?: "v1";
  delivery_receipts?: DeliveryReceiptMode;
}

export interface LeaseRenewFrame {
  version: "v1";
  frame_id: FrameId;
  type: "lease_renew";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
}

export interface PingFrame {
  version: "v1";
  frame_id: FrameId;
  type: "ping";
}

export interface ActivityFrame {
  version: "v1";
  frame_id: FrameId;
  type: "activity";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  activity_state: ClientActivityState;
  subagent_count?: number;
  subagents?: SubagentSummary[];
  goal?: GoalProjection;
  current_model?: ModelSummary;
}

export interface ModelCatalogFrame {
  version: "v1";
  frame_id: FrameId;
  type: "model_catalog";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  model_summaries: ModelSummary[];
  total_count?: number;
  truncated?: boolean;
  current_model?: ModelSummary;
}

export interface ModelControlFrame {
  version: "v1";
  frame_id: FrameId;
  type: "model_control";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  target_agent_id: AgentId;
  target_instance_id: InstanceId;
  control_id: FrameId;
  action: "set_model";
  model: ModelSummary;
}

export interface ModelControlForwardFrame {
  version: "v1";
  frame_id: FrameId;
  type: "model_control";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  source_agent_id: AgentId;
  control_id: FrameId;
  action: "set_model";
  model: ModelSummary;
  expires_at: UtcMillis;
}

export interface ModelControlAcceptedResultFrame {
  version: "v1";
  frame_id: FrameId;
  type: "model_control_result";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  source_agent_id: AgentId;
  control_id: FrameId;
  accepted: true;
  current_model: ModelSummary;
}

export interface ModelControlRejectedResultFrame {
  version: "v1";
  frame_id: FrameId;
  type: "model_control_result";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  source_agent_id: AgentId;
  control_id: FrameId;
  accepted: false;
  error_code: string;
  current_model: ModelSummary;
}

export type ModelControlResultFrame = ModelControlAcceptedResultFrame | ModelControlRejectedResultFrame;

export interface GoalControlFrame {
  version: "v1";
  frame_id: FrameId;
  type: "goal_control";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  target_agent_id: AgentId;
  target_instance_id: InstanceId;
  control_id: FrameId;
  action: "set" | "off";
  goal: string;
  settings: GoalControlSettings;
}

export interface GoalControlForwardFrame {
  version: "v1";
  frame_id: FrameId;
  type: "goal_control";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  source_agent_id: AgentId;
  control_id: FrameId;
  revision: number;
  action: "set" | "off";
  goal: string;
  settings: GoalControlSettings;
  expires_at: UtcMillis;
}

export interface GoalControlAcceptedResultFrame {
  version: "v1";
  frame_id: FrameId;
  type: "goal_control_result";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  source_agent_id: AgentId;
  control_id: FrameId;
  revision: number;
  accepted: true;
  goal_state: GoalState;
}

export interface GoalControlRejectedResultFrame {
  version: "v1";
  frame_id: FrameId;
  type: "goal_control_result";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  source_agent_id: AgentId;
  control_id: FrameId;
  revision: number;
  accepted: false;
  error_code: string;
  goal_state: GoalState;
}

export type GoalControlResultFrame = GoalControlAcceptedResultFrame | GoalControlRejectedResultFrame;

export interface ReminderLockControlFrame {
  version: "v1";
  frame_id: FrameId;
  type: "reminder_lock_control";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  target_agent_id: AgentId;
  target_instance_id: InstanceId;
  control_id: FrameId;
  reminder_id: ReminderId;
  action: "set" | "clear";
}

export interface ReminderLockControlForwardFrame {
  version: "v1";
  frame_id: FrameId;
  type: "reminder_lock_control";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  source_agent_id: AgentId;
  control_id: FrameId;
  reminder_id: ReminderId;
  action: "set" | "clear";
  expires_at: UtcMillis;
}

export interface ReminderLockControlAcceptedResultFrame {
  version: "v1";
  frame_id: FrameId;
  type: "reminder_lock_control_result";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  source_agent_id: AgentId;
  control_id: FrameId;
  reminder_id: ReminderId;
  accepted: true;
}

export interface ReminderLockControlRejectedResultFrame {
  version: "v1";
  frame_id: FrameId;
  type: "reminder_lock_control_result";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  source_agent_id: AgentId;
  control_id: FrameId;
  reminder_id: ReminderId;
  accepted: false;
  error_code: string;
}

export type ReminderLockControlResultFrame = ReminderLockControlAcceptedResultFrame | ReminderLockControlRejectedResultFrame;

export interface GoalStateFrame {
  version: "v1";
  frame_id: FrameId;
  type: "goal_state";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  goal_state: GoalState;
}

export interface MessageArrivalClientFrame {
  version: "v1";
  frame_id: FrameId;
  type: "message_arrival";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  from: string;
  source: { kind: "agent" | "discord" | "reminder" };
  preview: string;
  body: string;
  created_at: UtcMillis;
  message_id: MessageId;
  subject?: string;
}

export interface InjectionRequestedFrame {
  version: "v1";
  frame_id: FrameId;
  type: "injection_requested";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  message_id: MessageId;
  attempt_number: number;
  delivery_items?: DeliveryItem[];
}

export interface AckFrame {
  version: "v1";
  frame_id: FrameId;
  type: "ack";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  message_id: MessageId;
  attempt_number: number;
  delivery_items?: DeliveryItem[];
}

export interface NackFrame {
  version: "v1";
  frame_id: FrameId;
  type: "nack";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  message_id: MessageId;
  attempt_number: number;
  reason: FailureReason;
  delivery_items?: DeliveryItem[];
}

export interface SubagentSyncEnableFrame {
  version: "v1";
  frame_id: FrameId;
  type: "subagent_sync_enable";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  after_version?: number;
}

export interface LifecycleChild {
  id: string;
  type?: string;
  description?: string;
  status?: LifecycleChildStatus;
}

export interface SubagentLifecycleSnapshotFrame {
  version: "v1";
  frame_id: FrameId;
  type: "subagent_lifecycle_snapshot";
  agent_id: AgentId;
  instance_id: InstanceId;
  parent_session_id: FrameId;
  lease_id: LeaseId;
  producer_epoch: string;
  client_sequence: number;
  event_id: FrameId;
  subagents: LifecycleChild[];
}

export interface SubagentLifecycleEventFrame {
  version: "v1";
  frame_id: FrameId;
  type: "subagent_lifecycle_event";
  agent_id: AgentId;
  instance_id: InstanceId;
  parent_session_id: FrameId;
  lease_id: LeaseId;
  producer_epoch: string;
  client_sequence: number;
  event_id: FrameId;
  lifecycle_event: "started" | "terminal";
  subagent: LifecycleChild;
  terminal_reason?: FailureReason;
}

export interface SubagentStateResyncFrame {
  version: "v1";
  frame_id: FrameId;
  type: "subagent_state_resync";
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  after_version: number;
}

export interface RegisteredFrame {
  version: "v1";
  frame_id: FrameId;
  type: "registered";
  request_id: FrameId;
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  expires_at: UtcMillis;
  recovered: boolean;
  agent_role: AgentRole;
  ack_confirmation?: "correlated_v1";
  conversation_sync?: "v1";
}

export interface LeaseRejectedFrame {
  version: "v1";
  frame_id: FrameId;
  type: "lease_rejected";
  request_id: FrameId;
  code: "duplicate_identity";
  agent_id: AgentId;
  instance_id: InstanceId;
  active_instance_id: InstanceId;
}

export interface ErrorFrame {
  version: "v1";
  frame_id: FrameId;
  type: "error";
  code: string;
  request_id?: FrameId;
}

export interface PongFrame {
  version: "v1";
  frame_id: FrameId;
  type: "pong";
  request_id: FrameId;
}

export interface MessageFrame {
  version: "v1";
  frame_id: FrameId;
  type: "message";
  message: Envelope;
  message_incarnation_id: MessageIncarnationId;
  attempt_number: number;
  delivery_expires_at: UtcMillis;
  delivery_items?: DeliveryItem[];
  delivery_messages?: Envelope[];
}

export interface ConversationMessageFrame {
  version: "v1";
  frame_id: FrameId;
  type: "conversation_message";
  message: ConversationMessage;
}

export interface AgentGoalStateFrame {
  version: "v1";
  frame_id: FrameId;
  type: "agent_goal_state";
  agent_id: AgentId;
  instance_id: InstanceId;
  goal_state: GoalState;
  generated_at: UtcMillis;
}

export interface AgentActivityFrame {
  version: "v1";
  frame_id: FrameId;
  type: "agent_activity";
  agent_id: AgentId;
  instance_id?: InstanceId;
  activity_state: ObservableActivityState;
  subagent_count?: number;
  goal?: GoalProjection;
  subagents?: SubagentSummary[];
  current_model?: ModelSummary;
  generated_at: UtcMillis;
}

export interface AgentModelCatalogFrame {
  version: "v1";
  frame_id: FrameId;
  type: "agent_model_catalog";
  agent_id: AgentId;
  instance_id: InstanceId;
  model_summaries: ModelSummary[];
  total_count?: number;
  truncated?: boolean;
  current_model?: ModelSummary;
  generated_at: UtcMillis;
}

export interface MessageArrivalServerFrame {
  version: "v1";
  frame_id: FrameId;
  type: "message_arrival";
  agent_id: AgentId;
  from: string;
  source: { kind: "agent" | "discord" | "reminder" };
  preview: string;
  body: string;
  created_at: UtcMillis;
  message_id: MessageId;
  subject?: string;
}

export interface DeliveryAcknowledgedFrame {
  version: "v1";
  frame_id: FrameId;
  type: "delivery_acknowledged";
  request_id: FrameId;
  agent_id: AgentId;
  instance_id: InstanceId;
  lease_id: LeaseId;
  message_id: MessageId;
  message_incarnation_id: MessageIncarnationId;
  attempt_number: number;
}

export interface SubagentStateAgent {
  agent_id: AgentId;
  subagent_version: number;
  subagent_count: number;
  subagents: LifecycleChild[];
}

export interface SubagentStateSnapshotFrame {
  version: "v1";
  frame_id: FrameId;
  type: "subagent_state_snapshot";
  router_version: number;
  agents: SubagentStateAgent[];
}

export interface SubagentStateUpdateFrame {
  version: "v1";
  frame_id: FrameId;
  type: "subagent_state_update";
  router_version: number;
  agent: SubagentStateAgent;
}

export interface SubagentStateReplayFrame {
  version: "v1";
  frame_id: FrameId;
  type: "subagent_state_replay";
  router_version: number;
  updates: SubagentStateAgent[];
}

export type ClientFrame =
  | RegisterFrame
  | LeaseRenewFrame
  | ActivityFrame
  | ModelCatalogFrame
  | ModelControlFrame
  | ModelControlResultFrame
  | GoalControlFrame
  | GoalControlResultFrame
  | ReminderLockControlFrame
  | ReminderLockControlResultFrame
  | GoalStateFrame
  | MessageArrivalClientFrame
  | InjectionRequestedFrame
  | AckFrame
  | NackFrame
  | PingFrame
  | SubagentSyncEnableFrame
  | SubagentLifecycleSnapshotFrame
  | SubagentLifecycleEventFrame
  | SubagentStateResyncFrame;

export type ServerFrame =
  | RegisteredFrame
  | LeaseRejectedFrame
  | ErrorFrame
  | PongFrame
  | MessageFrame
  | ConversationMessageFrame
  | ModelControlForwardFrame
  | ModelControlResultFrame
  | GoalControlForwardFrame
  | GoalControlResultFrame
  | ReminderLockControlForwardFrame
  | ReminderLockControlResultFrame
  | AgentGoalStateFrame
  | DeliveryAcknowledgedFrame
  | AgentActivityFrame
  | AgentModelCatalogFrame
  | MessageArrivalServerFrame
  | SubagentStateSnapshotFrame
  | SubagentStateUpdateFrame
  | SubagentStateReplayFrame
  | SubagentLifecycleAcknowledgedFrame;

export interface SubagentLifecycleAcknowledgedFrame {
  version: "v1";
  frame_id: FrameId;
  type: "subagent_lifecycle_acknowledged";
  request_id: FrameId;
  event_id: FrameId;
  client_sequence: number;
  producer_epoch: string;
  agent_id: AgentId;
  instance_id: InstanceId;
  parent_session_id: FrameId;
  lease_id: LeaseId;
  disposition: "applied" | "accepted_noop" | "duplicate" | "stale" | "conflict";
  router_version: number;
  applied_version?: number;
}

export type MailboxFrame = ClientFrame | ServerFrame;
export type Frame = MailboxFrame;
