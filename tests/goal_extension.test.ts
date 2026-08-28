import assert from "node:assert/strict";
import test from "node:test";
import { createGoalExtension } from "../pi-extensions/mecha-messaging/goal.ts";

const waitForWakeup = () => new Promise((resolve) => setTimeout(resolve, 15));
const MAX_NOT_DONE_WAKEUPS = 1000;

test("goal wakeups reach the retry cap and reset after three completions", async () => {
  const handlers = new Map();
  const tools = new Map();
  const commands = new Map();
  const sent = [];
  const responses = Array(MAX_NOT_DONE_WAKEUPS + 1).fill("NOT_DONE").concat(Array(3).fill("COMPLETED"), ["NOT_DONE"]);
  const pi = {
    getSessionId: () => "session",
    appendEntry: () => {},
    sendMessage: (message) => sent.push(message),
    events: { on: () => {}, emit: () => {} },
    on: (name, handler) => handlers.set(name, handler),
    registerTool: (tool) => tools.set(tool.name, tool),
    registerCommand: (name, command) => commands.set(name, command),
  };
  const ctx = {
    model: { provider: "test" },
    sessionManager: { getSessionId: () => "session", getBranch: () => [] },
    modelRegistry: {
      complete: async () => ({
        content: [{ type: "text", text: `VERDICT: ${responses.shift()}\nREASON: still checking` }],
      }),
    },
  };
  createGoalExtension(pi);
  await tools.get("goal").execute("x", { action: "set", goal: "ship the fix" }, undefined, undefined, ctx);
  const settle = async () => {
    handlers.get("agent_settled")({ sessionId: "session" }, ctx);
    await waitForWakeup();
  };
  const resultWakeups = () => sent.filter((message) => message.customType === "goal-result");
  const notDoneWakeups = () => resultWakeups().filter((message) => message.details.outcome === "NOT_DONE");

  for (let attempt = 0; attempt < MAX_NOT_DONE_WAKEUPS; attempt += 1) {
    await settle(); // Each injected wakeup must trigger the next retry when it settles.
  }
  assert.equal(notDoneWakeups().length, MAX_NOT_DONE_WAKEUPS);
  assert.equal(notDoneWakeups()[0].content, "NOT_DONE: Review ship the fix and take the next concrete action now.\nVERDICT: NOT_DONE\nREASON: still checking");
  assert.equal(notDoneWakeups().at(-1).content, "NOT_DONE: ship the fix IS NOT COMPLETE. ACT NOW, DELIVER THE NEXT VERIFIED RESULT, AND ACCOUNT FOR THE REMAINING WORK!\nVERDICT: NOT_DONE\nREASON: still checking");

  await settle();
  assert.equal(notDoneWakeups().length, MAX_NOT_DONE_WAKEUPS);

  for (let completed = 0; completed < 3; completed += 1) {
    await settle();
  }
  await settle();

  assert.equal(notDoneWakeups().length, MAX_NOT_DONE_WAKEUPS + 1);
  assert.equal(notDoneWakeups().at(-1).content, "NOT_DONE: Review ship the fix and take the next concrete action now.\nVERDICT: NOT_DONE\nREASON: still checking");
});

test("goal wakeup converts an unsupported evaluator verdict to NOT_DONE", async () => {
  const handlers = new Map();
  const tools = new Map();
  const sent = [];
  const pi = {
    getSessionId: () => "session",
    appendEntry: () => {},
    sendMessage: (message) => sent.push(message),
    events: { on: () => {}, emit: () => {} },
    on: (name, handler) => handlers.set(name, handler),
    registerTool: (tool) => tools.set(tool.name, tool),
    registerCommand: () => {},
  };
  const ctx = {
    model: { provider: "test" },
    sessionManager: { getSessionId: () => "session", getBranch: () => [] },
    modelRegistry: {
      complete: async () => ({
        content: [{ type: "text", text: "VERDICT: NEEDS_REVIEW\nREASON: A different outcome was requested." }],
      }),
    },
  };
  createGoalExtension(pi);
  await tools.get("goal").execute("x", { action: "set", goal: "ship the fix" }, undefined, undefined, ctx);

  handlers.get("agent_settled")({ sessionId: "session" }, ctx);
  await waitForWakeup();

  const result = sent.find((message) => message.customType === "goal-result");
  assert.equal(result.details.goal, "ship the fix");
  assert.equal(result.details.outcome, "NOT_DONE");
  assert.match(result.content, /^NOT_DONE: Review ship the fix and take the next concrete action now\.\nVERDICT: NOT_DONE\nREASON: Invalid evaluator output:/);
});
