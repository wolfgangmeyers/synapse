import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { request as httpRequest } from "node:http";
import { DatabaseSync } from "node:sqlite";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import {
  ProtocolValidationError,
  validateClientFrame,
  validateEnvelope,
  validateServerFrame,
} from "../harness/protocol.js";
import { createAgentRouter } from "../harness/router.js";

const register = {
  version: "v1",
  frame_id: "register-1",
  type: "register",
  agent_id: "android-550e8400-e29b-41d4-a716-446655440000",
  instance_id: "instance-1",
  agent_role: "user_interaction",
  client_type: "android",
  ack_confirmation: "correlated_v1",
};

test("Android registration metadata is accepted while legacy registration remains valid", () => {
  assert.equal(validateClientFrame(register), register);

  const legacy = {
    version: "v1",
    frame_id: "register-legacy",
    type: "register",
    agent_id: "worker",
    instance_id: "instance-legacy",
  };
  assert.equal(validateClientFrame(legacy), legacy);
});

test("registered frames require the effective role and only allow negotiated ACK confirmation", () => {
  const registered = {
    version: "v1",
    frame_id: "registered-1",
    type: "registered",
    request_id: register.frame_id,
    agent_id: register.agent_id,
    instance_id: register.instance_id,
    lease_id: "lease-1",
    expires_at: "2026-07-15T06:00:30.000Z",
    recovered: false,
    agent_role: "user_interaction",
    ack_confirmation: "correlated_v1",
  };
  assert.equal(validateServerFrame(registered), registered);

  const missingRole = { ...registered };
  delete missingRole.agent_role;
  assert.throws(() => validateServerFrame(missingRole), ProtocolValidationError);

  const legacy = { ...registered, agent_role: "agent" };
  delete legacy.ack_confirmation;
  assert.equal(validateServerFrame(legacy), legacy);
});

test("router echoes Android ACK capability but preserves legacy response shape", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-contract-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const events = [];
  const router = createAgentRouter({
    repository,
    timeoutSweepIntervalMs: 60_000,
    eventLogger: (event, fields) => events.push({ event, fields }),
  });
  const websocketUrl = await router.listen();
  const sockets = [];
  const connect = async () => {
    const socket = new WebSocket(websocketUrl);
    sockets.push(socket);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    return socket;
  };
  const nextFrame = (socket) => new Promise((resolve, reject) => {
    socket.once("message", (payload) => resolve(JSON.parse(payload.toString("utf8"))));
    socket.once("error", reject);
  });
  try {
    const sender = await connect();
    const senderRegistered = nextFrame(sender);
    sender.send(JSON.stringify({
      version: "v1",
      frame_id: "sender-register-1",
      type: "register",
      agent_id: "sender",
      instance_id: "sender-instance",
    }));
    await senderRegistered;

    const android = await connect();
    const androidRegistered = nextFrame(android);
    android.send(JSON.stringify(register));
    const frame = await androidRegistered;
    assert.equal(frame.version, "v1");
    assert.match(frame.frame_id, /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/);
    assert.equal(frame.type, "registered");
    assert.equal(frame.request_id, register.frame_id);
    assert.equal(frame.agent_id, register.agent_id);
    assert.equal(frame.instance_id, register.instance_id);
    assert.match(frame.lease_id, /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/);
    assert.match(frame.expires_at, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    assert.equal(frame.recovered, false);
    assert.equal(frame.agent_role, "user_interaction");
    assert.equal(frame.ack_confirmation, "correlated_v1");
    const renewed = nextFrame(android);
    android.send(JSON.stringify({
      version: "v1",
      frame_id: "renew-1",
      type: "lease_renew",
      agent_id: register.agent_id,
      instance_id: register.instance_id,
      lease_id: frame.lease_id,
    }));
    assert.equal((await renewed).ack_confirmation, "correlated_v1");
    assert.deepEqual(events.find(({ event, fields }) => event === "agent_registered" && fields.agent_id === register.agent_id).fields.client_type, "android");

    const offered = nextFrame(android);
    router.enqueueMessage({
      id: "reply-hint-message",
      from: "sender",
      to: register.agent_id,
      body: "raw body",
      created_at: "2026-07-15T06:00:00.000Z",
    });
    assert.equal((await offered).message.body, "raw body\n\n[Reply via mailbox to sender.]");
    assert.equal(repository.messageFor("reply-hint-message").body, "raw body");
  } finally {
    for (const socket of sockets) socket.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("negotiated ACK sends one correlated settlement confirmation", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-confirmation-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let socket;
  let frameId = 0;
  const nextFrame = (timeoutMs = 500) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timed out waiting for router frame")), timeoutMs);
    socket.once("message", (payload) => {
      clearTimeout(timer);
      resolve(JSON.parse(payload.toString("utf8")));
    });
    socket.once("error", reject);
  });
  const send = (frame) => socket.send(JSON.stringify({ version: "v1", frame_id: `frame-${++frameId}`, ...frame }));
  try {
    socket = new WebSocket(await router.listen());
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    const registered = nextFrame();
    socket.send(JSON.stringify(register));
    const lease = await registered;

    const delivery = nextFrame();
    router.enqueueMessage({
      id: "confirmation-message",
      from: "sender",
      to: register.agent_id,
      body: "body",
      created_at: "2026-07-15T06:00:00.000Z",
    });
    const offered = await delivery;
    send({
      type: "injection_requested",
      agent_id: register.agent_id,
      instance_id: register.instance_id,
      lease_id: lease.lease_id,
      message_id: offered.message.id,
      attempt_number: offered.attempt_number,
    });
    const confirmation = nextFrame();
    send({
      type: "ack",
      agent_id: register.agent_id,
      instance_id: register.instance_id,
      lease_id: lease.lease_id,
      message_id: offered.message.id,
      attempt_number: offered.attempt_number,
    });
    const settled = await confirmation;
    assert.match(settled.frame_id, /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/);
    assert.deepEqual({ ...settled, frame_id: undefined }, {
      version: "v1",
      frame_id: undefined,
      type: "delivery_acknowledged",
      request_id: "frame-2",
      agent_id: register.agent_id,
      instance_id: register.instance_id,
      lease_id: lease.lease_id,
      message_id: offered.message.id,
      message_incarnation_id: offered.message_incarnation_id,
      attempt_number: offered.attempt_number,
    });
  } finally {
    socket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("legacy ACK does not receive correlated settlement confirmation", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-legacy-ack-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), maxBatchBodyChars: 4_096 });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let socket;
  let frameId = 0;
  const frames = [];
  const waitForFrame = async (predicate) => {
    for (let attempt = 0; attempt < 40; attempt += 1) {
      const frame = frames.find(predicate);
      if (frame !== undefined) return frame;
      await new Promise((resolve) => setTimeout(resolve, 5));
    }
    throw new Error("timed out waiting for frame");
  };
  const send = (frame) => socket.send(JSON.stringify({ version: "v1", frame_id: `legacy-${++frameId}`, ...frame }));
  try {
    socket = new WebSocket(await router.listen());
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    socket.on("message", (payload) => frames.push(JSON.parse(payload.toString("utf8"))));
    router.enqueueMessage({
      id: "legacy-ack-message",
      from: "sender",
      to: "legacy-worker",
      body: "body",
      created_at: "2026-07-15T06:00:00.000Z",
    });
    router.enqueueMessage({
      id: "legacy-ack-message-2",
      from: "sender",
      to: "legacy-worker",
      body: "second body",
      created_at: "2026-07-15T06:00:01.000Z",
    });

    send({ type: "register", agent_id: "legacy-worker", instance_id: "legacy-instance" });
    const lease = await waitForFrame((frame) => frame.type === "registered");
    assert.equal(lease.agent_role, "agent");
    assert.equal(lease.ack_confirmation, undefined);

    const offered = await waitForFrame((frame) => frame.type === "message");
    assert.equal(offered.message.id, "legacy-ack-message");
    assert.equal(offered.delivery_items.length, 2);
    assert.match(offered.message.body, /second body/);
    send({ type: "injection_requested", agent_id: "legacy-worker", instance_id: "legacy-instance", lease_id: lease.lease_id, message_id: offered.message.id, attempt_number: offered.attempt_number });
    send({ type: "ack", agent_id: "legacy-worker", instance_id: "legacy-instance", lease_id: lease.lease_id, message_id: offered.message.id, attempt_number: offered.attempt_number });
    await new Promise((resolve) => setTimeout(resolve, 25));
    assert.equal(repository.messageFor(offered.message.id), undefined);
    assert.equal(repository.messageFor("legacy-ack-message-2"), undefined);
  } finally {
    socket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("correlated delivery confirmation validates the settled lease and incarnation", () => {
  const confirmation = {
    version: "v1",
    frame_id: "confirmation-1",
    type: "delivery_acknowledged",
    request_id: "ack-1",
    agent_id: register.agent_id,
    instance_id: register.instance_id,
    lease_id: "lease-1",
    message_id: "message-1",
    message_incarnation_id: "550e8400-e29b-41d4-a716-446655440000",
    attempt_number: 1,
  };
  assert.equal(validateServerFrame(confirmation), confirmation);
});

test("delivery frames require the router-generated message incarnation", () => {
  const message = {
    version: "v1",
    frame_id: "message-1",
    type: "message",
    message: {
      id: "message-1",
      from: "sender",
      to: "worker",
      body: "body",
      created_at: "2026-07-15T06:00:00.000Z",
    },
    attempt_number: 1,
    delivery_expires_at: "2026-07-15T06:00:30.000Z",
    message_incarnation_id: "550e8400-e29b-41d4-a716-446655440000",
  };
  assert.equal(validateServerFrame(message), message);

  const missingIncarnation = { ...message };
  delete missingIncarnation.message_incarnation_id;
  assert.throws(() => validateServerFrame(missingIncarnation), ProtocolValidationError);
});

test("router rejects malformed WebSocket UTF-8 before frame admission", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-ws-utf8-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let socket;
  try {
    socket = new WebSocket(await router.listen());
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    const response = new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error("malformed frame was not rejected")), 500);
      socket.once("message", (payload) => {
        clearTimeout(timeout);
        resolve(JSON.parse(payload.toString("utf8")));
      });
      socket.once("close", () => {
        clearTimeout(timeout);
        resolve({ type: "closed" });
      });
      socket.once("error", reject);
    });
    socket.send(Buffer.from([0x7b, 0x22, 0x78, 0x22, 0x3a, 0x22, 0xed, 0xa0, 0x80, 0x22, 0x7d]), { binary: false });
    assert.ok(["error", "closed"].includes((await response).type));
    assert.equal(repository.leaseFor(register.agent_id), undefined);
  } finally {
    socket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router rejects malformed HTTP UTF-8 before queue admission", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-utf8-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  try {
    const websocketUrl = await router.listen();
    const body = Buffer.concat([
      Buffer.from('{"id":"invalid-utf8-1","from":"sender'),
      Buffer.from([0xed, 0xa0, 0x80]),
      Buffer.from('","to":"worker","body":"body","created_at":"2026-07-15T06:00:00.000Z"}'),
    ]);
    const response = await new Promise((resolve, reject) => {
      const request = httpRequest(websocketUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/messages"), {
        method: "POST",
        headers: { "content-type": "application/json", "content-length": body.length },
      }, (result) => {
        const chunks = [];
        result.on("data", (chunk) => chunks.push(chunk));
        result.on("end", () => resolve({ status: result.statusCode, body: Buffer.concat(chunks).toString("utf8") }));
      });
      request.once("error", reject);
      request.end(body);
    });
    assert.equal(response.status, 400);
    assert.equal(repository.messageFor("invalid-utf8-1"), undefined);
  } finally {
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("protocol rejects escaped unpaired surrogates before envelope admission", () => {
  assert.throws(() => validateEnvelope({
    id: "invalid-scalar-1",
    from: "sender\ud800",
    to: "worker",
    body: "body",
    created_at: "2026-07-15T06:00:00.000Z",
  }), ProtocolValidationError);
});

test("health reports migration 005 for Android preflight", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-health-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  try {
    const websocketUrl = await router.listen();
    const response = await fetch(websocketUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/health"));
    assert.equal(response.status, 200);
    assert.ok((await response.json()).schema_migrations.includes("005_message_incarnation"));
  } finally {
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("message lookup includes its router-generated incarnation", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-message-lookup-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  try {
    const websocketUrl = await router.listen();
    const queued = router.enqueueMessage({
      id: "lookup-incarnation-1",
      from: "sender",
      to: "worker",
      body: "body",
      created_at: "2026-07-15T06:00:00.000Z",
    });
    const response = await fetch(websocketUrl
      .replace("ws://", "http://")
      .replace("/v1/agents", "/v1/messages/lookup-incarnation-1"));
    assert.equal(response.status, 200);
    const { message } = await response.json();
    assert.equal(message.message_incarnation_id, queued.message.message_incarnation_id);
  } finally {
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router enqueue events never log the claimed sender", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-logging-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const events = [];
  const router = createAgentRouter({ repository, eventLogger: (event, fields) => events.push({ event, fields }) });
  try {
    router.enqueueMessage({
      id: "hostile-sender-1",
      from: "sender\nsecret-marker",
      to: "worker",
      body: "body",
      created_at: "2026-07-15T06:00:00.000Z",
    });
    const enqueue = events.find(({ event }) => event === "message_enqueued");
    assert.equal(enqueue.fields.from, undefined);
    assert.equal(JSON.stringify(enqueue), JSON.stringify({
      event: "message_enqueued",
      fields: {
        message_id: "hostile-sender-1",
        to: "worker",
        agent_id: "worker",
        state: "queued",
        created: true,
      },
    }));
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("migration 005 backfills an existing queued row with a UUIDv4 incarnation", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-migration-"));
  const databasePath = join(directory, "router.sqlite");
  const database = new DatabaseSync(databasePath);
  try {
    for (const id of ["001_initial", "002_delivery_attempt_deadline", "003_reminders", "004_server_jobs"]) {
      database.exec(readFileSync(new URL(`../harness/migrations/${id}.sql`, import.meta.url), "utf8"));
    }
    database.exec("CREATE TABLE schema_migrations (id TEXT PRIMARY KEY, applied_at TEXT NOT NULL)");
    for (const id of ["001_initial", "002_delivery_attempt_deadline", "003_reminders", "004_server_jobs"]) {
      database.prepare("INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)").run(id, "2026-07-15T06:00:00.000Z");
    }
    database.prepare("INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?)").run("worker", "2026-07-15T06:00:00.000Z");
    database.prepare(`
      INSERT INTO messages (id, sender, recipient, subject, body, created_at, expires_at, state)
      VALUES (?, ?, ?, NULL, ?, ?, ?, 'queued')
    `).run("pre-migration-message", "sender", "worker", "body", "2026-07-15T06:00:00.000Z", "2026-07-15T07:00:00.000Z");
  } finally {
    database.close();
  }

  const repository = openMessageRepository({ databasePath });
  try {
    assert.match(repository.messageFor("pre-migration-message").message_incarnation_id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("queue expiry is anchored to router first-seen time, not sender time", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-expiry-"));
  const firstSeenAt = new Date("2026-07-15T06:00:00.000Z");
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => firstSeenAt,
  });
  try {
    const { message } = repository.enqueue({
      id: "router-expiry-1",
      from: "sender",
      to: "worker",
      body: "body",
      created_at: "2099-01-01T00:00:00.000Z",
    });
    assert.equal(message.expires_at, "2026-07-15T07:00:00.000Z");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("queued messages retain one router-generated incarnation across identical enqueue", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-incarnation-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const message = {
    id: "incarnation-1",
    from: "sender",
    to: "worker",
    body: "body",
    created_at: "2026-07-15T06:00:00.000Z",
  };
  try {
    const first = repository.enqueue(message);
    assert.equal(first.created, true);
    assert.match(first.message.message_incarnation_id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);

    const duplicate = repository.enqueue(message);
    assert.equal(duplicate.created, false);
    assert.equal(duplicate.message.message_incarnation_id, first.message.message_incarnation_id);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("discovery excludes a session after its repository lease expires", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-discovery-expiry-"));
  let now = Date.parse("2026-07-15T06:00:00.000Z");
  const clock = () => new Date(now);
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock });
  const router = createAgentRouter({ repository, clock, leaseDurationMs: 30_000, timeoutSweepIntervalMs: 60_000 });
  let socket;
  try {
    const websocketUrl = await router.listen();
    socket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    const registered = new Promise((resolve, reject) => {
      socket.once("message", (payload) => resolve(JSON.parse(payload.toString("utf8"))));
      socket.once("error", reject);
    });
    socket.send(JSON.stringify(register));
    await registered;
    now += 30_000;
    const response = await fetch(websocketUrl.replace("ws://", "http://"));
    assert.deepEqual((await response.json()).agents, []);
  } finally {
    socket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("activity state is lease validated, broadcast on transitions, and expires by router TTL", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-activity-"));
  let now = Date.parse("2026-07-15T06:00:00.000Z");
  const clock = () => new Date(now);
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock });
  const router = createAgentRouter({ repository, clock, activityTtlMs: 5_000, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  const connect = async () => {
    const socket = new WebSocket(await websocketUrlPromise);
    sockets.push(socket);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    return socket;
  };
  let websocketUrlPromise;
  const nextFrame = (socket, predicate = () => true, timeoutMs = 500) => new Promise((resolve, reject) => {
    const cleanup = () => {
      clearTimeout(timer);
      socket.off("message", onMessage);
      socket.off("error", onError);
    };
    const timer = setTimeout(() => {
      cleanup();
      reject(new Error("timed out waiting for router frame"));
    }, timeoutMs);
    const onError = (error) => {
      cleanup();
      reject(error);
    };
    const onMessage = (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (!predicate(frame)) return;
      cleanup();
      resolve(frame);
    };
    socket.on("message", onMessage);
    socket.once("error", onError);
  });
  try {
    websocketUrlPromise = router.listen();
    const websocketUrl = await websocketUrlPromise;
    const worker = await connect();
    const workerRegistered = nextFrame(worker);
    worker.send(JSON.stringify({ version: "v1", frame_id: "worker-register", type: "register", agent_id: "worker", instance_id: "worker-instance" }));
    const workerLease = await workerRegistered;

    const android = await connect();
    const androidRegistered = nextFrame(android);
    android.send(JSON.stringify({ ...register, conversation_sync: "v1" }));
    await androidRegistered;

    const rogue = await connect();
    const rogueRegistered = nextFrame(rogue);
    rogue.send(JSON.stringify({ version: "v1", frame_id: "rogue-register", type: "register", agent_id: "rogue", instance_id: "rogue-instance" }));
    const rogueLease = await rogueRegistered;
    const rejected = nextFrame(android, (frame) => frame.type === "agent_activity", 150).then(() => "unexpected", () => "timeout");
    rogue.send(JSON.stringify({ version: "v1", frame_id: "wrong-activity", type: "activity", agent_id: "worker", instance_id: "rogue-instance", lease_id: rogueLease.lease_id, activity_state: "active" }));
    assert.equal((await nextFrame(rogue)).code, "lease_not_active");
    assert.equal(await rejected, "timeout");

    const activeEvent = nextFrame(android, (frame) => frame.type === "agent_activity" && frame.agent_id === "worker");
    worker.send(JSON.stringify({ version: "v1", frame_id: "worker-active", type: "activity", agent_id: "worker", instance_id: "worker-instance", lease_id: workerLease.lease_id, activity_state: "active", subagent_count: 2 }));
    const activeFrame = await activeEvent;
    assert.equal(activeFrame.activity_state, "active");
    assert.equal(activeFrame.subagent_count, 2);
    const activeSnapshot = await fetch(websocketUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/agents"));
    const activeWorker = (await activeSnapshot.json()).agents.find((agent) => agent.agent_id === "worker");
    assert.equal(activeWorker.activity_state, "active");
    assert.equal(activeWorker.subagent_count, 2);

    const clearingRogue = await connect();
    const clearingRogueRegistered = nextFrame(clearingRogue);
    clearingRogue.send(JSON.stringify({ version: "v1", frame_id: "clearing-rogue-register", type: "register", agent_id: "clearing-rogue", instance_id: "clearing-rogue-instance" }));
    const clearingRogueLease = await clearingRogueRegistered;
    const noRogueIdle = nextFrame(android, (frame) => frame.type === "agent_activity" && frame.activity_state === "idle", 150).then(() => "unexpected", () => "timeout");
    clearingRogue.send(JSON.stringify({ version: "v1", frame_id: "wrong-settled", type: "activity", agent_id: "worker", instance_id: "clearing-rogue-instance", lease_id: clearingRogueLease.lease_id, activity_state: "settled" }));
    assert.equal((await nextFrame(clearingRogue)).code, "lease_not_active");
    assert.equal(await noRogueIdle, "timeout");

    now += 4_000;
    worker.send(JSON.stringify({ version: "v1", frame_id: "worker-active-2", type: "activity", agent_id: "worker", instance_id: "worker-instance", lease_id: workerLease.lease_id, activity_state: "active", subagent_count: 2 }));
    assert.equal(await nextFrame(android, (frame) => frame.type === "agent_activity", 150).then(() => "unexpected", () => "timeout"), "timeout");
    const countChange = nextFrame(android, (frame) => frame.type === "agent_activity" && frame.agent_id === "worker");
    worker.send(JSON.stringify({ version: "v1", frame_id: "worker-active-count-change", type: "activity", agent_id: "worker", instance_id: "worker-instance", lease_id: workerLease.lease_id, activity_state: "active", subagent_count: 1 }));
    assert.equal((await countChange).subagent_count, 1);
    now += 1_001;
    router.sweepMessageLifecycles();
    const renewedSnapshot = await fetch(websocketUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/agents"));
    assert.equal((await renewedSnapshot.json()).agents.find((agent) => agent.agent_id === "worker").activity_state, "active");

    const idleAfterRenewal = nextFrame(android, (frame) => frame.type === "agent_activity" && frame.agent_id === "worker");
    now += 4_000;
    router.sweepMessageLifecycles();
    const idleAfterRenewalFrame = await idleAfterRenewal;
    assert.equal(idleAfterRenewalFrame.activity_state, "idle");
    assert.equal(idleAfterRenewalFrame.subagent_count, 0);

    const activeForSettle = nextFrame(android, (frame) => frame.type === "agent_activity" && frame.activity_state === "active");
    worker.send(JSON.stringify({ version: "v1", frame_id: "worker-active-for-settle", type: "activity", agent_id: "worker", instance_id: "worker-instance", lease_id: workerLease.lease_id, activity_state: "active" }));
    await activeForSettle;
    const idleEvent = nextFrame(android, (frame) => frame.type === "agent_activity" && frame.agent_id === "worker");
    worker.send(JSON.stringify({ version: "v1", frame_id: "worker-settled", type: "activity", agent_id: "worker", instance_id: "worker-instance", lease_id: workerLease.lease_id, activity_state: "settled" }));
    const settledFrame = await idleEvent;
    assert.equal(settledFrame.activity_state, "idle");
    assert.equal(settledFrame.subagent_count, 0);

    const activeAgain = nextFrame(android, (frame) => frame.type === "agent_activity" && frame.activity_state === "active");
    worker.send(JSON.stringify({ version: "v1", frame_id: "worker-active-3", type: "activity", agent_id: "worker", instance_id: "worker-instance", lease_id: workerLease.lease_id, activity_state: "active" }));
    await activeAgain;
    const disconnectedIdle = nextFrame(android, (frame) => frame.type === "agent_activity" && frame.agent_id === "worker");
    worker.close();
    assert.equal((await disconnectedIdle).activity_state, "idle");
  } finally {
    for (const socket of sockets) socket.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("expired leases cannot publish activity", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-expired-activity-"));
  let now = Date.parse("2026-07-15T06:00:00.000Z");
  const clock = () => new Date(now);
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock });
  const router = createAgentRouter({ repository, clock, leaseDurationMs: 1_000, activityTtlMs: 5_000, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  const nextFrame = (socket, predicate = () => true, timeoutMs = 500) => new Promise((resolve, reject) => {
    const cleanup = () => {
      clearTimeout(timer);
      socket.off("message", onMessage);
      socket.off("error", onError);
    };
    const timer = setTimeout(() => {
      cleanup();
      reject(new Error("timed out waiting for router frame"));
    }, timeoutMs);
    const onError = (error) => {
      cleanup();
      reject(error);
    };
    const onMessage = (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (!predicate(frame)) return;
      cleanup();
      resolve(frame);
    };
    socket.on("message", onMessage);
    socket.once("error", onError);
  });
  const connect = async (websocketUrl) => {
    const socket = new WebSocket(websocketUrl);
    sockets.push(socket);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    return socket;
  };
  try {
    const websocketUrl = await router.listen();
    const worker = await connect(websocketUrl);
    const workerRegistered = nextFrame(worker);
    worker.send(JSON.stringify({ version: "v1", frame_id: "expired-worker-register", type: "register", agent_id: "expired-worker", instance_id: "expired-worker-instance" }));
    const workerLease = await workerRegistered;

    const android = await connect(websocketUrl);
    const androidRegistered = nextFrame(android);
    android.send(JSON.stringify({ ...register, conversation_sync: "v1" }));
    await androidRegistered;

    const initialActive = nextFrame(android, (frame) => frame.type === "agent_activity" && frame.activity_state === "active");
    worker.send(JSON.stringify({ version: "v1", frame_id: "expired-worker-initial-active", type: "activity", agent_id: "expired-worker", instance_id: "expired-worker-instance", lease_id: workerLease.lease_id, activity_state: "active" }));
    await initialActive;

    now += 1_001;
    const noActivity = nextFrame(android, (frame) => frame.type === "agent_activity", 150).then(() => "unexpected", () => "timeout");
    worker.send(JSON.stringify({ version: "v1", frame_id: "expired-worker-active", type: "activity", agent_id: "expired-worker", instance_id: "expired-worker-instance", lease_id: workerLease.lease_id, activity_state: "active" }));
    assert.equal((await nextFrame(worker)).code, "lease_not_active");
    assert.equal(await noActivity, "timeout");
  } finally {
    for (const socket of sockets) socket.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("Android keeps one activity-owned router session across launcher and notification intents", () => {
  const manifest = readFileSync(new URL("../app/src/main/AndroidManifest.xml", import.meta.url), "utf8");
  const activity = readFileSync(new URL("../app/src/main/java/life/meyers/mecha/MainActivity.kt", import.meta.url), "utf8");
  assert.match(manifest, /<activity android:name="\.MainActivity" android:exported="true" android:launchMode="singleTask" android:windowSoftInputMode="adjustResize">/);
  assert.match(activity, /override fun onNewIntent\(intent: Intent\)/);
});

test("Android opts into shared conversations, hydrates by generation, and uses the web image protocol", () => {
  const activity = readFileSync(new URL("../app/src/main/java/life/meyers/mecha/MainActivity.kt", import.meta.url), "utf8");
  const images = readFileSync(new URL("../app/src/main/java/life/meyers/mecha/ImageAttachments.kt", import.meta.url), "utf8");
  assert.match(activity, /\.put\("conversation_sync", "v1"\)/);
  assert.match(activity, /"conversation_message" -> runOnUiThread \{\s*if \(generation == connectionGeneration\) receiveConversationMessage\(frame\)/);
  assert.match(activity, /"agent_activity" -> runOnUiThread \{\s*if \(generation == connectionGeneration\) receiveAgentActivity\(frame\)/);
  assert.match(activity, /addPathSegments\("v1\/conversations"\)/);
  assert.match(activity, /"history:\$activeRouter:\$agentId:\$peer"/);
  assert.match(activity, /generation != connectionGeneration \|\| activeRouter != router \|\| agentId != owner \|\| selectedPeer != peer/);
  assert.match(activity, /reconcileConversationMessages\(historyFor\(peer\), hydrated, highWaterSequence\)/);
  assert.match(activity, /rememberLauncherForActivityResult\(ActivityResultContracts\.GetMultipleContents\(\)\)/);
  assert.match(activity, /imagePicker\.launch\("image\/\*"\)/);
  assert.match(activity, /Icon\(PaperclipIcon, "Attach images", tint = Periwinkle\)/);
  assert.doesNotMatch(activity, /Text\("Attach"\)/);
  assert.doesNotMatch(activity, /Text\("Paste"\)/);
  assert.match(activity, /private fun synchronizedHaloPulse\(label: String\): HaloPulse/);
  assert.match(activity, /private fun PulsingDot\(color: Color, pulse: HaloPulse, size: Dp, haloSpread: Dp\)/);
  assert.match(activity, /Box\(Modifier\.size\(size \+ haloSpread \* 2\), contentAlignment = Alignment\.Center\)/);
  assert.doesNotMatch(activity, /synchronizedPulseAlpha/);
  assert.match(activity, /contentReceiver\(receiveImages\)/);
  assert.match(activity, /acceptedImageUri\(item\.uri\?\.toString\(\), item\.intent\?\.data\?\.toString\(\), item\.text\?\.toString\(\)\)/);
  assert.match(activity, /JSONObject\(\)\.put\("type", "image"\)\.put\("data_url", image\.dataUrl\)\.put\("name", image\.name\)/);
  assert.match(activity, /if \(response\.isSuccessful\) \{[\s\S]*drafts\.clearIfUnchanged\(router, recipient, text\)/);
  assert.match(images, /MAX_IMAGE_ATTACHMENTS = 3/);
  assert.match(images, /MAX_IMAGE_BYTES = 768 \* 1024/);
  assert.match(images, /MAX_IMAGE_INPUT_BYTES = 12 \* 1024 \* 1024/);
  assert.match(images, /resolver\.openInputStream\(uri\)/);
  assert.match(images, /data:image\/jpeg;base64/);
  assert.match(images, /Bitmap\.CompressFormat\.JPEG/);
});

test("Android home renders only router-discovered connected peers", () => {
  const activity = readFileSync(new URL("../app/src/main/java/life/meyers/mecha/MainActivity.kt", import.meta.url), "utf8");
  assert.match(activity, /scheduleAtFixedRate\(60_000, 60_000\)[\s\S]*discoverPeers\(generation\)/);
  assert.match(activity, /val discovered = buildList \{[\s\S]*peerStatusFromJson\(peer, agent, peers\.firstOrNull \{ it\.id == peer \}\)[\s\S]*\}\n\s*runOnUiThread \{ if \(generation == connectionGeneration\) peers = discovered\.sortedBy \{ it\.id \} \}/);
  assert.match(activity, /private fun receiveAgentActivity\(frame: JSONObject\)/);
  assert.match(activity, /normalizedSubagentSummaries\(source\.optJSONArray\("subagents"\)\)/);
  assert.match(activity, /current\?\.subagents\?\.size == incomingCount/);
  assert.match(activity, /PeerActivityIndicators\(peer\)/);
  assert.match(activity, /if \(peer\.subagentCount <= 0\) return/);
  assert.doesNotMatch(activity, /Text\(if \(peer\.active\) "Working" else "Available"/);
  assert.match(activity, /SubagentIndicator\(peer\.subagentCount, peer\.subagents, compact\)/);
  assert.match(activity, /Text\(humanName\(peer\), modifier = Modifier\.weight\(1f, fill = false\)/);
  assert.match(activity, /WorkingIndicator\(compact = true\)/);
  assert.doesNotMatch(activity, /addAll\(knownPeers\(\)\.filter/);
  assert.doesNotMatch(activity, /peers = \(peers \+ peer\)\.distinct\(\)\.sorted\(\)/);
  assert.match(activity, /Text\("Connected agents"/);
});

test("discovery exposes only active sessions with role and no lease metadata", async () => {
  const directory = await mkdtemp(join(tmpdir(), "android-router-discovery-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let socket;
  try {
    const websocketUrl = await router.listen();
    socket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    const registered = new Promise((resolve, reject) => {
      socket.once("message", (payload) => resolve(JSON.parse(payload.toString("utf8"))));
      socket.once("error", reject);
    });
    socket.send(JSON.stringify(register));
    await registered;

    const response = await fetch(websocketUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/agents"));
    assert.equal(response.status, 200);
    assert.equal(response.headers.get("cache-control"), "no-store");
    const snapshot = await response.json();
    assert.match(snapshot.generated_at, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    assert.deepEqual(snapshot.agents.map(({ agent_id, agent_role }) => ({ agent_id, agent_role })), [{
      agent_id: register.agent_id,
      agent_role: "user_interaction",
    }]);
    assert.match(snapshot.agents[0].connected_at, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    assert.match(snapshot.agents[0].lease_expires_at, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    assert.equal(snapshot.agents[0].instance_id, register.instance_id);
    assert.equal(snapshot.agents[0].activity_state, "idle");
    assert.equal(snapshot.agents[0].subagent_count, 0);
    assert.deepEqual(snapshot.agents[0].subagents, []);
    assert.deepEqual(snapshot.agents[0].available_models, []);
    assert.equal(snapshot.agents[0].model_catalog_total_count, 0);
    assert.equal(snapshot.agents[0].model_catalog_truncated, false);
    assert.deepEqual(Object.keys(snapshot.agents[0]).sort(), [
      "activity_state", "agent_id", "agent_role", "available_models", "connected_at", "instance_id", "lease_expires_at",
      "model_catalog_total_count", "model_catalog_truncated", "subagent_count", "subagents",
    ]);
  } finally {
    socket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});
