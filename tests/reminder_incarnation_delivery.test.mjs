import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import test from "node:test";
import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

function waitForFrame(socket, predicate) {
  return new Promise((resolve, reject) => {
    const onMessage = (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (!predicate(frame)) return;
      socket.off("message", onMessage);
      socket.off("error", onError);
      resolve(frame);
    };
    const onError = (error) => {
      socket.off("message", onMessage);
      reject(error);
    };
    socket.on("message", onMessage);
    socket.once("error", onError);
  });
}

for (const { name, intervalMs } of [
  { name: "one-shot", intervalMs: null },
  { name: "recurring", intervalMs: 60_000 },
]) {
  test(`${name} reminder backfills an incarnation without adding it to a legacy V1 offer`, async (t) => {
    const directory = await mkdtemp(join(tmpdir(), "mecha-reminder-incarnation-"));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const databasePath = join(directory, "router.sqlite");
    const now = "2030-01-01T00:00:00.000Z";
    const repository = openMessageRepository({ databasePath, clock: () => new Date(now) });
    const router = createAgentRouter({ repository, clock: () => new Date(now), timeoutSweepIntervalMs: 60_000 });
    let socket;
    t.after(async () => {
      socket?.terminate();
      await router.close();
      repository.close();
    });

    const identity = { agentId: "receiver", instanceId: "instance", leaseId: "lease" };
    repository.createReminder({
      id: `${name}-reminder`,
      target: identity.agentId,
      creator: "supervisor",
      body: "opaque reminder body",
      created_at: now,
      next_due_at: now,
      interval_ms: intervalMs,
    });
    if (intervalMs !== null) repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    const [fired] = router.sweepDueReminders();
    assert.ok(fired);
    if (intervalMs !== null) repository.releaseAgentLease(identity);

    // Simulate a queued reminder occurrence created before incarnation support.
    const database = new DatabaseSync(databasePath);
    database.prepare("UPDATE messages SET message_incarnation_id = NULL WHERE id = ?").run(fired.message.id);
    database.close();

    const websocketUrl = await router.listen();
    socket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    const registered = waitForFrame(socket, ({ type }) => type === "registered");
    const offered = waitForFrame(socket, ({ type }) => type === "message");
    socket.send(JSON.stringify({ version: "v1", frame_id: "register", type: "register", agent_id: identity.agentId, instance_id: identity.instanceId }));
    await registered;
    const delivery = await offered;

    assert.equal(delivery.message.id, fired.message.id);
    assert.equal(delivery.message_incarnation_id, undefined);
    assert.match(repository.messageFor(fired.message.id).message_incarnation_id, UUID_V4);
  });
}

for (const { name, stored, expected } of [
  { name: "preserves an existing message incarnation", stored: "550e8400-e29b-41d4-a716-446655440000", expected: "550e8400-e29b-41d4-a716-446655440000" },
  { name: "replaces a malformed message incarnation", stored: "legacy", expected: undefined },
]) {
  test(`claim ${name}`, async (t) => {
    const directory = await mkdtemp(join(tmpdir(), "mecha-reminder-incarnation-claim-"));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const databasePath = join(directory, "router.sqlite");
    const repository = openMessageRepository({ databasePath });
    t.after(() => repository.close());
    const identity = { agentId: "receiver", instanceId: "instance", leaseId: "lease" };
    repository.enqueue({ id: "existing-incarnation", from: "supervisor", to: "receiver", body: "opaque", created_at: "2030-01-01T00:00:00.000Z" });
    const database = new DatabaseSync(databasePath);
    database.prepare("UPDATE messages SET message_incarnation_id = ? WHERE id = ?").run(stored, "existing-incarnation");
    database.close();
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });

    const actual = repository.claimNextMessage(identity).message.message_incarnation_id;
    if (expected !== undefined) assert.equal(actual, expected);
    else assert.match(actual, UUID_V4);
  });
}
