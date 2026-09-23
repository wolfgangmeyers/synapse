import { completeSimple } from "@earendil-works/pi-ai/compat";
import type { Api, AssistantMessage, Context, Model, SimpleStreamOptions } from "@earendil-works/pi-ai";

type AnyRecord = Record<string, unknown>;
type TimerHandle = ReturnType<typeof setTimeout>;
type GoalUi = {
  notify?: (message: string, level?: string) => unknown;
};
type GoalCompleteSimple = (
  model: Model<Api>,
  context: Context,
  options?: SimpleStreamOptions,
) => Promise<AssistantMessage>;
export type GoalModelRegistry = {
  getApiKeyAndHeaders?: (model: Model<Api>) => Promise<
    | { ok: true; apiKey?: string; headers?: Record<string, string>; env?: Record<string, string> }
    | { ok: false; error: string }
  >;
};
type GoalToolHandler = (toolCallId: unknown, params: AnyRecord, signal: AbortSignal | undefined, onUpdate: unknown, ctx: GoalContext) => unknown;
type GoalCommandHandler = (args: string, ctx: GoalContext) => unknown;
type GoalRecord = Record<string, unknown>;
type GoalControlSettings = {
  goalLockEnabled: boolean;
  snoozeEnabled: boolean;
  snoozeDurationMs: number;
  snoozeTurnLimit?: number;
  snoozeBudgetCapMs?: number;
  snoozeRefillYieldMs: number;
  snoozeRefillElapsedMs: number;
};
type Outcome = "COMPLETED" | "NOT_DONE";
type GoalAction = "set" | "status" | "off";
type GoalControlAction = "set" | "off";
type GoalState = { enabled: boolean; goal: string };
type OutcomeCounts = Record<Outcome, number>;
type RearmScope = {
  sessionId: string;
  goal: string;
  activityBaselineRevision: number;
  workerId?: string;
  producerEpoch?: string;
};
type AppliedGoalControl = { instanceId: string; revision: number };
export type GoalContext = {
  sessionManager: {
    getBranch?: () => AnyRecord[];
    getSessionId?: () => string | undefined;
  };
  model?: Model<Api>;
  modelRegistry?: GoalModelRegistry;
  ui?: GoalUi;
  [key: string]: unknown;
};
type GoalOptions = {
  completeSimple?: GoalCompleteSimple;
  clock?: { now?: () => number };
  setTimeout?: (callback: () => void, delay: number) => TimerHandle;
  clearTimeout?: (timer: TimerHandle | undefined) => void;
  timer?: {
    setTimeout?: (callback: () => void, delay: number) => TimerHandle;
    clearTimeout?: (timer: TimerHandle | undefined) => void;
  };
};
export type GoalPi = {
  getSessionId?: () => string | undefined;
  appendEntry: (...args: unknown[]) => void;
  sendMessage: (...args: unknown[]) => void;
  on: (event: string, handler: (event: AnyRecord, ctx: GoalContext) => unknown) => void;
  registerTool: (tool: { execute?: GoalToolHandler; [key: string]: unknown }) => void;
  registerCommand: (name: string, command: { handler?: GoalCommandHandler; [key: string]: unknown }) => void;
  events?: {
    on?: (event: string, handler: (value: AnyRecord) => unknown) => void;
    emit?: (...args: unknown[]) => void;
  };
};
type PendingSnooze = {
  signal?: AbortSignal;
  onAbort: () => void;
  onSettled?: (completed: boolean) => void;
  startedAt: number;
  timeout: TimerHandle | undefined;
};
type PendingEvaluation = { event: AnyRecord; ctx: GoalContext; runGeneration: number; goal: string };
type PendingSelfWake = { goal: string; ctx: GoalContext; runGeneration: number; timeout: TimerHandle | undefined };
type PendingSnoozeWakeup = {
  goal: string;
  runGeneration: number;
  wakeup: string;
  ctx: GoalContext;
  allowAfterDisable: boolean;
};
type GoalActionResult = { ok: boolean; message: string; details?: AnyRecord };
type RestoredState = GoalState & {
  notDoneWakeups: number;
  completedThings: number;
  goalLockEnabled: boolean;
  snoozeEnabled: boolean;
  snoozeDurationMs: number;
  snoozeTurnLimit?: number;
  snoozeBudgetCapMs?: number;
  snoozeBudgetAvailableMs: number;
  snoozeRefillYieldMs: number;
  snoozeRefillElapsedMs: number;
  outcomeCounts: OutcomeCounts;
  rearmActive: boolean;
  rearmActivityCount: number;
  rearmScope?: RearmScope;
  appliedGoalControl?: AppliedGoalControl;
};

const GOAL_STATE_ENTRY = "goal-state";
const GOAL_RESULT_TYPE = "goal-result";
const GOAL_UPDATE_TYPE = "goal-update";
const GOAL_SNOOZE_AVAILABILITY_TYPE = "goal-snooze-availability";
export const GOAL_STATE_EVENT = "goal:state";
export const GOAL_STATE_REQUEST_EVENT = "goal:state-request";
// Progress recovery is a separate local bridge. Mailbox liveness and evaluator output
// are deliberately not activity signals: only the messaging extension's authoritative,
// uniquely identified lifecycle revisions may advance this state machine.
export const GOAL_ACTIVITY_EVENT = "goal:meaningful-activity";
export const GOAL_WORKER_ID_EVENT = "goal:worker-id";
// Remote controls remain a separate typed channel so model selection and goal
// state cannot be confused by a router or extension that only supports one.
export const GOAL_CONTROL_REQUEST_EVENT = "goal:control-request";
export const GOAL_CONTROL_STATE_EVENT = "goal:control-state";
const MAX_GOAL_CHARS = 4_000;
const MAX_CONTEXT_MESSAGES = 32;
const MAX_CONTEXT_CHARS = 24_000;
const OUTCOMES = new Set<Outcome>(["COMPLETED", "NOT_DONE"]);
const GOAL_ACTIONS = new Set<GoalAction>(["set", "status", "off"]);
const GOAL_CONTROL_ACTIONS = new Set<GoalControlAction>(["set", "off"]);
const OUTCOME_COUNT_KEYS: readonly Outcome[] = ["COMPLETED", "NOT_DONE"];
const GOAL_PARAMETERS = {
  type: "object",
  additionalProperties: false,
  required: ["action"],
  properties: {
    action: { type: "string", enum: ["set", "status", "off"] },
    goal: { type: "string" },
  },
};

const MAX_RATIONALE_CHARS = 240;
const MAX_EVALUATOR_CALLS = 1_000_000;
const MAX_NOT_DONE_WAKEUPS = 1000;
const MAX_SNOOZE_TURN_LIMIT = 20;
const MEANINGFUL_ACTIVITY_EVENTS_TO_REARM = 3;
const MEANINGFUL_ACTIVITY_KINDS = new Set(["tool-completed", "native-subagent-launch"]);
// Keep this aligned with pi-subagents' bounded diagnostic-token contract.
const SAFE_DIAGNOSTIC_KIND_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
const MAX_SEEN_ACTIVITY_IDS = 512;
const COMPLETIONS_TO_RESET_WAKEUPS = 3;
const DEFAULT_SNOOZE_DURATION_MS = 60_000;
const DEFAULT_SNOOZE_REFILL_YIELD_MS = 30_000;
const DEFAULT_SNOOZE_REFILL_ELAPSED_MS = 60_000;
const EVALUATOR_PROMPT = "goal: <goal description> - return exactly two lines: VERDICT: COMPLETED|NOT_DONE and REASON: <concise rationale>";
const PI_SUBAGENTS_SERVICE_REGISTRY = Symbol.for("@gotgenes/pi-subagents:service-registry");
const DIAGNOSTIC_JOURNAL_GLOBAL = Symbol.for("mecha.pi.diagnostic-journal.v1");

type DiagnosticJournalGlobal = {
  version: 1;
  record: (source: string, event: string, fields?: object) => unknown;
};

const SAFE_DIAGNOSTIC_STRING_FIELDS = new Set([
  "session_id", "event_id", "worker_id", "producer_epoch", "state", "reason", "kind", "component", "error_name",
]);
const SAFE_DIAGNOSTIC_ID_FIELDS = new Set(["session_id", "event_id", "worker_id", "producer_epoch"]);
const SAFE_DIAGNOSTIC_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
const SAFE_DIAGNOSTIC_ERROR_NAMES = new Set([
  "AggregateError", "AbortError", "Error", "EvalError", "RangeError", "ReferenceError", "SyntaxError", "TypeError", "URIError",
]);
const SAFE_DIAGNOSTIC_NUMBER_FIELDS = new Set(["revision"]);
const SAFE_DIAGNOSTIC_BOOLEAN_FIELDS = new Set(["enabled", "available"]);

function safeGoalIdentity(value: unknown): string | undefined {
  return typeof value === "string" && SAFE_DIAGNOSTIC_ID_PATTERN.test(value) ? value : undefined;
}

function recordGoalDiagnostic(event: string, fields: object = {}): void {
  try {
    const journal = Object.getOwnPropertyDescriptor(globalThis, DIAGNOSTIC_JOURNAL_GLOBAL)?.value as DiagnosticJournalGlobal | undefined;
    if (journal?.version !== 1 || typeof journal.record !== "function") return;
    const safeFields: AnyRecord = {};
    if (fields !== null && typeof fields === "object" && !Array.isArray(fields)) {
      for (const [key, value] of Object.entries(fields)) {
        if (SAFE_DIAGNOSTIC_ID_FIELDS.has(key)) {
          const identity = safeGoalIdentity(value);
          if (identity !== undefined) safeFields[key] = identity;
        } else if (key === "kind") {
          if (typeof value === "string" && SAFE_DIAGNOSTIC_KIND_PATTERN.test(value)) safeFields[key] = value;
        } else if (key === "error_name") {
          if (typeof value === "string" && SAFE_DIAGNOSTIC_ERROR_NAMES.has(value)) safeFields[key] = value;
        } else if (SAFE_DIAGNOSTIC_STRING_FIELDS.has(key) && typeof value === "string" && value.length <= 128) safeFields[key] = value;
        else if (SAFE_DIAGNOSTIC_NUMBER_FIELDS.has(key) && typeof value === "number" && Number.isSafeInteger(value) && value >= 0) safeFields[key] = value;
        else if (SAFE_DIAGNOSTIC_BOOLEAN_FIELDS.has(key) && typeof value === "boolean") safeFields[key] = value;
      }
    }
    journal.record("mecha-goal", event, safeFields);
  } catch {
    // The shared journal is optional and diagnostics cannot affect goal behavior.
  }
}

function isRecord(value: unknown): value is AnyRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function normalizeGoal(value: unknown) {
  if (typeof value !== "string") return "";
  return value.trim().slice(0, MAX_GOAL_CHARS);
}

function isSafeNonnegativeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function isGoalControlAction(value: unknown): value is GoalControlAction {
  return value === "set" || value === "off";
}

function isGoalContext(value: unknown): value is GoalContext {
  return isRecord(value) && isRecord(value.sessionManager);
}

function isAbortSignal(value: unknown): value is AbortSignal {
  if (!isRecord(value) || typeof value.aborted !== "boolean" || typeof value.addEventListener !== "function") return false;
  return value.removeEventListener === undefined || typeof value.removeEventListener === "function";
}

function isServiceRegistry(value: unknown): value is { get: (sessionId: string) => unknown } {
  return isRecord(value) && typeof value.get === "function";
}

function isGoalOwnerService(value: unknown): value is { hasRunning: () => boolean } {
  return isRecord(value) && typeof value.hasRunning === "function";
}

function compactToolCallAction(toolCall: AnyRecord | undefined) {
  if (!toolCall || toolCall.type !== "toolCall" || toolCall.name !== "mecha_context") return false;
  let argumentsValue = toolCall.arguments;
  if (typeof argumentsValue === "string") {
    try { argumentsValue = JSON.parse(argumentsValue); } catch { return false; }
  }
  return isRecord(argumentsValue) && argumentsValue.action === "compact";
}

export function hasPendingCompaction(ctx: GoalContext) {
  const pendingToolCallIds = new Set();
  for (const entry of ctx?.sessionManager?.getBranch?.() ?? []) {
    const message = entry?.type === "message" && isRecord(entry.message) ? entry.message : undefined;
    if (message?.role === "assistant" && Array.isArray(message.content)) {
      for (const rawToolCall of message.content) {
        if (!isRecord(rawToolCall)) continue;
        if (compactToolCallAction(rawToolCall) && typeof rawToolCall.id === "string") pendingToolCallIds.add(rawToolCall.id);
      }
    } else if (message?.role === "toolResult" && typeof message.toolCallId === "string") {
      pendingToolCallIds.delete(message.toolCallId);
    }
  }
  return pendingToolCallIds.size > 0;
}

function parsePositiveDuration(value: unknown) {
  const match = typeof value === "string" ? value.trim().match(/^([1-9]\d*)([sm])$/) : undefined;
  if (!match) return undefined;
  const amount = Number(match[1]);
  const durationMs = amount * (match[2] === "m" ? 60_000 : 1_000);
  return Number.isSafeInteger(amount) && Number.isSafeInteger(durationMs) && durationMs > 0
    ? durationMs
    : undefined;
}

function snoozeDurationFromInput(value: unknown) {
  return parsePositiveDuration(value) ?? DEFAULT_SNOOZE_DURATION_MS;
}

function parseSnoozeTurnLimit(value: unknown) {
  const input = typeof value === "string" ? value.trim() : "";
  if (!/^\d+$/.test(input)) return undefined;
  const limit = Number(input);
  return Number.isInteger(limit) && limit >= 1 && limit <= MAX_SNOOZE_TURN_LIMIT ? limit : undefined;
}

function goalProjection(state: GoalState) {
  return state.enabled === true && state.goal.length > 0
    ? { enabled: true, goal: state.goal }
    : { enabled: false, goal: "" };
}

function emptyOutcomeCounts(): OutcomeCounts {
  return { COMPLETED: 0, NOT_DONE: 0 };
}

function restoreOutcomeCounts(value: unknown): OutcomeCounts {
  const counts = emptyOutcomeCounts();
  if (!isRecord(value)) return counts;
  const record = value;
  for (const outcome of OUTCOME_COUNT_KEYS) {
    if (isSafeNonnegativeInteger(record[outcome])) counts[outcome] = record[outcome];
  }
  return counts;
}

function restoreAppliedGoalControl(value: unknown): AppliedGoalControl | undefined {
  if (!isRecord(value)) return undefined;
  const record = value;
  return typeof record.instanceId === "string" && record.instanceId.length > 0
    && isSafeNonnegativeInteger(record.revision) && record.revision > 0
    ? { instanceId: record.instanceId, revision: record.revision }
    : undefined;
}

function restoreRearmScope(value: unknown): RearmScope | undefined {
  if (!isRecord(value)) return undefined;
  const record = value;
  if (typeof record.sessionId !== "string" || record.sessionId.length === 0
    || typeof record.goal !== "string" || normalizeGoal(record.goal).length === 0
    || !isSafeNonnegativeInteger(record.activityBaselineRevision)
    || (record.workerId !== undefined && (typeof record.workerId !== "string" || record.workerId.length === 0))
    || (record.producerEpoch !== undefined && (typeof record.producerEpoch !== "string" || record.producerEpoch.length === 0))) {
    return undefined;
  }
  return {
    sessionId: record.sessionId,
    goal: normalizeGoal(record.goal),
    activityBaselineRevision: record.activityBaselineRevision,
    ...(record.workerId === undefined ? {} : { workerId: record.workerId }),
    ...(record.producerEpoch === undefined ? {} : { producerEpoch: record.producerEpoch }),
  };
}

function positiveSafeInteger(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0 ? value : undefined;
}

function normalizeGoalControlSettings(value: unknown): GoalControlSettings | undefined {
  if (!isRecord(value)) return undefined;
  const record = value;
  const required = [
    "goalLockEnabled",
    "snoozeEnabled",
    "snoozeDurationMs",
    "snoozeTurnLimit",
    "snoozeBudgetCapMs",
    "snoozeRefillYieldMs",
    "snoozeRefillElapsedMs",
  ];
  if (Object.keys(record).some((key) => !required.includes(key))
    || required.some((key) => !Object.hasOwn(record, key))) return undefined;
  const snoozeTurnLimit = record.snoozeTurnLimit === null ? undefined : parseSnoozeTurnLimit(String(record.snoozeTurnLimit));
  const snoozeBudgetCapMs = record.snoozeBudgetCapMs === null ? undefined : positiveSafeInteger(record.snoozeBudgetCapMs);
  if (typeof record.goalLockEnabled !== "boolean" || typeof record.snoozeEnabled !== "boolean"
    || positiveSafeInteger(record.snoozeDurationMs) === undefined
    || (record.snoozeTurnLimit !== null && snoozeTurnLimit === undefined)
    || (record.snoozeBudgetCapMs !== null && snoozeBudgetCapMs === undefined)
    || positiveSafeInteger(record.snoozeRefillYieldMs) === undefined
    || positiveSafeInteger(record.snoozeRefillElapsedMs) === undefined) return undefined;
  const snoozeDurationMs = positiveSafeInteger(record.snoozeDurationMs);
  const snoozeRefillYieldMs = positiveSafeInteger(record.snoozeRefillYieldMs);
  const snoozeRefillElapsedMs = positiveSafeInteger(record.snoozeRefillElapsedMs);
  if (snoozeDurationMs === undefined || snoozeRefillYieldMs === undefined || snoozeRefillElapsedMs === undefined) return undefined;
  return {
    goalLockEnabled: record.goalLockEnabled,
    snoozeEnabled: record.snoozeEnabled,
    snoozeDurationMs,
    snoozeTurnLimit,
    snoozeBudgetCapMs,
    snoozeRefillYieldMs,
    snoozeRefillElapsedMs,
  };
}

function restoreState(ctx: GoalContext): RestoredState {
  let restored: RestoredState = {
    enabled: false,
    goal: "",
    notDoneWakeups: 0,
    completedThings: 0,
    goalLockEnabled: false,
    snoozeEnabled: true,
    snoozeDurationMs: DEFAULT_SNOOZE_DURATION_MS,
    snoozeTurnLimit: undefined,
    snoozeBudgetCapMs: undefined,
    snoozeBudgetAvailableMs: 0,
    snoozeRefillYieldMs: DEFAULT_SNOOZE_REFILL_YIELD_MS,
    snoozeRefillElapsedMs: DEFAULT_SNOOZE_REFILL_ELAPSED_MS,
    outcomeCounts: emptyOutcomeCounts(),
    rearmActive: false,
    rearmActivityCount: 0,
    rearmScope: undefined,
    appliedGoalControl: undefined,
  };
  for (const entry of ctx.sessionManager.getBranch?.() ?? []) {
    if (entry.type !== "custom" || entry.customType !== GOAL_STATE_ENTRY) continue;
    const data = entry.data;
    if (!isRecord(data)) continue;
    const snoozeBudgetCapMs = positiveSafeInteger(data.snoozeBudgetCapMs);
    restored = {
      enabled: data.enabled === true,
      goal: normalizeGoal(data.goal),
      notDoneWakeups: isSafeNonnegativeInteger(data.notDoneWakeups)
        ? Math.min(data.notDoneWakeups, MAX_SNOOZE_TURN_LIMIT)
        : 0,
      completedThings: isSafeNonnegativeInteger(data.completedThings)
        ? data.completedThings % COMPLETIONS_TO_RESET_WAKEUPS
        : 0,
      goalLockEnabled: data.goalLockEnabled === true,
      snoozeEnabled: !Object.hasOwn(data, "snoozeEnabled") || data.snoozeEnabled === true,
      snoozeDurationMs: positiveSafeInteger(data.snoozeDurationMs) ?? DEFAULT_SNOOZE_DURATION_MS,
      snoozeTurnLimit: isSafeNonnegativeInteger(data.snoozeTurnLimit) && data.snoozeTurnLimit >= 1
        && data.snoozeTurnLimit <= MAX_SNOOZE_TURN_LIMIT ? data.snoozeTurnLimit : undefined,
      snoozeBudgetCapMs,
      snoozeBudgetAvailableMs: snoozeBudgetCapMs !== undefined && typeof data.snoozeBudgetAvailableMs === "number" && Number.isFinite(data.snoozeBudgetAvailableMs)
        ? Math.min(snoozeBudgetCapMs, Math.max(0, data.snoozeBudgetAvailableMs))
        : snoozeBudgetCapMs ?? 0,
      snoozeRefillYieldMs: positiveSafeInteger(data.snoozeRefillYieldMs) ?? DEFAULT_SNOOZE_REFILL_YIELD_MS,
      snoozeRefillElapsedMs: positiveSafeInteger(data.snoozeRefillElapsedMs) ?? DEFAULT_SNOOZE_REFILL_ELAPSED_MS,
      outcomeCounts: restoreOutcomeCounts(data.outcomeCounts),
      rearmActive: data.rearmActive === true,
      rearmActivityCount: isSafeNonnegativeInteger(data.rearmActivityCount)
        ? Math.min(data.rearmActivityCount, MEANINGFUL_ACTIVITY_EVENTS_TO_REARM - 1)
        : 0,
      rearmScope: restoreRearmScope(data.rearmScope),
      appliedGoalControl: restoreAppliedGoalControl(data.appliedGoalControl),
    };
  }
  return restored;
}

function textFromMessage(message: AnyRecord | undefined) {
  if (!message || typeof message.role !== "string" || !["user", "assistant", "toolResult"].includes(message.role)) return "";
  if (typeof message.content === "string") return message.content;
  if (!Array.isArray(message.content)) return "";
  return message.content
    .filter((part): part is AnyRecord => isRecord(part) && part.type === "text" && typeof part.text === "string")
    .map((part) => typeof part.text === "string" ? part.text : "")
    .join("\n");
}

function boundedActiveContext(ctx: GoalContext) {
  const entries = ctx.sessionManager.getBranch?.() ?? [];
  const messages = [];
  let characters = 0;
  for (let index = entries.length - 1; index >= 0 && messages.length < MAX_CONTEXT_MESSAGES; index -= 1) {
    const entry = entries[index];
    if (entry.type !== "message") continue;
    const message = isRecord(entry.message) ? entry.message : undefined;
    const text = textFromMessage(message);
    if (!text || typeof message?.role !== "string") continue;
    const remaining = MAX_CONTEXT_CHARS - characters;
    if (remaining <= 0) break;
    const boundedText = text.slice(-remaining);
    messages.unshift({
      role: message.role,
      content: [{ type: "text", text: boundedText }],
      timestamp: typeof message.timestamp === "number" ? message.timestamp : Date.now(),
    });
    characters += boundedText.length;
  }
  return messages;
}

function responseText(response: unknown) {
  return isRecord(response) && Array.isArray(response.content)
    ? response.content
      .filter((part): part is AnyRecord => isRecord(part) && part.type === "text" && typeof part.text === "string")
      .map((part) => typeof part.text === "string" ? part.text : "")
      .join("")
    : "";
}

function responseEvaluation(response: unknown): { outcome: Outcome; rationale: string } {
  const lines = responseText(response).split(/\r?\n/);
  const verdict = lines.length === 2
    ? lines[0].match(/^VERDICT: (COMPLETED|NOT_DONE)$/)?.[1]
    : undefined;
  const rawReason = lines.length === 2 ? lines[1].match(/^REASON: (.*)$/)?.[1] : undefined;
  if ((verdict !== "COMPLETED" && verdict !== "NOT_DONE") || !rawReason || rawReason.trim().length === 0 || rawReason.length > MAX_RATIONALE_CHARS) {
    return { outcome: "NOT_DONE", rationale: "Invalid evaluator output: " + JSON.stringify(response) };
  }
  return {
    outcome: verdict,
    rationale: rawReason.trim().replace(/\s+/g, " "),
  };
}

function goalResultContent(wakeup: string, outcome: Outcome, rationale: string) {
  return `${wakeup}\nVERDICT: ${outcome}\nREASON: ${rationale}`;
}

function notDoneWakeup(goal: string, attempt: number) {
  const messages = [
    `NOT_DONE: Review ${goal} and take the next concrete action now.`,
    `NOT_DONE: Continue ${goal} by completing its next actionable step now.`,
    `NOT_DONE: Re-engage with ${goal} and deliver the next concrete result now.`,
    `NOT_DONE: Identify what remains for ${goal} and act on it now.`,
    `NOT_DONE: Advance ${goal} with a specific, verifiable next action now.`,
    `NOT_DONE: ${goal} still needs action—COMPLETE the next step now!`,
    `NOT_DONE: Return to ${goal}, address the current gap, and move it forward NOW.`,
    `NOT_DONE: Take ownership of ${goal} and complete its next concrete action now!`,
    `NOT_DONE: Do not leave ${goal} idle—resolve the next blocker and proceed NOW.`,
    `NOT_DONE: ${goal} requires visible progress: take the next actionable step now!`,
    `NOT_DONE: ${goal} is NOT_DONE. Resolve the highest-priority remaining item and act now.`,
    `NOT_DONE: Move ${goal} forward immediately by completing the next required action.`,
    `NOT_DONE: ${goal} remains incomplete—address the outstanding work and produce a result NOW.`,
    `NOT_DONE: Take direct action on ${goal} now; do not stop at planning or inspection.`,
    `NOT_DONE: ${goal} needs urgent progress. Complete the next concrete deliverable NOW!`,
    `NOT_DONE: ${goal} IS STILL INCOMPLETE. Take the next required action immediately!`,
    `NOT_DONE: ACT NOW on ${goal}: resolve the top remaining issue and report the concrete result.`,
    `NOT_DONE: ${goal} cannot remain stalled. COMPLETE THE NEXT ACTION NOW and make progress visible!`,
    `NOT_DONE: TAKE OWNERSHIP of ${goal} immediately—finish the highest-priority remaining step and verify it NOW!`,
    `NOT_DONE: ${goal} IS NOT COMPLETE. ACT NOW, DELIVER THE NEXT VERIFIED RESULT, AND ACCOUNT FOR THE REMAINING WORK!`,
  ];
  if (attempt > messages.length) attempt = messages.length;
  return messages[attempt - 1];
}

function responseOutcome(response: unknown) {
  return responseEvaluation(response).outcome;
}

function errorDetails(error: unknown) {
  if (error instanceof Error) return error.stack ?? `${error.name}: ${error.message}`;
  try {
    return JSON.stringify(error);
  } catch {
    return String(error);
  }
}

async function completeEvaluator(
  ctx: GoalContext,
  request: Context,
  signal: AbortSignal,
  evaluate: GoalCompleteSimple,
): Promise<unknown> {
  if (!ctx.model || ctx.modelRegistry?.getApiKeyAndHeaders === undefined) {
    throw new Error("Model registry cannot evaluate the active goal");
  }
  const auth = await ctx.modelRegistry.getApiKeyAndHeaders(ctx.model);
  if (!auth.ok) throw new Error(auth.error);
  return evaluate(ctx.model, request, {
    signal,
    apiKey: auth.apiKey,
    headers: auth.headers,
    env: auth.env,
  });
}

export { boundedActiveContext, responseEvaluation, responseOutcome };

export function createGoalExtension(pi: GoalPi, options: GoalOptions = {}) {
  const evaluate = options.completeSimple ?? completeSimple;
  const clockNow = typeof options.clock?.now === "function"
    ? () => options.clock!.now!()
    : () => Date.now();
  const setTimer: (callback: () => void, delay: number) => TimerHandle =
    options.setTimeout ?? options.timer?.setTimeout ?? ((callback, delay) => setTimeout(callback, delay));
  const clearTimer: (timer: TimerHandle | undefined) => void =
    options.clearTimeout ?? options.timer?.clearTimeout ?? clearTimeout;
  let sessionId = pi.getSessionId?.();
  let sessionContext: GoalContext | undefined;
  let state: GoalState = { enabled: false, goal: "" };
  const isCurrentSession = (event: AnyRecord | undefined, ctx: GoalContext) => {
    const contextSessionId = ctx.sessionManager?.getSessionId?.();
    return (sessionId === undefined || event?.sessionId === undefined || event.sessionId === sessionId)
      && (sessionId === undefined || contextSessionId === undefined || contextSessionId === sessionId);
  };
  let generation = 0;
  let deferredEvaluation: TimerHandle | undefined;
  let deferredEvaluationRequest: PendingEvaluation | undefined;
  let activeAbort: AbortController | undefined;
  let activeEvaluationRequest: PendingEvaluation | undefined;
  let pendingSelfWake: PendingSelfWake | undefined;
  let pendingSnooze: PendingSnooze | undefined;
  let pendingSnoozeAvailability: TimerHandle | undefined;
  let pendingSnoozeWakeup: TimerHandle | undefined;
  let deferredSnoozeWakeup: PendingSnoozeWakeup | undefined;
  let snoozeAvailabilityInjected = false;
  let evaluatorCalls = 0;
  let notDoneWakeups = 0;
  let completedThings = 0;
  let goalLockEnabled = false;
  let completedOffAllowance = false;
  let snoozeEnabled = true;
  let snoozeDurationMs = DEFAULT_SNOOZE_DURATION_MS;
  let snoozeTurnLimit: number | undefined;
  let snoozeBudgetCapMs: number | undefined;
  let snoozeBudgetAvailableMs = 0;
  let snoozeBudgetLastAtMs: number | undefined;
  let snoozeRefillYieldMs = DEFAULT_SNOOZE_REFILL_YIELD_MS;
  let snoozeRefillElapsedMs = DEFAULT_SNOOZE_REFILL_ELAPSED_MS;
  let outcomeCounts = emptyOutcomeCounts();
  let appliedGoalControl: AppliedGoalControl | undefined;
  let rearmActive = false;
  let rearmActivityCount = 0;
  let rearmScope: RearmScope | undefined;
  let currentWorkerId: string | undefined;
  let currentActivityProducerEpoch: string | undefined;
  let latestActivityRevision = 0;
  const seenActivityIds = new Set<string>();
  let compactionLifecyclePause = false;
  let lastOwnerGateState = "allowed";

  const diagnosticSessionId = (ctx: GoalContext | undefined) => {
    const current = safeGoalIdentity(ctx?.sessionManager?.getSessionId?.());
    return current ?? safeGoalIdentity(sessionId);
  };
  const diagnosticState = () => (state.enabled && state.goal.length > 0 ? "enabled" : "disabled");
  const recordActivityGateRejection = (reason: string, ctx?: GoalContext, fields: AnyRecord = {}) => {
    recordGoalDiagnostic("activity_gate_rejected", {
      reason,
      state: diagnosticState(),
      enabled: state.enabled,
      ...(diagnosticSessionId(ctx) === undefined ? {} : { session_id: diagnosticSessionId(ctx) }),
      ...fields,
    });
  };
  const recordWakeupGateRejection = (reason: string, ctx?: GoalContext, fields: AnyRecord = {}) => {
    recordGoalDiagnostic("wakeup_gate_rejected", {
      reason,
      state: diagnosticState(),
      enabled: state.enabled,
      ...(diagnosticSessionId(ctx) === undefined ? {} : { session_id: diagnosticSessionId(ctx) }),
      ...fields,
    });
  };
  const recordOwnerFailure = (reason: string, ctx?: GoalContext, fields: AnyRecord = {}) => {
    recordGoalDiagnostic("owner_service_failed", {
      reason,
      state: diagnosticState(),
      enabled: state.enabled,
      ...(diagnosticSessionId(ctx) === undefined ? {} : { session_id: diagnosticSessionId(ctx) }),
      ...fields,
    });
  };
  const recordPublicationFailure = (reason: string, ctx?: GoalContext, fields: AnyRecord = {}) => {
    recordGoalDiagnostic("publication_failed", {
      reason,
      state: diagnosticState(),
      enabled: state.enabled,
      ...(diagnosticSessionId(ctx) === undefined ? {} : { session_id: diagnosticSessionId(ctx) }),
      ...fields,
    });
  };

  const refreshSnoozeBudget = () => {
    if (snoozeBudgetCapMs === undefined || pendingSnooze) return;
    const current = clockNow();
    if (!Number.isFinite(current)) return;
    if (snoozeBudgetLastAtMs === undefined) {
      snoozeBudgetLastAtMs = current;
      return;
    }
    const elapsed = current - snoozeBudgetLastAtMs;
    snoozeBudgetLastAtMs = current;
    if (elapsed <= 0 || snoozeRefillYieldMs === undefined || snoozeRefillElapsedMs === undefined) return;
    const earned = (elapsed / snoozeRefillElapsedMs) * snoozeRefillYieldMs;
    snoozeBudgetAvailableMs = Math.min(
      snoozeBudgetCapMs,
      Number.isFinite(earned) ? snoozeBudgetAvailableMs + earned : snoozeBudgetCapMs,
    );
  };
  const clearSnoozeBudget = () => {
    snoozeBudgetCapMs = undefined;
    snoozeBudgetAvailableMs = 0;
    snoozeBudgetLastAtMs = undefined;
    snoozeRefillYieldMs = DEFAULT_SNOOZE_REFILL_YIELD_MS;
    snoozeRefillElapsedMs = DEFAULT_SNOOZE_REFILL_ELAPSED_MS;
  };
  const setSnoozeBudget = (durationMs: number) => {
    snoozeBudgetCapMs = durationMs;
    snoozeBudgetAvailableMs = durationMs;
    snoozeBudgetLastAtMs = clockNow();
  };
  const setSnoozeRefill = (yieldMs: number, elapsedMs: number) => {
    refreshSnoozeBudget();
    snoozeRefillYieldMs = yieldMs;
    snoozeRefillElapsedMs = elapsedMs;
    snoozeBudgetLastAtMs = clockNow();
  };
  const pauseBudgetAt = (timestamp: number) => {
    if (snoozeBudgetCapMs !== undefined && Number.isFinite(timestamp)) snoozeBudgetLastAtMs = timestamp;
  };
  const consumeSnoozeBudget = (pending: PendingSnooze, completedAt: number) => {
    if (snoozeBudgetCapMs === undefined) return;
    const elapsed = Math.max(0, completedAt - pending.startedAt);
    // A positive balance admits a wait. The full completed interval is charged,
    // so a short refill can leave debt that later refill periods repay.
    snoozeBudgetAvailableMs -= elapsed;
    pauseBudgetAt(completedAt);
  };
  const settleSnooze = (pending: PendingSnooze, completed: boolean) => {
    if (pendingSnooze !== pending) return;
    pendingSnooze = undefined;
    clearTimer(pending.timeout);
    pending.signal?.removeEventListener?.("abort", pending.onAbort);
    if (completed) consumeSnoozeBudget(pending, clockNow());
    else pauseBudgetAt(clockNow());
    pending.onSettled?.(completed);
  };
  const cancelSnooze = () => {
    const pending = pendingSnooze;
    if (pending) settleSnooze(pending, false);
  };
  const cancelSnoozeAvailability = () => {
    if (pendingSnoozeAvailability === undefined) return;
    clearTimer(pendingSnoozeAvailability);
    pendingSnoozeAvailability = undefined;
  };
  const cancelSnoozeWakeup = () => {
    if (pendingSnoozeWakeup !== undefined) clearTimer(pendingSnoozeWakeup);
    pendingSnoozeWakeup = undefined;
    deferredSnoozeWakeup = undefined;
  };
  const scheduleSnooze = (durationMs: number, signal: AbortSignal | undefined, onSettled?: (completed: boolean) => void) => {
    const pending: PendingSnooze = { signal, onAbort: () => {}, onSettled, startedAt: clockNow(), timeout: undefined };
    pending.onAbort = () => settleSnooze(pending, false);
    pendingSnooze = pending;
    pauseBudgetAt(pending.startedAt);
    if (signal?.aborted) {
      settleSnooze(pending, false);
      return;
    }
    signal?.addEventListener?.("abort", pending.onAbort, { once: true });
    pending.timeout = setTimer(() => settleSnooze(pending, true), durationMs);
  };
  const cancel = () => {
    generation += 1;
    if (deferredEvaluation !== undefined) clearTimer(deferredEvaluation);
    deferredEvaluation = undefined;
    deferredEvaluationRequest = undefined;
    activeEvaluationRequest = undefined;
    if (pendingSelfWake !== undefined) clearTimer(pendingSelfWake.timeout);
    pendingSelfWake = undefined;
    cancelSnooze();
    cancelSnoozeAvailability();
    cancelSnoozeWakeup();
    activeAbort?.abort();
    activeAbort = undefined;
  };

  const goalControlState = () => ({
    ...goalProjection(state),
    goalLockEnabled,
    snoozeEnabled,
    snoozeDurationMs,
    snoozeTurnLimit: snoozeTurnLimit ?? null,
    snoozeBudgetCapMs: snoozeBudgetCapMs ?? null,
    snoozeRefillYieldMs,
    snoozeRefillElapsedMs,
    outcomeCounts: { ...outcomeCounts },
  });
  const rememberActivityId = (activityId: string) => {
    seenActivityIds.delete(activityId);
    seenActivityIds.add(activityId);
    while (seenActivityIds.size > MAX_SEEN_ACTIVITY_IDS) {
      const oldest = seenActivityIds.keys().next().value;
      if (oldest !== undefined) seenActivityIds.delete(oldest);
    }
  };
  const clearRearmState = () => {
    rearmActive = false;
    rearmActivityCount = 0;
    rearmScope = undefined;
  };
  const armRearmState = () => {
    rearmActive = true;
    rearmActivityCount = 0;
    rearmScope = {
      sessionId: typeof sessionId === "string" ? sessionId : "",
      goal: state.goal,
      activityBaselineRevision: latestActivityRevision,
      ...(currentWorkerId === undefined ? {} : { workerId: currentWorkerId }),
      ...(currentActivityProducerEpoch === undefined ? {} : { producerEpoch: currentActivityProducerEpoch }),
    };
  };
  const persist = () => pi.appendEntry(GOAL_STATE_ENTRY, {
    ...state,
    notDoneWakeups,
    completedThings,
    goalLockEnabled,
    snoozeEnabled,
    snoozeDurationMs,
    ...(snoozeTurnLimit === undefined ? {} : { snoozeTurnLimit }),
    ...(snoozeBudgetCapMs === undefined ? {} : {
      snoozeBudgetCapMs,
      snoozeBudgetAvailableMs,
    }),
    ...(snoozeBudgetCapMs !== undefined || snoozeRefillYieldMs !== DEFAULT_SNOOZE_REFILL_YIELD_MS
      || snoozeRefillElapsedMs !== DEFAULT_SNOOZE_REFILL_ELAPSED_MS
      ? { snoozeRefillYieldMs, snoozeRefillElapsedMs }
      : {}),
    outcomeCounts: { ...outcomeCounts },
    ...(rearmActive ? {
      rearmActive: true,
      rearmActivityCount,
      rearmScope: { ...rearmScope },
    } : {}),
    ...(appliedGoalControl === undefined ? {} : { appliedGoalControl: { ...appliedGoalControl } }),
  });
  const publish = () => {
    try { pi.events?.emit?.(GOAL_STATE_EVENT, goalProjection(state)); } catch (error) {
      recordPublicationFailure("state_event_emit", sessionContext, { component: "goal-state", error_name: error instanceof Error ? error.name : "Error" });
      /* optional UI publication is best-effort */
    }
    try { pi.events?.emit?.(GOAL_CONTROL_STATE_EVENT, goalControlState()); } catch (error) {
      recordPublicationFailure("control_state_event_emit", sessionContext, { component: "goal-control-state", error_name: error instanceof Error ? error.name : "Error" });
      /* optional control publication is best-effort */
    }
  };
  const recordOutcome = (outcome: Outcome) => {
    if (!OUTCOMES.has(outcome)) return;
    outcomeCounts[outcome] += 1;
    persist();
    publish();
  };
  const recordMeaningfulActivity = (activity: AnyRecord) => {
    if (!activity || typeof activity !== "object" || Array.isArray(activity)) {
      recordActivityGateRejection("invalid_activity");
      return;
    }
    const readActivityField = (key: string): unknown => {
      try { return activity[key]; } catch { return undefined; }
    };
    const rawActivityId = readActivityField("activityId");
    const rawKind = readActivityField("kind");
    const rawSession = readActivityField("sessionId");
    const rawWorker = readActivityField("workerId");
    const rawProducerEpoch = readActivityField("producerEpoch");
    const activityId = safeGoalIdentity(rawActivityId);
    const diagnosticKind = typeof rawKind === "string" && SAFE_DIAGNOSTIC_KIND_PATTERN.test(rawKind) ? rawKind : undefined;
    const kind = typeof rawKind === "string" && MEANINGFUL_ACTIVITY_KINDS.has(rawKind) ? rawKind : undefined;
    const session = safeGoalIdentity(rawSession);
    const worker = rawWorker === undefined ? undefined : safeGoalIdentity(rawWorker);
    const producerEpoch = rawProducerEpoch === undefined ? undefined : safeGoalIdentity(rawProducerEpoch);
    const revision = readActivityField("revision");
    const rawGoal = readActivityField("goal");
    const activityGoal = isRecord(rawGoal) ? rawGoal : undefined;
    const identity = {
      ...(activityId === undefined ? {} : { event_id: activityId }),
      ...(diagnosticKind === undefined ? {} : { kind: diagnosticKind }),
      ...(worker === undefined ? {} : { worker_id: worker }),
      ...(producerEpoch === undefined ? {} : { producer_epoch: producerEpoch }),
      ...(isSafeNonnegativeInteger(revision) ? { revision } : {}),
    };
    if (kind === undefined) {
      recordActivityGateRejection("invalid_kind", undefined, identity);
      return;
    }
    if (activityId === undefined) {
      recordActivityGateRejection("missing_activity_id", undefined, identity);
      return;
    }
    if (session === undefined) {
      recordActivityGateRejection("invalid_session_id", undefined, identity);
      return;
    }
    if (rawWorker !== undefined && worker === undefined) {
      recordActivityGateRejection("invalid_worker_id", undefined, { ...identity, session_id: session });
      return;
    }
    if (rawProducerEpoch !== undefined && producerEpoch === undefined) {
      recordActivityGateRejection("invalid_producer_epoch", undefined, { ...identity, session_id: session });
      return;
    }
    if (!isSafeNonnegativeInteger(revision) || revision < 1) {
      recordActivityGateRejection("invalid_revision", undefined, { ...identity, session_id: session });
      return;
    }
    if (session !== sessionId) {
      recordActivityGateRejection("stale_session", undefined, { ...identity, session_id: session });
      return;
    }
    if (seenActivityIds.has(activityId)) {
      recordActivityGateRejection("duplicate_activity", undefined, { ...identity, session_id: session });
      return;
    }
    if (activityGoal?.enabled !== true || normalizeGoal(activityGoal?.goal) !== state.goal) {
      recordActivityGateRejection("goal_projection_mismatch", undefined, { ...identity, session_id: session });
      return;
    }
    if (rearmActive && rearmScope?.workerId !== undefined && worker !== rearmScope.workerId) {
      recordActivityGateRejection("worker_mismatch", undefined, { ...identity, session_id: session });
      return;
    }
    latestActivityRevision = Math.max(latestActivityRevision, revision);
    rememberActivityId(activityId);
    if (!rearmActive || !state.enabled || !state.goal || !rearmScope) {
      recordActivityGateRejection("rearm_inactive", undefined, { ...identity, session_id: session });
      return;
    }
    if (rearmScope.sessionId !== session) {
      recordActivityGateRejection("rearm_session_mismatch", undefined, { ...identity, session_id: session });
      return;
    }
    if (rearmScope.goal !== state.goal) {
      recordActivityGateRejection("rearm_goal_mismatch", undefined, { ...identity, session_id: session });
      return;
    }
    if (rearmScope.workerId !== undefined && worker !== rearmScope.workerId) {
      recordActivityGateRejection("rearm_worker_mismatch", undefined, { ...identity, session_id: session });
      return;
    }
    if (rearmScope.producerEpoch !== undefined && producerEpoch !== rearmScope.producerEpoch) {
      recordActivityGateRejection("rearm_producer_mismatch", undefined, { ...identity, session_id: session });
      return;
    }
    if (rearmScope.workerId === undefined && worker !== undefined) rearmScope.workerId = worker;
    if (rearmScope.producerEpoch === undefined && producerEpoch !== undefined) rearmScope.producerEpoch = producerEpoch;
    rearmActivityCount += 1;
    if (rearmActivityCount < MEANINGFUL_ACTIVITY_EVENTS_TO_REARM) {
      persist();
      publish();
      return;
    }
    // The third new action re-arms delivery, but does not itself schedule a goal check.
    // The next real settled boundary owns the first post-cap attempt.
    notDoneWakeups = 0;
    clearRearmState();
    persist();
    publish();
  };
  const scheduleSelfWake = (goal: string, ctx: GoalContext) => {
    if (typeof sessionId !== "string" || sessionId.length === 0) {
      recordWakeupGateRejection("missing_session_id", ctx, { component: "self-wakeup" });
      return;
    }
    const pending: PendingSelfWake = { goal, ctx, runGeneration: generation, timeout: undefined };
    pendingSelfWake = pending;
    pending.timeout = setTimer(() => {
      if (pendingSelfWake !== pending) {
        recordWakeupGateRejection("superseded", ctx, { component: "self-wakeup" });
        return;
      }
      pending.timeout = undefined;
      if (pending.runGeneration !== generation || !state.enabled || state.goal !== goal || !isCurrentSession(undefined, ctx)) {
        pendingSelfWake = undefined;
        recordWakeupGateRejection("stale_self_wakeup", ctx, { component: "self-wakeup" });
        return;
      }
      if (compactionPending(ctx)) {
        recordWakeupGateRejection("compaction_pending", ctx, { component: "self-wakeup", state: "compacting" });
        return;
      }
      pendingSelfWake = undefined;
      try {
        pi.sendMessage({
          customType: GOAL_UPDATE_TYPE,
          content: `Goal set: ${goal}. Begin working toward this goal.`,
          display: true,
          details: { enabled: true, goal },
        }, { deliverAs: "steer", triggerTurn: true });
      } catch (error) {
        recordPublicationFailure("self_wakeup_send", ctx, { component: "goal-update", error_name: error instanceof Error ? error.name : "Error" });
        /* persistence remains authoritative when the session cannot wake */
      }
    }, 0);
  };
  const notify = (ctx: GoalContext, message: string, level: string = "info") => ctx.ui?.notify?.(message, level);
  const compactionPending = (ctx: GoalContext) => compactionLifecyclePause || hasPendingCompaction(ctx);
  const resumePendingSelfWake = (ctx: GoalContext) => {
    const pending = pendingSelfWake;
    if (!pending) return;
    if (compactionPending(ctx)) {
      recordWakeupGateRejection("compaction_pending", ctx, { component: "self-wakeup-resume", state: "compacting" });
      return;
    }
    if (pending.runGeneration !== generation || !state.enabled || state.goal !== pending.goal || !isCurrentSession(undefined, ctx)) {
      recordWakeupGateRejection("stale_self_wakeup", ctx, { component: "self-wakeup-resume" });
      return;
    }
    pendingSelfWake = undefined;
    try {
      pi.sendMessage({
        customType: GOAL_UPDATE_TYPE,
        content: `Goal set: ${pending.goal}. Begin working toward this goal.`,
        display: true,
        details: { enabled: true, goal: pending.goal },
      }, { deliverAs: "steer", triggerTurn: true });
    } catch (error) {
      recordPublicationFailure("self_wakeup_send", ctx, { component: "goal-update", error_name: error instanceof Error ? error.name : "Error" });
      /* persistence remains authoritative when the session cannot wake */
    }
  };
  const scheduleSnoozeAvailability = (ctx: GoalContext) => {
    if (snoozeAvailabilityInjected || pendingSnoozeAvailability !== undefined) {
      recordWakeupGateRejection("availability_already_pending", ctx, { component: "snooze-availability" });
      return;
    }
    const runGeneration = generation;
    pendingSnoozeAvailability = setTimer(() => {
      pendingSnoozeAvailability = undefined;
      if (runGeneration !== generation || !state.enabled || !state.goal || !snoozeEnabled
        || notDoneWakeups >= currentWakeupLimit() || compactionPending(ctx)) {
        recordWakeupGateRejection(compactionPending(ctx) ? "compaction_pending" : "stale_snooze_availability", ctx, {
          component: "snooze-availability",
          ...(compactionPending(ctx) ? { state: "compacting" } : {}),
        });
        return;
      }
      try {
        pi.sendMessage({
          customType: GOAL_SNOOZE_AVAILABILITY_TYPE,
          content: "Goal snooze is available. goal_snooze can be used to deliberately wait before the next goal wakeup. Only use this if you need to wait for something, otherwise continue working toward the goal.",
          display: true,
          details: { goal: state.goal, durationMs: snoozeDurationMs },
        }, { deliverAs: "steer", triggerTurn: true });
        snoozeAvailabilityInjected = true;
      } catch (error) {
        recordPublicationFailure("snooze_availability_send", ctx, { component: "goal-snooze-availability", error_name: error instanceof Error ? error.name : "Error" });
        /* availability remains optional when the session cannot wake */
      }
    }, 0);
  };
  const scheduleSnoozeWakeup = (goal: string, runGeneration: number, wakeup: string, ctx: GoalContext, allowAfterDisable = false) => {
    const timeout = setTimer(() => {
      if (pendingSnoozeWakeup === timeout) pendingSnoozeWakeup = undefined;
      if (runGeneration !== generation || !state.enabled || state.goal !== goal
        || !isCurrentSession(undefined, ctx) || (!snoozeEnabled && !allowAfterDisable)) {
        recordWakeupGateRejection("stale_snooze_wakeup", ctx, { component: "snooze-wakeup" });
        return;
      }
      if (compactionPending(ctx)) {
        recordWakeupGateRejection("compaction_pending", ctx, { component: "snooze-wakeup", state: "compacting" });
        deferredSnoozeWakeup = { goal, runGeneration, wakeup, ctx, allowAfterDisable };
        return;
      }
      try {
        pi.sendMessage({
          customType: GOAL_RESULT_TYPE,
          content: goalResultContent(wakeup, "NOT_DONE", "Work remains."),
          display: true,
          details: { goal, outcome: "NOT_DONE", rationale: "Work remains." },
        }, { deliverAs: "steer", triggerTurn: true });
      } catch (error) {
        recordPublicationFailure("snooze_wakeup_send", ctx, { component: "goal-result", error_name: error instanceof Error ? error.name : "Error" });
      }
    }, 0);
    pendingSnoozeWakeup = timeout;
  };
  const resumeDeferredSnoozeWakeup = (ctx: GoalContext) => {
    if (compactionPending(ctx) || deferredSnoozeWakeup === undefined) return;
    const pending = deferredSnoozeWakeup;
    deferredSnoozeWakeup = undefined;
    scheduleSnoozeWakeup(pending.goal, pending.runGeneration, pending.wakeup, pending.ctx, pending.allowAfterDisable);
  };
  const deactivateSnooze = ({ preservePendingWakeup = false, clearBudget = false } = {}) => {
    snoozeEnabled = false;
    snoozeAvailabilityInjected = false;
    snoozeTurnLimit = undefined;
    cancelSnooze();
    cancelSnoozeAvailability();
    if (!preservePendingWakeup) cancelSnoozeWakeup();
    if (clearBudget) clearSnoozeBudget();
    persist();
    publish();
  };
  const configureSnooze = (input: unknown, ctx: GoalContext) => {
    if (input === "off") {
      deactivateSnooze({ clearBudget: true });
      return "Goal snooze off";
    }
    const wasEnabled = snoozeEnabled;
    snoozeEnabled = true;
    snoozeDurationMs = snoozeDurationFromInput(input);
    persist();
    publish();
    if (!wasEnabled && state.enabled && state.goal) scheduleSnoozeAvailability(ctx);
    return "Goal snooze enabled";
  };
  const configureSnoozeTurnLimit = (input: unknown) => {
    const limit = parseSnoozeTurnLimit(input);
    if (limit === undefined) return { ok: false, message: "Usage: /goal-snooze-turn-limit <1-20>" };
    snoozeTurnLimit = limit;
    persist();
    publish();
    return { ok: true, message: `Goal snooze turn limit set: ${limit}` };
  };
  const configureSnoozeBudget = (input: unknown) => {
    if (input === "off") {
      clearSnoozeBudget();
      persist();
      publish();
      return { ok: true, message: "Goal snooze budget off" };
    }
    const durationMs = parsePositiveDuration(input);
    if (durationMs === undefined) return { ok: false, message: "Usage: /goal-snooze-budget <duration> | /goal-snooze-budget off" };
    setSnoozeBudget(durationMs);
    persist();
    publish();
    return { ok: true, message: "Goal snooze budget set" };
  };
  const configureSnoozeRefill = (input: unknown) => {
    const match = typeof input === "string" ? input.trim().match(/^([^/]+)\/([^/]+)$/) : undefined;
    const yieldMs = parsePositiveDuration(match?.[1]);
    const elapsedMs = parsePositiveDuration(match?.[2]);
    if (yieldMs === undefined || elapsedMs === undefined) {
      return { ok: false, message: "Usage: /goal-snooze-budget-refill <yield>/<elapsed>" };
    }
    setSnoozeRefill(yieldMs, elapsedMs);
    persist();
    publish();
    return { ok: true, message: "Goal snooze budget refill set" };
  };
  const configureGoalLock = (input: unknown) => {
    if (input !== "on" && input !== "off") return { ok: false, message: "Usage: /goal-lock on | /goal-lock off" };
    goalLockEnabled = input === "on";
    persist();
    publish();
    return { ok: true, message: `Goal lock ${goalLockEnabled ? "on" : "off"}` };
  };
  const currentWakeupLimit = () => snoozeEnabled && snoozeTurnLimit !== undefined
    ? snoozeTurnLimit
    : MAX_NOT_DONE_WAKEUPS;
  const attemptCapReached = () => notDoneWakeups >= currentWakeupLimit();
  const recordNotDoneWakeup = (): (() => void) => {
    notDoneWakeups += 1;
    if (attemptCapReached()) armRearmState();
    persist();
    return () => {
      notDoneWakeups = Math.max(0, notDoneWakeups - 1);
      if (rearmActive && !attemptCapReached()) clearRearmState();
      persist();
    };
  };
  const statusDetails = (action: GoalAction) => ({
    action,
    ...state,
    ...goalControlState(),
    evaluatorCalls,
    notDoneWakeups,
    completedThings,
    ...(rearmActive ? {
      rearmActive: true,
      rearmActivityCount,
      rearmRequired: MEANINGFUL_ACTIVITY_EVENTS_TO_REARM,
    } : {}),
    lastOwnerGateState,
  });
  const ownerActivityState = (ctx: GoalContext) => {
    // pi-subagents publishes one service per owning session. Resolve it at
    // settle time because its session_start handler may publish after this
    // extension's handler, and never let another owner's children gate us.
    const ownerSessionId = ctx?.sessionManager?.getSessionId?.() ?? sessionId;
    const registry = Object.getOwnPropertyDescriptor(globalThis, PI_SUBAGENTS_SERVICE_REGISTRY)?.value;
    const services = isRecord(registry) && isServiceRegistry(registry.services) ? registry.services : undefined;
    let service: unknown;
    try {
      service = typeof ownerSessionId === "string" && services !== undefined
        ? services.get(ownerSessionId)
        : undefined;
    } catch (error) {
      recordOwnerFailure("service_lookup", ctx, { error_name: error instanceof Error ? error.name : "Error" });
      lastOwnerGateState = "allowed";
      return lastOwnerGateState;
    }
    if (!isGoalOwnerService(service)) {
      lastOwnerGateState = "allowed";
      return lastOwnerGateState;
    }
    try {
      const running = service.hasRunning();
      if (typeof running !== "boolean") {
        recordOwnerFailure("invalid_running_state", ctx);
        lastOwnerGateState = "allowed";
      } else {
        lastOwnerGateState = running ? "suppressed" : "allowed";
      }
    } catch (error) {
      recordOwnerFailure("running_check", ctx, { error_name: error instanceof Error ? error.name : "Error" });
      lastOwnerGateState = "allowed";
    }
    return lastOwnerGateState;
  };
  const applyAction = (
    action: unknown,
    rawGoal: unknown,
    ctx: GoalContext,
    { programmatic = false, beforePersist }: { programmatic?: boolean; beforePersist?: () => void } = {},
  ): GoalActionResult => {
    if (action !== "set" && action !== "status" && action !== "off") {
      return { ok: false, message: "Invalid action. Use set, status, or off." };
    }
    if (programmatic && goalLockEnabled && action !== "status") {
      const canTurnOffCompletedGoal = action === "off" && completedOffAllowance && state.enabled && state.goal;
      if (!canTurnOffCompletedGoal) {
        return { ok: false, message: "Goal lock is on. Use /goal-lock off to change the goal." };
      }
    }
    if (action === "set") {
      const goal = normalizeGoal(rawGoal);
      if (!goal) return { ok: false, message: "A goal description is required for set." };
      cancel();
      evaluatorCalls = 0;
      notDoneWakeups = 0;
      completedThings = 0;
      clearRearmState();
      outcomeCounts = emptyOutcomeCounts();
      completedOffAllowance = false;
      lastOwnerGateState = "allowed";
      state = { enabled: true, goal };
      beforePersist?.();
      persist();
      publish();
      scheduleSelfWake(goal, ctx);
      if (snoozeEnabled) scheduleSnoozeAvailability(ctx);
      return { ok: true, message: `Goal set: ${goal}`, details: { action, ...state } };
    }
    if (action === "status") {
      return {
        ok: true,
        message: state.enabled ? `Goal enabled: ${state.goal}` : "Goal off",
        details: statusDetails(action),
      };
    }
    cancel();
    clearRearmState();
    completedOffAllowance = false;
    state = { ...state, enabled: false };
    if (programmatic && goalLockEnabled) goalLockEnabled = false;
    beforePersist?.();
    persist();
    publish();
    return { ok: true, message: "Goal off", details: { action, ...state } };
  };
  const applyGoalControl = (request: AnyRecord, ctx: GoalContext) => {
    const reject = (errorCode: string, message: string) => ({ accepted: false, errorCode, message });
    if (!request || typeof request !== "object" || !isCurrentSession(undefined, ctx)) {
      return reject("unavailable", "Goal is unavailable in this session.");
    }
    const { action, controlId, targetInstanceId, revision } = request;
    if (!isGoalControlAction(action) || typeof controlId !== "string" || controlId.length === 0
      || typeof targetInstanceId !== "string" || targetInstanceId.length === 0
      || !isSafeNonnegativeInteger(revision) || revision < 1) {
      return reject("invalid_control", "The goal control is invalid.");
    }
    if (appliedGoalControl?.instanceId === targetInstanceId) {
      if (revision < appliedGoalControl.revision) return reject("stale_revision", "The goal control is stale.");
      if (revision === appliedGoalControl.revision) {
        return { accepted: true, revision, goalState: goalControlState() };
      }
    }
    const settings = normalizeGoalControlSettings(request.settings);
    if (!settings) return reject("invalid_settings", "The goal settings are invalid.");
    if (action === "set" && !normalizeGoal(request.goal)) {
      return reject("invalid_goal", "A goal description is required.");
    }
    const applySettings = () => {
      const budgetChanged = snoozeBudgetCapMs !== settings.snoozeBudgetCapMs;
      goalLockEnabled = settings.goalLockEnabled;
      snoozeEnabled = settings.snoozeEnabled;
      snoozeDurationMs = settings.snoozeDurationMs;
      snoozeTurnLimit = settings.snoozeTurnLimit;
      snoozeBudgetCapMs = settings.snoozeBudgetCapMs;
      snoozeBudgetAvailableMs = snoozeBudgetCapMs === undefined
        ? 0
        : budgetChanged ? snoozeBudgetCapMs : Math.min(snoozeBudgetCapMs, Math.max(0, snoozeBudgetAvailableMs));
      snoozeBudgetLastAtMs = snoozeBudgetCapMs === undefined ? undefined : clockNow();
      snoozeRefillYieldMs = settings.snoozeRefillYieldMs;
      snoozeRefillElapsedMs = settings.snoozeRefillElapsedMs;
      appliedGoalControl = { instanceId: targetInstanceId, revision };
    };
    // A Web UI save is a user interaction. Goal lock still protects programmatic
    // tool calls, but it cannot prevent the UI from changing the lock itself.
    const result = applyAction(action, request.goal, ctx, { beforePersist: applySettings });
    if (!result.ok) return reject("rejected", result.message);
    return { accepted: true, revision, goalState: goalControlState() };
  };
  const scheduleInjection = (
    event: AnyRecord,
    ctx: GoalContext,
    runGeneration: number,
    controller: AbortController,
    message: AnyRecord,
    beforeSend?: () => (() => void) | undefined,
  ) => {
    if (runGeneration !== generation || controller.signal.aborted || !isCurrentSession(event, ctx)) {
      recordWakeupGateRejection("stale_injection", ctx, { component: "goal-result" });
      return;
    }
    setTimer(() => {
      if (runGeneration !== generation || controller.signal.aborted || !isCurrentSession(event, ctx) || !state.enabled || !state.goal) {
        recordWakeupGateRejection("stale_injection", ctx, { component: "goal-result" });
        return;
      }
      // Re-check in the wakeup macrotask. The evaluator result is stale if an
      // owner child was admitted after complete() returned.
      if (compactionPending(ctx)) {
        recordWakeupGateRejection("compaction_pending", ctx, { component: "goal-result", state: "compacting" });
        return;
      }
      if (ownerActivityState(ctx) === "suppressed") {
        recordWakeupGateRejection("owner_children_running", ctx, { component: "goal-result", state: "suppressed" });
        return;
      }
      let undoBeforeSend: (() => void) | undefined;
      try {
        undoBeforeSend = beforeSend?.();
        pi.sendMessage(message, { deliverAs: "steer", triggerTurn: true });
      } catch(error) {
        undoBeforeSend?.();
        recordPublicationFailure("goal_wakeup_send", ctx, { component: "goal-result", error_name: error instanceof Error ? error.name : "Error" });
      }
    }, 0);
  };
  const scheduleDeferredEvaluation = (event: AnyRecord, ctx: GoalContext, runGeneration: number, goal: string) => {
    if (deferredEvaluation !== undefined || activeAbort) {
      recordWakeupGateRejection("evaluation_already_pending", ctx, { component: "evaluation" });
      return;
    }
    const pending: PendingEvaluation = { event, ctx, runGeneration, goal };
    deferredEvaluationRequest = pending;
    deferredEvaluation = setTimer(async () => {
      deferredEvaluation = undefined;
      if (deferredEvaluationRequest !== pending) {
        recordWakeupGateRejection("superseded", ctx, { component: "evaluation" });
        return;
      }
      deferredEvaluationRequest = undefined;
      if (runGeneration !== generation || !isCurrentSession(event, ctx) || !state.enabled || state.goal !== goal || !ctx.model) {
        recordWakeupGateRejection("stale_evaluation", ctx, { component: "evaluation" });
        return;
      }
      if (compactionPending(ctx)) {
        recordWakeupGateRejection("compaction_pending", ctx, { component: "evaluation", state: "compacting" });
        deferredEvaluationRequest = pending;
        return;
      }
      const controller = new AbortController();
      activeAbort = controller;
      activeEvaluationRequest = pending;
      try {
        // The owner service is authoritative at this boundary. A child can be
        // admitted after the settle event, so the earlier gate cannot protect
        // the evaluator call.
        if (compactionPending(ctx)) {
          recordWakeupGateRejection("compaction_pending", ctx, { component: "evaluation", state: "compacting" });
          return;
        }
        if (ownerActivityState(ctx) === "suppressed") {
          recordWakeupGateRejection("owner_children_running", ctx, { component: "evaluation", state: "suppressed" });
          return;
        }
        evaluatorCalls = Math.min(evaluatorCalls + 1, MAX_EVALUATOR_CALLS);
        const request = {
          systemPrompt: EVALUATOR_PROMPT.replace("<goal description>", goal),
          messages: [{
            role: "user",
            content: [{ type: "text", text: `Goal: ${goal}\n\nActive conversation:\n${boundedActiveContext(ctx).map((message) => `${message.role}: ${message.content[0].text}`).join("\n\n")}\n\nReturn exactly two lines: VERDICT: COMPLETED|NOT_DONE and REASON: <concise rationale>.` }],
            timestamp: Date.now(),
          }],
        } satisfies Context;
        const response = await completeEvaluator(ctx, request, controller.signal, evaluate);
        if (runGeneration !== generation || controller.signal.aborted) {
          recordWakeupGateRejection("stale_evaluation_result", ctx, { component: "evaluation" });
          return;
        }
        if (compactionPending(ctx)) {
          recordWakeupGateRejection("compaction_pending", ctx, { component: "evaluation", state: "compacting" });
          deferredEvaluationRequest = pending;
          return;
        }
        const { outcome, rationale } = responseEvaluation(response);
        recordOutcome(outcome);
        completedOffAllowance = outcome === "COMPLETED";
        if (outcome === "COMPLETED") {
          completedThings += 1;
          if (completedThings >= COMPLETIONS_TO_RESET_WAKEUPS) {
            completedThings = 0;
            notDoneWakeups = 0;
            clearRearmState();
          }
          persist();
          publish();
        }
        if (outcome === "NOT_DONE" && notDoneWakeups >= currentWakeupLimit()) {
          recordWakeupGateRejection("retry_limit", ctx, { component: "evaluation" });
          return;
        }
        let undoNotDoneWakeup: (() => void) | undefined;
        const wakeup = outcome === "NOT_DONE"
          ? (() => {
            undoNotDoneWakeup = recordNotDoneWakeup();
            return notDoneWakeup(goal, notDoneWakeups);
          })()
          : "Goal check: COMPLETED. Contact the supervisor and report completion. Disable future goal notifications with /goal off.";
        const beforeSend = outcome === "NOT_DONE" ? () => undoNotDoneWakeup : undefined;
        scheduleInjection(event, ctx, runGeneration, controller, {
          customType: GOAL_RESULT_TYPE,
          content: goalResultContent(wakeup, outcome, rationale),
          display: true,
          details: { goal, outcome, rationale },
        }, beforeSend);
      } catch(e) {
        if (compactionPending(ctx)) {
          recordWakeupGateRejection("compaction_pending", ctx, { component: "evaluation", state: "compacting" });
          deferredEvaluationRequest = pending;
          return;
        }
        if (runGeneration !== generation || controller.signal.aborted) {
          recordWakeupGateRejection("stale_evaluation_result", ctx, { component: "evaluation" });
          return;
        }
        recordGoalDiagnostic("evaluation_failed", {
          reason: "evaluator_error",
          state: diagnosticState(),
          enabled: state.enabled,
          ...(diagnosticSessionId(ctx) === undefined ? {} : { session_id: diagnosticSessionId(ctx) }),
          error_name: e instanceof Error ? e.name : "Error",
        });
        recordOutcome("NOT_DONE");
        if (notDoneWakeups >= currentWakeupLimit()) {
          recordWakeupGateRejection("retry_limit", ctx, { component: "evaluation" });
          return;
        }
        let undoNotDoneWakeup: (() => void) | undefined;
        scheduleInjection(event, ctx, runGeneration, controller, {
          customType: GOAL_RESULT_TYPE,
          content: goalResultContent(
            (() => {
              undoNotDoneWakeup = recordNotDoneWakeup();
              return notDoneWakeup(goal, notDoneWakeups);
            })(),
            "NOT_DONE",
            "Evaluator unavailable: " + errorDetails(e),
          ),
          display: true,
          details: { goal, outcome: "NOT_DONE", rationale: "Evaluator unavailable" },
        }, () => undoNotDoneWakeup);
      } finally {
        if (activeAbort === controller) activeAbort = undefined;
        if (activeEvaluationRequest === pending) activeEvaluationRequest = undefined;
        if (deferredEvaluationRequest !== undefined && !compactionPending(ctx)) resumeDeferredEvaluation(ctx);
      }
    }, 0);
  };
  const resumeDeferredEvaluation = (ctx: GoalContext) => {
    const pending = deferredEvaluationRequest;
    if (!pending) return;
    if (compactionPending(ctx)) {
      recordWakeupGateRejection("compaction_pending", ctx, { component: "evaluation-resume", state: "compacting" });
      return;
    }
    if (deferredEvaluation !== undefined || activeAbort
      || pending.runGeneration !== generation || !isCurrentSession(pending.event, pending.ctx)
      || !state.enabled || state.goal !== pending.goal) {
      recordWakeupGateRejection("stale_evaluation", ctx, { component: "evaluation-resume" });
      return;
    }
    deferredEvaluationRequest = undefined;
    scheduleDeferredEvaluation(pending.event, pending.ctx, pending.runGeneration, pending.goal);
  };

  pi.events?.on?.(GOAL_STATE_REQUEST_EVENT, () => publish());
  pi.events?.on?.(GOAL_WORKER_ID_EVENT, (identity) => {
    if (!identity || typeof identity !== "object" || Array.isArray(identity)
      || (identity.sessionId !== undefined && identity.sessionId !== sessionId)
      || typeof identity.workerId !== "string" || identity.workerId.length === 0) {
      recordActivityGateRejection("invalid_worker_identity");
      return;
    }
    const producerEpoch = typeof identity.producerEpoch === "string" && identity.producerEpoch.length > 0
      ? identity.producerEpoch
      : undefined;
    const workerChanged = currentWorkerId !== identity.workerId;
    const producerChanged = currentActivityProducerEpoch !== producerEpoch;
    currentWorkerId = identity.workerId;
    currentActivityProducerEpoch = producerEpoch;
    if (!rearmActive || !rearmScope) return;
    if ((rearmScope.workerId !== undefined && rearmScope.workerId !== currentWorkerId)
      || (rearmScope.producerEpoch !== undefined && rearmScope.producerEpoch !== producerEpoch)) {
      rearmActivityCount = 0;
      rearmScope = {
        ...rearmScope,
        workerId: currentWorkerId,
        activityBaselineRevision: latestActivityRevision,
        ...(producerEpoch === undefined ? {} : { producerEpoch }),
      };
      persist();
      publish();
    } else if ((rearmScope.workerId === undefined && workerChanged)
      || (rearmScope.producerEpoch === undefined && producerChanged && producerEpoch !== undefined)) {
      rearmScope.workerId = currentWorkerId;
      if (producerEpoch !== undefined) rearmScope.producerEpoch = producerEpoch;
      persist();
    }
  });
  pi.events?.on?.(GOAL_ACTIVITY_EVENT, recordMeaningfulActivity);
  pi.events?.on?.(GOAL_CONTROL_REQUEST_EVENT, (request) => {
    const context = isGoalContext(request.context) ? request.context : sessionContext;
    if (context === undefined) {
      recordWakeupGateRejection("missing_context", undefined, { component: "goal-control" });
      return;
    }
    const result = applyGoalControl(request, context);
    const respond = request.respond;
    if (typeof respond === "function") {
      try { respond(result); } catch (error) {
        recordPublicationFailure("control_response", context, { component: "goal-control", error_name: error instanceof Error ? error.name : "Error" });
        /* the router can time out a disconnected source */
      }
    }
  });

  pi.on("session_start", async (event, ctx) => {
    if (sessionId === undefined) sessionId = typeof event.sessionId === "string" ? event.sessionId : ctx.sessionManager?.getSessionId?.();
    sessionContext = ctx;
    if (!isCurrentSession(event, ctx)) return;
    cancel();
    compactionLifecyclePause = false;
    snoozeAvailabilityInjected = false;
    const restored = restoreState(ctx);
    state = { enabled: restored.enabled, goal: restored.goal };
    notDoneWakeups = restored.notDoneWakeups;
    completedThings = restored.completedThings;
    goalLockEnabled = restored.goalLockEnabled;
    completedOffAllowance = false;
    snoozeEnabled = restored.snoozeEnabled;
    snoozeDurationMs = restored.snoozeDurationMs;
    snoozeTurnLimit = restored.snoozeTurnLimit;
    snoozeBudgetCapMs = restored.snoozeBudgetCapMs;
    snoozeBudgetAvailableMs = restored.snoozeBudgetAvailableMs;
    snoozeBudgetLastAtMs = snoozeBudgetCapMs === undefined ? undefined : clockNow();
    snoozeRefillYieldMs = restored.snoozeRefillYieldMs;
    snoozeRefillElapsedMs = restored.snoozeRefillElapsedMs;
    outcomeCounts = restored.outcomeCounts;
    appliedGoalControl = restored.appliedGoalControl;
    rearmActive = restored.rearmActive === true && restored.rearmScope !== undefined;
    rearmActivityCount = rearmActive ? restored.rearmActivityCount : 0;
    rearmScope = rearmActive ? restored.rearmScope : undefined;
    latestActivityRevision = rearmScope?.activityBaselineRevision ?? 0;
    seenActivityIds.clear();
    currentWorkerId = rearmScope?.workerId;
    currentActivityProducerEpoch = rearmScope?.producerEpoch;
    if (rearmActive && rearmScope && rearmScope.sessionId !== sessionId) {
      currentWorkerId = undefined;
      clearRearmState();
    }
    if (!rearmActive && attemptCapReached()) armRearmState();
    publish();
    if (state.enabled && snoozeEnabled) scheduleSnoozeAvailability(ctx);
  });

  pi.on("session_before_compact", (event, ctx) => {
    if (!isCurrentSession(event, ctx)) return;
    compactionLifecyclePause = true;
    if (activeAbort && activeEvaluationRequest) {
      deferredEvaluationRequest = activeEvaluationRequest;
      activeAbort.abort();
    }
    if (!isAbortSignal(event.signal)) return;
    event.signal.addEventListener("abort", () => {
      compactionLifecyclePause = false;
      setTimer(() => {
        resumePendingSelfWake(ctx);
        resumeDeferredEvaluation(ctx);
        resumeDeferredSnoozeWakeup(ctx);
      }, 0);
    }, { once: true });
  });

  pi.on("session_compact", (_event, ctx) => {
    if (!isCurrentSession(undefined, ctx)) return;
    compactionLifecyclePause = false;
    resumePendingSelfWake(ctx);
    resumeDeferredEvaluation(ctx);
    resumeDeferredSnoozeWakeup(ctx);
    if (state.enabled && snoozeEnabled) scheduleSnoozeAvailability(ctx);
  });

  pi.on("session_shutdown", async (event, ctx) => {
    if (!isCurrentSession(event, ctx)) return;
    sessionContext = undefined;
    compactionLifecyclePause = false;
    cancel();
  });

  pi.registerTool({
    name: "goal",
    label: "Goal",
    description: "Set, inspect, or disable the active goal evaluator.",
    promptSnippet: "Set, inspect, or disable the active goal evaluator.",
    promptGuidelines: [
      "Use goal with action set, status, or off for standing goal evaluation.",
      "Call pi__goal through the Cursor pi bridge.",
    ],
    parameters: GOAL_PARAMETERS,
    async execute(_toolCallId, params, _signal, _onUpdate, ctx) {
      if (!isCurrentSession(undefined, ctx)) {
        recordWakeupGateRejection("stale_session", ctx, { component: "goal-tool" });
        return { content: [{ type: "text", text: "Goal is unavailable in this session." }], isError: true };
      }
      const result = applyAction(params?.action, params?.goal, ctx, { programmatic: true });
      return {
        content: [{ type: "text", text: result.message }],
        ...(result.details ? { details: result.details } : {}),
        ...(result.ok ? {} : { isError: true }),
      };
    },
  });

  pi.registerTool({
    name: "goal_snooze",
    label: "Goal Snooze",
    description: "Deliberately wait before resuming an active goal when snooze is enabled.",
    parameters: {
      type: "object",
      additionalProperties: false,
      properties: {},
    },
    async execute(_toolCallId, _params, _signal, _onUpdate, ctx) {
      if (!isCurrentSession(undefined, ctx)) {
        recordWakeupGateRejection("stale_session", ctx, { component: "goal-snooze" });
        return { content: [{ type: "text", text: "Goal snooze is unavailable in this session." }], isError: true };
      }
      if (!snoozeEnabled) {
        recordWakeupGateRejection("snooze_disabled", ctx, { component: "goal-snooze" });
        return { content: [{ type: "text", text: "Goal snooze is not enabled. Use /goal-snooze first." }], isError: true };
      }
      if (!state.enabled || !state.goal) {
        recordWakeupGateRejection("inactive_goal", ctx, { component: "goal-snooze" });
        return { content: [{ type: "text", text: "Goal snooze needs an active goal." }], isError: true };
      }
      if (pendingSnooze || pendingSnoozeWakeup !== undefined || deferredSnoozeWakeup !== undefined) {
        recordWakeupGateRejection("snooze_already_waiting", ctx, { component: "goal-snooze" });
        return { content: [{ type: "text", text: "Goal snooze is already waiting." }], isError: true };
      }
      refreshSnoozeBudget();
      if (snoozeBudgetCapMs !== undefined && snoozeBudgetAvailableMs <= 0) {
        recordWakeupGateRejection("snooze_budget_exhausted", ctx, { component: "goal-snooze" });
        return { content: [{ type: "text", text: "Goal snooze budget is exhausted. Wait for a refill." }], isError: true };
      }
      if (notDoneWakeups >= currentWakeupLimit()) {
        recordWakeupGateRejection("retry_limit", ctx, { component: "goal-snooze" });
        return { content: [{ type: "text", text: "Goal snooze ended. The goal retry limit has been reached." }] };
      }
      const goal = state.goal;
      const runGeneration = generation;
      if (_signal?.aborted) {
        recordWakeupGateRejection("aborted", ctx, { component: "goal-snooze" });
        return { content: [{ type: "text", text: "Goal snooze was canceled." }], isError: true };
      }
      scheduleSnooze(snoozeDurationMs, _signal, (waited) => {
        if (!waited || runGeneration !== generation || !snoozeEnabled || !state.enabled || state.goal !== goal) {
          recordWakeupGateRejection(waited ? "stale_snooze" : "aborted", ctx, { component: "goal-snooze" });
          return;
        }
        if (notDoneWakeups >= currentWakeupLimit()) {
          recordWakeupGateRejection("retry_limit", ctx, { component: "goal-snooze" });
          return;
        }
        recordNotDoneWakeup();
        const reachedCustomLimit = snoozeTurnLimit !== undefined && notDoneWakeups >= snoozeTurnLimit;
        const exhaustedBudget = snoozeBudgetCapMs !== undefined
          && snoozeBudgetAvailableMs <= 0
          && snoozeRefillYieldMs === undefined;
        persist();
        scheduleSnoozeWakeup(goal, runGeneration, notDoneWakeup(goal, notDoneWakeups), ctx, reachedCustomLimit || exhaustedBudget);
        if (reachedCustomLimit || exhaustedBudget) deactivateSnooze({ preservePendingWakeup: true });
      });
      return { content: [{ type: "text", text: "Goal snooze scheduled. The next goal wakeup will be delivered after the configured delay." }] };
    },
  });

  pi.registerCommand("goal", {
    description: "Set, inspect, or disable the active goal evaluator",
    handler: async (args, ctx) => {
      if (!isCurrentSession(undefined, ctx)) return;
      const input = typeof args === "string" ? args.trim() : "";
      const [command, ...rest] = input.split(/\s+/);
      const result = applyAction(command, rest.join(" "), ctx);
      if (result.ok) {
        notify(ctx, result.message);
        return;
      }
      notify(ctx, command === "set" ? "Usage: /goal set <goal>" : "Usage: /goal set <goal> | /goal status | /goal off", "error");
    },
  });

  pi.registerCommand("goal-lock", {
    description: "Enable or disable protection against programmatic goal changes",
    handler: async (args, ctx) => {
      if (!isCurrentSession(undefined, ctx)) return;
      const result = configureGoalLock(typeof args === "string" ? args.trim() : "");
      notify(ctx, result.message, result.ok ? "info" : "error");
    },
  });

  pi.registerCommand("goal-snooze", {
    description: "Enable or disable deliberate goal snoozing",
    handler: async (args, ctx) => {
      if (!isCurrentSession(undefined, ctx)) return;
      const input = typeof args === "string" ? args.trim() : "";
      notify(ctx, configureSnooze(input, ctx));
    },
  });

  pi.registerCommand("goal-snooze-turn-limit", {
    description: "Set the ephemeral goal snooze turn limit",
    handler: async (args, ctx) => {
      if (!isCurrentSession(undefined, ctx)) return;
      const result = configureSnoozeTurnLimit(typeof args === "string" ? args.trim() : "");
      notify(ctx, result.message, result.ok ? "info" : "error");
    },
  });

  pi.registerCommand("goal-snooze-budget", {
    description: "Set or clear the ephemeral goal snooze time budget",
    handler: async (args, ctx) => {
      if (!isCurrentSession(undefined, ctx)) return;
      const result = configureSnoozeBudget(typeof args === "string" ? args.trim() : "");
      notify(ctx, result.message, result.ok ? "info" : "error");
    },
  });

  pi.registerCommand("goal-snooze-budget-refill", {
    description: "Set the ephemeral goal snooze budget refill rate",
    handler: async (args, ctx) => {
      if (!isCurrentSession(undefined, ctx)) return;
      const result = configureSnoozeRefill(typeof args === "string" ? args.trim() : "");
      notify(ctx, result.message, result.ok ? "info" : "error");
    },
  });

  pi.on("agent_settled", (event, ctx) => {
    if (!isCurrentSession(event, ctx)) {
      recordWakeupGateRejection("stale_session", ctx, { component: "agent-settled" });
      return;
    }
    if (!compactionPending(ctx)) resumePendingSelfWake(ctx);
    if (!state.enabled || !state.goal || !ctx.model) {
      recordWakeupGateRejection("inactive_goal_or_model", ctx, { component: "agent-settled" });
      return;
    }
    if (compactionPending(ctx)) {
      recordWakeupGateRejection("compaction_pending", ctx, { component: "agent-settled", state: "compacting" });
      return;
    }
    resumeDeferredSnoozeWakeup(ctx);
    if (pendingSnooze || pendingSnoozeWakeup !== undefined || deferredSnoozeWakeup !== undefined) {
      recordWakeupGateRejection("snooze_already_waiting", ctx, { component: "agent-settled" });
      return;
    }
    if (snoozeEnabled) scheduleSnoozeAvailability(ctx);
    if (ownerActivityState(ctx) === "suppressed") {
      recordWakeupGateRejection("owner_children_running", ctx, { component: "agent-settled", state: "suppressed" });
      return;
    }
    if (deferredEvaluation !== undefined || activeAbort) {
      recordWakeupGateRejection("evaluation_already_pending", ctx, { component: "agent-settled" });
      return;
    }
    scheduleDeferredEvaluation(event, ctx, generation, state.goal);
  });
}

export default createGoalExtension;
