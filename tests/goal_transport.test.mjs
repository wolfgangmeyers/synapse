import assert from "node:assert/strict";
import test from "node:test";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { validateClientFrame, validateServerFrame } from "../harness/protocol.js";
import goalExtension from "../pi-extensions/goal.js";
import { createMechaMessagingExtension } from "../pi-extensions/mecha-messaging.js";
import { createMailboxClient } from "../pi-extensions/mailbox-client.js";
import { startRouterWebUi } from "../tools/router_web_ui.mjs";

function nextFrame(socket, predicate = () => true, timeoutMs = 2_000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { socket.off("message", onMessage); reject(new Error("frame timeout")); }, timeoutMs);
    function onMessage(payload) {
      const frame = JSON.parse(payload.toString("utf8"));
      if (!predicate(frame)) return;
      clearTimeout(timer);
      socket.off("message", onMessage);
      resolve(frame);
    }
    socket.on("message", onMessage);
  });
}

async function register(url, agentId, instanceId, conversationSync = "v1", agentRole = undefined, afterRegistered = undefined) {
  const socket = new WebSocket(url);
  await new Promise((resolve, reject) => { socket.once("open", resolve); socket.once("error", reject); });
  const registrationFrame = JSON.stringify({
    version: "v1",
    frame_id: `${agentId}-${instanceId}-register`,
    type: "register",
    agent_id: agentId,
    instance_id: instanceId,
    conversation_sync: conversationSync,
    ...(agentRole === undefined ? {} : { agent_role: agentRole }),
  });
  if (afterRegistered === undefined) {
    socket.send(registrationFrame);
    const registered = await nextFrame(socket, (frame) => frame.type === "registered");
    return { socket, registered, postRegistrationFrame: undefined };
  }
  const { registered, postRegistrationFrame } = await new Promise((resolve, reject) => {
    let registered;
    const timer = setTimeout(() => {
      socket.off("message", onMessage);
      reject(new Error("frame timeout"));
    }, 2_000);
    function onMessage(payload) {
      const frame = JSON.parse(payload.toString("utf8"));
      if (registered === undefined) {
        if (frame.type !== "registered") return;
        registered = frame;
        return;
      }
      if (!afterRegistered(frame)) return;
      clearTimeout(timer);
      socket.off("message", onMessage);
      resolve({ registered, postRegistrationFrame: frame });
    }
    socket.on("message", onMessage);
    socket.send(registrationFrame);
  });
  return { socket, registered, postRegistrationFrame };
}

test("protocol accepts goal projections and rejects malformed goal fields", () => {
  const base = { version: "v1", frame_id: "activity-1", type: "activity", agent_id: "worker", instance_id: "instance-1", lease_id: "lease-1", activity_state: "active", goal: { enabled: true, goal: "ship it" } };
  assert.deepEqual(validateClientFrame(base).goal, base.goal);
  assert.throws(() => validateClientFrame({ ...base, goal: { enabled: false, goal: "stale" } }));
  assert.throws(() => validateClientFrame({ ...base, goal: { enabled: true, goal: "x".repeat(4_001) } }));
  assert.deepEqual(validateServerFrame({ version: "v1", frame_id: "activity-event", type: "agent_activity", agent_id: "worker", instance_id: "instance-1", activity_state: "active", goal: { enabled: false, goal: "" }, generated_at: "2030-01-01T00:00:00.000Z" }).goal, { enabled: false, goal: "" });
});

test("protocol validates typed goal control, confirmation, and full-state frames", () => {
  const goalState = {
    enabled: true,
    goal: "ship the goal editor",
    blocking: true,
    goal_lock_enabled: true,
    snooze_enabled: true,
    snooze_duration_ms: 120_000,
    snooze_turn_limit: 4,
    snooze_budget_cap_ms: 300_000,
    snooze_refill_yield_ms: 30_000,
    snooze_refill_elapsed_ms: 60_000,
    outcome_counts: { BLOCKED: 2, COMPLETED: 3, NOT_DONE: 4, WAITING_FOR_USER_AUTHORIZATION: 1 },
  };
  const settings = {
    blocking: goalState.blocking,
    goal_lock_enabled: goalState.goal_lock_enabled,
    snooze_enabled: goalState.snooze_enabled,
    snooze_duration_ms: goalState.snooze_duration_ms,
    snooze_turn_limit: goalState.snooze_turn_limit,
    snooze_budget_cap_ms: goalState.snooze_budget_cap_ms,
    snooze_refill_yield_ms: goalState.snooze_refill_yield_ms,
    snooze_refill_elapsed_ms: goalState.snooze_refill_elapsed_ms,
  };
  const control = {
    version: "v1", frame_id: "goal-control-1", type: "goal_control",
    agent_id: "goal-web", instance_id: "web-instance", lease_id: "web-lease",
    target_agent_id: "worker", target_instance_id: "worker-instance",
    control_id: "goal-control-1", action: "set", goal: goalState.goal, settings,
  };
  assert.deepEqual(validateClientFrame(control).settings, settings);
  assert.deepEqual(validateClientFrame({ ...control, settings: Object.fromEntries(Object.entries(settings).filter(([key]) => key !== "blocking")) }).settings.blocking, undefined);
  assert.throws(() => validateClientFrame({ ...control, settings: { ...settings, blocking: "yes" } }));
  assert.throws(() => validateClientFrame({ ...control, settings: { ...settings, snooze_duration_ms: 0 } }));
  assert.throws(() => validateClientFrame({ ...control, action: "off", goal: "must be blank" }));

  const forwarded = {
    version: "v1", frame_id: "goal-control-forward-1", type: "goal_control",
    agent_id: "worker", instance_id: "worker-instance", lease_id: "worker-lease",
    source_agent_id: "goal-web", control_id: "goal-control-1", revision: 1,
    action: "set", goal: goalState.goal, settings, expires_at: "2030-01-01T00:00:00.000Z",
  };
  assert.deepEqual(validateServerFrame(forwarded).settings, settings);

  const result = {
    version: "v1", frame_id: "goal-control-result-1", type: "goal_control_result",
    agent_id: "worker", instance_id: "worker-instance", lease_id: "worker-lease",
    source_agent_id: "goal-web", control_id: "goal-control-1", revision: 1,
    accepted: true, goal_state: goalState,
  };
  assert.deepEqual(validateClientFrame(result).goal_state, goalState);
  assert.deepEqual(validateServerFrame(result).goal_state, goalState);
  assert.throws(() => validateClientFrame({ ...result, goal_state: { ...goalState, blocking: 1 } }));
  assert.throws(() => validateServerFrame({ ...result, goal_state: { ...goalState, blocking: 1 } }));

  const state = {
    version: "v1", frame_id: "goal-state-1", type: "goal_state",
    agent_id: "worker", instance_id: "worker-instance", lease_id: "worker-lease", goal_state: goalState,
  };
  assert.deepEqual(validateClientFrame(state).goal_state, goalState);
  assert.deepEqual(validateServerFrame({
    version: "v1", frame_id: "agent-goal-state-1", type: "agent_goal_state",
    agent_id: "worker", instance_id: "worker-instance", goal_state: goalState,
    generated_at: "2030-01-01T00:00:00.000Z",
  }).goal_state, goalState);
});

test("goal desired state is exact-instance, durable, revisioned, and bounded", async () => {
  const directory = await mkdtemp(join(tmpdir(), "goal-control-db-"));
  const databasePath = join(directory, "router.sqlite");
  let now = new Date("2030-01-01T00:00:00.000Z");
  let repository = openMessageRepository({ databasePath, clock: () => now });
  const firstConfig = { sourceAgentId: "goal-web", controlId: "first", action: "set", goal: "ship it", settings: { snooze_enabled: true } };
  const secondConfig = { sourceAgentId: "goal-web", controlId: "second", action: "off", goal: "", settings: { snooze_enabled: false } };
  try {
    assert.ok(repository.migrationIds().includes("019_goal_control_desired_state"));
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "instance-a" }), undefined);
    const first = repository.upsertGoalControlDesired({
      agentId: "worker", instanceId: "instance-a", config: firstConfig,
      expiresAt: "2030-01-01T00:10:00.000Z",
    });
    assert.equal(first.revision, 1);
    assert.deepEqual(first.config, firstConfig);
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "instance-b" }), undefined);

    const second = repository.upsertGoalControlDesired({
      agentId: "worker", instanceId: "instance-a", config: secondConfig,
      expiresAt: "2030-01-01T00:20:00.000Z",
    });
    assert.equal(second.revision, 2);
    assert.deepEqual(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "instance-a" }).config, secondConfig);

    repository.close();
    repository = openMessageRepository({ databasePath, clock: () => now });
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "instance-a" }).revision, 2);
    repository.upsertGoalControlDesired({
      agentId: "worker", instanceId: "instance-b", config: firstConfig,
      expiresAt: "2030-01-01T00:20:00.000Z",
    });
    assert.equal(repository.deleteGoalControlDesiredExcept({ agentId: "worker", instanceId: "instance-b" }), 1);
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "instance-a" }), undefined);
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "instance-b" }).revision, 1);

    repository.upsertGoalControlDesired({
      agentId: "expired-worker", instanceId: "expired-instance", config: firstConfig,
      expiresAt: "2030-01-01T00:00:01.000Z",
    });
    now = new Date("2030-01-01T00:00:02.000Z");
    assert.equal(repository.pruneGoalControlDesired(), 1);
    assert.equal(repository.goalControlDesiredFor({ agentId: "expired-worker", instanceId: "expired-instance" }), undefined);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router forwards exact-instance goal controls and projects confirmed goal state", async () => {
  const directory = await mkdtemp(join(tmpdir(), "goal-control-router-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  try {
    const wsUrl = await router.listen();
    const web = await register(wsUrl, "goal-web", "web-instance", "v1", "user_interaction");
    const worker = await register(wsUrl, "worker", "worker-instance");
    sockets.push(web.socket, worker.socket);
    const settings = {
      blocking: true,
      goal_lock_enabled: false,
      snooze_enabled: true,
      snooze_duration_ms: 60_000,
      snooze_turn_limit: null,
      snooze_budget_cap_ms: null,
      snooze_refill_yield_ms: 30_000,
      snooze_refill_elapsed_ms: 60_000,
    };
    const forward = nextFrame(worker.socket, (frame) => frame.type === "goal_control");
    web.socket.send(JSON.stringify({
      version: "v1", frame_id: "goal-control-request", type: "goal_control",
      agent_id: "goal-web", instance_id: "web-instance", lease_id: web.registered.lease_id,
      target_agent_id: "worker", target_instance_id: "worker-instance",
      control_id: "goal-control-request", action: "set", goal: "ship the goal editor", settings,
    }));
    const control = await forward;
    assert.equal(control.agent_id, "worker");
    assert.equal(control.instance_id, "worker-instance");
    assert.equal(control.lease_id, worker.registered.lease_id);
    assert.equal(control.revision, 1);
    assert.deepEqual(control.settings, settings);

    const newerForward = nextFrame(worker.socket, (frame) => frame.type === "goal_control" && frame.control_id === "goal-control-newer");
    web.socket.send(JSON.stringify({
      version: "v1", frame_id: "goal-control-newer-request", type: "goal_control",
      agent_id: "goal-web", instance_id: "web-instance", lease_id: web.registered.lease_id,
      target_agent_id: "worker", target_instance_id: "worker-instance",
      control_id: "goal-control-newer", action: "set", goal: "ship the newer goal", settings,
    }));
    const newerControl = await newerForward;
    assert.equal(newerControl.revision, 2);

    const staleError = nextFrame(worker.socket, (frame) => frame.type === "error");
    worker.socket.send(JSON.stringify({
      version: "v1", frame_id: "stale-goal-control-result", type: "goal_control_result",
      agent_id: "worker", instance_id: "worker-instance", lease_id: worker.registered.lease_id,
      source_agent_id: "goal-web", control_id: "goal-control-request", revision: control.revision,
      accepted: true, goal_state: {
        enabled: true,
        goal: "stale goal",
        ...settings,
        outcome_counts: { BLOCKED: 0, COMPLETED: 0, NOT_DONE: 0, WAITING_FOR_USER_AUTHORIZATION: 0 },
      },
    }));
    assert.equal((await staleError).code, "goal_control_result_not_pending");
    const staleSnapshot = await fetch(wsUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/agents")).then((response) => response.json());
    assert.equal(staleSnapshot.agents.find((agent) => agent.agent_id === "worker").goal_state, undefined);

    const goalState = {
      enabled: true,
      goal: "ship the newer goal",
      ...settings,
      outcome_counts: { BLOCKED: 0, COMPLETED: 0, NOT_DONE: 0, WAITING_FOR_USER_AUTHORIZATION: 0 },
    };
    const result = nextFrame(web.socket, (frame) => frame.type === "goal_control_result");
    worker.socket.send(JSON.stringify({
      version: "v1", frame_id: "goal-control-result", type: "goal_control_result",
      agent_id: "worker", instance_id: "worker-instance", lease_id: worker.registered.lease_id,
      source_agent_id: "goal-web", control_id: "goal-control-newer", revision: newerControl.revision,
      accepted: true, goal_state: goalState,
    }));
    const confirmed = await result;
    assert.equal(confirmed.revision, 2);
    assert.deepEqual(confirmed.goal_state, goalState);
    const snapshot = await fetch(wsUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/agents")).then((response) => response.json());
    assert.deepEqual(snapshot.agents.find((agent) => agent.agent_id === "worker").goal_state, goalState);
  } finally {
    for (const socket of sockets) socket.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router replays desired control only to the reconnecting exact instance", async () => {
  const directory = await mkdtemp(join(tmpdir(), "goal-control-reconnect-"));
  const databasePath = join(directory, "router.sqlite");
  let repository;
  let router;
  const sockets = [];
  const settings = {
    goal_lock_enabled: false,
    snooze_enabled: true,
    snooze_duration_ms: 60_000,
    snooze_turn_limit: null,
    snooze_budget_cap_ms: null,
    snooze_refill_yield_ms: 30_000,
    snooze_refill_elapsed_ms: 60_000,
  };
  try {
    repository = openMessageRepository({ databasePath });
    router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    let wsUrl = await router.listen();
    const web = await register(wsUrl, "goal-web", "web-instance", "v1", "user_interaction");
    const firstWorker = await register(wsUrl, "worker", "worker-instance");
    sockets.push(web.socket, firstWorker.socket);
    const forwarded = nextFrame(firstWorker.socket, (frame) => frame.type === "goal_control");
    web.socket.send(JSON.stringify({
      version: "v1", frame_id: "durable-goal-control", type: "goal_control",
      agent_id: "goal-web", instance_id: "web-instance", lease_id: web.registered.lease_id,
      target_agent_id: "worker", target_instance_id: "worker-instance",
      control_id: "durable-goal-control", action: "set", goal: "resume safely", settings,
    }));
    assert.equal((await forwarded).revision, 1);

    await new Promise((resolve) => { firstWorker.socket.once("close", resolve); firstWorker.socket.close(); });
    const sameInstanceBeforeRestart = await register(
      wsUrl,
      "worker",
      "worker-instance",
      "v1",
      undefined,
      (frame) => frame.type === "goal_control",
    );
    sockets.push(sameInstanceBeforeRestart.socket);
    const replayBeforeRestart = sameInstanceBeforeRestart.postRegistrationFrame;
    assert.equal(replayBeforeRestart.control_id, "durable-goal-control");
    assert.equal(replayBeforeRestart.revision, 1);
    assert.equal(replayBeforeRestart.instance_id, "worker-instance");

    await router.close();
    router = undefined;
    repository.close();
    repository = openMessageRepository({ databasePath });
    router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    wsUrl = await router.listen();
    const sameInstance = await register(
      wsUrl,
      "worker",
      "worker-instance",
      "v1",
      undefined,
      (frame) => frame.type === "goal_control",
    );
    sockets.push(sameInstance.socket);
    const replay = sameInstance.postRegistrationFrame;
    assert.equal(replay.control_id, "durable-goal-control");
    assert.equal(replay.revision, 1);
    assert.equal(replay.instance_id, "worker-instance");

    await new Promise((resolve) => { sameInstance.socket.once("close", resolve); sameInstance.socket.close(); });
    const replacement = await register(wsUrl, "worker", "replacement-instance");
    sockets.push(replacement.socket);
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "worker-instance" }), undefined);
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "replacement-instance" }), undefined);
  } finally {
    for (const socket of sockets) socket.close();
    await router?.close();
    repository?.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("replacement registration clears desired state from an active old instance", async () => {
  const directory = await mkdtemp(join(tmpdir(), "goal-control-replacement-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  try {
    const wsUrl = await router.listen();
    const web = await register(wsUrl, "goal-web", "web-instance", "v1", "user_interaction");
    const worker = await register(wsUrl, "worker", "worker-instance");
    sockets.push(web.socket, worker.socket);
    const forwarded = nextFrame(worker.socket, (frame) => frame.type === "goal_control");
    web.socket.send(JSON.stringify({
      version: "v1", frame_id: "active-replacement-control", type: "goal_control",
      agent_id: "goal-web", instance_id: "web-instance", lease_id: web.registered.lease_id,
      target_agent_id: "worker", target_instance_id: "worker-instance",
      control_id: "active-replacement-control", action: "set", goal: "replace safely", settings: {
        goal_lock_enabled: false,
        snooze_enabled: true,
        snooze_duration_ms: 60_000,
        snooze_turn_limit: null,
        snooze_budget_cap_ms: null,
        snooze_refill_yield_ms: 30_000,
        snooze_refill_elapsed_ms: 60_000,
      },
    }));
    await forwarded;

    const replacement = await register(wsUrl, "worker", "replacement-instance");
    sockets.push(replacement.socket);
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "worker-instance" }), undefined);
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker", instanceId: "replacement-instance" }), undefined);
  } finally {
    for (const socket of sockets) socket.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("goal controls require an interaction source and keep pending controls bounded", async () => {
  const directory = await mkdtemp(join(tmpdir(), "goal-control-bounds-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({
    repository,
    timeoutSweepIntervalMs: 60_000,
    maxPendingModelControls: 1,
    maxPendingModelControlsPerSource: 1,
  });
  const sockets = [];
  const settings = {
    blocking: true,
    goal_lock_enabled: false,
    snooze_enabled: true,
    snooze_duration_ms: 60_000,
    snooze_turn_limit: null,
    snooze_budget_cap_ms: null,
    snooze_refill_yield_ms: 30_000,
    snooze_refill_elapsed_ms: 60_000,
  };
  try {
    const wsUrl = await router.listen();
    const nonInteraction = await register(wsUrl, "non-interaction", "non-interaction-instance");
    const source = await register(wsUrl, "goal-web", "web-instance", "v1", "user_interaction");
    const target = await register(wsUrl, "worker", "worker-instance");
    const targetTwo = await register(wsUrl, "worker-two", "worker-two-instance");
    sockets.push(nonInteraction.socket, source.socket, target.socket, targetTwo.socket);

    const roleError = nextFrame(nonInteraction.socket, (frame) => frame.type === "error");
    nonInteraction.socket.send(JSON.stringify({
      version: "v1", frame_id: "role-rejected", type: "goal_control",
      agent_id: "non-interaction", instance_id: "non-interaction-instance", lease_id: nonInteraction.registered.lease_id,
      target_agent_id: "worker", target_instance_id: "worker-instance",
      control_id: "role-rejected", action: "set", goal: "blocked", settings,
    }));
    assert.equal((await roleError).code, "goal_control_not_allowed");

    const targetError = nextFrame(source.socket, (frame) => frame.type === "error");
    source.socket.send(JSON.stringify({
      version: "v1", frame_id: "target-rejected", type: "goal_control",
      agent_id: "goal-web", instance_id: "web-instance", lease_id: source.registered.lease_id,
      target_agent_id: "worker", target_instance_id: "wrong-instance",
      control_id: "target-rejected", action: "set", goal: "blocked", settings,
    }));
    assert.equal((await targetError).code, "target_instance_changed");

    const firstForward = nextFrame(target.socket, (frame) => frame.type === "goal_control");
    source.socket.send(JSON.stringify({
      version: "v1", frame_id: "bounded-one", type: "goal_control",
      agent_id: "goal-web", instance_id: "web-instance", lease_id: source.registered.lease_id,
      target_agent_id: "worker", target_instance_id: "worker-instance",
      control_id: "bounded-one", action: "set", goal: "first", settings,
    }));
    await firstForward;

    const capacityError = nextFrame(source.socket, (frame) => frame.type === "error");
    source.socket.send(JSON.stringify({
      version: "v1", frame_id: "bounded-two", type: "goal_control",
      agent_id: "goal-web", instance_id: "web-instance", lease_id: source.registered.lease_id,
      target_agent_id: "worker-two", target_instance_id: "worker-two-instance",
      control_id: "bounded-two", action: "set", goal: "second", settings,
    }));
    assert.equal((await capacityError).code, "goal_control_capacity_exceeded");
    assert.equal(repository.goalControlDesiredFor({ agentId: "worker-two", instanceId: "worker-two-instance" }), undefined);
  } finally {
    for (const socket of sockets) socket.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router projects goal state through activity, snapshot, off, and replacement", async () => {
  const directory = await mkdtemp(join(tmpdir(), "goal-transport-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  try {
    const wsUrl = await router.listen();
    const reader = await register(wsUrl, "reader", "reader-instance");
    const worker = await register(wsUrl, "worker", "worker-instance");
    sockets.push(reader.socket, worker.socket);
    worker.socket.send(JSON.stringify({ version: "v1", frame_id: "goal-set", type: "activity", agent_id: "worker", instance_id: "worker-instance", lease_id: worker.registered.lease_id, activity_state: "active", goal: { enabled: true, goal: "ship the goal" } }));
    const setEvent = await nextFrame(reader.socket, (frame) => frame.type === "agent_activity" && frame.agent_id === "worker");
    assert.deepEqual(setEvent.goal, { enabled: true, goal: "ship the goal" });
    const snapshot = await fetch(wsUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/agents")).then((response) => response.json());
    assert.deepEqual(snapshot.agents.find((agent) => agent.agent_id === "worker").goal, { enabled: true, goal: "ship the goal" });

    const goalState = {
      enabled: true,
      goal: "ship the goal",
      blocking: true,
      goal_lock_enabled: false,
      snooze_enabled: true,
      snooze_duration_ms: 60_000,
      snooze_turn_limit: null,
      snooze_budget_cap_ms: null,
      snooze_refill_yield_ms: 30_000,
      snooze_refill_elapsed_ms: 60_000,
      outcome_counts: { BLOCKED: 0, COMPLETED: 0, NOT_DONE: 0, WAITING_FOR_USER_AUTHORIZATION: 0 },
    };
    const stateEvent = nextFrame(reader.socket, (frame) => frame.type === "agent_goal_state" && frame.agent_id === "worker");
    worker.socket.send(JSON.stringify({
      version: "v1", frame_id: "goal-state", type: "goal_state", agent_id: "worker",
      instance_id: "worker-instance", lease_id: worker.registered.lease_id, goal_state: goalState,
    }));
    assert.deepEqual((await stateEvent).goal_state, goalState);

    worker.socket.send(JSON.stringify({ version: "v1", frame_id: "goal-off", type: "activity", agent_id: "worker", instance_id: "worker-instance", lease_id: worker.registered.lease_id, activity_state: "active", goal: { enabled: false, goal: "" } }));
    const offEvent = await nextFrame(reader.socket, (frame) => frame.type === "agent_activity" && frame.agent_id === "worker");
    assert.deepEqual(offEvent.goal, { enabled: false, goal: "" });

    worker.socket.close();
    await nextFrame(reader.socket, (frame) => frame.type === "agent_activity" && frame.agent_id === "worker" && frame.activity_state === "idle");
    const replacement = await register(wsUrl, "worker", "worker-replacement");
    sockets.push(replacement.socket);
    const replacementSnapshot = await fetch(wsUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/agents")).then((response) => response.json());
    const replacementAgent = replacementSnapshot.agents.find((agent) => agent.agent_id === "worker");
    assert.equal(replacementAgent.goal, undefined);
    assert.equal(replacementAgent.goal_state, undefined);
    assert.deepEqual(replacementAgent.available_models, []);
    assert.equal(replacementAgent.model_catalog_total_count, 0);
    assert.equal(replacementAgent.model_catalog_truncated, false);
    assert.deepEqual(Object.keys(replacementAgent).sort(), [
      "activity_state", "agent_id", "agent_role", "available_models", "connected_at", "instance_id", "lease_expires_at",
      "model_catalog_total_count", "model_catalog_truncated", "subagent_count", "subagents",
    ]);
  } finally {
    for (const socket of sockets) socket.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("WebUI renders an accessible pink goal control and text-safe dialog", async () => {
  const app = await readFile(new URL("../web/router-ui/app.js", import.meta.url), "utf8");
  const styles = await readFile(new URL("../web/router-ui/styles.css", import.meta.url), "utf8");
  const html = await readFile(new URL("../web/router-ui/index.html", import.meta.url), "utf8");
  assert.match(app, /function goalIndicatorMarkup\(goal, peerName\)/);
  assert.match(app, /class="goal-indicator"/);
  assert.match(app, /event\.key === "Enter" \|\| event\.key === " "/);
  assert.match(app, /function renderGoalDialog\(peer\)/);
  assert.match(app, /const goalText = document\.createElement\("textarea"\);[\s\S]*goalText\.value = draft\.goal;[\s\S]*goalText\.disabled = !draft\.enabled;/);
  assert.match(app, /form\.addEventListener\("input", \(event\) => updateGoalDraftFromInput\(event\.target\)\);/);
  assert.match(app, /if \(input\.id === "goal-text"\) state\.goalDraft\.goal = input\.value;/);
  assert.match(app, /aria-controls="goal-dialog"/);
  assert.match(html, /<dialog id="goal-dialog"/);
  assert.match(styles, /\.goal-indicator[\s\S]*#f472b6/);
  assert.match(styles, /\.reminder-indicator i \{ color: #78e0cd; \}/);
  assert.match(app, /instanceId, goalProvided/);
});

test("EventBus goal state reaches the router and WebUI snapshot/live state", async () => {
  const directory = await mkdtemp(join(tmpdir(), "goal-transport-e2e-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let ui;
  let browser;
  const workerSockets = [];
  try {
    const routerWsUrl = await router.listen();
    const routerHttpUrl = routerWsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    ui = await startRouterWebUi({ router: routerHttpUrl, output: () => {} });
    browser = new WebSocket(`${ui.httpUrl.replace("http://", "ws://")}/ui/events`, {
      origin: ui.httpUrl,
    });
    await new Promise((resolve, reject) => { browser.once("open", resolve); browser.once("error", reject); });
    browser.send(JSON.stringify({ type: "connect", routerUrl: routerHttpUrl, agentId: "goal-web" }));
    await nextFrame(browser, (event) => event.type === "status" && event.state === "connected");

    const handlers = new Map();
    const commands = new Map();
    const events = new Map();
    const entries = [];
    const pi = {
      getSessionId: () => "worker-session",
      on(name, handler) { handlers.set(name, [...(handlers.get(name) ?? []), handler]); },
      events: {
        on(name, handler) { events.set(name, [...(events.get(name) ?? []), handler]); },
        emit(name, data) { for (const handler of events.get(name) ?? []) handler(data); },
      },
      appendEntry: (customType, data) => entries.push({ type: "custom", customType, data }),
      registerCommand(name, command) { commands.set(name, command); }, registerTool() {}, sendMessage() {},
      exec: async () => ({ code: 0, stderr: "" }),
    };
    const context = {
      sessionManager: { getSessionId: () => "worker-session", getBranch: () => entries, getHeader: () => ({}) },
      ui: { notify() {}, setStatus() {} },
    };
    goalExtension(pi);
    let connected;
    const clientReady = new Promise((resolve) => { connected = resolve; });
    createMechaMessagingExtension(pi, {
      resolveCurrentAgentId: () => "goal-worker",
      createClient: (options) => createMailboxClient({
        ...options,
        routerUrl: routerHttpUrl,
        onConnectionState: (state) => {
          options.onConnectionState(state);
          if (state.state === "connected") connected();
        },
      }),
      createSettledScheduler: () => ({ onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {}, dispose() {} }),
    });
    const trigger = async (name, event = {}) => {
      for (const handler of handlers.get(name) ?? []) await handler(event, context);
    };
    await trigger("session_start");
    await clientReady;
    await commands.get("goal").handler("set ship the goal", context);
    const activity = nextFrame(browser, (event) => event.type === "agent_activity" && event.agent === "goal-worker");
    await trigger("agent_start");
    browser.send(JSON.stringify({ type: "refresh_agents" }));
    const snapshot = await nextFrame(browser, (event) => event.type === "agents" && event.agents.some((agent) => agent.agent_id === "goal-worker"));
    assert.equal(snapshot.agents.find((agent) => agent.agent_id === "goal-worker").agent_id, "goal-worker");
    assert.deepEqual((await activity).goal, { enabled: true, goal: "ship the goal" });
    await trigger("session_shutdown");
  } finally {
    browser?.close();
    for (const socket of workerSockets) socket.close();
    await ui?.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("typed WebUI goal control reaches goal.js and returns correlated live and snapshot state", async () => {
  const directory = await mkdtemp(join(tmpdir(), "goal-control-webui-e2e-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let ui;
  let browser;
  let workerClient;
  let sessionStarted = false;
  const entries = [];
  let resolveWorkerConnected;
  const workerConnected = new Promise((resolve) => { resolveWorkerConnected = resolve; });
  const settings = {
    blocking: true,
    goal_lock_enabled: false,
    snooze_enabled: true,
    snooze_duration_ms: 60_000,
    snooze_turn_limit: 3,
    snooze_budget_cap_ms: null,
    snooze_refill_yield_ms: 30_000,
    snooze_refill_elapsed_ms: 60_000,
  };
  const goal = "ship the typed goal control";
  const expectedGoalState = {
    enabled: true,
    goal,
    blocking: true,
    goal_lock_enabled: false,
    snooze_enabled: true,
    snooze_duration_ms: 60_000,
    snooze_turn_limit: 3,
    snooze_budget_cap_ms: null,
    snooze_refill_yield_ms: 30_000,
    snooze_refill_elapsed_ms: 60_000,
    outcome_counts: { BLOCKED: 0, COMPLETED: 0, NOT_DONE: 0, WAITING_FOR_USER_AUTHORIZATION: 0 },
  };
  const handlers = new Map();
  const events = new Map();
  const pi = {
    getSessionId: () => "goal-worker-session",
    on(name, handler) { handlers.set(name, [...(handlers.get(name) ?? []), handler]); },
    events: {
      on(name, handler) { events.set(name, [...(events.get(name) ?? []), handler]); },
      emit(name, data) { for (const handler of events.get(name) ?? []) handler(data); },
    },
    appendEntry: (customType, data) => entries.push({ type: "custom", customType, data }),
    registerCommand() {},
    registerTool() {},
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
  };
  const context = {
    sessionManager: { getSessionId: () => "goal-worker-session", getBranch: () => entries, getHeader: () => ({}) },
    ui: { notify() {}, setStatus() {} },
  };
  const trigger = async (name, event = {}) => {
    for (const handler of handlers.get(name) ?? []) await handler(event, context);
  };
  try {
    const routerWsUrl = await router.listen();
    const routerHttpUrl = routerWsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    ui = await startRouterWebUi({ router: routerHttpUrl, output: () => {} });
    browser = new WebSocket(`${ui.httpUrl.replace("http://", "ws://")}/ui/events`, { origin: ui.httpUrl });
    await new Promise((resolve, reject) => { browser.once("open", resolve); browser.once("error", reject); });
    browser.send(JSON.stringify({ type: "connect", routerUrl: routerHttpUrl, agentId: "goal-web" }));
    await nextFrame(browser, (event) => event.type === "status" && event.state === "connected");

    goalExtension(pi);
    createMechaMessagingExtension(pi, {
      resolveCurrentAgentId: () => "goal-worker",
      createClient: (options) => {
        workerClient = createMailboxClient({
          ...options,
          routerUrl: routerHttpUrl,
          onConnectionState: (state) => {
            options.onConnectionState(state);
            if (state.state === "connected") resolveWorkerConnected();
          },
        });
        return workerClient;
      },
      createSettledScheduler: () => ({ onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {}, dispose() {} }),
    });
    await trigger("session_start", { sessionId: "goal-worker-session" });
    sessionStarted = true;
    await workerConnected;
    const targetInstanceId = workerClient.instanceId;
    const agents = await fetch(`${routerHttpUrl}/v1/agents`).then((response) => response.json());
    assert.equal(agents.agents.find((agent) => agent.agent_id === "goal-web").agent_role, "user_interaction");

    browser.send(JSON.stringify({
      type: "goal_control",
      controlId: "wrong-target-instance",
      targetAgentId: "goal-worker",
      targetInstanceId: "wrong-instance",
      action: "set",
      goal,
      settings,
    }));
    const fenced = await nextFrame(browser, (event) => event.type === "goal_control_result" && event.controlId === "wrong-target-instance");
    assert.equal(fenced.accepted, false);
    assert.equal(fenced.errorCode, "target_instance_changed");
    assert.equal(fenced.targetAgentId, "goal-worker");
    assert.equal(fenced.targetInstanceId, "wrong-instance");
    assert.equal(entries.some((entry) => entry.customType === "goal-state" && entry.data.goal === goal), false);

    const liveState = nextFrame(browser, (event) => event.type === "agent_goal_state"
      && event.agent === "goal-worker" && event.instanceId === targetInstanceId
      && event.goalState?.goal === goal);
    const controlResult = nextFrame(browser, (event) => event.type === "goal_control_result" && event.controlId === "typed-goal-control");
    browser.send(JSON.stringify({
      type: "goal_control",
      controlId: "typed-goal-control",
      targetAgentId: "goal-worker",
      targetInstanceId,
      action: "set",
      goal,
      settings,
    }));
    const [live, result] = await Promise.all([liveState, controlResult]);
    assert.equal(result.accepted, true);
    assert.equal(result.targetAgentId, "goal-worker");
    assert.equal(result.targetInstanceId, targetInstanceId);
    assert.equal(result.revision, 1);
    assert.deepEqual(result.goalState, expectedGoalState);
    assert.deepEqual(live.goalState, expectedGoalState);

    browser.send(JSON.stringify({ type: "refresh_agents" }));
    const snapshot = await nextFrame(browser, (event) => event.type === "agents"
      && event.agents.some((agent) => agent.agent_id === "goal-worker" && agent.goal_state?.goal === goal));
    const snapshotAgent = snapshot.agents.find((agent) => agent.agent_id === "goal-worker");
    assert.equal(snapshotAgent.instance_id, targetInstanceId);
    assert.deepEqual(snapshotAgent.goal_state, expectedGoalState);

    const persisted = entries.filter((entry) => entry.customType === "goal-state").at(-1);
    assert.equal(persisted.data.goal, goal);
    assert.deepEqual(persisted.data.appliedGoalControl, { instanceId: targetInstanceId, revision: 1 });
    assert.deepEqual(persisted.data.outcomeCounts, expectedGoalState.outcome_counts);
  } finally {
    if (sessionStarted) await trigger("session_shutdown", { sessionId: "goal-worker-session" });
    browser?.close();
    await ui?.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("messaging carries the cached enabled and explicit-off goal projection", async () => {
  const eventHandlers = new Map();
  const sentActivities = [];
  const goalHandlers = new Map();
  const bus = {
    on(name, handler) { eventHandlers.set(name, handler); goalHandlers.set(name, handler); },
    emit(name, data) {
      if (name === "goal:state") eventHandlers.get(name)?.(data);
      else goalHandlers.get(name)?.(data);
    },
  };
  const handlers = new Map();
  const client = { connect: async () => {}, close() {}, sendReceipt() {}, sendActivity: (state, options) => sentActivities.push({ state, options }) };
  const pi = {
    events: bus,
    on(name, handler) { handlers.set(name, handler); },
    registerCommand() {}, registerTool() {}, sendMessage() {}, exec: async () => ({ code: 0, stderr: "" }),
    getSessionId: () => "worker-session",
    appendEntry() {},
  };
  const context = { sessionManager: { getSessionId: () => "worker-session", getBranch: () => [], getHeader: () => ({}) }, ui: { notify() {}, setStatus() {} } };
  goalExtension(pi);
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: () => client,
    createSettledScheduler: () => ({ onParentActivity() {}, onParentSettled() {}, onChildStarted() {}, onChildDisposed() {}, dispose() {} }),
  });
  await handlers.get("session_start")({}, context);
  eventHandlers.get("goal:state")({ enabled: true, goal: "ship it" });
  await handlers.get("agent_start")({}, context);
  assert.deepEqual(sentActivities.at(-1).options.goal, { enabled: true, goal: "ship it" });
  eventHandlers.get("goal:state")({ enabled: false, goal: "" });
  await handlers.get("agent_settled")({}, context);
  assert.deepEqual(sentActivities.at(-1).options.goal, { enabled: false, goal: "" });
});
