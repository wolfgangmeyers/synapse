import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { createEnvelope } from "../harness/protocol.js";

async function prepared(t, now = "2030-01-01T00:00:00.000Z") {
  const directory = await mkdtemp(join(tmpdir(), "mecha-relay-capability-"));
  let clock = now;
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock: () => new Date(clock) });
  const message = createEnvelope({ from: "supervisor", to: "receiver", body: "opaque", sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" } });
  repository.enqueue(message);
  repository.claimSentinel(message.id, "occurrence", "attempt", { launchDeadlineAt: "2030-01-01T00:10:00.000Z" });
  repository.transitionSentinel("occurrence", "attempt", "launching");
  repository.transitionSentinel("occurrence", "attempt", "launched", { childIdentity: "child" });
  t.after(() => rm(directory, { recursive: true, force: true }));
  return { repository, databasePath: join(directory, "router.sqlite"), setClock: (value) => { clock = value; } };
}

test("queued relay result renders Sentinel provenance with fixed recipient, one-time expiry, and restart safety", async (t) => {
  const preparedRepository = await prepared(t);
  let repository = preparedRepository.repository;
  assert.throws(() => repository.enqueue({ id: "direct-forgery", from: "receiver", to: "supervisor", body: "forged body", created_at: "2030-01-01T00:00:00.000Z", sentinel: { receiver_policy: "sentinel_child", model: "relay", origin_sender: "supervisor", sentinel_child: { occurrence_id: "occurrence" } } }));
  repository.createSentinelRelayCapability({ occurrenceId: "occurrence", relayToken: "token", expiresAt: "2030-01-01T00:03:05.000Z" });
  const expiredMessage = createEnvelope({ from: "supervisor", to: "receiver", body: "opaque", sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra", origin_sender: "supervisor" } });
  repository.enqueue(expiredMessage);
  repository.claimSentinel(expiredMessage.id, "expired-occurrence", "expired-attempt", { launchDeadlineAt: "2030-01-01T00:10:00.000Z" });
  repository.transitionSentinel("expired-occurrence", "expired-attempt", "launching");
  repository.transitionSentinel("expired-occurrence", "expired-attempt", "launched", { childIdentity: "expired-child" });
  repository.createSentinelRelayCapability({ occurrenceId: "expired-occurrence", relayToken: "expired-token", expiresAt: "2030-01-01T00:03:05.000Z" });
  repository.enqueue({ id: "relay-collision", from: "other", to: "supervisor", body: "occupied", created_at: "2030-01-01T00:00:00.000Z" });
  assert.throws(() => repository.relaySentinelChildResult({ occurrenceId: "occurrence", relayToken: "token", body: "result", id: "relay-collision", createdAt: "2030-01-01T00:03:00.000Z" }));
  repository.close();
  repository = openMessageRepository({ databasePath: preparedRepository.databasePath, clock: () => new Date("2030-01-01T00:03:00.000Z") });
  const sentinelReceiverPolicies = {
    managed: { receiver: "receiver", senders: ["supervisor"], models: ["gpt-5.6-terra"], execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: [] } },
  };
  const events = [];
  let router = createAgentRouter({ repository, sentinelReceiverPolicies, eventLogger: (event, fields) => events.push({ event, ...fields }) });
  t.after(async () => { await router.close(); repository.close(); });
  let url = new URL(await router.listen()); url.protocol = "http:";
  const post = (body) => fetch(`${url.origin}/v1/sentinel-child-results`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) });
  const headers = { "content-type": "application/json", "x-test-identity": "supervisor" };
  const forged = { receiver_policy: "managed", model: "gpt-5.6-terra", sentinel_child: { occurrence_id: "occurrence" } };
  assert.equal((await fetch(`${url.origin}/v1/messages`, { method: "POST", headers, body: JSON.stringify({ id: "forged-child", from: "supervisor", to: "receiver", body: "forged body", created_at: "2030-01-01T00:00:00.000Z", sentinel: forged }) })).status, 400);
  assert.equal(repository.messageFor("forged-child"), undefined);
  assert.equal((await fetch(`${url.origin}/v1/reminders`, { method: "POST", headers, body: JSON.stringify({ id: "forged-reminder", creator: "supervisor", target: "receiver", body: "forged body", created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-02T00:00:00.000Z", interval_ms: null, sentinel: forged }) })).status, 400);
  assert.equal((await post({ occurrence_id: "occurrence", relay_token: "token", body: "result", to: "attacker" })).status, 400);
  const accepted = await post({ occurrence_id: "occurrence", relay_token: "token", body: "result" });
  assert.equal(accepted.status, 201);
  const relayEvent = events.find((event) => event.sentinel_child_occurrence_id === "occurrence");
  assert.deepEqual(relayEvent, {
    event: "message_enqueued",
    message_id: relayEvent.message_id,
    from: "receiver",
    to: "supervisor",
    agent_id: "supervisor",
    state: "queued",
    created: true,
    sentinel_child_occurrence_id: "occurrence",
  });
  const rendered = repository.queuedFor("supervisor").find((message) => message.sentinel?.sentinel_child?.occurrence_id === "occurrence");
  assert.equal(rendered.to, "supervisor");
  assert.deepEqual(rendered.sentinel.sentinel_child, { occurrence_id: "occurrence" });
  await router.close();
  repository.close();
  repository = openMessageRepository({ databasePath: preparedRepository.databasePath, clock: () => new Date("2030-01-01T00:03:06.000Z") });
  router = createAgentRouter({ repository, sentinelReceiverPolicies });
  url = new URL(await router.listen()); url.protocol = "http:";
  assert.equal((await post({ occurrence_id: "expired-occurrence", relay_token: "expired-token", body: "late" })).status, 400);
  assert.equal((await post({ occurrence_id: "occurrence", relay_token: "token", body: "again" })).status, 400);
  assert.equal((await post({ occurrence_id: "occurrence", relay_token: "wrong", body: "again" })).status, 400);
});
