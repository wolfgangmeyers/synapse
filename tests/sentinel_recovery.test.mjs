import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

async function setup(t, id) {
  const dir = await mkdtemp(join(tmpdir(), "mecha-sentinel-recovery-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const repo = openMessageRepository({ databasePath: join(dir, "db.sqlite") });
  t.after(() => repo.close());
  repo.enqueue({ id, from: "supervisor", to: "receiver", body: "secret", created_at: "2030-01-01T00:00:00.000Z", sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" } });
  const lease = { agentId: "receiver", instanceId: "instance", leaseId: "lease" };
  repo.acquireAgentLease({ ...lease, leaseDurationMs: 60_000 });
  repo.claimNextMessage({ ...lease });
  return { repo, lease };
}

test("expired claimed release reoffers the original body", async (t) => {
  const { repo, lease } = await setup(t, "claimed");
  repo.claimSentinel("claimed", "occ", "attempt", { launchDeadlineAt: "2000-01-01T00:00:00.000Z" });
  assert.equal(repo.recoverSentinelDeadlines("2030-01-01T00:00:00.000Z"), 1);
  assert.equal(repo.messageFor("claimed").body, "secret");
  assert.equal(repo.messageFor("claimed").state, "queued");
  assert.equal(repo.claimNextMessage({ ...lease }).message.id, "claimed");
});

test("router lifecycle sweep recovers an expired Sentinel claim before normal delivery recovery", async (t) => {
  const { repo } = await setup(t, "swept");
  repo.claimSentinel("swept", "occurrence", "attempt", { launchDeadlineAt: "2000-01-01T00:00:00.000Z" });
  createAgentRouter({ repository: repo }).sweepDeliveryTimeouts();
  assert.equal(repo.sentinelState("occurrence"), undefined);
  assert.equal(repo.messageFor("swept").state, "queued");
});

test("spawn failure releases the active delivery and tombstones reject retry", async (t) => {
  const { repo } = await setup(t, "spawn");
  repo.claimSentinel("spawn", "occ", "attempt", { launchDeadlineAt: "2030-01-01T00:01:00.000Z" });
  repo.transitionSentinel("occ", "attempt", "launching");
  repo.transitionSentinel("occ", "attempt", "spawn_failed");
  assert.equal(repo.messageFor("spawn").state, "queued");
  // A terminal uncertain occurrence is permanently non-retryable through generic retry.
  repo.claimSentinel("spawn", "term", "term-attempt", { launchDeadlineAt: "2000-01-01T00:00:00.000Z" });
  repo.transitionSentinel("term", "term-attempt", "launching");
  repo.recoverSentinelDeadlines("2030-01-01T00:00:00.000Z");
  assert.equal(repo.sentinelState("term").state, "failed");
  assert.equal(repo.messageFor("spawn").state, "dead_letter");
  assert.equal(repo.retryDeadLetter("spawn"), undefined);
});

test("active Sentinel launch recovery owns an expired normal delivery lease", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-delivery-deadline-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const now = { value: "2030-01-01T00:00:00.000Z" };
  const repo = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date(now.value),
  });
  t.after(() => repo.close());
  repo.enqueue({ id: "active", from: "supervisor", to: "receiver", body: "secret", created_at: now.value, sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" } });
  const lease = { agentId: "receiver", instanceId: "instance", leaseId: "lease" };
  repo.acquireAgentLease({ ...lease, leaseDurationMs: 60_000 });
  repo.claimNextMessage({ ...lease, deliveryLeaseDurationMs: 1 });
  repo.claimSentinel("active", "occurrence", "attempt", { launchDeadlineAt: "2030-01-01T01:00:00.000Z" });

  now.value = "2030-01-01T00:00:01.000Z";
  assert.deepEqual(repo.recoverTimedOutDeliveries(), []);
  assert.equal(repo.messageFor("active").state, "leased");
});

test("a launched Sentinel remains observable after its pre-launch deadline", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-launched-deadline-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const now = { value: "2030-01-01T00:00:00.000Z" };
  const repo = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date(now.value),
  });
  t.after(() => repo.close());
  repo.enqueue({ id: "launched", from: "supervisor", to: "receiver", body: "secret", created_at: now.value, sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" } });
  const lease = { agentId: "receiver", instanceId: "instance", leaseId: "lease" };
  repo.acquireAgentLease({ ...lease, leaseDurationMs: 60_000 });
  repo.claimNextMessage({ ...lease, deliveryLeaseDurationMs: 1 });
  repo.claimSentinel("launched", "occurrence", "attempt", { launchDeadlineAt: "2030-01-01T01:00:00.000Z" });
  repo.transitionSentinel("occurrence", "attempt", "launching");
  repo.transitionSentinel("occurrence", "attempt", "launched", { childIdentity: "child" });

  now.value = "2030-01-01T00:00:01.000Z";
  assert.deepEqual(repo.recoverTimedOutDeliveries(), []);
  now.value = "2030-01-01T01:00:00.000Z";
  assert.equal(repo.recoverSentinelDeadlines(), 0);
  assert.equal(repo.sentinelState("occurrence").state, "launched");
  assert.equal(repo.messageFor("launched").state, "acknowledged");
  assert.equal(repo.deliveryAttemptsFor("launched")[0].state, "acknowledged");
});
