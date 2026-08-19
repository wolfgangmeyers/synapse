import assert from "node:assert/strict";
import test from "node:test";

import goalExtension, { responseEvaluation } from "../pi-extensions/goal.js";

const GOAL_ENTRY = "goal-state";
const PI_SUBAGENTS_SERVICE_REGISTRY = Symbol.for("@gotgenes/pi-subagents:service-registry");
const MAX_CONTEXT_BYTES = 24_000;

function fixture({ response = { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] }, complete, onSendMessage, sendMessage, sessionId } = {}) {
  const handlers = new Map();
  const commands = new Map();
  const tools = new Map();
  const entries = [];
  const sent = [];
  const notices = [];
  const branch = [];
  const pi = {
    getSessionId() { return sessionId; },
    on(name, handler) { handlers.set(name, handler); },
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
    model: { provider: "test", id: "goal" },
    modelRegistry: { complete: complete ?? (async () => response) },
    sessionManager: {
      getBranch: () => branch,
      buildContextEntries: () => branch,
      getSessionId: () => sessionId,
    },
    ui: { notify(message, type) { notices.push({ message, type }); } },
  };
  goalExtension(pi);
  return { branch, commands, context, entries, handlers, notices, sent, tools };
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

test("evaluator parses only a strict verdict line and bounds the visible rationale", () => {
  assert.deepEqual(responseEvaluation({ content: [{ type: "text", text: "VERDICT: BLOCKED\nREASON: Need a supervisor decision." }] }), {
    outcome: "BLOCKED",
    rationale: "Need a supervisor decision.",
  });
  assert.deepEqual(responseEvaluation({ content: [{ type: "text", text: "VERDICT: COMPLETED extra\nREASON: This must not change the outcome." }] }), {
    outcome: "NOT_DONE",
    rationale: "Invalid evaluator output.",
  });
  assert.deepEqual(responseEvaluation({ content: [{ type: "text", text: "VERDICT: NOT_DONE\nREASON: one\nREASON: two" }] }), {
    outcome: "NOT_DONE",
    rationale: "Invalid evaluator output.",
  });
  assert.equal(responseEvaluation({ content: [{ type: "text", text: "REASON: Missing verdict" }] }).outcome, "NOT_DONE");
  assert.equal(responseEvaluation({ content: [{ type: "text", text: `VERDICT: NOT_DONE\nREASON: ${"x".repeat(300)}` }] }).rationale, "Invalid evaluator output.");
});

test("goal tool registers its action schema and shares state with /goal", async () => {
  const current = fixture();
  const tool = current.tools.get("goal");
  assert.ok(tool);
  assert.ok(tool.promptSnippet);
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
  assert.deepEqual(statusResult.details, { action: "status", enabled: true, goal: "ship the tool", evaluatorCalls: 0, lastOwnerGateState: "allowed" });
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

test("/goal commands persist branch-local state and support status/off", async () => {
  const current = fixture();
  await setGoal(current, "ship the focused extension");
  assert.deepEqual(current.entries.at(-1), {
    type: "custom",
    customType: GOAL_ENTRY,
    data: { enabled: true, goal: "ship the focused extension" },
  });
  await current.commands.get("goal").handler("status", current.context);
  assert.deepEqual(current.notices.at(-1), { message: "Goal enabled: ship the focused extension", type: "info" });

  const restored = fixture();
  restored.branch.push(...current.branch);
  await restored.handlers.get("session_start")({}, restored.context);
  await restored.commands.get("goal").handler("status", restored.context);
  assert.deepEqual(restored.notices.at(-1), { message: "Goal enabled: ship the focused extension", type: "info" });

  await restored.commands.get("goal").handler("off", restored.context);
  assert.deepEqual(restored.entries.at(-1).data, { enabled: false, goal: "ship the focused extension" });
  await settle(restored);
  assert.equal(restored.sent.length, 0);
});

test("settled goal evaluation uses bounded active context and exact steer wakeup options", async () => {
  const calls = [];
  const current = fixture({ complete: async (model, request, options) => {
    calls.push({ model, request, options });
    return { content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] };
  } });
  await setGoal(current, "finish the goal extension");
  current.branch.push({ type: "message", message: { role: "user", content: "x".repeat(MAX_CONTEXT_BYTES * 2) } });

  await settle(current);
  assert.equal(calls.length, 0, "evaluation starts only after a macrotask defer");
  await flushDeferred();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].model, current.context.model);
  assert.ok(calls[0].options?.signal instanceof AbortSignal);
  const prompt = calls[0].request.systemPrompt;
  assert.equal(prompt, "goal: finish the goal extension - return exactly two lines: VERDICT: BLOCKED|COMPLETED|NOT_DONE and REASON: <concise rationale>");
  assert.ok(Buffer.byteLength(prompt) <= 2_048 + MAX_CONTEXT_BYTES + 1_000);
  await flushDeferred();
  assert.deepEqual(current.sent, [{
    message: { customType: "goal-result", content: "Goal check: COMPLETED. Contact the supervisor and report completion.\nVERDICT: COMPLETED\nREASON: The goal is complete.", display: true, details: { goal: "finish the goal extension", outcome: "COMPLETED", rationale: "The goal is complete." } },
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

  await settle(current);
  assert.equal(current.sent.length, 0, "the continuation waits until the settled callback returns");
  await flushDeferred();
  await flushDeferred();
  await injectedSettle;
  assert.equal(current.sent[0].message.content, "Goal check: NOT_DONE. Resume working toward this goal: make progress.\nVERDICT: NOT_DONE\nREASON: Evaluated current progress.");
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
    await settle(current);
    await flushDeferred();
    await flushDeferred();
    assert.equal(current.sent[0].message.content, "Goal check: NOT_DONE. Resume working toward this goal: make progress.\nVERDICT: NOT_DONE\nREASON: Invalid evaluator output.");
  }

  let reject = true;
  const current = fixture({ complete: async () => { if (reject) throw new Error("provider unavailable"); return { content: [{ type: "text", text: "VERDICT: BLOCKED\nREASON: A supervisor decision is required." }] }; } });
  await setGoal(current, "make progress");
  await settle(current);
  await flushDeferred();
  await flushDeferred();
  assert.equal(current.sent[0].message.content, "Goal check: NOT_DONE. Resume working toward this goal: make progress.\nVERDICT: NOT_DONE\nREASON: Evaluator unavailable.");
});

test("BLOCKED wakeup tells the worker to contact the supervisor", async () => {
  const current = fixture({ response: { content: [{ type: "text", text: "VERDICT: BLOCKED\nREASON: A supervisor decision is required." }] } });
  await setGoal(current, "finish the goal extension");
  await settle(current);
  await flushDeferred();
  await flushDeferred();
  assert.equal(current.sent[0].message.content, "Goal check: BLOCKED. Contact the supervisor with the blocker and what is needed.\nVERDICT: BLOCKED\nREASON: A supervisor decision is required.");
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

test("missing optional subagent service does not break goal evaluation", async () => {
  const current = fixture({ sessionId: "owner-session" });
  await current.handlers.get("session_start")({ sessionId: "owner-session" }, current.context);
  const result = await callGoalTool(current, { action: "set", goal: "continue working" });
  assert.equal(result.isError, undefined);
  await settle(current);
  await flushDeferred();
  await flushDeferred();
  assert.equal(current.sent.length, 1);
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
    assert.equal(current.sent.length, 0);
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
    assert.equal(current.sent.length, 0, "active owner work suppresses the wakeup");
    const status = await callGoalTool(current, { action: "status" });
    assert.deepEqual(status.details, { action: "status", enabled: true, goal: "wait for the owner child", evaluatorCalls: 1, lastOwnerGateState: "suppressed" });
    active = false;
    await settle(current);
    await flushDeferred();
    await flushDeferred();
    assert.equal(current.sent.length, 1, "a later eligible settle resumes evaluation");
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
    assert.equal(current.sent.length, 1);
  } finally {
    cleanup();
  }
});

test("goal off cancels an in-flight evaluation and prevents stale injection", async () => {
  let resolve;
  const current = fixture({ complete: () => new Promise((done) => { resolve = done; }) });
  await setGoal(current, "cancel safely");
  const pending = settle(current);
  await flushDeferred();
  await current.commands.get("goal").handler("off", current.context);
  resolve({ content: [{ type: "text", text: "VERDICT: COMPLETED\nREASON: The goal is complete." }] });
  await pending;
  assert.equal(current.sent.length, 0);
});
