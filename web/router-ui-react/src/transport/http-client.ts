import { normalizeRouterUrl } from "../persistence/settings-repository";
import { httpRoutes } from "../protocol/manifest";
import { isAgentId, isConversationMessage, isModelSummary, isRecord } from "../protocol/guards";
import { normalizeGoalState } from "../domain/goals";
import type { Agent } from "../state/agent-projection";
import type { HistoryMessage } from "../domain/history";
import type { Reminder } from "../domain/reminders";
import type { PromptTemplate } from "../domain/prompt-templates";
import { resolveAttachmentUrl, withAbsoluteAttachmentUrls } from "./attachment-urls";

export interface Capabilities extends Record<string, unknown> {}
export interface HealthResponse { status: string; version: string; schema_migrations?: string[] }
export interface AgentListResponse { agents: Agent[]; generated_at?: string; activity_snapshot_version?: number }
export interface ConversationResponse { owner: string; peer: string; high_water_sequence: number; messages: HistoryMessage[] }
export interface ReminderResponse { reminders: Reminder[] }
export interface ReminderWriteResponse { reminder: Reminder }
export interface PromptTemplateResponse { prompt_templates: PromptTemplate[] }
export type FetchLike = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

export class HttpRequestError extends Error {
  readonly status: number;
  readonly code: string;
  readonly correlationId?: string;
  readonly body: unknown;
  constructor(status: number, code: string, message: string, body: unknown, correlationId?: string) {
    super(message);
    this.name = "HttpRequestError";
    this.status = status;
    this.code = code;
    this.body = body;
    if (correlationId !== undefined && correlationId !== "") this.correlationId = correlationId;
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

function responseError(body: unknown, status: number, correlationId: string | null): HttpRequestError {
  const root = recordValue(body); const detail = recordValue(root?.error);
  const code = stringValue(detail?.code) ?? stringValue(root?.code) ?? `http_${status}`;
  const message = stringValue(detail?.message) ?? stringValue(root?.message) ?? `Router request failed (${status})`;
  return new HttpRequestError(status, code, message, body, correlationId ?? undefined);
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
      if (!body || (Object.hasOwn(body, "goal_control_v1") && typeof body.goal_control_v1 !== "boolean") || (Object.hasOwn(body, "subagent_lifecycle_v2") && typeof body.subagent_lifecycle_v2 !== "boolean")) throw invalidResponse("Router returned invalid capabilities", body);
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
  };
}

export { httpRoutes };
