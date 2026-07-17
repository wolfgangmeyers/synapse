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
  launchSentinelChild,
  mailboxSessionDeliveryState,
  recoverSentinelExits,
} from "../pi-extensions/mecha-messaging.js";
import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const nextTurn = () => new Promise((resolve) => setImmediate(resolve));
const SENTINEL_POLICIES = {
  managed: {
    receiver: "worker",
    senders: ["supervisor"],
    models: ["gpt-5.6-terra"],
    execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: [] },
  },
};

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

test("Sentinel parent passes the opaque prompt to an isolated runner and receives only body-free status", async () => {
  const child = new EventEmitter();
  child.pid = 42;
  child.stdin = { written: "", closed: false, write(value, callback) { this.written += value; callback?.(); }, end(callback) { this.closed = true; callback?.(); } };
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.kill = () => {};
  const attempts = [];
  const launched = await launchSentinelChild({
    message: { body: "private Sentinel payload", from: "supervisor", to: "receiver", sentinel: { model: "gpt-5.6-terra" } },
    routerUrl: "http://127.0.0.1:8765",
    authPath: "operator-auth",
    markResultSendAttempt: async () => attempts.push("marker"),
    spawnImpl: (command, args, options) => {
      assert.equal(command, process.execPath);
      assert.match(args[0], /sentinel-child\.js$/);
      assert.deepEqual(options.stdio, ["pipe", "pipe", "pipe"]);
      assert.deepEqual(Object.keys(options.env), ["PATH"]);
      assert.doesNotMatch(JSON.stringify(args), /private Sentinel payload/);
      return child;
    },
  });
  assert.equal(launched.childIdentity, "child-42");
  assert.equal(JSON.parse(child.stdin.written).prompt, "private Sentinel payload");
  assert.equal(JSON.stringify(launched).includes("private Sentinel payload"), false);
  child.stdout.emit("data", Buffer.from('{"type":"result_send_attempt"}\n'));
  await nextTurn();
  assert.deepEqual(attempts, ["marker"]);
  assert.match(child.stdin.written, /result_send_authorized/);
  assert.equal(child.stdin.closed, true);
  child.stdout.emit("data", Buffer.from('{"type":"completed","state":"succeeded","code":"result_sent","event_counts":{},"stderr_bytes":0,"malformed_frames":0}\n'));
  assert.deepEqual(await launched.completion, { state: "succeeded", code: "result_sent" });
});

test("Sentinel parent rejects runner output that includes model text", async () => {
  const child = new EventEmitter();
  child.pid = 43;
  child.stdin = { write() {} };
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.kill = () => {};
  const diagnostics = [];
  const launched = await launchSentinelChild({
    message: { body: "private Sentinel payload", from: "supervisor", to: "receiver", sentinel: { model: "gpt-5.6-terra" } },
    authPath: "operator-auth", onDiagnostic: (diagnostic) => diagnostics.push(diagnostic), spawnImpl: () => child,
  });
  child.stdout.emit("data", Buffer.from('{"type":"completed","state":"succeeded","code":"result_sent","body":"private model text"}\n'));
  assert.deepEqual(await launched.completion, { state: "failed", code: "runner_malformed_status" });
  assert.equal(JSON.stringify(diagnostics).includes("private model text"), false);
});

test("Sentinel parent handles an asynchronous runner stdin EPIPE without body output", async () => {
  const child = new EventEmitter();
  child.pid = 44;
  const stdin = new EventEmitter();
  stdin.written = "";
  stdin.write = (value, callback) => { stdin.written += value; callback?.(); };
  stdin.end = () => queueMicrotask(() => stdin.emit("error", Object.assign(new Error("EPIPE"), { code: "EPIPE" })));
  child.stdin = stdin;
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.kill = () => {};
  const diagnostics = [];
  const launched = await launchSentinelChild({
    message: { body: "private Sentinel payload", from: "supervisor", to: "receiver", sentinel: { model: "gpt-5.6-terra" } },
    authPath: "operator-auth", markResultSendAttempt: async () => "relay-token", onDiagnostic: (diagnostic) => diagnostics.push(diagnostic), spawnImpl: () => child,
  });
  child.stdout.emit("data", Buffer.from('{"type":"result_send_attempt"}\n'));
  assert.deepEqual(await launched.completion, { state: "failed", code: "runner_stdin_error" });
  assert.equal(JSON.stringify(diagnostics).includes("private Sentinel payload"), false);
});

test("Sentinel parent does not accept result_sent before async authorization EPIPE", async () => {
  const child = new EventEmitter();
  child.pid = 45;
  const stdin = new EventEmitter();
  stdin.write = (_value, callback) => queueMicrotask(() => callback?.());
  stdin.end = (callback) => queueMicrotask(() => callback?.(Object.assign(new Error("EPIPE"), { code: "EPIPE" })));
  child.stdin = stdin;
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.kill = () => {};
  const diagnostics = [];
  const launched = await launchSentinelChild({
    message: { body: "private Sentinel payload", from: "supervisor", to: "receiver", sentinel: { model: "gpt-5.6-terra" } },
    authPath: "operator-auth", markResultSendAttempt: async () => "relay-token", onDiagnostic: (diagnostic) => diagnostics.push(diagnostic), spawnImpl: () => child,
  });
  child.stdout.emit("data", Buffer.from('{"type":"result_send_attempt"}\n{"type":"completed","state":"succeeded","code":"result_sent"}\n'));
  assert.deepEqual(await launched.completion, { state: "failed", code: "runner_stdin_error" });
  assert.equal(JSON.stringify(diagnostics).includes("private Sentinel payload"), false);
});

test("Sentinel delivery stays out of the parent session and launches one private child", async () => {
  const message = envelope("sentinel-message", {
    body: "private Sentinel payload",
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" },
  });
  const sent = [];
  const lifecycle = [];
  const exits = [];
  let childExit;
  let claimCount = 0;
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: {
      sendReceipt() { throw new Error("Sentinel must not use ordinary delivery receipts"); },
      async sendSentinelLifecycle(type, fields) {
        lifecycle.push({ type, fields });
        if (type === "sentinel_claim") {
          claimCount += 1;
          return { state: claimCount === 1 ? "claimed" : "launched" };
        }
        return { state: type === "sentinel_launching" ? "launching" : "launched" };
      },
      queueSentinelExit(exit) {
        exits.push(exit);
        exit.onConfirmed?.({ state: "exited", outcome: "failed", transition_sequence: 4 });
      },
    },
    sentinelReceiverPolicies: SENTINEL_POLICIES,
    launchChild: async ({ message: privateMessage }) => {
      assert.equal(privateMessage.body, "private Sentinel payload");
      return { childIdentity: "child-1", onExit: (callback) => { childExit = callback; } };
    },
  });
  const delivery = { messageId: message.id, attemptNumber: 1 };

  await coordinator.onEnvelope(message, delivery);
  await coordinator.onEnvelope(message, delivery);
  assert.equal(sent.every(([status]) => status.customType === "mecha-sentinel-status"), true);
  assert.doesNotMatch(JSON.stringify(sent), /private Sentinel payload/);
  assert.deepEqual(sent.map(([status]) => status.details.state), ["claimed", "launching", "spawned_pending", "launched", "launched"]);
  assert.deepEqual(sent[2][0].details, {
    message_id: message.id, state: "spawned_pending", occurrence_id: message.id, attempt_id: "1", child_identity: "child-1",
  });
  assert.doesNotMatch(JSON.stringify(lifecycle), /private Sentinel payload/);
  assert.deepEqual(lifecycle.map(({ type }) => type), ["sentinel_claim", "sentinel_launching", "sentinel_launched", "sentinel_claim"]);
  assert.equal(lifecycle[2].fields.childIdentity, "child-1");
  childExit(7);
  assert.deepEqual(exits.map(({ occurrenceId, attemptId, exitCode }) => ({ occurrenceId, attemptId, exitCode })), [{ occurrenceId: message.id, attemptId: "1", exitCode: 7 }]);
  const confirmedExit = sent.at(-1)[0];
  assert.equal(confirmedExit.details.state, "exited");
  assert.equal(confirmedExit.details.transition_sequence, 4);
  assert.equal(confirmedExit.details.outcome, "failed");
  assert.doesNotMatch(JSON.stringify(confirmedExit), /private Sentinel payload/);
});

test("a child exit before launch acknowledgement is reported once launch records", async () => {
  let exitChild;
  const exits = [];
  const message = envelope("fast-exit", {
    from: "supervisor",
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" },
  });
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: {
      sendReceipt() {},
      async sendSentinelLifecycle(type) {
        if (type === "sentinel_launched") exitChild(9);
        return { state: type === "sentinel_claim" ? "claimed" : type === "sentinel_launching" ? "launching" : "launched" };
      },
      queueSentinelExit: (exit) => exits.push(exit),
    },
    sentinelReceiverPolicies: SENTINEL_POLICIES,
    launchChild: async () => ({ childIdentity: "child-fast", onExit: (callback) => { exitChild = callback; } }),
  });
  await coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  assert.deepEqual(exits.map(({ occurrenceId, attemptId, exitCode }) => ({ occurrenceId, attemptId, exitCode })), [{ occurrenceId: message.id, attemptId: "1", exitCode: 9 }]);
});

test("body-free spawned status is durable before Sentinel launch acknowledgement", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-parent-status-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const sessionFile = join(directory, "parent.jsonl");
  const entries = [];
  const message = envelope("durable-sentinel", {
    from: "supervisor",
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" },
  });
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (status) => {
      entries.push({ type: "custom_message", ...status });
      writeFileSync(sessionFile, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
    } },
    sessionContext: { sessionManager: {
      getSessionId: () => "parent", getBranch: () => entries, isPersisted: () => true, getSessionFile: () => sessionFile,
    } },
    client: {
      sendReceipt() {},
      async sendSentinelLifecycle(type) {
        if (type === "sentinel_launched") {
          const persisted = readFileSync(sessionFile, "utf8");
          assert.match(persisted, /spawned_pending/);
          assert.match(persisted, /"transition_sequence":2/);
          assert.doesNotMatch(persisted, /durable body/);
        }
        return {
          state: type === "sentinel_claim" ? "claimed" : type === "sentinel_launching" ? "launching" : "launched",
          transition_sequence: type === "sentinel_claim" ? 1 : type === "sentinel_launching" ? 2 : 3,
        };
      },
    },
    sentinelReceiverPolicies: SENTINEL_POLICIES,
    launchChild: async () => ({ childIdentity: "child-durable", onExit() {} }),
  });
  await coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
});

test("router-confirmed Sentinel exit is durably persisted for parent restart", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-confirmed-exit-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const sessionFile = join(directory, "parent.jsonl");
  const entries = [];
  let exitChild;
  const message = envelope("confirmed-exit", {
    from: "supervisor",
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" },
  });
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (status) => {
      entries.push({ type: "custom_message", ...status });
      writeFileSync(sessionFile, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
    } },
    sessionContext: { sessionManager: {
      getSessionId: () => "parent", getBranch: () => entries, isPersisted: () => true, getSessionFile: () => sessionFile,
    } },
    client: {
      sendReceipt() {},
      async sendSentinelLifecycle(type) {
        return { state: type === "sentinel_claim" ? "claimed" : type === "sentinel_launching" ? "launching" : "launched", transition_sequence: 3 };
      },
      queueSentinelExit(exit) {
        exit.onConfirmed({ state: "exited", outcome: "failed", transition_sequence: 4 });
      },
    },
    sentinelReceiverPolicies: SENTINEL_POLICIES,
    launchChild: async () => ({ childIdentity: "child-confirmed", onExit: (callback) => { exitChild = callback; } }),
  });
  await coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  exitChild(7);
  const restartedEntries = (await readFile(sessionFile, "utf8")).trim().split("\n").map(JSON.parse);
  const confirmed = restartedEntries.at(-1);
  assert.deepEqual(confirmed.details, {
    message_id: message.id, state: "exited", outcome: "failed", occurrence_id: message.id,
    attempt_id: "1", transition_sequence: 4, exit_code: 7,
  });
  assert.doesNotMatch(JSON.stringify(restartedEntries), /durable body/);
});

test("durable body-free Sentinel exit status is recovered after parent restart", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-parent-recovery-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const entry = {
    type: "custom_message", customType: "mecha-sentinel-status", content: "[Sentinel sentinel-message] exited_pending", display: true,
    details: { message_id: "sentinel-message", state: "exited_pending", occurrence_id: "sentinel-message", attempt_id: "1", exit_code: 7 },
  };
  const sessionFile = join(directory, "parent.jsonl");
  await writeFile(sessionFile, `${JSON.stringify(entry)}\n`);
  const restartedEntries = (await readFile(sessionFile, "utf8")).trim().split("\n").map(JSON.parse);
  const recovered = [];
  recoverSentinelExits({ getBranch: () => restartedEntries }, {
    queueSentinelExit: (exit) => recovered.push(exit),
  });
  assert.deepEqual(recovered, [{ messageId: "sentinel-message", occurrenceId: "sentinel-message", attemptId: "1", exitCode: 7 }]);
  assert.doesNotMatch(await readFile(sessionFile, "utf8"), /private Sentinel payload/);
});

test("Sentinel extension rejects a mismatched fixed profile before child launch", async () => {
  let launched = false;
  const statuses = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (status) => statuses.push(status) },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt() {}, sendSentinelLifecycle() { throw new Error("must not claim"); } },
    sentinelReceiverPolicies: SENTINEL_POLICIES,
    launchChild: async () => { launched = true; },
  });
  await coordinator.onEnvelope(envelope("unauthorized", {
    from: "supervisor",
    sentinel: { receiver_policy: "managed", model: "unapproved", origin_sender: "supervisor" },
  }), { messageId: "unauthorized", attemptNumber: 1 });
  assert.equal(launched, false);
  assert.equal(statuses[0].details.state, "rejected_profile");
});

test("Sentinel extension rejects a wrong receiver before child launch or claim", async () => {
  let launched = false;
  let claimed = false;
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt() {}, sendSentinelLifecycle() { claimed = true; } },
    sentinelReceiverPolicies: SENTINEL_POLICIES,
    launchChild: async () => { launched = true; },
  });
  await coordinator.onEnvelope(envelope("wrong-receiver", {
    to: "other-worker", from: "supervisor",
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" },
  }), { messageId: "wrong-receiver", attemptNumber: 1 });
  assert.equal(launched, false);
  assert.equal(claimed, false);
});

test("Sentinel extension rejects a sender and supervisor binding mismatch before child launch or claim", async () => {
  let launched = false;
  let claimed = false;
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt() {}, sendSentinelLifecycle() { claimed = true; } },
    sentinelReceiverPolicies: SENTINEL_POLICIES,
    launchChild: async () => { launched = true; },
  });
  await coordinator.onEnvelope(envelope("wrong-supervisor", {
    from: "supervisor",
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "attacker" },
  }), { messageId: "wrong-supervisor", attemptNumber: 1 });
  assert.equal(launched, false);
  assert.equal(claimed, false);
});

test("Sentinel extension rejects a tool-profile mismatch before child launch or claim", async () => {
  let launched = false;
  let claimed = false;
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt() {}, sendSentinelLifecycle() { claimed = true; } },
    sentinelReceiverPolicies: {
      managed: { ...SENTINEL_POLICIES.managed, execution: { supervisor: "supervisor", tools: ["attacker_tool"] } },
    },
    launchChild: async () => { launched = true; },
  });
  await coordinator.onEnvelope(envelope("wrong-tool-profile", {
    from: "supervisor",
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" },
  }), { messageId: "wrong-tool-profile", attemptNumber: 1 });
  assert.equal(launched, false);
  assert.equal(claimed, false);
});

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

test("mailbox client sends Sentinel lifecycle frames with its active lease and retries child exit after reconnect", async () => {
  const sockets = [];
  const reconnectTimers = [];
  class FakeSocket extends EventTarget {
    readyState = 0;
    sent = [];
    send(frame) { this.sent.push(JSON.parse(frame)); }
    close() { this.readyState = 3; this.dispatchEvent(new Event("close")); }
  }
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    webSocketFactory: () => { const socket = new FakeSocket(); sockets.push(socket); return socket; },
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
    setTimeoutImpl: (callback) => { const timer = { callback }; reconnectTimers.push(timer); return timer; },
    clearTimeoutImpl() {},
  });
  const connected = client.connect();
  sockets[0].readyState = 1;
  sockets[0].dispatchEvent(new Event("open"));
  sockets[0].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({ type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1" }),
  }));
  await connected;

  const claim = client.sendSentinelLifecycle("sentinel_claim", {
    delivery: { messageId: "message", attemptNumber: 1 }, occurrenceId: "message", attemptId: "1",
  });
  const claimFrame = sockets[0].sent.at(-1);
  assert.deepEqual({ type: claimFrame.type, lease: claimFrame.lease_id, message: claimFrame.message_id }, {
    type: "sentinel_claim", lease: "lease-1", message: "message",
  });
  sockets[0].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({ type: "sentinel_state", request_id: claimFrame.frame_id, occurrence_id: "message", state: "claimed", transition_sequence: 1 }),
  }));
  assert.equal((await claim).state, "claimed");

  client.queueSentinelExit({ occurrenceId: "message", attemptId: "1", exitCode: 7 });
  assert.equal(sockets[0].sent.at(-1).type, "sentinel_exited");
  sockets[0].close();
  reconnectTimers[0].callback();
  sockets[1].readyState = 1;
  sockets[1].dispatchEvent(new Event("open"));
  sockets[1].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({ type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-2" }),
  }));
  const retriedExit = sockets[1].sent.at(-1);
  assert.deepEqual({ type: retriedExit.type, lease: retriedExit.lease_id, exitCode: retriedExit.exit_code }, {
    type: "sentinel_exited", lease: "lease-2", exitCode: 7,
  });
  sockets[1].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({ type: "sentinel_state", request_id: retriedExit.frame_id, occurrence_id: "message", state: "exited", transition_sequence: 4, exit_code: 7, outcome: "failed" }),
  }));
  client.close();
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
