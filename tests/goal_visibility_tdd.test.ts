import assert from "node:assert/strict";
import test from "node:test";

import goalExtension, { GOAL_CONTROL_STATE_EVENT, GOAL_STATE_EVENT, GOAL_STATE_REQUEST_EVENT } from "../pi-extensions/mecha-messaging/goal.ts";

function fixture({ sessionId = "worker-session", branch = [], onSend } = {}) {
  const handlers = new Map();
  const events = new Map();
  const commands = new Map();
  const entries = [...branch];
  const emitted = [];
  const sent = [];
  const pi = {
    getSessionId: () => sessionId,
    on: (name, handler) => handlers.set(name, handler),
    events: {
      on: (name, handler) => events.set(name, handler),
      emit: (name, data) => { emitted.push({ name, data }); events.get(name)?.(data); },
    },
    appendEntry: (customType, data) => entries.push({ type: "custom", customType, data }),
    registerCommand: (name, command) => commands.set(name, command),
    registerTool: () => {},
    sendMessage: (message, options) => { try { onSend?.(entries); } finally { sent.push({ message, options }); } },
  };
  const context = {
    model: undefined,
    sessionManager: { getBranch: () => entries, getSessionId: () => sessionId },
    ui: { notify: () => {} },
  };
  goalExtension(pi);
  return { commands, context, emitted, entries, events, handlers, sent };
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

test("goal publishes cached normalized state and replays it to a late subscriber", async () => {
  const current = fixture();
  await current.handlers.get("session_start")({}, current.context);
  await current.commands.get("goal").handler("set  ship <the> goal  ", current.context);
  assert.deepEqual(current.emitted.slice(-2), [
    { name: GOAL_STATE_EVENT, data: { enabled: true, goal: "ship <the> goal" } },
    {
      name: GOAL_CONTROL_STATE_EVENT,
      data: {
        enabled: true,
        goal: "ship <the> goal",
        goalLockEnabled: false,
        snoozeEnabled: true,
        snoozeDurationMs: 60_000,
        snoozeTurnLimit: null,
        snoozeBudgetCapMs: null,
        snoozeRefillYieldMs: 30_000,
        snoozeRefillElapsedMs: 60_000,
        outcomeCounts: { COMPLETED: 0, NOT_DONE: 0 },
      },
    },
  ]);
  const replay = [];
  current.events.set(GOAL_STATE_EVENT, (data) => replay.push(data));
  await current.events.get(GOAL_STATE_REQUEST_EVENT)();
  assert.deepEqual(replay, [{ enabled: true, goal: "ship <the> goal" }]);
  await current.commands.get("goal").handler("off", current.context);
  assert.deepEqual(current.emitted.slice(-2), [
    { name: GOAL_STATE_EVENT, data: { enabled: false, goal: "" } },
    {
      name: GOAL_CONTROL_STATE_EVENT,
      data: {
        enabled: false,
        goal: "",
        goalLockEnabled: false,
        snoozeEnabled: true,
        snoozeDurationMs: 60_000,
        snoozeTurnLimit: null,
        snoozeBudgetCapMs: null,
        snoozeRefillYieldMs: 30_000,
        snoozeRefillElapsedMs: 60_000,
        outcomeCounts: { COMPLETED: 0, NOT_DONE: 0 },
      },
    },
  ]);
});

test("goal set persists before one deferred self-wakeup and off does not wake", async () => {
  const current = fixture({ onSend: (entries) => assert.deepEqual(entries.at(-1).data, { enabled: true, goal: "start work", notDoneWakeups: 0, completedThings: 0, goalLockEnabled: false, snoozeEnabled: false, snoozeDurationMs: 60_000 }) });
  await current.commands.get("goal").handler("set start work", current.context);
  await current.commands.get("goal-snooze").handler("off", current.context);
  assert.equal(current.sent.length, 0);
  await flush();
  assert.equal(current.sent.length, 1);
  assert.deepEqual(current.sent[0].options, { deliverAs: "steer", triggerTurn: true });
  await current.commands.get("goal").handler("off", current.context);
  await flush();
  assert.equal(current.sent.length, 1);
});

test("goal wake failure leaves persisted state and does not evaluate", async () => {
  const current = fixture({ onSend: () => { throw new Error("wake unavailable"); } });
  await current.commands.get("goal").handler("set keep working", current.context);
  await current.commands.get("goal-snooze").handler("off", current.context);
  await flush();
  assert.deepEqual(current.entries.at(-1).data, { enabled: true, goal: "keep working", notDoneWakeups: 0, completedThings: 0, goalLockEnabled: false, snoozeEnabled: false, snoozeDurationMs: 60_000, outcomeCounts: { COMPLETED: 0, NOT_DONE: 0 } });
  assert.equal(current.sent.length, 1);
});
