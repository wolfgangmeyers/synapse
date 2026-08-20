const GOAL_STATE_ENTRY = "goal-state";
const GOAL_RESULT_TYPE = "goal-result";
const GOAL_UPDATE_TYPE = "goal-update";
export const GOAL_STATE_EVENT = "goal:state";
export const GOAL_STATE_REQUEST_EVENT = "goal:state-request";
const MAX_GOAL_CHARS = 4_000;
const MAX_CONTEXT_MESSAGES = 32;
const MAX_CONTEXT_CHARS = 24_000;
const OUTCOMES = new Set(["BLOCKED", "COMPLETED", "NOT_DONE"]);
const GOAL_ACTIONS = new Set(["set", "status", "off"]);
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
const EVALUATOR_PROMPT = "goal: <goal description> - return exactly two lines: VERDICT: BLOCKED|COMPLETED|NOT_DONE and REASON: <concise rationale>";
const PI_SUBAGENTS_SERVICE_REGISTRY = Symbol.for("@gotgenes/pi-subagents:service-registry");

function normalizeGoal(value) {
  if (typeof value !== "string") return "";
  return value.trim().slice(0, MAX_GOAL_CHARS);
}

function goalProjection(state) {
  return state.enabled === true && state.goal.length > 0
    ? { enabled: true, goal: state.goal }
    : { enabled: false, goal: "" };
}

function restoreState(ctx) {
  let restored = { enabled: false, goal: "" };
  for (const entry of ctx.sessionManager.getBranch?.() ?? []) {
    if (entry.type !== "custom" || entry.customType !== GOAL_STATE_ENTRY) continue;
    const data = entry.data;
    if (!data || typeof data !== "object") continue;
    restored = {
      enabled: data.enabled === true,
      goal: normalizeGoal(data.goal),
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
    ? lines[0].match(/^VERDICT: (BLOCKED|COMPLETED|NOT_DONE)$/)?.[1]
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

export function createGoalExtension(pi) {
  let sessionId = pi.getSessionId?.();
  let state = { enabled: false, goal: "" };
  const isCurrentSession = (event, ctx) => {
    const contextSessionId = ctx.sessionManager?.getSessionId?.();
    return (sessionId === undefined || event?.sessionId === undefined || event.sessionId === sessionId)
      && (sessionId === undefined || contextSessionId === undefined || contextSessionId === sessionId);
  };
  let generation = 0;
  let skipNextSettle = false;
  let deferredEvaluation;
  let activeAbort;
  let evaluatorCalls = 0;
  let lastOwnerGateState = "allowed";

  const cancel = () => {
    generation += 1;
    skipNextSettle = false;
    if (deferredEvaluation !== undefined) clearTimeout(deferredEvaluation);
    deferredEvaluation = undefined;
    activeAbort?.abort();
    activeAbort = undefined;
  };

  const persist = () => pi.appendEntry(GOAL_STATE_ENTRY, { ...state });
  const publish = () => {
    try { pi.events?.emit?.(GOAL_STATE_EVENT, goalProjection(state)); } catch { /* optional UI publication is best-effort */ }
  };
  const scheduleSelfWake = (goal) => {
    if (typeof sessionId !== "string" || sessionId.length === 0) return;
    setTimeout(() => {
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
  const statusDetails = (action) => ({
    action,
    ...state,
    evaluatorCalls,
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
  const applyAction = (action, rawGoal) => {
    if (!GOAL_ACTIONS.has(action)) {
      return { ok: false, message: "Invalid action. Use set, status, or off." };
    }
    if (action === "set") {
      const goal = normalizeGoal(rawGoal);
      if (!goal) return { ok: false, message: "A goal description is required for set." };
      cancel();
      evaluatorCalls = 0;
      lastOwnerGateState = "allowed";
      state = { enabled: true, goal };
      persist();
      publish();
      scheduleSelfWake(goal);
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
    state = { ...state, enabled: false };
    persist();
    publish();
    return { ok: true, message: "Goal off", details: { action, ...state } };
  };
  const scheduleInjection = (event, ctx, runGeneration, controller, message) => {
    if (runGeneration !== generation || controller.signal.aborted || !isCurrentSession(event, ctx)) return;
    skipNextSettle = true;
    setTimeout(() => {
      if (runGeneration !== generation || controller.signal.aborted || !isCurrentSession(event, ctx) || !state.enabled || !state.goal) {
        if (runGeneration === generation && !controller.signal.aborted) skipNextSettle = false;
        return;
      }
      // Re-check in the wakeup macrotask. The evaluator result is stale if an
      // owner child was admitted after complete() returned.
      if (ownerActivityState(ctx) === "suppressed") {
        skipNextSettle = false;
        return;
      }
      try {
        const sendResult = pi.sendMessage(message, { deliverAs: "steer", triggerTurn: true });
        if (sendResult && typeof sendResult.then === "function") {
          Promise.resolve(sendResult).then(
            () => {
              if (runGeneration === generation && !controller.signal.aborted && isCurrentSession(event, ctx)) skipNextSettle = true;
            },
            () => {
              if (runGeneration === generation && !controller.signal.aborted && isCurrentSession(event, ctx)) skipNextSettle = false;
            },
          );
        } else {
          skipNextSettle = true;
        }
      } catch(e) {
        console.error(e);
        skipNextSettle = false;
      }
    }, 0);
  };

  pi.events?.on?.(GOAL_STATE_REQUEST_EVENT, () => publish());

  pi.on("session_start", async (event, ctx) => {
    if (sessionId === undefined) sessionId = event?.sessionId ?? ctx.sessionManager?.getSessionId?.();
    if (!isCurrentSession(event, ctx)) return;
    cancel();
    state = restoreState(ctx);
    publish();
  });

  pi.on("session_shutdown", async (event, ctx) => {
    if (!isCurrentSession(event, ctx)) return;
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
      const result = applyAction(params?.action, params?.goal);
      return {
        content: [{ type: "text", text: result.message }],
        ...(result.details ? { details: result.details } : {}),
        ...(result.ok ? {} : { isError: true }),
      };
    },
  });

  pi.registerCommand("goal", {
    description: "Set, inspect, or disable the active goal evaluator",
    handler: async (args, ctx) => {
      if (!isCurrentSession(undefined, ctx)) return;
      const input = typeof args === "string" ? args.trim() : "";
      const [command, ...rest] = input.split(/\s+/);
      const result = applyAction(command, rest.join(" "));
      if (result.ok) {
        notify(ctx, result.message);
        return;
      }
      notify(ctx, command === "set" ? "Usage: /goal set <goal>" : "Usage: /goal set <goal> | /goal status | /goal off", "error");
    },
  });

  pi.on("agent_settled", (event, ctx) => {
    if (!isCurrentSession(event, ctx) || !state.enabled || !state.goal || !ctx.model) return;
    // The steer wakeup owns the next settle. Consume that marker before the
    // optional subagent gate so a child cannot make a later normal settle skip.
    if (skipNextSettle) {
      skipNextSettle = false;
      return;
    }
    if (ownerActivityState(ctx) === "suppressed" || deferredEvaluation !== undefined || activeAbort) return;
    const runGeneration = generation;
    const goal = state.goal;
    // agent_settled can run inside Pi's turn bookkeeping. Defer the evaluator
    // to a macrotask so it observes the fully settled branch and never overlaps
    // another evaluation scheduled by the same turn.
    deferredEvaluation = setTimeout(async () => {
      deferredEvaluation = undefined;
      if (runGeneration !== generation || !isCurrentSession(event, ctx) || !state.enabled || state.goal !== goal || !ctx.model) return;
      const controller = new AbortController();
      activeAbort = controller;
      try {
        // The owner service is authoritative at this boundary. A child can be
        // admitted after the settle event, so the earlier gate cannot protect
        // the evaluator call.
        if (ownerActivityState(ctx) === "suppressed") return;
        evaluatorCalls = Math.min(evaluatorCalls + 1, MAX_EVALUATOR_CALLS);
        const request = {
          systemPrompt: EVALUATOR_PROMPT.replace("<goal description>", goal),
          messages: [{
            role: "user",
            content: [{ type: "text", text: `Goal: ${goal}\n\nActive conversation:\n${boundedActiveContext(ctx).map((message) => `${message.role}: ${message.content[0].text}`).join("\n\n")}\n\nReturn exactly two lines: VERDICT: BLOCKED|COMPLETED|NOT_DONE and REASON: <concise rationale>.` }],
            timestamp: Date.now(),
          }],
        };
        const response = await completeEvaluator(ctx, request, controller.signal);
        if (runGeneration !== generation || controller.signal.aborted) return;
        const { outcome, rationale } = responseEvaluation(response);
        const wakeup = outcome === "NOT_DONE"
          ? `Goal check: NOT_DONE. Resume working toward this goal: ${goal}.`
          : outcome === "BLOCKED"
            ? "Goal check: BLOCKED. Contact the supervisor with the blocker and what is needed."
            : "Goal check: COMPLETED. Contact the supervisor and report completion. Disable future goal notifications with /goal off.";
        scheduleInjection(event, ctx, runGeneration, controller, {
          customType: GOAL_RESULT_TYPE,
          content: goalResultContent(wakeup, outcome, rationale),
          display: true,
          details: { goal, outcome, rationale },
        });
      } catch(e) {
        if (runGeneration !== generation || controller.signal.aborted) return;
        scheduleInjection(event, ctx, runGeneration, controller, {
          customType: GOAL_RESULT_TYPE,
          content: goalResultContent(
            `Goal check: NOT_DONE. Resume working toward this goal: ${goal}.`,
            "NOT_DONE",
            "Evaluator unavailable: " + errorDetails(e),
          ),
          display: true,
          details: { goal, outcome: "NOT_DONE", rationale: "Evaluator unavailable" },
        });
      } finally {
        if (activeAbort === controller) activeAbort = undefined;
      }
    }, 0);
  });
}

export default createGoalExtension;
