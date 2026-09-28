import assert from "node:assert/strict";
import test from "node:test";

import {
  captureNativeSentinelSpawner,
  createPiMailboxDeliveryCoordinator,
  mailboxSessionDeliveryState,
  spawnNativeSentinel,
  transientChildAgentId,
} from "../pi-extensions/mecha-messaging/index.ts";

const sentinelMessage = {
  id: "sentinel-message",
  from: "unknown-sender",
  to: "target-worker",
  body: "private scheduled prompt",
  created_at: "2030-01-01T00:00:00.000Z",
  sentinel: { model: "caller-selected-model" },
};

test("child Mailbox senders are transient and distinct from the target parent", () => {
  assert.equal(transientChildAgentId("child-session", () => "first"), "subagent-first");
  assert.notEqual(transientChildAgentId("child-session", () => "first"), transientChildAgentId("child-session", () => "second"));
});

test("native Sentinel adapter uses the owner-scoped Pi service accessor without a reduced profile", async () => {
  const calls = [];
  const service = { spawn: (...args) => { calls.push(args); return "native-child"; } };
  const id = await spawnNativeSentinel(sentinelMessage, () => service);
  assert.equal(id, "native-child");
  assert.deepEqual(calls, [["general-purpose", "private scheduled prompt", {
    description: "Scheduled Sentinel work", model: "caller-selected-model", inheritContext: true,
  }]]);
});

test("owner-scoped Sentinel lookup stays on the parent when a child service is registered", async () => {
  const parentRecords = [];
  const services = new Map();
  const parentService = {
    spawn: () => {
      const id = `parent-child-${parentRecords.length + 1}`;
      parentRecords.push({ id, status: "completed" });
      return id;
    },
    getRecord: (id) => parentRecords.find((record) => record.id === id),
  };
  services.set("parent-session", parentService);
  const spawn = captureNativeSentinelSpawner(() => services.get("parent-session"));
  services.set("child-session", { spawn: () => "child-service-record" });
  const first = await spawn({ ...sentinelMessage, id: "first" });
  services.delete("child-session");
  const second = await spawn({ ...sentinelMessage, id: "second" });
  assert.deepEqual([first, second], ["parent-child-1", "parent-child-2"]);
  assert.deepEqual(parentRecords, [
    { id: "parent-child-1", status: "completed" },
    { id: "parent-child-2", status: "completed" },
  ]);
});

test("target delivery hands off once, records body-free status, and uses ordinary acknowledgement", async () => {
  const sent = [];
  const receipts = [];
  const calls = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (message, options) => sent.push({ message, options }) },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt: (type, delivery, reason) => receipts.push({ type, delivery, reason }) },
    spawnSubagent: async (message) => { calls.push(message); return "native-child"; },
  });
  const delivery = { messageId: sentinelMessage.id, attemptNumber: 1 };
  await Promise.all([coordinator.onEnvelope(sentinelMessage, delivery), coordinator.onEnvelope(sentinelMessage, delivery)]);

  assert.deepEqual(calls, [sentinelMessage]);
  assert.deepEqual(receipts.map(({ type }) => type), ["injection_requested", "ack"]);
  assert.equal(sent.length, 1);
  assert.deepEqual(sent[0].message.details, {
    message_id: sentinelMessage.id, subagent_id: "native-child", state: "handed_off",
  });
  assert.equal(sent[0].message.display, false);
  assert.doesNotMatch(JSON.stringify(sent), /private scheduled prompt/);
  assert.doesNotMatch(JSON.stringify(sent), /caller-selected-model|Reply via mailbox to/);
  assert.equal(sent[0].message.customType, "mecha-sentinel-status");
  coordinator.close();
});

test("a pre-handoff native spawn failure uses the ordinary delivery retry path", async () => {
  const receipts = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() { throw new Error("must not record a handoff"); } },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt: (type, delivery, reason) => receipts.push({ type, delivery, reason }) },
    spawnSubagent: async () => { throw new Error("service unavailable"); },
  });
  await coordinator.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });
  assert.deepEqual(receipts.map(({ type, reason }) => ({ type, reason })), [{ type: "nack", reason: "injection_failed" }]);
});

test("native Sentinel rejects missing or invalid spawn inputs", async () => {
  const service = { spawn: () => "native-child" };
  const cases = [
    { name: "missing service", message: sentinelMessage, resolveService: async () => undefined },
    { name: "missing spawn function", message: sentinelMessage, resolveService: async () => ({}) },
    { name: "missing body", message: { ...sentinelMessage, body: undefined }, resolveService: async () => service },
    { name: "missing Sentinel metadata", message: { ...sentinelMessage, sentinel: undefined }, resolveService: async () => service },
  ];

  for (const { name, message, resolveService } of cases) {
    await assert.rejects(() => spawnNativeSentinel(message, resolveService), /service is unavailable/, name);
  }
});

test("invalid native child IDs NACK without recording a handoff", async () => {
  for (const childId of [undefined, ""]) {
    const receipts = [];
    const coordinator = createPiMailboxDeliveryCoordinator({
      pi: { sendMessage() { throw new Error("must not record a handoff"); } },
      sessionContext: { sessionManager: { getSessionId: () => "parent" } },
      client: { sendReceipt: (type, delivery, reason) => receipts.push({ type, delivery, reason }) },
      spawnSubagent: async () => childId,
    });

    await coordinator.onEnvelope({ ...sentinelMessage, id: `invalid-child-${String(childId)}` }, {
      messageId: `invalid-child-${String(childId)}`,
      attemptNumber: 1,
    });
    assert.deepEqual(receipts.map(({ type, reason }) => ({ type, reason })), [{ type: "nack", reason: "injection_failed" }]);
    coordinator.close();
  }
});

test("Sentinel spawn failure remains contained when its NACK cannot be sent", async () => {
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() { throw new Error("must not record a handoff"); } },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt() { throw new Error("socket closed"); } },
    spawnSubagent: async () => { throw new Error("service unavailable"); },
  });

  await assert.doesNotReject(() => coordinator.onEnvelope(sentinelMessage, {
    messageId: sentinelMessage.id, attemptNumber: 1,
  }));
  coordinator.close();
});

test("Sentinel status failure does not prevent ordinary acknowledgement", async () => {
  const receipts = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() { throw new Error("status write failed"); } },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt: (type) => receipts.push(type) },
    spawnSubagent: async () => "native-child",
  });

  await coordinator.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });

  assert.deepEqual(receipts, ["injection_requested", "ack"]);
  coordinator.close();
});

test("Sentinel receipt failure is contained after the child is handed off", async () => {
  const sent = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt() { throw new Error("socket closed"); } },
    spawnSubagent: async () => "native-child",
  });

  await assert.doesNotReject(() => coordinator.onEnvelope(sentinelMessage, {
    messageId: sentinelMessage.id, attemptNumber: 1,
  }));

  assert.equal(sent.length, 1);
  assert.equal(sent[0][0].customType, "mecha-sentinel-status");
  coordinator.close();
});

test("a settled Sentinel handoff does not suppress a later retry", async () => {
  const calls = [];
  const receipts = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt: (type) => receipts.push(type) },
    spawnSubagent: async () => {
      calls.push(calls.length + 1);
      if (calls.length === 1) throw new Error("service unavailable");
      return "native-child";
    },
  });

  await coordinator.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });
  await coordinator.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 2 });

  assert.deepEqual(calls, [1, 2]);
  assert.deepEqual(receipts, ["nack", "injection_requested", "ack"]);
  coordinator.close();
});

test("a lost Sentinel ACK retries the same handoff without spawning another child", async () => {
  const calls = [];
  const receipts = [];
  let failAck = true;
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: {
      sendReceipt(type) {
        if (type === "ack" && failAck) {
          failAck = false;
          throw new Error("lost ACK");
        }
        receipts.push(type);
      },
    },
    spawnSubagent: async () => {
      calls.push(calls.length + 1);
      return "native-child";
    },
  });

  await coordinator.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });
  await coordinator.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 2 });

  assert.deepEqual(calls, [1]);
  assert.deepEqual(receipts, ["injection_requested", "injection_requested", "ack"]);
  coordinator.close();
});

test("a lost Sentinel ACK keeps a completed handoff fenced until ownership is durable", async () => {
  const calls = [];
  const receipts = [];
  let dropAck = true;
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: {
      sessionManager: {
        getBranch: () => [],
        getSessionId: () => "parent",
        isPersisted: () => true,
        getSessionFile: () => "/tmp/session.jsonl",
      },
    },
    client: {
      sendReceipt: (type) => {
        if (type === "ack" && dropAck) {
          dropAck = false;
          return;
        }
        receipts.push(type);
      },
    },
    readSessionFile: () => "",
    spawnSubagent: async () => {
      calls.push(calls.length + 1);
      return "native-child";
    },
  });
  const delivery = { messageId: sentinelMessage.id, attemptNumber: 1 };

  await coordinator.onEnvelope(sentinelMessage, delivery);
  await coordinator.onEnvelope(sentinelMessage, { ...delivery, attemptNumber: 2 });

  assert.deepEqual(calls, [1]);
  assert.deepEqual(receipts, ["injection_requested", "injection_requested", "ack"]);
  coordinator.close();
});

test("successful Sentinel handoff polls until ownership is durable and then releases its timer", async () => {
  const item = {
    messageId: sentinelMessage.id,
    messageIncarnationId: "incarnation-after-settlement",
    attemptNumber: 1,
    deliveryExpiresAt: "expiry-after-settlement",
  };
  const delivery = { messageId: sentinelMessage.id, attemptNumber: 1, items: [item] };
  const branch = [];
  const deferred = [];
  const timers = [];
  const receipts = [];
  const calls = [];
  let persisted = "";
  const status = {
    type: "custom_message",
    customType: "mecha-sentinel-status",
    details: {
      message_id: sentinelMessage.id,
      message_incarnation_id: item.messageIncarnationId,
      subagent_id: "native-child",
      state: "handed_off",
    },
  };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: {
      sendMessage: (message) => {
        if (message.customType === "mecha-sentinel-status") branch.push({ type: "custom_message", ...message });
      },
    },
    sessionContext: {
      sessionManager: {
        getBranch: () => branch,
        getSessionId: () => "parent",
        isPersisted: () => true,
        getSessionFile: () => "/tmp/session.jsonl",
      },
    },
    client: { sendReceipt: (type) => receipts.push(type) },
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false, unref() {} };
      timers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
    readSessionFile: () => persisted,
    spawnSubagent: async () => {
      calls.push("spawned");
      return "native-child";
    },
  });

  await coordinator.onEnvelope(sentinelMessage, delivery);
  assert.deepEqual(calls, ["spawned"]);
  assert.deepEqual(receipts, ["injection_requested", "ack"]);
  assert.equal(timers.length, 1);
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-sentinel-status", details: {
    message_id: sentinelMessage.id, message_incarnation_id: item.messageIncarnationId,
  } } }, {
    sessionManager: { getSessionId: () => "parent" },
  });
  deferred[0]();

  // A retry that arrives while the status is still only in the branch must
  // share the existing child and keep the cleanup timer alive.
  await coordinator.onEnvelope(sentinelMessage, { ...delivery, attemptNumber: 2, items: [{ ...item, attemptNumber: 2 }] });
  assert.deepEqual(calls, ["spawned"]);
  assert.equal(timers.length, 1);
  timers[0].callback();
  assert.equal(timers.length, 2);

  persisted = `${JSON.stringify(status)}\n`;
  timers[1].callback();
  timers[1].callback();
  assert.equal(timers.length, 2, "durable ownership must stop polling");
  assert.equal(timers[0].cleared, false);
  assert.equal(timers[1].cleared, false);
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-sentinel-status", details: {
    message_id: sentinelMessage.id, message_incarnation_id: item.messageIncarnationId,
  } } }, {
    sessionManager: { getSessionId: () => "parent" },
  });
  deferred[1]();
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-sentinel-status", details: { message_id: sentinelMessage.id } } }, {
    sessionManager: { getSessionId: () => "parent" },
  });
  deferred[2]();
  await coordinator.onEnvelope(sentinelMessage, { ...delivery, attemptNumber: 3, items: [{ ...item, attemptNumber: 3 }] });
  assert.deepEqual(calls, ["spawned"]);
  assert.deepEqual(receipts, [
    "injection_requested", "ack",
    "injection_requested", "ack",
    "injection_requested", "ack",
  ]);
  coordinator.close();
});

test("durable Sentinel ownership fences a recreated coordinator and contains receipt failures", async () => {
  const item = {
    messageId: sentinelMessage.id, messageIncarnationId: "incarnation-durable", attemptNumber: 1, deliveryExpiresAt: "expiry-durable",
  };
  const delivery = { messageId: sentinelMessage.id, attemptNumber: 1, items: [item] };
  const status = {
    type: "custom_message",
    customType: "mecha-sentinel-status",
    details: {
      message_id: sentinelMessage.id,
      message_incarnation_id: item.messageIncarnationId,
      subagent_id: "native-child",
      state: "handed_off",
    },
  };
  const manager = {
    getBranch: () => [status],
    getSessionId: () => "parent",
    isPersisted: () => true,
    getSessionFile: () => "/tmp/session.jsonl",
  };
  const calls = [];
  const receipts = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: manager },
    client: { sendReceipt: (type) => receipts.push(type) },
    readSessionFile: () => `${JSON.stringify(status)}\n`,
    spawnSubagent: async () => {
      calls.push("spawned");
      return "unexpected-child";
    },
  });

  await coordinator.onEnvelope(sentinelMessage, delivery);
  assert.deepEqual(calls, []);
  assert.deepEqual(receipts, ["injection_requested", "ack"]);
  coordinator.close();

  const failedReceipts = [];
  const failedCoordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: manager },
    client: { sendReceipt() { throw new Error("socket closed"); } },
    readSessionFile: () => `${JSON.stringify(status)}\n`,
    spawnSubagent: async () => {
      failedReceipts.push("spawned");
      return "unexpected-child";
    },
  });
  await failedCoordinator.onEnvelope(sentinelMessage, delivery);
  assert.deepEqual(failedReceipts, []);
  failedCoordinator.close();
});

test("Sentinel ownership checks keep incomplete status history retryable", async () => {
  const item = {
    messageId: sentinelMessage.id, messageIncarnationId: "incarnation-current", attemptNumber: 1, deliveryExpiresAt: "expiry-current",
  };
  const delivery = { messageId: sentinelMessage.id, attemptNumber: 1, items: [item] };
  const status = {
    type: "custom_message",
    customType: "mecha-sentinel-status",
    details: {
      message_id: sentinelMessage.id,
      message_incarnation_id: item.messageIncarnationId,
      subagent_id: "native-child",
      state: "handed_off",
    },
  };
  const cases = [
    { name: "missing incarnation", manager: { getBranch: () => [{ ...status, details: { ...status.details, message_incarnation_id: undefined } }], isPersisted: () => true, getSessionFile: () => "/tmp/session.jsonl" }, readSessionFile: () => `${JSON.stringify(status)}\n` },
    { name: "invalid status type", manager: { getBranch: () => [{ type: "other" }], isPersisted: () => true, getSessionFile: () => "/tmp/session.jsonl" }, readSessionFile: () => `${JSON.stringify(status)}\n` },
    { name: "invalid status details", manager: { getBranch: () => [{ ...status, details: { ...status.details, state: "running" } }], isPersisted: () => true, getSessionFile: () => "/tmp/session.jsonl" }, readSessionFile: () => `${JSON.stringify(status)}\n` },
    { name: "unpersisted", manager: { getBranch: () => [status], isPersisted: () => false, getSessionFile: () => "/tmp/session.jsonl" }, readSessionFile: () => `${JSON.stringify(status)}\n` },
    { name: "missing session file", manager: { getBranch: () => [status], isPersisted: () => true, getSessionFile: () => undefined }, readSessionFile: () => { throw new Error("must not read missing session path"); } },
    { name: "unreadable session file", manager: { getBranch: () => [status], isPersisted: () => true, getSessionFile: () => "/tmp/session.jsonl" }, readSessionFile: () => { throw new Error("unreadable"); } },
  ];
  for (const { name, manager, readSessionFile } of cases) {
    const calls = [];
    const coordinator = createPiMailboxDeliveryCoordinator({
      pi: { sendMessage() {} },
      sessionContext: { sessionManager: { ...manager, getSessionId: () => "parent" } },
      client: { sendReceipt() {} },
      readSessionFile,
      spawnSubagent: async () => {
        calls.push(name);
        return "native-child";
      },
    });
    await coordinator.onEnvelope(sentinelMessage, delivery);
    assert.deepEqual(calls, [name]);
    coordinator.close();
  }

  const fallbackCalls = [];
  const fallback = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt() {} },
    spawnSubagent: async () => {
      fallbackCalls.push("spawned");
      return "native-child";
    },
  });
  await fallback.onEnvelope(sentinelMessage, {
    messageId: sentinelMessage.id,
    attemptNumber: 1,
    items: [{ ...item, messageId: "different-message" }],
  });
  assert.deepEqual(fallbackCalls, ["spawned"]);
  fallback.close();
});

test("durable ownership releases a completed Sentinel handoff for the same incarnation", async () => {
  const item = {
    messageId: sentinelMessage.id, messageIncarnationId: "incarnation-durable", attemptNumber: 1, deliveryExpiresAt: "expiry-durable",
  };
  const delivery = { messageId: sentinelMessage.id, attemptNumber: 1, items: [item] };
  const branch = [];
  const sent = [];
  const calls = [];
  const receipts = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: {
      sendMessage: (message) => {
        sent.push(message);
        branch.push({ type: "custom_message", ...message });
      },
    },
    sessionContext: {
      sessionManager: {
        getBranch: () => branch,
        getSessionId: () => "parent",
        isPersisted: () => true,
        getSessionFile: () => "/tmp/session.jsonl",
      },
    },
    client: { sendReceipt: (type) => receipts.push(type) },
    readSessionFile: () => `${JSON.stringify(branch[0])}\n`,
    spawnSubagent: async () => {
      calls.push("spawned");
      return "native-child";
    },
  });

  await coordinator.onEnvelope(sentinelMessage, delivery);
  await coordinator.onEnvelope(sentinelMessage, { ...delivery, attemptNumber: 2 });

  assert.deepEqual(calls, ["spawned"]);
  assert.equal(sent.length, 1);
  assert.deepEqual(receipts, ["injection_requested", "ack", "injection_requested", "ack"]);
  coordinator.close();
});

test("a settled Sentinel handoff allows a reused message ID with a new incarnation", async () => {
  const calls = [];
  const receipts = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt: (type) => receipts.push(type) },
    spawnSubagent: async () => {
      calls.push(calls.length + 1);
      return `native-child-${calls.length}`;
    },
  });
  const firstDelivery = {
    messageId: sentinelMessage.id, attemptNumber: 1,
    items: [{ messageId: sentinelMessage.id, messageIncarnationId: "incarnation-1", attemptNumber: 1, deliveryExpiresAt: "expiry-1" }],
  };
  const secondDelivery = {
    messageId: sentinelMessage.id, attemptNumber: 1,
    items: [{ messageId: sentinelMessage.id, messageIncarnationId: "incarnation-2", attemptNumber: 1, deliveryExpiresAt: "expiry-2" }],
  };

  await coordinator.onEnvelope(sentinelMessage, firstDelivery);
  await coordinator.onEnvelope(sentinelMessage, secondDelivery);

  assert.deepEqual(calls, [1, 2]);
  assert.deepEqual(receipts, ["injection_requested", "ack", "injection_requested", "ack"]);
  coordinator.close();
});

test("closing fences every continuation of an in-flight Sentinel handoff", async () => {
  const sent = [];
  const receipts = [];
  let resolveSpawn;
  const spawn = new Promise((resolve) => { resolveSpawn = resolve; });
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: { sendReceipt: (type) => receipts.push(type) },
    spawnSubagent: async () => spawn,
  });

  const handoff = coordinator.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });
  coordinator.close();
  resolveSpawn("native-child");
  await handoff;

  assert.deepEqual(sent, []);
  assert.deepEqual(receipts, []);
});

test("closing during a Sentinel receipt fences the remaining receipt", async () => {
  const sent = [];
  const receipts = [];
  let coordinator;
  coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: { getSessionId: () => "parent" } },
    client: {
      sendReceipt(type) {
        receipts.push(type);
        if (type === "injection_requested") coordinator.close();
      },
    },
    spawnSubagent: async () => "native-child",
  });

  await coordinator.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });

  assert.deepEqual(receipts, ["injection_requested"]);
  assert.equal(sent.length, 1);
});

test("a retry acknowledges durable attempt-one history without reinjecting attempt two", () => {
  const originalItem = {
    messageId: "ordinary-message",
    messageIncarnationId: "incarnation-1",
    attemptNumber: 1,
    deliveryExpiresAt: "2030-01-01T00:00:30.000Z",
  };
  const retryItem = { ...originalItem, attemptNumber: 2, deliveryExpiresAt: "2030-01-01T00:01:00.000Z" };
  const entry = {
    type: "custom_message",
    customType: "mecha-agent-message",
    details: { message_id: "ordinary-message", delivery_items: [originalItem] },
  };
  const sent = [];
  const receipts = [];
  const manager = {
    getBranch: () => [entry],
    getSessionId: () => "parent",
    isPersisted: () => true,
    getSessionFile: () => "/tmp/session.jsonl",
  };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: manager },
    client: { sendReceipt: (type, delivery) => receipts.push({ type, delivery }) },
    readSessionFile: () => `${JSON.stringify(entry)}\n`,
  });

  coordinator.onEnvelope({ id: "ordinary-message", from: "sender", to: "parent", body: "private body" }, {
    messageId: "ordinary-message", attemptNumber: 2, items: [retryItem],
  });

  assert.deepEqual(sent, []);
  assert.deepEqual(receipts.map(({ type, delivery }) => ({ type, attemptNumber: delivery.attemptNumber })), [
    { type: "injection_requested", attemptNumber: 2 },
    { type: "ack", attemptNumber: 2 },
  ]);
  coordinator.close();
});

test("an undefined session file stays unverified in the delivery-state coverage fixture", () => {
  const entry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: "missing-file" } };
  const state = mailboxSessionDeliveryState({
    getBranch: () => [entry],
    getSessionId: () => "parent",
    isPersisted: () => true,
    getSessionFile: () => undefined,
  }, "missing-file", { readSessionFile: () => { throw new Error("must not read a missing session path"); } });

  assert.equal(state, "history_unverified");
});

test("ordinary delivery acknowledges only after its matching session entry is persisted", () => {
  const item = {
    messageId: "ordinary-checkpoint",
    messageIncarnationId: "incarnation-1",
    attemptNumber: 1,
    deliveryExpiresAt: "2030-01-01T00:00:30.000Z",
  };
  const entry = {
    type: "custom_message",
    customType: "mecha-agent-message",
    details: { message_id: "ordinary-checkpoint", delivery_items: [item] },
  };
  const branch = [];
  let persisted = "";
  const deferred = [];
  const timers = [];
  const receipts = [];
  const checkpointStatuses = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: {
      sessionManager: {
        getBranch: () => branch,
        getSessionId: () => "parent",
        isPersisted: () => true,
        getSessionFile: () => "/tmp/session.jsonl",
      },
      ui: { setStatus() {} },
    },
    client: { sendReceipt: (type, delivery) => receipts.push({ type, delivery }) },
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      timers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
    readSessionFile: () => persisted,
  });
  const message = { id: "ordinary-checkpoint", from: "sender", to: "parent", body: "body" };
  const delivery = { messageId: message.id, attemptNumber: 1, items: [item] };

  coordinator.onEnvelope(message, delivery);
  branch.push(entry);
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } } }, {
    sessionManager: { ...coordinatorContextSessionManager(branch), getSessionId: () => "parent" },
    ui: { setStatus: (...args) => checkpointStatuses.push(args) },
  });
  deferred[0]();
  assert.deepEqual(receipts.map(({ type }) => type), ["injection_requested"]);
  assert.equal(timers.length, 1);

  persisted = `${JSON.stringify(entry)}\n`;
  timers[0].callback();
  assert.deepEqual(receipts.map(({ type }) => type), ["injection_requested", "ack"]);
  assert.deepEqual(checkpointStatuses, [
    ["mecha-mailbox", "mailbox: waiting for durable session checkpoint"],
    ["mecha-mailbox", "mailbox: connected"],
  ]);
  coordinator.close();
});

test("checkpoint retry acknowledges the original attempt after a newer retry arrives first", () => {
  const item = {
    messageId: "checkpoint-retry-race",
    messageIncarnationId: "incarnation-1",
    attemptNumber: 1,
    deliveryExpiresAt: "2030-01-01T00:00:30.000Z",
  };
  const entry = {
    type: "custom_message",
    customType: "mecha-agent-message",
    details: { message_id: item.messageId, delivery_items: [item] },
  };
  const branch = [];
  let persisted = "";
  const deferred = [];
  const timers = [];
  const receipts = [];
  const coordinator = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch }) },
    client: { sendReceipt: (type, delivery) => receipts.push({ type, attemptNumber: delivery.attemptNumber }) },
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      timers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
    readSessionFile: () => persisted,
  });
  const message = { id: item.messageId, from: "sender", to: "parent", body: "body" };

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1, items: [item] });
  branch.push(entry);
  const retryItem = { ...item, attemptNumber: 2, deliveryExpiresAt: "2030-01-01T00:01:00.000Z" };
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2, items: [retryItem] });
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } } }, {
    sessionManager: coverageSessionManager({ branch }),
  });
  deferred[0]();
  assert.equal(timers.length, 1);
  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "injection_requested", attemptNumber: 2 },
  ]);

  persisted = `${JSON.stringify(entry)}\n`;
  timers[0].callback();

  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "injection_requested", attemptNumber: 2 },
    { type: "ack", attemptNumber: 2 },
  ]);
  assert.equal(timers.length, 1, "durable original history must stop checkpoint polling");
  coordinator.close();
});

test("ordinary mailbox injections render the reply instruction for the envelope reply target", () => {
  const sent = [];
  const receipts = [];
  let time = 1_000;
  const coordinator = coverageCoordinator({
    pi: { sendMessage: (message) => sent.push(message) },
    client: { sendReceipt: (type) => receipts.push(type) },
    readSessionFile: () => "",
    pendingReinjectionDelayMs: 5_000,
    now: () => time,
  });
  const wolfgangMessage = { id: "from-wolfgang", from: "wolfgang", to: "supervisor", subject: "status", body: "first body" };
  const supervisorMessage = { id: "reply-to-supervisor", from: "supervisor", to: "worker", body: "second body" };

  coordinator.onEnvelope(wolfgangMessage, { messageId: wolfgangMessage.id, attemptNumber: 1, items: [] });
  coordinator.onEnvelope(supervisorMessage, { messageId: supervisorMessage.id, attemptNumber: 1, items: [] });
  time += 5_000;
  coordinator.onEnvelope(supervisorMessage, { messageId: supervisorMessage.id, attemptNumber: 2, items: [] });

  assert.equal(sent.length, 3);
  assert.equal(sent[0].content, "Mailbox message from wolfgang\nSubject: status\n\nfirst body\n\nReply via mailbox to wolfgang");
  assert.equal(sent[1].content, "Mailbox message from supervisor\n\nsecond body\n\nReply via mailbox to supervisor");
  assert.equal(sent[2].content, sent[1].content);
  assert.deepEqual(receipts, ["injection_requested", "injection_requested", "injection_requested"]);
  coordinator.close();

});

test("an absent pending delivery is reinjected only after the retry delay", () => {
  const sent = [];
  const receipts = [];
  let time = 1_000;
  const item = { messageId: "ordinary-retry", messageIncarnationId: "incarnation-1", attemptNumber: 1, deliveryExpiresAt: "2030-01-01T00:00:30.000Z" };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: {
      getBranch: () => [], getSessionId: () => "parent", isPersisted: () => true, getSessionFile: () => "/tmp/session.jsonl",
    } },
    client: { sendReceipt: (type, delivery) => receipts.push({ type, attemptNumber: delivery.attemptNumber }) },
    readSessionFile: () => "",
    pendingReinjectionDelayMs: 5_000,
    now: () => time,
  });
  const message = { id: "ordinary-retry", from: "sender", to: "parent", body: "body" };

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1, items: [item] });
  time += 4_999;
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2, items: [{ ...item, attemptNumber: 2 }] });
  assert.equal(sent.length, 1);
  time += 1;
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 3, items: [{ ...item, attemptNumber: 3 }] });

  assert.equal(sent.length, 2);
  for (const [index, attemptNumber] of [1, 3].entries()) {
    assert.deepEqual(sent[index], [{
      customType: "mecha-agent-message", display: true,
      content: "Mailbox message from sender\n\nbody\n\nReply via mailbox to sender",
      details: { message_id: message.id, from: "sender", to: "parent",
        message_incarnation_id: "incarnation-1", delivery_items: [{ ...item, attemptNumber }] },
    }, { deliverAs: "steer", triggerTurn: true }]);
  }
  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "injection_requested", attemptNumber: 2 },
    { type: "injection_requested", attemptNumber: 3 },
  ]);
  coordinator.close();
});

function coordinatorContextSessionManager(branch) {
  return {
    getBranch: () => branch,
    getSessionId: () => "parent",
    isPersisted: () => true,
    getSessionFile: () => "/tmp/session.jsonl",
  };
}

function coverageSessionManager({ branch = [], persisted = true, sessionFile = "/tmp/session.jsonl", sessionId = "parent" } = {}) {
  return {
    getBranch: () => branch,
    getSessionId: () => sessionId,
    isPersisted: () => persisted,
    getSessionFile: () => sessionFile,
  };
}

function coverageCoordinator(overrides = {}) {
  return createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: coverageSessionManager() },
    client: { sendReceipt() {} },
    ...overrides,
  });
}

test("delivery coordinator validates every required dependency and timing option", () => {
  const base = {
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: coverageSessionManager() },
    client: { sendReceipt() {} },
  };
  const invalid = [
    [{ ...base, pi: undefined }, /pi.sendMessage/],
    [{ ...base, pi: {} }, /pi.sendMessage/],
    [{ ...base, sessionContext: undefined }, /session context/],
    [{ ...base, sessionContext: {} }, /session context/],
    [{ ...base, client: undefined }, /sendReceipt/],
    [{ ...base, client: {} }, /sendReceipt/],
    [{ ...base, defer: "later" }, /defer/],

    [{ ...base, setTimeoutImpl: "later" }, /timers/],
    [{ ...base, clearTimeoutImpl: "later" }, /timers/],
    [{ ...base, checkpointRetryDelayMs: 0 }, /checkpointRetryDelayMs/],
    [{ ...base, checkpointRetryDelayMs: 1.5 }, /checkpointRetryDelayMs/],
    [{ ...base, pendingReinjectionDelayMs: 0 }, /pendingReinjectionDelayMs/],
    [{ ...base, pendingReinjectionDelayMs: 1.5 }, /pendingReinjectionDelayMs/],
    [{ ...base, readSessionFile: "read" }, /readSessionFile/],
    [{ ...base, now: "now" }, /now/],
    [{ ...base, spawnSubagent: "spawn" }, /spawnSubagent/],
  ];
  for (const [options, message] of invalid) assert.throws(() => createPiMailboxDeliveryCoordinator(options), message);
  assert.doesNotThrow(() => coverageCoordinator({
    defer: () => {}, setTimeoutImpl: () => {}, clearTimeoutImpl: () => {},
    checkpointRetryDelayMs: 1, pendingReinjectionDelayMs: 1,
    readSessionFile: () => "", now: () => 1, spawnSubagent: async () => "child",
  }));
});

test("mailbox session delivery state covers absent, unverified, durable, and persistence errors", () => {
  const item = { messageId: "state", messageIncarnationId: "inc", attemptNumber: 1, deliveryExpiresAt: "expiry" };
  const entry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: "state", delivery_items: [item] } };
  const manager = (options = {}) => coverageSessionManager({ branch: [entry], ...options });
  assert.equal(mailboxSessionDeliveryState(coverageSessionManager({ branch: [null] }), "state"), "absent");
  assert.equal(mailboxSessionDeliveryState(coverageSessionManager({ branch: [{ type: "wrong", customType: "mecha-agent-message", details: { message_id: "state" } }] }), "state"), "absent");
  assert.equal(mailboxSessionDeliveryState(coverageSessionManager({ branch: [{ type: "custom_message", customType: "wrong", details: { message_id: "state" } }] }), "state"), "absent");
  assert.equal(mailboxSessionDeliveryState(coverageSessionManager({ branch: [{ type: "custom_message", customType: "mecha-agent-message", details: { message_id: "other" } }] }), "state"), "absent");
  assert.equal(mailboxSessionDeliveryState(coverageSessionManager(), "state"), "absent");
  assert.equal(mailboxSessionDeliveryState(manager(), "state", { deliveryItems: [{ ...item, attemptNumber: 2 }] }), "history_unverified");
  assert.equal(mailboxSessionDeliveryState(manager({ persisted: false }), "state", { deliveryItems: [item] }), "history_unverified");
  assert.equal(mailboxSessionDeliveryState(manager({ sessionFile: "" }), "state", { deliveryItems: [item] }), "history_unverified");
  assert.equal(mailboxSessionDeliveryState(manager({ sessionFile: undefined }), "state", { deliveryItems: [item] }), "history_unverified");
  assert.equal(mailboxSessionDeliveryState(manager(), "state", { deliveryItems: [item], readSessionFile: undefined }), "history_unverified");

  const errors = [];
  assert.equal(mailboxSessionDeliveryState(manager(), "state", {
    deliveryItems: [item], readSessionFile: () => { throw { code: "ENOENT" }; }, onPersistenceError: (error) => errors.push(error),
  }), "history_unverified");
  assert.equal(errors.length, 0);
  assert.equal(mailboxSessionDeliveryState(manager(), "state", {
    deliveryItems: [item], readSessionFile: () => { throw { message: "plain failure" }; }, onPersistenceError: (error) => errors.push(error),
  }), "history_unverified");
  assert.equal(errors.at(-1).message, "plain failure");
  assert.equal(mailboxSessionDeliveryState(manager(), "state", {
    deliveryItems: [item], readSessionFile: () => { throw new Error("error failure"); }, onPersistenceError: (error) => errors.push(error),
  }), "history_unverified");
  assert.equal(errors.at(-1).message, "error failure");
  assert.equal(mailboxSessionDeliveryState(manager(), "state", {
    deliveryItems: [item], readSessionFile: () => { throw "string failure"; }, onPersistenceError: (error) => errors.push(error),
  }), "history_unverified");
  assert.equal(errors.at(-1).message, "string failure");
  assert.equal(mailboxSessionDeliveryState(manager(), "state", { deliveryItems: [item], readSessionFile: () => "" }), "history_unverified");
  assert.equal(mailboxSessionDeliveryState(manager(), "state", { deliveryItems: [item], readSessionFile: () => `${JSON.stringify(entry)}\n` }), "durable");
});

test("session delivery matching rejects malformed and mismatched persisted item shapes", () => {
  const valid = { messageId: "items", messageIncarnationId: "inc", attemptNumber: 1, deliveryExpiresAt: "expiry" };
  const branchEntry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: "items", delivery_items: [valid] } };
  const manager = coverageSessionManager({ branch: [branchEntry] });
  const state = (persistedItem) => mailboxSessionDeliveryState(manager, "items", {
    deliveryItems: [valid], readSessionFile: () => `${JSON.stringify({ ...branchEntry, details: { ...branchEntry.details, delivery_items: [persistedItem] } })}\n`,
  });
  assert.equal(state(null), "history_unverified");
  assert.equal(state([]), "history_unverified");
  assert.equal(state({ ...valid, messageId: 1 }), "history_unverified");
  assert.equal(state({ ...valid, messageIncarnationId: 1 }), "history_unverified");
  assert.equal(state({ ...valid, attemptNumber: "1" }), "history_unverified");
  assert.equal(state({ ...valid, deliveryExpiresAt: 1 }), "history_unverified");
  assert.equal(state({ ...valid, messageId: "other" }), "history_unverified");
  assert.equal(state({ ...valid, messageIncarnationId: "other" }), "history_unverified");
  assert.equal(state({ ...valid, attemptNumber: 2 }), "history_unverified");
  assert.equal(state({ ...valid, deliveryExpiresAt: "other" }), "history_unverified");
  assert.equal(state(valid), "durable");
});

test("ordinary delivery handles unverified history and every injection or receipt failure", () => {
  const message = { id: "ordinary-failure", from: "sender", to: "parent", body: "body" };
  const item = { messageId: message.id, messageIncarnationId: "inc", attemptNumber: 1, deliveryExpiresAt: "expiry" };
  const delivery = { messageId: message.id, attemptNumber: 1, items: [item] };
  const historyEntry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: message.id, delivery_items: [item] } };
  const statuses = [];
  const waiting = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [historyEntry] }), ui: { setStatus: (...args) => statuses.push(args) } },
    readSessionFile: () => "",
  });
  waiting.onEnvelope(message, delivery);
  assert.deepEqual(statuses, [["mecha-mailbox", "mailbox: waiting for durable session checkpoint"]]);
  waiting.close();

  const knownWithUi = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [historyEntry] }), ui: { setStatus() {} } },
    readSessionFile: () => `${JSON.stringify(historyEntry)}\n`,
  });
  knownWithUi.onEnvelope(message, delivery);
  knownWithUi.close();

  const failedReceipts = [];
  const injectionFailure = coverageCoordinator({
    pi: { sendMessage() { throw new Error("injection failed"); } },
    client: { sendReceipt: (type, received, reason) => failedReceipts.push({ type, received, reason }) },
  });
  injectionFailure.onEnvelope(message, delivery);
  assert.equal(failedReceipts[0].type, "nack");
  assert.equal(failedReceipts[0].reason, "injection_failed");
  injectionFailure.close();

  const nackFailure = coverageCoordinator({
    pi: { sendMessage() { throw new Error("injection failed"); } },
    client: { sendReceipt() { throw new Error("socket closed"); } },
  });
  assert.doesNotThrow(() => nackFailure.onEnvelope(message, delivery));
  nackFailure.close();

  let time = 1_000;
  let sends = 0;
  const reinjectionFailure = coverageCoordinator({
    pi: { sendMessage() { sends += 1; if (sends > 1) throw new Error("retry injection failed"); } },
    client: { sendReceipt() {} },
    readSessionFile: () => "",
    pendingReinjectionDelayMs: 5_000,
    now: () => time,
  });
  reinjectionFailure.onEnvelope(message, delivery);
  time += 5_000;
  assert.doesNotThrow(() => reinjectionFailure.onEnvelope(message, { ...delivery, attemptNumber: 2 }));
  assert.equal(sends, 2);
  reinjectionFailure.close();

  const arrivalFailure = coverageCoordinator({
    pi: { sendMessage() {} },
    client: { sendMessageArrival() { throw new Error("arrival failed"); }, sendReceipt() {} },
  });
  assert.doesNotThrow(() => arrivalFailure.onEnvelope({ ...message, source: { kind: "agent" } }, delivery));
  arrivalFailure.close();

  const receiptFailure = coverageCoordinator({
    pi: { sendMessage() {} },
    client: { sendReceipt() { throw new Error("receipt failed"); } },
  });
  assert.doesNotThrow(() => receiptFailure.onEnvelope(message, delivery));
  receiptFailure.close();
});

test("onMessageEnd fences invalid, stale, and unknown events and close cancels timers", () => {
  const branch = [];
  const deferred = [];
  const timers = [];
  const coordinator = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch }) },
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback) => { const timer = { callback, cleared: false }; timers.push(timer); return timer; },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
    readSessionFile: () => "",
  });
  const message = { id: "message-end", from: "sender", to: "parent", body: "body" };
  coordinator.onMessageEnd({}, { sessionManager: coverageSessionManager({ sessionId: "parent" }) });
  coordinator.onMessageEnd({ message: { role: "assistant", customType: "mecha-agent-message", details: { message_id: message.id } } }, {
    sessionManager: coverageSessionManager({ sessionId: "parent" }),
  });
  coordinator.onMessageEnd({ message: { role: "custom", customType: "other", details: { message_id: message.id } } }, {
    sessionManager: coverageSessionManager({ sessionId: "parent" }),
  });
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: {} } }, {
    sessionManager: coverageSessionManager({ sessionId: "parent" }),
  });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1, items: [] });
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } } }, {
    sessionManager: coverageSessionManager({ sessionId: "other" }),
  });
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: { message_id: "unknown" } } }, {
    sessionManager: coverageSessionManager({ sessionId: "parent" }),
  });
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } } }, {
    sessionManager: coverageSessionManager({ branch, sessionId: "parent" }),
  });
  assert.equal(deferred.length, 1);
  deferred[0]();
  assert.equal(timers.length, 1);
  deferred[0]();
  coordinator.close();
  assert.equal(timers[0].cleared, true);
  deferred[0]();
});

test("durable checkpoint acknowledgement tolerates a lost ACK and later stale callbacks", () => {
  const item = { messageId: "checkpoint-error", messageIncarnationId: "inc", attemptNumber: 1, deliveryExpiresAt: "expiry" };
  const entry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: item.messageId, delivery_items: [item] } };
  const branch = [];
  const deferred = [];
  const timers = [];
  let persisted = "";
  let failAck = true;
  const statuses = [];
  const coordinator = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch }), ui: { setStatus: (...args) => statuses.push(args) } },
    client: { sendReceipt(type) { if (type === "ack" && failAck) { failAck = false; throw new Error("lost ack"); } } },
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback) => { const timer = { callback, cleared: false }; timers.push(timer); return timer; },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
    readSessionFile: () => persisted,
  });
  const message = { id: item.messageId, from: "sender", to: "parent", body: "body" };
  coordinator.onEnvelope(message, { messageId: item.messageId, attemptNumber: 1, items: [item] });
  branch.push(entry);
  const checkpointUi = { setStatus() {}, notify() {} };
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: { message_id: item.messageId } } }, {
    sessionManager: coverageSessionManager({ branch, sessionId: "parent" }),
    ui: checkpointUi,
  });
  deferred[0]();
  assert.equal(timers.length, 1);
  persisted = `${JSON.stringify(entry)}\n`;
  timers[0].callback();
  assert.equal(timers[0].cleared, false);
  deferred[0]();
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: { message_id: item.messageId } } }, {
    sessionManager: coverageSessionManager({ branch, sessionId: "other" }),
  });
  deferred.push(() => {});
  coordinator.close();
  timers[0].callback();
});

test("known deliveries clear pending state and contain acknowledgement failures", () => {
  const item = { messageId: "known-pending", messageIncarnationId: "inc", attemptNumber: 1, deliveryExpiresAt: "expiry" };
  const entry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: item.messageId, delivery_items: [item] } };
  const branch = [];
  const receipts = [];
  const coordinator = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch }) },
    client: { sendReceipt: (type) => receipts.push(type) },
    readSessionFile: () => branch.length === 0 ? "" : `${JSON.stringify(entry)}\n`,
  });
  const message = { id: item.messageId, from: "sender", to: "parent", body: "body" };
  coordinator.onEnvelope(message, { messageId: item.messageId, attemptNumber: 1, items: [item] });
  branch.push(entry);
  coordinator.onEnvelope(message, { messageId: item.messageId, attemptNumber: 2, items: [{ ...item, attemptNumber: 2 }] });
  assert.deepEqual(receipts, ["injection_requested", "injection_requested", "ack"]);
  coordinator.close();

  const knownFailure = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [entry] }) },
    client: { sendReceipt() { throw new Error("known receipt failed"); } },
    readSessionFile: () => `${JSON.stringify(entry)}\n`,
  });
  assert.doesNotThrow(() => knownFailure.onEnvelope(message, { messageId: item.messageId, attemptNumber: 1, items: [item] }));
  knownFailure.close();
});

test("arrival publication, subject formatting, and inactive fences cover ordinary delivery edges", () => {
  const item = { messageId: "arrival-edges", messageIncarnationId: "inc", attemptNumber: 1, deliveryExpiresAt: "expiry" };
  const entry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: item.messageId, delivery_items: [item] } };
  const branch = [];
  let sent = 0;
  let arrivals = 0;
  const coordinator = coverageCoordinator({
    pi: { sendMessage(message) { sent += 1; assert.equal(message.details.subject, "subject"); } },
    sessionContext: { sessionManager: coverageSessionManager({ branch }) },
    client: { sendMessageArrival() { arrivals += 1; }, sendReceipt() {} },
    readSessionFile: () => branch.length === 0 ? "" : `${JSON.stringify(entry)}\n`,
  });
  const message = { id: item.messageId, from: "sender", to: "parent", body: "body", subject: "subject", source: { kind: "discord" } };
  coordinator.onEnvelope(message, { messageId: item.messageId, attemptNumber: 1, items: [item] });
  branch.push(entry);
  coordinator.onEnvelope(message, { messageId: item.messageId, attemptNumber: 2, items: [{ ...item, attemptNumber: 2 }] });
  branch.length = 0;
  coordinator.onEnvelope(message, { messageId: item.messageId, attemptNumber: 3, items: [{ ...item, attemptNumber: 3 }] });
  assert.equal(sent, 2);
  assert.equal(arrivals, 1);
  coordinator.close();
  assert.equal(coordinator.onEnvelope(message, { messageId: item.messageId, attemptNumber: 4, items: [] }), undefined);

  const dynamicEntry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: "dynamic", delivery_items: [item] } };
  let branchReads = 0;
  const dynamic = coverageCoordinator({
    sessionContext: { sessionManager: {
      getBranch: () => { branchReads += 1; return branchReads === 1 ? [dynamicEntry] : []; },
      getSessionId: () => "parent", isPersisted: () => true, getSessionFile: () => "/tmp/session.jsonl",
    }, ui: { setStatus() {} } },
    readSessionFile: () => "",
  });
  dynamic.onEnvelope({ ...message, id: "dynamic" }, { messageId: "dynamic", attemptNumber: 2, items: [{ ...item, messageId: "dynamic", attemptNumber: 2 }] });
  dynamic.close();

  const missingArrival = coverageCoordinator({
    client: { sendReceipt() {} },
  });
  missingArrival.onEnvelope({ ...message, id: "missing-arrival", from: "sender", to: "parent", source: { kind: "agent" } }, {
    messageId: "missing-arrival", attemptNumber: 1, items: [{ ...item, messageId: "missing-arrival" }],
  });
  missingArrival.close();
});

test("closing fences Sentinel rejection, invalid IDs, status recording, and ordinary callbacks", async () => {
  let rejectSpawn;
  const rejected = coverageCoordinator({ spawnSubagent: async () => new Promise((resolve, reject) => { rejectSpawn = reject; }) });
  const rejectedHandoff = rejected.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });
  rejected.close();
  rejectSpawn(new Error("late failure"));
  await rejectedHandoff;

  let resolveSpawn;
  const invalid = coverageCoordinator({ spawnSubagent: async () => new Promise((resolve) => { resolveSpawn = resolve; }) });
  const invalidHandoff = invalid.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });
  invalid.close();
  resolveSpawn("");
  await invalidHandoff;

  const invalidReceipt = coverageCoordinator({
    client: { sendReceipt() { throw new Error("socket closed"); } },
    spawnSubagent: async () => "",
  });
  await assert.doesNotReject(() => invalidReceipt.onEnvelope(sentinelMessage, {
    messageId: sentinelMessage.id, attemptNumber: 1,
  }));
  invalidReceipt.close();

  let statusCoordinator;
  const statusAfterClose = coverageCoordinator({
    pi: { sendMessage(message) { if (message.customType === "mecha-sentinel-status") statusCoordinator.close(); } },
    client: { sendReceipt() {} },
    spawnSubagent: async () => "child",
  });
  statusCoordinator = statusAfterClose;
  await statusAfterClose.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });
  statusAfterClose.close();

  let retryStatusCoordinator;
  let statusWrites = 0;
  const statusRetryAfterFailure = coverageCoordinator({
    pi: {
      sendMessage(message) {
        if (message.customType !== "mecha-sentinel-status") return;
        statusWrites += 1;
        if (statusWrites === 1) throw new Error("first status write failed");
        retryStatusCoordinator.close();
      },
    },
    client: { sendReceipt() {} },
    spawnSubagent: async () => "child",
  });
  retryStatusCoordinator = statusRetryAfterFailure;
  await statusRetryAfterFailure.onEnvelope(sentinelMessage, { messageId: sentinelMessage.id, attemptNumber: 1 });
  assert.equal(statusWrites, 2);
  statusRetryAfterFailure.close();
});

test("retry receipt errors are contained and reported persistence failures are deduplicated", () => {
  const item = { messageId: "retry-error", messageIncarnationId: "inc", attemptNumber: 1, deliveryExpiresAt: "expiry" };
  const message = { id: item.messageId, from: "sender", to: "parent", body: "body" };
  let receiptCalls = 0;
  const retryReceiptFailure = coverageCoordinator({
    client: { sendReceipt() { receiptCalls += 1; if (receiptCalls > 1) throw new Error("retry receipt failed"); } },
    readSessionFile: () => "",
  });
  retryReceiptFailure.onEnvelope(message, { messageId: item.messageId, attemptNumber: 1, items: [item] });
  assert.doesNotThrow(() => retryReceiptFailure.onEnvelope(message, { messageId: item.messageId, attemptNumber: 2, items: [{ ...item, attemptNumber: 2 }] }));
  retryReceiptFailure.close();

  const entry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: item.messageId, delivery_items: [item] } };
  const statuses = [];
  const unreadable = coverageCoordinator({
    sessionContext: { sessionManager: { ...coverageSessionManager({ branch: [entry] }), getSessionFile: () => "/tmp/session.jsonl" }, ui: { setStatus() {}, notify: (...args) => statuses.push(args) } },
    readSessionFile: () => { throw new Error("unreadable"); },
  });
  const retry = { messageId: item.messageId, attemptNumber: 2, items: [{ ...item, attemptNumber: 2 }] };
  const sessionUnreadable = coverageCoordinator({
    sessionContext: { sessionManager: { ...coverageSessionManager({ branch: [entry] }), getSessionFile: () => "/tmp/session.jsonl" }, ui: { setStatus() {}, notify: (...args) => statuses.push(args) } },
    readSessionFile: () => { throw new Error("session unreadable"); },
  });
  sessionUnreadable.onEnvelope(message, { messageId: item.messageId, attemptNumber: 1, items: [item] });
  sessionUnreadable.onEnvelope(message, { messageId: item.messageId, attemptNumber: 1, items: [item] });
  sessionUnreadable.close();
  unreadable.onEnvelope(message, retry);
  unreadable.onEnvelope(message, retry);
  assert.equal(statuses.length, 2);
  unreadable.close();

  const noBranch = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [entry], persisted: false }), ui: { setStatus() {} } },
    readSessionFile: () => "",
  });
  noBranch.onEnvelope(message, retry);
  noBranch.close();

  const noFile = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [entry], sessionFile: undefined }), ui: { setStatus() {} } },
    readSessionFile: () => "",
  });
  noFile.onEnvelope(message, retry);
  noFile.close();

  const emptyFile = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [entry], sessionFile: "" }), ui: { setStatus() {} } },
    readSessionFile: () => { throw new Error("must not read an empty session path"); },
  });
  emptyFile.onEnvelope(message, retry);
  emptyFile.close();
});

test("retry history recognizes single-item legacy entries and exact identities", () => {
  const item = { messageId: "retry-cases", messageIncarnationId: "inc", attemptNumber: 1, deliveryExpiresAt: "expiry" };
  const message = { id: "retry-cases", from: "sender", to: "parent", body: "body" };
  const receipts = [];
  const run = (persistedItems, deliveryItems = [item], branch = true) => {
    const entry = { type: "custom_message", customType: "mecha-agent-message", details: {
      message_id: message.id, ...(persistedItems === "missing" ? {} : { delivery_items: persistedItems }),
    } };
    const coordinator = coverageCoordinator({
      sessionContext: { sessionManager: coverageSessionManager({ branch: branch ? [entry] : [] }) },
      client: { sendReceipt: (type) => receipts.push(type) },
      readSessionFile: () => `${JSON.stringify(entry)}\n`,
    });
    coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2, items: deliveryItems });
    coordinator.close();
  };
  run("missing");
  run("missing", [item, { ...item, messageId: "second" }]);
  run([], [item]);
  run([{ ...item, messageId: "other" }]);
  run([{ ...item, messageIncarnationId: "other" }]);
  run([{ ...item, messageId: 1 }]);
  run([item, { ...item, messageId: "second" }], [item]);
  run([item, { ...item, messageId: "second" }], [item, { ...item, messageId: "second" }]);
  run([item], [item], false);

  const branchEntry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: message.id, delivery_items: [item] } };
  const unrelatedEntry = { type: "custom_message", customType: "mecha-agent-message", details: { message_id: "other" } };
  const mismatchedHistory = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [branchEntry] }) },
    readSessionFile: () => `${JSON.stringify(unrelatedEntry)}\n`,
  });
  mismatchedHistory.onEnvelope(message, { messageId: message.id, attemptNumber: 1, items: [item] });
  mismatchedHistory.close();

  assert.deepEqual(receipts, [
    "injection_requested", "ack", "injection_requested",
  ]);
});

test("legacy mailbox history needs the current message incarnation before retry acknowledgement", () => {
  const message = { ...sentinelMessage, id: "legacy-message", sentinel: undefined };
  const deliveryItem = {
    messageId: message.id, messageIncarnationId: "incarnation-current", attemptNumber: 2, deliveryExpiresAt: "expiry-current",
  };
  const legacyEntry = {
    type: "custom_message",
    customType: "mecha-agent-message",
    details: { message_id: message.id, message_incarnation_id: "incarnation-current" },
  };
  const receipts = [];
  const coordinator = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [legacyEntry] }) },
    client: { sendReceipt: (type) => receipts.push(type) },
    readSessionFile: () => `${JSON.stringify(legacyEntry)}\n`,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2, items: [deliveryItem] });

  assert.deepEqual(receipts, ["injection_requested", "ack"]);
  coordinator.close();

  const topLevelLegacyReceipts = [];
  const topLevelLegacyEntry = {
    type: "custom_message",
    customType: "mecha-agent-message",
    message_incarnation_id: "incarnation-current",
    details: { message_id: message.id },
  };
  const topLevelLegacy = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [topLevelLegacyEntry] }) },
    client: { sendReceipt: (type) => topLevelLegacyReceipts.push(type) },
    readSessionFile: () => `${JSON.stringify(topLevelLegacyEntry)}\n`,
  });
  topLevelLegacy.onEnvelope(message, { messageId: message.id, attemptNumber: 2, items: [deliveryItem] });
  assert.deepEqual(topLevelLegacyReceipts, ["injection_requested", "ack"]);
  topLevelLegacy.close();

  const reusedIdReceipts = [];
  const reusedIdInjections = [];
  const reusedIdEntry = { ...legacyEntry, details: { ...legacyEntry.details, message_incarnation_id: "incarnation-old" } };
  const reusedId = coverageCoordinator({
    pi: { sendMessage: (injection) => reusedIdInjections.push(injection) },
    sessionContext: { sessionManager: coverageSessionManager({ branch: [reusedIdEntry] }) },
    client: { sendReceipt: (type) => reusedIdReceipts.push(type) },
    readSessionFile: () => `${JSON.stringify(reusedIdEntry)}\n`,
  });
  reusedId.onEnvelope(message, { messageId: message.id, attemptNumber: 1, items: [{ ...deliveryItem, messageIncarnationId: "incarnation-new" }] });
  assert.deepEqual(reusedIdReceipts, []);
  assert.deepEqual(reusedIdInjections, []);
  reusedId.close();

  const emptyDeliveryReceipts = [];
  const emptyDelivery = coverageCoordinator({
    sessionContext: { sessionManager: coverageSessionManager({ branch: [reusedIdEntry] }) },
    client: { sendReceipt: (type) => emptyDeliveryReceipts.push(type) },
    readSessionFile: () => `${JSON.stringify(reusedIdEntry)}\n`,
  });
  emptyDelivery.onEnvelope(message, { messageId: message.id, attemptNumber: 1, items: [] });
  assert.deepEqual(emptyDeliveryReceipts, []);
  emptyDelivery.close();
});
