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
const OUTCOMES = new Set(["COMPLETED", "NOT_DONE"]);
const GOAL_ACTIONS = new Set(["set", "status", "off"]);
const GOAL_CONTROL_ACTIONS = new Set(["set", "off"]);
const OUTCOME_COUNT_KEYS = ["COMPLETED", "NOT_DONE"];
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
const MAX_SEEN_ACTIVITY_IDS = 512;
const COMPLETIONS_TO_RESET_WAKEUPS = 3;
const DEFAULT_SNOOZE_DURATION_MS = 60_000;
const DEFAULT_SNOOZE_REFILL_YIELD_MS = 30_000;
const DEFAULT_SNOOZE_REFILL_ELAPSED_MS = 60_000;
const EVALUATOR_PROMPT = "goal: <goal description> - return exactly two lines: VERDICT: COMPLETED|NOT_DONE and REASON: <concise rationale>";
const PI_SUBAGENTS_SERVICE_REGISTRY = Symbol.for("@gotgenes/pi-subagents:service-registry");

function normalizeGoal(value) {
  if (typeof value !== "string") return "";
  return value.trim().slice(0, MAX_GOAL_CHARS);
}

function compactToolCallAction(toolCall) {
  if (!toolCall || toolCall.type !== "toolCall" || toolCall.name !== "mecha_context") return false;
  let argumentsValue = toolCall.arguments;
  if (typeof argumentsValue === "string") {
    try { argumentsValue = JSON.parse(argumentsValue); } catch { return false; }
  }
  return argumentsValue?.action === "compact";
}

export function hasPendingCompaction(ctx) {
  const pendingToolCallIds = new Set();
  for (const entry of ctx?.sessionManager?.getBranch?.() ?? []) {
    const message = entry?.type === "message" ? entry.message : undefined;
    if (message?.role === "assistant" && Array.isArray(message.content)) {
      for (const toolCall of message.content) {
        if (compactToolCallAction(toolCall) && typeof toolCall.id === "string") pendingToolCallIds.add(toolCall.id);
      }
    } else if (message?.role === "toolResult" && typeof message.toolCallId === "string") {
      pendingToolCallIds.delete(message.toolCallId);
    }
  }
  return pendingToolCallIds.size > 0;
}

function parsePositiveDuration(value) {
  const match = typeof value === "string" ? value.trim().match(/^([1-9]\d*)([sm])$/) : undefined;
  if (!match) return undefined;
  const amount = Number(match[1]);
  const durationMs = amount * (match[2] === "m" ? 60_000 : 1_000);
  return Number.isSafeInteger(amount) && Number.isSafeInteger(durationMs) && durationMs > 0
    ? durationMs
    : undefined;
}

function snoozeDurationFromInput(value) {
  return parsePositiveDuration(value) ?? DEFAULT_SNOOZE_DURATION_MS;
}

function parseSnoozeTurnLimit(value) {
  const input = typeof value === "string" ? value.trim() : "";
  if (!/^\d+$/.test(input)) return undefined;
  const limit = Number(input);
  return Number.isInteger(limit) && limit >= 1 && limit <= MAX_SNOOZE_TURN_LIMIT ? limit : undefined;
}

function goalProjection(state) {
  return state.enabled === true && state.goal.length > 0
    ? { enabled: true, goal: state.goal }
    : { enabled: false, goal: "" };
}

function emptyOutcomeCounts() {
  return Object.fromEntries(OUTCOME_COUNT_KEYS.map((outcome) => [outcome, 0]));
}

function restoreOutcomeCounts(value) {
  const counts = emptyOutcomeCounts();
  if (!value || typeof value !== "object" || Array.isArray(value)) return counts;
  for (const outcome of OUTCOME_COUNT_KEYS) {
    if (Number.isSafeInteger(value[outcome]) && value[outcome] >= 0) counts[outcome] = value[outcome];
  }
  return counts;
}

function restoreAppliedGoalControl(value) {
  return value && typeof value === "object"
    && typeof value.instanceId === "string" && value.instanceId.length > 0
    && Number.isSafeInteger(value.revision) && value.revision > 0
    ? { instanceId: value.instanceId, revision: value.revision }
    : undefined;
}

function restoreRearmScope(value) {
  if (!value || typeof value !== "object"
    || typeof value.sessionId !== "string" || value.sessionId.length === 0
    || typeof value.goal !== "string" || normalizeGoal(value.goal).length === 0
    || !Number.isSafeInteger(value.activityBaselineRevision) || value.activityBaselineRevision < 0
    || (value.workerId !== undefined && (typeof value.workerId !== "string" || value.workerId.length === 0))
    || (value.producerEpoch !== undefined && (typeof value.producerEpoch !== "string" || value.producerEpoch.length === 0))) {
    return undefined;
  }
  return {
    sessionId: value.sessionId,
    goal: normalizeGoal(value.goal),
    activityBaselineRevision: value.activityBaselineRevision,
    ...(value.workerId === undefined ? {} : { workerId: value.workerId }),
    ...(value.producerEpoch === undefined ? {} : { producerEpoch: value.producerEpoch }),
  };
}

function positiveSafeInteger(value) {
  return Number.isSafeInteger(value) && value > 0 ? value : undefined;
}

function normalizeGoalControlSettings(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const required = [
    "goalLockEnabled",
    "snoozeEnabled",
    "snoozeDurationMs",
    "snoozeTurnLimit",
    "snoozeBudgetCapMs",
    "snoozeRefillYieldMs",
    "snoozeRefillElapsedMs",
  ];
  if (Object.keys(value).some((key) => !required.includes(key))
    || required.some((key) => !Object.hasOwn(value, key))) return undefined;
  const snoozeTurnLimit = value.snoozeTurnLimit === null ? undefined : parseSnoozeTurnLimit(String(value.snoozeTurnLimit));
  const snoozeBudgetCapMs = value.snoozeBudgetCapMs === null ? undefined : positiveSafeInteger(value.snoozeBudgetCapMs);
  if (typeof value.goalLockEnabled !== "boolean" || typeof value.snoozeEnabled !== "boolean"
    || positiveSafeInteger(value.snoozeDurationMs) === undefined
    || (value.snoozeTurnLimit !== null && snoozeTurnLimit === undefined)
    || (value.snoozeBudgetCapMs !== null && snoozeBudgetCapMs === undefined)
    || positiveSafeInteger(value.snoozeRefillYieldMs) === undefined
    || positiveSafeInteger(value.snoozeRefillElapsedMs) === undefined) return undefined;
  return {
    goalLockEnabled: value.goalLockEnabled,
    snoozeEnabled: value.snoozeEnabled,
    snoozeDurationMs: value.snoozeDurationMs,
    snoozeTurnLimit,
    snoozeBudgetCapMs,
    snoozeRefillYieldMs: value.snoozeRefillYieldMs,
    snoozeRefillElapsedMs: value.snoozeRefillElapsedMs,
  };
}

function restoreState(ctx) {
  let restored = {
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
    if (!data || typeof data !== "object") continue;
    const snoozeBudgetCapMs = positiveSafeInteger(data.snoozeBudgetCapMs);
    restored = {
      enabled: data.enabled === true,
      goal: normalizeGoal(data.goal),
      notDoneWakeups: Number.isInteger(data.notDoneWakeups) && data.notDoneWakeups >= 0
        ? Math.min(data.notDoneWakeups, MAX_SNOOZE_TURN_LIMIT)
        : 0,
      completedThings: Number.isInteger(data.completedThings) && data.completedThings >= 0
        ? data.completedThings % COMPLETIONS_TO_RESET_WAKEUPS
        : 0,
      goalLockEnabled: data.goalLockEnabled === true,
      snoozeEnabled: !Object.hasOwn(data, "snoozeEnabled") || data.snoozeEnabled === true,
      snoozeDurationMs: positiveSafeInteger(data.snoozeDurationMs) ?? DEFAULT_SNOOZE_DURATION_MS,
      snoozeTurnLimit: Number.isInteger(data.snoozeTurnLimit) && data.snoozeTurnLimit >= 1
        && data.snoozeTurnLimit <= MAX_SNOOZE_TURN_LIMIT ? data.snoozeTurnLimit : undefined,
      snoozeBudgetCapMs,
      snoozeBudgetAvailableMs: snoozeBudgetCapMs !== undefined && Number.isFinite(data.snoozeBudgetAvailableMs)
        ? Math.min(snoozeBudgetCapMs, Math.max(0, data.snoozeBudgetAvailableMs))
        : snoozeBudgetCapMs ?? 0,
      snoozeRefillYieldMs: positiveSafeInteger(data.snoozeRefillYieldMs) ?? DEFAULT_SNOOZE_REFILL_YIELD_MS,
      snoozeRefillElapsedMs: positiveSafeInteger(data.snoozeRefillElapsedMs) ?? DEFAULT_SNOOZE_REFILL_ELAPSED_MS,
      outcomeCounts: restoreOutcomeCounts(data.outcomeCounts),
      rearmActive: data.rearmActive === true,
      rearmActivityCount: Number.isInteger(data.rearmActivityCount) && data.rearmActivityCount >= 0
        ? Math.min(data.rearmActivityCount, MEANINGFUL_ACTIVITY_EVENTS_TO_REARM - 1)
        : 0,
      rearmScope: restoreRearmScope(data.rearmScope),
      appliedGoalControl: restoreAppliedGoalControl(data.appliedGoalControl),
    };
  }
  return restored;
}

function textFromMessage(message) {
  if (!message || !["user", "assistant", "toolResult"].includes(message.role)) return "";
  if (typeof message.content === "string") return message.content;
  if (!Array.isArray(message.content)) return "";
  return message.content
    .filter((part) => part?.type === "text" && typeof part.text === "string")
    .map((part) => part.text)
    .join("\n");
}

function boundedActiveContext(ctx) {
  const entries = ctx.sessionManager.getBranch?.() ?? [];
  const messages = [];
  let characters = 0;
  for (let index = entries.length - 1; index >= 0 && messages.length < MAX_CONTEXT_MESSAGES; index -= 1) {
    const entry = entries[index];
    if (entry.type !== "message") continue;
    const text = textFromMessage(entry.message);
    if (!text) continue;
    const remaining = MAX_CONTEXT_CHARS - characters;
    if (remaining <= 0) break;
    const boundedText = text.slice(-remaining);
    messages.unshift({
      role: entry.message.role,
      content: [{ type: "text", text: boundedText }],
      timestamp: entry.message.timestamp ?? Date.now(),
    });
    characters += boundedText.length;
  }
  return messages;
}

function responseText(response) {
  return Array.isArray(response?.content)
    ? response.content
      .filter((part) => part?.type === "text" && typeof part.text === "string")
      .map((part) => part.text)
      .join("")
    : "";
}

function responseEvaluation(response) {
  const lines = responseText(response).split(/\r?\n/);
  const verdict = lines.length === 2
    ? lines[0].match(/^VERDICT: (COMPLETED|NOT_DONE)$/)?.[1]
    : undefined;
  const rawReason = lines.length === 2 ? lines[1].match(/^REASON: (.*)$/)?.[1] : undefined;
  if (!OUTCOMES.has(verdict) || !rawReason || rawReason.trim().length === 0 || rawReason.length > MAX_RATIONALE_CHARS) {
    return { outcome: "NOT_DONE", rationale: "Invalid evaluator output: " + JSON.stringify(response) };
  }
  return {
    outcome: verdict,
    rationale: rawReason.trim().replace(/\s+/g, " "),
  };
}

function goalResultContent(wakeup, outcome, rationale) {
  return `${wakeup}\nVERDICT: ${outcome}\nREASON: ${rationale}`;
}

function notDoneWakeup(goal, attempt) {
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

function responseOutcome(response) {
  return responseEvaluation(response).outcome;
}

function errorDetails(error) {
  if (error instanceof Error) return error.stack ?? `${error.name}: ${error.message}`;
  try {
    return JSON.stringify(error);
  } catch {
    return String(error);
  }
}

async function completeEvaluator(ctx, request, signal) {
  const registry = ctx.modelRegistry;
  const provider = registry?.getProvider?.(ctx.model?.provider);
  if (typeof provider?.streamSimple === "function") {
    const auth = await registry.getApiKeyAndHeaders(ctx.model);
    if (!auth.ok) throw new Error(auth.error);
    return provider.streamSimple(ctx.model, request, {
      signal,
      apiKey: auth.apiKey,
      headers: auth.headers,
      env: auth.env,
    }).result();
  }
  if (typeof registry?.complete === "function") {
    return registry.complete(ctx.model, request, { signal });
  }
  throw new Error("Model registry cannot evaluate the active goal");
}

export { boundedActiveContext, responseEvaluation, responseOutcome };

export function createGoalExtension(pi, options = {}) {
  const clockNow = typeof options.clock?.now === "function"
    ? () => options.clock.now()
    : () => Date.now();
  const setTimer = options.setTimeout ?? options.timer?.setTimeout ?? setTimeout;
  const clearTimer = options.clearTimeout ?? options.timer?.clearTimeout ?? clearTimeout;
  let sessionId = pi.getSessionId?.();
  let sessionContext;
  let state = { enabled: false, goal: "" };
  const isCurrentSession = (event, ctx) => {
    const contextSessionId = ctx.sessionManager?.getSessionId?.();
    return (sessionId === undefined || event?.sessionId === undefined || event.sessionId === sessionId)
      && (sessionId === undefined || contextSessionId === undefined || contextSessionId === sessionId);
  };
  let generation = 0;
  let deferredEvaluation;
  let deferredEvaluationRequest;
  let activeAbort;
  let activeEvaluationRequest;
  let pendingSelfWake;
  let pendingSnooze;
  let pendingSnoozeAvailability;
  let pendingSnoozeWakeup;
  let deferredSnoozeWakeup;
  let snoozeAvailabilityInjected = false;
  let evaluatorCalls = 0;
  let notDoneWakeups = 0;
  let completedThings = 0;
  let goalLockEnabled = false;
  let completedOffAllowance = false;
  let snoozeEnabled = true;
  let snoozeDurationMs = DEFAULT_SNOOZE_DURATION_MS;
  let snoozeTurnLimit;
  let snoozeBudgetCapMs;
  let snoozeBudgetAvailableMs = 0;
  let snoozeBudgetLastAtMs;
  let snoozeRefillYieldMs = DEFAULT_SNOOZE_REFILL_YIELD_MS;
  let snoozeRefillElapsedMs = DEFAULT_SNOOZE_REFILL_ELAPSED_MS;
  let outcomeCounts = emptyOutcomeCounts();
  let appliedGoalControl;
  let rearmActive = false;
  let rearmActivityCount = 0;
  let rearmScope;
  let currentWorkerId;
  let currentActivityProducerEpoch;
  let latestActivityRevision = 0;
  const seenActivityIds = new Set();
  let compactionLifecyclePause = false;
  let lastOwnerGateState = "allowed";

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
  const setSnoozeBudget = (durationMs) => {
    snoozeBudgetCapMs = durationMs;
    snoozeBudgetAvailableMs = durationMs;
    snoozeBudgetLastAtMs = clockNow();
  };
  const setSnoozeRefill = (yieldMs, elapsedMs) => {
    refreshSnoozeBudget();
    snoozeRefillYieldMs = yieldMs;
    snoozeRefillElapsedMs = elapsedMs;
    snoozeBudgetLastAtMs = clockNow();
  };
  const pauseBudgetAt = (timestamp) => {
    if (snoozeBudgetCapMs !== undefined && Number.isFinite(timestamp)) snoozeBudgetLastAtMs = timestamp;
  };
  const consumeSnoozeBudget = (pending, completedAt) => {
    if (snoozeBudgetCapMs === undefined) return;
    const elapsed = Math.max(0, completedAt - pending.startedAt);
    // A positive balance admits a wait. The full completed interval is charged,
    // so a short refill can leave debt that later refill periods repay.
    snoozeBudgetAvailableMs -= elapsed;
    pauseBudgetAt(completedAt);
  };
  const settleSnooze = (pending, completed) => {
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
  const scheduleSnooze = (durationMs, signal, onSettled) => {
    const pending = { signal, onAbort: undefined, onSettled, startedAt: clockNow(), timeout: undefined };
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
  const rememberActivityId = (activityId) => {
    seenActivityIds.delete(activityId);
    seenActivityIds.add(activityId);
    while (seenActivityIds.size > MAX_SEEN_ACTIVITY_IDS) {
      seenActivityIds.delete(seenActivityIds.keys().next().value);
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
    try { pi.events?.emit?.(GOAL_STATE_EVENT, goalProjection(state)); } catch { /* optional UI publication is best-effort */ }
    try { pi.events?.emit?.(GOAL_CONTROL_STATE_EVENT, goalControlState()); } catch { /* optional control publication is best-effort */ }
  };
  const recordOutcome = (outcome) => {
    if (!OUTCOMES.has(outcome)) return;
    outcomeCounts[outcome] += 1;
    persist();
    publish();
  };
  const recordMeaningfulActivity = (activity) => {
    if (!activity || typeof activity !== "object" || Array.isArray(activity)) return;
    const activityId = typeof activity.activityId === "string" ? activity.activityId : "";
    const session = typeof activity.sessionId === "string" ? activity.sessionId : "";
    const worker = typeof activity.workerId === "string" ? activity.workerId : undefined;
    const producerEpoch = typeof activity.producerEpoch === "string" ? activity.producerEpoch : undefined;
    const revision = activity.revision;
    const activityGoal = activity.goal;
    if (!MEANINGFUL_ACTIVITY_KINDS.has(activity.kind)
      || activityId.length === 0 || session.length === 0 || !Number.isSafeInteger(revision) || revision < 1
      || session !== sessionId
      || seenActivityIds.has(activityId)
      || !activityGoal || activityGoal.enabled !== true || normalizeGoal(activityGoal.goal) !== state.goal
      || (rearmActive && rearmScope?.workerId !== undefined && worker !== rearmScope.workerId)) return;
    latestActivityRevision = Math.max(latestActivityRevision, revision);
    rememberActivityId(activityId);
    if (!rearmActive || !state.enabled || !state.goal || !rearmScope
      || rearmScope.sessionId !== session || rearmScope.goal !== state.goal
      || (rearmScope.workerId !== undefined && worker !== rearmScope.workerId)
      || (rearmScope.producerEpoch !== undefined && producerEpoch !== rearmScope.producerEpoch)) return;
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
  const scheduleSelfWake = (goal, ctx) => {
    if (typeof sessionId !== "string" || sessionId.length === 0) return;
    const pending = { goal, ctx, runGeneration: generation, timeout: undefined };
    pendingSelfWake = pending;
    pending.timeout = setTimer(() => {
      if (pendingSelfWake !== pending) return;
      pending.timeout = undefined;
      if (pending.runGeneration !== generation || !state.enabled || state.goal !== goal || !isCurrentSession(undefined, ctx)) {
        pendingSelfWake = undefined;
        return;
      }
      if (compactionPending(ctx)) return;
      pendingSelfWake = undefined;
      try {
        pi.sendMessage({
          customType: GOAL_UPDATE_TYPE,
          content: `Goal set: ${goal}. Begin working toward this goal.`,
          display: true,
          details: { enabled: true, goal },
        }, { deliverAs: "steer", triggerTurn: true });
      } catch { /* persistence remains authoritative when the session cannot wake */ }
    }, 0);
  };
  const notify = (ctx, message, level = "info") => ctx.ui?.notify?.(message, level);
  const compactionPending = (ctx) => compactionLifecyclePause || hasPendingCompaction(ctx);
  const resumePendingSelfWake = (ctx) => {
    const pending = pendingSelfWake;
    if (!pending || compactionPending(ctx) || pending.runGeneration !== generation
      || !state.enabled || state.goal !== pending.goal || !isCurrentSession(undefined, ctx)) return;
    pendingSelfWake = undefined;
    try {
      pi.sendMessage({
        customType: GOAL_UPDATE_TYPE,
        content: `Goal set: ${pending.goal}. Begin working toward this goal.`,
        display: true,
        details: { enabled: true, goal: pending.goal },
      }, { deliverAs: "steer", triggerTurn: true });
    } catch { /* persistence remains authoritative when the session cannot wake */ }
  };
  const scheduleSnoozeAvailability = (ctx) => {
    if (snoozeAvailabilityInjected || pendingSnoozeAvailability !== undefined) return;
    const runGeneration = generation;
    pendingSnoozeAvailability = setTimer(() => {
      pendingSnoozeAvailability = undefined;
      if (runGeneration !== generation || !state.enabled || !state.goal || !snoozeEnabled
        || notDoneWakeups >= currentWakeupLimit() || compactionPending(ctx)) return;
      try {
        pi.sendMessage({
          customType: GOAL_SNOOZE_AVAILABILITY_TYPE,
          content: "Goal snooze is available. goal_snooze can be used to deliberately wait before the next goal wakeup. Only use this if you need to wait for something, otherwise continue working toward the goal.",
          display: true,
          details: { goal: state.goal, durationMs: snoozeDurationMs },
        }, { deliverAs: "steer", triggerTurn: true });
        snoozeAvailabilityInjected = true;
      } catch { /* availability remains optional when the session cannot wake */ }
    }, 0);
  };
  const scheduleSnoozeWakeup = (goal, runGeneration, wakeup, ctx, allowAfterDisable = false) => {
    const timeout = setTimer(() => {
      if (pendingSnoozeWakeup === timeout) pendingSnoozeWakeup = undefined;
      if (runGeneration !== generation || !state.enabled || state.goal !== goal
        || !isCurrentSession(undefined, ctx) || (!snoozeEnabled && !allowAfterDisable)) return;
      if (compactionPending(ctx)) {
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
        console.error(error);
      }
    }, 0);
    pendingSnoozeWakeup = timeout;
  };
  const resumeDeferredSnoozeWakeup = (ctx) => {
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
  const configureSnooze = (input, ctx) => {
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
  const configureSnoozeTurnLimit = (input) => {
    const limit = parseSnoozeTurnLimit(input);
    if (limit === undefined) return { ok: false, message: "Usage: /goal-snooze-turn-limit <1-20>" };
    snoozeTurnLimit = limit;
    persist();
    publish();
    return { ok: true, message: `Goal snooze turn limit set: ${limit}` };
  };
  const configureSnoozeBudget = (input) => {
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
  const configureSnoozeRefill = (input) => {
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
  const configureGoalLock = (input) => {
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
  const recordNotDoneWakeup = () => {
    notDoneWakeups += 1;
    if (attemptCapReached()) armRearmState();
    persist();
    return () => {
      notDoneWakeups = Math.max(0, notDoneWakeups - 1);
      if (rearmActive && !attemptCapReached()) clearRearmState();
      persist();
    };
  };
  const statusDetails = (action) => ({
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
  const ownerActivityState = (ctx) => {
    // pi-subagents publishes one service per owning session. Resolve it at
    // settle time because its session_start handler may publish after this
    // extension's handler, and never let another owner's children gate us.
    const ownerSessionId = ctx?.sessionManager?.getSessionId?.() ?? sessionId;
    const services = globalThis[PI_SUBAGENTS_SERVICE_REGISTRY]?.services;
    const service = typeof ownerSessionId === "string" && typeof services?.get === "function"
      ? services.get(ownerSessionId)
      : undefined;
    if (typeof service?.hasRunning !== "function") {
      lastOwnerGateState = "allowed";
      return lastOwnerGateState;
    }
    try {
      lastOwnerGateState = service.hasRunning() === true ? "suppressed" : "allowed";
    } catch {
      lastOwnerGateState = "allowed";
    }
    return lastOwnerGateState;
  };
  const applyAction = (action, rawGoal, ctx, { programmatic = false, beforePersist } = {}) => {
    if (!GOAL_ACTIONS.has(action)) {
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
  const applyGoalControl = (request, ctx) => {
    const reject = (errorCode, message) => ({ accepted: false, errorCode, message });
    if (!request || typeof request !== "object" || !isCurrentSession(undefined, ctx)) {
      return reject("unavailable", "Goal is unavailable in this session.");
    }
    const { action, controlId, targetInstanceId, revision } = request;
    if (!GOAL_CONTROL_ACTIONS.has(action) || typeof controlId !== "string" || controlId.length === 0
      || typeof targetInstanceId !== "string" || targetInstanceId.length === 0
      || !Number.isSafeInteger(revision) || revision < 1) {
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
  const scheduleInjection = (event, ctx, runGeneration, controller, message, beforeSend) => {
    if (runGeneration !== generation || controller.signal.aborted || !isCurrentSession(event, ctx)) return;
    setTimer(() => {
      if (runGeneration !== generation || controller.signal.aborted || !isCurrentSession(event, ctx) || !state.enabled || !state.goal) return;
      // Re-check in the wakeup macrotask. The evaluator result is stale if an
      // owner child was admitted after complete() returned.
      if (compactionPending(ctx) || ownerActivityState(ctx) === "suppressed") return;
      let undoBeforeSend;
      try {
        undoBeforeSend = beforeSend?.();
        pi.sendMessage(message, { deliverAs: "steer", triggerTurn: true });
      } catch(e) {
        undoBeforeSend?.();
        console.error(e);
      }
    }, 0);
  };
  const scheduleDeferredEvaluation = (event, ctx, runGeneration, goal) => {
    if (deferredEvaluation !== undefined || activeAbort) return;
    const pending = { event, ctx, runGeneration, goal };
    deferredEvaluationRequest = pending;
    deferredEvaluation = setTimer(async () => {
      deferredEvaluation = undefined;
      if (deferredEvaluationRequest !== pending) return;
      deferredEvaluationRequest = undefined;
      if (runGeneration !== generation || !isCurrentSession(event, ctx) || !state.enabled || state.goal !== goal || !ctx.model) return;
      if (compactionPending(ctx)) {
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
        if (compactionPending(ctx) || ownerActivityState(ctx) === "suppressed") return;
        evaluatorCalls = Math.min(evaluatorCalls + 1, MAX_EVALUATOR_CALLS);
        const request = {
          systemPrompt: EVALUATOR_PROMPT.replace("<goal description>", goal),
          messages: [{
            role: "user",
            content: [{ type: "text", text: `Goal: ${goal}\n\nActive conversation:\n${boundedActiveContext(ctx).map((message) => `${message.role}: ${message.content[0].text}`).join("\n\n")}\n\nReturn exactly two lines: VERDICT: COMPLETED|NOT_DONE and REASON: <concise rationale>.` }],
            timestamp: Date.now(),
          }],
        };
        const response = await completeEvaluator(ctx, request, controller.signal);
        if (runGeneration !== generation || controller.signal.aborted) return;
        if (compactionPending(ctx)) {
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
        if (outcome === "NOT_DONE" && notDoneWakeups >= currentWakeupLimit()) return;
        let undoNotDoneWakeup;
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
          deferredEvaluationRequest = pending;
          return;
        }
        if (runGeneration !== generation || controller.signal.aborted) return;
        recordOutcome("NOT_DONE");
        if (notDoneWakeups >= currentWakeupLimit()) return;
        let undoNotDoneWakeup;
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
  const resumeDeferredEvaluation = (ctx) => {
    const pending = deferredEvaluationRequest;
    if (!pending || compactionPending(ctx) || deferredEvaluation !== undefined || activeAbort
      || pending.runGeneration !== generation || !isCurrentSession(pending.event, pending.ctx)
      || !state.enabled || state.goal !== pending.goal) return;
    deferredEvaluationRequest = undefined;
    scheduleDeferredEvaluation(pending.event, pending.ctx, pending.runGeneration, pending.goal);
  };

  pi.events?.on?.(GOAL_STATE_REQUEST_EVENT, () => publish());
  pi.events?.on?.(GOAL_WORKER_ID_EVENT, (identity) => {
    if (!identity || typeof identity !== "object" || Array.isArray(identity)
      || (identity.sessionId !== undefined && identity.sessionId !== sessionId)
      || typeof identity.workerId !== "string" || identity.workerId.length === 0) return;
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
    const result = applyGoalControl(request, request?.context ?? sessionContext);
    try { request?.respond?.(result); } catch { /* the router can time out a disconnected source */ }
  });

  pi.on("session_start", async (event, ctx) => {
    if (sessionId === undefined) sessionId = event?.sessionId ?? ctx.sessionManager?.getSessionId?.();
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
    if (rearmActive && rearmScope.sessionId !== sessionId) {
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
    event?.signal?.addEventListener?.("abort", () => {
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
        return { content: [{ type: "text", text: "Goal snooze is unavailable in this session." }], isError: true };
      }
      if (!snoozeEnabled) {
        return { content: [{ type: "text", text: "Goal snooze is not enabled. Use /goal-snooze first." }], isError: true };
      }
      if (!state.enabled || !state.goal) {
        return { content: [{ type: "text", text: "Goal snooze needs an active goal." }], isError: true };
      }
      if (pendingSnooze || pendingSnoozeWakeup !== undefined || deferredSnoozeWakeup !== undefined) {
        return { content: [{ type: "text", text: "Goal snooze is already waiting." }], isError: true };
      }
      refreshSnoozeBudget();
      if (snoozeBudgetCapMs !== undefined && snoozeBudgetAvailableMs <= 0) {
        return { content: [{ type: "text", text: "Goal snooze budget is exhausted. Wait for a refill." }], isError: true };
      }
      if (notDoneWakeups >= currentWakeupLimit()) {
        return { content: [{ type: "text", text: "Goal snooze ended. The goal retry limit has been reached." }] };
      }
      const goal = state.goal;
      const runGeneration = generation;
      if (_signal?.aborted) {
        return { content: [{ type: "text", text: "Goal snooze was canceled." }], isError: true };
      }
      scheduleSnooze(snoozeDurationMs, _signal, (waited) => {
        if (!waited || runGeneration !== generation || !snoozeEnabled || !state.enabled || state.goal !== goal) return;
        if (notDoneWakeups >= currentWakeupLimit()) return;
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
    if (!isCurrentSession(event, ctx)) return;
    if (!compactionPending(ctx)) resumePendingSelfWake(ctx);
    if (!state.enabled || !state.goal || !ctx.model) return;
    if (compactionPending(ctx)) return;
    resumeDeferredSnoozeWakeup(ctx);
    if (pendingSnooze || pendingSnoozeWakeup !== undefined || deferredSnoozeWakeup !== undefined) return;
    if (snoozeEnabled) scheduleSnoozeAvailability(ctx);
    if (ownerActivityState(ctx) === "suppressed" || deferredEvaluation !== undefined || activeAbort) return;
    scheduleDeferredEvaluation(event, ctx, generation, state.goal);
  });
}

export default createGoalExtension;
