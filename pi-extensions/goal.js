const GOAL_STATE_ENTRY = "goal-state";
const GOAL_RESULT_TYPE = "goal-result";
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

const EVALUATOR_PROMPT = "goal: <goal description> - is this completed? Answer with exactly BLOCKED, COMPLETED, or NOT_DONE";
const PI_SUBAGENTS_SERVICE = Symbol.for("@gotgenes/pi-subagents:service");

function normalizeGoal(value) {
  if (typeof value !== "string") return "";
  return value.trim().slice(0, MAX_GOAL_CHARS);
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
  if (!message || (message.role !== "user" && message.role !== "assistant")) return "";
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

function responseOutcome(response) {
  const text = Array.isArray(response?.content)
    ? response.content.filter((part) => part?.type === "text").map((part) => part.text).join("")
    : "";
  const token = text.trim().match(/^(BLOCKED|COMPLETED|NOT_DONE)$/)?.[1];
  return OUTCOMES.has(token) ? token : "NOT_DONE";
}

export { boundedActiveContext, responseOutcome };

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
  let subagentsService;

  const cancel = () => {
    generation += 1;
    skipNextSettle = false;
    if (deferredEvaluation !== undefined) clearTimeout(deferredEvaluation);
    deferredEvaluation = undefined;
    activeAbort?.abort();
    activeAbort = undefined;
  };

  const persist = () => pi.appendEntry(GOAL_STATE_ENTRY, { ...state });
  const notify = (ctx, message, level = "info") => ctx.ui?.notify?.(message, level);
  const configureSubagents = () => {
    // The package is optional. Its published Symbol is the runtime seam, so a
    // plain Node test (or Pi without the package) does not need module resolution.
    subagentsService = globalThis[PI_SUBAGENTS_SERVICE];
  };
  const hasActiveSubagent = () => {
    if (typeof subagentsService?.hasRunning !== "function") return false;
    try {
      return subagentsService.hasRunning() === true;
    } catch {
      return false;
    }
  };
  const applyAction = (action, rawGoal) => {
    if (!GOAL_ACTIONS.has(action)) {
      return { ok: false, message: "Invalid action. Use set, status, or off." };
    }
    if (action === "set") {
      const goal = normalizeGoal(rawGoal);
      if (!goal) return { ok: false, message: "A goal description is required for set." };
      cancel();
      state = { enabled: true, goal };
      persist();
      return { ok: true, message: `Goal set: ${goal}`, details: { action, ...state } };
    }
    if (action === "status") {
      return {
        ok: true,
        message: state.enabled ? `Goal enabled: ${state.goal}` : "Goal off",
        details: { action, ...state },
      };
    }
    cancel();
    state = { ...state, enabled: false };
    persist();
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
      } catch {
        skipNextSettle = false;
      }
    }, 0);
  };

  pi.on("session_start", async (event, ctx) => {
    if (sessionId === undefined) sessionId = event?.sessionId ?? ctx.sessionManager?.getSessionId?.();
    if (!isCurrentSession(event, ctx)) return;
    cancel();
    state = restoreState(ctx);
    configureSubagents();
  });

  pi.on("session_shutdown", async (event, ctx) => {
    if (!isCurrentSession(event, ctx)) return;
    cancel();
    subagentsService = undefined;
  });

  pi.registerTool({
    name: "goal",
    label: "Goal",
    description: "Set, inspect, or disable the active goal evaluator.",
    promptSnippet: "Use goal to set or update a standing goal, inspect its status, or turn it off.",
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
    if (hasActiveSubagent() || deferredEvaluation !== undefined || activeAbort) return;
    const runGeneration = generation;
    const goal = state.goal;
    // agent_settled can run inside Pi's turn bookkeeping. Defer the evaluator
    // to a macrotask so it observes the fully settled branch and never overlaps
    // another evaluation scheduled by the same turn.
    deferredEvaluation = setTimeout(async () => {
      deferredEvaluation = undefined;
      if (runGeneration !== generation || !isCurrentSession(event, ctx) || !state.enabled || state.goal !== goal || !ctx.model || hasActiveSubagent()) return;
      const controller = new AbortController();
      activeAbort = controller;
      try {
        const response = await ctx.modelRegistry.complete(
          ctx.model,
          {
            systemPrompt: EVALUATOR_PROMPT.replace("<goal description>", goal),
            messages: [{
              role: "user",
              content: [{ type: "text", text: `Goal: ${goal}\n\nActive conversation:\n${boundedActiveContext(ctx).map((message) => `${message.role}: ${message.content[0].text}`).join("\n\n")}\n\nReturn exactly one token: BLOCKED, COMPLETED, or NOT_DONE.` }],
              timestamp: Date.now(),
            }],
          },
          { signal: controller.signal },
        );
        if (runGeneration !== generation || controller.signal.aborted) return;
        const outcome = responseOutcome(response);
        scheduleInjection(event, ctx, runGeneration, controller, {
          customType: GOAL_RESULT_TYPE,
          content: outcome === "NOT_DONE"
            ? `Goal check: NOT_DONE. Resume working toward this goal: ${goal}.`
            : outcome === "BLOCKED"
              ? "Goal check: BLOCKED. Contact the supervisor with the blocker and what is needed."
              : "Goal check: COMPLETED. Contact the supervisor and report completion.",
          display: true,
          details: { goal, outcome },
        });
      } catch {
        if (runGeneration !== generation || controller.signal.aborted) return;
        scheduleInjection(event, ctx, runGeneration, controller, {
          customType: GOAL_RESULT_TYPE,
          content: `Goal check: NOT_DONE. Resume working toward this goal: ${goal}.`,
          display: true,
          details: { goal, outcome: "NOT_DONE" },
        });
      } finally {
        if (activeAbort === controller) activeAbort = undefined;
      }
    }, 0);
  });
}

export default createGoalExtension;
