import assert from "node:assert/strict";
import { DatabaseSync } from "node:sqlite";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const intervalMs = 15 * 60 * 1000;

async function withReminderRepository(t, clock) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-reminder-scheduler-"));
  t.after(async () => {
    await rm(directory, { recursive: true, force: true });
  });
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock,
  });
  t.after(() => repository.close());
  return repository;
}

test("dead-lettered recurring reminders rearm to a future scheduled occurrence", async (t) => {
  const initialDueAt = Date.parse("2030-01-01T00:00:00.000Z");
  let now = initialDueAt;
  const events = [];
  const repository = await withReminderRepository(t, () => new Date(now));
  const router = createAgentRouter({
    repository,
    clock: () => new Date(now),
    maxDeliveryAttempts: 1,
    eventLogger: (event, fields) => events.push({ event, ...fields }),
  });
  const identity = {
    agentId: "pr-watch",
    instanceId: "pr-watch-instance",
    leaseId: "pr-watch-lease",
  };

  repository.acquireAgentLease({ ...identity, leaseDurationMs: 60 * 60 * 1000 });
  repository.createReminder({
    id: "pr-watch-wakeup",
    target: identity.agentId,
    creator: "supervisor",
    subject: "PR watch wakeup",
    body: "Run one PR watch scan now.",
    created_at: new Date(initialDueAt).toISOString(),
    next_due_at: new Date(initialDueAt).toISOString(),
    interval_ms: intervalMs,
  });

  const [fired] = router.sweepDueReminders();
  assert.equal(fired.message.id, "reminder-pr-watch-wakeup-0");
  const delivery = repository.claimNextMessage(identity);
  repository.recordInjectionRequested({
    ...identity,
    messageId: delivery.message.id,
    attemptNumber: delivery.attempt_number,
  });

  now = initialDueAt + (2 * intervalMs) + 1;
  const [timedOut] = router.sweepDeliveryTimeouts();

  assert.equal(timedOut.state, "dead_letter");
  const reminder = repository.reminderFor("pr-watch-wakeup");
  assert.equal(reminder.state, "scheduled");
  assert.equal(reminder.linked_message_id, null);
  assert.equal(reminder.next_due_at, "2030-01-01T00:45:00.000Z");
  assert.ok(reminder.next_due_at > new Date(now).toISOString());
  assert.deepEqual(events.filter(({ event }) => event === "recurring_reminder_rearmed"), [{
    event: "recurring_reminder_rearmed",
    reminder_id: "pr-watch-wakeup",
    message_id: "reminder-pr-watch-wakeup-0",
    next_due_at: "2030-01-01T00:45:00.000Z",
    reason: "delivery_timeout",
  }]);
});

test("failed one-shot reminders remain cancelable through the documented delete path", async (t) => {
  const initialDueAt = Date.parse("2030-01-01T00:00:00.000Z");
  let now = initialDueAt;
  const repository = await withReminderRepository(t, () => new Date(now));
  const identity = {
    agentId: "supervisor",
    instanceId: "supervisor-instance",
    leaseId: "supervisor-lease",
  };

  repository.acquireAgentLease({ ...identity, leaseDurationMs: 60 * 60 * 1000 });
  repository.createReminder({
    id: "one-shot",
    target: identity.agentId,
    creator: "supervisor",
    body: "wake up",
    created_at: new Date(initialDueAt).toISOString(),
    next_due_at: new Date(initialDueAt).toISOString(),
    interval_ms: null,
  });
  repository.fireDueReminders();
  const delivery = repository.claimNextMessage(identity);
  repository.recordInjectionRequested({
    ...identity,
    messageId: delivery.message.id,
    attemptNumber: delivery.attempt_number,
  });
  repository.nackDelivery({
    ...identity,
    messageId: delivery.message.id,
    attemptNumber: delivery.attempt_number,
    reason: "delivery_failed",
    maxDeliveryAttempts: 1,
  });

  assert.equal(repository.reminderFor("one-shot").state, "failed");
  assert.equal(repository.cancelReminder("one-shot").canceled, true);
  assert.equal(repository.reminderFor("one-shot"), undefined);
});

test("cancel-all repairs failed reminders alongside scheduled ones", async (t) => {
  const initialDueAt = Date.parse("2030-01-01T00:00:00.000Z");
  let now = initialDueAt;
  const repository = await withReminderRepository(t, () => new Date(now));
  const identity = {
    agentId: "supervisor",
    instanceId: "supervisor-instance",
    leaseId: "supervisor-lease",
  };

  const oneShot = (id) => repository.createReminder({
    id,
    target: identity.agentId,
    creator: "supervisor",
    body: "wake up",
    created_at: new Date(initialDueAt).toISOString(),
    next_due_at: new Date(initialDueAt).toISOString(),
    interval_ms: null,
  });
  const claimAndRequestInjection = () => {
    const delivery = repository.claimNextMessage(identity);
    repository.recordInjectionRequested({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
    });
    return delivery;
  };

  repository.acquireAgentLease({ ...identity, leaseDurationMs: 60 * 60 * 1000 });
  oneShot("failing");
  repository.fireDueReminders();
  const failed = claimAndRequestInjection();
  repository.nackDelivery({
    ...identity,
    messageId: failed.message.id,
    attemptNumber: failed.attempt_number,
    reason: "delivery_failed",
    maxDeliveryAttempts: 1,
  });
  assert.equal(repository.reminderFor("failing").state, "failed");

  // A reminder that is genuinely mid-delivery must still block cancel-all.
  oneShot("pending");
  repository.fireDueReminders();
  const pending = claimAndRequestInjection();
  assert.throws(() => repository.cancelAllReminders(), { code: "reminder_not_cancelable" });

  repository.acknowledgeDelivery({
    ...identity,
    messageId: pending.message.id,
    attemptNumber: pending.attempt_number,
  });
  assert.equal(repository.reminderFor("pending"), undefined);
  assert.deepEqual(repository.cancelAllReminders(), { canceled: 1 });
  assert.equal(repository.reminderFor("failing"), undefined);
});

test("retrying an old failed recurring reminder does not leave its due time in the past", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-reminder-scheduler-"));
  t.after(async () => {
    await rm(directory, { recursive: true, force: true });
  });
  const databasePath = join(directory, "router.sqlite");
  const initialDueAt = Date.parse("2030-01-01T00:00:00.000Z");
  let now = initialDueAt;
  const repository = openMessageRepository({
    databasePath,
    clock: () => new Date(now),
  });
  t.after(() => repository.close());
  const identity = {
    agentId: "supervisor",
    instanceId: "supervisor-instance",
    leaseId: "supervisor-lease",
  };

  repository.acquireAgentLease({ ...identity, leaseDurationMs: 60 * 60 * 1000 });
  repository.createReminder({
    id: "old-recurring",
    target: identity.agentId,
    creator: "supervisor",
    body: "sweep",
    created_at: new Date(initialDueAt).toISOString(),
    next_due_at: new Date(initialDueAt).toISOString(),
    interval_ms: intervalMs,
  });
  repository.fireDueReminders();
  const delivery = repository.claimNextMessage(identity);
  repository.recordInjectionRequested({
    ...identity,
    messageId: delivery.message.id,
    attemptNumber: delivery.attempt_number,
  });
  repository.nackDelivery({
    ...identity,
    messageId: delivery.message.id,
    attemptNumber: delivery.attempt_number,
    reason: "delivery_failed",
    maxDeliveryAttempts: 1,
  });

  const database = new DatabaseSync(databasePath);
  database.prepare(`
    UPDATE reminders
    SET state = 'failed', linked_message_id = ?, next_due_at = ?
    WHERE id = ?
  `).run(delivery.message.id, "2030-01-01T00:15:00.000Z", "old-recurring");
  database.close();

  now = initialDueAt + (3 * intervalMs) + 1;
  repository.retryDeadLetter(delivery.message.id);

  const reminder = repository.reminderFor("old-recurring");
  assert.equal(reminder.state, "scheduled");
  assert.equal(reminder.linked_message_id, null);
  assert.equal(reminder.next_due_at, "2030-01-01T01:00:00.000Z");
  assert.ok(reminder.next_due_at > new Date(now).toISOString());
});

test("the live router fires every recurring slot despite normal sweep lateness and skips only missed intervals", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-reminder-scheduler-"));
  t.after(async () => {
    await rm(directory, { recursive: true, force: true });
  });

  const initialDueAt = Date.parse("2030-01-01T00:00:00.000Z");
  let now = initialDueAt;
  const events = [];
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date(now),
  });
  t.after(() => repository.close());
  const router = createAgentRouter({
    repository,
    clock: () => new Date(now),
    eventLogger: (event, fields) => events.push({ event, ...fields }),
  });
  const identity = {
    agentId: "pr-watch",
    instanceId: "pr-watch-instance",
    leaseId: "pr-watch-lease",
  };

  repository.acquireAgentLease({ ...identity, leaseDurationMs: 2 * intervalMs });
  repository.createReminder({
    id: "pr-watch-wakeup",
    target: identity.agentId,
    creator: "supervisor",
    subject: "PR watch wakeup",
    body: "Run one PR watch scan now.",
    created_at: new Date(initialDueAt).toISOString(),
    next_due_at: new Date(initialDueAt).toISOString(),
    interval_ms: intervalMs,
  });

  for (let occurrence = 0; occurrence < 3; occurrence += 1) {
    now = initialDueAt + (occurrence * intervalMs) + 250;
    const fired = router.sweepDueReminders();
    assert.deepEqual(fired.map(({ message }) => message.id), [
      `reminder-pr-watch-wakeup-${occurrence}`,
    ]);

    const delivery = repository.claimNextMessage(identity);
    repository.recordInjectionRequested({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
    });
    repository.acknowledgeDelivery({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
    });
    repository.renewAgentLease({ ...identity, leaseDurationMs: 2 * intervalMs });
  }

  assert.deepEqual(events.filter(({ event }) => event === "reminder_started").map((event) => ({
    reminderId: event.reminder_id,
    scheduledFor: event.scheduled_for,
  })), [
    { reminderId: "pr-watch-wakeup", scheduledFor: "2030-01-01T00:00:00.000Z" },
    { reminderId: "pr-watch-wakeup", scheduledFor: "2030-01-01T00:15:00.000Z" },
    { reminderId: "pr-watch-wakeup", scheduledFor: "2030-01-01T00:30:00.000Z" },
  ]);

  now = initialDueAt + (4 * intervalMs);
  assert.deepEqual(router.sweepDueReminders(), []);
  assert.deepEqual(repository.reminderFor("pr-watch-wakeup"), {
    id: "pr-watch-wakeup",
    target: "pr-watch",
    creator: "supervisor",
    subject: "PR watch wakeup",
    body: "Run one PR watch scan now.",
    created_at: "2030-01-01T00:00:00.000Z",
    next_due_at: "2030-01-01T01:15:00.000Z",
    interval_ms: intervalMs,
    state: "scheduled",
    linked_message_id: null,
    linked_message_state: null,
    occurrence_number: 3,
    attempt_count: 0,
    last_attempt_at: null,
  });
});
