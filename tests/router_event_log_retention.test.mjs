import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const HOUR_MS = 60 * 60 * 1000;

async function withRouter(t, clock, options = {}) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sweep-events-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock });
  t.after(() => repository.close());
  const events = [];
  const router = createAgentRouter({
    repository,
    clock,
    eventLogger: (event, fields) => events.push({ event, ...fields }),
    ...options,
  });
  return { repository, router, events };
}

const sweepRecords = (events) => events.filter(({ event }) => event === "lifecycle_sweep_completed");

test("an idle lifecycle sweep logs nothing and a working sweep still logs its counts", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { router, events } = await withRouter(t, () => new Date(now));

  // The sweep runs on a timer whether or not there is work, so an all-zero record is
  // a heartbeat: log volume with no signal.
  for (let index = 0; index < 5; index += 1) router.sweepMessageLifecycles();
  assert.deepEqual(events.filter(({ event }) => event === "lifecycle_sweep_completed"), []);

  // A sweep that actually did something must still be logged, in full. Queueing for
  // an agent that never connects leaves an hour-stale agent for the sweep to clean up.
  router.enqueueMessage({
    id: "unclaimed-message",
    from: "sender",
    to: "worker",
    body: "durable body",
    created_at: new Date(now).toISOString(),
  });
  now += 2 * 60 * 60 * 1000;
  const result = router.sweepMessageLifecycles();
  assert.deepEqual(result.staleAgents, ["worker"]);

  assert.deepEqual(events.filter(({ event }) => event === "lifecycle_sweep_completed"), [{
    event: "lifecycle_sweep_completed",
    stale_agent_count: 1,
    expired_message_count: 0,
    timed_out_delivery_count: 0,
    reminder_success_count: 0,
    reminder_failure_count: 0,
    server_job_success_count: 0,
    server_job_failure_count: 0,
  }]);

  // And the next idle sweep goes back to silence.
  router.sweepMessageLifecycles();
  assert.equal(events.filter(({ event }) => event === "lifecycle_sweep_completed").length, 1);
});

test("an idle router still records one sweep heartbeat an hour", async (t) => {
  // Suppressing every idle record would make an idle router and a router whose sweep
  // loop has wedged byte-identical in the log. One record an hour keeps "nothing for
  // over an hour" meaningful without reintroducing the volume.
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { router, events } = await withRouter(t, () => new Date(now));

  // A full hour of one-second sweeps, sampled every minute: still silent.
  router.sweepMessageLifecycles();
  for (let minute = 0; minute < 59; minute += 1) {
    now += 60 * 1000;
    router.sweepMessageLifecycles();
  }
  assert.deepEqual(sweepRecords(events), []);

  now += 60 * 1000;
  router.sweepMessageLifecycles();
  assert.deepEqual(sweepRecords(events), [{
    event: "lifecycle_sweep_completed",
    stale_agent_count: 0,
    expired_message_count: 0,
    timed_out_delivery_count: 0,
    reminder_success_count: 0,
    reminder_failure_count: 0,
    server_job_success_count: 0,
    server_job_failure_count: 0,
  }], "the heartbeat is an ordinary all-zero sweep record");

  // Twenty-four hours of one-second sweeps cost 24 records, not 86,400.
  for (let hour = 0; hour < 24; hour += 1) {
    for (let step = 0; step < 60; step += 1) {
      now += 60 * 1000;
      router.sweepMessageLifecycles();
    }
  }
  assert.equal(sweepRecords(events).length, 25);
});

test("real sweep work resets the heartbeat interval instead of adding to it", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository, router, events } = await withRouter(t, () => new Date(now));

  // Hold a long lease and drain each delivery, so the only work any sweep sees is the
  // reminder itself — no stale agent and no expired message trailing behind it.
  const identity = { agentId: "worker", instanceId: "worker-instance", leaseId: "worker-lease" };
  repository.acquireAgentLease({ ...identity, leaseDurationMs: 100 * HOUR_MS });

  const fireReminder = (id) => {
    repository.createReminder({
      id,
      target: identity.agentId,
      creator: "supervisor",
      subject: "Reminder",
      body: "Do the thing.",
      created_at: new Date(now).toISOString(),
      next_due_at: new Date(now).toISOString(),
      interval_ms: null,
    });
    router.sweepMessageLifecycles();
    const delivery = repository.claimNextMessage(identity);
    const receipt = { ...identity, messageId: delivery.message.id, attemptNumber: delivery.attempt_number };
    repository.recordInjectionRequested(receipt);
    repository.acknowledgeDelivery(receipt);
  };

  // Work just under the heartbeat interval, repeatedly: only the work is recorded.
  for (let index = 0; index < 3; index += 1) {
    now += HOUR_MS - 1000;
    fireReminder(`reminder-${index}`);
  }
  assert.equal(sweepRecords(events).length, 3);
  assert.deepEqual(sweepRecords(events).map(({ reminder_success_count }) => reminder_success_count), [1, 1, 1]);

  // Idle for less than an hour after that work: still nothing.
  now += HOUR_MS - 1000;
  router.sweepMessageLifecycles();
  assert.equal(sweepRecords(events).length, 3);

  // An hour after the last record, the heartbeat resumes.
  now += 1000;
  router.sweepMessageLifecycles();
  assert.equal(sweepRecords(events).length, 4);
});

test("the sweep heartbeat interval is validated like the other router intervals", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sweep-validate-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  assert.throws(
    () => createAgentRouter({ repository, sweepHeartbeatIntervalMs: 0 }),
    /sweepHeartbeatIntervalMs/,
  );
});

test("a sweep that did work other than stale-agent cleanup is still logged", async (t) => {
  // Guards against narrowing the suppression to one count: a sweep that fired a
  // reminder, expired a message or timed out a delivery is exactly the record you
  // want when mail goes missing, and none of those touch stale_agent_count.
  const dueAt = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository, router, events } = await withRouter(t, () => new Date(dueAt));

  repository.createReminder({
    id: "sweep-reminder",
    target: "worker",
    creator: "supervisor",
    subject: "Reminder",
    body: "Do the thing.",
    created_at: new Date(dueAt).toISOString(),
    next_due_at: new Date(dueAt).toISOString(),
    interval_ms: null,
  });

  const result = router.sweepMessageLifecycles();
  assert.equal(result.reminders.length, 1);
  assert.equal(result.staleAgents.length, 0);

  assert.deepEqual(events.filter(({ event }) => event === "lifecycle_sweep_completed"), [{
    event: "lifecycle_sweep_completed",
    stale_agent_count: 0,
    expired_message_count: 0,
    timed_out_delivery_count: 0,
    reminder_success_count: 1,
    reminder_failure_count: 0,
    server_job_success_count: 0,
    server_job_failure_count: 0,
  }]);
});
