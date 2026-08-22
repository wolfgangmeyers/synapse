import assert from "node:assert/strict";
import test from "node:test";

import { createMailboxClient } from "../pi-extensions/mailbox-client.js";

const state = {
  enabled: true,
  goal: "ship goal control",
  goalLockEnabled: true,
  snoozeEnabled: true,
  snoozeDurationMs: 60_000,
  snoozeTurnLimit: 4,
  snoozeBudgetCapMs: 300_000,
  snoozeRefillYieldMs: 30_000,
  snoozeRefillElapsedMs: 60_000,
  outcomeCounts: { BLOCKED: 1, COMPLETED: 2, NOT_DONE: 3, WAITING_FOR_USER_AUTHORIZATION: 4 },
};

function wireState(value = state) {
  return {
    enabled: value.enabled,
    goal: value.goal,
    goal_lock_enabled: value.goalLockEnabled,
    snooze_enabled: value.snoozeEnabled,
    snooze_duration_ms: value.snoozeDurationMs,
    snooze_turn_limit: value.snoozeTurnLimit,
    snooze_budget_cap_ms: value.snoozeBudgetCapMs,
    snooze_refill_yield_ms: value.snoozeRefillYieldMs,
    snooze_refill_elapsed_ms: value.snoozeRefillElapsedMs,
    outcome_counts: value.outcomeCounts,
  };
}

function wireSettings(value = state) {
  const full = wireState(value);
  const { outcome_counts: unused, ...settings } = full;
  return settings;
}

function socketFixture(sent) {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.send = (payload) => sent.push(JSON.parse(payload));
  socket.close = () => {
    socket.readyState = 3;
    socket.dispatchEvent(new Event("close"));
  };
  return socket;
}

async function registeredClient({ capabilities, getGoalState = () => state, onGoalControl, onGoalControlTrace, webSocketFactory, now } = {}) {
  const sent = [];
  const sockets = [];
  const timers = [];
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    getGoalState,
    ...(onGoalControl === undefined ? {} : { onGoalControl }),
    ...(onGoalControlTrace === undefined ? {} : { onGoalControlTrace }),
    ...(now === undefined ? {} : { now }),
    webSocketFactory: webSocketFactory ?? (() => {
      const socket = socketFixture(sent);
      sockets.push(socket);
      return socket;
    }),
    fetchImpl: async () => ({ ok: true, json: async () => capabilities ?? { goal_control_v1: true } }),
    generateId: (() => { let id = 0; return () => `id-${++id}`; })(),
    reconnectInitialDelayMs: 1,
    reconnectMaxDelayMs: 1,
    setTimeoutImpl: (callback, delay) => {
      timers.push({ callback, delay });
      return timers.length;
    },
    clearTimeoutImpl: () => {},
    setIntervalImpl: () => 1,
    clearIntervalImpl: () => {},
    random: () => 0,
  });
  const connected = client.connect();
  const socket = sockets[0];
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  const register = sent.find((frame) => frame.type === "register");
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1", request_id: register.frame_id,
  }) }));
  await connected;
  await new Promise((resolve) => setImmediate(resolve));
  return { client, sent, sockets, socket, timers };
}

test("goal state publication is capability-gated and uses a separate full-state frame", async () => {
  const unsupported = await registeredClient({ capabilities: { model_catalog_v1: true } });
  assert.equal(unsupported.sent.some((frame) => frame.type === "goal_state"), false);
  assert.equal(unsupported.client.sendGoalState(state), false);
  unsupported.client.close();

  const supported = await registeredClient();
  const published = supported.sent.find((frame) => frame.type === "goal_state");
  assert.deepEqual(published.goal_state, wireState());
  assert.equal(published.agent_id, "worker");
  assert.equal(published.instance_id, supported.client.instanceId);
  assert.equal(published.lease_id, "lease-1");
  const changed = { ...state, enabled: false, goal: "", outcomeCounts: { ...state.outcomeCounts, COMPLETED: 9 } };
  assert.equal(supported.client.sendGoalState(changed), true);
  assert.deepEqual(supported.sent.filter((frame) => frame.type === "goal_state").at(-1).goal_state, wireState(changed));
  supported.socket.close();
  supported.timers.at(-1).callback();
  const replacement = supported.sockets[1];
  replacement.readyState = 1;
  replacement.dispatchEvent(new Event("open"));
  const replacementRegister = supported.sent.filter((frame) => frame.type === "register").at(-1);
  replacement.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: supported.client.instanceId, lease_id: "lease-2", request_id: replacementRegister.frame_id,
  }) }));
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(supported.sent.filter((frame) => frame.type === "goal_state").at(-1).goal_state, wireState(changed));
  assert.equal(supported.sent.some((frame) => frame.type === "model_catalog"), false);
  supported.client.close();
});

test("incoming goal control maps the request and returns correlated full state", async () => {
  const controls = [];
  const fixture = await registeredClient({
    onGoalControl: async (control) => {
      controls.push(control);
      return { accepted: true, goalState: state };
    },
  });
  fixture.socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "goal_control",
    agent_id: "worker",
    instance_id: fixture.client.instanceId,
    lease_id: "lease-1",
    source_agent_id: "controller",
    control_id: "control-1",
    revision: 7,
    action: "set",
    goal: state.goal,
    settings: wireSettings(),
    expires_at: "2030-01-01T00:00:30.000Z",
  }) }));
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(controls, [{
    controlId: "control-1",
    sourceAgentId: "controller",
    targetAgentId: "worker",
    targetInstanceId: fixture.client.instanceId,
    revision: 7,
    action: "set",
    goal: state.goal,
    settings: {
      goalLockEnabled: true,
      snoozeEnabled: true,
      snoozeDurationMs: 60_000,
      snoozeTurnLimit: 4,
      snoozeBudgetCapMs: 300_000,
      snoozeRefillYieldMs: 30_000,
      snoozeRefillElapsedMs: 60_000,
    },
  }]);
  const result = fixture.sent.find((frame) => frame.type === "goal_control_result");
  assert.deepEqual(result, {
    version: "v1",
    frame_id: "id-5",
    type: "goal_control_result",
    agent_id: "worker",
    instance_id: fixture.client.instanceId,
    lease_id: "lease-1",
    source_agent_id: "controller",
    control_id: "control-1",
    revision: 7,
    accepted: true,
    goal_state: wireState(),
  });

  fixture.client.close();
});

test("goal controls serialize, drop expired queued work, and reject callback failures safely", async () => {
  let now = 0;
  let finishFirst;
  const started = [];
  const traces = [];
  const fixture = await registeredClient({
    getGoalState: () => state,
    onGoalControlTrace: (trace) => traces.push(trace),
    now: () => now,
    onGoalControl: ({ controlId }) => {
      started.push(controlId);
      if (controlId === "control-1") return new Promise((resolve) => { finishFirst = resolve; });
      throw new Error("goal authority failed");
    },
  });
  const control = (controlId, expiresAt) => fixture.socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "goal_control", agent_id: "worker", instance_id: fixture.client.instanceId, lease_id: "lease-1",
    source_agent_id: "controller", control_id: controlId, revision: 1, action: "set", goal: state.goal,
    settings: wireSettings(), expires_at: new Date(expiresAt).toISOString(),
  }) }));
  control("control-1", 100);
  control("control-2", 10);
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(started, ["control-1"]);
  now = 20;
  finishFirst({ accepted: true, goalState: state });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(started, ["control-1"]);
  assert.equal(fixture.sent.filter((frame) => frame.type === "goal_control_result").length, 1);
  assert.ok(traces.some(({ event, reason }) => event === "client_goal_control_dropped" && reason === "expired"));

  fixture.socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "goal_control", agent_id: "worker", instance_id: fixture.client.instanceId, lease_id: "lease-1",
    source_agent_id: "controller", control_id: "control-3", revision: 2, action: "off", goal: "",
    settings: wireSettings(), expires_at: new Date(100).toISOString(),
  }) }));
  await new Promise((resolve) => setImmediate(resolve));
  const rejection = fixture.sent.filter((frame) => frame.type === "goal_control_result").at(-1);
  assert.equal(rejection.accepted, false);
  assert.equal(rejection.error_code, "goal_control_failed");
  assert.deepEqual(rejection.goal_state, wireState());
  fixture.client.close();
});
