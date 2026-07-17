import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const intervalMs = 15 * 60 * 1000;

test("Sentinel claims are idempotent and retryable before launch", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-claim-"));
  t.after(async () => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  repository.enqueue({ id: "sentinel-message", from: "supervisor", to: "receiver", body: "opaque prompt", created_at: "2030-01-01T00:00:00.000Z", sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" } });
  assert.throws(() => repository.claimSentinel("sentinel-message", "missing-deadline", "attempt"), /launchDeadlineAt/);
  const deadline = "2030-01-01T00:01:00.000Z";
  const claimed = repository.claimSentinel("sentinel-message", "occurrence-1", "attempt-1", { launchDeadlineAt: deadline });
  assert.deepEqual(claimed, { occurrence_id: "occurrence-1", message_id: "sentinel-message", state: "claimed", attempt_id: "attempt-1", transition_sequence: 1 });
  assert.throws(
    () => repository.claimSentinel("sentinel-message", "occurrence-1", "other-attempt", { launchDeadlineAt: deadline }),
    /belongs to another delivery/,
  );
  assert.equal(repository.transitionSentinel("occurrence-1", "attempt-1", "launching").state, "launching");
  assert.equal(repository.transitionSentinel("occurrence-1", "attempt-1", "spawn_failed").state, "retryable");
  assert.equal(repository.sentinelState("occurrence-1"), undefined);
  assert.equal(repository.claimSentinel("sentinel-message", "occurrence-retry", "attempt-retry", { launchDeadlineAt: deadline }).state, "claimed");
});

test("a launching Sentinel attempt expires into a non-retryable tombstone", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-launch-"));
  t.after(async () => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  repository.enqueue({ id: "sentinel-launch", from: "supervisor", to: "receiver", body: "opaque", created_at: "2030-01-01T00:00:00.000Z", sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" } });
  repository.claimSentinel("sentinel-launch", "occurrence-2", "attempt-2", { launchDeadlineAt: "2000-01-01T00:00:00.000Z" });
  repository.transitionSentinel("occurrence-2", "attempt-2", "launching");
  const launching = repository.transitionSentinel("occurrence-2", "attempt-2", "launching");
  assert.equal(launching.state, "launching");
  assert.equal(repository.recoverSentinelDeadlines("2030-01-01T00:00:00.000Z"), 1);
  assert.equal(repository.sentinelState("occurrence-2").state, "failed");
  assert.equal(repository.sentinelState("occurrence-2").message_id, "sentinel-launch");
  assert.equal(repository.messageFor("sentinel-launch").state, "dead_letter");
  assert.equal(repository.messageFor("sentinel-launch").body, "");
  assert.equal(repository.claimSentinel("sentinel-launch", "replay", "replay-attempt", { launchDeadlineAt: "2030-01-01T00:01:00.000Z" }), undefined);
  assert.equal(repository.retryDeadLetter("sentinel-launch"), undefined);
  assert.equal(repository.cleanupSentinelTombstones("2030-01-02T00:00:01.000Z"), 1);
  assert.equal(repository.sentinelState("occurrence-2"), undefined);
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
