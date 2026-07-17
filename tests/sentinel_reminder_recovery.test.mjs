import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { openMessageRepository } from "../harness/db.js";

async function repositoryAt(t, now) {
  const dir = await mkdtemp(join(tmpdir(), "mecha-sentinel-reminder-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const repo = openMessageRepository({ databasePath: join(dir, "db.sqlite"), clock: () => new Date(now.value) });
  t.after(() => repo.close()); return repo;
}
function reminder(id, interval_ms = null) { return { id, target: "receiver", creator: "supervisor", body: "secret", created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:00:00.000Z", interval_ms, sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" } }; }

test("uncertain Sentinel launch fails one-shot reminder and terminalizes its delivery", async (t) => {
 const now={value:"2030-01-01T00:00:00.000Z"}; const repo=await repositoryAt(t,now); repo.createReminder(reminder("once")); const fired=repo.fireDueReminders()[0];
 const lease = { agentId: "receiver", instanceId: "instance", leaseId: "lease" }; repo.acquireAgentLease({ ...lease, leaseDurationMs: 60000 }); repo.claimNextMessage(lease);
 assert.deepEqual(fired.message.sentinel, { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" });
 repo.claimSentinel(fired.message.id,"o","a",{launchDeadlineAt:"2000-01-01T00:00:00.000Z"}); repo.transitionSentinel("o","a","launching"); repo.recoverSentinelDeadlines(now.value);
 const state=repo.reminderFor("once"); assert.equal(state.state,"failed"); assert.equal(state.linked_message_id,null); assert.equal(repo.deliveryAttemptsFor(fired.message.id)[0].state,"timed_out");
});
test("uncertain Sentinel launch returns recurring reminder to next slot and terminalizes its delivery", async (t) => {
 const now={value:"2030-01-01T00:00:00.000Z"}; const repo=await repositoryAt(t,now); repo.createReminder(reminder("repeat",60000)); const lease = { agentId: "receiver", instanceId: "instance", leaseId: "lease" }; repo.acquireAgentLease({ ...lease, leaseDurationMs: 60000 }); const fired=repo.fireDueReminders()[0]; const delivery = repo.claimNextMessage(lease); repo.recordInjectionRequested({ ...lease, messageId: fired.message.id, attemptNumber: delivery.attempt_number });
 assert.deepEqual(fired.message.sentinel, { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" });
 repo.claimSentinel(fired.message.id,"o","a",{launchDeadlineAt:"2000-01-01T00:00:00.000Z"}); repo.transitionSentinel("o","a","launching"); repo.recoverSentinelDeadlines(now.value);
 const state=repo.reminderFor("repeat"); assert.equal(state.state,"scheduled"); assert.equal(state.linked_message_id,null); assert.equal(state.next_due_at,"2030-01-01T00:01:00.000Z"); assert.equal(repo.deliveryAttemptsFor(fired.message.id)[0].state,"timed_out");
});
