import { normalizeRouterUrl } from "../persistence/settings-repository";
import { secureUuid } from "../platform/secure-uuid";
import { httpRoutes } from "../protocol/manifest";
import { isAgentId, isConversationMessage, isManagedAgent, isManagedChildrenProjection, isManagedCompleteHistoryEntry, isManagedHistoryEntry, isModelSummary, isRecord, isRouterUtcTimestamp } from "../protocol/guards";
import { normalizeGoalState, type GoalControlSettings, type GoalState } from "../domain/goals";
import type { Agent } from "../state/agent-projection";
import type { HistoryMessage } from "../domain/history";
import type { Reminder } from "../domain/reminders";
import type { PromptTemplate } from "../domain/prompt-templates";
import type { ManagedAgent, ManagedChildrenProjection, ManagedHistoryEntry } from "../protocol/types";
import { resolveAttachmentUrl, withAbsoluteAttachmentUrls } from "./attachment-urls";

export interface Capabilities extends Record<string, unknown> {}
export interface HealthResponse { status: string; version: string; schema_migrations?: string[] }
export interface AgentListResponse { agents: Agent[]; generated_at?: string; activity_snapshot_version?: number }
export interface ConversationResponse { owner: string; peer: string; high_water_sequence: number; messages: HistoryMessage[] }
export interface ReminderResponse { reminders: Reminder[] }
export interface ReminderWriteResponse { reminder: Reminder }
export interface PromptTemplateResponse { prompt_templates: PromptTemplate[] }
export interface ManagedAgentsResponse { generated_at: string; projection_version: number; agents: ManagedAgent[] }
export interface ManagedAgentResponse { generated_at: string; projection_version: number; agent: ManagedAgent }
export interface ManagedOperationResponse { operation_id: string; agent?: ManagedAgent; agents?: Array<{ agent_id: string; operation_id: string; status: string }> }
export interface ManagedDirectoriesResponse { path: string; parent: string; breadcrumbs?: Array<{ name: string; path: string }>; directories: Array<{ name: string; path: string }> }
export interface ManagedChildrenResponse extends ManagedChildrenProjection {}
export interface ManagedHistoryResponse { view: "quiet" | "complete"; entries: ManagedHistoryEntry[]; next_before: number | null }
export interface ManagedGoalResponse { goal_state: GoalState }
export interface ManagedInlineToolsResponse { enabled: boolean }
export interface ProviderModel { provider: "openai-codex" | "google" | "cursor"; id: string; name: string; available: boolean }
export interface ProviderStatus { provider: "openai-codex" | "google" | "cursor"; label: "ChatGPT" | "Google" | "Cursor"; authenticated: boolean; status: "authenticated" | "signed_out"; account_label?: string; updated_at: string; error?: string }
export interface ProviderAuthResponse { auth_revision: number; providers: ProviderStatus[] }
export interface ModelsResponse { auth_revision: number; models: ProviderModel[]; default_model: { provider: string; id: string; name?: string } | null }
export interface DefaultModelResponse { operation_id: string; default_model: { provider: string; id: string; name?: string } | null; effective_fallback: ProviderModel }
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
function timestamp(value: unknown): value is string { return typeof value === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(value) && Number.isFinite(Date.parse(value)) && new Date(value).toISOString() === value; }
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
  if (complete && (!isConversationMessage(item) || !validSource(item.source))) return false;
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
async function jsonBody(response: Response): Promise<unknown> {
  const text = await response.text();
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
    messages.push(withAbsoluteAttachmentUrls({ ...candidate, id: candidate.id }, routerUrl));
  }
  return { owner: root.owner, peer: root.peer, high_water_sequence: root.high_water_sequence, messages };
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
  if (!root || !isRouterUtcTimestamp(root.generated_at) || !safeInteger(root.projection_version) || !Array.isArray(root.agents) || !root.agents.every(isManagedAgent)) throw invalidResponse("Router returned an invalid managed-agent list", value);
  return { generated_at: root.generated_at, projection_version: root.projection_version, agents: root.agents };
}
function managedAgent(value: unknown): ManagedAgentResponse {
  const root = recordValue(value);
  if (!root || !isRouterUtcTimestamp(root.generated_at) || !safeInteger(root.projection_version) || !isManagedAgent(root.agent)) throw invalidResponse("Router returned an invalid managed-agent response", value);
  return { generated_at: root.generated_at, projection_version: root.projection_version, agent: root.agent };
}
function managedHistory(value: unknown): ManagedHistoryResponse {
  const root = recordValue(value); const entries = root?.entries;
  if (!root || (root.view !== "quiet" && root.view !== "complete") || !Array.isArray(entries) || !entries.every(root.view === "complete" ? isManagedCompleteHistoryEntry : isManagedHistoryEntry) || (root.next_before !== null && !safeInteger(root.next_before, 1))) throw invalidResponse("Router returned invalid managed history", value);
  return { view: root.view, entries, next_before: root.next_before };
}
function managedChildren(value: unknown): ManagedChildrenResponse {
  if (!isManagedChildrenProjection(value)) throw invalidResponse("Router returned invalid managed children", value);
  return value;
}
function managedGoal(value: unknown): ManagedGoalResponse {
  const root = recordValue(value); const state = normalizeGoalState(root?.goal_state);
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
function providerName(value: unknown): value is ProviderModel["provider"] { return value === "openai-codex" || value === "google" || value === "cursor"; }
function providerModel(value: unknown): value is ProviderModel {
  const item = recordValue(value);
  return !!item && providerName(item.provider) && nonEmptyString(item.id) && nonEmptyString(item.name) && typeof item.available === "boolean";
}
function providerStatus(value: unknown): value is ProviderStatus {
  const item = recordValue(value);
  if (!item || !providerName(item.provider) || typeof item.authenticated !== "boolean" || (item.status !== "authenticated" && item.status !== "signed_out") || item.authenticated !== (item.status === "authenticated") || !isRouterUtcTimestamp(item.updated_at) || (Object.hasOwn(item, "account_label") && !nonEmptyString(item.account_label)) || (Object.hasOwn(item, "error") && !nonEmptyString(item.error))) return false;
  const labels = { "openai-codex": "ChatGPT", google: "Google", cursor: "Cursor" } as const;
  return item.label === labels[item.provider];
}
function providerAuth(value: unknown): ProviderAuthResponse {
  const root = recordValue(value);
  if (!root || !safeInteger(root.auth_revision) || !Array.isArray(root.providers) || root.providers.length !== 3 || !root.providers.every(providerStatus) || new Set(root.providers.map((provider) => provider.provider)).size !== 3) throw invalidResponse("Router returned invalid provider state", value);
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
  if (!root || !safeInteger(root.auth_revision) || !Array.isArray(root.models) || root.models.length > 256 || !root.models.every(providerModel) || defaultModel === undefined) throw invalidResponse("Router returned invalid model catalog", value);
  return { auth_revision: root.auth_revision, models: root.models, default_model: defaultModel };
}
function defaultModel(value: unknown): DefaultModelResponse {
  const root = recordValue(value); const saved = savedModel(root?.default_model);
  if (!root || !nonEmptyString(root.operation_id) || saved === undefined || !providerModel(root.effective_fallback)) throw invalidResponse("Router returned invalid default model", value);
  return { operation_id: root.operation_id, default_model: saved, effective_fallback: root.effective_fallback };
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

export interface RouterHttpClient {
  readonly routerUrl: string;
  request(path: string, init?: RequestInit): Promise<unknown>;
  health(): Promise<HealthResponse>;
  capabilities(): Promise<Capabilities>;
  agents(options?: { includeModels?: boolean } | boolean): Promise<AgentListResponse>;
  conversations(owner: string, peer: string): Promise<ConversationResponse>;
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
  launchManaged(payload: { name: string; cwd: string; model?: { provider: string; id: string }; initial_instruction?: string }): Promise<ManagedOperationResponse>;
  restartManaged(id: string, model?: { provider: string; id: string }): Promise<ManagedOperationResponse>;
  resetManaged(id: string): Promise<ManagedOperationResponse>;
  terminateManaged(id: string): Promise<ManagedOperationResponse>;
  restartAllManaged(): Promise<ManagedOperationResponse>;
  models(): Promise<ModelsResponse>;
  providerAuth(): Promise<ProviderAuthResponse>;
  startProviderAuth(provider: ProviderModel["provider"], action: "login" | "logout"): Promise<ManagedOperationResponse>;
  respondProviderAuth(operationId: string, promptId: string, value: string): Promise<ManagedOperationResponse>;
  setDefaultModel(model: { provider: string; id: string } | null): Promise<DefaultModelResponse>;
}

export function createHttpClient(routerUrl: string, fetchImpl: FetchLike = globalThis.fetch.bind(globalThis)): RouterHttpClient {
  const base = normalizeRouterUrl(routerUrl);
  const request = async (path: string, init: RequestInit = {}): Promise<unknown> => {
    const response = await fetchImpl(resolveHttpEndpoint(base, path), init);
    const body = await jsonBody(response);
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
    conversations: async (owner, peer) => conversation(await request(`/v1/conversations?owner=${encodeURIComponent(owner)}&peer=${encodeURIComponent(peer)}`), base),
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
    launchManaged: async (payload) => managedOperation(await managedRequest("/v1/managed-agents", "POST", payload)),
    restartManaged: async (id, model) => managedOperation(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}/restart`, "POST", model === undefined ? {} : { model })),
    resetManaged: async (id) => managedOperation(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}/reset`, "POST", {})),
    terminateManaged: async (id) => managedOperation(await managedRequest(`/v1/managed-agents/${encodeURIComponent(id)}`, "DELETE")),
    restartAllManaged: async () => managedOperation(await managedRequest("/v1/managed-agents/restart-all", "POST", {})),
    models: async () => models(await managedRequest("/v1/models", "GET")),
    providerAuth: async () => providerAuth(await managedRequest("/v1/provider-auth", "GET")),
    startProviderAuth: async (provider, action) => managedOperation(await managedRequest(`/v1/provider-auth/${provider}/${action}`, "POST", {})),
    // The value is intentionally accepted only as a call argument. This client
    // does not cache, log, return, or add it to a URL.
    respondProviderAuth: async (operationId, promptId, value) => managedOperation(await managedRequest(`/v1/provider-auth/operations/${encodeURIComponent(operationId)}/response`, "POST", { prompt_id: promptId, value })),
    setDefaultModel: async (model) => defaultModel(await managedRequest("/v1/settings/default-model", "PUT", { model })),
  };
}

export { httpRoutes };
