import { normalizeRouterUrl, activeRouterUrl } from "../persistence/settings-repository";
import { secureUuid } from "../platform/secure-uuid";
import { httpRoutes } from "../protocol/manifest";
import { isAgentId, isConversationMessage, isManagedAgent, isManagedChildrenProjection, isManagedCompleteHistoryEntry, isManagedHistoryEntry, isModelSummary, isRecord, isRouterUtcTimestamp } from "../protocol/guards";
import { normalizeGoalState, type GoalControlSettings, type GoalState } from "../domain/goals";
import type { Agent } from "../state/agent-projection";
import type { HistoryMessage } from "../domain/history";
import type { Reminder } from "../domain/reminders";
import type { PromptTemplate } from "../domain/prompt-templates";
import type { Workflow, WorkflowState, WorkflowStatePosition, WorkflowStateResponse, WorkflowStatesResponse, WorkflowToolsResponse, WorkflowWithStates, CreateWorkflowRequest, UpdateWorkflowRequest, WorkflowExportBundle } from "../domain/workflow-states";
import type { ManagedAgent, ManagedAgentLaunchPayload, ManagedChildrenProjection, ManagedHistoryEntry, ManagedProvider, WorkflowSchedule } from "../protocol/types";
import { resolveAttachmentUrl, withAbsoluteAttachmentUrls } from "./attachment-urls";

export interface Capabilities extends Record<string, unknown> {}
export interface HealthResponse { status: string; version: string; schema_migrations?: string[] }
export interface AgentListResponse { agents: Agent[]; generated_at?: string; activity_snapshot_version?: number }
export interface ConversationResponse { owner: string; peer: string; high_water_sequence: number; messages: HistoryMessage[]; next_before?: number | null }
export interface ConversationTimelineEvent {
  id: string;
  agent_id: string;
  seq: number;
  kind: string;
  role?: string;
  content?: string;
  metadata?: Record<string, unknown> | null;
  created_at: number;
}
export interface ConversationTimelineResponse {
  agent_id: string;
  events: ConversationTimelineEvent[];
  next_before_seq: number | null;
}
export interface ReminderResponse { reminders: Reminder[] }
export interface ReminderWriteResponse { reminder: Reminder }
export interface PromptTemplateResponse { prompt_templates: PromptTemplate[] }
export interface ManagedAgentsResponse { generated_at: string; projection_version: number; agents: ManagedAgent[] }
export interface ManagedAgentResponse { generated_at: string; projection_version: number; agent: ManagedAgent }
export interface ManagedOperationResponse { operation_id: string; agent?: ManagedAgent; agents?: Array<{ agent_id: string; operation_id: string; status: string }> }
export interface ManagedDirectoriesResponse { path: string; parent: string; breadcrumbs?: Array<{ name: string; path: string }>; directories: Array<{ name: string; path: string }> }
export interface ManagedChildrenResponse extends ManagedChildrenProjection {}
export interface ManagedHistoryResponse { view: "quiet" | "complete"; entries: ManagedHistoryEntry[]; next_before: number | null }
export interface ScratchpadEntry {
  id: string;
  agent_id: string;
  content: string;
  pinned: boolean;
  created_at: string;
  updated_at: string;
}
export interface ScratchpadResponse {
  entries: ScratchpadEntry[];
  total_count: number;
}
export interface ProgressHistoryEntry {
  percent: number;
  note: string | null;
  timestamp: string;
}
export interface AgentMonitor {
  id: string;
  agent_id: string;
  workflow_state_id?: string;
  name: string;
  description: string;
  enabled: boolean;
  interval_seconds: number;
  next_eval_at: string;
  last_evaluated_at?: string;
  cooldown_seconds: number;
  last_fired_at?: string;
  consecutive_alerts: number;
  max_alerts: number;
  trailing_messages_count: number;
  question_type: 'noul' | 'choice' | 'score';
  question_instructions: string;
  question_criteria_json: string;
  question_options_json: string;
  condition_operator: string;
  condition_threshold: string;
  nudge_template: string;
  fallback_mode: string;
  created_at: string;
  updated_at: string;
}

export interface CreateAgentMonitorPayload {
  name: string;
  description?: string;
  enabled?: boolean;
  interval_seconds?: number;
  cooldown_seconds?: number;
  max_alerts?: number;
  trailing_messages_count?: number;
  question_type: 'noul' | 'choice' | 'score';
  question_instructions: string;
  question_criteria_json?: string;
  question_options_json?: string;
  condition_operator: string;
  condition_threshold: string;
  nudge_template: string;
  fallback_mode?: string;
}

export interface MonitorEvaluationResult {
  monitor_id: string;
  agent_id: string;
  evaluated_at: string;
  matched: boolean;
  skipped: boolean;
  skip_reason?: string;
  question_type: string;
  operator: string;
  threshold: string;
  actual_value?: any;
  nudge?: string;
  alert_fired: boolean;
  next_eval_at: string;
  error?: string;
}
export interface ManagedGoalResponse { goal_state: GoalState }
export interface ManagedInlineToolsResponse { enabled: boolean }
export interface ProviderModel { provider: ManagedProvider; id: string; name: string; available: boolean }
export interface ProviderStatus { provider: ManagedProvider; label: string; authenticated: boolean; status: "authenticated" | "signed_out"; account_label?: string; updated_at: string; error?: string }
export interface ProviderAuthResponse { auth_revision: number; providers: ProviderStatus[] }
export interface ModelsResponse { auth_revision: number; models: ProviderModel[]; default_model: { provider: string; id: string; name?: string } | null }
export interface RouterLogQuery { query: string; from: string | null; to: string | null; cursor: string | null }
export interface RouterLogLine { bytes_base64: string }
export type RouterLogStatus = "complete" | "partial" | "source_unavailable";
export type RouterLogDiagnosticCategory = "source_unavailable" | "generation_unreadable" | "generation_disappeared" | "snapshot_changed" | "tail_unterminated" | "cursor_invalidated";
export interface RouterLogDiagnostic { category: RouterLogDiagnosticCategory; count: number }
export interface RouterLogResponse { status: RouterLogStatus; lines: RouterLogLine[]; next_cursor: string | null; diagnostics: RouterLogDiagnostic[] }
export interface DefaultModelResponse { operation_id: string; default_model: { provider: string; id: string; name?: string } | null; effective_fallback: ProviderModel }
export interface SystemOneConfig { host: string; port: number; enabled: boolean }
export interface ModelContextThreshold { warning_percent: number; reset_percent: number }
export interface ContextThresholdConfig {
  warning_percent: number;
  reset_percent: number;
  model_thresholds: Record<string, ModelContextThreshold>;
}
export interface SystemOneTestRequest { model?: string | undefined; state: unknown; questions: Record<string, unknown> }
export interface SystemOneTestResponse {
  status: "ok" | "error";
  response?: {
    model: string;
    answers?: Record<string, unknown> | undefined;
    usage?: { input_tokens?: number | undefined; output_tokens?: number | undefined } | undefined;
  } | undefined;
  error?: string | undefined;
}
export type FetchLike = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

// Managed routes expose only stable, non-secret troubleshooting facts. Unknown
// fields are intentionally discarded so a malformed router response cannot add
// arbitrary text to a user-visible error.
export interface RouterErrorDiagnostic {
  operation?: string;
  agentId?: string;
  runId?: string;
  requestId?: string;
  state?: string;
  fence?: string;
  cause?: string;
}

export class HttpRequestError extends Error {
  readonly status: number;
  readonly code: string;
  readonly correlationId?: string;
  readonly diagnostic?: RouterErrorDiagnostic;
  readonly body: unknown;
  constructor(status: number, code: string, message: string, body: unknown, correlationId?: string, diagnostic?: RouterErrorDiagnostic) {
    super(message);
    this.name = "HttpRequestError";
    this.status = status;
    this.code = code;
    this.body = body;
    if (correlationId !== undefined && correlationId !== "") this.correlationId = correlationId;
    if (diagnostic !== undefined) this.diagnostic = diagnostic;
  }
}

function recordValue(value: unknown): Record<string, unknown> | undefined { return isRecord(value) ? value : undefined; }
function stringValue(value: unknown): string | undefined { return typeof value === "string" ? value : undefined; }
function nonEmptyString(value: unknown): value is string { return typeof value === "string" && value.length > 0 && !value.includes("\u0000"); }
function safeInteger(value: unknown, minimum = 0): value is number { return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum; }
function timestamp(value: unknown): value is string { return typeof value === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value) && Number.isFinite(Date.parse(value)); }
function invalidResponse(message: string, value: unknown): HttpRequestError { return new HttpRequestError(200, "invalid_response", message, value); }
function validOptionalString(item: Record<string, unknown>, key: string): boolean { return !Object.hasOwn(item, key) || typeof item[key] === "string"; }
function validOptionalTimestamp(item: Record<string, unknown>, key: string): boolean { return !Object.hasOwn(item, key) || timestamp(item[key]); }
function validAttachmentList(value: unknown): boolean {
  return Array.isArray(value) && value.every((candidate) => {
    const attachment = recordValue(candidate);
    return !!attachment && nonEmptyString(attachment.url) && validOptionalString(attachment, "id") && (!Object.hasOwn(attachment, "mime_type") || ["image/png", "image/jpeg", "image/webp", "image/gif"].includes(String(attachment.mime_type))) && (!Object.hasOwn(attachment, "size_bytes") || safeInteger(attachment.size_bytes, 0)) && validOptionalString(attachment, "name");
  });
}
function validSource(value: unknown): boolean {
  const source = recordValue(value);
  return !!source && Object.keys(source).length === 1 && ["agent", "web_ui", "android", "discord", "reminder", "system", "unknown"].includes(String(source.kind));
}
function validMessage(value: unknown, complete: boolean): value is Record<string, unknown> {
  const item = recordValue(value); if (!item || !nonEmptyString(item.id)) return false;
  if (complete && (!isConversationMessage(item) || (item.source !== undefined && !validSource(item.source)))) return false;
  if (!validOptionalString(item, "from") || (Object.hasOwn(item, "to") && !isAgentId(item.to)) || !validOptionalString(item, "body") || !validOptionalTimestamp(item, "created_at") || !validOptionalTimestamp(item, "recorded_at") || (Object.hasOwn(item, "sequence") && !safeInteger(item.sequence, 1)) || (Object.hasOwn(item, "message_incarnation_id") && !nonEmptyString(item.message_incarnation_id)) || (Object.hasOwn(item, "subject") && item.subject !== null && typeof item.subject !== "string") || (Object.hasOwn(item, "source") && !validSource(item.source))) return false;
  if (Object.hasOwn(item, "attachments") && !validAttachmentList(item.attachments)) return false;
  return true;
}
function validStoredMessage(value: unknown): value is Record<string, unknown> {
  const item = recordValue(value);
  return !!item && validMessage(item, false) && safeInteger(item.enqueue_sequence) && timestamp(item.expires_at) && nonEmptyString(item.message_incarnation_id) && nonEmptyString(item.state) && validSource(item.source);
}
function validAgent(value: unknown, includeModels = true): value is Agent {
  const item = recordValue(value);
  if (item && Object.hasOwn(item, "urgent") && typeof item.urgent !== "boolean") return false;
  if (!item || !isAgentId(item.agent_id) || !nonEmptyString(item.instance_id) || !["agent", "user_interaction"].includes(String(item.agent_role)) || !timestamp(item.connected_at) || !timestamp(item.lease_expires_at) || !["active", "idle", "compacting"].includes(String(item.activity_state)) || !safeInteger(item.subagent_count) || !Array.isArray(item.subagents)) return false;
  if (!item.subagents.every((candidate) => { const child = recordValue(candidate); return !!child && nonEmptyString(child.id) && nonEmptyString(child.description) && validOptionalString(child, "type") && (!Object.hasOwn(child, "status") || ["queued", "running", "completed", "steered", "aborted", "stopped", "error"].includes(String(child.status))); })) return false;
  if (includeModels && (!Array.isArray(item.available_models) || !item.available_models.every(isModelSummary) || !safeInteger(item.model_catalog_total_count) || typeof item.model_catalog_truncated !== "boolean")) return false;
  if (Object.hasOwn(item, "subagent_version") && !safeInteger(item.subagent_version)) return false;
  if (Object.hasOwn(item, "current_model") && !isModelSummary(item.current_model)) return false;
  if (Object.hasOwn(item, "goal") && normalizeGoalState(item.goal) === undefined) {
    const goal = recordValue(item.goal);
    if (!goal || typeof goal.enabled !== "boolean" || typeof goal.goal !== "string" || goal.goal.length > 4000 || (goal.enabled ? goal.goal.length === 0 : goal.goal !== "")) return false;
  }
  if (Object.hasOwn(item, "goal_state") && normalizeGoalState(item.goal_state) === undefined) return false;
  if (Object.hasOwn(item, "inline_tool_control_available") && typeof item.inline_tool_control_available !== "boolean") return false;
  if (Object.hasOwn(item, "inline_tool_enabled") && typeof item.inline_tool_enabled !== "boolean") return false;
  return true;
}
function validReminder(value: unknown): value is Reminder {
  const item = recordValue(value);
  if (!item || !nonEmptyString(item.id) || !isAgentId(item.target) || !isAgentId(item.creator) || !nonEmptyString(item.body) || !timestamp(item.created_at) || !timestamp(item.next_due_at) || (item.interval_ms !== null && !safeInteger(item.interval_ms, 1)) || !["scheduled", "due", "message_queued", "queued", "leased", "injection_requested", "acknowledged", "failed", "dead_letter"].includes(String(item.state)) || !safeInteger(item.occurrence_number) || !safeInteger(item.attempt_count)) return false;
  if ((Object.hasOwn(item, "subject") && item.subject !== null && typeof item.subject !== "string") || (Object.hasOwn(item, "linked_message_id") && item.linked_message_id !== null && !nonEmptyString(item.linked_message_id)) || (Object.hasOwn(item, "linked_message_state") && item.linked_message_state !== null && !nonEmptyString(item.linked_message_state)) || (Object.hasOwn(item, "last_attempt_at") && item.last_attempt_at !== null && !timestamp(item.last_attempt_at))) return false;
  return true;
}
function validTemplate(value: unknown): value is PromptTemplate {
  const item = recordValue(value);
  return !!item && nonEmptyString(item.id) && nonEmptyString(item.name) && typeof item.body === "string" && item.body.trim().length > 0 && timestamp(item.created_at) && timestamp(item.updated_at);
}
function reminderWrite(value: unknown): ReminderWriteResponse {
  const root = recordValue(value); if (!root || !validReminder(root.reminder)) throw invalidResponse("Router returned an invalid reminder response", value); return { reminder: root.reminder };
}
function templateWrite(value: unknown): Record<string, unknown> {
  const root = recordValue(value); if (!root || !validTemplate(root.prompt_template)) throw invalidResponse("Router returned an invalid prompt-template response", value); return root;
}
function messageWrite(value: unknown): Record<string, unknown> {
  const root = recordValue(value); if (!root || !validStoredMessage(root.message)) throw invalidResponse("Router returned an invalid message response", value);
  if (Object.hasOwn(root, "conversation_message") && !validMessage(root.conversation_message, true)) throw invalidResponse("Router returned an invalid conversation message response", value);
  return root;
}
export function resolveHttpEndpoint(routerUrl: string, path: string): URL {
  const base = new URL(normalizeRouterUrl(routerUrl));
  if (typeof path !== "string" || !path.startsWith("/") || path.startsWith("//")) throw new Error("HTTP endpoint must be a same-origin path");
  let endpoint: URL;
  try { endpoint = new URL(path, `${base.toString()}/`); } catch { throw new Error("HTTP endpoint is invalid"); }
  if (endpoint.origin !== base.origin || (endpoint.pathname !== "/" && !endpoint.pathname.startsWith("/v1/"))) throw new Error("HTTP endpoint must be a same-origin router path");
  return endpoint;
}

function routerErrorDiagnostic(value: unknown, requestId: unknown): RouterErrorDiagnostic | undefined {
  const details = recordValue(value);
  const result: RouterErrorDiagnostic = {};
  const copy = (key: keyof RouterErrorDiagnostic, source: unknown, maximum = 128): void => { if (nonEmptyString(source) && source.length <= maximum) result[key] = source; };
  copy("operation", details?.operation);
  if (isAgentId(details?.agent_id)) result.agentId = details.agent_id;
  copy("runId", details?.run_id);
  copy("requestId", details?.request_id);
  if (result.requestId === undefined) copy("requestId", requestId);
  copy("state", details?.state, 64);
  copy("fence", details?.fence, 64);
  copy("cause", details?.cause, 256);
  return Object.keys(result).length === 0 ? undefined : result;
}
function responseError(body: unknown, status: number, correlationId: string | null): HttpRequestError {
  const root = recordValue(body); const detail = recordValue(root?.error);
  const code = stringValue(detail?.code) ?? stringValue(root?.code) ?? `http_${status}`;
  const message = stringValue(detail?.message) ?? stringValue(root?.message) ?? `Router request failed (${status})`;
  return new HttpRequestError(status, code, message, body, correlationId ?? undefined, routerErrorDiagnostic(detail?.details, root?.request_id));
}

export function managedRequestErrorMessage(cause: unknown, fallback: string): string {
  if (!(cause instanceof HttpRequestError)) return cause instanceof Error ? cause.message : fallback;
  const facts: string[] = [`Code: ${cause.code}`];
  const diagnostic = cause.diagnostic;
  if (diagnostic?.operation) facts.push(`Operation: ${diagnostic.operation}`);
  if (diagnostic?.agentId) facts.push(`Agent: ${diagnostic.agentId}`);
  if (diagnostic?.runId) facts.push(`Run: ${diagnostic.runId}`);
  if (diagnostic?.requestId ?? cause.correlationId) facts.push(`Request: ${diagnostic?.requestId ?? cause.correlationId}`);
  if (diagnostic?.state) facts.push(`State: ${diagnostic.state}`);
  if (diagnostic?.fence) facts.push(`Fence: ${diagnostic.fence}`);
  if (diagnostic?.cause) facts.push(`Cause: ${diagnostic.cause}`);
  return `${cause.message} (${facts.join(" · ")})`;
}
async function jsonBody(response: Response, maxBytes?: number): Promise<unknown> {
  const bytes = new Uint8Array(await response.arrayBuffer());
  if (maxBytes !== undefined && bytes.length > maxBytes) throw new HttpRequestError(response.status, "invalid_response", "Router returned an oversized response", bytes.byteLength, response.headers.get("x-correlation-id") ?? undefined);
  let text: string;
  try { text = new TextDecoder("utf-8", { fatal: true }).decode(bytes); } catch { throw new HttpRequestError(response.status, "invalid_response", "Router returned invalid UTF-8", bytes.byteLength, response.headers.get("x-correlation-id") ?? undefined); }
  if (text === "") return {};
  try { return JSON.parse(text); } catch { throw new HttpRequestError(response.status, "invalid_response", "Router returned invalid JSON", text, response.headers.get("x-correlation-id") ?? undefined); }
}
function agentList(value: unknown, includeModels = true): AgentListResponse {
  const root = recordValue(value); const rawAgents = root?.agents;
  if (!root || !Array.isArray(rawAgents) || !rawAgents.every((agent) => validAgent(agent, includeModels))) throw invalidResponse("Router returned an invalid agent list", value);
  const generated = root.generated_at; const version = root.activity_snapshot_version;
  if (generated !== undefined && !timestamp(generated)) throw invalidResponse("Router returned an invalid agent-list timestamp", value);
  if (version !== undefined && !safeInteger(version)) throw invalidResponse("Router returned an invalid activity snapshot version", value);
  return { agents: rawAgents, ...(generated === undefined ? {} : { generated_at: generated }), ...(version === undefined ? {} : { activity_snapshot_version: version }) };
}
function conversation(value: unknown, routerUrl: string): ConversationResponse {
  const root = recordValue(value);
  if (!root || !nonEmptyString(root.owner) || !nonEmptyString(root.peer) || !safeInteger(root.high_water_sequence) || (root.messages !== null && !Array.isArray(root.messages))) throw invalidResponse("Router returned an invalid conversation", value);
  const messages: HistoryMessage[] = [];
  for (const candidate of root.messages ?? []) {
    if (!isConversationMessage(candidate)) throw invalidResponse("Router returned an invalid conversation message", value);
    const from = candidate.from ?? candidate.sender;
    const to = candidate.to ?? candidate.recipient;
    messages.push(withAbsoluteAttachmentUrls({ ...candidate, id: candidate.id, ...(from !== undefined ? { from } : {}), ...(to !== undefined ? { to } : {}) }, routerUrl));
  }
  const nextBefore = typeof root.next_before === "number" && Number.isSafeInteger(root.next_before) && root.next_before > 0 ? root.next_before : null;
  return { owner: root.owner, peer: root.peer, high_water_sequence: root.high_water_sequence, messages, next_before: nextBefore };
}
function conversationTimeline(value: unknown): ConversationTimelineResponse {
  const root = recordValue(value);
  if (!root || !nonEmptyString(root.agent_id) || (root.events !== null && !Array.isArray(root.events))) {
    throw invalidResponse("Router returned an invalid conversation timeline", value);
  }
  const events: ConversationTimelineEvent[] = [];
  for (const raw of root.events ?? []) {
    const item = recordValue(raw);
    if (!item || !nonEmptyString(item.id) || !nonEmptyString(item.agent_id) || typeof item.seq !== "number" || !nonEmptyString(item.kind) || typeof item.created_at !== "number") {
      throw invalidResponse("Router returned an invalid conversation timeline event", raw);
    }
    const metaRecord = recordValue(item.metadata);
    events.push({
      id: item.id,
      agent_id: item.agent_id,
      seq: item.seq,
      kind: item.kind,
      ...(typeof item.role === "string" ? { role: item.role } : {}),
      ...(typeof item.content === "string" ? { content: item.content } : {}),
      ...(metaRecord !== undefined ? { metadata: metaRecord } : {}),
      created_at: item.created_at,
    });
  }
  const nextBeforeSeq = typeof root.next_before_seq === "number" && Number.isSafeInteger(root.next_before_seq) && root.next_before_seq > 0
    ? root.next_before_seq
    : null;
  return {
    agent_id: root.agent_id,
    events,
    next_before_seq: nextBeforeSeq,
  };
}
function reminders(value: unknown): ReminderResponse {
  const root = recordValue(value); const rawReminders = root?.reminders;
  // Go's JSON encoder emits a nil empty slice as null. Normalize that valid wire response at the boundary.
  if (!root || (rawReminders !== null && !Array.isArray(rawReminders)) || (Array.isArray(rawReminders) && !rawReminders.every(validReminder))) throw invalidResponse("Router returned an invalid reminder list", value);
  return { reminders: rawReminders ?? [] };
}
function templates(value: unknown): PromptTemplateResponse {
  const root = recordValue(value); const rawTemplates = root?.prompt_templates;
  if (!root || !Array.isArray(rawTemplates) || !rawTemplates.every(validTemplate)) throw invalidResponse("Router returned an invalid prompt-template list", value);
  return { prompt_templates: rawTemplates };
}
function managedAgents(value: unknown): ManagedAgentsResponse {
  const root = recordValue(value);
  const rawAgents = root?.agents;
  // Archived records are not agents the current UI can operate. Ignore them at
  // the boundary so a legacy archive entry cannot invalidate the live snapshot.
  const agents = Array.isArray(rawAgents)
    ? rawAgents.filter((candidate) => recordValue(candidate)?.record_kind !== "archived")
    : undefined;
  if (!root || !isRouterUtcTimestamp(root.generated_at) || !safeInteger(root.projection_version) || !agents || !agents.every(isManagedAgent)) throw invalidResponse("Router returned an invalid managed-agent list", value);
  return { generated_at: root.generated_at, projection_version: root.projection_version, agents };
}
function managedAgent(value: unknown): ManagedAgentResponse {
  const root = recordValue(value);
  if (!root || !isRouterUtcTimestamp(root.generated_at) || !safeInteger(root.projection_version) || !isManagedAgent(root.agent)) throw invalidResponse("Router returned an invalid managed-agent response", value);
  return { generated_at: root.generated_at, projection_version: root.projection_version, agent: root.agent };
}
function managedHistory(value: unknown): ManagedHistoryResponse {
  const root = recordValue(value); const rawEntries = root?.entries;
  if (!root || (root.view !== "quiet" && root.view !== "complete") || !Array.isArray(rawEntries) || (root.next_before !== null && root.next_before !== undefined && !safeInteger(root.next_before, 1))) throw invalidResponse("Router returned invalid managed history", value);
  const isMatch = root.view === "complete" ? isManagedCompleteHistoryEntry : isManagedHistoryEntry;
  const entries = rawEntries.filter(isMatch);
  const nextBefore = typeof root.next_before === "number" && Number.isSafeInteger(root.next_before) && root.next_before > 0 ? root.next_before : null;
  return { view: root.view, entries, next_before: nextBefore };
}
function managedChildren(value: unknown): ManagedChildrenResponse {
  if (!isManagedChildrenProjection(value)) throw invalidResponse("Router returned invalid managed children", value);
  return value;
}
function managedGoal(value: unknown): ManagedGoalResponse {
  const root = recordValue(value);
  const rawState = root && Object.hasOwn(root, "goal_state") ? root.goal_state : root;
  const state = normalizeGoalState(rawState);
  if (!root || state === undefined) throw invalidResponse("Router returned invalid managed goal state", value);
  return { goal_state: state };
}
function managedInlineTools(value: unknown): ManagedInlineToolsResponse {
  const root = recordValue(value);
  if (!root || typeof root.enabled !== "boolean") throw invalidResponse("Router returned invalid managed inline-tool state", value);
  return { enabled: root.enabled };
}
function managedDirectories(value: unknown): ManagedDirectoriesResponse {
  const root = recordValue(value);
  const breadcrumbItems = root?.breadcrumbs;
  if (!root || !nonEmptyString(root.path) || typeof root.parent !== "string" || (breadcrumbItems !== undefined && (!Array.isArray(breadcrumbItems) || !breadcrumbItems.every((candidate) => { const item = recordValue(candidate); return !!item && nonEmptyString(item.name) && nonEmptyString(item.path); }))) || !Array.isArray(root.directories) || !root.directories.every((candidate) => { const item = recordValue(candidate); return !!item && nonEmptyString(item.name) && nonEmptyString(item.path); })) throw invalidResponse("Router returned an invalid directory list", value);
  return { path: root.path, parent: root.parent, ...(breadcrumbItems === undefined ? {} : { breadcrumbs: breadcrumbItems.flatMap((candidate) => { const item = recordValue(candidate); return item && nonEmptyString(item.name) && nonEmptyString(item.path) ? [{ name: item.name, path: item.path }] : []; }) }), directories: root.directories.flatMap((candidate) => { const item = recordValue(candidate); return item && nonEmptyString(item.name) && nonEmptyString(item.path) ? [{ name: item.name, path: item.path }] : []; }) };
}
function providerName(value: unknown): value is ProviderModel["provider"] {
  return value === "openai-codex" || value === "google" || value === "cursor" || value === "google-vertex";
}
function providerModel(value: unknown): value is ProviderModel {
  const item = recordValue(value);
  const nameValid = typeof item?.name === "string" ? nonEmptyString(item.name) : true;
  return !!item && providerName(item.provider) && nonEmptyString(item.id) && nameValid;
}
function providerStatus(value: unknown): value is ProviderStatus {
  const item = recordValue(value);
  if (!item || !providerName(item.provider) || typeof item.authenticated !== "boolean" || (item.status !== "authenticated" && item.status !== "signed_out") || item.authenticated !== (item.status === "authenticated") || !isRouterUtcTimestamp(item.updated_at) || (Object.hasOwn(item, "account_label") && !nonEmptyString(item.account_label)) || (Object.hasOwn(item, "error") && !nonEmptyString(item.error))) return false;
  const labels: Record<ManagedProvider, readonly string[]> = {
    "openai-codex": ["ChatGPT"],
    google: ["Google"],
    cursor: ["Cursor"],
    "google-vertex": ["Google Vertex AI", "Vertex AI", "Google Cloud Vertex AI", "Vertex"],
  };
  return typeof item.label === "string" && (labels[item.provider]?.includes(item.label) ?? false);
}
function providerAuth(value: unknown): ProviderAuthResponse {
  const root = recordValue(value);
  if (!root || !safeInteger(root.auth_revision) || !Array.isArray(root.providers) || root.providers.length !== 4 || !root.providers.every(providerStatus) || new Set(root.providers.map((provider) => provider.provider)).size !== 4) throw invalidResponse("Router returned invalid provider state", value);
  return { auth_revision: root.auth_revision, providers: root.providers };
}
function savedModel(value: unknown): { provider: string; id: string; name?: string } | null | undefined {
  if (value === null) return null;
  const item = recordValue(value);
  if (!item || !nonEmptyString(item.provider) || !nonEmptyString(item.id)) return undefined;
  return { provider: item.provider, id: item.id, ...(typeof item.name === "string" ? { name: item.name } : {}) };
}
function models(value: unknown): ModelsResponse {
  const root = recordValue(value); const defaultModel = savedModel(root?.default_model);
  if (!root || !safeInteger(root.auth_revision) || !Array.isArray(root.models) || root.models.length > 4_096 || !root.models.every(providerModel) || defaultModel === undefined) throw invalidResponse("Router returned invalid model catalog", value);
  return { auth_revision: root.auth_revision, models: root.models, default_model: defaultModel };
}
function defaultModel(value: unknown): DefaultModelResponse {
  const root = recordValue(value); const saved = savedModel(root?.default_model);
  if (!root || !nonEmptyString(root.operation_id) || saved === undefined || !providerModel(root.effective_fallback)) throw invalidResponse("Router returned invalid default model", value);
  const fallback = root.effective_fallback;
  const effectiveFallback: ProviderModel = {
    ...fallback,
    name: typeof fallback.name === "string" && nonEmptyString(fallback.name) ? fallback.name : fallback.id,
    available: typeof fallback.available === "boolean" ? fallback.available : false,
  };
  return { operation_id: root.operation_id, default_model: saved, effective_fallback: effectiveFallback };
}
const routerLogDiagnosticCategories: readonly RouterLogDiagnosticCategory[] = ["source_unavailable", "generation_unreadable", "generation_disappeared", "snapshot_changed", "tail_unterminated", "cursor_invalidated"];
function isRouterLogDiagnosticCategory(value: unknown): value is RouterLogDiagnosticCategory { return value === "source_unavailable" || value === "generation_unreadable" || value === "generation_disappeared" || value === "snapshot_changed" || value === "tail_unterminated" || value === "cursor_invalidated"; }
function isRouterLogStatus(value: unknown): value is RouterLogStatus { return value === "complete" || value === "partial" || value === "source_unavailable"; }
function utf8Length(value: string): number { return new TextEncoder().encode(value).length; }
function strictUtcTimestamp(value: string): boolean {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?Z$/.exec(value);
  if (match === null) return false;
  const year = Number(match[1]); const month = Number(match[2]); const day = Number(match[3]);
  const hour = Number(match[4]); const minute = Number(match[5]); const second = Number(match[6]);
  if (hour > 23 || minute > 59 || second > 59) return false;
  const date = new Date(0); date.setUTCFullYear(year, month - 1, day); date.setUTCHours(hour, minute, second, 0);
  return date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day && date.getUTCHours() === hour && date.getUTCMinutes() === minute && date.getUTCSeconds() === second && Number.isFinite(Date.parse(value));
}
function routerLogQuery(value: unknown): value is RouterLogQuery {
  const item = recordValue(value);
  if (!item || Object.keys(item).length !== 4 || typeof item.query !== "string" || item.from !== null && typeof item.from !== "string" || item.to !== null && typeof item.to !== "string" || item.cursor !== null && typeof item.cursor !== "string") return false;
  if (utf8Length(item.query) > 128 || item.query.includes("\u0000") || (item.cursor !== null && (item.cursor.length === 0 || utf8Length(item.cursor) > 4096))) return false;
  for (const bound of [item.from, item.to]) if (bound !== null && (bound.length === 0 || utf8Length(bound) > 64 || !strictUtcTimestamp(bound))) return false;
  return item.from === null || item.to === null || Date.parse(item.from) < Date.parse(item.to);
}
function validBase64(value: unknown): value is string { return typeof value === "string" && value.length % 4 === 0 && /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value); }
function routerLogResponse(value: unknown): RouterLogResponse {
  const root = recordValue(value);
  if (!root || Object.keys(root).length !== 4 || Object.keys(root).some((key) => !["status", "lines", "next_cursor", "diagnostics"].includes(key)) || !isRouterLogStatus(root.status) || !Array.isArray(root.lines) || root.lines.length > 500 || (root.next_cursor !== null && (typeof root.next_cursor !== "string" || root.next_cursor.length === 0 || utf8Length(root.next_cursor) > 4096)) || !Array.isArray(root.diagnostics) || root.diagnostics.length > 8) throw invalidResponse("Router returned an invalid router-log response", value);
  const lines: RouterLogLine[] = [];
  for (const candidate of root.lines) { const line = recordValue(candidate); if (!line || Object.keys(line).length !== 1 || !validBase64(line.bytes_base64)) throw invalidResponse("Router returned an invalid router-log line", value); try { atob(line.bytes_base64); } catch { throw invalidResponse("Router returned an invalid router-log line", value); } lines.push({ bytes_base64: line.bytes_base64 }); }
  const diagnostics: RouterLogDiagnostic[] = [];
  const diagnosticNames = new Set<RouterLogDiagnosticCategory>();
  for (const candidate of root.diagnostics) { const diagnostic = recordValue(candidate); if (!diagnostic || Object.keys(diagnostic).length !== 2 || !isRouterLogDiagnosticCategory(diagnostic.category) || diagnosticNames.has(diagnostic.category) || !safeInteger(diagnostic.count) || diagnostic.count > 999) throw invalidResponse("Router returned invalid router-log diagnostics", value); diagnosticNames.add(diagnostic.category); diagnostics.push({ category: diagnostic.category, count: diagnostic.count }); }
  return { status: root.status, lines, next_cursor: root.next_cursor, diagnostics };
}
function managedOperation(value: unknown): ManagedOperationResponse {
  const root = recordValue(value);
  if (!root || !nonEmptyString(root.operation_id)) throw invalidResponse("Router returned an invalid managed operation", value);
  if (Object.hasOwn(root, "agent") && !isManagedAgent(root.agent)) throw invalidResponse("Router returned an invalid managed operation", value);
  if (Object.hasOwn(root, "agents") && (!Array.isArray(root.agents) || !root.agents.every((candidate) => { const item = recordValue(candidate); return !!item && nonEmptyString(item.agent_id) && nonEmptyString(item.operation_id) && nonEmptyString(item.status); }))) throw invalidResponse("Router returned an invalid managed operation", value);
  const result: ManagedOperationResponse = { operation_id: root.operation_id };
  if (isManagedAgent(root.agent)) result.agent = root.agent;
  if (Array.isArray(root.agents)) result.agents = root.agents.flatMap((candidate) => { const item = recordValue(candidate); return item && nonEmptyString(item.agent_id) && nonEmptyString(item.operation_id) && nonEmptyString(item.status) ? [{ agent_id: item.agent_id, operation_id: item.operation_id, status: item.status }] : []; });
  return result;
}

export interface MCPServer {
  id: string;
  name: string;
  url: string;
  auth_type: "none" | "bearer" | "oauth" | string;
  client_id?: string;
  token_endpoint?: string;
  auth_endpoint?: string;
  registration_endpoint?: string;
  revocation_endpoint?: string;
  scopes?: string;
  status: "connected" | "needs_auth" | "error" | "unconfigured" | string;
  last_error?: string;
  tools_count: number;
  last_sync_at?: string;
  created_at?: string;
  updated_at?: string;
}

export interface AddMCPServerRequest {
  url: string;
  name?: string;
  auth_type?: "none" | "bearer" | "oauth";
  auth_mode?: "none" | "bearer" | "oauth";
  client_id?: string;
  client_secret?: string;
  token_endpoint?: string;
  auth_endpoint?: string;
  registration_endpoint?: string;
  revocation_endpoint?: string;
  scopes?: string;
}

export interface MCPServerDetailResponse {
  server: MCPServer;
  tools?: unknown[];
  token?: unknown;
}

export interface ToolDefinitionItem {
  tool_name: string;
  category: string;
  enabled: boolean;
  mcp_server_id?: string | null;
}

export interface AgentToolItem extends ToolDefinitionItem {
  effective_enabled: boolean;
}

export interface AgentToolsResponse {
  agent_id: string;
  workflow_override: boolean;
  active_workflow_state: string;
  tools: AgentToolItem[];
}

export interface GitSyncTarget {
  target: string;
  repo_url: string;
  updated_at?: string;
}

export interface GitSyncPullResponse {
  exists?: boolean;
  message?: string;
  success?: boolean;
  error?: string;
  count?: number;
  templates?: PromptTemplate[];
}

export interface GitSyncPushResponse {
  success?: boolean;
  error?: string;
  count?: number;
}

export interface RouterHttpClient {
  readonly routerUrl: string;
  request(path: string, init?: RequestInit): Promise<unknown>;
  health(): Promise<HealthResponse>;
  capabilities(): Promise<Capabilities>;
  agents(options?: { includeModels?: boolean } | boolean): Promise<AgentListResponse>;
  conversations(owner: string, peer: string, options?: { before?: number; limit?: number }): Promise<ConversationResponse>;
  fetchConversationTimeline(agentId: string, limit?: number, beforeSeq?: number): Promise<ConversationTimelineResponse>;
  sendMessage(message: unknown): Promise<Record<string, unknown>>;
  message(id: string): Promise<unknown>;
  retryMessage(id: string): Promise<unknown>;
  deleteMessage(id: string): Promise<unknown>;
  memoryPolicy(): Promise<unknown>;
  memory(path: "rebuild" | "lookup" | "expand" | "apply" | "touch", payload?: unknown): Promise<unknown>;
  tmux(path?: string, method?: "GET" | "POST" | "PATCH" | "DELETE", payload?: unknown): Promise<unknown>;
  reminders(): Promise<ReminderResponse>;
  createReminder(payload: unknown): Promise<ReminderWriteResponse>;
  updateReminder(id: string, payload: unknown): Promise<ReminderWriteResponse>;
  deleteReminder(id: string): Promise<unknown>;
  promptTemplates(): Promise<PromptTemplateResponse>;
  createPromptTemplate(payload: unknown): Promise<unknown>;
  updatePromptTemplate(id: string, payload: unknown): Promise<unknown>;
  deletePromptTemplate(id: string): Promise<unknown>;
  workflowStates(): Promise<WorkflowStatesResponse>;
  workflowTools(): Promise<WorkflowToolsResponse>;
  getWorkflowState(id: string): Promise<WorkflowStateResponse>;
  createWorkflowState(payload: unknown): Promise<WorkflowStateResponse>;
  updateWorkflowState(id: string, payload: unknown): Promise<WorkflowStateResponse>;
  deleteWorkflowState(id: string): Promise<unknown>;
  listWorkflows(): Promise<Workflow[]>;
  getWorkflow(id: string): Promise<WorkflowWithStates>;
  createWorkflow(req: CreateWorkflowRequest): Promise<Workflow>;
  updateWorkflow(id: string, req: UpdateWorkflowRequest): Promise<Workflow>;
  deleteWorkflow(id: string): Promise<void>;
  duplicateWorkflow(id: string, req?: { name?: string }): Promise<Workflow>;
  exportWorkflow(id: string): Promise<WorkflowExportBundle>;
  importWorkflow(bundle: WorkflowExportBundle): Promise<Workflow>;
  updateWorkflowLayout(id: string, positions: WorkflowStatePosition[]): Promise<void>;
  runWorkflow(workflowId: string, options?: { variables?: Record<string, string>; cwd?: string; model?: { provider: string; id: string } }): Promise<ManagedAgent>;
  listWorkflowSchedules(workflowId?: string): Promise<WorkflowSchedule[]>;
  createWorkflowSchedule(req: Partial<WorkflowSchedule>): Promise<WorkflowSchedule>;
  updateWorkflowSchedule(id: string, req: Partial<WorkflowSchedule>): Promise<WorkflowSchedule>;
  deleteWorkflowSchedule(id: string): Promise<void>;
  setManagedAgentWorkflowState(agentId: string, workflowStateId: string | null, options?: { workflow_id?: string; variables?: Record<string, string> }): Promise<unknown>;
  setManagedAgentWorkflow(agentId: string, workflowId: string, variables?: Record<string, string>): Promise<unknown>;
  setAgentUrgency(agentId: string, urgent: boolean): Promise<unknown>;
  setManagedAgentAllowInlineTools(agentId: string, allowed: boolean): Promise<unknown>;
  attachmentUrl(reference: string): string;
  managedAgents(): Promise<ManagedAgentsResponse>;
  managedAgent(id: string): Promise<ManagedAgentResponse>;
  managedDirectories(path: string): Promise<ManagedDirectoriesResponse>;
  managedChildren(id: string): Promise<ManagedChildrenResponse>;
  managedGoal(id: string): Promise<ManagedGoalResponse>;
  saveManagedGoal(id: string, payload: { action: "set" | "off"; goal: string; settings: GoalControlSettings }): Promise<ManagedGoalResponse>;
  managedInlineTools(id: string): Promise<ManagedInlineToolsResponse>;
  saveManagedInlineTools(id: string, enabled: boolean): Promise<ManagedInlineToolsResponse>;
  managedHistory(agentId: string, view: "quiet" | "complete", options?: { before?: number; limit?: number }): Promise<ManagedHistoryResponse>;
  worktreeCheck(path: string): Promise<{ is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }>;
  launchManaged(payload: ManagedAgentLaunchPayload): Promise<ManagedOperationResponse>;
  restartManaged(id: string, model?: { provider: string; id: string }): Promise<ManagedOperationResponse>;
  resetManaged(id: string): Promise<ManagedOperationResponse>;
  terminateManaged(id: string, options?: { cleanupWorktree?: boolean }): Promise<ManagedOperationResponse>;
  models(): Promise<ModelsResponse>;
  providerAuth(): Promise<ProviderAuthResponse>;
  startProviderAuth(provider: ProviderModel["provider"], action: "login" | "logout"): Promise<ManagedOperationResponse>;
  respondProviderAuth(operationId: string, promptId: string, value: string): Promise<ManagedOperationResponse>;
  cancelProviderAuth(operationId: string): Promise<{ operation_id: string; status: string }>;
  setDefaultModel(model: { provider: string; id: string } | null): Promise<DefaultModelResponse>;
  systemOneConfig(): Promise<SystemOneConfig>;
  saveSystemOneConfig(config: SystemOneConfig): Promise<SystemOneConfig>;
  contextThresholds(): Promise<ContextThresholdConfig>;
  saveContextThresholds(config: ContextThresholdConfig): Promise<ContextThresholdConfig>;
  testSystemOne(payload?: SystemOneTestRequest): Promise<SystemOneTestResponse>;
  listMCPServers(): Promise<MCPServer[]>;
  addMCPServer(payload: AddMCPServerRequest): Promise<{ server: MCPServer; auth_url?: string }>;
  deleteMCPServer(id: string): Promise<void>;
  syncMCPServer(id: string): Promise<{ server: MCPServer; tools?: unknown[]; error?: unknown }>;
  startMCPOAuth(id: string): Promise<{ authorization_url: string }>;
  revokeMCPServer(id: string): Promise<{ server: MCPServer }>;
  restartRouter(options?: { update?: boolean }): Promise<{ ok: boolean; restarting: boolean }>;
  routerLog(query: RouterLogQuery): Promise<RouterLogResponse>;
  routerLogForAgent(agentId: string, query: RouterLogQuery): Promise<RouterLogResponse>;
  scratchpadForAgent(agentId: string, limit?: number): Promise<ScratchpadResponse>;
  listAgentMonitors(agentId: string): Promise<AgentMonitor[]>;
  createAgentMonitor(agentId: string, payload: CreateAgentMonitorPayload): Promise<AgentMonitor>;
  getAgentMonitor(agentId: string, monitorId: string): Promise<AgentMonitor>;
  updateAgentMonitor(agentId: string, monitorId: string, payload: Partial<CreateAgentMonitorPayload>): Promise<AgentMonitor>;
  deleteAgentMonitor(agentId: string, monitorId: string): Promise<void>;
  evaluateAgentMonitor(agentId: string, monitorId: string): Promise<MonitorEvaluationResult>;
  getToolDefaults(): Promise<ToolDefinitionItem[]>;
  updateToolDefaults(payload: { tool_name?: string; category?: string; enabled: boolean }): Promise<{ ok: boolean }>;
  getAgentTools(agentId: string): Promise<AgentToolsResponse>;
  updateAgentTools(agentId: string, payload: { tool_name?: string; category?: string; enabled: boolean }): Promise<{ ok: boolean }>;
  getGitSyncTarget(target: string): Promise<GitSyncTarget | null>;
  setGitSyncTarget(target: string, repoUrl: string): Promise<GitSyncTarget>;
  clearGitSyncTarget(target: string): Promise<void>;
  pullGitSync(target: string): Promise<GitSyncPullResponse>;
  pushGitSync(target: string): Promise<GitSyncPushResponse>;
  getKBFile(path?: string): Promise<string>;
  progressHistory(agentId: string): Promise<ProgressHistoryEntry[]>;
  readonly baseUrl?: string;
  interruptManagedAgent(agentId: string): Promise<void>;
  stopManagedSubagent(agentId: string, childId: string): Promise<void>;
}

export function createHttpClient(routerUrl: string, fetchImpl: FetchLike = globalThis.fetch.bind(globalThis)): RouterHttpClient {
  const base = normalizeRouterUrl(routerUrl);
  const request = async (path: string, init: RequestInit = {}, maxResponseBytes?: number): Promise<unknown> => {
    const response = await fetchImpl(resolveHttpEndpoint(base, path), init);
    const body = await jsonBody(response, maxResponseBytes);
    if (!response.ok) throw responseError(body, response.status, response.headers.get("x-correlation-id"));
    return body;
  };
  const jsonRequest = (path: string, method: string, payload?: unknown): Promise<unknown> => request(path, { method, headers: { "content-type": "application/json" }, ...(payload === undefined ? {} : { body: JSON.stringify(payload) }) });
  // Managed P3 calls have no browser authority material. The request ID is a
  // worker-wide idempotency key, and fetch is explicitly forbidden to attach
  // cookies should a browser happen to hold any for this origin.
  const managedRequest = async (path: string, method: "GET" | "POST" | "PUT" | "DELETE", payload?: unknown): Promise<unknown> => {
    const headers: Record<string, string> = {};
    if (payload !== undefined) headers["content-type"] = "application/json";
    if (method !== "GET") headers["x-request-id"] = secureUuid();
    return request(path, { method, credentials: "omit", ...(Object.keys(headers).length === 0 ? {} : { headers }), ...(payload === undefined ? {} : { body: JSON.stringify(payload) }) });
  };
  return {
    routerUrl: base,
    baseUrl: base,
    async interruptManagedAgent(agentId: string): Promise<void> {
      const doFetch = fetchImpl ?? fetch;
      await doFetch(`${this.baseUrl ?? base}/v1/managed-agents/${encodeURIComponent(agentId)}/interrupt`, { method: "POST" });
    },
    async stopManagedSubagent(agentId: string, childId: string): Promise<void> {
      const doFetch = fetchImpl ?? fetch;
      await doFetch(`${this.baseUrl ?? base}/v1/managed-agents/${encodeURIComponent(agentId)}/subagents/${encodeURIComponent(childId)}/stop`, { method: "POST" });
    },
    request,
    health: async () => {
      const body = recordValue(await request("/v1/health"));
      if (!body || typeof body.status !== "string" || typeof body.version !== "string" || !Array.isArray(body.schema_migrations) || !body.schema_migrations.every((item): item is string => typeof item === "string")) throw new HttpRequestError(200, "invalid_response", "Router returned an invalid health response", body);
      return { status: body.status, version: body.version, schema_migrations: body.schema_migrations };
    },
    capabilities: async () => {
      const body = recordValue(await request("/v1/capabilities"));
      if (!body || (Object.hasOwn(body, "goal_control_v1") && typeof body.goal_control_v1 !== "boolean") || (Object.hasOwn(body, "inline_tool_control_v1") && typeof body.inline_tool_control_v1 !== "boolean") || (Object.hasOwn(body, "subagent_lifecycle_v2") && typeof body.subagent_lifecycle_v2 !== "boolean") || (Object.hasOwn(body, "managed_v1") && typeof body.managed_v1 !== "boolean") || (Object.hasOwn(body, "ui_live_v1") && typeof body.ui_live_v1 !== "boolean")) throw invalidResponse("Router returned invalid capabilities", body);
      return body;
    },
    agents: async (options = {}) => { const includeModels = typeof options === "boolean" ? options : options.includeModels !== false; return agentList(await request(includeModels ? "/v1/agents" : "/v1/agents?models=omit"), includeModels); },
    conversations: async (owner, peer, options = {}) => {
      const params = new URLSearchParams({ owner, peer });
      if (options.before !== undefined && options.before > 0) params.set("before", String(options.before));
      if (options.limit !== undefined && options.limit > 0) params.set("limit", String(options.limit));
      return conversation(await request(`/v1/conversations?${params.toString()}`), base);
    },
    fetchConversationTimeline: async (agentId, limit = 50, beforeSeq) => {
      const params = new URLSearchParams({ agent_id: agentId });
      if (limit !== undefined && limit > 0) params.set("limit", String(limit));
      if (beforeSeq !== undefined && beforeSeq > 0) params.set("before_seq", String(beforeSeq));
      return conversationTimeline(await request(`/v1/conversation?${params.toString()}`));
    },
    sendMessage: async (message) => messageWrite(await jsonRequest("/v1/messages", "POST", message)),
    message: (id) => request(`/v1/messages/${encodeURIComponent(id)}`),
    retryMessage: (id) => jsonRequest(`/v1/messages/${encodeURIComponent(id)}/retry`, "POST"),
    deleteMessage: (id) => request(`/v1/messages/${encodeURIComponent(id)}`, { method: "DELETE" }),
    memoryPolicy: () => request("/v1/memory/policy"),
    memory: (path, payload) => path === "rebuild" ? jsonRequest("/v1/memory/rebuild", "POST", payload ?? {}) : path === "lookup" ? jsonRequest("/v1/memory/lookup", "POST", payload ?? {}) : path === "expand" ? jsonRequest("/v1/memory/expand", "POST", payload ?? {}) : path === "apply" ? jsonRequest("/v1/memory/apply", "POST", payload ?? {}) : jsonRequest("/v1/memory/touch", "POST", payload ?? {}),
    tmux: (path = "/v1/tmux/windows", method = "GET", payload) => method === "GET" ? request(path) : jsonRequest(path, method, payload),
    reminders: async () => reminders(await request("/v1/reminders")),
    createReminder: async (payload) => reminderWrite(await jsonRequest("/v1/reminders", "POST", payload)),
    updateReminder: async (id, payload) => reminderWrite(await jsonRequest(`/v1/reminders/${encodeURIComponent(id)}`, "PATCH", payload)),
    deleteReminder: (id) => request(`/v1/reminders/${encodeURIComponent(id)}`, { method: "DELETE" }),
    promptTemplates: async () => templates(await request("/v1/prompt-templates")),
    createPromptTemplate: async (payload) => templateWrite(await jsonRequest("/v1/prompt-templates", "POST", payload)),
    updatePromptTemplate: async (id, payload) => templateWrite(await jsonRequest(`/v1/prompt-templates/${encodeURIComponent(id)}`, "PATCH", payload)),
    deletePromptTemplate: (id) => request(`/v1/prompt-templates/${encodeURIComponent(id)}`, { method: "DELETE" }),
    workflowStates: async () => request("/v1/workflow-states") as Promise<WorkflowStatesResponse>,
    workflowTools: async () => request("/v1/workflow-tools") as Promise<WorkflowToolsResponse>,
    getWorkflowState: async (id) => request(`/v1/workflow-states/${encodeURIComponent(id)}`) as Promise<WorkflowStateResponse>,
    createWorkflowState: async (payload) => jsonRequest("/v1/workflow-states", "POST", payload) as Promise<WorkflowStateResponse>,
    updateWorkflowState: async (id, payload) => jsonRequest(`/v1/workflow-states/${encodeURIComponent(id)}`, "PUT", payload) as Promise<WorkflowStateResponse>,
    deleteWorkflowState: (id) => request(`/v1/workflow-states/${encodeURIComponent(id)}`, { method: "DELETE" }),
    listWorkflows: async () => request("/v1/workflows") as Promise<Workflow[]>,
    getWorkflow: async (id) => request(`/v1/workflows/${encodeURIComponent(id)}`) as Promise<WorkflowWithStates>,
    createWorkflow: async (req) => jsonRequest("/v1/workflows", "POST", req) as Promise<Workflow>,
    updateWorkflow: async (id, req) => jsonRequest(`/v1/workflows/${encodeURIComponent(id)}`, "PUT", req) as Promise<Workflow>,
    deleteWorkflow: async (id) => { await request(`/v1/workflows/${encodeURIComponent(id)}`, { method: "DELETE" }); },
    duplicateWorkflow: async (id, req) => jsonRequest(`/v1/workflows/${encodeURIComponent(id)}/duplicate`, "POST", req ?? {}) as Promise<Workflow>,
    exportWorkflow: async (id) => request(`/v1/workflows/${encodeURIComponent(id)}/export`) as Promise<WorkflowExportBundle>,
    importWorkflow: async (bundle) => jsonRequest("/v1/workflows/import", "POST", bundle) as Promise<Workflow>,
    updateWorkflowLayout: async (id, positions) => { await jsonRequest(`/v1/workflows/${encodeURIComponent(id)}/layout`, "PUT", { positions }); },
    runWorkflow: async (workflowId: string, options?: { variables?: Record<string, string>; cwd?: string; model?: { provider: string; id: string } }) => {
      const body = await jsonRequest(`/v1/workflows/${encodeURIComponent(workflowId)}/run`, "POST", options ?? {});
      return (recordValue(body)?.agent ?? body) as ManagedAgent;
    },
    listWorkflowSchedules: async (workflowId?: string) => {
      const q = workflowId ? `?workflow_id=${encodeURIComponent(workflowId)}` : "";
      const body = await request(`/v1/workflow-schedules${q}`);
      const rec = recordValue(body);
      if (Array.isArray(rec?.schedules)) return rec.schedules as WorkflowSchedule[];
      if (Array.isArray(body)) return body as WorkflowSchedule[];
      return [];
    },
    createWorkflowSchedule: async (req: Partial<WorkflowSchedule>) => {
      const body = await jsonRequest("/v1/workflow-schedules", "POST", req);
      const rec = recordValue(body);
      return (rec?.schedule ?? body) as WorkflowSchedule;
    },
    updateWorkflowSchedule: async (id: string, req: Partial<WorkflowSchedule>) => {
      const body = await jsonRequest(`/v1/workflow-schedules/${encodeURIComponent(id)}`, "PATCH", req);
      const rec = recordValue(body);
      return (rec?.schedule ?? body) as WorkflowSchedule;
    },
    deleteWorkflowSchedule: async (id: string) => {
      await request(`/v1/workflow-schedules/${encodeURIComponent(id)}`, { method: "DELETE" });
    },
    setManagedAgentWorkflowState: async (agentId: string, workflowStateId: string | null, options?: { workflow_id?: string; variables?: Record<string, string> }) => {
      const payload: Record<string, unknown> = workflowStateId
        ? { action: "activate", workflow_state_id: workflowStateId }
        : { action: "deactivate" };
      if (options?.workflow_id !== undefined) payload.workflow_id = options.workflow_id;
      if (options?.variables !== undefined) payload.variables = options.variables;
      return managedRequest(`/v1/managed-agents/${encodeURIComponent(agentId)}/workflow-state`, "PUT", payload);
    },
    setManagedAgentWorkflow: async (agentId: string, workflowId: string, variables?: Record<string, string>) => {
      const payload: Record<string, unknown> = { action: "activate", workflow_id: workflowId };
      if (variables !== undefined) payload.variables = variables;
      return managedRequest(`/v1/managed-agents/${encodeURIComponent(agentId)}/workflow-state`, "PUT", payload);
    },
    setAgentUrgency: async (agentId, urgent) => request(`/v1/agents/${encodeURIComponent(agentId)}/urgency`, { method: "PUT", credentials: "omit", headers: { "content-type": "application/json" }, body: JSON.stringify({ urgent }) }),
    setManagedAgentAllowInlineTools: async (agentId: string, allowed: boolean) =>
      managedRequest(`/v1/managed-agents/${encodeURIComponent(agentId)}/allow-inline-tools`, "POST", { allow_inline_tools: allowed }),
    attachmentUrl: (reference) => resolveAttachmentUrl(base, reference),
    managedAgents: async () => managedAgents(await managedRequest("/v1/managed-agents", "GET")),
    managedAgent: async (id) => managedAgent(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}`, "GET")),
    managedDirectories: async (path) => managedDirectories(await managedRequest(`/v1/managed-directories?path=${encodeURIComponent(path)}`, "GET")),
    managedChildren: async (id) => managedChildren(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}/children`, "GET")),
    managedGoal: async (id) => managedGoal(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}/goal`, "GET")),
    saveManagedGoal: async (id, payload) => managedGoal(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}/goal`, "PUT", payload.action === "off" ? { action: "off", settings: payload.settings } : { action: "set", goal: payload.goal, settings: payload.settings })),
    managedInlineTools: async (id) => managedInlineTools(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}/inline-tools`, "GET")),
    saveManagedInlineTools: async (id, enabled) => managedInlineTools(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}/inline-tools`, "PUT", { enabled })),
    managedHistory: async (agentId, view, options = {}) => {
      const query = new URLSearchParams({ agent_id: agentId, view });
      if (options.before !== undefined) query.set("before", String(options.before));
      if (options.limit !== undefined) query.set("limit", String(options.limit));
      return managedHistory(await managedRequest(`/v1/history?${query.toString()}`, "GET"));
    },
    worktreeCheck: async (path: string) => {
      const raw = await managedRequest(`/v1/git/worktree-check?path=${encodeURIComponent(path)}`, "GET");
      const body = recordValue(raw);
      return {
        is_git_repo: Boolean(body?.is_git_repo),
        is_main_worktree: Boolean(body?.is_main_worktree),
        has_uncommitted_changes: Boolean(body?.has_uncommitted_changes),
      };
    },
    launchManaged: async (payload) => {
      const body: Record<string, unknown> = { ...payload };
      if (payload.worktree !== undefined) body.worktree = payload.worktree;
      if (payload.worktree_branch !== undefined) body.worktree_branch = payload.worktree_branch;
      return managedOperation(await managedRequest("/v1/managed-agents", "POST", body));
    },
    restartManaged: async (id, model) => managedOperation(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}/restart`, "POST", model === undefined ? {} : { model })),
    resetManaged: async (id) => managedOperation(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}/reset`, "POST", {})),
    terminateManaged: async (id, options) => {
      const query = options?.cleanupWorktree !== undefined ? `?cleanup_worktree=${options.cleanupWorktree}` : "";
      return managedOperation(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}${query}`, "DELETE"));
    },
    models: async () => models(await managedRequest("/v1/models", "GET")),
    providerAuth: async () => providerAuth(await managedRequest("/v1/provider-auth", "GET")),
    startProviderAuth: async (provider, action) => managedOperation(await managedRequest(`/v1/provider-auth/${provider}/${action}`, "POST", {})),
    // The value is intentionally accepted only as a call argument. This client
    // does not cache, log, return, or add it to a URL.
    respondProviderAuth: async (operationId, promptId, value) => managedOperation(await managedRequest(`/v1/provider-auth/operations/${encodeURIComponent(operationId)}/response`, "POST", { prompt_id: promptId, value })),
    cancelProviderAuth: async (operationId) => {
      const raw = await managedRequest(`/v1/provider-auth/operations/${encodeURIComponent(operationId)}/cancel`, "POST", {});
      const body = recordValue(raw);
      if (!body || typeof body.operation_id !== "string" || typeof body.status !== "string") throw invalidResponse("Router returned an invalid cancel response", body);
      return { operation_id: body.operation_id, status: body.status };
    },
    setDefaultModel: async (model) => defaultModel(await managedRequest("/v1/settings/default-model", "PUT", { model })),
    systemOneConfig: async () => {
      const body = await request("/v1/settings/systemone");
      const rec = recordValue(body);
      return {
        host: typeof rec?.host === "string" ? rec.host : "",
        port: typeof rec?.port === "number" ? rec.port : 0,
        enabled: Boolean(rec?.enabled),
      };
    },
    saveSystemOneConfig: async (config) => {
      const body = await jsonRequest("/v1/settings/systemone", "PUT", config);
      const rec = recordValue(body);
      return {
        host: typeof rec?.host === "string" ? rec.host : "",
        port: typeof rec?.port === "number" ? rec.port : 0,
        enabled: Boolean(rec?.enabled),
      };
    },
    contextThresholds: async () => {
      const body = await request("/v1/settings/context-thresholds");
      const rec = recordValue(body);
      const warning_percent = typeof rec?.warning_percent === "number" ? rec.warning_percent : 10;
      const reset_percent = typeof rec?.reset_percent === "number" ? rec.reset_percent : 20;
      const model_thresholds: Record<string, ModelContextThreshold> = {};
      const rawOverrides = recordValue(rec?.model_thresholds);
      if (rawOverrides) {
        for (const [key, val] of Object.entries(rawOverrides)) {
          const item = recordValue(val);
          if (item && typeof item.warning_percent === "number" && typeof item.reset_percent === "number") {
            model_thresholds[key] = {
              warning_percent: item.warning_percent,
              reset_percent: item.reset_percent,
            };
          }
        }
      }
      return { warning_percent, reset_percent, model_thresholds };
    },
    saveContextThresholds: async (config) => {
      const body = await jsonRequest("/v1/settings/context-thresholds", "PUT", config);
      const rec = recordValue(body);
      const warning_percent = typeof rec?.warning_percent === "number" ? rec.warning_percent : 10;
      const reset_percent = typeof rec?.reset_percent === "number" ? rec.reset_percent : 20;
      const model_thresholds: Record<string, ModelContextThreshold> = {};
      const rawOverrides = recordValue(rec?.model_thresholds);
      if (rawOverrides) {
        for (const [key, val] of Object.entries(rawOverrides)) {
          const item = recordValue(val);
          if (item && typeof item.warning_percent === "number" && typeof item.reset_percent === "number") {
            model_thresholds[key] = {
              warning_percent: item.warning_percent,
              reset_percent: item.reset_percent,
            };
          }
        }
      }
      return { warning_percent, reset_percent, model_thresholds };
    },
    testSystemOne: async (payload) => {
      const body = await jsonRequest("/v1/systemone/test", "POST", payload ?? {});
      const rec = recordValue(body);
      return {
        status: rec?.status === "ok" ? "ok" : "error",
        ...(rec?.response ? { response: rec.response as SystemOneTestResponse["response"] } : {}),
        ...(rec?.error ? { error: String(rec.error) } : {}),
      };
    },
    listMCPServers: async () => {
      const body = await request("/v1/settings/mcp-servers");
      const rec = recordValue(body);
      if (!rec || !Array.isArray(rec.servers)) return [];
      return rec.servers as MCPServer[];
    },
    addMCPServer: async (payload) => {
      const body = await jsonRequest("/v1/settings/mcp-servers", "POST", payload);
      const rec = recordValue(body);
      return {
        server: rec?.server as MCPServer,
        ...(typeof rec?.auth_url === "string" ? { auth_url: rec.auth_url } : {}),
      };
    },
    deleteMCPServer: async (id) => {
      await request(`/v1/settings/mcp-servers/${encodeURIComponent(id)}`, { method: "DELETE" });
    },
    syncMCPServer: async (id) => {
      const body = await jsonRequest(`/v1/settings/mcp-servers/${encodeURIComponent(id)}/sync`, "POST", {});
      const rec = recordValue(body);
      return {
        server: rec?.server as MCPServer,
        ...(Array.isArray(rec?.tools) ? { tools: rec.tools } : {}),
        ...(rec?.error ? { error: rec.error } : {}),
      };
    },
    startMCPOAuth: async (id) => {
      const body = await jsonRequest(`/v1/settings/mcp-servers/${encodeURIComponent(id)}/auth/start`, "POST", {});
      const rec = recordValue(body);
      return {
        authorization_url: typeof rec?.authorization_url === "string" ? rec.authorization_url : "",
      };
    },
    revokeMCPServer: async (id) => {
      const body = await jsonRequest(`/v1/settings/mcp-servers/${encodeURIComponent(id)}/auth/revoke`, "POST", {});
      const rec = recordValue(body);
      return {
        server: rec?.server as MCPServer,
      };
    },
    restartRouter: async (options?: { update?: boolean }) => {
      const body = await jsonRequest("/v1/router/restart", "POST", {
        update: Boolean(options?.update),
      });
      const rec = recordValue(body);
      return {
        ok: Boolean(rec?.ok),
        restarting: Boolean(rec?.restarting),
      };
    },
    routerLog: async (query) => { if (!routerLogQuery(query)) throw new Error("Router-log query is invalid"); const params = new URLSearchParams(); params.set("query", query.query); if (query.from !== null) params.set("from", query.from); if (query.to !== null) params.set("to", query.to); if (query.cursor !== null) params.set("cursor", query.cursor); return routerLogResponse(await request(`/v1/router-log?${params.toString()}`, { method: "GET", credentials: "omit" }, 48 * 1024 * 1024)); },
    routerLogForAgent: async (agentId, query) => { if (!isAgentId(agentId) || !routerLogQuery(query)) throw new Error("Router-log query is invalid"); const params = new URLSearchParams(); params.set("query", query.query); if (query.from !== null) params.set("from", query.from); if (query.to !== null) params.set("to", query.to); if (query.cursor !== null) params.set("cursor", query.cursor); return routerLogResponse(await request(`/v1/managed-agents/${encodeURIComponent(agentId)}/router-log?${params.toString()}`, { method: "GET", credentials: "omit" }, 48 * 1024 * 1024)); },
    scratchpadForAgent: async (agentId: string, limit?: number) => {
      const params = new URLSearchParams();
      if (typeof limit === "number" && limit > 0) params.set("limit", String(limit));
      const queryString = params.toString();
      const path = `/v1/managed-agents/${encodeURIComponent(agentId)}/scratchpad${queryString ? `?${queryString}` : ""}`;
      const res = await request(path, { method: "GET", credentials: "omit" });
      const body = recordValue(res);
      if (!body || !Array.isArray(body.entries) || typeof body.total_count !== "number") {
        throw invalidResponse("Router returned an invalid scratchpad response", body);
      }
      return {
        entries: body.entries as ScratchpadEntry[],
        total_count: body.total_count,
      };
    },
    listAgentMonitors: async (agentId: string): Promise<AgentMonitor[]> => {
      const res = await request(`/v1/agents/${encodeURIComponent(agentId)}/monitors`, { method: "GET", credentials: "omit" });
      const body = recordValue(res);
      if (!body || !Array.isArray(body.monitors)) {
        throw invalidResponse("Router returned an invalid agent monitors list response", body);
      }
      return body.monitors as AgentMonitor[];
    },
    createAgentMonitor: async (agentId: string, payload: CreateAgentMonitorPayload): Promise<AgentMonitor> => {
      const res = await jsonRequest(`/v1/agents/${encodeURIComponent(agentId)}/monitors`, "POST", payload);
      const body = recordValue(res);
      if (!body || !body.monitor) {
        throw invalidResponse("Router returned an invalid create monitor response", body);
      }
      return body.monitor as AgentMonitor;
    },
    getAgentMonitor: async (agentId: string, monitorId: string): Promise<AgentMonitor> => {
      const res = await request(`/v1/agents/${encodeURIComponent(agentId)}/monitors/${encodeURIComponent(monitorId)}`, { method: "GET", credentials: "omit" });
      const body = recordValue(res);
      if (!body || !body.monitor) {
        throw invalidResponse("Router returned an invalid get monitor response", body);
      }
      return body.monitor as AgentMonitor;
    },
    updateAgentMonitor: async (agentId: string, monitorId: string, payload: Partial<CreateAgentMonitorPayload>): Promise<AgentMonitor> => {
      const res = await jsonRequest(`/v1/agents/${encodeURIComponent(agentId)}/monitors/${encodeURIComponent(monitorId)}`, "PUT", payload);
      const body = recordValue(res);
      if (!body || !body.monitor) {
        throw invalidResponse("Router returned an invalid update monitor response", body);
      }
      return body.monitor as AgentMonitor;
    },
    deleteAgentMonitor: async (agentId: string, monitorId: string): Promise<void> => {
      await request(`/v1/agents/${encodeURIComponent(agentId)}/monitors/${encodeURIComponent(monitorId)}`, { method: "DELETE", credentials: "omit" });
    },
    evaluateAgentMonitor: async (agentId: string, monitorId: string): Promise<MonitorEvaluationResult> => {
      const res = await jsonRequest(`/v1/agents/${encodeURIComponent(agentId)}/monitors/${encodeURIComponent(monitorId)}/evaluate`, "POST", {});
      const body = recordValue(res);
      if (!body) {
        throw invalidResponse("Router returned an invalid evaluate monitor response", body);
      }
      return body as unknown as MonitorEvaluationResult;
    },
    getToolDefaults: async (): Promise<ToolDefinitionItem[]> => {
      const res = await request("/v1/settings/tools", { method: "GET" });
      const body = recordValue(res);
      if (!body || !Array.isArray(body.tools)) return [];
      return body.tools as ToolDefinitionItem[];
    },
    updateToolDefaults: async (payload: { tool_name?: string; category?: string; enabled: boolean }): Promise<{ ok: boolean }> => {
      const res = await jsonRequest("/v1/settings/tools", "PUT", payload);
      return (recordValue(res) ?? { ok: true }) as { ok: boolean };
    },
    getAgentTools: async (agentId: string): Promise<AgentToolsResponse> => {
      const res = await request(`/v1/managed-agents/${encodeURIComponent(agentId)}/tools`, { method: "GET" });
      const body = recordValue(res);
      if (!body || !Array.isArray(body.tools)) {
        return { agent_id: agentId, workflow_override: false, active_workflow_state: "", tools: [] };
      }
      return body as unknown as AgentToolsResponse;
    },
    updateAgentTools: async (agentId: string, payload: { tool_name?: string; category?: string; enabled: boolean }): Promise<{ ok: boolean }> => {
      const res = await jsonRequest(`/v1/managed-agents/${encodeURIComponent(agentId)}/tools`, "PUT", payload);
      return (recordValue(res) ?? { ok: true }) as { ok: boolean };
    },
    getGitSyncTarget: async (target: string): Promise<GitSyncTarget | null> => {
      try {
        const res = await request(`/v1/git-sync/${encodeURIComponent(target)}`, { method: "GET" });
        const body = recordValue(res);
        if (!body || typeof body.target !== "string" || typeof body.repo_url !== "string") return null;
        return {
          target: body.target,
          repo_url: body.repo_url,
          ...(typeof body.updated_at === "string" ? { updated_at: body.updated_at } : {}),
        };
      } catch (err) {
        if (err instanceof HttpRequestError && err.status === 404) {
          return null;
        }
        throw err;
      }
    },
    setGitSyncTarget: async (target: string, repoUrl: string): Promise<GitSyncTarget> => {
      const res = await jsonRequest(`/v1/git-sync/${encodeURIComponent(target)}`, "PUT", { repo_url: repoUrl });
      const body = recordValue(res);
      if (!body || typeof body.target !== "string" || typeof body.repo_url !== "string") {
        throw invalidResponse("Router returned an invalid git sync target response", body);
      }
      return {
        target: body.target,
        repo_url: body.repo_url,
        ...(typeof body.updated_at === "string" ? { updated_at: body.updated_at } : {}),
      };
    },
    clearGitSyncTarget: async (target: string): Promise<void> => {
      await request(`/v1/git-sync/${encodeURIComponent(target)}`, { method: "DELETE" });
    },
    pullGitSync: async (target: string): Promise<GitSyncPullResponse> => {
      const res = await jsonRequest(`/v1/git-sync/${encodeURIComponent(target)}/pull`, "POST", {});
      return (recordValue(res) ?? {}) as GitSyncPullResponse;
    },
    pushGitSync: async (target: string): Promise<GitSyncPushResponse> => {
      const res = await jsonRequest(`/v1/git-sync/${encodeURIComponent(target)}/push`, "POST", {});
      return (recordValue(res) ?? {}) as GitSyncPushResponse;
    },
    getKBFile: async (path?: string): Promise<string> => {
      const endpoint = path ? `/v1/kb/file?path=${encodeURIComponent(path)}` : "/v1/kb/file";
      const res = await fetchImpl(resolveHttpEndpoint(base, endpoint), { method: "GET" });
      if (!res.ok) {
        throw new HttpRequestError(res.status, "kb_fetch_failed", "Failed to fetch KB file", await res.text());
      }
      return await res.text();
    },
    progressHistory: async (agentId: string): Promise<ProgressHistoryEntry[]> => {
      const res = await request(`/v1/agents/${encodeURIComponent(agentId)}/progress/history`, { method: "GET", credentials: "omit" });
      if (!Array.isArray(res)) {
        throw invalidResponse("Router returned an invalid progress history response", res);
      }
      return res as ProgressHistoryEntry[];
    },
  };
}

export async function fetchConversationTimeline(
  agentId: string,
  limit = 50,
  beforeSeq?: number,
  baseUrl?: string
): Promise<ConversationTimelineResponse> {
  const client = createHttpClient(baseUrl ?? activeRouterUrl());
  return client.fetchConversationTimeline(agentId, limit, beforeSeq);
}

export { httpRoutes };
