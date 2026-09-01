import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import goalExtension, { GOAL_CONTROL_REQUEST_EVENT } from "../pi-extensions/mecha-goal/index.ts";
import { createMechaMessagingExtension } from "../pi-extensions/mecha-messaging/index.ts";
import { createDiagnosticJournal } from "../pi-extensions/mecha-messaging/diagnostics-history.js";

const settings = {
  goalLockEnabled: true,
  snoozeEnabled: true,
  snoozeDurationMs: 120_000,
  snoozeTurnLimit: 4,
  snoozeBudgetCapMs: 300_000,
  snoozeRefillYieldMs: 30_000,
  snoozeRefillElapsedMs: 60_000,
};

function fixture({ installGoal = true, immediateTimeout = false, environment, emitFailure, sendGoalStateFailure } = {}) {
  const handlers = new Map();
  const eventHandlers = new Map();
  const entries = [];
  const controlRequests = [];
  const published = [];
  let clientOptions;
  const bus = {
    on(name, handler) {
      const current = eventHandlers.get(name) ?? [];
      current.push(handler);
      eventHandlers.set(name, current);
    },
    emit(name, value) {
      emitFailure?.(name, value);
      for (const handler of eventHandlers.get(name) ?? []) handler(value);
    },
  };
  const pi = {
    events: bus,
    getSessionId: () => "parent-session",
    on(name, handler) {
      const current = handlers.get(name) ?? [];
      current.push(handler);
      handlers.set(name, current);
    },
    appendEntry(customType, data) {
      entries.push({ type: "custom", customType, data });
    },
    registerCommand() {},
    registerTool() {},
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
  };
  const context = {
    sessionManager: {
      getSessionId: () => "parent-session",
      getBranch: () => entries,
    },
    ui: { notify() {}, setStatus() {} },
  };
  const client = {
    connect: async () => clientOptions.onConnectionState({ state: "connected" }),
    close() {},
    sendReceipt() {},
    sendActivity() {},
    sendGoalState(state) {
      if (sendGoalStateFailure) throw new Error("private publication error");
      published.push(state);
    },
  };

  if (installGoal) goalExtension(pi);
  bus.on(GOAL_CONTROL_REQUEST_EVENT, (request) => controlRequests.push(request));
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "parent",
    createClient(options) {
      clientOptions = options;
      return client;
    },
    createSettledScheduler: () => ({
      onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {}, dispose() {},
    }),
    ...(environment === undefined ? {} : { operationalTools: { environment } }),
    ...(immediateTimeout ? {
      setTimeoutImpl(callback) { callback(); return 1; },
      clearTimeoutImpl() {},
      setIntervalImpl() { return undefined; },
      clearIntervalImpl() {},
    } : {}),
  });

  return { bus, client, clientOptions: () => clientOptions, context, controlRequests, handlers, published };
}

async function trigger(fixture, name, event = {}) {
  for (const handler of fixture.handlers.get(name) ?? []) await handler(event, fixture.context);
}

test("bridges complete goal state and typed parent controls through the EventBus", async () => {
  const current = fixture();
  await trigger(current, "session_start", { sessionId: "parent-session" });

  const options = current.clientOptions();
  assert.equal(typeof options.getGoalState, "function");
  assert.equal(typeof options.onGoalControl, "function");
  assert.deepEqual(options.getGoalState(), {
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
  });
  assert.deepEqual(current.published.at(-1), options.getGoalState());

  const result = await options.onGoalControl({
    correlationId: "correlation-1",
    controlId: "control-1",
    sourceAgentId: "goal-web",
    targetInstanceId: "web-instance",
    revision: 1,
    action: "set",
    goal: "ship the bridge",
    settings,
  });

  assert.equal(result.accepted, true);
  assert.deepEqual(current.controlRequests[0], {
    correlationId: "correlation-1",
    controlId: "control-1",
    sourceAgentId: "goal-web",
    targetInstanceId: "web-instance",
    revision: 1,
    action: "set",
    goal: "ship the bridge",
    settings,
    context: current.context,
    respond: current.controlRequests[0].respond,
  });
  assert.equal(result.goalState.enabled, true);
  assert.equal(result.goalState.goal, "ship the bridge");
  assert.deepEqual(result.goalState.outcomeCounts, {
    COMPLETED: 0,
    NOT_DONE: 0,
  });
  assert.deepEqual(options.getGoalState(), result.goalState);
  assert.deepEqual(current.published.at(-1), result.goalState);

  const rejected = await options.onGoalControl({
    controlId: "invalid-control",
    sourceAgentId: "goal-web",
    targetInstanceId: "web-instance",
    revision: 0,
    action: "off",
    goal: "",
    settings,
  });
  assert.equal(rejected.accepted, false);
  assert.equal(rejected.errorCode, "invalid_control");
  assert.deepEqual(rejected.goalState, result.goalState);
});

test("records bridge activity gates and publication failures without goal text", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-goal-bridge-diagnostic-"));
  const environment = {
    HOME: directory,
    MECHA_TMUX_WINDOW: "goal-bridge-test",
    MECHA_PI_DIAGNOSTIC_JOURNAL_PATH: join(directory, "journal.jsonl"),
  };
  const journal = createDiagnosticJournal({ environment });
  let failActivityEmit = false;
  try {
    const current = fixture({
      environment,
      sendGoalStateFailure: true,
      emitFailure: (name) => {
        if (failActivityEmit && name === "goal:meaningful-activity") throw new Error("private bridge error");
      },
    });
    await trigger(current, "session_start", { sessionId: "parent-session" });
    current.bus.emit("goal:state", { enabled: true, goal: "private goal text" });
    await trigger(current, "tool_result", { toolName: "bash", toolCallId: "tool-1", isError: false });
    await trigger(current, "tool_result", { toolName: "bash", toolCallId: "tool-1", isError: false });
    failActivityEmit = true;
    await trigger(current, "tool_result", { toolName: "edit", toolCallId: "tool-2", isError: false });
    await journal.flush();
    assert.equal(typeof journal.path, "string");
    if (typeof journal.path !== "string") throw new Error("diagnostic journal path is unavailable");
    const records = (await readFile(journal.path, "utf8")).trim().split("\n").map((line) => JSON.parse(line));
    assert.ok(records.some(({ event, reason }) => event === "activity_gate_rejected" && reason === "duplicate_activity"));
    assert.ok(records.some(({ event, reason, error_name }) => event === "publication_failed" && reason === "activity_emit" && error_name === "Error"));
    assert.ok(records.some(({ event, reason }) => event === "publication_failed" && reason === "goal_state_publish"));
    assert.doesNotMatch(JSON.stringify(records), /private goal text|private bridge error|private publication error/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("bounds a missing EventBus control response and returns the cached full state", async () => {
  const current = fixture({ installGoal: false, immediateTimeout: true });
  await trigger(current, "session_start", { sessionId: "parent-session" });
  const result = await current.clientOptions().onGoalControl({
    controlId: "missing-handler-control",
    sourceAgentId: "goal-web",
    targetInstanceId: "web-instance",
    revision: 1,
    action: "off",
    goal: "",
    settings,
  });
  assert.deepEqual(result, {
    accepted: false,
    errorCode: "unavailable",
    goalState: current.clientOptions().getGoalState(),
  });
});
