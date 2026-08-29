import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { validateClientFrame, validateServerFrame } from "../harness/protocol.js";
import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.js";

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

async function connect(wsUrl, { agentId, instanceId, sync = false, agentRole } = {}) {
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
    ...(agentRole === undefined ? {} : { agent_role: agentRole }),
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

async function routerFixture(routerOptions = {}) {
  const directory = await mkdtemp(join(tmpdir(), "model-catalog-"));
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    ...(routerOptions.clock === undefined ? {} : { clock: routerOptions.clock }),
  });
  const router = createAgentRouter({
    repository,
    timeoutSweepIntervalMs: 60_000,
    imageAttachmentDirectory: join(directory, "attachments"),
    ...routerOptions,
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

async function modelCatalogCapabilityResponse() {
  return {
    ok: true,
    async json() {
      return {
        model_catalog_v1: true,
        model_catalog_limits: { frame_bytes: 60 * 1024 },
        subagent_lifecycle_v2: false,
      };
    },
  };
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
    current_model: { provider: "provider", id: "model", name: "Model" },
    total_count: 1,
    truncated: false,
  };
  assert.equal(validateClientFrame(catalog), catalog);
  assert.doesNotThrow(() => validateServerFrame({
    version: "v1",
    frame_id: "catalog-event",
    type: "agent_model_catalog",
    agent_id: "worker",
    instance_id: "instance",
    model_summaries: catalog.model_summaries,
    total_count: 1,
    truncated: false,
    generated_at: "2030-01-01T00:00:00.000Z",
  }));
  assert.doesNotThrow(() => validateServerFrame({
    version: "v1",
    frame_id: "model-control-forward",
    type: "model_control",
    agent_id: "worker",
    instance_id: "instance",
    lease_id: "lease",
    source_agent_id: "controller",
    control_id: "control",
    action: "set_model",
    model: { provider: "provider", id: "model" },
    expires_at: "2030-01-01T00:00:30.000Z",
  }));
  const modelControlResult = {
    version: "v1",
    frame_id: "model-control-result",
    type: "model_control_result",
    agent_id: "worker",
    instance_id: "instance",
    lease_id: "lease",
    source_agent_id: "controller",
    control_id: "control",
    accepted: true,
    current_model: { provider: "provider", id: "model" },
  };
  assert.equal(validateClientFrame(modelControlResult), modelControlResult);
  assert.throws(() => validateClientFrame({
    ...modelControlResult,
    available_models: [{ provider: "provider", id: "model" }],
  }));
  assert.throws(() => validateClientFrame({
    ...modelControlResult,
    error_code: "unexpected_error",
  }));
  assert.throws(() => validateClientFrame({
    ...modelControlResult,
    accepted: false,
    error_code: "model_unavailable",
    current_model: undefined,
  }));
  const maximumCatalog = Array.from({ length: 1_000 }, (_, index) => ({
    provider: "provider",
    id: `model-${index}`,
  }));
  assert.doesNotThrow(() => validateClientFrame({
    ...catalog,
    frame_id: "maximum-catalog",
    model_summaries: maximumCatalog,
    total_count: 1_000,
  }));
  assert.throws(() => validateClientFrame({
    ...catalog,
    frame_id: "oversized-catalog",
    model_summaries: [...maximumCatalog, { provider: "provider", id: "model-overflow" }],
    total_count: 1_000,
    truncated: true,
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
    getCurrentModel: () => catalog[0],
    fetchImpl: modelCatalogCapabilityResponse,
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
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(catalogCalls, 1);
  assert.equal(sent.filter((frame) => frame.type === "model_catalog").length, 1);
  assert.equal(sent.find((frame) => frame.type === "model_catalog").model_summaries.length, 150);
  assert.deepEqual(sent.find((frame) => frame.type === "model_catalog").current_model, catalog[0]);
  assert.deepEqual(intervals.map(({ delay }) => delay), [10_000, 10_000]);
  intervals[1].callback();
  assert.equal(catalogCalls, 2);
  assert.equal(sent.filter((frame) => frame.type === "model_catalog").length, 1);
  client.sendActivity("active", { currentModel: catalog[0], availableModels: catalog });
  assert.equal("current_model" in sent.at(-1), true);
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
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(catalogCalls, 3);
  assert.equal(sent.filter((frame) => frame.type === "model_catalog").length, 2);
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
  let catalog = [current, target];
  let currentProjection = current;
  let id = 0;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    onModelControl: async (control) => {
      controls.push(control);
      currentProjection = target;
      return { accepted: true, currentModel: target, availableModels: [current, target] };
    },
    getModelCatalog: () => catalog,
    getCurrentModel: () => currentProjection,
    fetchImpl: modelCatalogCapabilityResponse,
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
  await new Promise((resolve) => setImmediate(resolve));

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
  assert.equal("available_models" in result, false);
  const catalogFrames = sent.filter((frame) => frame.type === "model_catalog");
  assert.equal(catalogFrames.length, 2);
  assert.deepEqual(catalogFrames.at(-1).model_summaries, [current, target]);
  assert.deepEqual(catalogFrames.at(-1).current_model, target);
  assert.ok(sent.indexOf(catalogFrames.at(-1)) < sent.indexOf(result));
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

    const supplementaryId = "🚀".repeat(256);
    const largeCatalog = [
      ...modelCatalog(150, "large-provider"),
      { provider: "large-provider", id: supplementaryId },
    ];
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
    assert.equal(projection.model_summaries.length, 151);
    assert.equal(projection.model_summaries.at(-1).id, supplementaryId);
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
    assert.equal(agents.get("catalog-worker").available_models.length, 151);
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
    assert.equal(new Map(renewedSnapshot.agents.map((agent) => [agent.agent_id, agent])).get("catalog-worker").available_models.length, 151);
    assert.deepEqual(app.repository.conversationHistory({ owner: "shared-user", peer: "catalog-worker" }), []);

    await closeSocket(worker.socket);
    sockets.splice(sockets.indexOf(worker.socket), 1);
    const replacement = await connect(app.wsUrl, { agentId: "catalog-worker", instanceId: "worker-instance" });
    sockets.push(replacement.socket);
    const replacementSnapshot = await (await fetch(`${app.httpUrl}/v1/agents`)).json();
    const replacementAgent = replacementSnapshot.agents.find((agent) => agent.agent_id === "catalog-worker");
    assert.deepEqual(replacementAgent.available_models, []);
  } finally {
    for (const socket of sockets) await closeSocket(socket).catch(() => {});
    await app.close();
  }
});

test("router closes expired sessions and discards their model catalogs", async () => {
  const app = await routerFixture({ leaseDurationMs: 40, timeoutSweepIntervalMs: 10 });
  let worker;
  try {
    worker = await connect(app.wsUrl, { agentId: "expiring-catalog-worker", instanceId: "worker-instance" });
    worker.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "expiring-catalog",
      type: "model_catalog",
      agent_id: "expiring-catalog-worker",
      instance_id: "worker-instance",
      lease_id: worker.registered.lease_id,
      model_summaries: [{ provider: "provider", id: "model" }],
    }));
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("timed out waiting for expired session close")), 1_000);
      worker.socket.once("close", () => {
        clearTimeout(timer);
        resolve();
      });
    });
    const expiredSnapshot = await (await fetch(`${app.httpUrl}/v1/agents`)).json();
    assert.equal(expiredSnapshot.agents.some((agent) => agent.agent_id === "expiring-catalog-worker"), false);

    const replacement = await connect(app.wsUrl, {
      agentId: "expiring-catalog-worker",
      instanceId: "replacement-instance",
    });
    worker = replacement;
    const replacementSnapshot = await (await fetch(`${app.httpUrl}/v1/agents`)).json();
    assert.deepEqual(
      replacementSnapshot.agents.find((agent) => agent.agent_id === "expiring-catalog-worker").available_models,
      [],
    );
  } finally {
    if (worker?.socket.readyState !== WebSocket.CLOSED) await closeSocket(worker.socket).catch(() => {});
    await app.close();
  }
});

test("router fences model control by source lease and connected target", async () => {
  const app = await routerFixture();
  const sockets = [];
  try {
    const source = await connect(app.wsUrl, { agentId: "source-agent", instanceId: "source-instance", agentRole: "user_interaction" });
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
      target_instance_id: "target-instance",
      control_id: "control-stale",
      action: "set_model",
      model: { provider: "provider", id: "target" },
    }));
    assert.equal((await staleError).code, "lease_not_active");
    await closeSocket(source.socket);
    sockets.splice(sockets.indexOf(source.socket), 1);

    const replacementSource = await connect(app.wsUrl, { agentId: "source-agent", instanceId: "replacement-source-instance", agentRole: "user_interaction" });
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
      target_instance_id: "missing-target-instance",
      control_id: "control-disconnected",
      action: "set_model",
      model: { provider: "provider", id: "target" },
    }));
    assert.equal((await disconnectedError).code, "target_not_connected");

    const replacementTarget = await connect(app.wsUrl, {
      agentId: "target-agent",
      instanceId: "replacement-target-instance",
    });
    sockets.push(replacementTarget.socket);
    const replacedError = nextFrame(replacementSource.socket, (frame) => frame.type === "error");
    replacementSource.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "replaced-target-control",
      type: "model_control",
      agent_id: "source-agent",
      instance_id: "replacement-source-instance",
      lease_id: replacementSource.registered.lease_id,
      target_agent_id: "target-agent",
      target_instance_id: "target-instance",
      control_id: "control-replaced-target",
      action: "set_model",
      model: { provider: "provider", id: "target" },
    }));
    assert.equal((await replacedError).code, "target_instance_changed");
  } finally {
    for (const socket of sockets) await closeSocket(socket).catch(() => {});
    await app.close();
  }
});

test("router does not acknowledge a model control when the target send throws", async () => {
  const app = await routerFixture();
  const sockets = [];
  const originalSend = WebSocket.prototype.send;
  try {
    const source = await connect(app.wsUrl, { agentId: "send-source", instanceId: "send-source-instance", agentRole: "user_interaction" });
    sockets.push(source.socket);
    const target = await connect(app.wsUrl, { agentId: "send-target", instanceId: "send-target-instance" });
    sockets.push(target.socket);

    WebSocket.prototype.send = function failTargetModelControl(payload, ...args) {
      const frame = JSON.parse(payload.toString());
      if (frame.type === "model_control" && frame.target_agent_id === undefined) {
        throw new Error("injected target send failure");
      }
      return originalSend.call(this, payload, ...args);
    };

    const outcome = nextFrame(source.socket, (frame) => frame.type === "error");
    originalSend.call(source.socket, JSON.stringify({
      version: "v1",
      frame_id: "send-failure-control",
      type: "model_control",
      agent_id: "send-source",
      instance_id: "send-source-instance",
      lease_id: source.registered.lease_id,
      target_agent_id: "send-target",
      target_instance_id: "send-target-instance",
      control_id: "send-failure",
      action: "set_model",
      model: { provider: "provider", id: "target" },
    }));
    const response = await outcome;
    assert.equal(response.type, "error");
    assert.equal(response.code, "target_send_failed");
  } finally {
    WebSocket.prototype.send = originalSend;
    for (const socket of sockets) await closeSocket(socket).catch(() => {});
    await app.close();
  }
});

test("router advertises model catalog support with wildcard browser CORS", async () => {
  const app = await routerFixture();
  try {
    const capabilities = await (await fetch(`${app.httpUrl}/v1/capabilities`)).json();
    assert.equal(capabilities.model_catalog_v1, true);
    assert.equal(capabilities.model_catalog_limits.frame_bytes, 60 * 1024);

    const agents = await fetch(`${app.httpUrl}/v1/agents`, {
      headers: { origin: "https://untrusted.example" },
    });
    assert.equal(agents.status, 200);
    assert.equal(agents.headers.get("access-control-allow-origin"), "*");

    const preflight = await fetch(`${app.httpUrl}/v1/agents`, {
      method: "OPTIONS",
      headers: {
        origin: "https://untrusted.example",
        "access-control-request-method": "GET",
      },
    });
    assert.equal(preflight.status, 204);
    assert.equal(preflight.headers.get("access-control-allow-origin"), "*");
  } finally {
    await app.close();
  }
});

test("router accepts one exact model-control result and rejects mismatches, duplicates, and expiry", async () => {
  let nowMs = Date.now();
  const clock = () => new Date(nowMs);
  const app = await routerFixture({
    clock,
    leaseDurationMs: 60_000,
    pendingModelControlTtlMs: 30_000,
  });
  const sockets = [];
  try {
    const source = await connect(app.wsUrl, { agentId: "source-agent", instanceId: "source-instance", agentRole: "user_interaction" });
    sockets.push(source.socket);
    const target = await connect(app.wsUrl, { agentId: "target-agent", instanceId: "target-instance" });
    sockets.push(target.socket);
    const otherTarget = await connect(app.wsUrl, { agentId: "other-target", instanceId: "other-target-instance" });
    sockets.push(otherTarget.socket);

    const initialCatalog = [{ provider: "provider", id: "initial" }];
    target.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "initial-catalog",
      type: "model_catalog",
      agent_id: "target-agent",
      instance_id: "target-instance",
      lease_id: target.registered.lease_id,
      model_summaries: initialCatalog,
    }));

    const forwarded = nextFrame(target.socket, (frame) => frame.type === "model_control");
    source.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "control-request",
      type: "model_control",
      agent_id: "source-agent",
      instance_id: "source-instance",
      lease_id: source.registered.lease_id,
      target_agent_id: "target-agent",
      target_instance_id: "target-instance",
      control_id: "control-one",
      action: "set_model",
      model: { provider: "provider", id: "selected" },
    }));
    await forwarded;

    const wrongTargetError = nextFrame(otherTarget.socket, (frame) => frame.type === "error");
    otherTarget.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "wrong-target-result",
      type: "model_control_result",
      agent_id: "other-target",
      instance_id: "other-target-instance",
      lease_id: otherTarget.registered.lease_id,
      source_agent_id: "source-agent",
      control_id: "control-one",
      accepted: true,
      current_model: { provider: "provider", id: "wrong-target" },
    }));
    assert.equal((await wrongTargetError).code, "model_control_result_not_pending");
    const otherTargetSnapshot = (await (await fetch(`${app.httpUrl}/v1/agents`)).json()).agents
      .find((agent) => agent.agent_id === "other-target");
    assert.equal(otherTargetSnapshot.current_model, undefined);

    const mismatchError = nextFrame(target.socket, (frame) => frame.type === "error");
    target.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "mismatched-result",
      type: "model_control_result",
      agent_id: "target-agent",
      instance_id: "target-instance",
      lease_id: target.registered.lease_id,
      source_agent_id: "different-source",
      control_id: "control-one",
      accepted: true,
      current_model: { provider: "provider", id: "mismatched" },
    }));
    assert.equal((await mismatchError).code, "model_control_result_not_pending");
    let targetSnapshot = (await (await fetch(`${app.httpUrl}/v1/agents`)).json()).agents
      .find((agent) => agent.agent_id === "target-agent");
    assert.deepEqual(targetSnapshot.available_models, initialCatalog);
    assert.equal(targetSnapshot.current_model, undefined);

    const resultForSource = nextFrame(source.socket, (frame) => frame.type === "model_control_result");
    const acceptedProjection = [{ provider: "provider", id: "selected" }];
    target.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "accepted-catalog",
      type: "model_catalog",
      agent_id: "target-agent",
      instance_id: "target-instance",
      lease_id: target.registered.lease_id,
      model_summaries: acceptedProjection,
      current_model: acceptedProjection[0],
    }));
    const exactResult = {
      version: "v1",
      frame_id: "exact-result",
      type: "model_control_result",
      agent_id: "target-agent",
      instance_id: "target-instance",
      lease_id: target.registered.lease_id,
      source_agent_id: "source-agent",
      control_id: "control-one",
      accepted: true,
      current_model: acceptedProjection[0],
    };
    target.socket.send(JSON.stringify(exactResult));
    assert.equal((await resultForSource).accepted, true);

    const duplicateError = nextFrame(target.socket, (frame) => frame.type === "error");
    target.socket.send(JSON.stringify({ ...exactResult, frame_id: "duplicate-result" }));
    assert.equal((await duplicateError).code, "model_control_result_not_pending");
    targetSnapshot = (await (await fetch(`${app.httpUrl}/v1/agents`)).json()).agents
      .find((agent) => agent.agent_id === "target-agent");
    assert.deepEqual(targetSnapshot.current_model, acceptedProjection[0]);
    assert.deepEqual(targetSnapshot.available_models, acceptedProjection);

    const secondForward = nextFrame(target.socket, (frame) => frame.type === "model_control" && frame.control_id === "control-expiring");
    source.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "expiring-control-request",
      type: "model_control",
      agent_id: "source-agent",
      instance_id: "source-instance",
      lease_id: source.registered.lease_id,
      target_agent_id: "target-agent",
      target_instance_id: "target-instance",
      control_id: "control-expiring",
      action: "set_model",
      model: { provider: "provider", id: "late" },
    }));
    await secondForward;
    nowMs += 30_001;
    const expiredForSource = nextFrame(source.socket, (frame) => frame.type === "error"
      && frame.request_id === "expiring-control-request");
    const lateError = nextFrame(target.socket, (frame) => frame.type === "error");
    target.socket.send(JSON.stringify({
      ...exactResult,
      frame_id: "late-result",
      control_id: "control-expiring",
      current_model: { provider: "provider", id: "late" },
    }));
    assert.equal((await expiredForSource).code, "model_control_expired");
    assert.equal((await lateError).code, "model_control_result_not_pending");
    targetSnapshot = (await (await fetch(`${app.httpUrl}/v1/agents`)).json()).agents
      .find((agent) => agent.agent_id === "target-agent");
    assert.deepEqual(targetSnapshot.current_model, acceptedProjection[0]);
    assert.deepEqual(targetSnapshot.available_models, acceptedProjection);
  } finally {
    for (const socket of sockets) await closeSocket(socket).catch(() => {});
    await app.close();
  }
});

test("router rejects a result after the exact source lease expires", async () => {
  let nowMs = Date.now();
  const app = await routerFixture({
    clock: () => new Date(nowMs),
    leaseDurationMs: 1_000,
    pendingModelControlTtlMs: 30_000,
  });
  const sockets = [];
  try {
    const source = await connect(app.wsUrl, { agentId: "expiring-source", instanceId: "expiring-source-instance", agentRole: "user_interaction" });
    sockets.push(source.socket);
    nowMs += 500;
    const target = await connect(app.wsUrl, { agentId: "live-target", instanceId: "live-target-instance" });
    sockets.push(target.socket);

    const forwarded = nextFrame(target.socket, (frame) => frame.type === "model_control");
    source.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "expiring-source-control",
      type: "model_control",
      agent_id: "expiring-source",
      instance_id: "expiring-source-instance",
      lease_id: source.registered.lease_id,
      target_agent_id: "live-target",
      target_instance_id: "live-target-instance",
      control_id: "expiring-source-control",
      action: "set_model",
      model: { provider: "provider", id: "target" },
    }));
    await forwarded;

    nowMs += 600;
    let sourceResultCount = 0;
    const countSourceResult = (payload) => {
      if (JSON.parse(payload.toString("utf8")).type === "model_control_result") sourceResultCount += 1;
    };
    source.socket.on("message", countSourceResult);
    const rejected = nextFrame(target.socket, (frame) => frame.type === "error");
    target.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "result-after-source-expiry",
      type: "model_control_result",
      agent_id: "live-target",
      instance_id: "live-target-instance",
      lease_id: target.registered.lease_id,
      source_agent_id: "expiring-source",
      control_id: "expiring-source-control",
      accepted: true,
      current_model: { provider: "provider", id: "target" },
    }));
    assert.equal((await rejected).code, "model_control_result_not_pending");
    await new Promise((resolve) => setTimeout(resolve, 20));
    source.socket.off("message", countSourceResult);
    assert.equal(sourceResultCount, 0);
    const snapshot = await (await fetch(`${app.httpUrl}/v1/agents`)).json();
    assert.equal(snapshot.agents.find((agent) => agent.agent_id === "live-target").current_model, undefined);
  } finally {
    for (const socket of sockets) await closeSocket(socket).catch(() => {});
    await app.close();
  }
});

test("router bounds pending model controls and cleans them up when either exact session disconnects", async () => {
  const app = await routerFixture({
    maxPendingModelControls: 2,
    maxPendingModelControlsPerSource: 1,
  });
  const sockets = [];
  try {
    const source = await connect(app.wsUrl, { agentId: "source-one", instanceId: "source-one-instance", agentRole: "user_interaction" });
    sockets.push(source.socket);
    const sourceTwo = await connect(app.wsUrl, { agentId: "source-two", instanceId: "source-two-instance", agentRole: "user_interaction" });
    sockets.push(sourceTwo.socket);
    const sourceThree = await connect(app.wsUrl, { agentId: "source-three", instanceId: "source-three-instance", agentRole: "user_interaction" });
    sockets.push(sourceThree.socket);
    const target = await connect(app.wsUrl, { agentId: "target-one", instanceId: "target-one-instance" });
    sockets.push(target.socket);
    const targetTwo = await connect(app.wsUrl, { agentId: "target-two", instanceId: "target-two-instance" });
    sockets.push(targetTwo.socket);

    const sendControl = (sender, targetAgentId, controlId) => sender.socket.send(JSON.stringify({
      version: "v1",
      frame_id: `${controlId}-frame`,
      type: "model_control",
      agent_id: sender.registered.agent_id,
      instance_id: sender.registered.instance_id,
      lease_id: sender.registered.lease_id,
      target_agent_id: targetAgentId,
      target_instance_id: `${targetAgentId}-instance`,
      control_id: controlId,
      action: "set_model",
      model: { provider: "provider", id: controlId },
    }));

    const firstForward = nextFrame(target.socket, (frame) => frame.type === "model_control" && frame.control_id === "control-one");
    sendControl(source, "target-one", "control-one");
    await firstForward;

    const duplicateError = nextFrame(source.socket, (frame) => frame.type === "error");
    sendControl(source, "target-one", "control-one");
    assert.equal((await duplicateError).code, "model_control_already_pending");

    const perSourceError = nextFrame(source.socket, (frame) => frame.type === "error");
    sendControl(source, "target-two", "control-two");
    assert.equal((await perSourceError).code, "model_control_capacity_exceeded");

    const secondForward = nextFrame(targetTwo.socket, (frame) => frame.type === "model_control" && frame.control_id === "control-three");
    sendControl(sourceTwo, "target-two", "control-three");
    await secondForward;

    const globalError = nextFrame(sourceThree.socket, (frame) => frame.type === "error");
    sendControl(sourceThree, "target-one", "control-four");
    assert.equal((await globalError).code, "model_control_capacity_exceeded");

    await closeSocket(source.socket);
    sockets.splice(sockets.indexOf(source.socket), 1);
    const sourceReplacement = await connect(app.wsUrl, { agentId: "source-one", instanceId: "source-one-instance", agentRole: "user_interaction" });
    sockets.push(sourceReplacement.socket);
    const afterSourceCleanup = nextFrame(target.socket, (frame) => frame.type === "model_control" && frame.control_id === "control-after-source-close");
    sendControl(sourceReplacement, "target-one", "control-after-source-close");
    await afterSourceCleanup;

    const targetDisconnected = nextFrame(sourceTwo.socket, (frame) => frame.type === "error"
      && frame.request_id === "control-three-frame");
    await closeSocket(targetTwo.socket);
    assert.equal((await targetDisconnected).code, "target_not_connected");
    sockets.splice(sockets.indexOf(targetTwo.socket), 1);
    const targetTwoReplacement = await connect(app.wsUrl, { agentId: "target-two", instanceId: "target-two-instance" });
    sockets.push(targetTwoReplacement.socket);
    const afterTargetCleanup = nextFrame(targetTwoReplacement.socket, (frame) => frame.type === "model_control" && frame.control_id === "control-after-target-close");
    sendControl(sourceTwo, "target-two", "control-after-target-close");
    await afterTargetCleanup;
  } finally {
    for (const socket of sockets) await closeSocket(socket).catch(() => {});
    await app.close();
  }
});
