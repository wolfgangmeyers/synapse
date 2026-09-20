import type { ProtocolError } from "./errors";
import type {
  AckFrame,
  ActivityFrame,
  AgentActivityFrame,
  AgentGoalStateFrame,
  AgentModelCatalogFrame,
  AttachmentReference,
  ClientFrame,
  ConversationMessage,
  ConversationMessageFrame,
  DeliveryAcknowledgedFrame,
  DeliveryItem,
  DeliveryReceiptMode,
  Envelope,
  ErrorFrame,
  GoalControlFrame,
  GoalControlForwardFrame,
  GoalControlResultFrame,
  GoalControlSettings,
  GoalOutcomeCounts,
  GoalProjection,
  GoalState,
  GoalStateFrame,
  InjectionRequestedFrame,
  InlineToolControlForwardFrame,
  InlineToolControlFrame,
  InlineToolControlResultFrame,
  InlineToolStateFrame,
  LeaseRejectedFrame,
  LeaseRenewFrame,
  LifecycleChild,
  MailboxFrame,
  MessageArrivalClientFrame,
  MessageArrivalServerFrame,
  MessageFrame,
  ModelCatalogFrame,
  ModelControlForwardFrame,
  ModelControlFrame,
  ModelControlResultFrame,
  ModelSummary,
  NackFrame,
  PingFrame,
  PongFrame,
  RegisterFrame,
  RemindersChangedFrame,
  RegisteredFrame,
  ReminderLockControlForwardFrame,
  ReminderLockControlFrame,
  ReminderLockControlResultFrame,
  ServerFrame,
  SubagentLifecycleAcknowledgedFrame,
  SubagentLifecycleEventFrame,
  SubagentLifecycleSnapshotFrame,
  SubagentStateAgent,
  SubagentStateReplayFrame,
  SubagentStateResyncFrame,
  SubagentStateSnapshotFrame,
  SubagentStateUpdateFrame,
  SubagentSummary,
  SubagentSyncEnableFrame,
  UILiveFrame,
  ManagedAgent,
  ManagedAgentArchive,
  ManagedChild,
  ManagedChildrenProjection,
  ManagedHistoryEntry,
  JsonValue,
} from "./types";

type RecordValue = Record<string, unknown>;
type Result<T> = { ok: true; value: T } | { ok: false; error: ProtocolError };

export type ParseResult<T> = Result<T>;

const frameIdPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const agentIdPattern = /^(?!.*@)[A-Za-z0-9][A-Za-z0-9._-]*$/;
const messageIdPattern = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
const reminderIdPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$/;
const attachmentIdPattern = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
const v4Pattern = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const utcMillisPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
const routerUtcTimestampPattern = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?Z$/;
const lifecycleEpochPattern = /^[0-9a-fA-F-]{36}$/;
const failureReasonPattern = /^[a-z][a-z0-9_]*$/;
const managedHistorySensitivePattern = /token|secret|password|authorization|cookie|credential|argument|request body|stdout|stderr|environment|session_|session id|process_|process id|runtime_generation|\/users\/|\/home\/|\/tmp\//i;

const invalid = (path: string, message: string): ProtocolError => ({ code: "invalid_protocol_value", path, message });

export function isRecord(value: unknown): value is RecordValue {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasOnlyKeys(value: RecordValue, keys: readonly string[]): boolean {
  return Object.keys(value).every((key) => keys.includes(key));
}

function isSafeInteger(value: unknown, minimum: number, maximum = Number.MAX_SAFE_INTEGER): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum && value <= maximum;
}

function isSafeString(value: unknown, minimum: number, maximum: number): value is string {
  if (typeof value !== "string" || value.length < minimum || value.length > maximum || value.includes("\u0000")) return false;
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (Number.isNaN(next) || next < 0xdc00 || next > 0xdfff) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
  }
  return true;
}

function isPatternString(value: unknown, pattern: RegExp, minimum: number, maximum: number): value is string {
  return isSafeString(value, minimum, maximum) && pattern.test(value);
}

function isExactRecord(value: unknown, keys: readonly string[]): value is RecordValue {
  return isRecord(value) && Object.keys(value).length === keys.length && hasOnlyKeys(value, keys);
}

export function isFrameId(value: unknown): value is string { return isPatternString(value, frameIdPattern, 1, 128); }
export function isAgentId(value: unknown): value is string { return isPatternString(value, agentIdPattern, 1, 128); }
export function isMessageId(value: unknown): value is string { return isPatternString(value, messageIdPattern, 1, 128); }
export function isReminderId(value: unknown): value is string { return isPatternString(value, reminderIdPattern, 1, 99); }
export function isAttachmentId(value: unknown): value is string { return isPatternString(value, attachmentIdPattern, 1, 192); }
export function isMessageIncarnationId(value: unknown): value is string { return isPatternString(value, v4Pattern, 36, 36); }
export function isFailureReason(value: unknown): value is string { return isPatternString(value, failureReasonPattern, 1, 64); }

export function isUtcMillis(value: unknown): value is string {
  if (!isPatternString(value, utcMillisPattern, 24, 24)) return false;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) && new Date(parsed).toISOString() === value;
}

// Router-managed projections are formatted by Go with time.RFC3339Nano. Keep
// this separate from the established millisecond-only P1/P2 wire validators.
export function isRouterUtcTimestamp(value: unknown): value is string {
  if (!isSafeString(value, 20, 30)) return false;
  const match = routerUtcTimestampPattern.exec(value);
  if (match === null) return false;
  const [, year, month, day, hour, minute, second] = match;
  const parsed = new Date(0);
  parsed.setUTCFullYear(Number(year), Number(month) - 1, Number(day));
  parsed.setUTCHours(Number(hour), Number(minute), Number(second), 0);
  return Number.isFinite(parsed.getTime())
    && parsed.getUTCFullYear() === Number(year)
    && parsed.getUTCMonth() === Number(month) - 1
    && parsed.getUTCDate() === Number(day)
    && parsed.getUTCHours() === Number(hour)
    && parsed.getUTCMinutes() === Number(minute)
    && parsed.getUTCSeconds() === Number(second);
}

export function isManagedAgent(value: unknown): value is ManagedAgent {
  const required = ["agent_id", "source", "cwd", "model", "desired_state", "lifecycle_state", "activity_state", "accepting_prompts", "manifest", "created_at", "started_at", "updated_at", "last_error"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, "goal_state"]) || !required.every((key) => Object.hasOwn(value, key))) return false;
  const model = value.model; const manifest = value.manifest; const lastError = value.last_error;
  if (!isAgentId(value.agent_id) || value.source !== "router-managed" || !isSafeString(value.cwd, 1, 4096) || !isSafeString(value.desired_state, 1, 64) || !isSafeString(value.lifecycle_state, 1, 64) || !isSafeString(value.activity_state, 1, 64) || typeof value.accepting_prompts !== "boolean" || !isRouterUtcTimestamp(value.created_at) || (value.started_at !== null && !isRouterUtcTimestamp(value.started_at)) || !isRouterUtcTimestamp(value.updated_at)) return false;
  if (!isExactRecord(model, ["provider", "id"]) || !isSafeString(model.provider, 1, 128) || !isSafeString(model.id, 1, 256)) return false;
  if (!isExactRecord(manifest, ["revision", "sha256", "installed_tool_count"]) || !isSafeInteger(manifest.revision, 1) || !isPatternString(manifest.sha256, /^[0-9a-f]{64}$/, 64, 64) || !isSafeInteger(manifest.installed_tool_count, 0, 1024)) return false;
  return (value.goal_state === undefined || isGoalState(value.goal_state)) && (lastError === null || isExactRecord(lastError, ["code", "message", "at"]) && isSafeString(lastError.code, 1, 128) && isSafeString(lastError.message, 1, 1024) && isRouterUtcTimestamp(lastError.at));
}

function isManagedArchiveDelivery(value: unknown): boolean {
  if (!isRecord(value) || !isExactRecord(value, ["disposition", "total", "queued_never_attempted", "in_flight_or_unknown", "completed", "released_or_failed", "overflow"])) return false;
  if (!isSafeString(value.disposition, 1, 64)) return false;
  for (const key of ["total", "queued_never_attempted", "in_flight_or_unknown", "completed", "released_or_failed"]) {
    if (!isSafeInteger(value[key], 0)) return false;
  }
  const overflow = value.overflow;
  return isExactRecord(overflow, ["total", "queued_never_attempted", "in_flight_or_unknown", "completed", "released_or_failed"])
    && ["total", "queued_never_attempted", "in_flight_or_unknown", "completed", "released_or_failed"].every((key) => typeof overflow[key] === "boolean");
}

export function isManagedAgentArchive(value: unknown): value is ManagedAgentArchive {
  return isRecord(value) && isExactRecord(value, ["record_kind", "lifecycle_state", "agent_id", "name", "archive_key", "terminal_outcome", "completed_at", "delivery"])
    && value.record_kind === "archived" && value.lifecycle_state === "archived" && isAgentId(value.agent_id)
    && isSafeString(value.name, 1, 512) && isSafeString(value.archive_key, 1, 128)
    && isSafeString(value.terminal_outcome, 1, 64) && isRouterUtcTimestamp(value.completed_at)
    && isManagedArchiveDelivery(value.delivery);
}

export function isManagedAgentRecord(value: unknown): value is ManagedAgent | ManagedAgentArchive {
  return isManagedAgent(value) || isManagedAgentArchive(value);
}

function isManagedChildModel(value: unknown): boolean {
  return isRecord(value) && hasOnlyKeys(value, ["provider", "id", "name"]) && isSafeString(value.provider, 1, 128) && isSafeString(value.id, 1, 256) && (value.name === undefined || isSafeString(value.name, 1, 256));
}
function isManagedChildCompaction(value: unknown): boolean {
  return isRecord(value) && hasOnlyKeys(value, ["state", "count", "started_at", "last_outcome"])
    && (value.state === "idle" || value.state === "compacting") && isSafeInteger(value.count, 0)
    && (value.started_at === undefined || isRouterUtcTimestamp(value.started_at))
    && (value.last_outcome === undefined || value.last_outcome === "completed" || value.last_outcome === "failed" || value.last_outcome === "aborted" || value.last_outcome === "reconciled_unknown");
}
function isManagedChildRecovery(value: unknown): boolean {
  if (!isRecord(value) || !isRouterUtcTimestamp(value.observed_at)) return false;
  if (value.state === "confirmed") return isExactRecord(value, ["state", "cause", "observed_at"]) && value.cause === null;
  return isExactRecord(value, ["state", "cause", "observed_at"]) && value.state === "interrupted_unknown" && (value.cause === "snapshot_omitted" || value.cause === "parent_process_lost" || value.cause === "session_changed");
}
function isManagedChild(value: unknown): value is ManagedChild {
  if (!isRecord(value) || !hasOnlyKeys(value, ["id", "parent_entry_id", "task_id", "run_id", "sequence", "recovery", "description", "model", "status", "started_at", "ended_at", "duration_ms", "compaction", "compaction_state"])) return false;
  return isSafeString(value.id, 1, 128) && isSafeString(value.parent_entry_id, 1, 128) && isSafeString(value.task_id, 1, 128) && isSafeString(value.run_id, 1, 128) && isSafeInteger(value.sequence, 0) && isManagedChildRecovery(value.recovery)
    && (value.description === undefined || isSafeString(value.description, 0, 512)) && (value.model === undefined || isManagedChildModel(value.model)) && (isChildStatus(value.status) || value.status === "unknown")
    && (value.started_at === undefined || isRouterUtcTimestamp(value.started_at)) && (value.ended_at === undefined || isRouterUtcTimestamp(value.ended_at)) && (value.duration_ms === undefined || isSafeInteger(value.duration_ms, 0))
    && isManagedChildCompaction(value.compaction) && typeof value.compaction_state === "string" && ["none", "compacting", "completed", "unknown"].includes(value.compaction_state);
}
export function isManagedChildrenProjection(value: unknown): value is ManagedChildrenProjection {
  return isExactRecord(value, ["agent_id", "source", "lifecycle_version", "recovery_state", "children"]) && isAgentId(value.agent_id) && value.source === "router-managed" && isSafeInteger(value.lifecycle_version, 0) && (value.recovery_state === "current" || value.recovery_state === "reconstructing") && Array.isArray(value.children) && value.children.length <= 999 && value.children.every(isManagedChild);
}

function isProviderName(value: unknown): value is "openai-codex" | "google" | "cursor" { return value === "openai-codex" || value === "google" || value === "cursor"; }
function isSafeProviderURL(value: unknown): boolean {
  if (!isSafeString(value, 1, 2048)) return false;
  try {
    const url = new URL(value);
    // Provider links are informational only, but keeping the boundary strict
    // prevents a live frame from turning the router UI into a credential link.
    return url.protocol === "https:" && url.hostname !== "" && url.username === "" && url.password === "" && url.hash === "";
  } catch {
    return false;
  }
}

function isProviderPrompt(value: unknown): boolean {
  if (!isRecord(value) || !hasOnlyKeys(value, ["id", "kind", "message", "placeholder", "url"]) || !isSafeString(value.id, 1, 128) || typeof value.kind !== "string" || !["text", "secret", "select", "manual_code"].includes(value.kind) || !isSafeString(value.message, 1, 512)) return false;
  return (value.placeholder === undefined || isSafeString(value.placeholder, 0, 256)) && (value.url === undefined || isSafeProviderURL(value.url));
}

function isSafeManagedHistoryString(value: unknown, minimum: number, maximum: number): value is string {
  return isSafeString(value, minimum, maximum) && !managedHistorySensitivePattern.test(value);
}
function isManagedHistoryError(value: unknown): boolean {
  return isRecord(value) && hasOnlyKeys(value, ["code", "retryable", "detail_available"])
    && isPatternString(value.code, /^[A-Za-z][A-Za-z0-9_]*$/, 1, 128) && typeof value.retryable === "boolean" && typeof value.detail_available === "boolean";
}
function isManagedHistoryPresentation(value: unknown): boolean {
  if (!isRecord(value) || !hasOnlyKeys(value, ["kind", "tool_name", "outcome", "from", "to", "child_label", "child_state", "summary", "error"]) || typeof value.kind !== "string" || !["message", "lifecycle", "state", "tool", "child", "compaction", "recovery", "error", "reminder", "model", "auth"].includes(value.kind)) return false;
  if (value.tool_name !== undefined && !isPatternString(value.tool_name, /^[A-Za-z][A-Za-z0-9_.-]*$/, 1, 128)) return false;
  if (value.outcome !== undefined && (typeof value.outcome !== "string" || !["active", "success", "warning", "error", "unknown", "compacting", "completed", "failed", "interrupted", "aborted", "reconciled_unknown"].includes(value.outcome))) return false;
  if (value.from !== undefined && !isSafeManagedHistoryString(value.from, 1, 48)) return false;
  if (value.to !== undefined && !isSafeManagedHistoryString(value.to, 1, 48)) return false;
  if (value.child_label !== undefined && !isSafeManagedHistoryString(value.child_label, 1, 160)) return false;
  if (value.child_state !== undefined && (typeof value.child_state !== "string" || !["queued", "running", "completed", "steered", "aborted", "stopped", "error", "confirmed", "interrupted_unknown"].includes(value.child_state))) return false;
  return (value.summary === undefined || isSafeManagedHistoryString(value.summary, 1, 512)) && (value.error === undefined || isManagedHistoryError(value.error));
}
function isJsonValue(value: unknown, ancestors = new Set<object>(), depth = 0): value is JsonValue {
  if (value === null || typeof value === "boolean" || typeof value === "string") return true;
  if (typeof value === "number") return Number.isFinite(value);
  if (depth >= 64 || typeof value !== "object") return false;
  if (ancestors.has(value)) return false;
  ancestors.add(value);
  const valid = Array.isArray(value)
    ? value.every((item) => isJsonValue(item, ancestors, depth + 1))
    : isRecord(value)
      && (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null)
      && Object.values(value).every((item) => isJsonValue(item, ancestors, depth + 1));
  ancestors.delete(value);
  return valid;
}
function isManagedHistoryToolInput(value: unknown): value is JsonValue {
  if (!isJsonValue(value)) return false;
  try {
    const encoded = JSON.stringify(value);
    return encoded !== undefined && new TextEncoder().encode(encoded).byteLength <= 4 * 1024 * 1024;
  } catch {
    return false;
  }
}
export function isManagedHistoryEntry(value: unknown): value is ManagedHistoryEntry {
  return isRecord(value) && hasOnlyKeys(value, ["id", "agent_id", "at", "level", "message", "presentation", "message_content", "sender_identity", "tool_input"])
    && isSafeString(value.id, 1, 128)
    && isAgentId(value.agent_id)
    && (value.at === undefined || isRouterUtcTimestamp(value.at))
    && (value.level === undefined || isSafeString(value.level, 1, 64))
    && (value.message === undefined || isSafeString(value.message, 0, 65536))
    && (value.presentation === undefined || isManagedHistoryPresentation(value.presentation))
    && (value.message_content === undefined || isSafeString(value.message_content, 0, 65536))
    && (value.sender_identity === undefined || isSafeString(value.sender_identity, 1, 128))
    && (value.tool_input === undefined || isManagedHistoryToolInput(value.tool_input));
}
export function isManagedCompleteHistoryEntry(value: unknown): value is ManagedHistoryEntry {
  return isManagedHistoryEntry(value) && (value.message === undefined || isSafeManagedHistoryString(value.message, 0, 512));
}

export function isUILiveFrame(value: unknown): value is UILiveFrame {
  if (!isRecord(value) || value.protocol !== "mecha.web/v1") return false;
  if (value.type === "managed_children_snapshot") return isExactRecord(value, ["type", "protocol", "lifecycle_version", "generated_at", "children"]) && isSafeInteger(value.lifecycle_version, 0) && isRouterUtcTimestamp(value.generated_at) && Array.isArray(value.children) && value.children.every(isManagedChildrenProjection);
  if (!isSafeInteger(value.projection_version, 0)) return false;
  if (value.type === "managed_agents_snapshot") return isExactRecord(value, ["type", "protocol", "projection_version", "generated_at", "agents"]) && isRouterUtcTimestamp(value.generated_at) && Array.isArray(value.agents) && value.agents.every(isManagedAgentRecord);
  // Older routers add an internal record key to lifecycle frames. It is not
  // presentation data, but accepting this one known field keeps a termination
  // from invalidating the complete managed-agent projection.
  if (value.type === "managed_agent_delta") return hasOnlyKeys(value, ["type", "protocol", "projection_version", "base_version", "agent_id", "changes", "record_key"]) && (value.record_key === undefined || isSafeString(value.record_key, 1, 256)) && isSafeInteger(value.base_version, 0) && isAgentId(value.agent_id) && isManagedAgent(value.changes) && value.changes.agent_id === value.agent_id;
  if (value.type === "managed_agent_removed") return hasOnlyKeys(value, ["type", "protocol", "projection_version", "base_version", "agent_id", "record_key"]) && (value.record_key === undefined || isSafeString(value.record_key, 1, 256)) && isSafeInteger(value.base_version, 0) && isAgentId(value.agent_id);
  if (value.type === "history_entry") return isExactRecord(value, ["type", "protocol", "projection_version", "view", "entry"]) && (value.view === "quiet" ? isManagedHistoryEntry(value.entry) : value.view === "complete" && isManagedCompleteHistoryEntry(value.entry));
  if (value.type === "provider_auth_prompt") return isExactRecord(value, ["type", "protocol", "projection_version", "operation_id", "provider", "prompt"]) && isSafeString(value.operation_id, 1, 128) && isProviderName(value.provider) && isProviderPrompt(value.prompt);
  if (value.type === "provider_auth_state") return isRecord(value) && hasOnlyKeys(value, ["type", "protocol", "projection_version", "operation_id", "provider", "status", "error"]) && isSafeString(value.operation_id, 1, 128) && isProviderName(value.provider) && typeof value.status === "string" && ["pending", "succeeded", "failed", "expired", "cancelled"].includes(value.status) && (value.error === undefined || isSafeString(value.error, 1, 512));
  return false;
}

function isAgentRole(value: unknown): value is "agent" | "user_interaction" { return value === "agent" || value === "user_interaction"; }
function isClientActivityState(value: unknown): value is ActivityFrame["activity_state"] { return value === "active" || value === "settled" || value === "compacting"; }
function isObservableActivityState(value: unknown): value is AgentActivityFrame["activity_state"] { return value === "active" || value === "idle" || value === "compacting"; }
function isChildStatus(value: unknown): value is LifecycleChild["status"] { return value === "queued" || value === "running" || value === "completed" || value === "steered" || value === "aborted" || value === "stopped" || value === "error"; }
function isSourceKind(value: unknown): boolean { return value === "agent" || value === "web_ui" || value === "android" || value === "discord" || value === "reminder" || value === "system" || value === "unknown"; }
function isArrivalSourceKind(value: unknown): boolean { return value === "agent" || value === "discord" || value === "reminder"; }

function isFrame(value: unknown, type: string, keys: readonly string[]): value is RecordValue {
  return isRecord(value) && hasOnlyKeys(value, keys) && value.version === "v1" && isFrameId(value.frame_id) && value.type === type;
}

function isLeaseFields(value: RecordValue): boolean {
  return isAgentId(value.agent_id) && isFrameId(value.instance_id) && isFrameId(value.lease_id);
}

function isTargetFields(value: RecordValue): boolean {
  return isAgentId(value.target_agent_id) && isFrameId(value.target_instance_id) && isFrameId(value.control_id);
}

export function isModelSummary(value: unknown): value is ModelSummary {
  if (!isRecord(value) || !hasOnlyKeys(value, ["provider", "id", "name"]) || !isSafeString(value.provider, 1, 128) || !isSafeString(value.id, 1, 256)) return false;
  return value.name === undefined || isSafeString(value.name, 1, 256);
}

export function isGoalProjection(value: unknown): value is GoalProjection {
  return isRecord(value) && hasOnlyKeys(value, ["enabled", "goal"]) && typeof value.enabled === "boolean" && isSafeString(value.goal, value.enabled ? 1 : 0, 4000) && (value.enabled || value.goal === "");
}

export function isGoalSettings(value: unknown): value is GoalControlSettings {
  if (!isRecord(value) || !hasOnlyKeys(value, ["goal_lock_enabled", "snooze_enabled", "snooze_duration_ms", "snooze_turn_limit", "snooze_budget_cap_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms"])) return false;
  return typeof value.goal_lock_enabled === "boolean" && typeof value.snooze_enabled === "boolean" && isSafeInteger(value.snooze_duration_ms, 1) && (value.snooze_turn_limit === null || isSafeInteger(value.snooze_turn_limit, 1, 20)) && (value.snooze_budget_cap_ms === null || isSafeInteger(value.snooze_budget_cap_ms, 1)) && isSafeInteger(value.snooze_refill_yield_ms, 1) && isSafeInteger(value.snooze_refill_elapsed_ms, 1);
}

export function isGoalOutcomeCounts(value: unknown): value is GoalOutcomeCounts {
  return isRecord(value) && hasOnlyKeys(value, ["COMPLETED", "NOT_DONE"]) && isSafeInteger(value.COMPLETED, 0, 1_000_000) && isSafeInteger(value.NOT_DONE, 0, 1_000_000);
}

export function isGoalState(value: unknown): value is GoalState {
  if (!isRecord(value) || !hasOnlyKeys(value, ["enabled", "goal", "goal_lock_enabled", "snooze_enabled", "snooze_duration_ms", "snooze_turn_limit", "snooze_budget_cap_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms", "outcome_counts"]) || typeof value.enabled !== "boolean" || !isSafeString(value.goal, value.enabled ? 1 : 0, 4000) || (value.enabled === false && value.goal !== "")) return false;
  return isGoalSettings(value) || (typeof value.goal_lock_enabled === "boolean" && typeof value.snooze_enabled === "boolean" && isSafeInteger(value.snooze_duration_ms, 1) && (value.snooze_turn_limit === null || isSafeInteger(value.snooze_turn_limit, 1, 20)) && (value.snooze_budget_cap_ms === null || isSafeInteger(value.snooze_budget_cap_ms, 1)) && isSafeInteger(value.snooze_refill_yield_ms, 1) && isSafeInteger(value.snooze_refill_elapsed_ms, 1) && isGoalOutcomeCounts(value.outcome_counts));
}

export function isSubagentSummary(value: unknown): value is SubagentSummary {
  if (!isRecord(value) || !hasOnlyKeys(value, ["id", "type", "description", "status"]) || !isSafeString(value.id, 1, 128) || !isSafeString(value.description, 1, 512)) return false;
  return (value.type === undefined || isSafeString(value.type, 1, 64)) && (value.status === undefined || isChildStatus(value.status));
}

function isSubagentSummaries(value: unknown): value is SubagentSummary[] {
  return Array.isArray(value) && value.length <= 999 && value.every(isSubagentSummary);
}

export function isLifecycleChild(value: unknown): value is LifecycleChild {
  if (!isRecord(value) || !hasOnlyKeys(value, ["id", "type", "description", "status"]) || !isSafeString(value.id, 1, 128)) return false;
  return (value.type === undefined || isSafeString(value.type, 1, 64)) && (value.description === undefined || isSafeString(value.description, 1, 512)) && (value.status === undefined || isChildStatus(value.status));
}

function isLifecycleChildren(value: unknown): value is LifecycleChild[] {
  return Array.isArray(value) && value.length <= 999 && value.every(isLifecycleChild);
}

function isModelSummaries(value: unknown): value is ModelSummary[] {
  return Array.isArray(value) && value.length <= 1000 && value.every(isModelSummary);
}

function isMessageSource(value: unknown): boolean {
  return isRecord(value) && hasOnlyKeys(value, ["kind"]) && isSourceKind(value.kind);
}

function isArrivalSource(value: unknown): boolean {
  return isRecord(value) && hasOnlyKeys(value, ["kind"]) && isArrivalSourceKind(value.kind);
}

function isSentinel(value: unknown): boolean {
  return isRecord(value) && hasOnlyKeys(value, ["model"]) && isPatternString(value.model, /^[A-Za-z0-9][A-Za-z0-9._:-]*$/, 1, 128);
}

export function isEnvelope(value: unknown): value is Envelope {
  if (!isRecord(value) || !hasOnlyKeys(value, ["id", "from", "to", "body", "created_at", "subject", "sentinel", "source"]) || !isMessageId(value.id) || !isSafeString(value.from, 1, 128) || !isAgentId(value.to) || !isSafeString(value.body, 0, 65536) || !isUtcMillis(value.created_at)) return false;
  return (value.subject === undefined || isSafeString(value.subject, 0, 256)) && (value.sentinel === undefined || isSentinel(value.sentinel)) && (value.source === undefined || isMessageSource(value.source));
}

export function isAttachmentReference(value: unknown): value is AttachmentReference {
  if (!isRecord(value) || !hasOnlyKeys(value, ["id", "url", "mime_type", "size_bytes", "name"]) || !isAttachmentId(value.id) || !isSafeString(value.url, 1, 512) || !/^\/v1\/attachments\/[A-Za-z0-9._~%-]+$/.test(value.url) || !isSafeString(value.mime_type, 1, 16) || !["image/png", "image/jpeg", "image/webp", "image/gif"].includes(value.mime_type) || !isSafeInteger(value.size_bytes, 1, 786432)) return false;
  return value.name === undefined || isSafeString(value.name, 1, 120);
}

export function isDeliveryItem(value: unknown): value is DeliveryItem {
  return isRecord(value) && hasOnlyKeys(value, ["message_id", "message_incarnation_id", "attempt_number", "delivery_expires_at"]) && isMessageId(value.message_id) && isMessageIncarnationId(value.message_incarnation_id) && isSafeInteger(value.attempt_number, 1) && isUtcMillis(value.delivery_expires_at);
}

export function isDeliveryItems(value: unknown): value is DeliveryItem[] {
  return Array.isArray(value) && value.length >= 1 && value.every(isDeliveryItem);
}

export function isConversationMessage(value: unknown): value is ConversationMessage {
  if (!isRecord(value) || !hasOnlyKeys(value, ["sequence", "id", "message_incarnation_id", "from", "to", "subject", "body", "created_at", "recorded_at", "attachments", "source"]) || !isSafeInteger(value.sequence, 1) || !isMessageId(value.id) || !isMessageIncarnationId(value.message_incarnation_id) || !isSafeString(value.from, 1, 128) || !isAgentId(value.to) || !isSafeString(value.body, 0, 65536) || !isUtcMillis(value.created_at) || !isUtcMillis(value.recorded_at) || !Array.isArray(value.attachments) || value.attachments.length > 3 || !value.attachments.every(isAttachmentReference)) return false;
  return (value.subject === undefined || isSafeString(value.subject, 0, 256)) && (value.source === undefined || isMessageSource(value.source));
}

function isCommonLeaseFrame(value: unknown, type: string, keys: readonly string[]): value is RecordValue {
  return isFrame(value, type, keys) && isLeaseFields(value);
}

export function isRegisterFrame(value: unknown): value is RegisterFrame {
  return isFrame(value, "register", ["version", "frame_id", "type", "agent_id", "instance_id", "agent_role", "client_type", "ack_confirmation", "conversation_sync", "delivery_receipts", "inline_tool_control"]) && isAgentId(value.agent_id) && isFrameId(value.instance_id) && (value.agent_role === undefined || isAgentRole(value.agent_role)) && (value.client_type === undefined || isPatternString(value.client_type, /^[a-z][a-z0-9_-]{0,31}$/, 1, 32)) && (value.ack_confirmation === undefined || value.ack_confirmation === "correlated_v1") && (value.conversation_sync === undefined || value.conversation_sync === "v1") && (value.delivery_receipts === undefined || value.delivery_receipts === "delivery_items_v1") && (value.inline_tool_control === undefined || value.inline_tool_control === "v1");
}

export function isLeaseRenewFrame(value: unknown): value is LeaseRenewFrame { return isCommonLeaseFrame(value, "lease_renew", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id"]); }
export function isPingFrame(value: unknown): value is PingFrame { return isFrame(value, "ping", ["version", "frame_id", "type"]); }

export function isActivityFrame(value: unknown): value is ActivityFrame {
  return isCommonLeaseFrame(value, "activity", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "activity_state", "subagent_count", "subagents", "goal", "current_model"]) && isClientActivityState(value.activity_state) && (value.subagent_count === undefined || isSafeInteger(value.subagent_count, 0, 999)) && (value.subagents === undefined || isSubagentSummaries(value.subagents)) && (value.goal === undefined || isGoalProjection(value.goal)) && (value.current_model === undefined || isModelSummary(value.current_model));
}

export function isModelCatalogFrame(value: unknown): value is ModelCatalogFrame {
  return isCommonLeaseFrame(value, "model_catalog", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "model_summaries", "total_count", "truncated", "current_model"]) && isModelSummaries(value.model_summaries) && (value.total_count === undefined || isSafeInteger(value.total_count, 0, 1_000_000)) && (value.truncated === undefined || typeof value.truncated === "boolean") && (value.current_model === undefined || isModelSummary(value.current_model));
}

export function isInlineToolStateFrame(value: unknown): value is InlineToolStateFrame {
  return isCommonLeaseFrame(value, "inline_tool_state", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "enabled"]) && typeof value.enabled === "boolean";
}

export function isInlineToolControlFrame(value: unknown): value is InlineToolControlFrame {
  return isCommonLeaseFrame(value, "inline_tool_control", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "enabled"]) && isTargetFields(value) && typeof value.enabled === "boolean";
}

export function isInlineToolControlForwardFrame(value: unknown): value is InlineToolControlForwardFrame {
  return isCommonLeaseFrame(value, "inline_tool_control", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "action", "enabled", "expires_at"]) && isAgentId(value.source_agent_id) && isFrameId(value.control_id) && value.action === "set" && typeof value.enabled === "boolean" && isUtcMillis(value.expires_at);
}

export function isInlineToolControlResultFrame(value: unknown): value is InlineToolControlResultFrame {
  if (!isCommonLeaseFrame(value, "inline_tool_control_result", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "accepted", "error_code", "enabled"]) || !isAgentId(value.source_agent_id) || !isFrameId(value.control_id) || typeof value.accepted !== "boolean" || typeof value.enabled !== "boolean") return false;
  return value.accepted ? value.error_code === undefined : isSafeString(value.error_code, 1, 128);
}

export function isModelControlFrame(value: unknown): value is ModelControlFrame {
  return isCommonLeaseFrame(value, "model_control", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "model"]) && isTargetFields(value) && value.action === "set_model" && isModelSummary(value.model);
}

export function isModelControlForwardFrame(value: unknown): value is ModelControlForwardFrame {
  return isCommonLeaseFrame(value, "model_control", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "action", "model", "expires_at"]) && isAgentId(value.source_agent_id) && isFrameId(value.control_id) && value.action === "set_model" && isModelSummary(value.model) && isUtcMillis(value.expires_at);
}

export function isModelControlResultFrame(value: unknown): value is ModelControlResultFrame {
  if (!isCommonLeaseFrame(value, "model_control_result", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "accepted", "error_code", "current_model"]) || !isAgentId(value.source_agent_id) || !isFrameId(value.control_id) || typeof value.accepted !== "boolean") return false;
  if (value.accepted) return value.error_code === undefined && isModelSummary(value.current_model);
  return isSafeString(value.error_code, 1, 128) && isModelSummary(value.current_model);
}

export function isGoalControlFrame(value: unknown): value is GoalControlFrame {
  return isCommonLeaseFrame(value, "goal_control", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "goal", "settings"]) && isTargetFields(value) && (value.action === "set" || value.action === "off") && isSafeString(value.goal, value.action === "set" ? 1 : 0, 4000) && (value.action === "set" || value.goal === "") && isGoalSettings(value.settings);
}

export function isGoalControlForwardFrame(value: unknown): value is GoalControlForwardFrame {
  return isCommonLeaseFrame(value, "goal_control", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "revision", "action", "goal", "settings", "expires_at"]) && isAgentId(value.source_agent_id) && isFrameId(value.control_id) && isSafeInteger(value.revision, 1) && (value.action === "set" || value.action === "off") && isSafeString(value.goal, value.action === "set" ? 1 : 0, 4000) && (value.action === "set" || value.goal === "") && isGoalSettings(value.settings) && isUtcMillis(value.expires_at);
}

export function isGoalControlResultFrame(value: unknown): value is GoalControlResultFrame {
  if (!isCommonLeaseFrame(value, "goal_control_result", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "revision", "accepted", "error_code", "goal_state"]) || !isAgentId(value.source_agent_id) || !isFrameId(value.control_id) || !isSafeInteger(value.revision, 1) || typeof value.accepted !== "boolean" || !isGoalState(value.goal_state)) return false;
  return value.accepted ? value.error_code === undefined : isSafeString(value.error_code, 1, 128);
}

export function isReminderLockControlFrame(value: unknown): value is ReminderLockControlFrame {
  return isCommonLeaseFrame(value, "reminder_lock_control", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "reminder_id", "action"]) && isTargetFields(value) && isReminderId(value.reminder_id) && (value.action === "set" || value.action === "clear");
}

export function isReminderLockControlForwardFrame(value: unknown): value is ReminderLockControlForwardFrame {
  return isCommonLeaseFrame(value, "reminder_lock_control", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "reminder_id", "action", "expires_at"]) && isAgentId(value.source_agent_id) && isFrameId(value.control_id) && isReminderId(value.reminder_id) && (value.action === "set" || value.action === "clear") && isUtcMillis(value.expires_at);
}

export function isReminderLockControlResultFrame(value: unknown): value is ReminderLockControlResultFrame {
  if (!isCommonLeaseFrame(value, "reminder_lock_control_result", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "reminder_id", "accepted", "error_code"]) || !isAgentId(value.source_agent_id) || !isFrameId(value.control_id) || !isReminderId(value.reminder_id) || typeof value.accepted !== "boolean") return false;
  return value.accepted ? value.error_code === undefined : isSafeString(value.error_code, 1, 128);
}

export function isGoalStateFrame(value: unknown): value is GoalStateFrame { return isCommonLeaseFrame(value, "goal_state", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "goal_state"]) && isGoalState(value.goal_state); }

function isArrivalFrame(value: unknown, client: boolean): boolean {
  const keys = client ? ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "from", "subject", "source", "preview", "body", "created_at", "message_id"] : ["version", "frame_id", "type", "agent_id", "from", "subject", "source", "preview", "body", "created_at", "message_id"];
  if (!isFrame(value, "message_arrival", keys) || !isAgentId(value.agent_id) || !isSafeString(value.from, 1, 128) || !isArrivalSource(value.source) || !isSafeString(value.preview, 0, 140) || !isSafeString(value.body, 0, 65536) || !isUtcMillis(value.created_at) || !isMessageId(value.message_id) || (value.subject !== undefined && !isSafeString(value.subject, 0, 256))) return false;
  return !client || (isFrameId(value.instance_id) && isFrameId(value.lease_id));
}

export function isMessageArrivalClientFrame(value: unknown): value is MessageArrivalClientFrame { return isArrivalFrame(value, true); }
export function isMessageArrivalServerFrame(value: unknown): value is MessageArrivalServerFrame { return isArrivalFrame(value, false); }

function isReceiptFields(value: unknown, type: string, keys: readonly string[]): value is RecordValue {
  return isCommonLeaseFrame(value, type, keys) && isMessageId(value.message_id) && isSafeInteger(value.attempt_number, 1);
}

export function isInjectionRequestedFrame(value: unknown): value is InjectionRequestedFrame {
  return isReceiptFields(value, "injection_requested", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "message_id", "attempt_number", "delivery_items"]) && (value.delivery_items === undefined || isDeliveryItems(value.delivery_items));
}
export function isAckFrame(value: unknown): value is AckFrame {
  return isReceiptFields(value, "ack", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "message_id", "attempt_number", "delivery_items"]) && (value.delivery_items === undefined || isDeliveryItems(value.delivery_items));
}
export function isNackFrame(value: unknown): value is NackFrame {
  return isReceiptFields(value, "nack", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "message_id", "attempt_number", "delivery_items", "reason"]) && isFailureReason(value.reason) && (value.delivery_items === undefined || isDeliveryItems(value.delivery_items));
}

export function isSubagentSyncEnableFrame(value: unknown): value is SubagentSyncEnableFrame { return isCommonLeaseFrame(value, "subagent_sync_enable", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "after_version"]) && (value.after_version === undefined || isSafeInteger(value.after_version, 0)); }

function isLifecycleIdentity(value: RecordValue): boolean {
  return isAgentId(value.agent_id) && isFrameId(value.instance_id) && isFrameId(value.parent_session_id) && isFrameId(value.lease_id) && isPatternString(value.producer_epoch, lifecycleEpochPattern, 36, 36) && isSafeInteger(value.client_sequence, 1) && isFrameId(value.event_id);
}

export function isSubagentLifecycleSnapshotFrame(value: unknown): value is SubagentLifecycleSnapshotFrame {
  return isFrame(value, "subagent_lifecycle_snapshot", ["version", "frame_id", "type", "agent_id", "instance_id", "parent_session_id", "lease_id", "producer_epoch", "client_sequence", "event_id", "subagents"]) && isLifecycleIdentity(value) && isLifecycleChildren(value.subagents);
}
export function isSubagentLifecycleEventFrame(value: unknown): value is SubagentLifecycleEventFrame {
  if (!isFrame(value, "subagent_lifecycle_event", ["version", "frame_id", "type", "agent_id", "instance_id", "parent_session_id", "lease_id", "producer_epoch", "client_sequence", "event_id", "lifecycle_event", "subagent", "terminal_reason"]) || !isLifecycleIdentity(value) || (value.lifecycle_event !== "started" && value.lifecycle_event !== "terminal") || !isLifecycleChild(value.subagent)) return false;
  return value.lifecycle_event === "started" ? value.terminal_reason === undefined : isFailureReason(value.terminal_reason);
}
export function isSubagentStateResyncFrame(value: unknown): value is SubagentStateResyncFrame { return isCommonLeaseFrame(value, "subagent_state_resync", ["version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "after_version"]) && isSafeInteger(value.after_version, 0); }

function isSubagentStateAgent(value: unknown): value is SubagentStateAgent {
  return isRecord(value) && hasOnlyKeys(value, ["agent_id", "subagent_version", "subagent_count", "subagents"]) && isAgentId(value.agent_id) && isSafeInteger(value.subagent_version, 0) && isSafeInteger(value.subagent_count, 0, 999) && isLifecycleChildren(value.subagents);
}

export function isSubagentStateSnapshotFrame(value: unknown): value is SubagentStateSnapshotFrame { return isFrame(value, "subagent_state_snapshot", ["version", "frame_id", "type", "router_version", "agents"]) && isSafeInteger(value.router_version, 0) && Array.isArray(value.agents) && value.agents.every(isSubagentStateAgent); }
export function isSubagentStateUpdateFrame(value: unknown): value is SubagentStateUpdateFrame { return isFrame(value, "subagent_state_update", ["version", "frame_id", "type", "router_version", "agent"]) && isSafeInteger(value.router_version, 0) && isSubagentStateAgent(value.agent); }
export function isSubagentStateReplayFrame(value: unknown): value is SubagentStateReplayFrame { return isFrame(value, "subagent_state_replay", ["version", "frame_id", "type", "router_version", "updates"]) && isSafeInteger(value.router_version, 0) && Array.isArray(value.updates) && value.updates.every(isSubagentStateAgent); }

export function isRegisteredFrame(value: unknown): value is RegisteredFrame {
  return isFrame(value, "registered", ["version", "frame_id", "type", "request_id", "agent_id", "instance_id", "lease_id", "expires_at", "recovered", "agent_role", "ack_confirmation", "conversation_sync"]) && isFrameId(value.request_id) && isAgentId(value.agent_id) && isFrameId(value.instance_id) && isFrameId(value.lease_id) && isUtcMillis(value.expires_at) && typeof value.recovered === "boolean" && isAgentRole(value.agent_role) && (value.ack_confirmation === undefined || value.ack_confirmation === "correlated_v1") && (value.conversation_sync === undefined || value.conversation_sync === "v1");
}
export function isLeaseRejectedFrame(value: unknown): value is LeaseRejectedFrame { return isFrame(value, "lease_rejected", ["version", "frame_id", "type", "request_id", "code", "agent_id", "instance_id", "active_instance_id"]) && isFrameId(value.request_id) && value.code === "duplicate_identity" && isAgentId(value.agent_id) && isFrameId(value.instance_id) && isFrameId(value.active_instance_id); }
export function isErrorFrame(value: unknown): value is ErrorFrame { return isFrame(value, "error", ["version", "frame_id", "type", "code", "request_id"]) && isSafeString(value.code, 1, 128) && (value.request_id === undefined || isFrameId(value.request_id)); }
export function isPongFrame(value: unknown): value is PongFrame { return isFrame(value, "pong", ["version", "frame_id", "type", "request_id"]) && isFrameId(value.request_id); }

export function isMessageFrame(value: unknown): value is MessageFrame {
  if (!isFrame(value, "message", ["version", "frame_id", "type", "message", "message_incarnation_id", "attempt_number", "delivery_expires_at", "delivery_items", "delivery_messages"])) return false;
  const parentMessage = value.message;
  if (!isEnvelope(parentMessage) || !isMessageIncarnationId(value.message_incarnation_id) || !isSafeInteger(value.attempt_number, 1) || !isUtcMillis(value.delivery_expires_at)) return false;
  const deliveryItems = value.delivery_items;
  const deliveryMessages = value.delivery_messages;
  if (deliveryItems === undefined) return deliveryMessages === undefined;
  if (!isDeliveryItems(deliveryItems)) return false;
  const firstItem = deliveryItems[0];
  if (firstItem === undefined || parentMessage.id !== firstItem.message_id || value.message_incarnation_id !== firstItem.message_incarnation_id || value.attempt_number !== firstItem.attempt_number) return false;
  if (deliveryItems.length < 2) return deliveryMessages === undefined;
  if (!Array.isArray(deliveryMessages) || deliveryMessages.length !== deliveryItems.length || deliveryMessages.length < 2 || !deliveryMessages.every(isEnvelope)) return false;
  return deliveryMessages.every((nestedMessage, index) => {
    const item = deliveryItems[index];
    return item !== undefined && nestedMessage.id === item.message_id && nestedMessage.to === parentMessage.to;
  });
}

export const isMessageFramePublic = isMessageFrame;
export function isConversationMessageFrame(value: unknown): value is ConversationMessageFrame { return isFrame(value, "conversation_message", ["version", "frame_id", "type", "message"]) && isConversationMessage(value.message); }
export function isRemindersChangedFrame(value: unknown): value is RemindersChangedFrame { return isFrame(value, "reminders_changed", ["version", "frame_id", "type"]); }
export function isAgentGoalStateFrame(value: unknown): value is AgentGoalStateFrame { return isFrame(value, "agent_goal_state", ["version", "frame_id", "type", "agent_id", "instance_id", "goal_state", "generated_at"]) && isAgentId(value.agent_id) && isFrameId(value.instance_id) && isGoalState(value.goal_state) && isUtcMillis(value.generated_at); }
export function isAgentActivityFrame(value: unknown): value is AgentActivityFrame { return isFrame(value, "agent_activity", ["version", "frame_id", "type", "agent_id", "instance_id", "activity_state", "subagent_count", "goal", "subagents", "current_model", "generated_at"]) && isAgentId(value.agent_id) && (value.instance_id === undefined || isFrameId(value.instance_id)) && isObservableActivityState(value.activity_state) && (value.subagent_count === undefined || isSafeInteger(value.subagent_count, 0, 999)) && (value.goal === undefined || isGoalProjection(value.goal)) && (value.subagents === undefined || isSubagentSummaries(value.subagents)) && (value.current_model === undefined || isModelSummary(value.current_model)) && isUtcMillis(value.generated_at); }
export function isAgentModelCatalogFrame(value: unknown): value is AgentModelCatalogFrame { return isFrame(value, "agent_model_catalog", ["version", "frame_id", "type", "agent_id", "instance_id", "model_summaries", "total_count", "truncated", "current_model", "generated_at"]) && isAgentId(value.agent_id) && isFrameId(value.instance_id) && isModelSummaries(value.model_summaries) && (value.total_count === undefined || isSafeInteger(value.total_count, 0, 1_000_000)) && (value.truncated === undefined || typeof value.truncated === "boolean") && (value.current_model === undefined || isModelSummary(value.current_model)) && isUtcMillis(value.generated_at); }
export function isDeliveryAcknowledgedFrame(value: unknown): value is DeliveryAcknowledgedFrame { return isFrame(value, "delivery_acknowledged", ["version", "frame_id", "type", "request_id", "agent_id", "instance_id", "lease_id", "message_id", "message_incarnation_id", "attempt_number"]) && isFrameId(value.request_id) && isLeaseFields(value) && isMessageId(value.message_id) && isMessageIncarnationId(value.message_incarnation_id) && isSafeInteger(value.attempt_number, 1); }
function isLifecycleDisposition(value: unknown): value is SubagentLifecycleAcknowledgedFrame["disposition"] { return value === "applied" || value === "accepted_noop" || value === "duplicate" || value === "stale" || value === "conflict"; }
export function isSubagentLifecycleAcknowledgedFrame(value: unknown): value is SubagentLifecycleAcknowledgedFrame { return isFrame(value, "subagent_lifecycle_acknowledged", ["version", "frame_id", "type", "request_id", "event_id", "client_sequence", "producer_epoch", "agent_id", "instance_id", "parent_session_id", "lease_id", "disposition", "router_version", "applied_version"]) && isFrameId(value.request_id) && isFrameId(value.event_id) && isSafeInteger(value.client_sequence, 1) && isPatternString(value.producer_epoch, lifecycleEpochPattern, 36, 36) && isAgentId(value.agent_id) && isFrameId(value.instance_id) && isFrameId(value.parent_session_id) && isFrameId(value.lease_id) && isLifecycleDisposition(value.disposition) && isSafeInteger(value.router_version, 0) && (value.applied_version === undefined || isSafeInteger(value.applied_version, 0)); }

const clientFrameGuards: Array<(value: unknown) => value is ClientFrame> = [isRegisterFrame, isLeaseRenewFrame, isActivityFrame, isModelCatalogFrame, isInlineToolStateFrame, isInlineToolControlFrame, isInlineToolControlResultFrame, isModelControlFrame, isModelControlResultFrame, isGoalControlFrame, isGoalControlResultFrame, isReminderLockControlFrame, isReminderLockControlResultFrame, isGoalStateFrame, isMessageArrivalClientFrame, isInjectionRequestedFrame, isAckFrame, isNackFrame, isPingFrame, isSubagentSyncEnableFrame, isSubagentLifecycleSnapshotFrame, isSubagentLifecycleEventFrame, isSubagentStateResyncFrame];
const serverFrameGuards: Array<(value: unknown) => value is ServerFrame> = [isRegisteredFrame, isLeaseRejectedFrame, isErrorFrame, isPongFrame, isMessageFrame, isConversationMessageFrame, isRemindersChangedFrame, isInlineToolStateFrame, isInlineToolControlForwardFrame, isInlineToolControlResultFrame, isModelControlForwardFrame, isModelControlResultFrame, isGoalControlForwardFrame, isGoalControlResultFrame, isReminderLockControlForwardFrame, isReminderLockControlResultFrame, isAgentGoalStateFrame, isDeliveryAcknowledgedFrame, isAgentActivityFrame, isAgentModelCatalogFrame, isMessageArrivalServerFrame, isSubagentStateSnapshotFrame, isSubagentStateUpdateFrame, isSubagentStateReplayFrame, isSubagentLifecycleAcknowledgedFrame];

export function isClientFrame(value: unknown): value is ClientFrame { return clientFrameGuards.some((guard) => guard(value)); }
export function isServerFrame(value: unknown): value is ServerFrame { return serverFrameGuards.some((guard) => guard(value)); }
export function isMailboxFrame(value: unknown): value is MailboxFrame { return isClientFrame(value) || isServerFrame(value); }

export function parseServerFrame(value: unknown): ParseResult<ServerFrame> {
  if (isServerFrame(value)) return { ok: true, value };
  return { ok: false, error: invalid("frame", "value does not match a Mailbox V2 server frame") };
}

export function parseClientFrame(value: unknown): ParseResult<ClientFrame> {
  if (isClientFrame(value)) return { ok: true, value };
  return { ok: false, error: invalid("frame", "value does not match a Mailbox V2 client frame") };
}

export function parseMailboxFrame(value: unknown): ParseResult<MailboxFrame> {
  if (isMailboxFrame(value)) return { ok: true, value };
  return { ok: false, error: invalid("frame", "value does not match a Mailbox V2 frame") };
}

export function parseEnvelope(value: unknown): ParseResult<Envelope> { return isEnvelope(value) ? { ok: true, value } : { ok: false, error: invalid("envelope", "invalid envelope") }; }
export function parseDeliveryItem(value: unknown): ParseResult<DeliveryItem> { return isDeliveryItem(value) ? { ok: true, value } : { ok: false, error: invalid("delivery_item", "invalid delivery item") }; }
export function parseAttachmentReference(value: unknown): ParseResult<AttachmentReference> { return isAttachmentReference(value) ? { ok: true, value } : { ok: false, error: invalid("attachment", "invalid attachment reference") }; }
export function isDeliveryReceiptMode(value: unknown): value is DeliveryReceiptMode { return value === "delivery_items_v1"; }
