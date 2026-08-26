import assert from "node:assert/strict";
import test from "node:test";

import { createReminderLockState, createRouterReminderReader } from "../pi-extensions/reminder-lock.js";
import { guardedReminderMutation } from "../pi-extensions/operational-tools.js";
import { createMailboxClient } from "../pi-extensions/mailbox-client.js";

const row = (overrides = {}) => ({
  id: "reminder-1",
  target: "worker",
  creator: "supervisor",
  created_at: "2030-01-01T00:00:00.000Z",
  ...overrides,
});

function socketFixture(sent) {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.send = (payload) => sent.push(JSON.parse(payload));
  socket.close = () => { socket.readyState = 3; socket.dispatchEvent(new Event("close")); };
  return socket;
}

async function clientFixture(onReminderLockControl, onReminderLockReset = () => {}) {
  const sent = [];
  const sockets = [];
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    onReminderLockControl,
    onReminderLockReset,
    webSocketFactory: () => {
      const socket = socketFixture(sent);
      sockets.push(socket);
      return socket;
    },
    fetchImpl: async () => ({ ok: true, json: async () => ({}) }),
    generateId: (() => { let value = 0; return () => `id-${++value}`; })(),
    setIntervalImpl: () => undefined,
    clearIntervalImpl: () => {},
    setTimeoutImpl: (callback) => { setImmediate(callback); return 1; },
    clearTimeoutImpl: () => {},
    reconnectInitialDelayMs: 1,
    reconnectMaxDelayMs: 1,
    random: () => 0,
  });
  const connected = client.connect();
  const socket = sockets[0];
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  const registration = sent.find((frame) => frame.type === "register");
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1", request_id: registration.frame_id,
  }) }));
  await connected;
  return { client, socket, sent };
}

function control(state, action = "set", extra = {}) {
  return state.control({
    targetAgentId: "worker",
    targetInstanceId: "instance-1",
    leaseId: "lease-1",
    reminderId: "reminder-1",
    action,
    expiresAt: "2030-01-01T00:01:00.000Z",
    isCurrent: () => true,
    ...extra,
  });
}

test("router reminder reader preserves row keys and tracks incarnation continuity", async () => {
  const unchanged = row();
  const recreated = row();
  const responses = [
    { reminder: unchanged, token: "incarnation-a" },
    { reminder: unchanged, token: "incarnation-a" },
    { reminder: unchanged, token: "incarnation-a" },
    { reminder: recreated, token: "incarnation-b" },
  ];
  const fetchImpl = async (url) => {
    const response = responses.shift();
    assert.equal(url, "http://router.test/v1/reminders/reminder-1");
    return {
      ok: true,
      json: () => ({ reminder: response.reminder }),
      headers: { get: () => response.token },
    };
  };
  const readReminder = createRouterReminderReader({ fetchImpl, routerUrl: "http://router.test/" });

  const first = await readReminder("reminder-1");
  assert.deepEqual(Object.keys(first), Object.keys(unchanged));
  assert.deepEqual({ ...first }, unchanged);
  const metadata = Object.getOwnPropertySymbols(first);
  assert.equal(metadata.length, 1);
  assert.equal(Object.prototype.propertyIsEnumerable.call(first, metadata[0]), false);

  const state = createReminderLockState({
    getCurrentReminder: readReminder,
    getCurrentTarget: () => ({ agentId: "worker", instanceId: "instance-1", leaseId: "lease-1" }),
  });
  await control(state);
  assert.deepEqual(await state.guard("reminder-1"), { locked: true, errorCode: "reminder_locked" });
  assert.deepEqual(await state.guard("reminder-1"), { locked: false });
});

test("set and clear are idempotent and bind the current row identity", async () => {
  let current = row();
  const state = createReminderLockState({
    getCurrentReminder: async () => current,
    getCurrentTarget: () => ({ agentId: "worker", instanceId: "instance-1", leaseId: "lease-1" }),
    now: () => Date.parse("2030-01-01T00:00:00.000Z"),
  });
  assert.deepEqual(await control(state), { accepted: true });
  assert.deepEqual(await control(state), { accepted: true });
  assert.equal(state.size(), 1);
  assert.deepEqual(await control(state, "clear"), { accepted: true });
  assert.deepEqual(await control(state, "clear"), { accepted: true });
  assert.equal(state.size(), 0);
});

test("native mutation guard blocks a locked update before PATCH and releases it after clear", async () => {
  let current = row();
  const state = createReminderLockState({
    getCurrentReminder: async () => current,
    getCurrentTarget: () => ({ agentId: "worker", instanceId: "instance-1", leaseId: "lease-1" }),
  });
  await control(state);
  const patchCalls = [];
  const locked = await guardedReminderMutation({
    reminderId: "reminder-1", readReminder: async () => current, reminderLock: state,
    mutate: async () => { patchCalls.push({ method: "PATCH" }); },
  });
  assert.equal(locked.details.error_code, "reminder_locked");
  assert.deepEqual(patchCalls, []);
  await control(state, "clear");
  await guardedReminderMutation({
    reminderId: "reminder-1", readReminder: async () => current, reminderLock: state,
    mutate: async () => { patchCalls.push({ method: "PATCH" }); },
  });
  assert.deepEqual(patchCalls, [{ method: "PATCH" }]);
});

test("target changes and same-ID recreation fail open and discard the old lock", async () => {
  let current = row();
  let target = { agentId: "worker", instanceId: "instance-1", leaseId: "lease-1" };
  const state = createReminderLockState({ getCurrentReminder: async () => current, getCurrentTarget: () => target });
  await control(state);
  target = { agentId: "other-worker", instanceId: "instance-2", leaseId: "lease-2" };
  assert.deepEqual(await state.guard("reminder-1", current), { locked: false });
  target = { agentId: "worker", instanceId: "instance-1", leaseId: "lease-1" };
  await control(state);
  current = row({ created_at: "2030-01-01T00:02:00.000Z" });
  assert.deepEqual(await state.guard("reminder-1", current), { locked: false });
  assert.equal(state.size(), 0);
});

test("native mutation fails open for same-ID recreation with identical row identity", async () => {
  let current = row();
  const state = createReminderLockState({
    getCurrentReminder: async () => current,
    getCurrentTarget: () => ({ agentId: "worker", instanceId: "instance-1", leaseId: "lease-1" }),
  });
  await control(state);
  const recreated = row();
  assert.notEqual(recreated, current);
  current = recreated;
  const mutations = [];
  const result = await guardedReminderMutation({
    reminderId: "reminder-1",
    readReminder: async () => current,
    reminderLock: state,
    mutate: async () => {
      mutations.push("mutated");
      return { ok: true };
    },
  });
  assert.deepEqual(result, { ok: true });
  assert.deepEqual(mutations, ["mutated"]);
  assert.equal(state.size(), 0);
});

test("expiry and a stale lease cannot mutate the lock map", async () => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  let current = row();
  let target = { agentId: "worker", instanceId: "instance-1", leaseId: "lease-1" };
  const state = createReminderLockState({ getCurrentReminder: async () => current, getCurrentTarget: () => target, now: () => now });
  assert.deepEqual(await control(state, "set", { expiresAt: "2029-12-31T23:59:59.000Z" }), { accepted: false, errorCode: "expired" });
  let currentCheck = true;
  assert.deepEqual(await control(state, "set", { isCurrent: () => currentCheck }), { accepted: true });
  currentCheck = false;
  now += 1_000;
  assert.deepEqual(await control(state, "clear", { isCurrent: () => currentCheck }), { accepted: false, errorCode: "stale_target" });
});

test("restart/reconnect reset clears the target map and result is correlated", async () => {
  const controls = [];
  const state = createReminderLockState({
    getCurrentReminder: async () => row(),
    getCurrentTarget: () => ({ agentId: "worker", instanceId: "instance-1", leaseId: "lease-1" }),
  });
  await control(state);
  assert.equal(state.size(), 1);
  const fixture = await clientFixture(
    async (controlRequest) => { controls.push(controlRequest); return { accepted: true }; },
    () => state.clear(),
  );
  fixture.client.close();
  assert.equal(typeof fixture.client.currentLeaseId, "function");
  assert.equal(state.size(), 0);
  fixture.socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "reminder_lock_control", agent_id: "worker", instance_id: fixture.client.instanceId,
    lease_id: "lease-1", source_agent_id: "web", control_id: "control-1", reminder_id: "reminder-1",
    action: "set", expires_at: "2030-01-01T00:01:00.000Z",
  }) }));
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(controls.length, 0);
});

test("mailbox forwards a live control and returns the exact target lease result", async () => {
  const controls = [];
  const fixture = await clientFixture(async (control) => { controls.push(control); return { accepted: true }; });
  fixture.socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "reminder_lock_control", agent_id: "worker", instance_id: fixture.client.instanceId,
    lease_id: "lease-1", source_agent_id: "web", control_id: "control-1", reminder_id: "reminder-1",
    action: "set", expires_at: "2030-01-01T00:01:00.000Z",
  }) }));
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(controls[0].targetAgentId, "worker");
  assert.equal(controls[0].leaseId, "lease-1");
  const result = fixture.sent.find((frame) => frame.type === "reminder_lock_control_result");
  assert.equal(result.version, "v1");
  assert.match(result.frame_id, /^id-\d+$/);
  assert.deepEqual({ ...result, frame_id: undefined }, {
    version: "v1", frame_id: undefined, type: "reminder_lock_control_result", agent_id: "worker",
    instance_id: fixture.client.instanceId, lease_id: "lease-1", source_agent_id: "web",
    control_id: "control-1", reminder_id: "reminder-1", accepted: true,
  });
  fixture.client.close();
});

test("native create/list/unrelated paths remain unchanged when no lock guard is configured", async () => {
  const requests = [];
  const tools = (await import("../pi-extensions/operational-tools.js")).createOperationalToolDefinitions({
    fetchImpl: async (url, request = {}) => {
      requests.push({ url, request });
      return { ok: true, status: 200, json: async () => ({ reminders: [], healthy: true }) };
    },
    environment: { MECHA_ROUTER_URL: "http://127.0.0.1:8765", MECHA_AGENT_ID: "worker" },
    now: () => new Date("2030-01-01T00:00:00.000Z"),
    generateId: () => "reminder-2",
  });
  const byName = new Map(tools.map((tool) => [tool.name, tool]));
  const run = (name, input) => byName.get(name).execute("call", input);
  await run("mecha_reminder", { action: "list" });
  await run("mecha_reminder", { action: "create", target: "worker", message: "hello", delayMinutes: 1 });
  await run("mecha_router", { action: "health" });
  assert.deepEqual(requests.map(({ url, request }) => [url, request.method ?? "GET"]), [
    ["http://127.0.0.1:8765/v1/reminders", "GET"],
    ["http://127.0.0.1:8765/v1/reminders", "POST"],
    ["http://127.0.0.1:8765/v1/health", "GET"],
  ]);
});
