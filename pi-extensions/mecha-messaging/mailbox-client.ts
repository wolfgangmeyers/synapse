import { createHash, randomBytes, randomUUID } from "node:crypto";

type UnknownRecord = Record<string, unknown>;
type WireRecord = UnknownRecord & {
  type?: string;
  frame_id?: string;
  agent_id?: string;
  instance_id?: string;
  lease_id?: string;
  parent_session_id?: string;
  producer_epoch?: string;
  event_id?: string;
  request_id?: string;
  client_sequence?: number;
  router_version?: number;
  subagents?: WireRecord[];
  subagent?: WireRecord;
  lifecycle_event?: string;
  model_summaries?: ModelSummary[];
  total_count?: number;
  truncated?: boolean;
  current_model?: ModelSummary;
  model?: UnknownRecord;
  control_id?: string;
  source_agent_id?: string;
  activity_state?: string;
  receipt_state?: string;
  connection_generation?: number;
  action?: string;
  expires_at?: string;
  revision?: number;
  goal?: unknown;
  settings?: UnknownRecord;
  reminder_id?: string;
  code?: string;
  source?: { kind?: string };
  id?: string;
  from?: string;
  to?: string;
  subject?: string;
  body?: unknown;
  created_at?: unknown;
  message_incarnation_id?: string;
  attempt_number?: number;
  delivery_expires_at?: string;
  delivery_items?: DeliveryWireItem[];
  message?: MailboxMessage;
};
type ModelSummary = { provider: string; id: string; name?: string };
type DeliveryWireItem = {
  message_id?: string;
  message_incarnation_id?: string;
  attempt_number?: number;
  delivery_expires_at?: string;
};
type MailboxMessage = WireRecord & { id: string };
type GoalState = {
  enabled: boolean;
  goal: string;
  goalLockEnabled: boolean;
  snoozeEnabled: boolean;
  snoozeDurationMs: number;
  snoozeTurnLimit: number | null;
  snoozeBudgetCapMs: number | null;
  snoozeRefillYieldMs: number;
  snoozeRefillElapsedMs: number;
  outcomeCounts: Record<string, number>;
};
type SocketEvent = { data?: unknown; error?: unknown };
type MailboxSocket = {
  readyState: number;
  send(data: string): void;
  close(): void;
  addEventListener(type: string, listener: (event: SocketEvent) => void, options?: { once?: boolean }): void;
};
type Delivery = {
  messageId: string;
  attemptNumber: number;
  items: Array<{
    messageId: string | undefined;
    messageIncarnationId: string | undefined;
    attemptNumber: number | undefined;
    deliveryExpiresAt: string | undefined;
  }>;
};
type ControlContext = { socket: MailboxSocket | undefined; leaseId: string | undefined; expiresAtMs: number };
type JournalItem = { frame: WireRecord; bytes: number; enqueuedAt: number };
type ControlFrame = WireRecord & {
  control_id: string;
  source_agent_id: string;
  action: string;
  expires_at?: string;
};
type GoalControlFrame = ControlFrame & {
  agent_id: string;
  instance_id: string;
  lease_id: string;
  revision: number;
};
type ReminderLockControlFrame = ControlFrame & {
  agent_id: string;
  instance_id: string;
  lease_id: string;
  reminder_id: string;
  expires_at: string;
};
type ModelCatalogFrame = WireRecord & {
  model_summaries: ModelSummary[];
  total_count: number;
  truncated: boolean;
};
type ModelControlRequest = {
  controlId: string;
  sourceAgentId: string;
  action: string;
  model: ModelSummary | undefined;
};
type GoalControlRequest = {
  controlId: string;
  sourceAgentId: string;
  targetAgentId: string;
  targetInstanceId: string;
  revision: number;
  action: string;
  goal: unknown;
  settings: GoalSettingsInput | undefined;
};
type ReminderLockControlRequest = {
  controlId: string;
  sourceAgentId: string;
  targetAgentId: string;
  targetInstanceId: string;
  leaseId: string;
  reminderId: string;
  action: string;
  expiresAt: string;
  isCurrent: () => boolean;
};
type ModelControlResult = { accepted?: boolean; errorCode?: string; currentModel?: unknown; };
type GoalControlResult = { accepted?: boolean; errorCode?: string; goalState?: unknown; };
type ReminderLockControlResult = { accepted?: boolean; errorCode?: string; };
type MailboxClientOptions = {
  agentId?: unknown;
  routerUrl?: string;
  onEnvelope?: (message: MailboxMessage, delivery: Delivery) => unknown;
  onConnectionState?: (state: UnknownRecord) => void;
  onKickstartFailure?: (error: unknown) => void;
  kickstart?: () => unknown;
  log?: (...values: unknown[]) => void;
  webSocketFactory?: (url: string) => MailboxSocket;
  fetchImpl?: typeof fetch;
  generateId?: () => string;
  renewIntervalMs?: number;
  reconnectInitialDelayMs?: number;
  reconnectMaxDelayMs?: number;
  random?: () => number;
  setTimeoutImpl?: typeof setTimeout;
  clearTimeoutImpl?: typeof clearTimeout;
  setIntervalImpl?: typeof setInterval;
  clearIntervalImpl?: typeof clearInterval;
  assertIdentity?: (agentId: unknown) => string;
  parentSessionId?: string;
  producerEpoch?: string;
  capabilityTimeoutMs?: number;
  now?: () => number;
  onLifecycleReady?: (value: UnknownRecord) => void;
  onLifecycleDiagnostic?: (value: UnknownRecord) => void;
  onLifecycleTrace?: (value: UnknownRecord) => void;
  onModelControlTrace?: (value: UnknownRecord) => void;
  onModelControl?: (request: ModelControlRequest) => ModelControlResult | Promise<ModelControlResult>;
  getModelCatalog?: () => unknown;
  getCurrentModel?: () => unknown;
  onGoalControlTrace?: (value: UnknownRecord) => void;
  onGoalControl?: (request: GoalControlRequest) => GoalControlResult | Promise<GoalControlResult>;
  getGoalState?: () => unknown;
  onReminderLockControl?: (request: ReminderLockControlRequest) => ReminderLockControlResult | Promise<ReminderLockControlResult>;
  onReminderLockReset?: () => void;
};

type ReplyPayload = { message?: WireRecord; error?: { code?: string } };
type GoalSettingsInput = {
  goalLockEnabled: boolean;
  snoozeEnabled: boolean;
  snoozeDurationMs: number;
  snoozeTurnLimit: number | null;
  snoozeBudgetCapMs: number | null;
  snoozeRefillYieldMs: number;
  snoozeRefillElapsedMs: number;
};

function isRecord(value: unknown): value is UnknownRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function errorProperty(error: unknown, property: "code" | "cause" | "message" | "name"): unknown {
  return isRecord(error) ? error[property] : undefined;
}

function errorName(error: unknown): string {
  const name = errorProperty(error, "name");
  return typeof name === "string" ? name : "Error";
}

function errorMessage(error: unknown): string | undefined {
  const message = errorProperty(error, "message");
  return typeof message === "string" ? message : undefined;
}

function socketPayloadText(data: unknown): string {
  if (typeof data === "string") return data;
  if (Buffer.isBuffer(data)) return data.toString("utf8");
  if (data instanceof ArrayBuffer) return Buffer.from(data).toString("utf8");
  if (data instanceof Uint8Array) return Buffer.from(data).toString("utf8");
  throw new TypeError("Mailbox v2 router sent an unsupported message payload");
}

function isGoalSettingsInput(value: unknown): value is GoalSettingsInput {
  if (!isRecord(value)
    || Object.keys(value).some((key) => !GOAL_SETTINGS_KEYS.includes(key))
    || GOAL_SETTINGS_REQUIRED_KEYS.some((key) => !Object.hasOwn(value, key))) return false;
  return typeof value.goalLockEnabled === "boolean"
    && typeof value.snoozeEnabled === "boolean"
    && typeof value.snoozeDurationMs === "number"
    && (value.snoozeTurnLimit === null || typeof value.snoozeTurnLimit === "number")
    && (value.snoozeBudgetCapMs === null || typeof value.snoozeBudgetCapMs === "number")
    && typeof value.snoozeRefillYieldMs === "number"
    && typeof value.snoozeRefillElapsedMs === "number";
}

function isModelControlFrame(frame: WireRecord): frame is ControlFrame {
  return typeof frame.control_id === "string" && typeof frame.source_agent_id === "string"
    && typeof frame.action === "string"
    && (frame.expires_at === undefined || typeof frame.expires_at === "string");
}

function isGoalControlFrame(frame: WireRecord): frame is GoalControlFrame {
  return isModelControlFrame(frame) && typeof frame.agent_id === "string"
    && typeof frame.instance_id === "string" && typeof frame.lease_id === "string"
    && Number.isSafeInteger(frame.revision);
}

function isReminderLockControlFrame(frame: WireRecord): frame is ReminderLockControlFrame {
  return isModelControlFrame(frame) && typeof frame.agent_id === "string"
    && typeof frame.instance_id === "string" && typeof frame.lease_id === "string"
    && typeof frame.reminder_id === "string" && typeof frame.expires_at === "string";
}

function isMailboxMessage(value: unknown): value is MailboxMessage {
  return isRecord(value) && typeof value.id === "string";
}

function isDelivery(value: unknown): value is Delivery {
  if (!isRecord(value) || typeof value.messageId !== "string"
    || typeof value.attemptNumber !== "number" || !Number.isSafeInteger(value.attemptNumber) || value.attemptNumber <= 0) return false;
  if (value.items === undefined) return true;
  return Array.isArray(value.items) && value.items.every((item) => isRecord(item));
}

const WebSocket = globalThis.WebSocket;

const NANOID_ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

export function createNanoId() {
  let id = "";
  while (id.length < 21) {
    for (const byte of randomBytes(21 - id.length)) {
      if (byte < 248) id += NANOID_ALPHABET[byte % NANOID_ALPHABET.length];
    }
  }
  return id;
}

const LOOPBACK_HOSTS = new Set(["127.0.0.1", "[::1]"]);
const DEFAULT_ROUTER_URL = "http://127.0.0.1:8765";
const DEFAULT_RECONNECT_INITIAL_DELAY_MS = 250;
const DEFAULT_RECONNECT_MAX_DELAY_MS = 30_000;
const DEFAULT_REPLY_TIMEOUT_MS = 5_000;
const DEFAULT_CAPABILITY_TIMEOUT_MS = 2_000;
const MODEL_CATALOG_REFRESH_INTERVAL_MS = 10_000;
const MAX_MODEL_CATALOG_FRAME_BYTES = 64 * 1024;
const MAX_SUBAGENTS = 999;
const LIFECYCLE_JOURNAL_MAX_ITEMS = 256;
const LIFECYCLE_JOURNAL_MAX_BYTES = 262_144;
const LIFECYCLE_JOURNAL_MAX_ITEM_BYTES = 65_536;
const LIFECYCLE_JOURNAL_MAX_AGE_MS = 10 * 60_000;
const SUBAGENT_STATUSES = new Set(["queued", "running", "completed", "steered", "aborted", "stopped", "error"]);
const MESSAGE_ARRIVAL_PREVIEW_CHARS = 140;
const MAX_GOAL_CHARS = 4_000;
const MAX_MODELS = 1_000;
const MAX_QUEUED_MODEL_CONTROLS = 256;
const MAX_QUEUED_GOAL_CONTROLS = 256;
const MAX_QUEUED_REMINDER_LOCK_CONTROLS = 256;
const DEFAULT_MODEL_CONTROL_TTL_MS = 30_000;
const DEFAULT_GOAL_CONTROL_TTL_MS = 30_000;
const CAPABILITY_RETRY_INITIAL_DELAY_MS = 1_000;
const CAPABILITY_RETRY_MAX_DELAY_MS = 30_000;
const FATAL_ROUTER_ERROR_CODES = new Set([
  "already_registered",
  "internal_error",
  "invalid_frame",
  "lease_expired",
  "lease_not_active",
]);

// This pattern is narrower than the protocol's agentId on purpose, and the difference is a dot.
// The protocol allows `A-Za-z0-9._-` (protocol/v1/schema.json, $defs.agentId); a tmux window name
// cannot contain a dot, so no mailbox agent ever has one. Self-registered Claude sessions, which
// exist to show presence in the router UI and are not mailbox addresses, take dotted ids for
// exactly that reason. Allowing a dot here merges the two id spaces: an ad hoc session could then
// register under a live worker's name, and this function is what stops such an id from ever
// reaching a mail-injecting sidecar. Widening it removes that guarantee silently.
export function assertAgentId(agentId: unknown): string {
  if (typeof agentId !== "string" || agentId.length === 0) {
    throw new Error("Mailbox v2 requires a valid current tmux window name");
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]*$/.test(agentId)) {
    throw new Error("Mailbox v2 agent identity is invalid");
  }
  return agentId;
}

// Self-registered Claude sessions exist to show presence in the router UI. They are not mailbox
// addresses and must never be able to become one, so this REQUIRES the dot that assertAgentId
// forbids. The two validators are complements: no string satisfies both, so a presence id cannot
// be handed to a mail-injecting sidecar and a worker id cannot be claimed by an ad hoc session.
// The pattern otherwise matches the protocol's agentId (protocol/v1/schema.json, $defs.agentId).
export function assertPresenceAgentId(agentId: unknown): string {
  if (typeof agentId !== "string" || agentId.length === 0) {
    throw new Error("Mailbox v2 presence identity is required");
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(agentId) || !agentId.includes(".")) {
    throw new Error("Mailbox v2 presence identity must be dotted and cannot be a worker id");
  }
  if (agentId.length > 128) throw new Error("Mailbox v2 presence identity is too long");
  return agentId;
}

export function boundedConversationText(content: unknown, maximum = 4096): string {
  const blocks = Array.isArray(content) ? content : [content];
  const text = blocks.map((block) => typeof block === "string" ? block : block?.type === "text" && typeof block.text === "string" ? block.text : "").filter(Boolean).join("\n");
  return text.slice(-maximum);
}

export function routerHttpUrl(routerUrl: string): string {
  const url = new URL(routerUrl);
  if (!["http:", "https:"].includes(url.protocol) || !LOOPBACK_HOSTS.has(url.hostname)
    || !["", "/"].includes(url.pathname) || url.search || url.hash) {
    throw new Error("MECHA_ROUTER_URL must be an exact loopback HTTP(S) base URL");
  }
  return url.toString().replace(/\/$/, "");
}

export function routerWebSocketUrl(routerUrl: string): string {
  const url = new URL(routerHttpUrl(routerUrl));
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.pathname = "/v1/agents";
  return url.toString();
}

const PIONEER_SQUARE_ROUTE_PREFIX = "pioneer-square:";
const PIONEER_SQUARE_ROUTE_SUBJECT_PREFIX = "pioneer-square.route.v1:";

export function parsePioneerSquareRoute(route: unknown): { channelName: string; channelId: string; route: string } | undefined {
  if (typeof route !== "string") return undefined;
  const match = /^pioneer-square:#([a-z0-9][a-z0-9-]{0,98}):(\d{17,20})$/u.exec(route);
  if (match === null) return undefined;
  const [, channelName, channelId] = match;
  return { channelName, channelId, route };
}

export function parsePioneerSquareRouteSubject(subject: unknown): { channelName: string; channelId: string; route: string } | undefined {
  if (typeof subject !== "string" || !subject.startsWith(PIONEER_SQUARE_ROUTE_SUBJECT_PREFIX)) return undefined;
  return parsePioneerSquareRoute(subject.slice(PIONEER_SQUARE_ROUTE_SUBJECT_PREFIX.length));
}

export function pioneerSquareRouteSubject(route: string): string {
  if (parsePioneerSquareRoute(route) === undefined) throw new TypeError("invalid Pioneer Square route");
  return `${PIONEER_SQUARE_ROUTE_SUBJECT_PREFIX}${route}`;
}

export function pioneerSquareRouteTarget(route: string): { to: "pioneer-square"; subject: string } {
  return { to: "pioneer-square", subject: pioneerSquareRouteSubject(route) };
}

export function parseReplyArguments(argumentsText: string): { recipient: string; body: string } {
  const [recipient, ...bodyParts] = argumentsText.trim().split(/\s+/);
  const body = bodyParts.join(" ");
  if (!recipient || !body || recipient.includes("@")) {
    throw new Error("Usage: /mecha-reply <recipient> <message>");
  }
  return { recipient, body };
}

function socketOpen(socket: MailboxSocket | undefined): boolean {
  return socket?.readyState === (WebSocket?.OPEN ?? 1);
}

function connectionRefused(error: unknown): boolean {
  const cause = errorProperty(error, "cause");
  return errorProperty(error, "code") === "ECONNREFUSED"
    || errorProperty(cause, "code") === "ECONNREFUSED";
}

function normalizeGoalProjection(value: unknown): { enabled: boolean; goal: string } | undefined {
  if (!isRecord(value)
    || typeof value.enabled !== "boolean" || typeof value.goal !== "string"
    || value.goal.length > MAX_GOAL_CHARS) return undefined;
  if (!value.enabled && value.goal !== "") return undefined;
  if (value.enabled && value.goal.length === 0) return undefined;
  return { enabled: value.enabled, goal: value.enabled ? value.goal : "" };
}

function normalizeSubagentSummary(summary: unknown): WireRecord | undefined {
  if (!isRecord(summary)) return undefined;
  const id = typeof summary.id === "string" ? summary.id.trim().slice(0, 128) : "";
  const description = typeof summary.description === "string" ? summary.description.trim().slice(0, 512) : "";
  if (id.length === 0 || description.length === 0) return undefined;
  const type = typeof summary.type === "string" ? summary.type.trim().slice(0, 64) : "";
  const status = typeof summary.status === "string" ? summary.status : undefined;
  const normalized = { id, ...(type.length > 0 ? { type } : {}), description };
  return status !== undefined && SUBAGENT_STATUSES.has(status) ? { ...normalized, status } : normalized;
}

function normalizeSubagents(subagents: unknown): WireRecord[] {
  if (!Array.isArray(subagents)) return [];
  return subagents.map(normalizeSubagentSummary).filter((summary): summary is WireRecord => summary !== undefined).slice(0, MAX_SUBAGENTS);
}

function normalizeModelString(value: unknown, maximum: number): string {
  if (typeof value !== "string") return "";
  const normalized = value.trim();
  return normalized.length > 0 && [...normalized].length <= maximum ? normalized : "";
}

function normalizeModelSummary(model: unknown): ModelSummary | undefined {
  if (!isRecord(model)) return undefined;
  const provider = normalizeModelString(model.provider, 128);
  const id = normalizeModelString(model.id, 256);
  if (provider.length === 0 || id.length === 0) return undefined;
  const name = normalizeModelString(model.name, 256);
  return { provider, id, ...(name.length > 0 ? { name } : {}) };
}

function normalizeModelControlResult(result: unknown): { accepted: boolean; errorCode?: string; currentModel?: ModelSummary } {
  const accepted = isRecord(result) && result.accepted === true;
  const resultErrorCode = isRecord(result) ? result.errorCode : undefined;
  const errorCode = typeof resultErrorCode === "string"
    && /^[a-z][a-z0-9_]{0,127}$/.test(resultErrorCode.trim())
    ? resultErrorCode.trim()
    : "model_switch_failed";
  const currentModel = normalizeModelSummary(isRecord(result) ? result.currentModel : undefined);
  return {
    accepted,
    ...(accepted ? {} : { errorCode }),
    ...(currentModel === undefined ? {} : { currentModel }),
  };
}

function normalizeModelSummaries(models: unknown, maximum = MAX_MODELS): ModelSummary[] {
  if (!Array.isArray(models)) return [];
  const seen = new Set();
  return models.map(normalizeModelSummary).filter((model): model is ModelSummary => {
    if (model === undefined) return false;
    const key = `${model.provider}\u0000${model.id}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  }).slice(0, maximum);
}

const GOAL_OUTCOME_KEYS = ["COMPLETED", "NOT_DONE"];
const GOAL_STATE_REQUIRED_KEYS = [
  "enabled", "goal", "goalLockEnabled", "snoozeEnabled", "snoozeDurationMs", "snoozeTurnLimit",
  "snoozeBudgetCapMs", "snoozeRefillYieldMs", "snoozeRefillElapsedMs", "outcomeCounts",
];
const GOAL_SETTINGS_REQUIRED_KEYS = [
  "goalLockEnabled", "snoozeEnabled", "snoozeDurationMs", "snoozeTurnLimit", "snoozeBudgetCapMs",
  "snoozeRefillYieldMs", "snoozeRefillElapsedMs",
];
const GOAL_STATE_KEYS = GOAL_STATE_REQUIRED_KEYS;
const GOAL_SETTINGS_KEYS = GOAL_SETTINGS_REQUIRED_KEYS;

function positiveGoalInteger(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0 ? value : undefined;
}

function normalizeGoalControlSettings(value: unknown): Omit<GoalState, "enabled" | "goal" | "outcomeCounts"> | undefined {
  if (!isGoalSettingsInput(value)) return undefined;
  const snoozeTurnLimit = value.snoozeTurnLimit === null ? null : positiveGoalInteger(value.snoozeTurnLimit);
  const snoozeBudgetCapMs = value.snoozeBudgetCapMs === null ? null : positiveGoalInteger(value.snoozeBudgetCapMs);
  if (typeof value.goalLockEnabled !== "boolean" || typeof value.snoozeEnabled !== "boolean"
    || positiveGoalInteger(value.snoozeDurationMs) === undefined
    || (value.snoozeTurnLimit !== null && (snoozeTurnLimit === undefined || snoozeTurnLimit === null || snoozeTurnLimit > 20))
    || (value.snoozeBudgetCapMs !== null && snoozeBudgetCapMs === undefined)
    || positiveGoalInteger(value.snoozeRefillYieldMs) === undefined
    || positiveGoalInteger(value.snoozeRefillElapsedMs) === undefined) return undefined;
  return {
    goalLockEnabled: value.goalLockEnabled,
    snoozeEnabled: value.snoozeEnabled,
    snoozeDurationMs: value.snoozeDurationMs,
    snoozeTurnLimit: snoozeTurnLimit === undefined ? null : snoozeTurnLimit,
    snoozeBudgetCapMs: snoozeBudgetCapMs === undefined ? null : snoozeBudgetCapMs,
    snoozeRefillYieldMs: value.snoozeRefillYieldMs,
    snoozeRefillElapsedMs: value.snoozeRefillElapsedMs,
  };
}

function normalizeGoalState(value: unknown): GoalState | undefined {
  if (!isRecord(value)
    || Object.keys(value).some((key) => !GOAL_STATE_KEYS.includes(key))
    || GOAL_STATE_REQUIRED_KEYS.some((key) => !Object.hasOwn(value, key))
    || typeof value.enabled !== "boolean" || typeof value.goal !== "string"
    || value.goal.length > MAX_GOAL_CHARS
    || (value.enabled && value.goal.length === 0)
    || (!value.enabled && value.goal !== "")) return undefined;
  const settings = normalizeGoalControlSettings({
    ...Object.fromEntries(GOAL_SETTINGS_REQUIRED_KEYS.map((key) => [key, value[key]])),
  });
  const outcomeCounts = value.outcomeCounts;
  if (settings === undefined || !isRecord(outcomeCounts)
    || Object.keys(outcomeCounts).length !== GOAL_OUTCOME_KEYS.length
    || GOAL_OUTCOME_KEYS.some((key) => !Object.hasOwn(outcomeCounts, key)
      || typeof outcomeCounts[key] !== "number" || !Number.isSafeInteger(outcomeCounts[key])
      || outcomeCounts[key] < 0 || outcomeCounts[key] > 1_000_000)) return undefined;
  const normalizedOutcomeCounts: Record<string, number> = {};
  for (const key of GOAL_OUTCOME_KEYS) {
    const count = outcomeCounts[key];
    if (typeof count !== "number") return undefined;
    normalizedOutcomeCounts[key] = count;
  }
  return {
    enabled: value.enabled,
    goal: value.enabled ? value.goal : "",
    ...settings,
    outcomeCounts: normalizedOutcomeCounts,
  };
}

function defaultGoalState(): GoalState {
  return {
    enabled: false,
    goal: "",
    goalLockEnabled: false,
    snoozeEnabled: true,
    snoozeDurationMs: 60_000,
    snoozeTurnLimit: null,
    snoozeBudgetCapMs: null,
    snoozeRefillYieldMs: 30_000,
    snoozeRefillElapsedMs: 60_000,
    outcomeCounts: Object.fromEntries(GOAL_OUTCOME_KEYS.map((key) => [key, 0])),
  };
}

function goalStateToWire(state: GoalState): WireRecord {
  return {
    enabled: state.enabled,
    goal: state.goal,
    goal_lock_enabled: state.goalLockEnabled,
    snooze_enabled: state.snoozeEnabled,
    snooze_duration_ms: state.snoozeDurationMs,
    snooze_turn_limit: state.snoozeTurnLimit,
    snooze_budget_cap_ms: state.snoozeBudgetCapMs,
    snooze_refill_yield_ms: state.snoozeRefillYieldMs,
    snooze_refill_elapsed_ms: state.snoozeRefillElapsedMs,
    outcome_counts: state.outcomeCounts,
  };
}

function goalSettingsFromWire(settings: unknown): Omit<GoalState, "enabled" | "goal" | "outcomeCounts"> | undefined {
  if (!isRecord(settings)) return undefined;
  return normalizeGoalControlSettings({
    goalLockEnabled: settings.goal_lock_enabled,
    snoozeEnabled: settings.snooze_enabled,
    snoozeDurationMs: settings.snooze_duration_ms,
    snoozeTurnLimit: settings.snooze_turn_limit,
    snoozeBudgetCapMs: settings.snooze_budget_cap_ms,
    snoozeRefillYieldMs: settings.snooze_refill_yield_ms,
    snoozeRefillElapsedMs: settings.snooze_refill_elapsed_ms,
  });
}

function modelCatalogSourceSignature(frameSignature: string, sourceSummaries: ModelSummary[]): string {
  const hash = createHash("sha256");
  hash.update(frameSignature);
  for (const model of sourceSummaries) {
    hash.update("\u0000");
    hash.update(model.provider);
    hash.update("\u0000");
    hash.update(model.id);
    hash.update("\u0000");
    hash.update(model.name ?? "");
  }
  return hash.digest("hex");
}

function normalizeTerminalReason(reason: unknown): string {
  const normalized = typeof reason === "string" ? reason.trim().toLowerCase() : "";
  return /^[a-z][a-z0-9_]{0,63}$/.test(normalized) ? normalized : "completed";
}

function arrivalPreview(value: unknown): string {
  const text = String(value ?? "").replace(/\s+/g, " ").trim();
  if (text.length <= MESSAGE_ARRIVAL_PREVIEW_CHARS) return text;
  let previewChars = MESSAGE_ARRIVAL_PREVIEW_CHARS - 1;
  const lastCodeUnit = text.charCodeAt(previewChars - 1);
  if (lastCodeUnit >= 0xd800 && lastCodeUnit <= 0xdbff) previewChars -= 1;
  return `${text.slice(0, previewChars)}…`;
}

function validateReconnectOptions(initialDelayMs: number, maxDelayMs: number): void {
  if (!Number.isSafeInteger(initialDelayMs) || initialDelayMs <= 0) {
    throw new TypeError("reconnectInitialDelayMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(maxDelayMs) || maxDelayMs < initialDelayMs) {
    throw new TypeError("reconnectMaxDelayMs must be a safe integer no smaller than reconnectInitialDelayMs");
  }
}

// The identity validator selects the id space for this client. Presence-only clients use the
// complementary validator and lifecycle identity remains process-local across socket replacement.
export function createMailboxClient({
  agentId: rawAgentId,
  routerUrl = process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL,
  onEnvelope,
  onConnectionState = () => {},
  onKickstartFailure = () => {},
  kickstart = () => {},
  log = () => {},
  webSocketFactory = (url: string) => new WebSocket(url),
  fetchImpl = fetch,
  generateId = createNanoId,
  renewIntervalMs = 10_000,
  reconnectInitialDelayMs = DEFAULT_RECONNECT_INITIAL_DELAY_MS,
  reconnectMaxDelayMs = DEFAULT_RECONNECT_MAX_DELAY_MS,
  random = Math.random,
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
  assertIdentity = assertAgentId,
  parentSessionId = generateId(),
  producerEpoch = randomUUID(),
  capabilityTimeoutMs = DEFAULT_CAPABILITY_TIMEOUT_MS,
  now = Date.now,
  onLifecycleReady = () => {},
  onLifecycleDiagnostic = () => {},
  onLifecycleTrace = () => {},
  onModelControlTrace = () => {},
  onModelControl = async () => ({ accepted: false, errorCode: "unsupported" }),
  getModelCatalog = () => [],
  getCurrentModel = () => undefined,
  onGoalControlTrace = () => {},
  onGoalControl = async () => ({ accepted: false, errorCode: "unsupported" }),
  getGoalState = defaultGoalState,
  onReminderLockControl = async () => ({ accepted: false, errorCode: "unsupported" }),
  onReminderLockReset = () => {},
}: MailboxClientOptions = {}) {
  assertIdentity(rawAgentId);
  if (typeof rawAgentId !== "string") {
    throw new TypeError("Mailbox v2 requires a valid current tmux window name");
  }
  const agentId = rawAgentId;
  const envelopeHandler = onEnvelope;
  if (typeof envelopeHandler !== "function") throw new TypeError("onEnvelope is required");
  if (typeof onConnectionState !== "function") throw new TypeError("onConnectionState must be a function");
  if (typeof onKickstartFailure !== "function") throw new TypeError("onKickstartFailure must be a function");
  if (typeof kickstart !== "function") throw new TypeError("kickstart must be a function");
  if (!Number.isSafeInteger(renewIntervalMs) || renewIntervalMs <= 0) {
    throw new TypeError("renewIntervalMs must be a positive safe integer");
  }
  if (typeof random !== "function") throw new TypeError("random must be a function");
  if (!Number.isSafeInteger(capabilityTimeoutMs) || capabilityTimeoutMs <= 0) throw new TypeError("capabilityTimeoutMs must be a positive safe integer");
  if (typeof now !== "function") throw new TypeError("now must be a function");
  if (typeof onLifecycleReady !== "function" || typeof onLifecycleDiagnostic !== "function" || typeof onLifecycleTrace !== "function") throw new TypeError("lifecycle callbacks must be functions");
  if (typeof onModelControlTrace !== "function") throw new TypeError("onModelControlTrace must be a function");
  if (typeof onModelControl !== "function") throw new TypeError("onModelControl must be a function");
  if (typeof getModelCatalog !== "function") throw new TypeError("getModelCatalog must be a function");
  if (typeof getCurrentModel !== "function") throw new TypeError("getCurrentModel must be a function");
  if (typeof onGoalControlTrace !== "function") throw new TypeError("onGoalControlTrace must be a function");
  if (typeof onGoalControl !== "function") throw new TypeError("onGoalControl must be a function");
  if (typeof getGoalState !== "function") throw new TypeError("getGoalState must be a function");
  if (typeof onReminderLockControl !== "function") throw new TypeError("onReminderLockControl must be a function");
  if (typeof onReminderLockReset !== "function") throw new TypeError("onReminderLockReset must be a function");
  if (typeof parentSessionId !== "string" || parentSessionId.length === 0 || typeof producerEpoch !== "string" || !/^[0-9a-fA-F-]{36}$/.test(producerEpoch)) throw new TypeError("valid lifecycle identity is required");
  validateReconnectOptions(reconnectInitialDelayMs, reconnectMaxDelayMs);
  routerHttpUrl(routerUrl);

  const instanceId = generateId();
  let socket: MailboxSocket | undefined;
  let leaseId: string | undefined;
  let renewTimer: ReturnType<typeof setInterval> | undefined;
  let modelCatalogTimer: ReturnType<typeof setInterval> | undefined;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let reconnectAttempt = 0;
  // A socket generation lets traces distinguish the same durable lifecycle event
  // being retried under a replacement lease after reconnect.
  let connectionGeneration = 0;
  let capabilityRetryTimer: ReturnType<typeof setTimeout> | undefined;
  let capabilityRetryAttempt = 0;
  let stopped = false;
  let paused = false;
  let initialConnection: Promise<void> | undefined;
  let resolveInitialConnection: (() => void) | undefined;
  let rejectInitialConnection: ((reason?: unknown) => void) | undefined;
  let lifecycleSupported = false;
  let lifecycleReady = false;
  let lifecycleConnectionDisabled = false;
  let lifecycleSequence = 0;
  let lifecycleRouterVersion = 0;
  let capabilitiesSocket: MailboxSocket | undefined;
  let modelCatalogSupported = false;
  let modelCatalogFrameBytes = MAX_MODEL_CATALOG_FRAME_BYTES;
  let lastPublishedModelCatalog: string | undefined;
  let goalControlSupported = false;
  let latestGoalState: GoalState | undefined;
  const modelControlQueue: Array<{ frame: ControlFrame; context: ControlContext }> = [];
  let modelControlProcessing = false;
  const goalControlQueue: Array<{ frame: GoalControlFrame; context: ControlContext }> = [];
  let goalControlProcessing = false;
  const reminderLockControlQueue: Array<{ frame: ReminderLockControlFrame; context: ControlContext }> = [];
  let reminderLockControlProcessing = false;
  // The newest full assertion is retained separately from the retry journal. A reconnect must
  // lead with it, because a replacement lease is fenced until the router accepts a snapshot.
  let lifecycleSnapshotChildren: WireRecord[] = [];
  const lifecycleJournal = new Map<string, JournalItem>();

  function report(state: string, error?: unknown): void {
    try {
      onConnectionState({ state, error });
    } catch {
      // Connection diagnostics must not alter delivery behavior.
    }
  }

  function send(fields: WireRecord): string {
    const currentSocket = socket;
    if (currentSocket === undefined || !socketOpen(currentSocket)) throw new Error("Mailbox v2 socket is not connected");
    const frameId = generateId();
    currentSocket.send(JSON.stringify({ version: "v1", frame_id: frameId, ...fields }));
    return frameId;
  }

  function lifecycleTrace(event: string, fields: WireRecord = {}): void {
    try {
      onLifecycleTrace({ event, agent_id: agentId, instance_id: instanceId, parent_session_id: parentSessionId, ...fields });
    } catch {
      // Trace collection cannot alter lifecycle delivery.
    }
  }

  function modelControlTrace(event: string, fields: WireRecord = {}): void {
    try {
      onModelControlTrace({
        event,
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: leaseId,
        ...fields,
      });
    } catch {
      // Diagnostic callbacks cannot alter model-control delivery.
    }
  }

  function goalControlTrace(event: string, fields: WireRecord = {}): void {
    try {
      onGoalControlTrace({
        event,
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: leaseId,
        ...fields,
      });
    } catch {
      // Diagnostic callbacks cannot alter goal-control delivery.
    }
  }

  function lifecycleFrameTrace(frame: WireRecord, phase: string): WireRecord {
    return {
      phase,
      frame_type: frame.type,
      frame_id: frame.frame_id,
      event_id: frame.event_id,
      request_id: frame.request_id,
      client_sequence: frame.client_sequence,
      lease_id: frame.lease_id,
      producer_epoch: frame.producer_epoch,
      connection_generation: connectionGeneration,
      snapshot_count: Array.isArray(frame.subagents) ? frame.subagents.length : undefined,
      subagent_count: frame.subagent === undefined ? undefined : 1,
      subagents: frame.subagents ?? (frame.subagent === undefined ? undefined : [frame.subagent]),
      pending: lifecycleJournal.size,
      bytes: serializedLifecycleBytes(frame),
    };
  }

  function lifecycleDiagnostic(reason: string): void {
    lifecycleTrace("client_lifecycle_diagnostic", { reason, pending: lifecycleJournal.size, bytes: journalBytes() });
    try { onLifecycleDiagnostic({ reason, pending: lifecycleJournal.size }); } catch { /* diagnostics cannot alter lifecycle delivery */ }
  }

  function canonicalLifecycleFrame(fields: WireRecord): WireRecord {
    return { version: "v1", frame_id: generateId(), ...fields };
  }

  function serializedLifecycleBytes(frame: WireRecord): number {
    return Buffer.byteLength(JSON.stringify(frame), "utf8");
  }

  function lifecycleSnapshotFields(subagents: WireRecord[] = lifecycleSnapshotChildren): WireRecord {
    return {
      type: "subagent_lifecycle_snapshot", agent_id: agentId, instance_id: instanceId,
      parent_session_id: parentSessionId, lease_id: leaseId, producer_epoch: producerEpoch,
      client_sequence: ++lifecycleSequence, event_id: generateId(), subagents,
    };
  }

  function failLifecycleClosed() {
    lifecycleTrace("client_lifecycle_closed", { reason: "journal_oversize", pending: lifecycleJournal.size, bytes: journalBytes() });
    lifecycleJournal.clear();
    lifecycleReady = false;
    lifecycleConnectionDisabled = true;
    lifecycleDiagnostic("journal_oversize");
  }

  function journalBytes() {
    let bytes = 0;
    for (const item of lifecycleJournal.values()) bytes += item.bytes;
    return bytes;
  }

  function makeReplacementSnapshot(reason: string, snapshot: WireRecord[]): WireRecord | undefined {
    const frame = canonicalLifecycleFrame(lifecycleSnapshotFields(snapshot));
    const bytes = serializedLifecycleBytes(frame);
    if (bytes > LIFECYCLE_JOURNAL_MAX_ITEM_BYTES || bytes > LIFECYCLE_JOURNAL_MAX_BYTES) {
      failLifecycleClosed();
      return undefined;
    }
    lifecycleJournal.clear();
    const eventId = frame.event_id ?? generateId();
    lifecycleJournal.set(eventId, { frame: { ...frame, event_id: eventId }, bytes, enqueuedAt: now() });
    lifecycleTrace("client_lifecycle_journal_replaced", { ...lifecycleFrameTrace(frame, reason), pending: lifecycleJournal.size, bytes });
    lifecycleDiagnostic(reason);
    return frame;
  }

  function lifecycleCanJournal() {
    return lifecycleSupported && lifecycleReady && !lifecycleConnectionDisabled && leaseId !== undefined;
  }

  function journalLifecycle(fields: WireRecord, snapshot: WireRecord[]): WireRecord | undefined {
    if (!lifecycleCanJournal()) return undefined;
    const frame = canonicalLifecycleFrame(fields);
    const bytes = serializedLifecycleBytes(frame);
    if (bytes > LIFECYCLE_JOURNAL_MAX_ITEM_BYTES) {
      failLifecycleClosed();
      return undefined;
    }
    const oldest = lifecycleJournal.values().next().value;
    if (oldest !== undefined && now() - oldest.enqueuedAt >= LIFECYCLE_JOURNAL_MAX_AGE_MS) {
      return makeReplacementSnapshot("journal_expired", snapshot);
    }
    if (lifecycleJournal.size >= LIFECYCLE_JOURNAL_MAX_ITEMS || journalBytes() + bytes > LIFECYCLE_JOURNAL_MAX_BYTES) {
      return makeReplacementSnapshot("journal_collapsed", snapshot);
    }
    const eventId = frame.event_id ?? generateId();
    lifecycleJournal.set(eventId, { frame: { ...frame, event_id: eventId }, bytes, enqueuedAt: now() });
    lifecycleTrace("client_lifecycle_journaled", { ...lifecycleFrameTrace(frame, "queued"), pending: lifecycleJournal.size, bytes });
    return frame;
  }

  function rebindLifecycleJournalLease() {
    if (leaseId === undefined || lifecycleJournal.size === 0) return;
    const rebound: Array<[string, JournalItem]> = [];
    let totalBytes = 0;
    for (const item of lifecycleJournal.values()) {
      const frame: WireRecord = { ...item.frame, frame_id: generateId(), lease_id: leaseId };
      const bytes = serializedLifecycleBytes(frame);
      totalBytes += bytes;
      if (bytes > LIFECYCLE_JOURNAL_MAX_ITEM_BYTES || totalBytes > LIFECYCLE_JOURNAL_MAX_BYTES) {
        failLifecycleClosed();
        return;
      }
      const eventId = frame.event_id ?? generateId();
      rebound.push([eventId, { frame: { ...frame, event_id: eventId }, bytes, enqueuedAt: item.enqueuedAt }]);
    }
    lifecycleJournal.clear();
    for (const [eventId, item] of rebound) lifecycleJournal.set(eventId, item);
  }

  function flushLifecycleJournal() {
    const currentSocket = socket;
    if (currentSocket === undefined || !socketOpen(currentSocket) || !lifecycleReady || lifecycleConnectionDisabled) return;
    const entries = [...lifecycleJournal.values()];
    // A replacement lease must see the newest complete assertion first. Sending an older
    // snapshot first could authoritatively omit a child that the producer already knows runs.
    const repair = entries
      .filter((item) => item.frame.type === "subagent_lifecycle_snapshot")
      .sort((left, right) => (right.frame.client_sequence ?? 0) - (left.frame.client_sequence ?? 0))[0];
    const pending = repair === undefined
      ? entries.sort((left, right) => (left.frame.client_sequence ?? 0) - (right.frame.client_sequence ?? 0))
      : [repair, ...entries.filter((item) => item !== repair)
        .sort((left, right) => (left.frame.client_sequence ?? 0) - (right.frame.client_sequence ?? 0))];
    for (const item of pending) {
      lifecycleTrace("client_lifecycle_publish_attempt", lifecycleFrameTrace(item.frame, "flush"));
      try {
        currentSocket.send(JSON.stringify(item.frame));
        lifecycleTrace("client_lifecycle_published", lifecycleFrameTrace(item.frame, "flush"));
      } catch (error) {
        lifecycleTrace("client_lifecycle_publish_error", { ...lifecycleFrameTrace(item.frame, "flush"), error_name: errorName(error) });
        throw error;
      }
    }
  }

  function lifecycleSnapshot(subagents: unknown): string | undefined {
    lifecycleSnapshotChildren = normalizeSubagents(subagents);
    if (!lifecycleCanJournal()) {
      lifecycleTrace("client_lifecycle_publish_deferred", {
        frame_type: "subagent_lifecycle_snapshot", snapshot_count: lifecycleSnapshotChildren.length,
        subagents: lifecycleSnapshotChildren, ready: lifecycleReady, available: lifecycleSupported,
      });
      return undefined;
    }
    const frame = journalLifecycle(lifecycleSnapshotFields(), lifecycleSnapshotChildren);
    if (frame !== undefined) flushLifecycleJournal();
    return frame?.event_id;
  }

  function lifecycleEvent({ lifecycleEvent, subagent, terminalReason, snapshot }: { lifecycleEvent: string; subagent: WireRecord; terminalReason?: unknown; snapshot: unknown[] }): string | undefined {
    if (!['started', 'terminal'].includes(lifecycleEvent) || subagent === undefined) throw new TypeError("a lifecycle event and child are required");
    const child = normalizeSubagentSummary(subagent);
    if (child === undefined) throw new TypeError("a safe lifecycle child is required");
    if (!Array.isArray(snapshot)) throw new TypeError("a complete lifecycle snapshot is required");
    lifecycleSnapshotChildren = normalizeSubagents(snapshot);
    if (!lifecycleCanJournal()) {
      lifecycleTrace("client_lifecycle_publish_deferred", {
        frame_type: "subagent_lifecycle_event", snapshot_count: lifecycleSnapshotChildren.length,
        subagent_count: 1, subagents: [child], ready: lifecycleReady, available: lifecycleSupported,
      });
      return undefined;
    }
    const frame = journalLifecycle({
      type: "subagent_lifecycle_event", agent_id: agentId, instance_id: instanceId,
      parent_session_id: parentSessionId, lease_id: leaseId, producer_epoch: producerEpoch,
      client_sequence: ++lifecycleSequence, event_id: generateId(), lifecycle_event: lifecycleEvent,
      subagent: child, ...(lifecycleEvent === "terminal" ? { terminal_reason: normalizeTerminalReason(terminalReason) } : {}),
    }, lifecycleSnapshotChildren);
    if (frame !== undefined) flushLifecycleJournal();
    return frame?.event_id;
  }

  function activity(activityState: string, { subagentCount = 0, subagents, goal, currentModel }: { subagentCount?: number; subagents?: unknown; goal?: unknown; currentModel?: unknown } = {}): void {
    if (!["active", "settled", "compacting"].includes(activityState)) {
      throw new TypeError("unsupported mailbox activity state");
    }
    const summaries = normalizeSubagents(subagents);
    const count = Math.max(summaries.length, subagentCount);
    if (!Number.isSafeInteger(count) || count < 0) {
      throw new TypeError("subagentCount must be a non-negative safe integer");
    }
    const clampedCount = Math.min(count, MAX_SUBAGENTS);
    const frameId = send({
      type: "activity",
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
      activity_state: activityState,
      subagent_count: clampedCount,
      ...(summaries.length > 0 || clampedCount === 0 ? { subagents: summaries } : {}),
      ...(normalizeGoalProjection(goal) === undefined ? {} : { goal: normalizeGoalProjection(goal) }),
      ...(!modelCatalogSupported || normalizeModelSummary(currentModel) === undefined
        ? {}
        : { current_model: normalizeModelSummary(currentModel) }),
    });
    // Activity has no router receipt/ack contract. Record that explicitly rather
    // than implying durable delivery from a successful local socket write.
    lifecycleTrace("client_activity_published", {
      frame_type: "activity", frame_id: frameId, lease_id: leaseId, activity_state: activityState,
      subagent_count: clampedCount, receipt_state: "not_supported", connection_generation: connectionGeneration,
    });
    lifecycleTrace("client_activity_receipt_unavailable", {
      frame_type: "activity", frame_id: frameId, lease_id: leaseId, activity_state: activityState,
      receipt_state: "not_supported", connection_generation: connectionGeneration,
    });
  }

  function modelCatalogFrame(modelSummaries: ModelSummary[], totalCount = modelSummaries.length, currentModel: unknown = undefined): ModelCatalogFrame {
    return {
      version: "v1",
      frame_id: generateId(),
      type: "model_catalog",
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
      model_summaries: modelSummaries,
      total_count: totalCount,
      truncated: modelSummaries.length < totalCount,
      ...(normalizeModelSummary(currentModel) === undefined
        ? {}
        : { current_model: normalizeModelSummary(currentModel) }),
    };
  }

  function fitModelCatalogFrame(modelSummaries: ModelSummary[], currentModel: unknown, totalCount = modelSummaries.length): { frame: ModelCatalogFrame; serialized: string; signature: string } | undefined {
    const frame = modelCatalogFrame([], totalCount, currentModel);
    const serialize = (count: number): string => {
      frame.model_summaries = modelSummaries.slice(0, count);
      frame.truncated = count < frame.total_count;
      return JSON.stringify(frame);
    };
    if (Buffer.byteLength(serialize(0), "utf8") > modelCatalogFrameBytes) return undefined;
    let low = 0;
    let high = modelSummaries.length;
    while (low < high) {
      const middle = Math.ceil((low + high) / 2);
      if (Buffer.byteLength(serialize(middle), "utf8") <= modelCatalogFrameBytes) low = middle;
      else high = middle - 1;
    }
    const serialized = serialize(low);
    return {
      frame,
      serialized,
      signature: JSON.stringify({
        model_summaries: frame.model_summaries,
        total_count: frame.total_count,
        truncated: frame.truncated,
        current_model: frame.current_model,
      }),
    };
  }

  function publishModelCatalog({ force = false } = {}) {
    const currentSocket = socket;
    if (currentSocket === undefined || !modelCatalogSupported || !socketOpen(currentSocket) || leaseId === undefined) return false;
    let catalog;
    let currentModel;
    try {
      catalog = getModelCatalog();
      currentModel = getCurrentModel();
    } catch (error) {
      modelControlTrace("client_model_catalog_read_failed", { error_name: errorName(error) });
      return false;
    }
    if (!Array.isArray(catalog)) {
      modelControlTrace("client_model_catalog_read_failed", { error_name: "TypeError" });
      return false;
    }
    const sourceSummaries = normalizeModelSummaries(catalog, 1_000_000);
    const summaries = sourceSummaries.slice(0, MAX_MODELS);
    const fitted = fitModelCatalogFrame(summaries, currentModel, sourceSummaries.length);
    if (fitted === undefined) {
      modelControlTrace("client_model_catalog_publish_failed", { reason: "frame_budget_too_small", frame_bytes: modelCatalogFrameBytes });
      return false;
    }
    const sourceSignature = modelCatalogSourceSignature(fitted.signature, sourceSummaries);
    if (!force && sourceSignature === lastPublishedModelCatalog) {
      return false;
    }
    try {
      currentSocket.send(fitted.serialized);
    } catch (error) {
      modelControlTrace("client_model_catalog_publish_failed", {
        reason: socketOpen(socket) ? "socket_send_failed" : "client_not_connected",
        error_name: errorName(error),
      });
      return false;
    }
    lastPublishedModelCatalog = sourceSignature;
    modelControlTrace("client_model_catalog_published", {
      model_count: fitted.frame.model_summaries.length,
      source_model_count: sourceSummaries.length,
      bytes: Buffer.byteLength(fitted.serialized, "utf8"),
      truncated: fitted.frame.model_summaries.length < sourceSummaries.length,
    });
    return true;
  }

  function ensureModelCatalogTimer() {
    if (!modelCatalogSupported || modelCatalogTimer !== undefined) return;
    modelCatalogTimer = setIntervalImpl(() => {
      try {
        publishModelCatalog();
      } catch (error) {
        modelControlTrace("client_model_catalog_publish_failed", {
          reason: "unexpected_error",
          error_name: errorName(error),
        });
      }
    }, MODEL_CATALOG_REFRESH_INTERVAL_MS);
  }

  function modelControlContextIsCurrent(context: ControlContext): boolean {
    return socket === context.socket
      && leaseId === context.leaseId
      && socketOpen(context.socket)
      && context.expiresAtMs > now();
  }

  async function receiveModelControl(frame: ControlFrame, context: ControlContext): Promise<void> {
    if (!modelControlContextIsCurrent(context)) {
      modelControlTrace("client_model_control_dropped", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: context.expiresAtMs <= now() ? "expired" : "stale_connection",
      });
      return;
    }
    modelControlTrace("client_model_control_received", {
      lease_id: context.leaseId,
      control_id: frame.control_id,
      source_agent_id: frame.source_agent_id,
      requested_provider: frame.model?.provider,
      requested_model_id: frame.model?.id,
      action: frame.action,
    });
    let result;
    try {
      result = await onModelControl({
        controlId: frame.control_id,
        sourceAgentId: frame.source_agent_id,
        action: frame.action,
        model: normalizeModelSummary(frame.model),
      });
    } catch (error) {
      modelControlTrace("client_model_control_callback_failed", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        error_name: errorName(error),
      });
      result = { accepted: false, errorCode: "model_switch_failed" };
    }
    const normalizedResult = normalizeModelControlResult(result);
    const currentModel = normalizedResult.currentModel;
    publishModelCatalog({ force: true });
    if (!modelControlContextIsCurrent(context)) {
      modelControlTrace("client_model_control_result_failed", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: context.expiresAtMs <= now() ? "expired" : "stale_connection",
      });
      return;
    }
    try {
      const currentSocket = context.socket;
      if (currentSocket === undefined) return;
      currentSocket.send(JSON.stringify({
        version: "v1",
        frame_id: generateId(),
        type: "model_control_result",
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: context.leaseId,
        source_agent_id: frame.source_agent_id,
        control_id: frame.control_id,
        accepted: normalizedResult.accepted,
        ...(normalizedResult.errorCode === undefined ? {} : { error_code: normalizedResult.errorCode }),
        ...(currentModel === undefined ? {} : { current_model: currentModel }),
      }));
      modelControlTrace("client_model_control_result_sent", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        accepted: normalizedResult.accepted,
        ...(normalizedResult.errorCode === undefined ? {} : { error_code: normalizedResult.errorCode }),
        ...(currentModel?.provider === undefined ? {} : { current_provider: currentModel.provider }),
        ...(currentModel?.id === undefined ? {} : { current_model_id: currentModel.id }),
      });
    } catch (error) {
      modelControlTrace("client_model_control_result_failed", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: socketOpen(context.socket) ? "socket_send_failed" : "client_not_connected",
        error_name: errorName(error),
      });
    }
  }

  async function drainModelControlQueue() {
    if (modelControlProcessing) return;
    modelControlProcessing = true;
    try {
      while (modelControlQueue.length > 0) {
        const pending = modelControlQueue.shift();
        if (pending === undefined) continue;
        await receiveModelControl(pending.frame, pending.context);
      }
    } finally {
      modelControlProcessing = false;
      if (modelControlQueue.length > 0) void drainModelControlQueue();
    }
  }

  function enqueueModelControl(frame: ControlFrame): void {
    const parsedExpiry = Date.parse(frame.expires_at ?? "");
    const context = {
      socket,
      leaseId,
      expiresAtMs: Number.isFinite(parsedExpiry) ? parsedExpiry : now() + DEFAULT_MODEL_CONTROL_TTL_MS,
    };
    if (!modelControlContextIsCurrent(context)) {
      modelControlTrace("client_model_control_dropped", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: "stale_connection",
      });
      return;
    }
    if (modelControlQueue.length >= MAX_QUEUED_MODEL_CONTROLS) {
      modelControlTrace("client_model_control_dropped", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: "queue_capacity",
      });
      return;
    }
    modelControlQueue.push({ frame, context });
    void drainModelControlQueue().catch((error) => {
      modelControlTrace("client_model_control_processing_failed", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        error_name: errorName(error),
      });
    });
  }

  function dropQueuedModelControls(currentSocket: MailboxSocket, reason: string): void {
    for (let index = modelControlQueue.length - 1; index >= 0; index -= 1) {
      const pending = modelControlQueue[index];
      if (pending.context.socket !== currentSocket) continue;
      modelControlQueue.splice(index, 1);
      modelControlTrace("client_model_control_dropped", {
        lease_id: pending.context.leaseId,
        control_id: pending.frame.control_id,
        source_agent_id: pending.frame.source_agent_id,
        reason,
      });
    }
  }

  function readGoalState(): GoalState | undefined {
    try {
      const normalized = normalizeGoalState(getGoalState());
      if (normalized !== undefined) {
        latestGoalState = normalized;
        return normalized;
      }
      goalControlTrace("client_goal_state_read_failed", { error_name: "TypeError" });
    } catch (error) {
      goalControlTrace("client_goal_state_read_failed", { error_name: errorName(error) });
    }
    return undefined;
  }

  function publishGoalState({ refresh = false }: { refresh?: boolean } = {}): boolean {
    if (refresh && latestGoalState === undefined) {
      readGoalState();
      if (latestGoalState === undefined) latestGoalState = defaultGoalState();
    }
    if (!goalControlSupported || !socketOpen(socket) || leaseId === undefined) return false;
    const state = latestGoalState ?? defaultGoalState();
    try {
      socket!.send(JSON.stringify({
        version: "v1",
        frame_id: generateId(),
        type: "goal_state",
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: leaseId,
        goal_state: goalStateToWire(state),
      }));
      goalControlTrace("client_goal_state_published", {});
      return true;
    } catch (error) {
      goalControlTrace("client_goal_state_publish_failed", {
        reason: socketOpen(socket) ? "socket_send_failed" : "client_not_connected",
        error_name: errorName(error),
      });
      return false;
    }
  }

  function goalControlContextIsCurrent(context: ControlContext): boolean {
    return context.leaseId !== undefined
      && socket === context.socket
      && leaseId === context.leaseId
      && socketOpen(context.socket)
      && context.expiresAtMs > now();
  }

  function reminderLockControlContextIsCurrent(frame: ReminderLockControlFrame, context: ControlContext): boolean {
    return context.leaseId !== undefined
      && frame.agent_id === agentId
      && frame.instance_id === instanceId
      && frame.lease_id === context.leaseId
      && socket === context.socket
      && leaseId === context.leaseId
      && socketOpen(context.socket)
      && context.expiresAtMs > now();
  }

  async function receiveReminderLockControl(frame: ReminderLockControlFrame, context: ControlContext): Promise<void> {
    if (!reminderLockControlContextIsCurrent(frame, context)) return;
    let result;
    try {
      result = await onReminderLockControl({
        controlId: frame.control_id,
        sourceAgentId: frame.source_agent_id,
        targetAgentId: frame.agent_id,
        targetInstanceId: frame.instance_id,
        leaseId: frame.lease_id,
        reminderId: frame.reminder_id,
        action: frame.action,
        expiresAt: frame.expires_at,
        isCurrent: () => reminderLockControlContextIsCurrent(frame, context),
      });
    } catch {
      result = { accepted: false, errorCode: "reminder_lock_failed" };
    }
    if (!reminderLockControlContextIsCurrent(frame, context)) return;
    const accepted = result?.accepted === true;
    const errorCode = typeof result?.errorCode === "string"
      && /^[a-z][a-z0-9_]{0,127}$/.test(result.errorCode)
      ? result.errorCode
      : "reminder_lock_failed";
    try {
      const currentSocket = context.socket;
      if (currentSocket === undefined) return;
      currentSocket.send(JSON.stringify({
        version: "v1",
        frame_id: generateId(),
        type: "reminder_lock_control_result",
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: context.leaseId,
        source_agent_id: frame.source_agent_id,
        control_id: frame.control_id,
        reminder_id: frame.reminder_id,
        accepted,
        ...(accepted ? {} : { error_code: errorCode }),
      }));
    } catch {
      // A lost connection fences the result and the router can discard the control.
    }
  }

  async function drainReminderLockControlQueue() {
    if (reminderLockControlProcessing) return;
    reminderLockControlProcessing = true;
    try {
      while (reminderLockControlQueue.length > 0) {
        const pending = reminderLockControlQueue.shift();
        if (pending === undefined) continue;
        await receiveReminderLockControl(pending.frame, pending.context);
      }
    } finally {
      reminderLockControlProcessing = false;
      if (reminderLockControlQueue.length > 0) void drainReminderLockControlQueue();
    }
  }

  function enqueueReminderLockControl(frame: ReminderLockControlFrame): void {
    const parsedExpiry = Date.parse(frame.expires_at ?? "");
    const context = { socket, leaseId, expiresAtMs: Number.isFinite(parsedExpiry) ? parsedExpiry : 0 };
    if (!reminderLockControlContextIsCurrent(frame, context)
      || reminderLockControlQueue.length >= MAX_QUEUED_REMINDER_LOCK_CONTROLS) return;
    reminderLockControlQueue.push({ frame, context });
    void drainReminderLockControlQueue();
  }

  function dropQueuedReminderLockControls(currentSocket: MailboxSocket): void {
    for (let index = reminderLockControlQueue.length - 1; index >= 0; index -= 1) {
      if (reminderLockControlQueue[index].context.socket === currentSocket) reminderLockControlQueue.splice(index, 1);
    }
  }

  async function receiveGoalControl(frame: GoalControlFrame, context: ControlContext): Promise<void> {
    if (!goalControlContextIsCurrent(context)) {
      goalControlTrace("client_goal_control_dropped", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: context.expiresAtMs <= now() ? "expired" : "stale_connection",
      });
      return;
    }
    goalControlTrace("client_goal_control_received", {
      lease_id: context.leaseId,
      control_id: frame.control_id,
      source_agent_id: frame.source_agent_id,
      target_instance_id: frame.instance_id,
      revision: frame.revision,
      action: frame.action,
    });
    let result;
    try {
      result = await onGoalControl({
        controlId: frame.control_id,
        sourceAgentId: frame.source_agent_id,
        targetAgentId: frame.agent_id,
        targetInstanceId: frame.instance_id,
        revision: frame.revision,
        action: frame.action,
        goal: frame.goal,
        settings: goalSettingsFromWire(frame.settings),
      });
    } catch (error) {
      goalControlTrace("client_goal_control_callback_failed", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        error_name: errorName(error),
      });
      result = { accepted: false, errorCode: "goal_control_failed" };
    }
    const accepted = result?.accepted === true;
    const errorCode = typeof result?.errorCode === "string"
      && /^[a-z][a-z0-9_]{0,127}$/.test(result.errorCode.trim())
      ? result.errorCode.trim()
      : "goal_control_failed";
    let state = normalizeGoalState(result?.goalState);
    if (state === undefined) state = readGoalState();
    if (state === undefined) state = latestGoalState ?? defaultGoalState();
    latestGoalState = state;
    if (!goalControlContextIsCurrent(context)) {
      goalControlTrace("client_goal_control_result_failed", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: context.expiresAtMs <= now() ? "expired" : "stale_connection",
      });
      return;
    }
    try {
      const currentSocket = context.socket;
      if (currentSocket === undefined) return;
      currentSocket.send(JSON.stringify({
        version: "v1",
        frame_id: generateId(),
        type: "goal_control_result",
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: context.leaseId,
        source_agent_id: frame.source_agent_id,
        control_id: frame.control_id,
        revision: frame.revision,
        accepted,
        ...(accepted ? {} : { error_code: errorCode }),
        goal_state: goalStateToWire(state),
      }));
      goalControlTrace("client_goal_control_result_sent", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        accepted,
        revision: frame.revision,
      });
    } catch (error) {
      goalControlTrace("client_goal_control_result_failed", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: socketOpen(context.socket) ? "socket_send_failed" : "client_not_connected",
        error_name: errorName(error),
      });
    }
  }

  async function drainGoalControlQueue() {
    if (goalControlProcessing) return;
    goalControlProcessing = true;
    try {
      while (goalControlQueue.length > 0) {
        const pending = goalControlQueue.shift();
        if (pending === undefined) continue;
        await receiveGoalControl(pending.frame, pending.context);
      }
    } finally {
      goalControlProcessing = false;
      if (goalControlQueue.length > 0) void drainGoalControlQueue();
    }
  }

  function enqueueGoalControl(frame: GoalControlFrame): void {
    const parsedExpiry = Date.parse(frame.expires_at ?? "");
    const context = {
      socket,
      leaseId,
      expiresAtMs: Number.isFinite(parsedExpiry) ? parsedExpiry : now() + DEFAULT_GOAL_CONTROL_TTL_MS,
    };
    if (!goalControlContextIsCurrent(context)) {
      goalControlTrace("client_goal_control_dropped", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: "stale_connection",
      });
      return;
    }
    if (goalControlQueue.length >= MAX_QUEUED_GOAL_CONTROLS) {
      goalControlTrace("client_goal_control_dropped", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        reason: "queue_capacity",
      });
      return;
    }
    goalControlQueue.push({ frame, context });
    void drainGoalControlQueue().catch((error) => {
      goalControlTrace("client_goal_control_processing_failed", {
        lease_id: context.leaseId,
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        error_name: errorName(error),
      });
    });
  }

  function dropQueuedGoalControls(currentSocket: MailboxSocket, reason: string): void {
    for (let index = goalControlQueue.length - 1; index >= 0; index -= 1) {
      const pending = goalControlQueue[index];
      if (pending.context.socket !== currentSocket) continue;
      goalControlQueue.splice(index, 1);
      goalControlTrace("client_goal_control_dropped", {
        lease_id: pending.context.leaseId,
        control_id: pending.frame.control_id,
        source_agent_id: pending.frame.source_agent_id,
        reason,
      });
    }
  }

  function messageArrival(message: WireRecord): void {
    const sourceKind = message.source?.kind;
    if (typeof message.id !== "string" || typeof message.from !== "string" || typeof message.to !== "string"
      || typeof sourceKind !== "string" || !["agent", "discord", "reminder"].includes(sourceKind) || message.from === agentId) {
      return;
    }
    send({
      type: "message_arrival",
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
      from: message.from,
      ...(typeof message.subject === "string" ? { subject: message.subject } : {}),
      source: { kind: sourceKind },
      preview: arrivalPreview(message.body),
      body: String(message.body ?? ""),
      created_at: message.created_at,
      message_id: message.id,
    });
  }

  function receipt(type: string, delivery: unknown, reason?: string): void {
    if (!isDelivery(delivery)) {
      throw new TypeError("a valid delivery receipt is required");
    }
    if (!["injection_requested", "ack", "nack"].includes(type)) {
      throw new TypeError("unsupported mailbox delivery receipt");
    }
    send({
      type,
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
      message_id: delivery.messageId,
      attempt_number: delivery.attemptNumber,
      ...(delivery.items === undefined || delivery.items.length <= 1 ? {} : {
        delivery_items: delivery.items.map((item) => ({
          message_id: item.messageId,
          message_incarnation_id: item.messageIncarnationId,
          attempt_number: item.attemptNumber,
          delivery_expires_at: item.deliveryExpiresAt,
        })),
      }),
      ...(type === "nack" ? { reason } : {}),
    });
  }

  function clearConnectionTimers() {
    clearIntervalImpl(renewTimer);
    renewTimer = undefined;
    clearIntervalImpl(modelCatalogTimer);
    modelCatalogTimer = undefined;
    if (reconnectTimer !== undefined) clearTimeoutImpl(reconnectTimer);
    reconnectTimer = undefined;
    if (capabilityRetryTimer !== undefined) clearTimeoutImpl(capabilityRetryTimer);
    capabilityRetryTimer = undefined;
    capabilityRetryAttempt = 0;
  }

  function reportKickstartFailure(error: unknown): void {
    if (stopped) return;
    log(`MECHA_V2_FRAME kickstart_failed agent=${agentId} error=${errorMessage(error)}`);
    try {
      onKickstartFailure(error);
    } catch {
      // Failure diagnostics cannot change reconnect behavior.
    }
  }

  function scheduleReconnect({ kickstartRouter = false }: { kickstartRouter?: boolean } = {}): void {
    if (stopped || paused || reconnectTimer !== undefined || socket !== undefined) return;
    if (kickstartRouter) {
      try {
        Promise.resolve(kickstart()).catch(reportKickstartFailure);
      } catch (error) {
        reportKickstartFailure(error);
      }
    }
    const cap = Math.min(
      reconnectMaxDelayMs,
      reconnectInitialDelayMs * (2 ** Math.min(reconnectAttempt, 30)),
    );
    const jitter = Math.max(0, Math.min(1, Number(random()) || 0));
    const delay = Math.max(1, Math.floor(cap * (0.5 + (jitter / 2))));
    reconnectAttempt += 1;
    lifecycleTrace("client_lifecycle_reconnect_scheduled", { attempt: reconnectAttempt, phase: kickstartRouter ? "kickstart" : "socket" });
    report("reconnecting");
    reconnectTimer = setTimeoutImpl(() => {
      reconnectTimer = undefined;
      openSocket();
    }, delay);
  }

  function disconnected(currentSocket: MailboxSocket, error?: unknown, { kickstartRouter = false }: { kickstartRouter?: boolean } = {}): void {
    if (socket !== currentSocket) return;
    dropQueuedModelControls(currentSocket, "disconnected");
    dropQueuedGoalControls(currentSocket, "disconnected");
    dropQueuedReminderLockControls(currentSocket);
    try { onReminderLockReset(); } catch { /* local lock cleanup cannot alter reconnect behavior */ }
    clearIntervalImpl(renewTimer);
    renewTimer = undefined;
    clearIntervalImpl(modelCatalogTimer);
    modelCatalogTimer = undefined;
    if (capabilityRetryTimer !== undefined) clearTimeoutImpl(capabilityRetryTimer);
    capabilityRetryTimer = undefined;
    capabilityRetryAttempt = 0;
    socket = undefined;
    leaseId = undefined;
    lifecycleSupported = false;
    lifecycleReady = false;
    lifecycleConnectionDisabled = false;
    modelCatalogSupported = false;
    goalControlSupported = false;
    capabilitiesSocket = undefined;
    lifecycleTrace("client_lifecycle_disconnected", { connected: false, error_name: errorName(error), phase: kickstartRouter ? "kickstart" : "socket" });
    if (stopped) return;
    report("disconnected", error);
    if (paused) return;
    scheduleReconnect({ kickstartRouter });
  }

  function closeSocket(currentSocket: MailboxSocket): void {
    if (currentSocket.readyState === (WebSocket?.OPEN ?? 1)
      || currentSocket.readyState === (WebSocket?.CONNECTING ?? 0)) {
      try {
        currentSocket.close();
      } catch {
        // The reconnect path is already active.
      }
    }
  }

  function scheduleCapabilityRetry(currentSocket: MailboxSocket): void {
    if (socket !== currentSocket || stopped || paused || capabilityRetryTimer !== undefined) return;
    const delay = Math.min(
      CAPABILITY_RETRY_MAX_DELAY_MS,
      CAPABILITY_RETRY_INITIAL_DELAY_MS * (2 ** Math.min(capabilityRetryAttempt, 5)),
    );
    capabilityRetryAttempt += 1;
    modelControlTrace("client_model_catalog_capability_retry_scheduled", {
      attempt: capabilityRetryAttempt,
    });
    goalControlTrace("client_goal_control_capability_retry_scheduled", {
      attempt: capabilityRetryAttempt,
    });
    capabilityRetryTimer = setTimeoutImpl(() => {
      capabilityRetryTimer = undefined;
      if (socket === currentSocket && !stopped && !paused) void discoverCapabilities(currentSocket);
    }, delay);
  }

  async function discoverCapabilities(currentSocket: MailboxSocket): Promise<void> {
    lifecycleTrace("client_lifecycle_capability_lookup", { phase: "started" });
    modelControlTrace("client_model_catalog_capability_lookup", { phase: "started" });
    goalControlTrace("client_goal_control_capability_lookup", { phase: "started" });
    try {
      const response = await fetchImpl(`${routerHttpUrl(routerUrl)}/v1/capabilities`, { signal: AbortSignal.timeout(capabilityTimeoutMs) });
      if (!response.ok && response.status !== 404) {
        throw new Error(`Capability discovery failed (${response.status ?? "unknown"})`);
      }
      const capabilitiesValue: unknown = response.ok ? await response.json() : undefined;
      const capabilities = isRecord(capabilitiesValue) ? capabilitiesValue : {};
      if (socket !== currentSocket || stopped) return;
      if (capabilityRetryTimer !== undefined) clearTimeoutImpl(capabilityRetryTimer);
      capabilityRetryTimer = undefined;
      capabilityRetryAttempt = 0;
      lifecycleSupported = capabilities?.subagent_lifecycle_v2 === true;
      lifecycleTrace("client_lifecycle_capability_result", { available: lifecycleSupported, ready: response.ok });
      modelCatalogSupported = capabilities?.model_catalog_v1 === true;
      goalControlSupported = capabilities?.goal_control_v1 === true;
      goalControlTrace("client_goal_control_capability_result", {
        available: goalControlSupported,
        ready: response.ok,
      });
      const modelCatalogLimits = capabilities.model_catalog_limits;
      const advertisedFrameBytes = isRecord(modelCatalogLimits) ? modelCatalogLimits.frame_bytes : undefined;
      modelCatalogFrameBytes = typeof advertisedFrameBytes === "number"
        && Number.isSafeInteger(advertisedFrameBytes) && advertisedFrameBytes > 0
        ? Math.min(advertisedFrameBytes, MAX_MODEL_CATALOG_FRAME_BYTES)
        : MAX_MODEL_CATALOG_FRAME_BYTES;
      modelControlTrace("client_model_catalog_capability_result", {
        available: modelCatalogSupported,
        ready: response.ok,
        frame_bytes: modelCatalogFrameBytes,
      });
      if (modelCatalogSupported) {
        publishModelCatalog({ force: true });
        ensureModelCatalogTimer();
      }
      if (goalControlSupported) publishGoalState({ refresh: true });
      if (lifecycleSupported) {
        lifecycleConnectionDisabled = false;
        lifecycleTrace("client_lifecycle_sync_enable_attempt", { router_version: lifecycleRouterVersion });
        send({ type: "subagent_sync_enable", agent_id: agentId, instance_id: instanceId, lease_id: leaseId,
          ...(lifecycleRouterVersion > 0 ? { after_version: lifecycleRouterVersion } : {}) });
      }
    } catch (error) {
      lifecycleTrace("client_lifecycle_capability_error", { error_name: errorName(error) });
      modelControlTrace("client_model_catalog_capability_error", { error_name: errorName(error) });
      goalControlTrace("client_goal_control_capability_error", { error_name: errorName(error) });
      if (socket === currentSocket && !stopped) {
        lifecycleSupported = false;
        modelCatalogSupported = false;
        goalControlSupported = false;
        scheduleCapabilityRetry(currentSocket);
      }
    }
  }

  async function receive(frame: WireRecord): Promise<void> {
    if (frame.type === "registered" && frame.agent_id === agentId && frame.instance_id === instanceId) {
      leaseId = frame.lease_id;
      lifecycleTrace("client_lifecycle_registered", {
        connected: true, pending: lifecycleJournal.size, lease_id: leaseId,
        connection_generation: connectionGeneration,
      });
      rebindLifecycleJournalLease();
      reconnectAttempt = 0;
      log(`MECHA_V2_FRAME registered agent=${agentId} lease=${leaseId}`);
      if (renewTimer === undefined) {
        renewTimer = setIntervalImpl(() => {
          try {
            send({ type: "lease_renew", agent_id: agentId, instance_id: instanceId, lease_id: leaseId });
          } catch (error) {
            log(`MECHA_V2_FRAME lease_renew_failed agent=${agentId} error=${errorMessage(error)}`);
          }
        }, renewIntervalMs);
      }
      if (resolveInitialConnection !== undefined) {
        resolveInitialConnection();
        resolveInitialConnection = undefined;
        rejectInitialConnection = undefined;
      }
      report("connected");
      if (capabilitiesSocket !== socket) {
        const currentSocket = socket;
        if (currentSocket === undefined) return;
        capabilitiesSocket = currentSocket;
        void discoverCapabilities(currentSocket);
      }
      return;
    }
    if (frame.type === "subagent_state_snapshot" || frame.type === "subagent_state_replay") {
      lifecycleRouterVersion = Math.max(lifecycleRouterVersion, frame.router_version ?? 0);
      lifecycleTrace("client_lifecycle_router_snapshot", { frame_type: frame.type, router_version: lifecycleRouterVersion });
      if (lifecycleSupported && !lifecycleConnectionDisabled) {
        const reconnect = lifecycleJournal.size > 0;
        lifecycleReady = true;
        // This current assertion is first after every enablement/reconnect. It fences a
        // replacement lease before any older retry can be replayed.
        lifecycleSnapshot(lifecycleSnapshotChildren);
        lifecycleTrace("client_lifecycle_ready", { router_version: lifecycleRouterVersion, reconnect, pending: lifecycleJournal.size });
        try { onLifecycleReady({ routerVersion: lifecycleRouterVersion, reconnect }); } catch { /* local reporting cannot break mailbox delivery */ }
        flushLifecycleJournal();
      }
      return;
    }
    if (frame.type === "subagent_lifecycle_acknowledged") {
      const pending = [...lifecycleJournal.values()].find((item) => item.frame.frame_id === frame.request_id);
      if (pending === undefined
        || frame.event_id !== pending.frame.event_id
        || frame.client_sequence !== pending.frame.client_sequence
        || frame.producer_epoch !== pending.frame.producer_epoch
        || frame.agent_id !== agentId
        || frame.instance_id !== instanceId
        || frame.parent_session_id !== parentSessionId
        || frame.lease_id !== pending.frame.lease_id) {
        lifecycleTrace("client_lifecycle_ack_ignored", {
          request_id: frame.request_id, frame_id: frame.frame_id, event_id: frame.event_id,
          client_sequence: frame.client_sequence, producer_epoch: frame.producer_epoch,
          lease_id: frame.lease_id, router_version: frame.router_version ?? 0,
          reason: pending === undefined ? "unknown_request" : "identity_mismatch",
          connection_generation: connectionGeneration,
        });
        return;
      }
      // The router sends this correlated acknowledgment only after its transaction commits.
      lifecycleRouterVersion = Math.max(lifecycleRouterVersion, frame.router_version ?? 0);
      const eventId = pending.frame.event_id;
      if (eventId === undefined) return;
      lifecycleJournal.delete(eventId);
      lifecycleTrace("client_lifecycle_acknowledged", {
        frame_type: pending.frame.type, frame_id: pending.frame.frame_id, request_id: frame.request_id,
        ack_frame_id: frame.frame_id, event_id: frame.event_id, client_sequence: frame.client_sequence,
        producer_epoch: frame.producer_epoch, lease_id: frame.lease_id,
        disposition: frame.disposition, router_version: lifecycleRouterVersion,
        pending: lifecycleJournal.size, connection_generation: connectionGeneration,
      });
      return;
    }
    if (frame.type === "model_control") {
      if (isModelControlFrame(frame)) enqueueModelControl(frame);
      return;
    }
    if (frame.type === "goal_control") {
      if (isGoalControlFrame(frame)) enqueueGoalControl(frame);
      return;
    }
    if (frame.type === "reminder_lock_control") {
      if (isReminderLockControlFrame(frame)) enqueueReminderLockControl(frame);
      return;
    }
    if (frame.type !== "message") {
      if (frame.type === "error" || frame.type === "lease_rejected") {
        log(`MECHA_V2_FRAME ${frame.type} agent=${agentId} code=${frame.code ?? "unknown"}`);
      }
      return;
    }
    const message = frame.message;
    const attemptNumber = frame.attempt_number;
    if (!isMailboxMessage(message)
      || typeof attemptNumber !== "number" || !Number.isSafeInteger(attemptNumber) || attemptNumber <= 0) return;
    const deliveryItems = frame.delivery_items;
    const items = Array.isArray(deliveryItems)
      ? deliveryItems.map((item) => ({
        messageId: item.message_id,
        messageIncarnationId: item.message_incarnation_id,
        attemptNumber: item.attempt_number,
        deliveryExpiresAt: item.delivery_expires_at,
      }))
      : [{
        messageId: message.id,
        messageIncarnationId: frame.message_incarnation_id,
        attemptNumber,
        deliveryExpiresAt: frame.delivery_expires_at,
      }];
    const delivery: Delivery = {
      messageId: message.id,
      attemptNumber,
      items,
    };
    if (typeof envelopeHandler !== "function") return;
    try {
      await envelopeHandler(message, delivery);
    } catch (error) {
      try {
        receipt("nack", delivery, "injection_failed");
      } catch {
        // A lost connection leaves the lease for router timeout/replay.
      }
      log(`MECHA_V2_FRAME injection_failed agent=${agentId} message=${message.id} error=${errorMessage(error)}`);
    }
  }

  function openSocket(): void {
    if (stopped || paused || socket !== undefined) return;
    let currentSocket: MailboxSocket;
    let opened = false;
    try {
      currentSocket = webSocketFactory(routerWebSocketUrl(routerUrl));
      socket = currentSocket;
      connectionGeneration += 1;
      lifecycleTrace("client_lifecycle_connection_opening", { connection_generation: connectionGeneration });
      report("connecting");
    } catch (error) {
      report("disconnected", error);
      scheduleReconnect({ kickstartRouter: connectionRefused(error) });
      return;
    }
    currentSocket.addEventListener("open", () => {
      if (socket !== currentSocket || stopped) return;
      opened = true;
      try {
        send({ type: "register", agent_id: agentId, instance_id: instanceId, delivery_receipts: "delivery_items_v1" });
      } catch (error) {
        disconnected(currentSocket, error);
        closeSocket(currentSocket);
      }
    });
    currentSocket.addEventListener("message", (payload) => {
      if (socket !== currentSocket || stopped) return;
      let frame: WireRecord;
      try {
        const parsed: unknown = JSON.parse(socketPayloadText(payload.data));
        if (!isRecord(parsed)) throw new TypeError("Mailbox v2 router sent a non-object frame");
        frame = parsed;
      } catch {
        const error = new Error("Mailbox v2 router sent invalid JSON");
        disconnected(currentSocket, error);
        closeSocket(currentSocket);
        return;
      }
      void receive(frame).then(() => {
        if (frame.type === "lease_rejected"
          || (frame.type === "error" && typeof frame.code === "string" && FATAL_ROUTER_ERROR_CODES.has(frame.code))) {
          const error = new Error(`Mailbox v2 registration rejected (${frame.code ?? "unknown"})`);
          disconnected(currentSocket, error);
          closeSocket(currentSocket);
        }
      }).catch((error) => {
        modelControlTrace("client_receive_failed", {
          frame_type: frame.type,
          error_name: errorName(error),
        });
        log(`MECHA_V2_FRAME receive_failed agent=${agentId} type=${frame.type ?? "unknown"} error=${errorMessage(error) ?? "unknown"}`);
      });
    });
    currentSocket.addEventListener("error", (event) => {
      const error = event.error ?? new Error("Mailbox v2 socket error");
      disconnected(currentSocket, error, {
        kickstartRouter: !opened && connectionRefused(error),
      });
      closeSocket(currentSocket);
    }, { once: true });
    currentSocket.addEventListener("close", () => disconnected(currentSocket));
  }

  return {
    instanceId,
    parentSessionId,
    producerEpoch,
    connect() {
      if (initialConnection === undefined) {
        initialConnection = new Promise((resolve, reject) => {
          resolveInitialConnection = resolve;
          rejectInitialConnection = reject;
        });
      }
      if (!stopped) openSocket();
      return initialConnection;
    },
    sendReceipt: receipt,
    sendActivity: activity,
    sendGoalState(state: unknown) {
      const normalized = normalizeGoalState(state);
      if (normalized === undefined) throw new TypeError("a valid full goal state is required");
      latestGoalState = normalized;
      return publishGoalState();
    },
    sendLifecycleSnapshot: lifecycleSnapshot,
    sendLifecycleEvent: lifecycleEvent,
    requestLifecycleResync() {
      if (!lifecycleSupported || !lifecycleReady || leaseId === undefined) {
        lifecycleTrace("client_lifecycle_resync_deferred", { available: lifecycleSupported, ready: lifecycleReady, router_version: lifecycleRouterVersion });
        return false;
      }
      lifecycleTrace("client_lifecycle_resync_attempt", { router_version: lifecycleRouterVersion });
      send({ type: "subagent_state_resync", agent_id: agentId, instance_id: instanceId, lease_id: leaseId, after_version: lifecycleRouterVersion });
      return true;
    },
    lifecycleStatus() { return { supported: lifecycleSupported, ready: lifecycleReady, routerVersion: lifecycleRouterVersion, pending: lifecycleJournal.size, bytes: journalBytes() }; },
    currentLeaseId() { return leaseId; },
    sendMessageArrival: messageArrival,
    pause() {
      if (stopped || paused) return;
      paused = true;
      clearConnectionTimers();
      const currentSocket = socket;
      if (currentSocket !== undefined) {
        dropQueuedModelControls(currentSocket, "paused");
        dropQueuedGoalControls(currentSocket, "paused");
        dropQueuedReminderLockControls(currentSocket);
      }
      socket = undefined;
      leaseId = undefined;
      try { onReminderLockReset(); } catch { /* local lock cleanup cannot alter pause behavior */ }
      if (currentSocket !== undefined) closeSocket(currentSocket);
    },
    resume() {
      if (stopped || !paused) return initialConnection ?? Promise.resolve();
      paused = false;
      openSocket();
      return initialConnection ?? Promise.resolve();
    },
    async sendReply({ to, body, subject = "Pi mailbox reply" }: { to: string; body: string; subject?: string }) {
      if (typeof to !== "string" || to.length === 0 || to.includes("@")) throw new Error("a mailbox recipient is required");
      if (typeof body !== "string" || body.length === 0) throw new Error("a reply body is required");
      const response = await fetchImpl(`${routerHttpUrl(routerUrl)}/v1/messages`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        signal: AbortSignal.timeout(DEFAULT_REPLY_TIMEOUT_MS),
        body: JSON.stringify({
          id: generateId(),
          from: agentId,
          to,
          subject,
          body,
          created_at: new Date().toISOString(),
          source: { kind: "agent" },
        }),
      });
      const payload = await response.json();
      if (!response.ok || payload?.message?.state === undefined) {
        throw new Error(`Mailbox v2 router rejected reply (${payload?.error?.code ?? response.status})`);
      }
      log(`MECHA_V2_REPLY accepted from=${agentId} to=${to} message=${payload.message.id}`);
      return payload.message;
    },
    close() {
      if (stopped) return;
      stopped = true;
      clearConnectionTimers();
      const currentSocket = socket;
      if (currentSocket !== undefined) {
        dropQueuedModelControls(currentSocket, "closed");
        dropQueuedGoalControls(currentSocket, "closed");
        dropQueuedReminderLockControls(currentSocket);
      }
      socket = undefined;
      leaseId = undefined;
      try { onReminderLockReset(); } catch { /* local lock cleanup cannot alter shutdown behavior */ }
      if (rejectInitialConnection !== undefined) {
        rejectInitialConnection(new Error("Mailbox v2 client closed before connecting"));
        resolveInitialConnection = undefined;
        rejectInitialConnection = undefined;
      }
      if (currentSocket !== undefined) closeSocket(currentSocket);
    },
  };
}
