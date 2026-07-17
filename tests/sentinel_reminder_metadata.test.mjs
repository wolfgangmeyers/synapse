import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository, ReminderIdempotencyConflictError, validateReminder } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

function sentinelReminder(overrides = {}) {
  return {
    id: "sentinel-reminder", target: "receiver", creator: "unknown-sender", body: "private prompt",
    created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:00:00.000Z",
    interval_ms: null, sentinel: { model: "gpt-5.6-terra" }, ...overrides,
  };
}

test("Sentinel reminder metadata is model-only and survives firing", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-reminder-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date("2030-01-01T00:00:00.000Z"),
  });
  t.after(() => repository.close());

  assert.deepEqual(validateReminder(sentinelReminder()).sentinel, { model: "gpt-5.6-terra" });
  assert.equal(repository.createReminder(sentinelReminder()).created, true);
  assert.equal(repository.createReminder(sentinelReminder()).created, false);
  assert.throws(() => repository.createReminder(sentinelReminder({ sentinel: { model: "other" } })), ReminderIdempotencyConflictError);
  const fired = repository.fireDueReminders()[0];
  assert.deepEqual(fired.message.sentinel, { model: "gpt-5.6-terra" });
});

test("any local sender can schedule immediate, one-time, and recurring Sentinel work for a target", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-admission-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const now = { value: "2030-01-01T00:00:00.000Z" };
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock: () => new Date(now.value) });
  const router = createAgentRouter({ repository });
  t.after(async () => { await router.close(); repository.close(); });
  const url = new URL(await router.listen());
  url.protocol = "http:";
  const headers = { "content-type": "application/json" };
  const sentinel = { model: "not-policy-selected" };

  const immediate = await fetch(`${url.origin}/v1/messages`, { method: "POST", headers, body: JSON.stringify({
    id: "immediate", from: "unknown-sender", to: "other-worker", body: "opaque", created_at: now.value, sentinel,
  }) });
  assert.equal(immediate.status, 201);
  const oneTime = await fetch(`${url.origin}/v1/reminders`, { method: "POST", headers, body: JSON.stringify(sentinelReminder({
    id: "one-time", target: "other-worker", next_due_at: "2030-01-02T00:00:00.000Z", sentinel,
  })) });
  assert.equal(oneTime.status, 201);
  const recurring = await fetch(`${url.origin}/v1/reminders`, { method: "POST", headers, body: JSON.stringify(sentinelReminder({
    id: "recurring", target: "other-worker", next_due_at: "2030-01-02T00:01:00.000Z", interval_ms: 60_000, sentinel,
  })) });
  assert.equal(recurring.status, 201);
});
