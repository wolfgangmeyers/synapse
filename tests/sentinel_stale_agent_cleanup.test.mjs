import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";

test("stale cleanup preserves a launched recurring Sentinel and its linked reminder", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-stale-agent-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const clock = { value: "2030-01-01T00:00:00.000Z" };
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date(clock.value),
  });
  t.after(() => repository.close());
  repository.createReminder({
    id: "recurring", target: "receiver", creator: "supervisor", body: "secret",
    created_at: clock.value, next_due_at: clock.value, interval_ms: 60_000,
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
  });
  const lease = { agentId: "receiver", instanceId: "instance", leaseId: "lease" };
  repository.acquireAgentLease({ ...lease, leaseDurationMs: 60_000 });
  const fired = repository.fireDueReminders()[0];
  const delivery = repository.claimNextMessage(lease);
  repository.claimSentinel(fired.message.id, "occurrence", "attempt", {
    launchDeadlineAt: "2030-01-01T00:01:00.000Z",
    delivery: { ...lease, messageId: fired.message.id, attemptNumber: delivery.attempt_number },
  });
  repository.transitionSentinel("occurrence", "attempt", "launching", { actorId: lease.agentId, lease });
  repository.transitionSentinel("occurrence", "attempt", "launched", {
    actorId: lease.agentId, lease, childIdentity: "child",
  });
  repository.releaseAgentLease(lease);

  clock.value = "2030-01-01T01:00:00.001Z";
  assert.deepEqual(repository.recoverStaleAgentResources(), []);
  assert.equal(repository.sentinelState("occurrence").state, "launched");
  assert.equal(repository.messageFor(fired.message.id).body, "secret");
  const reminder = repository.reminderFor("recurring");
  assert.equal(reminder.state, "scheduled");
  assert.equal(reminder.linked_message_id, fired.message.id);
  assert.equal(repository.fireDueReminders().length, 0);
  assert.equal(repository.messageFor("reminder-recurring-1"), undefined);
});
