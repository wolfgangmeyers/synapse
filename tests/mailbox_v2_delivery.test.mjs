import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { readFileSync, writeFileSync } from "node:fs";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import WebSocket from "ws";

import {
  createMailboxClient,
  createMechaMessagingExtension,
  createPiMailboxDeliveryCoordinator,
  mailboxSessionDeliveryState,
} from "../pi-extensions/mecha-messaging.js";
import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const nextTurn = () => new Promise((resolve) => setImmediate(resolve));
function envelope(id, overrides = {}) {
  return {
    id,
    from: "sender",
    to: "worker",
    body: "durable body",
    created_at: "2026-07-13T00:00:00.000Z",
    ...overrides,
  };
}

function sessionEntry(messageId) {
  return {
    type: "custom_message",
    customType: "mecha-agent-message",
    content: "message",
    display: true,
    details: { message_id: messageId },
  };
}

async function durableSession(messageId) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-session-"));
  const sessionFile = join(directory, "session.jsonl");
  const entries = [];
  const manager = {
    getBranch: () => entries,
    isPersisted: () => true,
    getSessionFile: () => sessionFile,
    getSessionId: () => "session-1",
  };
  return {
    directory,
    sessionFile,
    entries,
    manager,
    async persist() {
      if (!entries.some((entry) => entry.details?.message_id === messageId)) {
        entries.push(sessionEntry(messageId));
      }
      await writeFile(sessionFile, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
    },
    async close() {
      await rm(directory, { recursive: true, force: true });
    },
  };
}

function nextRouterFrame(socket) {
  return new Promise((resolve, reject) => {
    const onMessage = (payload) => settle(resolve, JSON.parse(payload.toString("utf8")));
    const onError = (error) => settle(reject, error);
    const settle = (callback, value) => {
      socket.off("message", onMessage);
      socket.off("error", onError);
      callback(value);
    };
    socket.once("message", onMessage);
    socket.once("error", onError);
  });
}

function receiptClient() {
  const receipts = [];
  return {
    receipts,
    sendReceipt(type, delivery, reason) {
      receipts.push({ type, messageId: delivery.messageId, attemptNumber: delivery.attemptNumber, reason });
    },
  };
}

test("Pi delivery ACK follows a persisted session entry, not pi.sendMessage", async () => {
  const message = envelope("message-1", { subject: "Durability" });
  const session = await durableSession(message.id);
  const sent = [];
  const client = receiptClient();
  const statuses = [];
  const context = {
    sessionManager: session.manager,
    ui: { setStatus: (...args) => statuses.push(args) },
  };
  const checkpointTimers = [];
  let deferredCheckpoint;
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (customMessage, options) => sent.push({ customMessage, options }) },
    sessionContext: context,
    client,
    defer: (callback) => { deferredCheckpoint = callback; },
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      checkpointTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.equal(sent.length, 1);
  assert.equal(sent[0].customMessage.content, "Mailbox message from sender\n\ndurable body");
  assert.deepEqual(sent[0].options, { deliverAs: "steer", triggerTurn: true });
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
  ]);

  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  // Pi appends the custom entry after extension message_end handlers, before the deferred check.
  session.entries.push(sessionEntry(message.id));
  assert.equal(typeof deferredCheckpoint, "function");
  assert.equal(client.receipts.length, 1);
  deferredCheckpoint();
  assert.equal(client.receipts.length, 1);
  assert.deepEqual(checkpointTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 250, cleared: false },
  ]);

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });
  assert.equal(sent.length, 1);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);

  await session.persist();
  checkpointTimers[0].callback();

  assert.equal(mailboxSessionDeliveryState(session.manager, message.id), "durable");
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "ack", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);
  assert.deepEqual(statuses.slice(-1), [["mecha-mailbox", "mailbox: connected"]]);
  coordinator.close();
  await session.close();
});

test("extension forwards Pi message_end into the durable mailbox checkpoint", async () => {
  const message = envelope("message-extension-wiring");
  const session = await durableSession(message.id);
  const handlers = new Map();
  const childEvents = new Map();
  const sent = [];
  const receipts = [];
  let clientOptions;
  let closeCount = 0;
  const client = {
    connect: () => Promise.resolve(),
    close() { closeCount += 1; },
    sendReply: async () => ({ id: "reply" }),
    sendReceipt(type, delivery) {
      receipts.push({ type, attemptNumber: delivery.attemptNumber });
    },
  };
  const pi = {
    events: { on(type, handler) { childEvents.set(type, handler); } },
    on(type, handler) { handlers.set(type, handler); },
    registerCommand() {},
    registerTool() {},
    sendMessage: (...args) => sent.push(args),
    exec: async () => ({ code: 0, stderr: "" }),
  };
  const context = {
    sessionManager: session.manager,
    ui: { notify() {}, setStatus() {} },
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: (options) => {
      clientOptions = options;
      return client;
    },
  });

  handlers.get("session_start")({}, context);
  clientOptions.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  assert.equal(sent.length, 1);
  assert.deepEqual(receipts, [{ type: "injection_requested", attemptNumber: 1 }]);

  session.entries.push(sessionEntry(message.id));
  await session.persist();
  handlers.get("message_end")({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  await nextTurn();

  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "ack", attemptNumber: 1 },
  ]);
  const childContext = {
    ...context,
    sessionManager: { ...session.manager, getSessionId: () => "child-session" },
  };
  childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });
  handlers.get("session_shutdown")({}, childContext);
  assert.equal(closeCount, 0);
  childEvents.get("subagents:child:disposed")({ sessionId: "child-session" });
  handlers.get("session_shutdown")({}, context);
  assert.equal(closeCount, 1);
  await session.close();
});

test("unverified session history is never reinjected or acknowledged", async () => {
  const message = envelope("message-2");
  const session = await durableSession(message.id);
  session.entries.push(sessionEntry(message.id));
  const sent = [];
  const status = [];
  const client = receiptClient();
  const context = {
    sessionManager: session.manager,
    ui: { setStatus: (...args) => status.push(args) },
  };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  await nextTurn();

  assert.equal(mailboxSessionDeliveryState(session.manager, message.id), "history_unverified");
  assert.deepEqual(sent, []);
  assert.deepEqual(client.receipts, []);
  assert.deepEqual(status, [["mecha-mailbox", "mailbox: waiting for durable session checkpoint"]]);
  coordinator.close();
  await session.close();
});

test("unreadable persisted history is reported without reinjection or ACK", async () => {
  const message = envelope("message-corrupt-history");
  const session = await durableSession(message.id);
  session.entries.push(sessionEntry(message.id));
  await writeFile(session.sessionFile, "not-json\n");
  const sent = [];
  const notices = [];
  const client = receiptClient();
  const context = {
    sessionManager: session.manager,
    ui: {
      setStatus() {},
      notify: (...args) => notices.push(args),
    },
  };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.deepEqual(sent, []);
  assert.deepEqual(client.receipts, []);
  assert.equal(notices.length, 1);
  assert.match(notices[0][0], /session checkpoint unreadable/);
  assert.equal(notices[0][1], "error");
  coordinator.close();
  await session.close();
});

test("replayed durable delivery ACKs without a second Pi message", async () => {
  const message = envelope("message-3");
  const session = await durableSession(message.id);
  await session.persist();
  const sent = [];
  const client = receiptClient();
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.deepEqual(sent, []);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "ack", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);
  coordinator.close();
  await session.close();
});

test("post-injection receipt failure retains the pending message for replay", async () => {
  const message = envelope("message-receipt-failure");
  const session = await durableSession(message.id);
  const sent = [];
  const receipts = [];
  let failReceipt = true;
  const client = {
    sendReceipt(type, delivery) {
      if (type === "injection_requested" && failReceipt) {
        failReceipt = false;
        throw new Error("socket closed");
      }
      receipts.push({ type, attemptNumber: delivery.attemptNumber });
    },
  };
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.equal(sent.length, 1);
  assert.deepEqual(receipts, [{ type: "injection_requested", attemptNumber: 2 }]);
  coordinator.close();
  await session.close();
});

test("client does not NACK a Pi injection when its receipt send fails", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => {
    const parsed = JSON.parse(frame);
    if (parsed.type === "injection_requested") throw new Error("socket write failed");
    socket.sent.push(parsed);
  };
  socket.close = () => { socket.readyState = 3; };
  const context = {
    sessionManager: {
      getBranch: () => [],
      isPersisted: () => false,
      getSessionFile: () => undefined,
      getSessionId: () => "session-1",
    },
    ui: { setStatus() {} },
  };
  const sent = [];
  let coordinator;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: (message, delivery) => coordinator.onEnvelope(message, delivery),
    webSocketFactory: () => socket,
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
  });
  coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({ type: "message", message: envelope("message-client-receipt"), attempt_number: 1 }),
  }));
  await nextTurn();

  assert.equal(sent.length, 1);
  assert.deepEqual(socket.sent.map(({ type }) => type), ["register"]);
  coordinator.close();
  client.close();
});

test("Pi injection failure NACKs without acknowledging the delivery", async () => {
  const message = envelope("message-injection-failure");
  const session = await durableSession(message.id);
  const client = receiptClient();
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: () => { throw new Error("Pi rejected message"); } },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.deepEqual(client.receipts, [
    { type: "nack", messageId: message.id, attemptNumber: 1, reason: "injection_failed" },
  ]);
  coordinator.close();
  await session.close();
});

test("failed durable ACK is retried from persisted session history without reinjection", async () => {
  const message = envelope("message-ack-failure");
  const session = await durableSession(message.id);
  await session.persist();
  const sent = [];
  const receipts = [];
  let failAck = true;
  const client = {
    sendReceipt(type, delivery) {
      if (type === "ack" && failAck) {
        failAck = false;
        throw new Error("socket closed");
      }
      receipts.push({ type, attemptNumber: delivery.attemptNumber });
    },
  };
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.deepEqual(sent, []);
  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "injection_requested", attemptNumber: 2 },
    { type: "ack", attemptNumber: 2 },
  ]);
  coordinator.close();
  await session.close();
});

test("replayed in-flight delivery does not create a second Pi injection", async () => {
  const message = envelope("message-4");
  const session = await durableSession(message.id);
  const sent = [];
  const client = receiptClient();
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });
  await session.persist();
  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  await nextTurn();

  assert.equal(sent.length, 1);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "ack", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);
  coordinator.close();
  await session.close();
});

test("mailbox client reconnects after close and stops retrying after shutdown", async () => {
  const sockets = [];
  const reconnectTimers = [];
  const kickstarts = [];
  class FakeSocket extends EventTarget {
    readyState = 0;
    sent = [];

    send(frame) {
      this.sent.push(JSON.parse(frame));
    }

    close() {
      this.readyState = 3;
      this.dispatchEvent(new Event("close"));
    }
  }
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    kickstart: () => { kickstarts.push("kickstart"); },
    webSocketFactory: () => {
      const socket = new FakeSocket();
      sockets.push(socket);
      return socket;
    },
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
    reconnectInitialDelayMs: 2,
    reconnectMaxDelayMs: 4,
    random: () => 1,
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      reconnectTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  const connected = client.connect();
  sockets[0].readyState = 1;
  sockets[0].dispatchEvent(new Event("open"));
  sockets[0].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;

  sockets[0].close();
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
  ]);
  assert.deepEqual(kickstarts, []);
  reconnectTimers[0].callback();
  assert.equal(sockets.length, 2);
  sockets[1].readyState = 1;
  sockets[1].dispatchEvent(new Event("open"));
  sockets[1].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-2",
    }),
  }));
  sockets[1].close();
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
    { delay: 2, cleared: false },
  ]);
  assert.deepEqual(kickstarts, []);
  client.close();
  assert.equal(reconnectTimers[1].cleared, true);
  reconnectTimers[1].callback();
  assert.equal(sockets.length, 2);
});

test("connection refusal requests launchd once per scheduled retry", () => {
  const sockets = [];
  const createSocket = () => {
    const socket = new EventTarget();
    socket.readyState = 0;
    socket.close = () => { socket.readyState = 3; };
    sockets.push(socket);
    return socket;
  };
  const kickstarts = [];
  const reconnectTimers = [];
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    kickstart: () => { kickstarts.push("kickstart"); },
    webSocketFactory: createSocket,
    reconnectInitialDelayMs: 2,
    reconnectMaxDelayMs: 4,
    random: () => 1,
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      reconnectTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  void client.connect().catch(() => {});
  const refusal = new Event("error");
  Object.defineProperty(refusal, "error", {
    value: Object.assign(new Error("connection refused"), { code: "ECONNREFUSED" }),
  });
  sockets[0].dispatchEvent(refusal);

  assert.deepEqual(kickstarts, ["kickstart"]);
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
  ]);
  reconnectTimers[0].callback();
  assert.equal(sockets.length, 2);
  const secondRefusal = new Event("error");
  Object.defineProperty(secondRefusal, "error", {
    value: Object.assign(new Error("connection refused"), { code: "ECONNREFUSED" }),
  });
  sockets[1].dispatchEvent(secondRefusal);
  assert.deepEqual(kickstarts, ["kickstart", "kickstart"]);
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
    { delay: 4, cleared: false },
  ]);
  client.close();
  assert.equal(reconnectTimers[1].cleared, true);
});

test("router and SQLite recover timed-out delivery attempts and complete one-shot reminders", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-router-"));
  const databasePath = join(directory, "router.sqlite");
  let now = Date.parse("2026-07-13T00:00:00.000Z");
  let repository;
  let router;
  const openRouter = () => {
    repository = openMessageRepository({
      databasePath,
      clock: () => new Date(now),
    });
    router = createAgentRouter({
      repository,
      clock: () => new Date(now),
      leaseDurationMs: 10_000,
      deliveryLeaseDurationMs: 100,
      timeoutSweepIntervalMs: 60_000,
    });
  };
  openRouter();
  let socket;
  let instanceId = "worker-instance";
  let frameId = 0;
  const send = (frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `client-${++frameId}`,
    ...frame,
  }));
  const receipt = (type, messageId, attemptNumber, leaseId) => send({
    type,
    agent_id: "worker",
    instance_id: instanceId,
    lease_id: leaseId,
    message_id: messageId,
    attempt_number: attemptNumber,
  });
  const drainAck = async () => {
    const pong = nextRouterFrame(socket);
    send({ type: "ping" });
    assert.equal((await pong).type, "pong");
  };
  try {
    const websocketUrl = await router.listen();
    assert.match(websocketUrl, /^ws:\/\/127\.0\.0\.1:/);
    socket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });

    const registered = nextRouterFrame(socket);
    send({ type: "register", agent_id: "worker", instance_id: instanceId });
    const registration = await registered;
    assert.equal(registration.type, "registered");
    assert.match(registration.frame_id, /^[A-Za-z0-9]{21}$/);
    const leaseId = registration.lease_id;
    assert.match(leaseId, /^[A-Za-z0-9]{21}$/);
    assert.notEqual(registration.frame_id, leaseId);

    const firstOffer = nextRouterFrame(socket);
    router.enqueueMessage(envelope("delivery-1"));
    const firstDelivery = await firstOffer;
    assert.deepEqual(
      { type: firstDelivery.type, messageId: firstDelivery.message.id, attempt: firstDelivery.attempt_number },
      { type: "message", messageId: "delivery-1", attempt: 1 },
    );
    receipt("injection_requested", "delivery-1", firstDelivery.attempt_number, leaseId);
    await drainAck();
    router.enqueueMessage(envelope("delivery-2"));

    repository.createReminder({
      id: "reminder-1",
      target: "worker",
      creator: "tester",
      body: "reminder body",
      created_at: new Date(now).toISOString(),
      next_due_at: new Date(now + 50).toISOString(),
      interval_ms: null,
    });
    socket.terminate();
    await router.close();
    repository.close();
    openRouter();
    const restartedUrl = await router.listen();
    socket = new WebSocket(restartedUrl);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    instanceId = "worker-instance-restarted";
    const restartedRegistration = nextRouterFrame(socket);
    send({ type: "register", agent_id: "worker", instance_id: instanceId });
    const restartedLease = await restartedRegistration;
    assert.equal(restartedLease.type, "registered");
    const recoveredLeaseId = restartedLease.lease_id;

    now += 101;
    const replayOffer = nextRouterFrame(socket);
    assert.deepEqual(router.sweepDeliveryTimeouts().map((outcome) => outcome.state), ["queued"]);
    const replay = await replayOffer;
    assert.deepEqual(
      { messageId: replay.message.id, attempt: replay.attempt_number },
      { messageId: "delivery-1", attempt: 2 },
    );
    receipt("injection_requested", "delivery-1", replay.attempt_number, recoveredLeaseId);
    const secondOffer = nextRouterFrame(socket);
    receipt("ack", "delivery-1", replay.attempt_number, recoveredLeaseId);
    const secondDelivery = await secondOffer;
    await drainAck();
    assert.equal(repository.messageFor("delivery-1"), undefined);
    assert.deepEqual(
      { messageId: secondDelivery.message.id, attempt: secondDelivery.attempt_number },
      { messageId: "delivery-2", attempt: 1 },
    );
    receipt("injection_requested", "delivery-2", secondDelivery.attempt_number, recoveredLeaseId);
    receipt("ack", "delivery-2", secondDelivery.attempt_number, recoveredLeaseId);
    await drainAck();
    assert.equal(repository.messageFor("delivery-2"), undefined);

    const reminderOffer = nextRouterFrame(socket);
    const fired = router.sweepDueReminders();
    assert.equal(fired.length, 1);
    const reminderDelivery = await reminderOffer;
    assert.equal(reminderDelivery.message.id, fired[0].message.id);
    receipt("injection_requested", reminderDelivery.message.id, reminderDelivery.attempt_number, recoveredLeaseId);
    receipt("ack", reminderDelivery.message.id, reminderDelivery.attempt_number, recoveredLeaseId);
    await drainAck();
    assert.equal(repository.reminderFor("reminder-1"), undefined);
  } finally {
    socket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});
