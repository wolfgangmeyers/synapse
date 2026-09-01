import assert from "node:assert/strict";
import test from "node:test";

import goalExtension, { GOAL_ACTIVITY_EVENT, GOAL_CONTROL_REQUEST_EVENT, GOAL_STATE_EVENT, GOAL_STATE_REQUEST_EVENT, GOAL_WORKER_ID_EVENT, responseEvaluation } from "../pi-extensions/mecha-goal/index.ts";

const GOAL_ENTRY = "goal-state";
const PI_SUBAGENTS_SERVICE_REGISTRY = Symbol.for("@gotgenes/pi-subagents:service-registry");
const MAX_CONTEXT_BYTES = 24_000;
const MAX_NOT_DONE_WAKEUPS = 1000;

function fixture({ response = { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] }, complete, model = { provider: "test", id: "goal" }, provider, onSendMessage, sendMessage, sessionId, extensionOptions } = {}) {
  const handlers = new Map();
  const eventHandlers = new Map();
  const emittedEvents = [];
  const commands = new Map();
  const tools = new Map();
  const entries = [];
  const sent = [];
  const notices = [];
  const branch = [];
  const pi = {
    getSessionId() { return sessionId; },
    on(name, handler) { handlers.set(name, handler); },
    events: {
      on(name, handler) { eventHandlers.set(name, handler); },
      emit(name, data) { emittedEvents.push({ name, data }); },
    },
    registerCommand(name, command) { commands.set(name, command); },
    registerTool(tool) { tools.set(tool.name, tool); },
    appendEntry(customType, data) { const entry = { type: "custom", customType, data }; entries.push(entry); branch.push(entry); },
    sendMessage(message, options) {
      sent.push({ message, options });
      onSendMessage?.();
      return sendMessage?.(message, options);
    },
  };
  const context = {
    model,
    modelRegistry: {
      complete: complete ?? (async () => response),
      getApiKeyAndHeaders: async () => ({ ok: true, apiKey: "cursor-api-key", headers: {}, env: {} }),
      getProvider: () => provider,
    },
    sessionManager: {
      getBranch: () => branch,
      buildContextEntries: () => branch,
      getSessionId: () => sessionId,
    },
    ui: { notify(message, type) { notices.push({ message, type }); } },
  };
  goalExtension(pi, extensionOptions);
  return { branch, commands, context, emittedEvents, entries, eventHandlers, handlers, notices, sent, tools };
}

async function setGoal(fixture, description) {
  await fixture.commands.get("goal").handler(`set ${description}`, fixture.context);
}

async function callGoalTool(fixture, params) {
  return fixture.tools.get("goal").execute("tool-call", params, undefined, undefined, fixture.context);
}

async function settle(fixture) {
  await fixture.handlers.get("agent_settled")({}, fixture.context);
}

async function flushDeferred() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function applyGoalControl(current, control) {
  const handler = current.eventHandlers.get(GOAL_CONTROL_REQUEST_EVENT);
  assert.equal(typeof handler, "function", "goal extension registers the typed control handler");
  let result;
  handler({ ...control, context: current.context, respond(value) { result = value; } });
  return result;
}

function fakeTimer() {
  let now = 0;
  let nextId = 1;
  const timers = new Map();
  const clock = { now: () => now };
  const setTimeout = (callback, delay = 0) => {
    const id = nextId++;
    timers.set(id, { callback, due: now + Math.max(0, delay) });
    return id;
  };
  const clearTimeout = (id) => timers.delete(id);
  const advance = (durationMs) => {
    now += durationMs;
    while (true) {
      const due = [...timers.entries()]
        .filter(([, timer]) => timer.due <= now)
        .sort((a, b) => a[1].due - b[1].due)[0];
      if (!due) return;
      timers.delete(due[0]);
      due[1].callback();
    }
  };
  return { clock, setTimeout, clearTimeout, advance };
}

function publishService(ownerSessionId, service) {
  const previous = globalThis[PI_SUBAGENTS_SERVICE_REGISTRY];
  const registry = previous ?? { services: new Map() };
  registry.services.set(ownerSessionId, service);
  globalThis[PI_SUBAGENTS_SERVICE_REGISTRY] = registry;
  return () => {
    registry.services.delete(ownerSessionId);
    if (registry.services.size === 0 && previous === undefined) delete globalThis[PI_SUBAGENTS_SERVICE_REGISTRY];
  };
}

function captureDiagnostics() {
  const symbol = Symbol.for("mecha.pi.diagnostic-journal.v1");
  const previous = Object.getOwnPropertyDescriptor(globalThis, symbol);
  const records = [];
  Object.defineProperty(globalThis, symbol, {
    configurable: true,
    value: { version: 1, record(source, event, fields) { records.push({ source, event, fields }); return true; } },
  });
  return {
    records,
    restore() {
      if (previous === undefined) delete globalThis[symbol];
      else Object.defineProperty(globalThis, symbol, previous);
    },
  };
}

test("evaluator parses only a strict verdict line and bounds the visible rationale", () => {
  assert.deepEqual(responseEvaluation({ content: [{ type: "text", text: "VERDICT: COMPLETED extra\nREASON: This must not change the outcome." }] }), {
    outcome: "NOT_DONE",
    rationale: 'Invalid evaluator output: {"content":[{"type":"text","text":"VERDICT: COMPLETED extra\\nREASON: This must not change the outcome."}]}',
  });
  assert.deepEqual(responseEvaluation({ content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: one\nREASON: two" }] }), {
    outcome: "NOT_DONE",
    rationale: 'Invalid evaluator output: {"content":[{"type":"text","text":"VERDICT: NOT_DONE\\nREASON: one\\nREASON: two"}]}',
  });
  assert.equal(responseEvaluation({ content: [{ type: "text", text: "REASON: Missing verdict" }] }).outcome, "NOT_DONE");
  assert.match(responseEvaluation({ content: [{ type: "text", text: `VERDICT: NOT_DONE\nREASON: ${"x".repeat(300)}` }] }).rationale, /^Invalid evaluator output: /);
});

test("goal tool registers its action schema and shares state with /goal", async () => {
  const current = fixture();
  const tool = current.tools.get("goal");
  assert.ok(tool);
  assert.ok(tool.promptSnippet);
  assert.ok(Array.isArray(tool.promptGuidelines) && tool.promptGuidelines.length > 0);
  for (const guideline of tool.promptGuidelines) assert.match(guideline, /goal/);
  assert.deepEqual(tool.parameters, {
    type: "object",
    additionalProperties: false,
    required: ["action"],
    properties: {
      action: { type: "string", enum: ["set", "status", "off"] },
      goal: { type: "string" },
    },
  });

  const setResult = await callGoalTool(current, { action: "set", goal: "  ship the tool  " });
  assert.equal(setResult.isError, undefined);
  assert.deepEqual(setResult.details, { action: "set", enabled: true, goal: "ship the tool" });
  await current.commands.get("goal").handler("status", current.context);
  assert.deepEqual(current.notices.at(-1), { message: "Goal enabled: ship the tool", type: "info" });

  const statusResult = await callGoalTool(current, { action: "status" });
  assert.deepEqual(statusResult.details, {
    action: "status",
    enabled: true,
    goal: "ship the tool",
    evaluatorCalls: 0,
    notDoneWakeups: 0,
    completedThings: 0,
    goalLockEnabled: false,
    snoozeEnabled: true,
    snoozeDurationMs: 60_000,
    snoozeTurnLimit: null,
    snoozeBudgetCapMs: null,
    snoozeRefillYieldMs: 30_000,
    snoozeRefillElapsedMs: 60_000,
    outcomeCounts: { COMPLETED: 0, NOT_DONE: 0 },
    lastOwnerGateState: "allowed",
  });
  const updateResult = await callGoalTool(current, { action: "set", goal: "ship the updated tool" });
  assert.deepEqual(updateResult.details, { action: "set", enabled: true, goal: "ship the updated tool" });
  const offResult = await callGoalTool(current, { action: "off" });
  assert.deepEqual(offResult.details, { action: "off", enabled: false, goal: "ship the updated tool" });
  await settle(current);
  assert.equal(current.sent.length, 0);

  const invalidAction = await callGoalTool(current, { action: "pause" });
  assert.equal(invalidAction.isError, true);
  assert.match(invalidAction.content[0].text, /Invalid action/);
  const missingGoal = await callGoalTool(current, { action: "set", goal: "  " });
  assert.equal(missingGoal.isError, true);
  assert.match(missingGoal.content[0].text, /required/);
});

test("typed goal control persists full configuration and current-session outcome counts", async () => {
  const runtime = fakeTimer();
  const current = fixture({
    extensionOptions: runtime,
    response: { content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: Work remains." }] },
  });
  const settings = {
    goalLockEnabled: true,
    snoozeEnabled: true,
    snoozeDurationMs: 120_000,
    snoozeTurnLimit: 4,
    snoozeBudgetCapMs: 300_000,
    snoozeRefillYieldMs: 30_000,
    snoozeRefillElapsedMs: 60_000,
  };

  const applied = applyGoalControl(current, {
    action: "set",
    controlId: "goal-control-1",
    targetInstanceId: "mailbox-instance-1",
    revision: 1,
    goal: "ship the WebUI editor",
    settings,
  });

  assert.deepEqual(applied, {
    accepted: true,
    revision: 1,
    goalState: {
      enabled: true,
      goal: "ship the WebUI editor",
      ...settings,
      outcomeCounts: {
        COMPLETED: 0,
        NOT_DONE: 0,
      },
    },
  });
  assert.deepEqual(current.entries.at(-1).data, {
    enabled: true,
    goal: "ship the WebUI editor",
    notDoneWakeups: 0,
    completedThings: 0,
    goalLockEnabled: true,
    snoozeEnabled: true,
    snoozeDurationMs: 120_000,
    snoozeTurnLimit: 4,
    snoozeBudgetCapMs: 300_000,
    snoozeBudgetAvailableMs: 300_000,
    snoozeRefillYieldMs: 30_000,
    snoozeRefillElapsedMs: 60_000,
    outcomeCounts: {
      COMPLETED: 0,
      NOT_DONE: 0,
    },
    appliedGoalControl: { instanceId: "mailbox-instance-1", revision: 1 },
  });

  await settle(current);
  for (let step = 0; step < 6; step += 1) {
    runtime.advance(0);
    await Promise.resolve();
  }
  assert.equal((await callGoalTool(current, { action: "status" })).details.outcomeCounts.NOT_DONE, 1);
  assert.equal(current.entries.at(-1).data.outcomeCounts.NOT_DONE, 1);
});

test("/goal commands persist branch-local state and support status/off", async () => {
  const current = fixture();
  await setGoal(current, "ship the focused extension");
  assert.deepEqual(current.entries.at(-1), {
    type: "custom",
    customType: GOAL_ENTRY,
    data: {
      enabled: true,
      goal: "ship the focused extension",
      notDoneWakeups: 0,
      completedThings: 0,
      goalLockEnabled: false,
      snoozeEnabled: true,
      snoozeDurationMs: 60_000,
      outcomeCounts: { COMPLETED: 0, NOT_DONE: 0 },
    },
  });
  await current.commands.get("goal").handler("status", current.context);
  assert.deepEqual(current.notices.at(-1), { message: "Goal enabled: ship the focused extension", type: "info" });

  const restored = fixture();
  restored.branch.push(...current.branch);
  await restored.handlers.get("session_start")({}, restored.context);
  await restored.commands.get("goal").handler("status", restored.context);
  assert.deepEqual(restored.notices.at(-1), { message: "Goal enabled: ship the focused extension", type: "info" });

  await restored.commands.get("goal").handler("off", restored.context);
  assert.deepEqual(restored.entries.at(-1).data, {
    enabled: false,
    goal: "ship the focused extension",
    notDoneWakeups: 0,
    completedThings: 0,
    goalLockEnabled: false,
    snoozeEnabled: true,
    snoozeDurationMs: 60_000,
    outcomeCounts: { COMPLETED: 0, NOT_DONE: 0 },
  });
  await settle(restored);
  assert.equal(restored.sent.length, 0);
});

test("goal lock registers, persists and recovers only its setting, and slash override always works", async () => {
  const current = fixture();
  assert.ok(current.commands.has("goal-lock"));
  await current.commands.get("goal-lock").handler("on", current.context);
  assert.deepEqual(current.notices.at(-1), { message: "Goal lock on", type: "info" });
  assert.equal(current.entries.at(-1).data.goalLockEnabled, true);
  assert.ok(!Object.hasOwn(current.entries.at(-1).data, "waitingAuthWakeups"));

  const blockedSet = await callGoalTool(current, { action: "set", goal: "blocked goal" });
  assert.equal(blockedSet.isError, true);
  assert.match(blockedSet.content[0].text, /Goal lock is on/);
  const status = await callGoalTool(current, { action: "status" });
  assert.equal(status.details.goalLockEnabled, true);

  const recovered = fixture();
  recovered.branch.push(...current.branch);
  await recovered.handlers.get("session_start")({}, recovered.context);
  assert.equal((await callGoalTool(recovered, { action: "status" })).details.goalLockEnabled, true);
  await recovered.commands.get("goal-lock").handler("off", recovered.context);
  assert.deepEqual(recovered.notices.at(-1), { message: "Goal lock off", type: "info" });
  const allowedSet = await callGoalTool(recovered, { action: "set", goal: "unlocked goal" });
  assert.equal(allowedSet.isError, undefined);
});

test("goal lock gates programmatic set and off, then permits only a fresh completion off and clears the lock", async () => {
  const current = fixture();
  await setGoal(current, "protected goal");
  await current.commands.get("goal-lock").handler("on", current.context);
  assert.equal((await callGoalTool(current, { action: "set", goal: "replacement" })).isError, true);
  assert.equal((await callGoalTool(current, { action: "off" })).isError, true);

  await settle(current);
  await flushDeferred();
  await flushDeferred();
  assert.equal((await callGoalTool(current, { action: "set", goal: "still protected" })).isError, true, "completion does not permit set");
  const off = await callGoalTool(current, { action: "off" });
  assert.equal(off.isError, undefined);
  assert.equal(current.entries.at(-1).data.goalLockEnabled, false, "completion off clears the persisted lock");
  const setAfterOff = await callGoalTool(current, { action: "set", goal: "new goal" });
  assert.equal(setAfterOff.isError, undefined);
});

test("goal lock completion allowance is fresh and a new goal clears it", async () => {
  const current = fixture({
    complete: async () => ({ content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: Complete for now." }] }),
  });
  await setGoal(current, "protected goal");
  await current.commands.get("goal-lock").handler("on", current.context);
  await settle(current);
  await flushDeferred();
  await flushDeferred();
  await current.commands.get("goal").handler("set replacement goal", current.context);
  assert.equal((await callGoalTool(current, { action: "off" })).isError, true, "a new goal clears the completion allowance");

  const nonCompleted = fixture({ response: { content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: Still working." }] } });
  await setGoal(nonCompleted, "protected goal");
  await nonCompleted.commands.get("goal-lock").handler("on", nonCompleted.context);
  await settle(nonCompleted);
  await flushDeferred();
  await flushDeferred();
  assert.equal((await callGoalTool(nonCompleted, { action: "off" })).isError, true, "non-completed results never permit off");
});

test("pending mecha_context compaction pauses repeated goal checks until its tool result", async () => {
  const calls = [];
  const current = fixture({
    complete: async () => {
      calls.push(true);
      return { content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: Work remains." }] };
    },
  });
  await setGoal(current, "make progress");
  current.branch.push({
    type: "message",
    message: {
      role: "assistant",
      content: [{ type: "toolCall", id: "compact-call", name: "mecha_context", arguments: { action: "compact" } }],
    },
  });

  for (let attempt = 0; attempt < 3; attempt += 1) {
    await settle(current);
    await flushDeferred();
  }
  assert.equal(calls.length, 0);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 0);

  current.branch.push({
    type: "message",
    message: {
      role: "toolResult",
      toolCallId: "compact-call",
      toolName: "mecha_context",
      content: [{ type: "text", text: "Self-compaction deferred." }],
      isError: false,
    },
  });
  await settle(current);
  await flushDeferred();
  await flushDeferred();
  assert.equal(calls.length, 1);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 1);
});

test("session compaction pauses a scheduled evaluator and reactivates it after completion", async () => {
  const calls = [];
  const current = fixture({
    complete: async () => {
      calls.push(true);
      return { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] };
    },
  });
  await setGoal(current, "make progress");
  await settle(current);
  current.handlers.get("session_before_compact")({}, current.context);
  await flushDeferred();
  assert.equal(calls.length, 0, "the evaluator stays paused during compaction");
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 0);

  current.handlers.get("session_compact")({}, current.context);
  await flushDeferred();
  await flushDeferred();
  assert.equal(calls.length, 1, "the skipped evaluator is rescheduled after compaction");
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 1);
});

test("session compaction gates and resumes the goal self-wakeup", async () => {
  const current = fixture({ sessionId: "session" });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("off", current.context);
  current.handlers.get("session_before_compact")({}, current.context);
  await flushDeferred();
  assert.equal(current.sent.length, 0, "self-wakeup cannot bypass compaction");

  current.handlers.get("session_compact")({}, current.context);
  await flushDeferred();
  assert.deepEqual(current.sent.map(({ message }) => message), [{
    customType: "goal-update",
    content: "Goal set: make progress. Begin working toward this goal.",
    display: true,
    details: { enabled: true, goal: "make progress" },
  }]);
});

test("a self-wakeup deferred by unresolved compaction resumes once on the next settled path", async () => {
  const current = fixture({ sessionId: "session", model: null });
  await setGoal(current, "make progress");
  current.branch.push({
    type: "message",
    message: {
      role: "assistant",
      content: [{ type: "toolCall", id: "compact-call", name: "mecha_context", arguments: { action: "compact" } }],
    },
  });
  await flushDeferred();
  assert.equal(current.sent.length, 0);

  current.branch.push({
    type: "message",
    message: {
      role: "toolResult",
      toolCallId: "compact-call",
      toolName: "mecha_context",
      content: [{ type: "text", text: "Self-compaction deferred." }],
    },
  });
  await settle(current);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-update").length, 1);
  await flushDeferred();
  await settle(current);
  await flushDeferred();
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-update").length, 1);
});

test("stale goal self-wakeups are canceled when the goal changes or turns off", async () => {
  const current = fixture({ sessionId: "session" });
  await setGoal(current, "old goal");
  await current.commands.get("goal").handler("set new goal", current.context);
  await current.commands.get("goal").handler("off", current.context);
  await flushDeferred();
  assert.equal(current.sent.length, 0);
});

test("settled goal evaluation forwards bounded tool-result text without non-text content", async () => {
  const calls = [];
  const current = fixture({ complete: async (_model, request) => {
    calls.push(request);
    return { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] };
  } });
  await setGoal(current, "use the search result");
  current.branch.push({
    type: "message",
    message: {
      role: "toolResult",
      toolName: "codex_search",
      toolCallId: "search-call",
      content: [
        { type: "text", text: "Search found the requested recipe." },
        { type: "image", data: "must stay redacted" },
      ],
    },
  });

  await settle(current);
  await flushDeferred();
  assert.equal(calls.length, 1);
  const context = calls[0].messages[0].content[0].text;
  assert.match(context, /toolResult: Search found the requested recipe\./);
  assert.doesNotMatch(context, /must stay redacted|codex_search|search-call/);
});

test("settled goal evaluation uses the registered provider stream instead of compat", async () => {
  const calls = [];
  const current = fixture({
    model: { provider: "cursor", api: "cursor-sdk", id: "gpt-5.6-luna" },
    complete: async () => {
      throw new Error("compat path must not be used");
    },
    provider: {
      streamSimple(model, request, options) {
        calls.push({ model, request, options });
        return {
          result: async () => ({ content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] }),
        };
      },
    },
  });
  await setGoal(current, "use the Cursor provider");
  await current.commands.get("goal-snooze").handler("off", current.context);

  await settle(current);
  await flushDeferred();
  await flushDeferred();

  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].model, current.context.model);
  assert.equal(calls[0].options.apiKey, "cursor-api-key");
  assert.ok(calls[0].options.signal instanceof AbortSignal);
  assert.match(current.sent[0].message.content, /VERDICT: COMPLETED/);
});

test("settled goal evaluation uses bounded active context and exact steer wakeup options", async () => {
  const calls = [];
  const current = fixture({ complete: async (model, request, options) => {
    calls.push({ model, request, options });
    return { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] };
  } });
  await setGoal(current, "finish the goal extension");
  await current.commands.get("goal-snooze").handler("off", current.context);
  current.branch.push({ type: "message", message: { role: "user", content: "x".repeat(MAX_CONTEXT_BYTES * 2) } });

  await settle(current);
  assert.equal(calls.length, 0, "evaluation starts only after a macrotask defer");
  await flushDeferred();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].model, current.context.model);
  assert.ok(calls[0].options?.signal instanceof AbortSignal);
  const prompt = calls[0].request.systemPrompt;
  assert.equal(prompt, "goal: finish the goal extension - return exactly two lines: VERDICT: COMPLETED|NOT_DONE and REASON: <concise rationale>");
  assert.match(calls[0].request.messages[0].content[0].text, /VERDICT: COMPLETED\|NOT_DONE/);
  assert.ok(Buffer.byteLength(prompt) <= 2_048 + MAX_CONTEXT_BYTES + 1_000);
  await flushDeferred();
  assert.deepEqual(current.sent, [{
    message: { customType: "goal-result", content: "Goal check: COMPLETED. Contact the supervisor and report completion. Disable future goal notifications with /goal off.\nVERDICT: COMPLETED\nREASON: The goal is complete.", display: true, details: { goal: "finish the goal extension", outcome: "COMPLETED", rationale: "The goal is complete." } },
    options: { deliverAs: "steer", triggerTurn: true },
  }]);

  await settle(current);
  assert.equal(calls.length, 1, "the steer wakeup consumes exactly one settled event");
  assert.equal(current.sent.length, 1);
});

test("NOT_DONE defers its continuation, skips exactly one injected settle, and evaluates the next normal settle", async () => {
  const outcomes = ["NOT_DONE", "COMPLETED"];
  const calls = [];
  let injectedSettle;
  const current = fixture({
    complete: async () => {
      calls.push(true);
      return { content: [{ type: "text", text: `VERDICT: ${outcomes.shift()}\nREASON: Evaluated current progress.` }] };
    },
    onSendMessage: () => {
      queueMicrotask(() => { injectedSettle = settle(current); });
    },
  });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("off", current.context);

  await settle(current);
  assert.equal(current.sent.length, 0, "the continuation waits until the settled callback returns");
  await flushDeferred();
  await flushDeferred();
  await injectedSettle;
  assert.equal(current.sent[0].message.content, "NOT_DONE: Review make progress and take the next concrete action now.\nVERDICT: NOT_DONE\nREASON: Evaluated current progress.");
  assert.equal(calls.length, 1, "the injected settle is skipped");

  await settle(current);
  await flushDeferred();
  await flushDeferred();
  assert.equal(calls.length, 2, "the next normal settle evaluates again");
  assert.equal(current.sent.length, 2);
});

test("invalid, missing, or failed model output injects NOT_DONE", async () => {
  for (const response of [
    { content: [{ type: "text", text: "COMPLETED extra" }] },
    { content: [{ type: "text", text: "" }] },
    undefined,
  ]) {
    const current = fixture({ complete: async () => response });
    await setGoal(current, "make progress");
    await current.commands.get("goal-snooze").handler("off", current.context);
    await settle(current);
    await flushDeferred();
    await flushDeferred();
    assert.match(current.sent[0].message.content, /^NOT_DONE: Review make progress and take the next concrete action now\.\nVERDICT: NOT_DONE\nREASON: Invalid evaluator output: /);
  }

  let reject = true;
  const current = fixture({ complete: async () => { if (reject) throw new Error("provider unavailable"); return { content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: A supervisor decision is required." }] }; } });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("off", current.context);
  await settle(current);
  await flushDeferred();
  await flushDeferred();
  assert.match(current.sent[0].message.content, /^NOT_DONE: Review make progress and take the next concrete action now\.\nVERDICT: NOT_DONE\nREASON: Evaluator unavailable: Error: provider unavailable/);
});

test("sendMessage failure leaves the next normal settle available", async () => {
  let fail = true;
  const calls = [];
  const current = fixture({
    complete: async () => {
      calls.push(true);
      return { content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: More work remains." }] };
    },
    sendMessage: () => {
      if (fail) {
        fail = false;
        throw new Error("send failed");
      }
    },
  });
  await setGoal(current, "make progress");
  await settle(current);
  await flushDeferred();
  await flushDeferred();
  assert.equal(calls.length, 1);
  await settle(current);
  await flushDeferred();
  assert.equal(calls.length, 2);
});

test("stale session settles cannot start an evaluation", async () => {
  const calls = [];
  const current = fixture({
    sessionId: "owner-session",
    complete: async () => {
      calls.push(true);
      return { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] };
    },
  });
  await setGoal(current, "stay current");
  await current.handlers.get("agent_settled")({ sessionId: "other-session" }, current.context);
  await flushDeferred();
  assert.equal(calls.length, 0);
  await current.handlers.get("agent_settled")({ sessionId: "owner-session" }, current.context);
  await flushDeferred();
  assert.equal(calls.length, 1);
});

test("activity gate rejections record safe reasons and identities without goal text", async () => {
  const diagnostics = captureDiagnostics();
  try {
    const current = fixture({ sessionId: "owner-session" });
    await setGoal(current, "private goal text");
    const activity = current.eventHandlers.get(GOAL_ACTIVITY_EVENT);
    activity({ kind: "unknown", activityId: "bad-kind", sessionId: "owner-session", revision: 1, goal: { enabled: true, goal: "private goal text" } });
    activity({ kind: "tool-completed", activityId: "stale", sessionId: "other-session", revision: 1, goal: { enabled: true, goal: "private goal text" } });
    const valid = { kind: "tool-completed", activityId: "same-id", sessionId: "owner-session", revision: 1, goal: { enabled: true, goal: "private goal text" } };
    activity(valid);
    activity(valid);
    const rejected = diagnostics.records.filter(({ event }) => event === "activity_gate_rejected");
    assert.deepEqual(rejected.map(({ fields }) => fields.reason), ["invalid_kind", "stale_session", "rearm_inactive", "duplicate_activity"]);
    assert.ok(rejected.every(({ fields }) => fields.session_id === undefined || typeof fields.session_id === "string"));
    assert.doesNotMatch(JSON.stringify(rejected), /private goal text/);
  } finally {
    diagnostics.restore();
  }
});

test("goal diagnostics retain bounded cross-repo subagent kinds", async () => {
  const diagnostics = captureDiagnostics();
  try {
    const current = fixture({ sessionId: "owner-session" });
    await setGoal(current, "private goal text");
    const activity = current.eventHandlers.get(GOAL_ACTIVITY_EVENT);
    activity({
      kind: "Plan",
      activityId: "plan-activity",
      sessionId: "owner-session",
      workerId: "worker",
      producerEpoch: "producer-epoch",
      revision: 1,
      goal: { enabled: true, goal: "private goal text" },
    });
    const acceptedKind = diagnostics.records.find(({ event }) => event === "activity_gate_rejected");
    assert.equal(acceptedKind?.fields.kind, "Plan");

    activity({ kind: `${"x".repeat(128)}!`, activityId: "long-kind", sessionId: "owner-session" });
    const rejectedKind = diagnostics.records.filter(({ event }) => event === "activity_gate_rejected").at(-1);
    assert.equal(Object.hasOwn(rejectedKind?.fields ?? {}, "kind"), false);
  } finally {
    diagnostics.restore();
  }
});

test("malformed activity identities are rejected before goal diagnostics are recorded", async () => {
  const diagnostics = captureDiagnostics();
  try {
    const current = fixture({ sessionId: "owner-session" });
    await setGoal(current, "private goal text");
    const activity = current.eventHandlers.get(GOAL_ACTIVITY_EVENT);
    activity({
      kind: "tool-completed private",
      activityId: "private activity id",
      sessionId: "private session id",
      workerId: "private worker id",
      producerEpoch: "private producer epoch",
      revision: 1,
      goal: { enabled: true, goal: "private goal text" },
    });
    const rejection = diagnostics.records.find(({ event }) => event === "activity_gate_rejected");
    assert.equal(rejection?.fields.reason, "invalid_kind");
    assert.equal(Object.hasOwn(rejection?.fields ?? {}, "event_id"), false);
    assert.equal(Object.hasOwn(rejection?.fields ?? {}, "worker_id"), false);
    assert.equal(Object.hasOwn(rejection?.fields ?? {}, "producer_epoch"), false);
    assert.doesNotMatch(JSON.stringify(rejection), /private/);
  } finally {
    diagnostics.restore();
  }
});

test("owner and wakeup failures record safe state without changing gates", async () => {
  const diagnostics = captureDiagnostics();
  const cleanup = publishService("owner-session", {
    hasRunning() { throw new Error("private owner error"); },
  });
  try {
    const current = fixture({ sessionId: "owner-session", sendMessage: (message) => {
      if (message.customType === "goal-update") throw new Error("private wakeup error");
    } });
    await setGoal(current, "private goal text");
    await flushDeferred();
    await settle(current);
    await flushDeferred();
    assert.ok(current.sent.length >= 1, "the failed wakeup still follows the existing send attempt");
    assert.ok(diagnostics.records.some(({ event, fields }) => event === "publication_failed" && fields.reason === "self_wakeup_send" && fields.error_name === "Error"));
    assert.ok(diagnostics.records.some(({ event, fields }) => event === "owner_service_failed" && fields.reason === "running_check" && fields.state === "enabled"));
    assert.doesNotMatch(JSON.stringify(diagnostics.records), /private goal text|private owner error|private wakeup error/);
  } finally {
    cleanup();
    diagnostics.restore();
  }
});

test("missing optional subagent service does not break goal evaluation", async () => {
  const current = fixture({ sessionId: "owner-session" });
  await current.handlers.get("session_start")({ sessionId: "owner-session" }, current.context);
  const result = await callGoalTool(current, { action: "set", goal: "continue working" });
  assert.equal(result.isError, undefined);
  await settle(current);
  await flushDeferred();
  await flushDeferred();
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 1);
});

test("owner-published queued or running subagents defer evaluation until the parent is idle", async () => {
  const ownerSessionId = "owner-session";
  const foreignSessionId = "foreign-session";
  const records = new Map();
  const service = {
    hasRunning: () => [...records.values()].some(({ status }) => status === "queued" || status === "running"),
  };
  const cleanup = publishService(ownerSessionId, service);
  const foreignCleanup = publishService(foreignSessionId, {
    hasRunning: () => true,
  });
  try {
    const calls = [];
    const current = fixture({
      sessionId: ownerSessionId,
      complete: async () => {
        calls.push(true);
        return { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] };
      },
    });
    await current.handlers.get("session_start")({ sessionId: ownerSessionId }, current.context);
    await setGoal(current, "wait for the child");

    records.set("queued-child", { status: "queued" });
    await settle(current);
    await flushDeferred();
    assert.equal(calls.length, 0, "the owner's queued child suppresses evaluation");

    records.get("queued-child").status = "running";
    await settle(current);
    await flushDeferred();
    assert.equal(calls.length, 0, "the owner's running child suppresses evaluation");

    records.get("queued-child").status = "completed";
    await settle(current);
    await flushDeferred();
    await flushDeferred();
    assert.equal(calls.length, 1, "a later settle evaluates after the owner's child is complete");
  } finally {
    cleanup();
    foreignCleanup();
  }
});

test("the deferred gate rechecks the owner immediately before complete", async () => {
  let checks = 0;
  const calls = [];
  const cleanup = publishService("owner-session", {
    hasRunning: () => {
      checks += 1;
      return checks === 2;
    },
  });
  try {
    const current = fixture({
      sessionId: "owner-session",
      complete: async () => {
        calls.push(true);
        return { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: Must not run." }] };
      },
    });
    await current.handlers.get("session_start")({ sessionId: "owner-session" }, current.context);
    await setGoal(current, "wait for the owner child");
    await settle(current);
    await flushDeferred();
    assert.equal(checks, 2);
    assert.equal(calls.length, 0);
    assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 0);
  } finally {
    cleanup();
  }
});

test("the wakeup macrotask rechecks owner activity after evaluation", async () => {
  let active = false;
  let firstEvaluation = true;
  const cleanup = publishService("owner-session", { hasRunning: () => active });
  try {
    const current = fixture({
      sessionId: "owner-session",
      complete: async () => {
        if (firstEvaluation) {
          active = true;
          firstEvaluation = false;
        }
        return { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: Completed." }] };
      },
    });
    await current.handlers.get("session_start")({ sessionId: "owner-session" }, current.context);
    await setGoal(current, "wait for the owner child");
    await settle(current);
    await flushDeferred();
    await flushDeferred();
    assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 0, "active owner work suppresses the wakeup");
    const status = await callGoalTool(current, { action: "status" });
    assert.deepEqual(status.details, {
      action: "status",
      enabled: true,
      goal: "wait for the owner child",
      evaluatorCalls: 1,
      notDoneWakeups: 0,
      completedThings: 1,
      goalLockEnabled: false,
      snoozeEnabled: true,
      snoozeDurationMs: 60_000,
      snoozeTurnLimit: null,
      snoozeBudgetCapMs: null,
      snoozeRefillYieldMs: 30_000,
      snoozeRefillElapsedMs: 60_000,
      outcomeCounts: { COMPLETED: 1, NOT_DONE: 0 },
      lastOwnerGateState: "suppressed",
    });
    active = false;
    await settle(current);
    await flushDeferred();
    await flushDeferred();
    assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 1, "a later eligible settle resumes evaluation");
  } finally {
    cleanup();
  }
});

test("a foreign owner's active subagent does not suppress this owner's evaluator", async () => {
  const cleanup = publishService("foreign-session", { hasRunning: () => true });
  try {
    const current = fixture({ sessionId: "owner-session" });
    await current.handlers.get("session_start")({ sessionId: "owner-session" }, current.context);
    await setGoal(current, "continue working");
    await settle(current);
    await flushDeferred();
    await flushDeferred();
    assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 1);
  } finally {
    cleanup();
  }
});

test("goal off cancels an in-flight evaluation and prevents stale injection", async () => {
  let resolve;
  const current = fixture({ complete: () => new Promise((done) => { resolve = done; }) });
  await setGoal(current, "cancel safely");
  await current.commands.get("goal-snooze").handler("off", current.context);
  const pending = settle(current);
  await flushDeferred();
  await current.commands.get("goal").handler("off", current.context);
  resolve({ content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] });
  await pending;
  assert.equal(current.sent.length, 0);
});

test("goal snooze is enabled by default, parses duration, and injects availability once", async () => {
  const current = fixture({
    complete: async () => ({ content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: Work remains." }] }),
  });
  const snoozeTool = current.tools.get("goal_snooze");
  assert.ok(snoozeTool);
  const initialStatus = await callGoalTool(current, { action: "status" });
  assert.equal(initialStatus.details.snoozeEnabled, true);
  assert.equal(initialStatus.details.snoozeDurationMs, 60_000);

  await setGoal(current, "make progress");
  await flushDeferred();

  await current.commands.get("goal-snooze").handler("", current.context);
  assert.deepEqual(current.entries.at(-1).data, {
    enabled: true,
    goal: "make progress",
    notDoneWakeups: 0,
    completedThings: 0,
    goalLockEnabled: false,
    snoozeEnabled: true,
    snoozeDurationMs: 60_000,
    outcomeCounts: { COMPLETED: 0, NOT_DONE: 0 },
  });
  await flushDeferred();
  const availability = current.sent.filter(({ message }) => message.customType === "goal-snooze-availability");
  assert.equal(availability.length, 1);
  assert.match(availability[0].message.content, /goal_snooze/);

  await current.commands.get("goal-snooze").handler("123s", current.context);
  assert.equal(current.entries.at(-1).data.snoozeDurationMs, 123_000);
  await current.commands.get("goal-snooze").handler("123m", current.context);
  assert.equal(current.entries.at(-1).data.snoozeDurationMs, 7_380_000);
  await current.commands.get("goal-snooze").handler("not-a-duration", current.context);
  assert.equal(current.entries.at(-1).data.snoozeDurationMs, 60_000);
  await flushDeferred();
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-snooze-availability").length, 1);

  await settle(current);
  await flushDeferred();
  await flushDeferred();
  const wakeup = current.sent.find(({ message }) => message.customType === "goal-result");
  assert.ok(wakeup);
  assert.doesNotMatch(wakeup.message.content, /snooze/i);

  await current.commands.get("goal-snooze").handler("off", current.context);
  assert.equal(current.entries.at(-1).data.snoozeEnabled, false);
  const disabledAgain = await snoozeTool.execute("snooze", {}, undefined, undefined, current.context);
  assert.equal(disabledAgain.isError, true);

  await current.commands.get("goal-snooze").handler("", current.context);
  await flushDeferred();
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-snooze-availability").length, 2);
});

test("goal snooze settles immediately and wakes once after its background timer", async () => {
  const runtime = fakeTimer();
  let calls = 0;
  const current = fixture({
    extensionOptions: runtime,
    complete: async () => {
      calls += 1;
      return { content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: Work remains." }] };
    },
  });
  const resultWakeups = () => current.sent.filter(({ message }) => message.customType === "goal-result");
  await setGoal(current, "make progress");
  await settle(current);
  runtime.advance(0);
  await Promise.resolve();
  runtime.advance(0);
  await Promise.resolve();
  runtime.advance(0);
  await Promise.resolve();
  runtime.advance(0);
  assert.equal(resultWakeups().length, 1);
  assert.equal(resultWakeups()[0].message.content, "NOT_DONE: Review make progress and take the next concrete action now.\nVERDICT: NOT_DONE\nREASON: Work remains.");

  await current.commands.get("goal-snooze").handler("1s", current.context);
  const sleeping = current.tools.get("goal_snooze").execute("snooze", {}, undefined, undefined, current.context);
  const result = await sleeping;
  assert.equal(result.isError, undefined);
  assert.match(result.content[0].text, /scheduled/);
  await settle(current);
  runtime.advance(0);
  await Promise.resolve();
  assert.equal(calls, 1, "a settled event during snooze stays idle");
  runtime.advance(999);
  assert.equal(resultWakeups().length, 1, "snooze does not wake the goal early");
  runtime.advance(1);

  assert.equal(resultWakeups().length, 2);
  assert.equal(resultWakeups()[1].message.content, "NOT_DONE: Continue make progress by completing its next actionable step now.\nVERDICT: NOT_DONE\nREASON: Work remains.");
  assert.doesNotMatch(resultWakeups()[1].message.content, /snooze/i);
  assert.equal(current.entries.at(-1).data.notDoneWakeups, 2);
});

test("goal snooze suppresses a stale session wakeup", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime, sessionId: "owner" });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  const scheduled = await current.tools.get("goal_snooze").execute("snooze", {}, undefined, undefined, current.context);
  assert.equal(scheduled.isError, undefined);
  current.context.sessionManager.getSessionId = () => "other";
  runtime.advance(1_000);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 0);
});

test("goal snooze resumes a wakeup deferred by compaction abort", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime, sessionId: "session" });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  const scheduled = await current.tools.get("goal_snooze").execute("snooze", {}, undefined, undefined, current.context);
  assert.equal(scheduled.isError, undefined);

  const compaction = new AbortController();
  current.handlers.get("session_before_compact")({ signal: compaction.signal }, current.context);
  runtime.advance(1_000);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 0);
  compaction.abort();
  runtime.advance(0);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 1);
});

test("goal snooze rejects stale sessions and inactive goals", async () => {
  const stale = fixture({ sessionId: "owner" });
  const staleContext = {
    ...stale.context,
    sessionManager: { ...stale.context.sessionManager, getSessionId: () => "other" },
  };
  const staleResult = await stale.tools.get("goal_snooze").execute("snooze", {}, undefined, undefined, staleContext);
  assert.equal(staleResult.isError, true);
  assert.match(staleResult.content[0].text, /unavailable/);

  const inactive = fixture();
  await inactive.commands.get("goal-snooze").handler("", inactive.context);
  const inactiveResult = await inactive.tools.get("goal_snooze").execute("snooze", {}, undefined, undefined, inactive.context);
  assert.equal(inactiveResult.isError, true);
  assert.match(inactiveResult.content[0].text, /active goal/);
});

test("goal snooze rejects overlap and goal off cancels the background wait", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  const waiting = current.tools.get("goal_snooze").execute("snooze", {}, undefined, undefined, current.context);
  assert.equal((await waiting).isError, undefined);
  const duplicate = await current.tools.get("goal_snooze").execute("snooze", {}, undefined, undefined, current.context);
  assert.equal(duplicate.isError, true);
  assert.match(duplicate.content[0].text, /already waiting/);

  await current.commands.get("goal-snooze").handler("off", current.context);
  runtime.advance(1_000);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 0);
});

test("goal snooze rejects a second call while its timer is pending", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  const snooze = current.tools.get("goal_snooze");
  const first = snooze.execute("snooze", {}, undefined, undefined, current.context);
  assert.equal((await first).isError, undefined);

  const duplicate = await snooze.execute("snooze", {}, undefined, undefined, current.context);
  assert.equal(duplicate.isError, true);
  assert.match(duplicate.content[0].text, /already waiting/);
  runtime.advance(1_000);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 1);
});

test("goal snooze honors the tool AbortSignal without consuming its budget", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  await current.commands.get("goal-snooze-budget").handler("1s", current.context);
  const controller = new AbortController();
  const snooze = current.tools.get("goal_snooze");
  const waiting = snooze.execute("snooze", {}, controller.signal, undefined, current.context);
  assert.equal((await waiting).isError, undefined);
  controller.abort();
  runtime.advance(1_000);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 0);

  const second = snooze.execute("snooze", {}, undefined, undefined, current.context);
  assert.equal((await second).isError, undefined, "an aborted wait does not consume the budget");
  runtime.advance(1_000);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 1);
});

test("goal snooze honors the persisted retry cap", async () => {
  const current = fixture();
  current.branch.push({
    type: "custom",
    customType: GOAL_ENTRY,
    data: {
      enabled: true,
      goal: "make progress",
      notDoneWakeups: 20,
      completedThings: 0,
      snoozeEnabled: true,
      snoozeTurnLimit: 20,
      snoozeDurationMs: 1_000,
    },
  });
  await current.handlers.get("session_start")({}, current.context);
  await flushDeferred();
  const availability = current.sent.find(({ message }) => message.customType === "goal-snooze-availability");
  assert.equal(availability, undefined, "snooze is not advertised at the retry cap");

  const result = await current.tools.get("goal_snooze").execute("snooze", {}, undefined, undefined, current.context);
  assert.equal(result.isError, undefined);
  assert.match(result.content[0].text, /retry limit/);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 0);
  const status = await callGoalTool(current, { action: "status" });
  assert.equal(status.details.notDoneWakeups, 20);
});

test("goal snooze persists escalation when wakeup delivery fails", async () => {
  const runtime = fakeTimer();
  const current = fixture({
    extensionOptions: runtime,
    sendMessage: (message) => {
      if (message.customType === "goal-result") throw new Error("snooze delivery failed");
    },
  });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);

  const result = await current.tools.get("goal_snooze").execute("snooze", {}, undefined, undefined, current.context);
  assert.equal(result.isError, undefined);
  runtime.advance(1_000);
  assert.equal(current.entries.at(-1).data.notDoneWakeups, 1);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 1);
});

test("snooze commands persist turn limits, budgets, and refills for the WebUI", async () => {
  const current = fixture();
  assert.ok(current.commands.has("goal-snooze-turn-limit"));
  assert.ok(current.commands.has("goal-snooze-budget"));
  assert.ok(current.commands.has("goal-snooze-budget-refill"));

  await current.commands.get("goal-snooze-turn-limit").handler("0", current.context);
  assert.equal(current.notices.at(-1).type, "error");
  await current.commands.get("goal-snooze-turn-limit").handler("21", current.context);
  assert.equal(current.notices.at(-1).type, "error");
  await current.commands.get("goal-snooze-turn-limit").handler("11 extra", current.context);
  assert.equal(current.notices.at(-1).type, "error");
  await current.commands.get("goal-snooze-turn-limit").handler("20", current.context);
  assert.equal(current.notices.at(-1).message, "Goal snooze turn limit set: 20");

  for (const value of ["0s", "-1s", "1x", "1e309s", "999999999999999999999999999999999999999999999999999999s"]) {
    await current.commands.get("goal-snooze-budget").handler(value, current.context);
    assert.equal(current.notices.at(-1).type, "error", value);
  }
  await current.commands.get("goal-snooze-budget").handler("2s", current.context);
  await current.commands.get("goal-snooze-budget-refill").handler("2s/1s", current.context);
  assert.equal(current.notices.at(-1).message, "Goal snooze budget refill set");
  for (const value of ["0s/1s", "1s/0s", "1s/-1s", "1s/1x", "1e309s/1s", "1s/1e309s", "1s/1s/1s"]) {
    await current.commands.get("goal-snooze-budget-refill").handler(value, current.context);
    assert.equal(current.notices.at(-1).type, "error", value);
  }
  assert.deepEqual(current.entries.at(-1).data, {
    enabled: false,
    goal: "",
    notDoneWakeups: 0,
    completedThings: 0,
    goalLockEnabled: false,
    snoozeEnabled: true,
    snoozeDurationMs: 60_000,
    snoozeTurnLimit: 20,
    snoozeBudgetCapMs: 2_000,
    snoozeBudgetAvailableMs: 2_000,
    snoozeRefillYieldMs: 2_000,
    snoozeRefillElapsedMs: 1_000,
    outcomeCounts: { COMPLETED: 0, NOT_DONE: 0 },
  });

  await current.commands.get("goal-snooze-budget").handler("off", current.context);
  assert.equal(current.notices.at(-1).message, "Goal snooze budget off");
  assert.equal(current.entries.at(-1).data.snoozeTurnLimit, 20);
  assert.equal(Object.hasOwn(current.entries.at(-1).data, "snoozeBudgetCapMs"), false);
  await current.commands.get("goal-snooze").handler("off", current.context);
  assert.equal(current.entries.at(-1).data.snoozeEnabled, false);
  assert.equal(Object.hasOwn(current.entries.at(-1).data, "snoozeTurnLimit"), false);
  assert.equal(Object.hasOwn(current.entries.at(-1).data, "snoozeBudgetCapMs"), false);
});

test("custom snooze turn limit supports levels eleven through twenty and resets on disable", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  await current.commands.get("goal-snooze-turn-limit").handler("20", current.context);

  const snooze = current.tools.get("goal_snooze");
  for (let level = 1; level <= 20; level += 1) {
    const waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
    runtime.advance(1_000);
    const result = await waiting;
    assert.equal(result.isError, undefined, `level ${level}`);
    runtime.advance(0);
  }
  const expectedWakeups = [
    "Review make progress and take the next concrete action now.",
    "Continue make progress by completing its next actionable step now.",
    "Re-engage with make progress and deliver the next concrete result now.",
    "Identify what remains for make progress and act on it now.",
    "Advance make progress with a specific, verifiable next action now.",
    "make progress still needs action—COMPLETE the next step now!",
    "Return to make progress, address the current gap, and move it forward NOW.",
    "Take ownership of make progress and complete its next concrete action now!",
    "Do not leave make progress idle—resolve the next blocker and proceed NOW.",
    "make progress requires visible progress: take the next actionable step now!",
    "make progress is NOT_DONE. Resolve the highest-priority remaining item and act now.",
    "Move make progress forward immediately by completing the next required action.",
    "make progress remains incomplete—address the outstanding work and produce a result NOW.",
    "Take direct action on make progress now; do not stop at planning or inspection.",
    "make progress needs urgent progress. Complete the next concrete deliverable NOW!",
    "make progress IS STILL INCOMPLETE. Take the next required action immediately!",
    "ACT NOW on make progress: resolve the top remaining issue and report the concrete result.",
    "make progress cannot remain stalled. COMPLETE THE NEXT ACTION NOW and make progress visible!",
    "TAKE OWNERSHIP of make progress immediately—finish the highest-priority remaining step and verify it NOW!",
    "make progress IS NOT COMPLETE. ACT NOW, DELIVER THE NEXT VERIFIED RESULT, AND ACCOUNT FOR THE REMAINING WORK!",
  ].map((message) => `NOT_DONE: ${message}\nVERDICT: NOT_DONE\nREASON: Work remains.`);
  assert.deepEqual(current.sent.filter(({ message }) => message.customType === "goal-result").map(({ message }) => message.content), expectedWakeups);
  const status = await callGoalTool(current, { action: "status" });
  assert.equal(status.details.notDoneWakeups, 20);
  assert.equal((await snooze.execute("snooze", {}, undefined, undefined, current.context)).isError, true);

  await current.commands.get("goal-snooze").handler("", current.context);
  const resetStatus = await callGoalTool(current, { action: "status" });
  assert.equal(resetStatus.details.notDoneWakeups, 20);
});

test("/goal-snooze off clears a custom turn limit before snooze is re-enabled", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  await current.commands.get("goal-snooze-turn-limit").handler("2", current.context);
  const snooze = current.tools.get("goal_snooze");

  let waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  runtime.advance(1_000);
  assert.equal((await waiting).isError, undefined);
  runtime.advance(0);
  await current.commands.get("goal-snooze").handler("off", current.context);
  await current.commands.get("goal-snooze").handler("1s", current.context);

  waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  runtime.advance(1_000);
  const result = await waiting;
  assert.equal(result.isError, undefined, "the second snooze passes the old custom limit");
  assert.match(result.content[0].text, /snooze scheduled/);
  assert.equal((await callGoalTool(current, { action: "status" })).details.notDoneWakeups, 2);
});

test("snooze budget consumes completed waits and auto-disables at zero without refill", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  await current.commands.get("goal-snooze-budget").handler("2s", current.context);
  const snooze = current.tools.get("goal_snooze");

  for (let level = 1; level <= 2; level += 1) {
    const waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
    runtime.advance(1_000);
    assert.equal((await waiting).isError, undefined);
    runtime.advance(0);
  }
  assert.equal((await snooze.execute("snooze", {}, undefined, undefined, current.context)).isError, true);
  assert.equal((await callGoalTool(current, { action: "status" })).details.notDoneWakeups, 2);
});

test("snooze refill carries partial periods, allows yield above elapsed, and enforces the cap", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "make progress");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  await current.commands.get("goal-snooze-budget").handler("1s", current.context);
  await current.commands.get("goal-snooze-budget-refill").handler("2s/1s", current.context);
  const snooze = current.tools.get("goal_snooze");

  let waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  runtime.advance(1_000);
  await waiting;
  runtime.advance(0);
  runtime.advance(250);
  waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  runtime.advance(1_000);
  assert.equal((await waiting).isError, undefined, "a positive partial refill admits a wait");
  runtime.advance(0);
  runtime.advance(750);
  waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  runtime.advance(1_000);
  assert.equal((await waiting).isError, undefined, "refill is capped before admitting the wait");
  runtime.advance(0);
  assert.equal((await snooze.execute("snooze", {}, undefined, undefined, current.context)).isError, true, "the completed wait consumes the capped balance");
});

test("snooze budget pauses refill during waits and cancellation consumes nothing", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "first goal");
  await current.commands.get("goal-snooze").handler("1s", current.context);
  await current.commands.get("goal-snooze-budget").handler("1s", current.context);
  await current.commands.get("goal-snooze-budget-refill").handler("1s/1s", current.context);
  const snooze = current.tools.get("goal_snooze");

  let waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  assert.equal((await waiting).isError, undefined);
  runtime.advance(1_000);
  runtime.advance(0);
  assert.equal((await snooze.execute("snooze", {}, undefined, undefined, current.context)).isError, true, "waiting did not refill itself");
  runtime.advance(999);
  waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  runtime.advance(1_000);
  assert.equal((await waiting).isError, undefined, "a positive refill balance admits the wait");
  await current.commands.get("goal-snooze-budget").handler("1s", current.context);
  waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  assert.equal((await waiting).isError, undefined);
  runtime.advance(500);
  await current.commands.get("goal").handler("set second goal", current.context);
  runtime.advance(500);
  assert.equal(current.sent.filter(({ message }) => message.customType === "goal-result").length, 2, "canceled waiting produced no wakeup");
  waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  assert.equal((await waiting).isError, undefined, "canceled waiting did not consume the budget");
  runtime.advance(1_000);
});

test("session recovery clears ephemeral snooze budget and turn limit but restores snooze settings", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "recover the goal");
  await current.commands.get("goal-snooze").handler("2s", current.context);
  await current.commands.get("goal-snooze-turn-limit").handler("20", current.context);
  await current.commands.get("goal-snooze-budget").handler("1s", current.context);
  await current.commands.get("goal-snooze-budget-refill").handler("1s/1s", current.context);
  const persisted = [...current.branch];
  const recovered = fixture({ extensionOptions: runtime });
  recovered.branch.push(...persisted);
  await recovered.handlers.get("session_start")({}, recovered.context);
  assert.equal(recovered.entries.length, 0);
  const snooze = recovered.tools.get("goal_snooze");
  const waiting = snooze.execute("snooze", {}, undefined, undefined, recovered.context);
  runtime.advance(2_000);
  assert.equal((await waiting).isError, undefined, "recovery must not restore the ephemeral budget");
  runtime.advance(0);
  assert.equal((await callGoalTool(recovered, { action: "status" })).details.notDoneWakeups, 1);
});

test("snooze defaults remain configurable and persist across session recovery", async () => {
  const current = fixture();
  await setGoal(current, "recover snooze settings");
  await current.commands.get("goal-snooze").handler("off", current.context);
  await current.commands.get("goal-snooze").handler("123s", current.context);

  const recovered = fixture();
  recovered.branch.push(...current.branch);
  await recovered.handlers.get("session_start")({}, recovered.context);
  const status = await callGoalTool(recovered, { action: "status" });
  assert.equal(status.details.snoozeEnabled, true);
  assert.equal(status.details.snoozeDurationMs, 123_000);

  await current.commands.get("goal-snooze").handler("off", current.context);
  const disabled = fixture();
  disabled.branch.push(...current.branch);
  await disabled.handlers.get("session_start")({}, disabled.context);
  const disabledStatus = await callGoalTool(disabled, { action: "status" });
  assert.equal(disabledStatus.details.snoozeEnabled, false);
  assert.equal(disabledStatus.details.snoozeDurationMs, 123_000);
});

test("default snooze budget refill yields thirty seconds per minute", async () => {
  const runtime = fakeTimer();
  const current = fixture({ extensionOptions: runtime });
  await setGoal(current, "refill snooze budget");
  await current.commands.get("goal-snooze").handler("1m", current.context);
  await current.commands.get("goal-snooze-budget").handler("1m", current.context);
  const snooze = current.tools.get("goal_snooze");

  let waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  runtime.advance(60_000);
  assert.equal((await waiting).isError, undefined);
  runtime.advance(0);
  await current.commands.get("goal-snooze").handler("15s", current.context);
  runtime.advance(30_000);

  waiting = snooze.execute("snooze", {}, undefined, undefined, current.context);
  runtime.advance(15_000);
  assert.equal((await waiting).isError, undefined);
  runtime.advance(0);
  const exhausted = await snooze.execute("snooze", {}, undefined, undefined, current.context);
  assert.equal(exhausted.isError, true, "thirty seconds should refill fifteen seconds after the first minute is spent");
});

test("legacy goal state defaults snooze to enabled with the standard duration", async () => {
  const current = fixture();
  current.branch.push({
    type: "custom",
    customType: GOAL_ENTRY,
    data: { enabled: true, goal: "legacy goal", notDoneWakeups: 0, completedThings: 0 },
  });
  await current.handlers.get("session_start")({}, current.context);
  const status = await callGoalTool(current, { action: "status" });
  assert.equal(status.details.snoozeEnabled, true);
  assert.equal(status.details.snoozeDurationMs, 60_000);
});

test("NOT_DONE cap re-arms only after three new scoped concrete activities and caps again", async () => {
  const current = fixture({
    sessionId: "worker-session",
    response: { content: [{ type: "text", text: "VERDICT: NOT_DONE\\nREASON: Work remains." }] },
  });
  await setGoal(current, "ship the recovery proof");
  await current.commands.get("goal-snooze").handler("off", current.context);
  current.eventHandlers.get(GOAL_WORKER_ID_EVENT)({ sessionId: "worker-session", workerId: "worker" });
  const runEvaluation = async () => {
    await settle(current);
    await flushDeferred();
    await flushDeferred();
  };
  const results = () => current.sent.filter(({ message }) => message.customType === "goal-result");
  for (let attempt = 0; attempt < MAX_NOT_DONE_WAKEUPS; attempt += 1) await runEvaluation();
  assert.equal(results().length, MAX_NOT_DONE_WAKEUPS);
  assert.equal((await callGoalTool(current, { action: "status" })).details.notDoneWakeups, MAX_NOT_DONE_WAKEUPS);
  assert.equal((await callGoalTool(current, { action: "status" })).details.rearmActivityCount, 0);
  assert.equal(current.entries.at(-1).data.rearmActive, true);

  const activity = current.eventHandlers.get(GOAL_ACTIVITY_EVENT);
  const event = (activityId, revision, overrides = {}) => activity({
    activityId,
    kind: "tool-completed",
    revision,
    sessionId: "worker-session",
    workerId: "worker",
    goal: { enabled: true, goal: "ship the recovery proof" },
    ...overrides,
  });
  // Transport/control noise and foreign scopes do not consume a meaningful event.
  event("heartbeat", 1, { kind: "heartbeat" });
  event("foreign-worker", 1, { workerId: "other-worker" });
  event("foreign-goal", 1, { goal: { enabled: true, goal: "other goal" } });
  event("stale-goal-check", 1, { kind: "goal-check" });
  event("action-1", 1);
  event("action-1", 2);
  event("action-2", 2);
  assert.equal((await callGoalTool(current, { action: "status" })).details.notDoneWakeups, MAX_NOT_DONE_WAKEUPS);
  assert.equal((await callGoalTool(current, { action: "status" })).details.rearmActivityCount, 2);
  assert.equal(current.entries.at(-1).data.rearmActivityCount, 2, "partial recovery is persisted");

  // A settled/evaluator boundary cannot consume or reset the partial recovery state.
  await runEvaluation();
  assert.equal(results().length, MAX_NOT_DONE_WAKEUPS);
  assert.equal((await callGoalTool(current, { action: "status" })).details.rearmActivityCount, 2);
  event("action-3", 3);
  assert.equal((await callGoalTool(current, { action: "status" })).details.notDoneWakeups, 0);
  assert.equal((await callGoalTool(current, { action: "status" })).details.rearmActive, undefined);
  assert.equal(current.entries.at(-1).data.rearmActive, undefined);

  await runEvaluation();
  assert.equal(results().length, MAX_NOT_DONE_WAKEUPS + 1, "the next eligible settle delivers the first re-armed attempt");
  assert.equal((await callGoalTool(current, { action: "status" })).details.notDoneWakeups, 1);
  for (let attempt = 0; attempt < MAX_NOT_DONE_WAKEUPS - 1; attempt += 1) await runEvaluation();
  assert.equal(results().length, MAX_NOT_DONE_WAKEUPS * 2);
  assert.equal((await callGoalTool(current, { action: "status" })).details.notDoneWakeups, MAX_NOT_DONE_WAKEUPS);
  await runEvaluation();
  assert.equal(results().length, MAX_NOT_DONE_WAKEUPS * 2, "the second cycle is capped at the retry limit");
});

test("fresh activity producer epochs reject stale events while preserving the NOT_DONE cap", async () => {
  const current = fixture({
    sessionId: "worker-session",
    response: { content: [{ type: "text", text: "VERDICT: NOT_DONE\\nREASON: Work remains." }] },
  });
  await setGoal(current, "fence stale recovery events");
  await current.commands.get("goal-snooze").handler("off", current.context);
  current.eventHandlers.get(GOAL_WORKER_ID_EVENT)({
    sessionId: "worker-session", workerId: "worker", producerEpoch: "epoch-old",
  });
  const runEvaluation = async () => {
    await settle(current);
    await flushDeferred();
    await flushDeferred();
  };
  for (let attempt = 0; attempt < MAX_NOT_DONE_WAKEUPS; attempt += 1) await runEvaluation();
  const activity = current.eventHandlers.get(GOAL_ACTIVITY_EVENT);
  const event = (activityId, revision, producerEpoch) => activity({
    activityId,
    kind: "tool-completed",
    revision,
    sessionId: "worker-session",
    workerId: "worker",
    producerEpoch,
    goal: { enabled: true, goal: "fence stale recovery events" },
  });
  event("old-action", 1, "epoch-old");
  assert.equal((await callGoalTool(current, { action: "status" })).details.rearmActivityCount, 1);
  assert.equal(current.entries.at(-1).data.rearmScope.producerEpoch, "epoch-old");

  const restored = fixture({
    sessionId: "worker-session",
    response: { content: [{ type: "text", text: "VERDICT: NOT_DONE\\nREASON: Work remains." }] },
  });
  restored.branch.push(...current.branch);
  await restored.handlers.get("session_start")({ sessionId: "worker-session" }, restored.context);
  restored.eventHandlers.get(GOAL_WORKER_ID_EVENT)({
    sessionId: "worker-session", workerId: "worker", producerEpoch: "epoch-new",
  });
  assert.equal((await callGoalTool(restored, { action: "status" })).details.rearmActivityCount, 0);
  const restoredActivity = restored.eventHandlers.get(GOAL_ACTIVITY_EVENT);
  const restoredEvent = (activityId, revision, producerEpoch) => restoredActivity({
    activityId,
    kind: "tool-completed",
    revision,
    sessionId: "worker-session",
    workerId: "worker",
    producerEpoch,
    goal: { enabled: true, goal: "fence stale recovery events" },
  });
  restoredEvent("replayed-old-action", 1, "epoch-old");
  assert.equal((await callGoalTool(restored, { action: "status" })).details.rearmActivityCount, 0);
  restoredEvent("new-action-1", 1, "epoch-new");
  restoredEvent("new-action-2", 2, "epoch-new");
  assert.equal((await callGoalTool(restored, { action: "status" })).details.rearmActivityCount, 2);
  restoredEvent("new-action-3", 3, "epoch-new");
  assert.equal((await callGoalTool(restored, { action: "status" })).details.rearmActivityCount, undefined);
  assert.equal((await callGoalTool(restored, { action: "status" })).details.notDoneWakeups, 0);
});
