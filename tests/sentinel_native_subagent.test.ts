import assert from "node:assert/strict";
import test from "node:test";

import {
  captureNativeSentinelSpawner,
  createPiMailboxDeliveryCoordinator,
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

  assert.equal(calls.length, 1);
  assert.deepEqual(receipts.map(({ type }) => type), ["injection_requested", "ack"]);
  assert.equal(sent.length, 1);
  assert.deepEqual(sent[0].message.details, {
    message_id: sentinelMessage.id, subagent_id: "native-child", state: "handed_off",
  });
  assert.equal(sent[0].message.display, false);
  assert.doesNotMatch(JSON.stringify(sent), /private scheduled prompt/);
  assert.doesNotMatch(JSON.stringify(sent), /caller-selected-model/);
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
