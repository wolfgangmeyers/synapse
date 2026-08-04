import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

async function withRouter(t, options = {}) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-update-reminder-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  const events = [];
  const router = createAgentRouter({
    repository,
    eventLogger: (event, fields) => events.push({ event, ...fields }),
    ...options,
  });
  t.after(async () => {
    try {
      await router.close();
    } catch {
      // Some tests exercise the router without listening, so close may have no server to close.
    }
  });
  return { repository, router, events };
}

function claimSupervisorMessage(repository) {
  const identity = { agentId: "supervisor", instanceId: "supervisor-instance", leaseId: "supervisor-lease" };
  repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
  return repository.claimNextMessage(identity)?.message;
}

test("router update reminder enqueues only canned advisory supervisor guidance", async (t) => {
  const now = new Date("2030-01-01T00:00:00.000Z");
  const { repository, router, events } = await withRouter(t, { clock: () => now });

  const result = router.sendMechaDeveloperUpdateReminder();

  assert.equal(result.created, true);
  assert.equal(result.message.from, "router-update-reminder");
  assert.equal(result.message.to, "supervisor");
  assert.equal(result.message.subject, "Check mecha-developer updates");
  assert.match(result.message.body, /\/Users\/wolfgang\/code\/mecha-developer/);
  assert.match(result.message.body, /advisory only/);
  assert.match(result.message.body, /do not auto-pull, auto-restart, deploy, or mutate external services/);
  assert.match(result.message.body, /Inspect local dirty state before pulling/);
  assert.match(result.message.body, /router, web UI, affected agents\/workers, or the supervisor/);
  assert.match(result.message.body, /stop them safely first and verify they are stopped or settled before restarting/);
  assert.match(result.message.body, /Avoid voluntary compaction during the update\/restart window/);
  assert.deepEqual(events.filter(({ event }) => event === "mecha_developer_update_reminder_sent"), [{
    event: "mecha_developer_update_reminder_sent",
    message_id: result.message.id,
    created: true,
  }]);

  const delivered = claimSupervisorMessage(repository);
  assert.equal(delivered.id, result.message.id);
  assert.equal(delivered.body, result.message.body);
});

test("router schedules the canned update reminder without needing git access", async (t) => {
  const { repository, router } = await withRouter(t, { mechaDeveloperUpdateReminderIntervalMs: 25 });
  await router.listen();

  let message;
  for (let attempt = 0; attempt < 20; attempt += 1) {
    await delay(10);
    message = claimSupervisorMessage(repository);
    if (message !== undefined) break;
  }

  assert.ok(message, "expected the short scheduler interval to enqueue a reminder");
  assert.equal(message.from, "router-update-reminder");
  assert.equal(message.to, "supervisor");
  assert.match(message.body, /Check whether \/Users\/wolfgang\/code\/mecha-developer has upstream changes available/);
});

test("router validates the update reminder interval", async (t) => {
  const { repository } = await withRouter(t);
  assert.throws(
    () => createAgentRouter({ repository, mechaDeveloperUpdateReminderIntervalMs: 0 }),
    /mechaDeveloperUpdateReminderIntervalMs/,
  );
});
