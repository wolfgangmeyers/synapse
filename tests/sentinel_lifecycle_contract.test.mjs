import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  LeaseNotActiveError,
  openMessageRepository,
  SentinelOccurrenceConflictError,
} from "../harness/db.js";

const now = "2030-01-01T00:00:00.000Z";
const sentinel = { receiver_policy: "managed", model: "gpt-5.6-terra" };

async function repositoryAt(t) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-contract-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock: () => new Date(now) });
  t.after(() => repository.close());
  return repository;
}

function enqueue(repository, id, recipient = "receiver") {
  repository.enqueue({ id, from: "supervisor", to: recipient, body: "opaque", created_at: now, sentinel });
}

function launch(repository, messageId, occurrenceId, attemptId, lease) {
  const delivery = repository.claimNextMessage(lease);
  assert.equal(delivery.message.id, messageId);
  repository.claimSentinel(messageId, occurrenceId, attemptId, {
    launchDeadlineAt: "2030-01-01T00:01:00.000Z",
    delivery: { ...lease, messageId, attemptNumber: delivery.attempt_number },
  });
  repository.transitionSentinel(occurrenceId, attemptId, "launching", { actorId: lease.agentId, lease });
  return repository.transitionSentinel(occurrenceId, attemptId, "launched", {
    actorId: lease.agentId,
    lease,
    childIdentity: `${occurrenceId}-child`,
  });
}

test("Sentinel occurrence IDs are idempotent only for their original message and attempt", async (t) => {
  const repository = await repositoryAt(t);
  enqueue(repository, "first");
  enqueue(repository, "second");

  const first = repository.claimSentinel("first", "shared-occurrence", "attempt-a", {
    launchDeadlineAt: "2030-01-01T00:01:00.000Z",
  });
  assert.deepEqual(repository.claimSentinel("first", "shared-occurrence", "attempt-a", {
    launchDeadlineAt: "2030-01-01T00:02:00.000Z",
  }), first);
  assert.throws(
    () => repository.claimSentinel("second", "shared-occurrence", "attempt-a", { launchDeadlineAt: "2030-01-01T00:01:00.000Z" }),
    SentinelOccurrenceConflictError,
  );
  assert.throws(
    () => repository.claimSentinel("first", "shared-occurrence", "attempt-b", { launchDeadlineAt: "2030-01-01T00:01:00.000Z" }),
    SentinelOccurrenceConflictError,
  );
});

test("only the recipient's renewed lease can record launched-child exit outcomes", async (t) => {
  const repository = await repositoryAt(t);
  const oldLease = { agentId: "receiver", instanceId: "old-instance", leaseId: "old-lease" };
  enqueue(repository, "failed-child");
  repository.acquireAgentLease({ ...oldLease, leaseDurationMs: 60_000 });
  assert.equal(launch(repository, "failed-child", "failed-occurrence", "failed-attempt", oldLease).state, "launched");

  repository.releaseAgentLease(oldLease);
  const renewedLease = { agentId: "receiver", instanceId: "new-instance", leaseId: "new-lease" };
  repository.acquireAgentLease({ ...renewedLease, leaseDurationMs: 60_000 });
  assert.throws(
    () => repository.transitionSentinel("failed-occurrence", "failed-attempt", "exited", {
      actorId: oldLease.agentId,
      lease: oldLease,
      exitCode: 7,
    }),
    LeaseNotActiveError,
  );

  const otherLease = { agentId: "other", instanceId: "other-instance", leaseId: "other-lease" };
  repository.acquireAgentLease({ ...otherLease, leaseDurationMs: 60_000 });
  assert.equal(repository.transitionSentinel("failed-occurrence", "failed-attempt", "exited", {
    actorId: otherLease.agentId,
    lease: otherLease,
    exitCode: 7,
  }), undefined);

  const failed = repository.transitionSentinel("failed-occurrence", "failed-attempt", "exited", {
    actorId: renewedLease.agentId,
    lease: renewedLease,
    exitCode: 7,
  });
  assert.deepEqual({ state: failed.state, exit_code: failed.exit_code, outcome: failed.outcome }, {
    state: "exited", exit_code: 7, outcome: "failed",
  });

  enqueue(repository, "successful-child");
  const launched = launch(repository, "successful-child", "successful-occurrence", "successful-attempt", renewedLease);
  assert.equal(launched.state, "launched");
  const succeeded = repository.transitionSentinel("successful-occurrence", "successful-attempt", "exited", {
    actorId: renewedLease.agentId,
    lease: renewedLease,
    exitCode: 0,
  });
  assert.deepEqual({ state: succeeded.state, exit_code: succeeded.exit_code, outcome: succeeded.outcome }, {
    state: "exited", exit_code: 0, outcome: "succeeded",
  });
});
