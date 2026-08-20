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

async function register(url, agentId, instanceId, conversationSync = "v1") {
  const socket = new WebSocket(url);
  await new Promise((resolve, reject) => { socket.once("open", resolve); socket.once("error", reject); });
  socket.send(JSON.stringify({ version: "v1", frame_id: `${agentId}-${instanceId}-register`, type: "register", agent_id: agentId, instance_id: instanceId, conversation_sync: conversationSync }));
  const registered = await nextFrame(socket, (frame) => frame.type === "registered");
  return { socket, registered };
}

test("protocol accepts goal projections and rejects malformed goal fields", () => {
  const base = { version: "v1", frame_id: "activity-1", type: "activity", agent_id: "worker", instance_id: "instance-1", lease_id: "lease-1", activity_state: "active", goal: { enabled: true, goal: "ship it" } };
  assert.deepEqual(validateClientFrame(base).goal, base.goal);
  assert.throws(() => validateClientFrame({ ...base, goal: { enabled: false, goal: "stale" } }));
  assert.throws(() => validateClientFrame({ ...base, goal: { enabled: true, goal: "x".repeat(4_001) } }));
  assert.deepEqual(validateServerFrame({ version: "v1", frame_id: "activity-event", type: "agent_activity", agent_id: "worker", instance_id: "instance-1", activity_state: "active", goal: { enabled: false, goal: "" }, generated_at: "2030-01-01T00:00:00.000Z" }).goal, { enabled: false, goal: "" });
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
  assert.match(app, /text\.textContent = goal\.goal/);
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
    browser = new WebSocket(`${ui.httpUrl.replace("http://", "ws://")}/ui/events`);
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
