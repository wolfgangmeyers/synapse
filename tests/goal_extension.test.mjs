import assert from "node:assert/strict";
import test from "node:test";
import { createGoalExtension } from "../pi-extensions/goal.js";

const waitForWakeup = () => new Promise((resolve) => setTimeout(resolve, 15));

test("goal wakeups escalate ten times and reset after three completions", async () => {
  const handlers = new Map();
  const tools = new Map();
  const sent = [];
  const responses = Array(11).fill("NOT_DONE").concat(Array(3).fill("COMPLETED"), ["NOT_DONE"]);
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

  for (let attempt = 0; attempt < 10; attempt += 1) {
    await settle(); // Each injected wakeup must trigger the next retry when it settles.
  }
  assert.equal(notDoneWakeups().length, 10);
  assert.match(notDoneWakeups()[0].content, /Pick the next concrete action/);
  assert.match(notDoneWakeups()[9].content, /waited long enough/);

  await settle();
  assert.equal(notDoneWakeups().length, 10);

  for (let completed = 0; completed < 3; completed += 1) {
    await settle();
  }
  await settle();

  assert.equal(notDoneWakeups().length, 11);
  assert.match(notDoneWakeups().at(-1).content, /Pick the next concrete action/);
});
