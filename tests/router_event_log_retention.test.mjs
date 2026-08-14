import assert from "node:assert/strict";
import { DatabaseSync } from "node:sqlite";
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
  return { repository, router, events, databasePath: join(directory, "router.sqlite") };
}

const sweepRecords = (events) => events.filter(({ event }) => event === "lifecycle_sweep_completed");

function lifecycleSnapshot(sequence, eventId, children, overrides = {}) {
  return {
    agent_id: "retention-parent", instance_id: "retention-instance", parent_session_id: "retention-session",
    lease_id: "retention-lease", producer_epoch: "11111111-1111-4111-8111-111111111111",
    client_sequence: sequence, event_id: eventId, subagents: children, ...overrides,
  };
}

test("prunes lifecycle-owned retention without deleting live running state", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository } = await withRouter(t, () => new Date(now));
  repository.acquireAgentLease({
    agentId: "retention-parent", instanceId: "retention-instance", leaseId: "retention-lease",
    leaseDurationMs: 9 * 24 * HOUR_MS,
  });
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "live", [{ id: "live-child", description: "Live child" }]));
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "terminal", [], {
    agent_id: "terminal-parent", instance_id: "terminal-instance", parent_session_id: "terminal-session",
    lease_id: "terminal-lease", producer_epoch: "22222222-2222-4222-8222-222222222222",
  }));
  repository.applySubagentLifecycleEvent({
    ...lifecycleSnapshot(2, "terminal-event", [], {
      agent_id: "terminal-parent", instance_id: "terminal-instance", parent_session_id: "terminal-session",
      lease_id: "terminal-lease", producer_epoch: "22222222-2222-4222-8222-222222222222",
    }), lifecycle_event: "terminal", subagent: { id: "old-child", description: "Old child" }, terminal_reason: "completed",
  });
  now += 8 * 24 * HOUR_MS;
  const pruned = repository.pruneSubagentLifecycle();
  assert.ok(pruned.tombstones >= 1);
  assert.ok(pruned.ledger >= 1);
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "retention-parent" }).agents[0].subagent_count, 1);
  assert.equal(repository.replaySubagentLifecycle({ afterVersion: 0 }).mode, "snapshot");
});

test("falls back to a snapshot for a pruned lifecycle cursor", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository } = await withRouter(t, () => new Date(now));
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "running", [{ id: "child", description: "Child" }]));
  now += 8 * 24 * HOUR_MS;
  repository.pruneSubagentLifecycle();
  const replay = repository.replaySubagentLifecycle({ afterVersion: 0 });
  assert.equal(replay.mode, "snapshot");
  assert.equal(replay.router_version, 2);
});

test("keeps terminal tombstones through the 24-hour window and rejects delayed starts", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository } = await withRouter(t, () => new Date(now));
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "empty", []));
  repository.applySubagentLifecycleEvent({
    ...lifecycleSnapshot(2, "terminal", []), lifecycle_event: "terminal",
    subagent: { id: "child", description: "Child" }, terminal_reason: "completed",
  });
  now += 24 * HOUR_MS - 1;
  assert.equal(repository.pruneSubagentLifecycle().tombstones, 0);
  assert.equal(repository.applySubagentLifecycleEvent({
    ...lifecycleSnapshot(3, "delayed-start", []), lifecycle_event: "started",
    subagent: { id: "child", description: "Child" },
  }).disposition, "accepted_noop");
  now += 1;
  assert.equal(repository.pruneSubagentLifecycle().tombstones, 1);
});

test("expires disconnected running children into tombstones at seven days, then removes their producer", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository, databasePath, router, events } = await withRouter(t, () => new Date(now));
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "running", [{ id: "stale-child", description: "Stale child" }]));

  now += 7 * 24 * HOUR_MS - 1;
  assert.equal(repository.pruneSubagentLifecycle().expiredRunning, 0);
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "retention-parent" }).agents[0].subagent_count, 1);

  now += 1;
  const sweep = router.sweepMessageLifecycles();
  assert.equal(sweep.lifecycle.expiredRunning, 1);
  assert.equal(sweepRecords(events).at(-1).lifecycle_running_expired_count, 1);
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "retention-parent" }).agents[0].subagent_count, 0);
  const database = new DatabaseSync(databasePath, { readOnly: true });
  t.after(() => database.close());
  const tombstone = database.prepare(`
    SELECT state, terminal_reason
    FROM subagent_lifecycle_state
    WHERE parent_agent_id = 'retention-parent' AND subagent_id = 'stale-child'
  `).get();
  assert.equal(tombstone.state, "terminal");
  assert.equal(tombstone.terminal_reason, "producer_expired");
  assert.notEqual(repository.subagentLifecycleProducerFor("retention-parent"), undefined);

  now += 24 * HOUR_MS - 1;
  assert.equal(repository.pruneSubagentLifecycle().tombstones, 0);
  now += 1;
  const removedTombstone = repository.pruneSubagentLifecycle();
  assert.equal(removedTombstone.tombstones, 1);
  // The expiry itself is an accepted terminal transition. Its retained ledger
  // row keeps the producer until the normal seven-day event window ends.
  assert.equal(removedTombstone.producers, 0);
  assert.notEqual(repository.subagentLifecycleProducerFor("retention-parent"), undefined);

  now += 6 * 24 * HOUR_MS - 1;
  assert.equal(repository.pruneSubagentLifecycle().producers, 0);
  now += 1;
  const removedProducer = repository.pruneSubagentLifecycle();
  assert.equal(removedProducer.ledger, 1);
  assert.equal(removedProducer.producers, 1);
  assert.equal(repository.subagentLifecycleProducerFor("retention-parent"), undefined);
});

test("keeps only leases that remain live past the seven-day producer cutoff", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository } = await withRouter(t, () => new Date(now));
  const cutoffMs = 7 * 24 * HOUR_MS;
  const parents = [
    { agent: "lease-live-parent", lease: "lease-live", duration: cutoffMs + 1, epoch: "33333333-3333-4333-8333-333333333333" },
    { agent: "lease-expired-parent", lease: "lease-expired", duration: cutoffMs, epoch: "44444444-4444-4444-8444-444444444444" },
    { agent: "lease-missing-parent", lease: "lease-missing", duration: undefined, epoch: "55555555-5555-4555-8555-555555555555" },
  ];
  for (const parent of parents) {
    if (parent.duration !== undefined) {
      repository.acquireAgentLease({
        agentId: parent.agent, instanceId: `${parent.agent}-instance`, leaseId: parent.lease,
        leaseDurationMs: parent.duration,
      });
    }
    repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, `${parent.agent}-running`, [{ id: "child", description: "Child" }], {
      agent_id: parent.agent,
      instance_id: `${parent.agent}-instance`,
      parent_session_id: `${parent.agent}-session`,
      lease_id: parent.lease,
      producer_epoch: parent.epoch,
    }));
  }

  now += cutoffMs;
  assert.ok(new Date(repository.leaseFor("lease-live-parent").expires_at).getTime() > now);
  assert.ok(new Date(repository.leaseFor("lease-expired-parent").expires_at).getTime() <= now);
  const pruned = repository.pruneSubagentLifecycle();
  assert.equal(pruned.expiredRunning, 2);
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "lease-live-parent" }).agents[0].subagent_count, 1);
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "lease-expired-parent" }).agents[0].subagent_count, 0);
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "lease-missing-parent" }).agents[0].subagent_count, 0);
});

test("prunes lifecycle ledger and disconnected producers at the exact seven-day boundary", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository } = await withRouter(t, () => new Date(now));
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "empty", []));
  repository.applySubagentLifecycleEvent({
    ...lifecycleSnapshot(2, "terminal", []), lifecycle_event: "terminal",
    subagent: { id: "child", description: "Child" }, terminal_reason: "completed",
  });
  now += 7 * 24 * HOUR_MS;
  const pruned = repository.pruneSubagentLifecycle();
  assert.equal(pruned.ledger, 1);
  assert.equal(pruned.producers, 1);
});

test("retains a lifecycle producer for a live lease after seven days", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository } = await withRouter(t, () => new Date(now));
  repository.acquireAgentLease({
    agentId: "retention-parent", instanceId: "retention-instance", leaseId: "retention-lease",
    leaseDurationMs: 9 * 24 * HOUR_MS,
  });
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "empty", []));
  now += 8 * 24 * HOUR_MS;
  assert.equal(repository.pruneSubagentLifecycle().producers, 0);
  assert.notEqual(repository.subagentLifecycleProducerFor("retention-parent"), undefined);
  now += 2 * 24 * HOUR_MS;
  assert.equal(repository.pruneSubagentLifecycle().producers, 1);
});

test("caps the lifecycle ledger at 100000 newest rows", async (t) => {
  const { repository, databasePath } = await withRouter(t, () => new Date("2030-01-01T00:00:00.000Z"));
  // Seed the isolated fixture through its own SQLite connection so this checks
  // the production retention query without making the repository expose SQL.
  const database = new DatabaseSync(databasePath);
  t.after(() => database.close());
  const receivedAt = "2030-01-01T00:00:00.000Z";
  const insert = database.prepare(`
    INSERT INTO subagent_lifecycle_events (
      parent_agent_id, instance_id, parent_session_id, producer_epoch, subagent_id,
      event_id, client_sequence, event_kind, state, type, description, terminal_reason,
      payload_hash, received_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 'snapshot', 'running', NULL, NULL, NULL, 'hash', ?)
  `);
  database.exec("BEGIN IMMEDIATE");
  try {
    for (let sequence = 1; sequence <= 100001; sequence += 1) {
      insert.run("cap-parent", "cap-instance", "cap-session", "33333333-3333-4333-8333-333333333333", "cap-child", `cap-${sequence}`, sequence, receivedAt);
    }
    database.exec("COMMIT");
  } catch (error) {
    database.exec("ROLLBACK");
    throw error;
  }
  assert.equal(repository.pruneSubagentLifecycle().ledger, 1);
  assert.equal(database.prepare("SELECT COUNT(*) AS count FROM subagent_lifecycle_events").get().count, 100000);
  assert.equal(database.prepare("SELECT MIN(client_sequence) AS sequence FROM subagent_lifecycle_events").get().sequence, 2);
});

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
