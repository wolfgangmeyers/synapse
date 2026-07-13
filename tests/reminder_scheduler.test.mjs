import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const intervalMs = 15 * 60 * 1000;

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
