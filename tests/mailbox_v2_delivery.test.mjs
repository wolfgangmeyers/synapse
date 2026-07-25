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
  createSettledNotificationScheduler,
  mailboxSessionDeliveryState,
} from "../pi-extensions/mecha-messaging.js";
import { QueueLimitError, openMessageRepository } from "../harness/db.js";
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

function fakeTimers() {
  let now = 0;
  const timers = [];
  return {
    timers,
    setTimeout(callback, delay) {
      const timer = { callback, dueAt: now + delay, cleared: false, fired: false };
      timers.push(timer);
      return timer;
    },
    clearTimeout(timer) { timer.cleared = true; },
    advanceTo(target) {
      for (;;) {
        const timer = timers
          .filter((candidate) => !candidate.cleared && !candidate.fired && candidate.dueAt <= target)
          .sort((left, right) => left.dueAt - right.dueAt)[0];
        if (timer === undefined) break;
        now = timer.dueAt;
        timer.fired = true;
        timer.callback();
      }
      now = target;
    },
  };
}

test("settled scheduler sends the no-child schedule at 0, 1, 5, and 10 minutes", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  clock.advanceTo(59999);
  assert.deepEqual(sent, ["sent"]);
  clock.advanceTo(60_000);
  clock.advanceTo(300_000);
  clock.advanceTo(600_000);
  clock.advanceTo(600_001);

  assert.deepEqual(sent, ["sent", "sent", "sent", "sent"]);
});

test("settled scheduler rejects missing timer functions", () => {
  assert.throws(
    () => createSettledNotificationScheduler({ setTimeoutImpl: null, clearTimeoutImpl: () => {} }),
    /settled notification timers are required/,
  );
  assert.throws(
    () => createSettledNotificationScheduler({ setTimeoutImpl: () => {}, clearTimeoutImpl: null }),
    /settled notification timers are required/,
  );
});

test("settled scheduler suppresses a settled parent while a native child is active", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onChildStarted("child-session");
  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  assert.equal(scheduler.activeChildCount(), 1);
  assert.deepEqual(sent, []);

  scheduler.onChildDisposed("child-session");
  scheduler.onParentSettled(() => sent.push("retroactive"));
  assert.deepEqual(sent, []);

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("next-settle"));
  assert.deepEqual(sent, ["next-settle"]);
});

test("settled scheduler disposal cancels pending follow-ups", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  const staleTimers = [...clock.timers];
  scheduler.dispose();
  for (const timer of staleTimers) timer.callback();
  assert.deepEqual(sent, ["sent"]);
  assert.equal(scheduler.activeChildCount(), 0);
});

test("child starts and stale timers suppress pending settled follow-ups", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("first"));
  const staleTimers = [...clock.timers];
  scheduler.onChildStarted("child-session");
  for (const timer of staleTimers) timer.callback();
  clock.advanceTo(600_000);
  assert.deepEqual(sent, ["first"]);

  scheduler.onChildDisposed("child-session");
  scheduler.onParentSettled(() => sent.push("retroactive"));
  assert.deepEqual(sent, ["first"]);
  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("second"));
  assert.deepEqual(sent, ["first", "second"]);
});

test("parent activity before each follow-up cancels that idle generation", () => {
  for (const cutoff of [59_999, 299_999, 599_999]) {
    const clock = fakeTimers();
    const scheduler = createSettledNotificationScheduler({
      setTimeoutImpl: clock.setTimeout,
      clearTimeoutImpl: clock.clearTimeout,
    });
    const sent = [];
    scheduler.onParentActivity();
    scheduler.onParentSettled(() => sent.push("sent"));
    clock.advanceTo(cutoff);
    const staleTimers = [...clock.timers];
    scheduler.onParentActivity();
    for (const timer of staleTimers) timer.callback();
    clock.advanceTo(600_000);
    assert.equal(sent.length, cutoff < 60_000 ? 1 : cutoff < 300_000 ? 2 : 3);
  }
});

test("subject-bearing PR watch wakeups preserve their exact subject through durable Pi delivery", async () => {
  const message = envelope("message-1", { to: "pr-watch", subject: "PR watch wakeup" });
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
  assert.equal(
    sent[0].customMessage.content,
    "Mailbox message from sender\nSubject: PR watch wakeup\n\ndurable body",
  );
  assert.deepEqual(sent[0].customMessage.details, {
    message_id: "message-1",
    from: "sender",
    to: "pr-watch",
    subject: "PR watch wakeup",
  });
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

function settledNotificationFixture(t, {
  recipient,
  sendReply = async () => ({ id: "reply" }),
  createSettledScheduler,
} = {}) {
  const previousRecipient = process.env.MECHA_PI_NOTIFY_ON_SETTLED;
  if (recipient === undefined) delete process.env.MECHA_PI_NOTIFY_ON_SETTLED;
  else process.env.MECHA_PI_NOTIFY_ON_SETTLED = recipient;
  t.after(() => {
    if (previousRecipient === undefined) delete process.env.MECHA_PI_NOTIFY_ON_SETTLED;
    else process.env.MECHA_PI_NOTIFY_ON_SETTLED = previousRecipient;
  });

  const handlers = new Map();
  const childEvents = new Map();
  const tools = new Map();
  const notifications = [];
  const context = {
    sessionManager: { getSessionId: () => "parent-session" },
    ui: { notify: (...args) => notifications.push(args), setStatus() {} },
  };
  const client = {
    connect: () => Promise.resolve(),
    close() {},
    sendReply,
    sendReceipt() {},
  };
  const pi = {
    events: { on(type, handler) { childEvents.set(type, handler); } },
    on(type, handler) { handlers.set(type, handler); },
    registerCommand() {},
    registerTool(tool) { tools.set(tool.name, tool); },
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: () => client,
    ...(createSettledScheduler === undefined ? {} : { createSettledScheduler }),
  });
  handlers.get("session_start")({}, context);
  return {
    childEvents,
    client,
    context,
    handlers,
    notifications,
    tools,
  };
}

test("settled notification sends one exact reply when top-level work settles", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  assert.deepEqual(attempts, []);
  assert.equal(fixture.handlers.has("agent_end"), false);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.deepEqual(attempts, [{
    to: "supervisor",
    subject: "Pi worker settled",
    body: "Worker worker has settled and is idle.",
  }]);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 1);
});

test("settled notification stays disabled without a valid opt-in", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);
});

test("settled notification excludes child sessions", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });
  const childContext = {
    ...fixture.context,
    sessionManager: { getSessionId: () => "child-session" },
  };
  fixture.handlers.get("session_start")({}, childContext);
  fixture.handlers.get("agent_start")({}, childContext);
  fixture.handlers.get("agent_settled")({}, childContext);
  assert.equal(attempts.length, 0);
  fixture.childEvents.get("subagents:child:disposed")({ sessionId: "child-session" });
});

test("native child lifecycle suppresses parent settled notices until a new parent settle", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.childEvents.get("subagents:started")({ id: "child-run" });
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);

  fixture.childEvents.get("subagents:completed")({ id: "child-run" });
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 1);
});

test("native child failure releases parent suppression for the next parent settle", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.childEvents.get("subagents:started")({ id: "child-run" });
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  fixture.childEvents.get("subagents:failed")({ id: "child-run" });
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);

  assert.equal(attempts.length, 1);
});

test("parent session shutdown disposes armed settled follow-ups", async (t) => {
  const calls = [];
  const scheduler = {
    onParentActivity() { calls.push("activity"); },
    onParentSettled(send) { calls.push("settled"); send(); },
    onChildStarted() {},
    onChildDisposed() {},
    dispose() { calls.push("dispose"); },
  };
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    createSettledScheduler: () => scheduler,
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  fixture.handlers.get("session_shutdown")({}, fixture.context);

  assert.deepEqual(calls, ["activity", "settled", "dispose"]);
});

test("child sessions do not schedule recall work", async (t) => {
  const fixture = settledNotificationFixture(t, { recipient: "supervisor" });
  const previousFetch = globalThis.fetch;
  let fetches = 0;
  globalThis.fetch = async () => { fetches += 1; throw new Error("child recall must not fetch policy"); };
  t.after(() => { globalThis.fetch = previousFetch; });

  await fixture.tools.get("mecha_recall").execute("enable-recall", { action: "enable", stores: ["default"] });
  fixture.childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });
  const childContext = { ...fixture.context, sessionManager: { getSessionId: () => "child-session" } };
  fixture.handlers.get("message_start")({}, childContext);
  fixture.handlers.get("agent_settled")({}, childContext);
  await nextTurn();

  assert.equal(fetches, 0);
  fixture.childEvents.get("subagents:child:disposed")({ sessionId: "child-session" });
});

test("an invalid settled recipient leaves notifications disabled", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "not a mailbox agent",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);
});

test("a rejected settled notification reports once and does not block a later run", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply(message) {
      attempts.push(message);
      return attempts.length === 1 ? Promise.reject(new Error("router rejected")) : Promise.resolve({ id: "reply" });
    },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  await nextTurn();
  assert.deepEqual(fixture.notifications, [["Pi settled notification failed: router rejected", "error"]]);
  assert.equal(attempts.length, 1);

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  await nextTurn();
  assert.equal(attempts.length, 2);
  assert.deepEqual(fixture.notifications, [["Pi settled notification failed: router rejected", "error"]]);
});

test("overlapping settled notification sends make independent attempts", async (t) => {
  const attempts = [];
  const deferred = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply(message) {
      attempts.push(message);
      return new Promise((resolve) => deferred.push(resolve));
    },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 2);

  deferred[0]({ id: "first" });
  await nextTurn();
  assert.equal(attempts.length, 2);
  deferred[1]({ id: "second" });
  await nextTurn();
  assert.equal(attempts.length, 2);
  assert.deepEqual(fixture.notifications, []);
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
  assert.equal(sent[0][0].content, "Mailbox message from sender\n\ndurable body");
  assert.deepEqual(sent[0][0].details, {
    message_id: message.id,
    from: "sender",
    to: "worker",
  });
  assert.deepEqual(sent[0][1], { deliverAs: "steer", triggerTurn: true });
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

test("same instance reconnect registration replaces the old live socket without duplicate_identity", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-reconnect-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let firstSocket;
  let secondSocket;
  let frameId = 0;
  const send = (socket, frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `client-${++frameId}`,
    ...frame,
  }));
  try {
    const websocketUrl = await router.listen();
    firstSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      firstSocket.once("open", resolve);
      firstSocket.once("error", reject);
    });
    const firstRegistered = nextRouterFrame(firstSocket);
    send(firstSocket, { type: "register", agent_id: "worker", instance_id: "same-instance" });
    const firstRegistration = await firstRegistered;
    assert.equal(firstRegistration.type, "registered");

    const firstClosed = new Promise((resolve) => firstSocket.once("close", resolve));
    secondSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      secondSocket.once("open", resolve);
      secondSocket.once("error", reject);
    });
    const secondRegistered = nextRouterFrame(secondSocket);
    send(secondSocket, { type: "register", agent_id: "worker", instance_id: "same-instance" });
    const secondRegistration = await secondRegistered;

    assert.equal(secondRegistration.type, "registered");
    assert.notEqual(secondRegistration.lease_id, firstRegistration.lease_id);
    const activeLease = repository.leaseFor("worker");
    assert.equal(activeLease.agent_id, "worker");
    assert.equal(activeLease.instance_id, "same-instance");
    assert.equal(activeLease.lease_id, secondRegistration.lease_id);
    assert.equal(activeLease.expires_at, secondRegistration.expires_at);
    await firstClosed;
  } finally {
    firstSocket?.terminate();
    secondSocket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router batches roughly ten mixed-sender idle notifications into one queued delivery", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  try {
    const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    for (let index = 1; index <= 10; index += 1) {
      const sender = `worker-${index}`;
      const result = router.enqueueMessage(envelope(`idle-${index}`, {
        from: sender,
        to: "supervisor",
        subject: index % 2 === 0 ? "Pi worker settled" : "Status update",
        body: `Worker ${sender} has settled and is idle.`,
      }));
      assert.equal(result.message.id, "idle-1");
      assert.equal(result.message.to, "supervisor");
      assert.equal(result.batched === true, index > 1);
    }

    const queued = repository.queuedFor("supervisor");
    assert.equal(queued.length, 1);
    assert.equal(queued[0].id, "idle-1");
    assert.match(queued[0].body, /^Mailbox message batch\n\n/);
    for (let index = 1; index <= 10; index += 1) {
      assert.match(queued[0].body, new RegExp(`From: worker-${index}`));
      assert.match(queued[0].body, new RegExp(`Worker worker-${index} has settled and is idle\\.`));
      assert.match(queued[0].body, new RegExp(`\\[Reply via mailbox to worker-${index}\\.\\]`));
    }
    assert.match(queued[0].body, /Subject: Status update/);
    assert.match(queued[0].body, /Subject: Pi worker settled/);
    assert.equal((queued[0].body.match(/\n\n---\n\n/g) ?? []).length, 9);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("batched delivery carries per-item reply hints without one global sender footer", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-delivery-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let senderSocket;
  let receiverSocket;
  let frameId = 0;
  const send = (socket, frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `client-${++frameId}`,
    ...frame,
  }));
  try {
    const websocketUrl = await router.listen();
    senderSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      senderSocket.once("open", resolve);
      senderSocket.once("error", reject);
    });
    const senderRegistered = nextRouterFrame(senderSocket);
    send(senderSocket, { type: "register", agent_id: "worker-1", instance_id: "sender-instance" });
    assert.equal((await senderRegistered).type, "registered");

    router.enqueueMessage(envelope("batch-delivery-1", {
      from: "worker-1",
      to: "supervisor",
      subject: "Pi worker settled",
      body: "Worker worker-1 has settled and is idle.",
    }));
    router.enqueueMessage(envelope("batch-delivery-2", {
      from: "worker-2",
      to: "supervisor",
      subject: "Status update",
      body: "Worker worker-2 has settled and is idle.",
    }));

    receiverSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      receiverSocket.once("open", resolve);
      receiverSocket.once("error", reject);
    });
    const receiverFrames = [];
    receiverSocket.on("message", (payload) => receiverFrames.push(JSON.parse(payload.toString("utf8"))));
    send(receiverSocket, { type: "register", agent_id: "supervisor", instance_id: "receiver-instance" });
    await waitFor(() => receiverFrames.some((frame) => frame.type === "message"));
    assert.equal(receiverFrames[0].type, "registered");
    const delivery = receiverFrames.find((frame) => frame.type === "message");

    assert.equal(delivery.type, "message");
    assert.match(delivery.message.body, /^Mailbox message batch\n\n/);
    assert.equal((delivery.message.body.match(/\[Reply via mailbox to worker-1\.\]/g) ?? []).length, 1);
    assert.equal((delivery.message.body.match(/\[Reply via mailbox to worker-2\.\]/g) ?? []).length, 1);
    assert.doesNotMatch(delivery.message.body, /\[Reply via mailbox to worker-1\.\]$/);
  } finally {
    senderSocket?.terminate();
    receiverSocket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router honors the configured maximum batched body character count", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-limit-"));
  const batched = "Mailbox message batch\n\nFrom: worker-a\nSubject: Pi worker settled\n\nfirst\n\n[Reply via mailbox to worker-a.]\n\n---\n\nFrom: worker-b\nSubject: Another subject\n\nsecond\n\n[Reply via mailbox to worker-b.]";
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    maxBatchBodyChars: batched.length - 1,
  });
  try {
    const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    router.enqueueMessage(envelope("batch-limit-1", {
      from: "worker-a",
      to: "supervisor",
      subject: "Pi worker settled",
      body: "first",
    }));
    const result = router.enqueueMessage(envelope("batch-limit-2", {
      from: "worker-b",
      to: "supervisor",
      subject: "Another subject",
      body: "second",
    }));

    assert.equal(result.batched, undefined);
    assert.deepEqual(repository.queuedFor("supervisor").map(({ id }) => id), ["batch-limit-1", "batch-limit-2"]);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router rejects the sixteenth active unbatchable mailbox message by default", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-capacity-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), maxBatchBodyChars: 0 });
  try {
    const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    for (let index = 1; index <= 15; index += 1) {
      router.enqueueMessage(envelope(`message-${index}`, {
        from: "worker-a",
        to: "supervisor",
        subject: `unique-${index}`,
        body: `message ${index}`,
      }));
    }

    assert.equal(repository.queueUsageFor("supervisor").message_count, 15);
    assert.throws(() => router.enqueueMessage(envelope("message-16", {
      from: "worker-a",
      to: "supervisor",
      subject: "unique-16",
      body: "message 16",
    })), QueueLimitError);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
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
