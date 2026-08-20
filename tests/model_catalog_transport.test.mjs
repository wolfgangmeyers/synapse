import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { validateClientFrame, validateServerFrame } from "../harness/protocol.js";
import { createMailboxClient } from "../pi-extensions/mailbox-client.js";

function nextFrame(socket, predicate = () => true, timeoutMs = 1_000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => cleanup(reject, new Error("timed out waiting for frame")), timeoutMs);
    const onMessage = (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (predicate(frame)) cleanup(resolve, frame);
    };
    const onError = (error) => cleanup(reject, error);
    const cleanup = (done, value) => {
      clearTimeout(timer);
      socket.off("message", onMessage);
      socket.off("error", onError);
      done(value);
    };
    socket.on("message", onMessage);
    socket.once("error", onError);
  });
}

async function connect(wsUrl, { agentId, instanceId, sync = false } = {}) {
  const socket = new WebSocket(wsUrl);
  await new Promise((resolve, reject) => {
    socket.once("open", resolve);
    socket.once("error", reject);
  });
  const registered = nextFrame(socket, (frame) => frame.type === "registered");
  socket.send(JSON.stringify({
    version: "v1",
    frame_id: `register-${instanceId}`,
    type: "register",
    agent_id: agentId,
    instance_id: instanceId,
    ...(sync ? { conversation_sync: "v1" } : {}),
  }));
  return { socket, registered: await registered };
}

function closeSocket(socket) {
  if (socket.readyState === WebSocket.CLOSED) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      socket.off("close", onClose);
      reject(new Error("timed out waiting for socket close"));
    }, 1_000);
    const onClose = () => {
      clearTimeout(timer);
      resolve();
    };
    socket.once("close", onClose);
    socket.close();
  });
}

async function routerFixture() {
  const directory = await mkdtemp(join(tmpdir(), "model-catalog-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({
    repository,
    timeoutSweepIntervalMs: 60_000,
    imageAttachmentDirectory: join(directory, "attachments"),
  });
  const wsUrl = await router.listen();
  return {
    directory,
    repository,
    router,
    wsUrl,
    httpUrl: wsUrl.replace("ws://", "http://").replace("/v1/agents", ""),
    async close() {
      await router.close();
      repository.close();
      await rm(directory, { recursive: true, force: true });
    },
  };
}

function modelCatalog(size, prefix = "provider") {
  return Array.from({ length: size }, (_, index) => ({
    provider: prefix,
    id: `model-${index}`,
    name: `Model ${index}`,
  }));
}

class FakeSocket {
  constructor(sent) {
    this.readyState = 0;
    this.sent = sent;
    this.listeners = new Map();
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  send(payload) {
    this.sent.push(JSON.parse(payload));
  }

  close() {
    this.readyState = 3;
    this.emit("close", {});
  }

  open() {
    this.readyState = 1;
    this.emit("open", {});
  }

  message(frame) {
    this.emit("message", { data: JSON.stringify(frame) });
  }

  emit(type, event) {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

test("model catalog frames validate and activity frames stay lightweight", () => {
  const catalog = {
    version: "v1",
    frame_id: "catalog-frame",
    type: "model_catalog",
    agent_id: "worker",
    instance_id: "instance",
    lease_id: "lease",
    model_summaries: [{ provider: "provider", id: "model", name: "Model" }],
  };
  assert.equal(validateClientFrame(catalog), catalog);
  assert.doesNotThrow(() => validateServerFrame({
    version: "v1",
    frame_id: "catalog-event",
    type: "agent_model_catalog",
    agent_id: "worker",
    instance_id: "instance",
    model_summaries: catalog.model_summaries,
    generated_at: "2030-01-01T00:00:00.000Z",
  }));
  assert.throws(() => validateClientFrame({
    version: "v1",
    frame_id: "activity-frame",
    type: "activity",
    agent_id: "worker",
    instance_id: "instance",
    lease_id: "lease",
    activity_state: "active",
    available_models: catalog.model_summaries,
  }));
});

test("mailbox client sends the full catalog once per registration socket and omits it from activity", async () => {
  const sent = [];
  const sockets = [];
  const timers = [];
  const intervals = [];
  const catalog = modelCatalog(150);
  let catalogCalls = 0;
  let id = 0;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    getModelCatalog: () => {
      catalogCalls += 1;
      return catalog;
    },
    webSocketFactory: () => {
      const socket = new FakeSocket(sent);
      sockets.push(socket);
      return socket;
    },
    generateId: () => `id-${++id}`,
    reconnectInitialDelayMs: 1,
    reconnectMaxDelayMs: 1,
    setTimeoutImpl: (callback) => {
      timers.push(callback);
      return timers.length;
    },
    clearTimeoutImpl: () => {},
    setIntervalImpl: (callback, delay) => {
      intervals.push({ callback, delay });
      return intervals.length;
    },
    clearIntervalImpl: () => {},
    random: () => 0,
  });

  const connected = client.connect();
  sockets[0].open();
  const registered = sent.find((frame) => frame.type === "register");
  sockets[0].message({
    version: "v1",
    frame_id: "registered-1",
    type: "registered",
    request_id: registered.frame_id,
    agent_id: "worker",
    instance_id: client.instanceId,
    lease_id: "lease-1",
    expires_at: "2030-01-01T00:00:00.000Z",
    recovered: false,
    agent_role: "agent",
  });
  await connected;

  assert.equal(catalogCalls, 1);
  assert.equal(sent.filter((frame) => frame.type === "model_catalog").length, 1);
  assert.equal(sent.find((frame) => frame.type === "model_catalog").model_summaries.length, 150);
  assert.deepEqual(intervals.map(({ delay }) => delay), [10_000, 10_000]);
  intervals[0].callback();
  assert.equal(catalogCalls, 2);
  assert.equal(sent.filter((frame) => frame.type === "model_catalog").length, 2);
  client.sendActivity("active", { currentModel: catalog[0], availableModels: catalog });
  assert.equal("available_models" in sent.at(-1), false);

  sockets[0].message({
    version: "v1",
    frame_id: "registered-renew",
    type: "registered",
    request_id: "renew-request",
    agent_id: "worker",
    instance_id: client.instanceId,
    lease_id: "lease-1",
    expires_at: "2030-01-01T00:01:00.000Z",
    recovered: false,
    agent_role: "agent",
  });
  assert.equal(catalogCalls, 2);

  sockets[0].close();
  timers.shift()();
  sockets[1].open();
  const reconnectRegister = sent.filter((frame) => frame.type === "register").at(-1);
  sockets[1].message({
    version: "v1",
    frame_id: "registered-2",
    type: "registered",
    request_id: reconnectRegister.frame_id,
    agent_id: "worker",
    instance_id: client.instanceId,
    lease_id: "lease-2",
    expires_at: "2030-01-01T00:02:00.000Z",
    recovered: true,
    agent_role: "agent",
  });
  assert.equal(catalogCalls, 3);
  assert.equal(sent.filter((frame) => frame.type === "model_catalog").length, 3);
  client.close();
});

test("mailbox client receives model control and reports the target result", async () => {
  const sent = [];
  const sockets = [];
  const timers = [];
  const intervals = [];
  const controls = [];
  const current = { provider: "provider", id: "current" };
  const target = { provider: "provider", id: "target" };
  let id = 0;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    onModelControl: async (control) => {
      controls.push(control);
      return { accepted: true, currentModel: target, availableModels: [current, target] };
    },
    webSocketFactory: () => {
      const socket = new FakeSocket(sent);
      sockets.push(socket);
      return socket;
    },
    generateId: () => `id-${++id}`,
    reconnectInitialDelayMs: 1,
    reconnectMaxDelayMs: 1,
    setTimeoutImpl: (callback) => {
      timers.push(callback);
      return timers.length;
    },
    clearTimeoutImpl: () => {},
    setIntervalImpl: (callback) => {
      intervals.push(callback);
      return intervals.length;
    },
    clearIntervalImpl: () => {},
    random: () => 0,
  });

  const connected = client.connect();
  sockets[0].open();
  const registered = sent.find((frame) => frame.type === "register");
  sockets[0].message({
    version: "v1",
    frame_id: "registered-1",
    type: "registered",
    request_id: registered.frame_id,
    agent_id: "worker",
    instance_id: client.instanceId,
    lease_id: "lease-1",
    expires_at: "2030-01-01T00:00:00.000Z",
    recovered: false,
    agent_role: "agent",
  });
  await connected;

  sockets[0].message({
    version: "v1",
    frame_id: "control-frame",
    type: "model_control",
    agent_id: "worker",
    instance_id: client.instanceId,
    lease_id: "lease-1",
    source_agent_id: "controller",
    control_id: "control-1",
    action: "set_model",
    model: target,
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(controls, [{
    controlId: "control-1",
    sourceAgentId: "controller",
    action: "set_model",
    model: target,
  }]);
  const result = sent.find((frame) => frame.type === "model_control_result");
  assert.match(result.frame_id, /^id-\d+$/);
  assert.equal(result.version, "v1");
  assert.equal(result.agent_id, "worker");
  assert.equal(result.instance_id, client.instanceId);
  assert.equal(result.lease_id, "lease-1");
  assert.equal(result.source_agent_id, "controller");
  assert.equal(result.control_id, "control-1");
  assert.equal(result.accepted, true);
  assert.deepEqual(result.current_model, target);
  assert.deepEqual(result.available_models, [current, target]);
  assert.equal(timers.length, 0);
  assert.equal(intervals.length, 2);
  client.close();
});

test("router retains bounded catalogs per active agent, projects updates, and writes no conversation row", async () => {
  const app = await routerFixture();
  const sockets = [];
  try {
    const ui = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "ui-instance", sync: true });
    sockets.push(ui.socket);
    const worker = await connect(app.wsUrl, { agentId: "catalog-worker", instanceId: "worker-instance" });
    sockets.push(worker.socket);
    const other = await connect(app.wsUrl, { agentId: "other-worker", instanceId: "other-instance" });
    sockets.push(other.socket);

    const largeCatalog = modelCatalog(150, "large-provider");
    const largeProjection = nextFrame(ui.socket, (frame) => frame.type === "agent_model_catalog" && frame.agent_id === "catalog-worker");
    worker.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "catalog-worker-frame",
      type: "model_catalog",
      agent_id: "catalog-worker",
      instance_id: "worker-instance",
      lease_id: worker.registered.lease_id,
      model_summaries: largeCatalog,
    }));
    const projection = await largeProjection;
    assert.equal(projection.model_summaries.length, 150);
    assert.equal("conversation_message" in projection, false);

    const otherCatalog = modelCatalog(1, "other-provider");
    other.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "other-worker-frame",
      type: "model_catalog",
      agent_id: "other-worker",
      instance_id: "other-instance",
      lease_id: other.registered.lease_id,
      model_summaries: otherCatalog,
    }));

    const snapshotResponse = await fetch(`${app.httpUrl}/v1/agents`);
    const snapshot = await snapshotResponse.json();
    const agents = new Map(snapshot.agents.map((agent) => [agent.agent_id, agent]));
    assert.equal(agents.get("catalog-worker").available_models.length, 150);
    assert.deepEqual(agents.get("other-worker").available_models, otherCatalog);

    const renewed = nextFrame(worker.socket, (frame) => frame.type === "registered" && frame.request_id === "renew-worker");
    worker.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "renew-worker",
      type: "lease_renew",
      agent_id: "catalog-worker",
      instance_id: "worker-instance",
      lease_id: worker.registered.lease_id,
    }));
    await renewed;
    const renewedSnapshot = await (await fetch(`${app.httpUrl}/v1/agents`)).json();
    assert.equal(new Map(renewedSnapshot.agents.map((agent) => [agent.agent_id, agent])).get("catalog-worker").available_models.length, 150);
    assert.deepEqual(app.repository.conversationHistory({ owner: "shared-user", peer: "catalog-worker" }), []);

    await closeSocket(worker.socket);
    sockets.splice(sockets.indexOf(worker.socket), 1);
    const replacement = await connect(app.wsUrl, { agentId: "catalog-worker", instanceId: "replacement-instance" });
    sockets.push(replacement.socket);
    const replacementSnapshot = await (await fetch(`${app.httpUrl}/v1/agents`)).json();
    const replacementAgent = replacementSnapshot.agents.find((agent) => agent.agent_id === "catalog-worker");
    assert.deepEqual(replacementAgent.available_models, []);
  } finally {
    for (const socket of sockets) await closeSocket(socket).catch(() => {});
    await app.close();
  }
});

test("router fences model control by source lease and connected target", async () => {
  const app = await routerFixture();
  const sockets = [];
  try {
    const source = await connect(app.wsUrl, { agentId: "source-agent", instanceId: "source-instance" });
    sockets.push(source.socket);
    const target = await connect(app.wsUrl, { agentId: "target-agent", instanceId: "target-instance" });
    sockets.push(target.socket);

    const staleError = nextFrame(source.socket, (frame) => frame.type === "error");
    source.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "stale-control",
      type: "model_control",
      agent_id: "source-agent",
      instance_id: "source-instance",
      lease_id: "stale-lease",
      target_agent_id: "target-agent",
      control_id: "control-stale",
      action: "set_model",
      model: { provider: "provider", id: "target" },
    }));
    assert.equal((await staleError).code, "lease_not_active");
    await closeSocket(source.socket);
    sockets.splice(sockets.indexOf(source.socket), 1);

    const replacementSource = await connect(app.wsUrl, { agentId: "source-agent", instanceId: "replacement-source-instance" });
    sockets.push(replacementSource.socket);
    await closeSocket(target.socket);
    sockets.splice(sockets.indexOf(target.socket), 1);

    const disconnectedError = nextFrame(replacementSource.socket, (frame) => frame.type === "error");
    replacementSource.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "disconnected-control",
      type: "model_control",
      agent_id: "source-agent",
      instance_id: "replacement-source-instance",
      lease_id: replacementSource.registered.lease_id,
      target_agent_id: "target-agent",
      control_id: "control-disconnected",
      action: "set_model",
      model: { provider: "provider", id: "target" },
    }));
    assert.equal((await disconnectedError).code, "target_not_connected");
  } finally {
    for (const socket of sockets) await closeSocket(socket).catch(() => {});
    await app.close();
  }
});
