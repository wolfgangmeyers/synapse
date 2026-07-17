import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  openMessageRepository,
  ReminderIdempotencyConflictError,
  ReminderValidationError,
  validateReminder,
} from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

function sentinelReminder(sentinel = { receiver_policy: "managed", model: "gpt-5.6-terra" }) {
  return {
    id: "sentinel-reminder",
    target: "receiver",
    creator: "supervisor",
    body: "secret",
    created_at: "2030-01-01T00:00:00.000Z",
    next_due_at: "2030-01-01T00:00:00.000Z",
    interval_ms: null,
    sentinel,
  };
}

test("reminder input validates the optional Sentinel descriptor", () => {
  assert.throws(
    () => validateReminder(sentinelReminder({ receiver_policy: "managed" })),
    ReminderValidationError,
  );
  assert.throws(
    () => validateReminder(sentinelReminder({ receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "forged" })),
    ReminderValidationError,
  );
});

test("reminder idempotency includes Sentinel metadata without key-order sensitivity", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-reminder-metadata-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());

  assert.equal(repository.createReminder(sentinelReminder()).created, true);
  assert.equal(repository.createReminder(sentinelReminder({
    model: "gpt-5.6-terra",
    receiver_policy: "managed",
  })).created, false);
  assert.throws(
    () => repository.createReminder(sentinelReminder({
      receiver_policy: "managed",
      model: "gpt-5.7-terra",
    })),
    ReminderIdempotencyConflictError,
  );
});

test("Sentinel reminder metadata survives a repository restart before firing", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-reminder-restart-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const databasePath = join(directory, "router.sqlite");
  const clock = () => new Date("2030-01-01T00:00:00.000Z");
  const created = openMessageRepository({ databasePath, clock });
  created.createReminder(sentinelReminder());
  created.close();

  const repository = openMessageRepository({ databasePath, clock });
  t.after(() => repository.close());
  assert.deepEqual(repository.reminderFor("sentinel-reminder").sentinel, {
    receiver_policy: "managed",
    model: "gpt-5.6-terra",
  });
  const fired = repository.fireDueReminders()[0];
  assert.equal(fired.message.from, "reminders");
  assert.deepEqual(fired.message.sentinel, {
    receiver_policy: "managed",
    model: "gpt-5.6-terra",
    origin_sender: "supervisor",
  });
  assert.deepEqual(repository.messageFor(fired.message.id).sentinel, {
    receiver_policy: "managed",
    model: "gpt-5.6-terra",
    origin_sender: "supervisor",
  });
});

test("local receiver policy admits matching Sentinel messages and reminders without bearer identity", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-reminder-api-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({
    repository,
    sentinelReceiverPolicies: {
      managed: { receiver: "receiver", senders: ["supervisor"], models: ["gpt-5.6-terra"], execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: [] } },
    },
  });
  t.after(async () => {
    await router.close();
    repository.close();
  });
  const routerUrl = new URL(await router.listen());
  routerUrl.protocol = "http:";
  const baseUrl = routerUrl.origin;

  const allowed = await fetch(`${baseUrl}/v1/reminders`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ ...sentinelReminder(), next_due_at: "2030-01-02T00:00:00.000Z" }),
  });
  assert.equal(allowed.status, 201);

  const rejected = await fetch(`${baseUrl}/v1/reminders`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      ...sentinelReminder(),
      id: "untrusted-sentinel-reminder",
      creator: "untrusted",
      next_due_at: "2030-01-02T00:00:00.000Z",
    }),
  });
  assert.equal(rejected.status, 400);
  assert.equal((await rejected.json()).error.code, "invalid_sentinel_policy");

  const allowedMessage = await fetch(`${baseUrl}/v1/messages`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      id: "allowed-sentinel-message",
      from: "supervisor",
      to: "receiver",
      body: "opaque",
      created_at: "2030-01-01T00:00:00.000Z",
      sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
    }),
  });
  assert.equal(allowedMessage.status, 201);

  const rejectedMessage = await fetch(`${baseUrl}/v1/messages`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      id: "rejected-sentinel-message",
      from: "untrusted",
      to: "receiver",
      body: "opaque",
      created_at: "2030-01-01T00:00:00.000Z",
      sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
    }),
  });
  assert.equal(rejectedMessage.status, 400);
  assert.equal((await rejectedMessage.json()).error.code, "invalid_sentinel_policy");
});

test("local existing message and reminder APIs create one-shot and recurring Sentinel work", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-authenticated-api-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const now = { value: "2030-01-01T00:00:00.000Z" };
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock: () => new Date(now.value) });
  const router = createAgentRouter({
    repository,
    sentinelReceiverPolicies: {
      managed: { receiver: "receiver", senders: ["supervisor"], models: ["gpt-5.6-terra"], execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: [] } },
    },
  });
  t.after(async () => { await router.close(); repository.close(); });
  const url = new URL(await router.listen());
  url.protocol = "http:";
  const headers = { "content-type": "application/json" };
  const oneShot = await fetch(`${url.origin}/v1/reminders`, {
    method: "POST", headers,
    body: JSON.stringify({ ...sentinelReminder(), id: "authenticated-one-shot", next_due_at: "2030-01-02T00:00:00.000Z" }),
  });
  assert.equal(oneShot.status, 201);
  const recurring = await fetch(`${url.origin}/v1/reminders`, {
    method: "POST", headers,
    body: JSON.stringify({ ...sentinelReminder(), id: "authenticated-recurring", interval_ms: 60_000, next_due_at: "2030-02-02T00:01:00.000Z" }),
  });
  assert.equal(recurring.status, 201);
  const message = await fetch(`${url.origin}/v1/messages`, {
    method: "POST", headers,
    body: JSON.stringify({
      id: "authenticated-message", from: "supervisor", to: "receiver", body: "opaque",
      created_at: "2030-01-01T00:00:00.000Z", sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
    }),
  });
  assert.equal(message.status, 201);
  assert.deepEqual((await message.json()).message.sentinel, {
    receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor",
  });
  now.value = "2030-02-02T00:00:00.000Z";
  repository.acquireAgentLease({ agentId: "receiver", instanceId: "instance", leaseId: "lease", leaseDurationMs: 600_000 });
  const oneShotFired = repository.fireDueReminders();
  assert.equal(oneShotFired.length, 1);
  const delivered = repository.claimNextMessage({ agentId: "receiver", instanceId: "instance", leaseId: "lease", deliveryLeaseDurationMs: 60_000 });
  repository.recordInjectionRequested({ agentId: "receiver", instanceId: "instance", leaseId: "lease", messageId: delivered.message.id, attemptNumber: delivered.attempt_number });
  repository.acknowledgeDelivery({ agentId: "receiver", instanceId: "instance", leaseId: "lease", messageId: delivered.message.id, attemptNumber: delivered.attempt_number });
  now.value = "2030-02-02T00:01:00.000Z";
  const recurringFired = repository.fireDueReminders();
  assert.equal(recurringFired.length, 1);
  assert.deepEqual([oneShotFired[0].message.sentinel, recurringFired[0].message.sentinel], [
    { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" },
    { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" },
  ]);
});
