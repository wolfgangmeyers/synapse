import {
  isAckFrame,
  isActivityFrame,
  isFrameId,
  isGoalControlFrame,
  isGoalSettings,
  isGoalStateFrame,
  isInjectionRequestedFrame,
  isLeaseRenewFrame,
  isModelCatalogFrame,
  isModelControlFrame,
  isNackFrame,
  isPingFrame,
  isRecord,
  isRegisterFrame,
  isMessageArrivalClientFrame,
  isReminderLockControlFrame,
  isSubagentLifecycleEventFrame,
  isSubagentLifecycleSnapshotFrame,
  isSubagentStateResyncFrame,
  isSubagentSyncEnableFrame,
  isDeliveryItem,
} from "./guards";
import { ProtocolValidationError } from "./errors";
import type {
  AckFrame,
  ActivityFrame,
  GoalControlFrame,
  GoalStateFrame,
  InjectionRequestedFrame,
  LeaseRenewFrame,
  MessageArrivalClientFrame,
  ModelCatalogFrame,
  ModelControlFrame,
  NackFrame,
  PingFrame,
  RegisterFrame,
  ReminderLockControlFrame,
  SubagentLifecycleEventFrame,
  SubagentLifecycleSnapshotFrame,
  SubagentStateResyncFrame,
  SubagentSyncEnableFrame,
  DeliveryItem,
} from "./types";

interface ReceiptFrames {
  injectionRequested?: InjectionRequestedFrame;
  ack?: AckFrame;
  nack?: NackFrame;
}

export type BatchReceiptFrames = ReceiptFrames;

type RecordValue = Record<string, unknown>;

function inputRecord(input: unknown): RecordValue {
  if (!isRecord(input)) throw new ProtocolValidationError("Builder input must be an object");
  return input;
}

function generatedFrameId(): string {
  const uuid = globalThis.crypto?.randomUUID();
  if (typeof uuid !== "string" || !isFrameId(uuid)) throw new ProtocolValidationError("Unable to create a valid frame ID");
  return uuid;
}

function alternateKey(key: string): string | undefined {
  switch (key) {
    case "frameId": return "frame_id";
    case "agentId": return "agent_id";
    case "instanceId": return "instance_id";
    case "leaseId": return "lease_id";
    case "targetAgentId": return "target_agent_id";
    case "targetInstanceId": return "target_instance_id";
    case "controlId": return "control_id";
    case "reminderId": return "reminder_id";
    case "messageId": return "message_id";
    case "attemptNumber": return "attempt_number";
    case "deliveryItems": return "delivery_items";
    case "failureReason": return "failure_reason";
    case "afterVersion": return "after_version";
    case "activityState": return "activity_state";
    case "subagentCount": return "subagent_count";
    case "currentModel": return "current_model";
    case "modelSummaries": return "model_summaries";
    case "totalCount": return "total_count";
    case "goalState": return "goal_state";
    case "parentSessionId": return "parent_session_id";
    case "producerEpoch": return "producer_epoch";
    case "clientSequence": return "client_sequence";
    case "eventId": return "event_id";
    case "lifecycleEvent": return "lifecycle_event";
    case "terminalReason": return "terminal_reason";
    case "createdAt": return "created_at";
    default: return undefined;
  }
}

function inputValue(record: RecordValue, key: string): unknown {
  const value = record[key];
  if (value !== undefined) return value;
  const alternate = alternateKey(key);
  return alternate === undefined ? undefined : record[alternate];
}

function suppliedFrameId(record: RecordValue): string {
  const value = inputValue(record, "frameId");
  if (value === undefined) return generatedFrameId();
  if (!isFrameId(value)) throw new ProtocolValidationError("frameId must be a schema-valid frame ID");
  return value;
}

function requiredValue(record: RecordValue, key: string): unknown {
  const value = inputValue(record, key);
  if (value === undefined) throw new ProtocolValidationError(`${key} is required`);
  return value;
}

function valid<T>(candidate: RecordValue, guard: (value: unknown) => value is T): T {
  if (!guard(candidate)) throw new ProtocolValidationError("Builder input does not match the Mailbox V2 schema");
  return candidate;
}

function identity(record: RecordValue): RecordValue {
  return {
    agent_id: requiredValue(record, "agentId"),
    instance_id: requiredValue(record, "instanceId"),
    lease_id: requiredValue(record, "leaseId"),
  };
}

export function buildRegisterFrame(input: unknown): RegisterFrame {
  const record = inputRecord(input);
  return valid({
    version: "v1",
    frame_id: suppliedFrameId(record),
    type: "register",
    agent_id: requiredValue(record, "agentId"),
    instance_id: requiredValue(record, "instanceId"),
    agent_role: "user_interaction",
    client_type: "web",
    ack_confirmation: "correlated_v1",
    conversation_sync: "v1",
    delivery_receipts: "delivery_items_v1",
  }, isRegisterFrame);
}

export function buildLeaseRenewFrame(input: unknown): LeaseRenewFrame {
  const record = inputRecord(input);
  return valid({ version: "v1", frame_id: suppliedFrameId(record), type: "lease_renew", ...identity(record) }, isLeaseRenewFrame);
}

export function buildPingFrame(input: unknown = {}): PingFrame {
  const record = inputRecord(input);
  return valid({ version: "v1", frame_id: suppliedFrameId(record), type: "ping" }, isPingFrame);
}

export function buildActivityFrame(input: unknown): ActivityFrame {
  const record = inputRecord(input);
  const candidate: RecordValue = { version: "v1", frame_id: suppliedFrameId(record), type: "activity", ...identity(record), activity_state: requiredValue(record, "activityState") };
  const optionalFields: Array<[string, string]> = [["subagentCount", "subagent_count"], ["subagents", "subagents"], ["goal", "goal"], ["currentModel", "current_model"]];
  for (const [inputKey, outputKey] of optionalFields) {
    const value = inputValue(record, inputKey);
    if (value !== undefined) candidate[outputKey] = value;
  }
  return valid(candidate, isActivityFrame);
}

export function buildModelCatalogFrame(input: unknown): ModelCatalogFrame {
  const record = inputRecord(input);
  const candidate: RecordValue = {
    version: "v1",
    frame_id: suppliedFrameId(record),
    type: "model_catalog",
    ...identity(record),
    model_summaries: requiredValue(record, "modelSummaries"),
  };
  for (const [inputKey, outputKey] of [["totalCount", "total_count"], ["truncated", "truncated"], ["currentModel", "current_model"]] as const) {
    const value = inputValue(record, inputKey);
    if (value !== undefined) candidate[outputKey] = value;
  }
  return valid(candidate, isModelCatalogFrame);
}

export function buildModelControlFrame(input: unknown): ModelControlFrame {
  const record = inputRecord(input);
  return valid({
    version: "v1",
    frame_id: suppliedFrameId(record),
    type: "model_control",
    ...identity(record),
    target_agent_id: requiredValue(record, "targetAgentId"),
    target_instance_id: requiredValue(record, "targetInstanceId"),
    control_id: requiredValue(record, "controlId"),
    action: "set_model",
    model: requiredValue(record, "model"),
  }, isModelControlFrame);
}

function buildGoalCandidate(record: RecordValue): RecordValue {
  const settings = requiredValue(record, "settings");
  if (!isGoalSettings(settings)) throw new ProtocolValidationError("settings must be a valid goal control settings object");
  return {
    version: "v1",
    frame_id: suppliedFrameId(record),
    type: "goal_control",
    ...identity(record),
    target_agent_id: requiredValue(record, "targetAgentId"),
    target_instance_id: requiredValue(record, "targetInstanceId"),
    control_id: requiredValue(record, "controlId"),
    action: requiredValue(record, "action"),
    goal: requiredValue(record, "goal"),
    settings,
  };
}

export function buildGoalControlFrame(input: unknown): GoalControlFrame {
  return valid(buildGoalCandidate(inputRecord(input)), isGoalControlFrame);
}

export function buildGoalStateFrame(input: unknown): GoalStateFrame {
  const record = inputRecord(input);
  return valid({ version: "v1", frame_id: suppliedFrameId(record), type: "goal_state", ...identity(record), goal_state: requiredValue(record, "goalState") }, isGoalStateFrame);
}

export function buildMessageArrivalFrame(input: unknown): MessageArrivalClientFrame {
  const record = inputRecord(input);
  const candidate: RecordValue = {
    version: "v1",
    frame_id: suppliedFrameId(record),
    type: "message_arrival",
    ...identity(record),
    from: requiredValue(record, "from"),
    source: requiredValue(record, "source"),
    preview: requiredValue(record, "preview"),
    body: requiredValue(record, "body"),
    created_at: requiredValue(record, "createdAt"),
    message_id: requiredValue(record, "messageId"),
  };
  const subject = inputValue(record, "subject");
  if (subject !== undefined) candidate.subject = subject;
  return valid(candidate, isMessageArrivalClientFrame);
}

export function buildReminderLockControlFrame(input: unknown): ReminderLockControlFrame {
  const record = inputRecord(input);
  return valid({
    version: "v1",
    frame_id: suppliedFrameId(record),
    type: "reminder_lock_control",
    ...identity(record),
    target_agent_id: requiredValue(record, "targetAgentId"),
    target_instance_id: requiredValue(record, "targetInstanceId"),
    control_id: requiredValue(record, "controlId"),
    reminder_id: requiredValue(record, "reminderId"),
    action: requiredValue(record, "action"),
  }, isReminderLockControlFrame);
}

function receiptCandidate(record: RecordValue, type: "ack" | "nack", deliveryItems?: DeliveryItem[]): RecordValue {
  const candidate: RecordValue = {
    version: "v1",
    frame_id: suppliedFrameId(record),
    type,
    ...identity(record),
    message_id: requiredValue(record, "messageId"),
    attempt_number: requiredValue(record, "attemptNumber"),
  };
  if (deliveryItems !== undefined) candidate.delivery_items = deliveryItems;
  if (type === "nack") candidate.reason = requiredValue(record, "reason");
  return candidate;
}

function validateReceiptIdentity(record: RecordValue, deliveryItems: DeliveryItem[] | undefined): void {
  if (deliveryItems === undefined) return;
  const first = deliveryItems[0];
  if (first === undefined || requiredValue(record, "messageId") !== first.message_id || requiredValue(record, "attemptNumber") !== first.attempt_number) throw new ProtocolValidationError("Receipt identity must match the first delivery item");
}

export function buildInjectionRequestedFrame(input: unknown): InjectionRequestedFrame {
  const record = inputRecord(input);
  const rawItems = inputValue(record, "deliveryItems");
  const deliveryItems = rawItems === undefined ? undefined : validatedItems(rawItems);
  validateReceiptIdentity(record, deliveryItems);
  return valid({
    version: "v1",
    frame_id: suppliedFrameId(record),
    type: "injection_requested",
    ...identity(record),
    message_id: requiredValue(record, "messageId"),
    attempt_number: requiredValue(record, "attemptNumber"),
    ...(deliveryItems === undefined ? {} : { delivery_items: deliveryItems }),
  }, isInjectionRequestedFrame);
}

export function buildAckFrame(input: unknown): AckFrame {
  const record = inputRecord(input);
  const rawItems = inputValue(record, "deliveryItems");
  const deliveryItems = rawItems === undefined ? undefined : validatedItems(rawItems);
  validateReceiptIdentity(record, deliveryItems);
  return valid(receiptCandidate(record, "ack", deliveryItems), isAckFrame);
}

export function buildNackFrame(input: unknown): NackFrame {
  const record = inputRecord(input);
  const rawItems = inputValue(record, "deliveryItems");
  const deliveryItems = rawItems === undefined ? undefined : validatedItems(rawItems);
  validateReceiptIdentity(record, deliveryItems);
  return valid(receiptCandidate(record, "nack", deliveryItems), isNackFrame);
}

function validatedItems(input: unknown): DeliveryItem[] {
  if (!Array.isArray(input) || input.length === 0) throw new ProtocolValidationError("deliveryItems must contain at least one item");
  const items: DeliveryItem[] = [];
  for (const item of input) {
    if (!isDeliveryItem(item)) throw new ProtocolValidationError("deliveryItems contains an invalid item");
    items.push(item);
  }
  return items;
}

function receiptIdentityInput(record: RecordValue): RecordValue {
  return {
    agentId: requiredValue(record, "agentId"),
    instanceId: requiredValue(record, "instanceId"),
    leaseId: requiredValue(record, "leaseId"),
  };
}

export function buildBatchReceipts(input: unknown): BatchReceiptFrames {
  const record = inputRecord(input);
  const items = validatedItems(requiredValue(record, "deliveryItems"));
  const failureReason = inputValue(record, "failureReason");
  const first = items[0];
  if (first === undefined) throw new ProtocolValidationError("deliveryItems must contain at least one item");
  const base = receiptIdentityInput(record);
  if (failureReason !== undefined) {
    return {
      nack: buildNackFrame({ ...base, messageId: first.message_id, attemptNumber: first.attempt_number, deliveryItems: items, reason: failureReason }),
    };
  }
  return {
    injectionRequested: valid({ version: "v1", frame_id: generatedFrameId(), type: "injection_requested", ...identity(record), message_id: first.message_id, attempt_number: first.attempt_number, delivery_items: items }, isInjectionRequestedFrame),
    ack: buildAckFrame({ ...base, deliveryItems: items, messageId: first.message_id, attemptNumber: first.attempt_number }),
  };
}

export const buildDeliveryReceipts = buildBatchReceipts;

function lifecycleCandidate(record: RecordValue, type: "subagent_lifecycle_snapshot" | "subagent_lifecycle_event"): RecordValue {
  return {
    version: "v1",
    frame_id: suppliedFrameId(record),
    type,
    agent_id: requiredValue(record, "agentId"),
    instance_id: requiredValue(record, "instanceId"),
    parent_session_id: requiredValue(record, "parentSessionId"),
    lease_id: requiredValue(record, "leaseId"),
    producer_epoch: requiredValue(record, "producerEpoch"),
    client_sequence: requiredValue(record, "clientSequence"),
    event_id: requiredValue(record, "eventId"),
  };
}

export function buildSubagentLifecycleSnapshotFrame(input: unknown): SubagentLifecycleSnapshotFrame {
  const record = inputRecord(input);
  return valid({ ...lifecycleCandidate(record, "subagent_lifecycle_snapshot"), subagents: requiredValue(record, "subagents") }, isSubagentLifecycleSnapshotFrame);
}

export function buildSubagentLifecycleEventFrame(input: unknown): SubagentLifecycleEventFrame {
  const record = inputRecord(input);
  const candidate: RecordValue = { ...lifecycleCandidate(record, "subagent_lifecycle_event"), lifecycle_event: requiredValue(record, "lifecycleEvent"), subagent: requiredValue(record, "subagent") };
  const terminalReason = inputValue(record, "terminalReason");
  if (terminalReason !== undefined) candidate.terminal_reason = terminalReason;
  return valid(candidate, isSubagentLifecycleEventFrame);
}

export function buildSubagentSyncEnableFrame(input: unknown): SubagentSyncEnableFrame {
  const record = inputRecord(input);
  const candidate: RecordValue = { version: "v1", frame_id: suppliedFrameId(record), type: "subagent_sync_enable", ...identity(record) };
  const afterVersion = inputValue(record, "afterVersion");
  if (afterVersion !== undefined) candidate.after_version = afterVersion;
  return valid(candidate, isSubagentSyncEnableFrame);
}

export function buildSubagentStateResyncFrame(input: unknown): SubagentStateResyncFrame {
  const record = inputRecord(input);
  return valid({ version: "v1", frame_id: suppliedFrameId(record), type: "subagent_state_resync", ...identity(record), after_version: requiredValue(record, "afterVersion") }, isSubagentStateResyncFrame);
}

