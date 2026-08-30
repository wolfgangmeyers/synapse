import assert from "node:assert/strict";
import { EventEmitter, once } from "node:events";
import { readFileSync, writeFileSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import WebSocket from "ws";

import { createPiMailboxDeliveryCoordinator as createDirectPiMailboxDeliveryCoordinator } from "../pi-extensions/mecha-messaging/delivery-coordinator.ts";
import {
  appendMemoryCaptureDiagnostic,
  compactionNotificationMessage,
  compactionPolicyFromEnvironment,
  createMailboxClient,
  createMechaMessagingExtension,
  createPiMailboxDeliveryCoordinator,
  createSettledNotificationScheduler,
  DEFAULT_COMPACTION_POLICY,
  effectiveCompactionPolicy,

  mailboxSessionDeliveryState,
  nextCompactionThreshold,
  postCompactionNotificationMessage,
  quarantineOrphanToolResults,
  selectCompactionTier,
} from "../pi-extensions/mecha-messaging/index.ts";
import { QueueLimitError, openMessageRepository } from "../harness/db.js";
import { validateClientFrame } from "../harness/protocol.js";
import { createAgentRouter } from "../harness/router.js";

const nextTurn = () => new Promise((resolve) => setImmediate(resolve));
async function waitFor(assertion, attempts = 20) {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (assertion()) return;
    await nextTurn();
  }
  assert.ok(assertion());
}
function envelope(id, overrides = {}) {
  return {
    id,
    from: "sender",
    to: "worker",
    body: "durable body",
    created_at: "2026-07-13T00:00:00.000Z",
    ...overrides,
  };
}

async function lifecycleRepository(t, { clock = () => new Date("2030-01-01T00:00:00.000Z") } = {}) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-lifecycle-"));
  const databasePath = join(directory, "router.sqlite");
  const repository = openMessageRepository({ databasePath, clock });
  t.after(() => { repository.close(); return rm(directory, { recursive: true, force: true }); });
  return { repository, databasePath };
}

function lifecycleSnapshot(sequence, eventId, children = [{ id: "child", type: "general-purpose", description: "Safe child label" }], overrides = {}) {
  return {
    agent_id: "lifecycle-parent", instance_id: "lifecycle-instance", parent_session_id: "parent-session",
    lease_id: "lifecycle-lease", producer_epoch: "11111111-1111-4111-8111-111111111111",
    client_sequence: sequence, event_id: eventId, subagents: children, ...overrides,
  };
}

function lifecycleEvent(sequence, eventId, lifecycleEventName, overrides = {}) {
  return {
    ...lifecycleSnapshot(sequence, eventId, [], overrides), lifecycle_event: lifecycleEventName,
    subagent: { id: "child", type: "general-purpose", description: "Safe child label" },
    ...(lifecycleEventName === "terminal" ? { terminal_reason: "completed" } : {}),
  };
}

test("persists start and terminal lifecycle transitions", async (t) => {
  const { repository } = await lifecycleRepository(t);
  const started = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "snapshot-start"));
  assert.equal(started.disposition, "applied");
  assert.equal(started.snapshot.agents[0].subagent_count, 1);
  const terminal = repository.applySubagentLifecycleEvent(lifecycleEvent(2, "terminal-child", "terminal"));
  assert.equal(terminal.disposition, "applied");
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "lifecycle-parent" }).agents[0].subagent_count, 0);
});

test("acknowledges duplicate lifecycle frames without a ledger row or version", async (t) => {
  const { repository } = await lifecycleRepository(t);
  const first = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "snapshot-duplicate"));
  const duplicate = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "snapshot-duplicate"));
  assert.equal(duplicate.disposition, "duplicate");
  assert.equal(duplicate.router_version, first.router_version);
});

test("acknowledges a matching snapshot without a ledger row or version", async (t) => {
  const { repository } = await lifecycleRepository(t);
  const first = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "snapshot-original"));
  const noop = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(2, "snapshot-noop"));
  assert.equal(noop.disposition, "accepted_noop");
  assert.equal(noop.router_version, first.router_version);
  assert.deepEqual(repository.subagentLifecycleProducerFor("lifecycle-parent"), {
    parent_agent_id: "lifecycle-parent",
    instance_id: "lifecycle-instance",
    parent_session_id: "parent-session",
    producer_epoch: "11111111-1111-4111-8111-111111111111",
    lease_id: "lifecycle-lease",
    authority: "v2",
    highest_client_sequence: 2,
    last_event_id: "snapshot-noop",
    last_snapshot_sequence: 2,
  });
  assert.equal(repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(2, "snapshot-noop", [])).disposition, "conflict");
  assert.equal(repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "late-lower-sequence")).disposition, "stale");
});

test("acknowledges stale lower-sequence lifecycle frames without mutation", async (t) => {
  const { repository } = await lifecycleRepository(t);
  const first = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(2, "snapshot-current"));
  const stale = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "snapshot-stale", []));
  assert.equal(stale.disposition, "stale");
  assert.equal(stale.router_version, first.router_version);
});

test("rejects conflicting lifecycle identity reuse without mutation", async (t) => {
  const { repository } = await lifecycleRepository(t);
  const first = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "snapshot-conflict"));
  const conflict = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "snapshot-conflict", []));
  assert.equal(conflict.disposition, "conflict");
  assert.equal(conflict.router_version, first.router_version);
});

test("keeps terminal-first unknown-child tombstones terminal", async (t) => {
  const { repository } = await lifecycleRepository(t);
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "empty", []));
  assert.equal(repository.applySubagentLifecycleEvent(lifecycleEvent(2, "terminal-first", "terminal")).disposition, "applied");
  assert.equal(repository.applySubagentLifecycleEvent(lifecycleEvent(3, "late-start", "started")).disposition, "accepted_noop");
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "lifecycle-parent" }).agents[0].subagent_count, 0);
});

test("rejects a late start after a terminal tombstone", async (t) => {
  const { repository } = await lifecycleRepository(t);
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "initial"));
  repository.applySubagentLifecycleEvent(lifecycleEvent(2, "finished", "terminal"));
  assert.equal(repository.applySubagentLifecycleEvent(lifecycleEvent(3, "late", "started")).disposition, "accepted_noop");
});

test("rebinds a same-instance epoch only through a fenced replacement snapshot", async (t) => {
  const { repository } = await lifecycleRepository(t);
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "first", []));
  const candidate = lifecycleSnapshot(2, "replacement", [], { lease_id: "replacement-lease" });
  assert.equal(repository.applySubagentLifecycleSnapshot(candidate).disposition, "stale");
  const rebound = repository.applySubagentLifecycleSnapshot(candidate, { allowLeaseRebind: true });
  assert.equal(rebound.disposition, "accepted_noop");
  assert.equal(repository.subagentLifecycleProducerFor("lifecycle-parent").lease_id, "replacement-lease");
  assert.equal(repository.subagentLifecycleProducerFor("lifecycle-parent").highest_client_sequence, 2);
  assert.equal(repository.subagentLifecycleProducerFor("lifecycle-parent").last_snapshot_sequence, 2);
  assert.equal(repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "old-lease-lower", [])).disposition, "stale");
});

test("rejects a changed epoch for an existing producer identity", async (t) => {
  const { repository } = await lifecycleRepository(t);
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "first"));
  const result = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "changed-epoch", [], { producer_epoch: "22222222-2222-4222-8222-222222222222" }));
  assert.equal(result.disposition, "conflict");
});

test("replaces a producer only from a new-instance first snapshot", async (t) => {
  const { repository } = await lifecycleRepository(t);
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "first"));
  const result = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "new-instance", [], {
    instance_id: "new-instance", parent_session_id: "new-session", lease_id: "new-lease", producer_epoch: "22222222-2222-4222-8222-222222222222",
  }));
  assert.equal(result.disposition, "applied");
  assert.equal(result.snapshot.agents[0].subagent_count, 0);
});

test("allows a reused child id only in a new-epoch replacement snapshot", async (t) => {
  const { repository } = await lifecycleRepository(t);
  repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "first"));
  const result = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "replacement", [{ id: "child", description: "New process child" }], {
    instance_id: "new-instance", parent_session_id: "new-session", lease_id: "new-lease", producer_epoch: "22222222-2222-4222-8222-222222222222",
  }));
  assert.equal(result.snapshot.agents[0].subagent_count, 1);
});

test("restores durable lifecycle state after router restart", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-lifecycle-restart-"));
  const databasePath = join(directory, "router.sqlite");
  const firstRepository = openMessageRepository({ databasePath });
  firstRepository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "restart"));
  const firstRouter = createAgentRouter({ repository: firstRepository, timeoutSweepIntervalMs: 60_000 });
  await firstRouter.listen();
  await firstRouter.close();
  firstRepository.close();

  const secondRepository = openMessageRepository({ databasePath });
  const secondRouter = createAgentRouter({ repository: secondRepository, timeoutSweepIntervalMs: 60_000 });
  const routerUrl = await secondRouter.listen();
  const reader = new WebSocket(routerUrl);
  t.after(async () => {
    reader.close();
    await secondRouter.close();
    secondRepository.close();
    await rm(directory, { recursive: true, force: true });
  });
  await new Promise((resolve, reject) => { reader.once("open", resolve); reader.once("error", reject); });
  const registered = lifecycleRouterFrame(reader, "registered");
  reader.send(JSON.stringify({ version: "v1", frame_id: "reader-register", type: "register", agent_id: "lifecycle-reader", instance_id: "reader-instance", conversation_sync: "v1" }));
  const registration = await registered;
  const restored = lifecycleRouterFrame(reader, "subagent_state_snapshot");
  reader.send(JSON.stringify({ version: "v1", frame_id: "reader-enable", type: "subagent_sync_enable", agent_id: "lifecycle-reader", instance_id: "reader-instance", lease_id: registration.lease_id }));
  assert.equal((await restored).agents[0].subagent_count, 1);
});

function lifecycleRouterFrame(socket, type) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => settle(reject, new Error(`timed out waiting for ${type}`)), 2_000);
    const onMessage = (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (frame.type === type) settle(resolve, frame);
    };
    const onError = (error) => settle(reject, error);
    const settle = (callback, value) => {
      clearTimeout(timer);
      socket.off("message", onMessage);
      socket.off("error", onError);
      callback(value);
    };
    socket.on("message", onMessage);
    socket.once("error", onError);
  });
}

test("replays retained lifecycle transitions after a cursor", async (t) => {
  const { repository } = await lifecycleRepository(t);
  const start = repository.applySubagentLifecycleSnapshot(lifecycleSnapshot(1, "replay-start"));
  repository.applySubagentLifecycleEvent(lifecycleEvent(2, "replay-end", "terminal"));
  const replay = repository.replaySubagentLifecycle({ afterVersion: start.router_version });
  assert.equal(replay.mode, "replay");
  assert.equal(replay.updates.length, 1);
});

test("keeps queued native children visible as running", async (t) => {
  const { repository } = await lifecycleRepository(t);
  // Stage 1 only receives the safe, normalized lifecycle child. A queued
  // native record therefore arrives as the same visible running child that a
  // running native record will use once Stage 2 begins producing snapshots.
  const queuedChild = { id: "queued-child", type: "general-purpose", description: "Queued child" };
  assert.equal(repository.applySubagentLifecycleSnapshot(
    lifecycleSnapshot(1, "queued-snapshot", [queuedChild]),
  ).disposition, "applied");
  assert.deepEqual(repository.subagentLifecycleSnapshot({ parentAgentId: "lifecycle-parent" }).agents[0].subagents, [queuedChild]);
  assert.equal(repository.applySubagentLifecycleEvent({
    ...lifecycleEvent(2, "queued-terminal", "terminal"), subagent: queuedChild,
  }).disposition, "applied");
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "lifecycle-parent" }).agents[0].subagent_count, 0);
});

test("repairs a missed lifecycle update with the next heartbeat snapshot", async (t) => {
  const { repository } = await lifecycleRepository(t);
  assert.equal(repository.applySubagentLifecycleSnapshot(
    lifecycleSnapshot(1, "stale-heartbeat", [{ id: "stale-child", description: "Stale child" }]),
  ).disposition, "applied");
  // The native transition was missed. A later complete heartbeat must replace,
  // not merge with, the old active projection.
  assert.equal(repository.applySubagentLifecycleSnapshot(
    lifecycleSnapshot(2, "repair-heartbeat", [{ id: "repaired-child", description: "Repaired child", status: "running" }]),
  ).disposition, "applied");
  assert.deepEqual(repository.subagentLifecycleSnapshot({ parentAgentId: "lifecycle-parent" }).agents[0].subagents, [
    { id: "repaired-child", description: "Repaired child", status: "running" },
  ]);
});

test("keeps lifecycle snapshots active while a settled parent has a child", async (t) => {
  const { repository, routerUrl } = await lifecycleRouter(t);
  const producer = await connectLifecycleSocket(t, routerUrl, { agentId: "settled-parent", instanceId: "settled-instance" });
  await enableLifecycleSocket(producer.socket, producer.registration);
  const firstAck = lifecycleRouterFrame(producer.socket, "subagent_lifecycle_acknowledged");
  producer.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "settled-start", agentId: "settled-parent", instanceId: "settled-instance",
    leaseId: producer.registration.lease_id, sequence: 1, eventId: "settled-start",
  })));
  assert.equal((await firstAck).disposition, "applied");

  const heartbeatAck = lifecycleRouterFrame(producer.socket, "subagent_lifecycle_acknowledged");
  producer.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "settled-heartbeat", agentId: "settled-parent", instanceId: "settled-instance",
    leaseId: producer.registration.lease_id, sequence: 2, eventId: "settled-heartbeat",
  })));
  assert.equal((await heartbeatAck).disposition, "accepted_noop");
  const pong = lifecycleRouterFrame(producer.socket, "pong");
  producer.socket.send(JSON.stringify({
    version: "v1", frame_id: "settled-activity", type: "activity", agent_id: "settled-parent", instance_id: "settled-instance",
    lease_id: producer.registration.lease_id, activity_state: "settled", subagent_count: 0,
  }));
  producer.socket.send(JSON.stringify({ version: "v1", frame_id: "settled-barrier", type: "ping" }));
  await pong;
  const agents = await (await fetch(routerUrl.replace("ws://", "http://"))).json();
  assert.equal(agents.agents.find((agent) => agent.agent_id === "settled-parent").subagent_count, 1);
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "settled-parent" }).agents[0].subagent_count, 1);
});

test("retries journal entries across reconnect without a second transition", async (t) => {
  const { repository, routerUrl } = await lifecycleRouter(t);
  const first = await connectLifecycleSocket(t, routerUrl, { agentId: "retry-parent", instanceId: "retry-instance" });
  await enableLifecycleSocket(first.socket, first.registration);
  const acknowledged = lifecycleRouterFrame(first.socket, "subagent_lifecycle_acknowledged");
  first.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "retry-first-frame", agentId: "retry-parent", instanceId: "retry-instance",
    leaseId: first.registration.lease_id, sequence: 1, eventId: "retry-event",
  })));
  const initial = await acknowledged;
  assert.equal(initial.disposition, "applied");
  first.socket.close();
  await once(first.socket, "close");

  const replacement = await connectLifecycleSocket(t, routerUrl, { agentId: "retry-parent", instanceId: "retry-instance" });
  await enableLifecycleSocket(replacement.socket, replacement.registration);
  const retried = lifecycleRouterFrame(replacement.socket, "subagent_lifecycle_acknowledged");
  replacement.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "retry-frame-after-reconnect", agentId: "retry-parent", instanceId: "retry-instance",
    leaseId: replacement.registration.lease_id, sequence: 1, eventId: "retry-event",
  })));
  const retry = await retried;
  assert.equal(retry.disposition, "duplicate");
  assert.equal(retry.router_version, initial.router_version);
  assert.equal(repository.replaySubagentLifecycle({ afterVersion: 0 }).updates.length, 1);
});

async function lifecycleRouter(t, { clock = () => new Date() } = {}) {
  const { repository, databasePath } = await lifecycleRepository(t, { clock });
  const router = createAgentRouter({ repository, clock, timeoutSweepIntervalMs: 60_000 });
  const routerUrl = await router.listen();
  t.after(() => router.close());
  return { repository, databasePath, routerUrl };
}

async function connectLifecycleSocket(t, routerUrl, { agentId, instanceId }) {
  const socket = new WebSocket(routerUrl);
  t.after(() => socket.close());
  await new Promise((resolve, reject) => { socket.once("open", resolve); socket.once("error", reject); });
  const registered = lifecycleRouterFrame(socket, "registered");
  socket.send(JSON.stringify({ version: "v1", frame_id: `register-${agentId}-${instanceId}`, type: "register", agent_id: agentId, instance_id: instanceId, conversation_sync: "v1" }));
  return { socket, registration: await registered };
}

function lifecycleWireFrame({ type, frameId, agentId, instanceId, leaseId, sequence, eventId, epoch = "11111111-1111-4111-8111-111111111111", children = [{ id: "child", description: "Safe child" }] }) {
  return {
    version: "v1", frame_id: frameId, type, agent_id: agentId, instance_id: instanceId,
    parent_session_id: "parent-session", lease_id: leaseId, producer_epoch: epoch,
    client_sequence: sequence, event_id: eventId,
    ...(type === "subagent_lifecycle_snapshot" ? { subagents: children } : {}),
  };
}

async function enableLifecycleSocket(socket, registration, { afterVersion } = {}) {
  const snapshot = lifecycleRouterFrame(socket, afterVersion === undefined ? "subagent_state_snapshot" : "subagent_state_replay");
  socket.send(JSON.stringify({
    version: "v1", frame_id: `enable-${registration.lease_id}`, type: "subagent_sync_enable",
    agent_id: registration.agent_id, instance_id: registration.instance_id, lease_id: registration.lease_id,
    ...(afterVersion === undefined ? {} : { after_version: afterVersion }),
  }));
  return snapshot;
}

test("router preserves lifecycle labels and status through the existing agents path", async (t) => {
  const { routerUrl } = await lifecycleRouter(t);
  const producer = await connectLifecycleSocket(t, routerUrl, { agentId: "status-parent", instanceId: "status-instance" });
  await enableLifecycleSocket(producer.socket, producer.registration);
  const acknowledged = lifecycleRouterFrame(producer.socket, "subagent_lifecycle_acknowledged");
  producer.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "soak-sleeper", agentId: "status-parent", instanceId: "status-instance",
    leaseId: producer.registration.lease_id, sequence: 1, eventId: "soak-sleeper",
    children: [{ id: "soak-child", type: "general-purpose", description: "Soak sleeper 1 2m", status: "running" }],
  })));
  assert.equal((await acknowledged).disposition, "applied");

  const agents = await (await fetch(routerUrl.replace("ws://", "http://"))).json();
  assert.deepEqual(agents.agents.find((agent) => agent.agent_id === "status-parent").subagents, [
    { id: "soak-child", type: "general-purpose", description: "Soak sleeper 1 2m", status: "running" },
  ]);
});

test("router fences an old socket and accepts a complete authenticated replacement snapshot", async (t) => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const { repository, routerUrl } = await lifecycleRouter(t, { clock: () => new Date(now) });
  const old = await connectLifecycleSocket(t, routerUrl, { agentId: "socket-parent", instanceId: "socket-instance" });
  await enableLifecycleSocket(old.socket, old.registration);
  const initialAck = lifecycleRouterFrame(old.socket, "subagent_lifecycle_acknowledged");
  old.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "old-first", agentId: "socket-parent", instanceId: "socket-instance",
    leaseId: old.registration.lease_id, sequence: 1, eventId: "old-first",
  })));
  assert.equal((await initialAck).disposition, "applied");

  const oldClosed = once(old.socket, "close");
  now += 31_000;
  const replacement = await connectLifecycleSocket(t, routerUrl, { agentId: "socket-parent", instanceId: "socket-instance" });
  await oldClosed;
  await enableLifecycleSocket(replacement.socket, replacement.registration);
  const replacementAck = lifecycleRouterFrame(replacement.socket, "subagent_lifecycle_acknowledged");
  replacement.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "replacement-snapshot", agentId: "socket-parent", instanceId: "socket-instance",
    leaseId: replacement.registration.lease_id, sequence: 2, eventId: "replacement-snapshot", children: [],
  })));
  assert.equal((await replacementAck).disposition, "applied");
  assert.equal(repository.subagentLifecycleProducerFor("socket-parent").lease_id, replacement.registration.lease_id);
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "socket-parent" }).agents[0].subagent_count, 0);
});

test("router rejects a changed epoch on an authenticated socket", async (t) => {
  const { repository, routerUrl } = await lifecycleRouter(t);
  const producer = await connectLifecycleSocket(t, routerUrl, { agentId: "epoch-parent", instanceId: "epoch-instance" });
  await enableLifecycleSocket(producer.socket, producer.registration);
  const initialAck = lifecycleRouterFrame(producer.socket, "subagent_lifecycle_acknowledged");
  producer.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "epoch-first", agentId: "epoch-parent", instanceId: "epoch-instance",
    leaseId: producer.registration.lease_id, sequence: 1, eventId: "epoch-first",
  })));
  assert.equal((await initialAck).disposition, "applied");
  const changedAck = lifecycleRouterFrame(producer.socket, "subagent_lifecycle_acknowledged");
  producer.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "epoch-changed", agentId: "epoch-parent", instanceId: "epoch-instance",
    leaseId: producer.registration.lease_id, sequence: 2, eventId: "epoch-changed",
    epoch: "22222222-2222-4222-8222-222222222222", children: [],
  })));
  assert.equal((await changedAck).disposition, "conflict");
  assert.equal(repository.subagentLifecycleSnapshot({ parentAgentId: "epoch-parent" }).agents[0].subagent_count, 1);
});

test("router sends lifecycle acknowledgment after commit and before the live update", async (t) => {
  const { repository, routerUrl } = await lifecycleRouter(t);
  const producer = await connectLifecycleSocket(t, routerUrl, { agentId: "ack-parent", instanceId: "ack-instance" });
  await enableLifecycleSocket(producer.socket, producer.registration);
  const received = [];
  let stateAtAck;
  producer.socket.on("message", (payload) => {
    const frame = JSON.parse(payload.toString("utf8"));
    if (frame.type === "subagent_lifecycle_acknowledged") {
      stateAtAck = repository.subagentLifecycleSnapshot({ parentAgentId: "ack-parent" }).agents[0].subagent_count;
    }
    if (["subagent_lifecycle_acknowledged", "subagent_state_update"].includes(frame.type)) received.push(frame.type);
  });
  const acknowledged = lifecycleRouterFrame(producer.socket, "subagent_lifecycle_acknowledged");
  const update = lifecycleRouterFrame(producer.socket, "subagent_state_update");
  producer.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "ack-ordered", agentId: "ack-parent", instanceId: "ack-instance",
    leaseId: producer.registration.lease_id, sequence: 1, eventId: "ack-ordered",
  })));
  await Promise.all([acknowledged, update]);
  await waitFor(() => received.length >= 2);
  assert.equal(stateAtAck, 1);
  assert.deepEqual(received.slice(-2), ["subagent_lifecycle_acknowledged", "subagent_state_update"]);
});

test("router sends a durable snapshot then replay after a reader reconnects", async (t) => {
  const { routerUrl } = await lifecycleRouter(t);
  const producer = await connectLifecycleSocket(t, routerUrl, { agentId: "replay-parent", instanceId: "replay-instance" });
  await enableLifecycleSocket(producer.socket, producer.registration);
  const reader = await connectLifecycleSocket(t, routerUrl, { agentId: "replay-reader", instanceId: "reader-instance" });
  const initialSnapshot = await enableLifecycleSocket(reader.socket, reader.registration);
  assert.equal(initialSnapshot.router_version, 0);
  const applied = lifecycleRouterFrame(producer.socket, "subagent_lifecycle_acknowledged");
  const update = lifecycleRouterFrame(reader.socket, "subagent_state_update");
  producer.socket.send(JSON.stringify(lifecycleWireFrame({
    type: "subagent_lifecycle_snapshot", frameId: "replay-state", agentId: "replay-parent", instanceId: "replay-instance",
    leaseId: producer.registration.lease_id, sequence: 1, eventId: "replay-state",
  })));
  const version = (await applied).router_version;
  assert.equal((await update).router_version, version);
  const terminalAck = lifecycleRouterFrame(producer.socket, "subagent_lifecycle_acknowledged");
  const terminalUpdate = lifecycleRouterFrame(reader.socket, "subagent_state_update");
  producer.socket.send(JSON.stringify({
    ...lifecycleWireFrame({
      type: "subagent_lifecycle_event", frameId: "replay-terminal", agentId: "replay-parent", instanceId: "replay-instance",
      leaseId: producer.registration.lease_id, sequence: 2, eventId: "replay-terminal", children: [],
    }), lifecycle_event: "terminal", subagent: { id: "child", description: "Safe child" }, terminal_reason: "completed",
  }));
  const terminalVersion = (await terminalAck).router_version;
  assert.equal((await terminalUpdate).router_version, terminalVersion);
  reader.socket.close();
  await once(reader.socket, "close");
  const reconnected = await connectLifecycleSocket(t, routerUrl, { agentId: "replay-reader", instanceId: "reader-instance" });
  const replay = await enableLifecycleSocket(reconnected.socket, reconnected.registration, { afterVersion: version });
  assert.equal(replay.router_version, terminalVersion);
  assert.equal(replay.updates.length, 1);
});

function sessionEntry(messageId) {
  return {
    type: "custom_message",
    customType: "mecha-agent-message",
    content: "message",
    display: true,
    details: { message_id: messageId },
  };
}

async function durableSession(messageId, t) {
  if (t === undefined || typeof t.after !== "function") throw new TypeError("a test context is required");
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-session-"));
  const sessionFile = join(directory, "session.jsonl");
  const entries = [];
  let closePromise;
  const closeDirectory = async () => {
    closePromise ??= rm(directory, { recursive: true, force: true });
    return closePromise;
  };
  t.after(async () => {
    try {
      await closeDirectory();
    } catch {
      // Test cleanup must make a second best-effort removal after an explicit close failure.
      await rm(directory, { recursive: true, force: true }).catch(() => {});
    }
  });
  const manager = {
    getBranch: () => entries,
    isPersisted: () => true,
    getSessionFile: () => sessionFile,
    getSessionId: () => "session-1",
  };
  return {
    directory,
    sessionFile,
    entries,
    manager,
    async persist() {
      if (!entries.some((entry) => entry.details?.message_id === messageId)) {
        entries.push(sessionEntry(messageId));
      }
      await writeFile(sessionFile, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
    },
    async close() {
      await closeDirectory();
    },
  };
}

test("orphan tool results are quarantined while valid tool results are preserved", () => {
  const assistant = { role: "assistant", content: [{ type: "toolCall", id: "call-valid|fc-assistant", name: "bash", arguments: {} }] };
  const validResult = { role: "toolResult", toolCallId: "call-valid|fc-result", toolName: "bash", content: [] };
  const orphanResult = { role: "toolResult", toolCallId: "call-orphan|fc-result", toolName: "bash", content: [] };
  const user = { role: "user", content: [{ type: "text", text: "continue" }] };

  const result = quarantineOrphanToolResults([assistant, validResult, orphanResult, user]);

  assert.deepEqual(result.messages, [assistant, validResult, user]);
  assert.equal(result.orphaned.length, 1);
  assert.deepEqual(result.orphaned[0], {
    messageIndex: 2,
    toolCallId: "call-orphan|fc-result",
    providerToolCallId: "call-orphan",
    toolName: "bash",
    previousRole: "toolResult",
    nextRole: "user",
    nearestAssistantIndex: 0,
    nearestAssistantStopReason: undefined,
    nearestAssistantToolCallIds: ["call-valid"],
    matchingToolCallLater: false,
  });
});

test("errored assistant tool calls do not make following tool results safe", () => {
  const erroredAssistant = {
    role: "assistant",
    stopReason: "error",
    content: [{ type: "toolCall", id: "call-error|fc-assistant", name: "bash", arguments: {} }],
  };
  const resultAfterError = { role: "toolResult", toolCallId: "call-error|fc-result", toolName: "bash", content: [] };

  const result = quarantineOrphanToolResults([erroredAssistant, resultAfterError]);

  assert.deepEqual(result.messages, [erroredAssistant]);
  assert.equal(result.orphaned.length, 1);
  assert.equal(result.orphaned[0].toolCallId, "call-error|fc-result");
  assert.equal(result.orphaned[0].nearestAssistantStopReason, "error");
});

test("context hook drops orphan tool results before the provider request", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-orphan-diagnostics-"));
  const path = join(directory, "orphan-tool-results.jsonl");
  const previousPath = process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH;
  process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH = path;
  const handlers = new Map();
  const notices = [];
  const pi = {
    events: new EventEmitter(),
    on(event, handler) { handlers.set(event, handler); },
    registerTool() {},
    registerCommand() {},
    sendMessage() {},
  };
  createMechaMessagingExtension(pi, { createClient: () => ({ connect: async () => {}, close: () => {} }) });
  const assistant = { role: "assistant", content: [{ type: "toolCall", id: "call-valid", name: "bash", arguments: {} }] };
  const validResult = { role: "toolResult", toolCallId: "call-valid", toolName: "bash", content: [] };
  const orphanResult = { role: "toolResult", toolCallId: "call-orphan", toolName: "bash", content: [] };

  try {
    const result = handlers.get("context")(
      { messages: [assistant, validResult, orphanResult] },
      {
        sessionManager: { getSessionId: () => "session-1", getSessionFile: () => "/tmp/session.jsonl" },
        model: "openai-codex/gpt-5.5",
        ui: { notify: (...args) => notices.push(args) },
      },
    );

    assert.deepEqual(result.messages, [assistant, validResult]);
    assert.deepEqual(notices, [["Quarantined orphan tool result(s) before provider request: call-orphan", "error"]]);
    const records = (await readFile(path, "utf8")).trim().split("\n").map((line) => JSON.parse(line));
    assert.equal(records.length, 1);
    assert.equal(records[0].session_id, "session-1");
    assert.equal(records[0].session_file, "/tmp/session.jsonl");
    assert.equal(records[0].model, "openai-codex/gpt-5.5");
    assert.equal(records[0].quarantined_count, 1);
    assert.equal(records[0].orphans[0].tool_call_id, "call-orphan");
    assert.equal(records[0].orphans[0].message_index, 2);
  } finally {
    if (previousPath === undefined) delete process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH;
    else process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH = previousPath;
    await rm(directory, { recursive: true, force: true });
  }
});

function nextRouterFrame(socket) {
  return new Promise((resolve, reject) => {
    const onMessage = (payload) => settle(resolve, JSON.parse(payload.toString("utf8")));
    const onError = (error) => settle(reject, error);
    const settle = (callback, value) => {
      socket.off("message", onMessage);
      socket.off("error", onError);
      callback(value);
    };
    socket.once("message", onMessage);
    socket.once("error", onError);
  });
}

function nextMessageFrame(socket) {
  return new Promise((resolve, reject) => {
    const onMessage = (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (frame.type === "message") settle(resolve, frame);
    };
    const onError = (error) => settle(reject, error);
    const settle = (callback, value) => {
      socket.off("message", onMessage);
      socket.off("error", onError);
      callback(value);
    };
    socket.on("message", onMessage);
    socket.once("error", onError);
  });
}

function receiptClient() {
  const receipts = [];
  return {
    receipts,
    sendReceipt(type, delivery, reason) {
      receipts.push({
        type,
        messageId: delivery.messageId,
        attemptNumber: delivery.attemptNumber,
        ...(delivery.items === undefined ? {} : { items: delivery.items }),
        reason,
      });
    },
  };
}

function deliveryItems(delivery) {
  return delivery.items.map(({ message_id: messageId, message_incarnation_id: incarnationId, attempt_number: attemptNumber }) => ({
    messageId,
    incarnationId,
    attemptNumber,
  }));
}

function receiptItems(delivery) {
  return delivery.items.map(({ message_id: messageId, attempt_number: attemptNumber }) => ({ messageId, attemptNumber }));
}

function fakeTimers() {
  let now = 0;
  const timers = [];
  return {
    timers,
    setTimeout(callback, delay) {
      const timer = { callback, dueAt: now + delay, cleared: false, fired: false };
      timers.push(timer);
      return timer;
    },
    clearTimeout(timer) { timer.cleared = true; },
    advanceTo(target) {
      for (;;) {
        const timer = timers
          .filter((candidate) => !candidate.cleared && !candidate.fired && candidate.dueAt <= target)
          .sort((left, right) => left.dueAt - right.dueAt)[0];
        if (timer === undefined) break;
        now = timer.dueAt;
        timer.fired = true;
        timer.callback();
      }
      now = target;
    },
  };
}

test("settled scheduler sends once after the cooldown", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  clock.advanceTo(119_999);
  assert.deepEqual(sent, []);
  clock.advanceTo(120_000);
  clock.advanceTo(300_000);
  clock.advanceTo(600_000);
  clock.advanceTo(600_001);

  assert.deepEqual(sent, ["sent"]);
});

test("settled scheduler rejects missing timer functions", () => {
  assert.throws(
    () => createSettledNotificationScheduler({ setTimeoutImpl: null, clearTimeoutImpl: () => {} }),
    /settled notification timers are required/,
  );
  assert.throws(
    () => createSettledNotificationScheduler({ setTimeoutImpl: () => {}, clearTimeoutImpl: null }),
    /settled notification timers are required/,
  );
});

test("settled scheduler suppresses a settled parent while a native child is active", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onChildStarted("child-session");
  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  assert.equal(scheduler.activeChildCount(), 1);
  assert.deepEqual(sent, []);

  scheduler.onChildDisposed("child-session");
  scheduler.onParentSettled(() => sent.push("retroactive"));
  assert.deepEqual(sent, []);

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("next-settle"));
  clock.advanceTo(120_000);
  assert.deepEqual(sent, ["next-settle"]);
});

test("settled scheduler disposal cancels the pending cooldown", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  const staleTimers = [...clock.timers];
  scheduler.dispose();
  for (const timer of staleTimers) timer.callback();
  assert.deepEqual(sent, []);
  assert.equal(scheduler.activeChildCount(), 0);
});

test("child starts and stale timers suppress pending settled cooldown", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];

  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("first"));
  const staleTimers = [...clock.timers];
  scheduler.onChildStarted("child-session");
  for (const timer of staleTimers) timer.callback();
  clock.advanceTo(600_000);
  assert.deepEqual(sent, []);

  scheduler.onChildDisposed("child-session");
  scheduler.onParentSettled(() => sent.push("retroactive"));
  assert.deepEqual(sent, []);
  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("second"));
  clock.advanceTo(720_000);
  assert.deepEqual(sent, ["second"]);
});

test("parent activity before the cooldown cancels that idle generation", () => {
  const clock = fakeTimers();
  const scheduler = createSettledNotificationScheduler({
    setTimeoutImpl: clock.setTimeout,
    clearTimeoutImpl: clock.clearTimeout,
  });
  const sent = [];
  scheduler.onParentActivity();
  scheduler.onParentSettled(() => sent.push("sent"));
  clock.advanceTo(119_999);
  const staleTimers = [...clock.timers];
  scheduler.onParentActivity();
  for (const timer of staleTimers) timer.callback();
  clock.advanceTo(600_000);
  assert.equal(sent.length, 0);
});

test("subject-bearing PR watch wakeups preserve their exact subject through durable Pi delivery", async (t) => {
  const message = envelope("message-1", { to: "pr-watch", subject: "PR watch wakeup" });

  const session = await durableSession(message.id, t);
  const sent = [];
  const client = receiptClient();
  const statuses = [];
  const context = {
    sessionManager: session.manager,
    ui: { setStatus: (...args) => statuses.push(args) },
  };
  const checkpointTimers = [];
  let deferredCheckpoint;
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (customMessage, options) => sent.push({ customMessage, options }) },
    sessionContext: context,
    client,
    defer: (callback) => { deferredCheckpoint = callback; },
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      checkpointTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.equal(sent.length, 1);
  assert.equal(
    sent[0].customMessage.content,
    "Mailbox message from sender\nSubject: PR watch wakeup\n\ndurable body",
  );
  assert.deepEqual(sent[0].customMessage.details, {
    message_id: "message-1",
    from: "sender",
    to: "pr-watch",
    subject: "PR watch wakeup",
  });
  assert.deepEqual(sent[0].options, { deliverAs: "steer", triggerTurn: true });
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
  ]);

  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  // Pi appends the custom entry after extension message_end handlers, before the deferred check.
  session.entries.push(sessionEntry(message.id));
  assert.equal(typeof deferredCheckpoint, "function");
  assert.equal(client.receipts.length, 1);
  deferredCheckpoint();
  assert.equal(client.receipts.length, 1);
  assert.deepEqual(checkpointTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 250, cleared: false },
  ]);

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });
  assert.equal(sent.length, 1);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);

  await session.persist();
  checkpointTimers[0].callback();

  assert.equal(mailboxSessionDeliveryState(session.manager, message.id), "durable");
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "ack", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);
  assert.deepEqual(statuses.slice(-1), [["mecha-mailbox", "mailbox: connected"]]);
  coordinator.close();
  await session.close();
});

test("post-compaction handoff message includes bounded note and compact status without summary text", () => {
  const message = postCompactionNotificationMessage("Resume the validation checklist.", {
    tokensBefore: 123_456,
    estimatedTokensAfter: 12_345,
    firstKeptEntryId: "entry-1",
    summary: "secret summary text",
  });
  assert.match(message, /Resume the validation checklist/);
  assert.match(message, /tokens before: 123,456/);
  assert.match(message, /estimated tokens after: 12,345/);
});

test("direct delivery coordinator module preserves mailbox injection", async (t) => {
  const message = envelope("direct-coordinator");
  const session = await durableSession(message.id, t);
  const sent = [];
  const client = receiptClient();
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: session.manager, ui: { setStatus() {} } },
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.equal(sent.length, 1);
  assert.equal(sent[0][0].customType, "mecha-agent-message");
  assert.deepEqual(sent[0][1], { deliverAs: "steer", triggerTurn: true });
  assert.deepEqual(client.receipts.map(({ type }) => type), ["injection_requested"]);
  coordinator.close();
  await session.close();
});

test("message_end ignores unrelated messages without ACKs or checkpoint timers", async (t) => {
  const message = envelope("message-end-negative");
  const session = await durableSession(message.id, t);
  const client = receiptClient();
  const deferred = [];
  const timers = [];
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: context,
    client,
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      timers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  const unrelatedEvents = [
    { message: { role: "custom", customType: "mecha-agent-message", details: { message_id: "other-id" } } },
    { message: { role: "assistant", customType: "mecha-agent-message", details: { message_id: message.id } } },
    { message: { role: "custom", customType: "other-message", details: { message_id: message.id } } },
    { message: { role: "custom", customType: "mecha-agent-message", details: {} } },
  ];
  for (const event of unrelatedEvents) coordinator.onMessageEnd(event, context);
  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, { ...context, sessionManager: { ...session.manager, getSessionId: () => "other-session" } });

  assert.equal(deferred.length, 0);
  assert.equal(timers.length, 0);
  assert.deepEqual(client.receipts.map(({ type }) => type), ["injection_requested"]);
  coordinator.close();
  await session.close();
});

test("closing the coordinator cancels a pending checkpoint retry", async (t) => {
  const message = envelope("message-close-retry");
  const session = await durableSession(message.id, t);
  const client = receiptClient();
  const deferred = [];
  const timers = [];
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: session.manager, ui: { setStatus() {} } },
    client,
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      timers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  session.entries.push(sessionEntry(message.id));
  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, { sessionManager: session.manager, ui: { setStatus() {} } });
  deferred[0]();

  assert.deepEqual(timers.map(({ delay, cleared }) => ({ delay, cleared })), [{ delay: 250, cleared: false }]);
  coordinator.close();
  assert.equal(timers[0].cleared, true);
  timers[0].callback();
  assert.deepEqual(client.receipts.map(({ type }) => type), ["injection_requested"]);
  await session.close();
});

test("durable session history prevents reinjection after a failed checkpoint ACK and reconnect", async (t) => {
  const message = envelope("message-reconnect-durable");
  const session = await durableSession(message.id, t);
  const sent = [];
  const receipts = [];
  let failAck = true;
  const client = {
    sendReceipt(type, delivery, reason) {
      if (type === "ack" && failAck) {
        failAck = false;
        throw new Error("lost ACK");
      }
      receipts.push({ type, attemptNumber: delivery.attemptNumber, reason });
    },
  };
  const deferred = [];
  const timers = [];
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      timers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  session.entries.push(sessionEntry(message.id));
  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  deferred[0]();
  await session.persist();
  timers[0].callback();
  coordinator.close();

  const reconnected = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });
  reconnected.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.equal(sent.length, 1);
  assert.deepEqual(receipts.map(({ type, attemptNumber }) => ({ type, attemptNumber })), [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "injection_requested", attemptNumber: 2 },
    { type: "ack", attemptNumber: 2 },
  ]);
  reconnected.close();
  await session.close();
});

// Managed worker shells export MECHA_PI_MODEL_KEY (worker_launch.py pins it to
// the worker runtime model) and may export MECHA_PI_COMPACTION_POLICY. Both are
// legitimate runtime inputs to the compaction resolver, so assertions about the
// default ladders pass an explicit empty environment instead of inheriting the
// ambient worker shell.
const NO_COMPACTION_ENV = Object.freeze({});
const COMPACTION_ENV_KEYS = ["MECHA_PI_MODEL_KEY", "MECHA_PI_COMPACTION_POLICY"];
const tierFor = (policy, contextWindow, model) => selectCompactionTier(policy, contextWindow, model, NO_COMPACTION_ENV);
const thresholdFor = (policy, usage, alreadyNotifiedPercent = 0) => nextCompactionThreshold(policy, usage, alreadyNotifiedPercent, undefined, undefined, NO_COMPACTION_ENV);
const effectivePolicyFor = (policy, model, environment = NO_COMPACTION_ENV) => effectiveCompactionPolicy(policy, model, environment);

// Extension-level tests construct the extension, which reads process.env
// directly, so the compaction keys are cleared for the duration of the test.
function isolateCompactionEnvironment(t) {
  const previous = COMPACTION_ENV_KEYS.map((key) => [key, process.env[key]]);
  for (const key of COMPACTION_ENV_KEYS) delete process.env[key];
  t.after(() => {
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });
}

test("model-specific compaction overrides preserve context-size fallback behavior", () => {
  const millionTokenTier = tierFor(DEFAULT_COMPACTION_POLICY, 1_000_000);
  assert.equal(millionTokenTier.levels.find((level) => level.mandatory).percent, 45);
  assert.equal(thresholdFor(DEFAULT_COMPACTION_POLICY, { percent: 49, contextWindow: 1_000_000 }, 40).mandatory, true);
  const solTier = tierFor(DEFAULT_COMPACTION_POLICY, 272_000, { provider: "openai-codex", id: "gpt-5.6-sol", name: "Sol" });
  assert.equal(solTier.matchedModelKey, undefined);
  assert.equal(thresholdFor(DEFAULT_COMPACTION_POLICY, { percent: 36, contextWindow: 272_000, model: { provider: "openai-codex", id: "gpt-5.6-sol" } }, 25), undefined);
  assert.equal(thresholdFor(DEFAULT_COMPACTION_POLICY, { percent: 41, contextWindow: 272_000, model: { provider: "openai-codex", id: "gpt-5.6-sol" } }).percent, 40);

  const configured = compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({
    levels: [40, 90],
    models: { "openai-codex/custom-sol": { levels: [10, { percent: 30, mandatory: true }] } },
  }) });
  assert.deepEqual(configured.tiers[0].levels.map((level) => level.percent), [40, 90]);
  assert.equal(effectivePolicyFor(configured, { provider: "openai-codex", id: "custom-sol" }).matchedModelKey, "openai-codex/custom-sol");
  assert.equal(effectiveCompactionPolicy(configured, undefined, { MECHA_PI_MODEL_KEY: "openai-codex/custom-sol" }).matchedModelKey, "openai-codex/custom-sol");
  assert.equal(thresholdFor(configured, { percent: 31, contextWindow: 272_000, model: { provider: "openai-codex", id: "custom-sol" } }).mandatory, true);
  assert.deepEqual(compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: "45, 75" }).tiers[0].levels.map((level) => level.percent), [45, 75]);
});

test("claude opus 5 resolves the low-threshold ladder with small-context guidance wording", () => {
  const smallGuidance = DEFAULT_COMPACTION_POLICY.tiers[0].levels.map((level) => level.guidance);
  for (const modelKeyEnv of ["anthropic/claude-opus-5", "claude-opus-5"]) {
    const effective = effectiveCompactionPolicy(DEFAULT_COMPACTION_POLICY, undefined, { MECHA_PI_MODEL_KEY: modelKeyEnv });
    assert.equal(effective.matchedModelKey, modelKeyEnv);
    const levels = effective.policy.tiers[0].levels;
    assert.deepEqual(levels.map((level) => level.percent), [15, 20, 25, 30, 35, 40]);
    assert.deepEqual(levels.map((level) => level.guidance), smallGuidance);
    assert.deepEqual(levels.filter((level) => level.mandatory).map((level) => level.percent), [40]);
  }

  // Model object keys resolve the same ladder, even at a 1M context window
  // (which would otherwise select the large-context tier).
  const opusTier = tierFor(DEFAULT_COMPACTION_POLICY, 1_000_000, { provider: "anthropic", id: "claude-opus-5", name: "Claude Opus 5" });
  assert.equal(opusTier.matchedModelKey, "anthropic/claude-opus-5");
  assert.deepEqual(opusTier.levels.map((level) => level.percent), [15, 20, 25, 30, 35, 40]);
  assert.equal(thresholdFor(DEFAULT_COMPACTION_POLICY, { percent: 14, contextWindow: 1_000_000, model: { provider: "anthropic", id: "claude-opus-5" } }), undefined);
  assert.equal(thresholdFor(DEFAULT_COMPACTION_POLICY, { percent: 15, contextWindow: 1_000_000, model: { provider: "anthropic", id: "claude-opus-5" } }).guidance, "consider compacting at a reasonable stopping point to keep future calls cheaper");
  assert.equal(thresholdFor(DEFAULT_COMPACTION_POLICY, { percent: 41, contextWindow: 1_000_000, model: { provider: "anthropic", id: "claude-opus-5" } }, 35).mandatory, true);
});

test("configured compaction policy can add early gpt-5.5 threshold nudges", () => {
  const configured = compactionPolicyFromEnvironment({ MECHA_PI_COMPACTION_POLICY: JSON.stringify({
    levels: [99],
    models: {
      "openai-codex/gpt-5.5": {
        levels: [
          { percent: 20, guidance: "Context is around 20%. No compaction needed yet." },
          { percent: 30, guidance: "Context is around 30%. Keep the main thread lean." },
          { percent: 40, guidance: "Context is around 40%. Look for a clean boundary to compact soon." },
        ],
      },
    },
  }) });
  const effective = effectivePolicyFor(configured, { provider: "openai-codex", id: "gpt-5.5" });
  assert.deepEqual(effective.policy.tiers[0].levels.map((level) => level.percent), [20, 30, 40]);
  assert.equal(thresholdFor(configured, { percent: 20, contextWindow: 272_000, model: { provider: "openai-codex", id: "gpt-5.5" } }).percent, 20);
  assert.equal(thresholdFor(configured, { percent: 30, contextWindow: 272_000, model: { provider: "openai-codex", id: "gpt-5.5" } }, 20).percent, 30);
});

test("compaction threshold message appends delegation suffix without hard-coded compact instructions", () => {
  const early = compactionNotificationMessage({
    level: { percent: 20, guidance: "Context is around 20%. No compaction needed yet." },
    usage: { tokens: 54_400, contextWindow: 272_000, percent: 20 },
  });
  assert.match(early, /Use workers or subagents .*avoid context bloat/);
  assert.doesNotMatch(early, /mecha_context action=compact/);

  const mandatory = compactionNotificationMessage({
    level: { percent: 90, guidance: "Context is around 90%. Stop immediately and compact before doing more work.", mandatory: true },
    usage: { tokens: 900_000, contextWindow: 1_000_000, percent: 90 },
  });
  assert.match(mandatory, /Stop immediately and compact before doing more work/);
  assert.match(mandatory, /Use workers or subagents .*avoid context bloat/);
  assert.doesNotMatch(mandatory, /mecha_context action=compact/);
});

function immediateSettledScheduler() {
  const children = new Set();
  let parentSettled = false;
  return {
    onParentActivity() { parentSettled = false; },
    onParentSettled(send) {
      if (parentSettled || children.size > 0) return;
      parentSettled = true;
      send();
    },
    onChildStarted(id) { children.add(id); },
    onChildDisposed(id) { children.delete(id); },
    dispose() { children.clear(); parentSettled = false; },
  };
}

function settledNotificationFixture(t, {
  recipient,
  sendReply = async () => ({ id: "reply" }),
  createSettledScheduler,
  sendMessage,
  setTimeoutImpl,
  setIntervalImpl,
  clearIntervalImpl,
  nowImpl,
  subagentsService,
  getSubagentsService,
  isIdle,
  immediateScheduler = true,
} = {}) {
  isolateCompactionEnvironment(t);
  const previousRecipient = process.env.MECHA_PI_NOTIFY_ON_SETTLED;
  if (recipient === undefined) delete process.env.MECHA_PI_NOTIFY_ON_SETTLED;
  else process.env.MECHA_PI_NOTIFY_ON_SETTLED = recipient;
  t.after(() => {
    if (previousRecipient === undefined) delete process.env.MECHA_PI_NOTIFY_ON_SETTLED;
    else process.env.MECHA_PI_NOTIFY_ON_SETTLED = previousRecipient;
  });

  const registeredHandlers = new Map();
  const handlers = {
    get(type) {
      const list = registeredHandlers.get(type);
      if (list === undefined) return undefined;
      return (...args) => {
        const invoked = type === "session_shutdown" ? list : list.slice(-1);
        const results = invoked.map((handler) => handler(...args));
        return results.length === 1 ? results[0] : Promise.all(results);
      };
    },
    has(type) { return registeredHandlers.has(type); },
  };
  const childEvents = new Map();
  const tools = new Map();
  const notifications = [];
  const sent = [];
  const activity = [];
  const lifecycle = [];
  const context = {
    sessionManager: { getSessionId: () => "parent-session" },
    ui: { notify: (...args) => notifications.push(args), setStatus() {} },
    ...(isIdle !== undefined ? { isIdle } : {}),
  };
  const client = {
    connect: () => Promise.resolve(),
    close() {},
    sendReply,
    sendReceipt() {},
    sendActivity: (state, options) => activity.push({
      state,
      subagentCount: options?.subagentCount ?? 0,
      ...(Array.isArray(options?.subagents) && options.subagents.length > 0 ? { subagents: options.subagents } : {}),
    }),
    sendLifecycleSnapshot: (subagents) => lifecycle.push({ type: "snapshot", subagents }),
    sendLifecycleEvent: (event) => lifecycle.push({ type: "event", ...event }),
  };
  const pi = {
    events: { on(type, handler) { childEvents.set(type, handler); } },
    on(type, handler) {
      const list = registeredHandlers.get(type) ?? [];
      list.push(handler);
      registeredHandlers.set(type, list);
    },
    registerCommand() {},
    registerTool(tool) { tools.set(tool.name, tool); },
    sendMessage: sendMessage ?? ((...args) => sent.push(args)),
    exec: async () => ({ code: 0, stderr: "" }),
  };
  const extensionOptions = {
    resolveCurrentAgentId: () => "worker",
    createClient: (options) => {
      client.onEnvelope = options.onEnvelope;
      return client;
    },
    setTimeoutImpl,
    setIntervalImpl,
    clearIntervalImpl,
    nowImpl,
    getSubagentsService: getSubagentsService ?? ((ownerSessionId) => (
      ownerSessionId === "parent-session"
        ? subagentsService ?? globalThis[Symbol.for("@gotgenes/pi-subagents:service")]
        : undefined
    )),
  };
  if (createSettledScheduler !== undefined) extensionOptions.createSettledScheduler = createSettledScheduler;
  else if (immediateScheduler) extensionOptions.createSettledScheduler = immediateSettledScheduler;
  createMechaMessagingExtension(pi, extensionOptions);
  handlers.get("session_start")({}, context);
  // Startup identity is covered by the dedicated startup test. Keep this fixture's
  // send buffer focused on the behavior under test.
  sent.length = 0;
  return {
    childEvents,
    client,
    context,
    handlers,
    notifications,
    sent,
    activity,
    lifecycle,
    tools,
  };
}

async function enableSettledNotifications(fixture) {
  const result = await fixture.tools.get("mecha_settled_notifications").execute("enable", { action: "enable" });
  assert.equal(result.isError, undefined);
  assert.equal(result.details.enabled, true);
}

test("extension reports top-level agent activity and settled transitions", async (t) => {
  const fixture = settledNotificationFixture(t);

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);

  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 0 },
    { state: "settled", subagentCount: 0 },
  ]);
});

test("native lifecycle visibility polls full sanitized snapshots every two seconds", (t) => {
  const intervals = [];
  let subscribeCalls = 0;
  const completed = Object.freeze({ id: "child", type: "general-purpose", description: "Completed child", status: "completed", result: "kept result" });
  let snapshots = Object.freeze([]);
  const service = {
    getLifecycleSnapshots: () => snapshots,
    subscribeLifecycle() { subscribeCalls += 1; return () => {}; },
    getRecord: (id) => id === completed.id ? completed : undefined,
  };
  const fixture = settledNotificationFixture(t, {
    subagentsService: service,
    setIntervalImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false, unref() {} };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.childEvents.get("subagents:started")({ id: "child" });
  fixture.childEvents.get("subagents:completed")({ id: "child" });
  assert.deepEqual(fixture.lifecycle, []);
  assert.equal(subscribeCalls, 0);
  assert.equal(intervals[0].delay, 2_000);

  snapshots = Object.freeze([Object.freeze({
    id: "child", type: "general-purpose", description: "Safe child", status: "running",
    prompt: "private prompt", output: "private output", result: "private result", error: "private error", startedAt: 1,
  })]);
  intervals[0].callback();
  assert.deepEqual(fixture.lifecycle.at(-1), {
    type: "snapshot",
    subagents: [{ id: "child", type: "general-purpose", description: "Safe child", status: "running" }],
  });
  assert.deepEqual(Object.keys(fixture.lifecycle.at(-1).subagents[0]), ["id", "type", "description", "status"]);

  snapshots = Object.freeze([]);
  fixture.childEvents.get("subagents:completed")({ id: "child" });
  assert.equal(fixture.lifecycle.length, 1, "terminal events must not publish lifecycle frames");
  intervals[0].callback();
  assert.deepEqual(fixture.lifecycle.at(-1), { type: "snapshot", subagents: [] });
  assert.equal(service.getRecord(completed.id).result, "kept result");
});

test("extension bounds an invalid oversized lifecycle projection to 100 records", (t) => {
  const snapshots = Object.freeze(Array.from({ length: 101 }, (_, index) => Object.freeze({
    id: `child-${String(index).padStart(3, "0")}`,
    type: "general-purpose",
    description: `Child ${index}`,
    status: "queued",
  })));
  const intervals = [];
  const fixture = settledNotificationFixture(t, {
    subagentsService: { getLifecycleSnapshots: () => snapshots },
    setIntervalImpl: (callback) => {
      const timer = { callback, unref() {} };
      intervals.push(timer);
      return timer;
    },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  intervals[0].callback();
  assert.equal(fixture.activity.at(-1).subagentCount, 100);
  assert.equal(fixture.activity.at(-1).subagents.length, 100);
});

test("extension refreshes unchanged activity less often than the heartbeat tick", async (t) => {
  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  assert.equal(intervals[0].delay, 2_000);
  now += 5_000;
  intervals[0].callback();
  now += 5_000;
  intervals[0].callback();
  fixture.handlers.get("agent_settled")({}, fixture.context);
  intervals[0].callback();

  // Settled alone must not clear the heartbeat: polling may still need to
  // repair native lifecycle events the parent never observed.
  assert.equal(intervals[0].cleared, false);
  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 0 },
    { state: "active", subagentCount: 0 },
    { state: "settled", subagentCount: 0 },
  ]);
});

test("heartbeat recomputes a stale active parent as settled from Pi context", async (t) => {
  const intervals = [];
  let now = 0;
  let idle = false;
  const fixture = settledNotificationFixture(t, {
    isIdle: () => idle,
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  idle = true;
  now += 5_000;
  intervals[0].callback();

  // Settled alone must not clear the heartbeat even when Pi context confirms
  // idle: polling may still need to repair missed native lifecycle events.
  assert.equal(intervals[0].cleared, false);
  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 0 },
    { state: "settled", subagentCount: 0 },
  ]);
});

test("heartbeat recomputes a stale settled parent as active from Pi context", async (t) => {
  const intervals = [];
  let now = 0;
  let idle = true;
  const fixture = settledNotificationFixture(t, {
    isIdle: () => idle,
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.childEvents.get("subagents:started")({ id: "child-one" });
  idle = false;
  now += 5_000;
  intervals[0].callback();

  assert.equal(intervals[0].cleared, false);
  assert.deepEqual(fixture.activity, [
    { state: "settled", subagentCount: 1 },
    { state: "active", subagentCount: 1 },
  ]);
});

test("agent_settled does not stop heartbeats while Pi context is still active", async (t) => {
  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    isIdle: () => false,
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  now += 10_000;
  intervals[0].callback();

  assert.equal(intervals[0].cleared, false);
  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 0 },
    { state: "active", subagentCount: 0 },
  ]);
});

test("extension excludes child sessions from activity reporting", async (t) => {
  const fixture = settledNotificationFixture(t);
  const childContext = { sessionManager: { getSessionId: () => "child-session" }, ui: { notify() {}, setStatus() {} } };
  fixture.childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });

  fixture.handlers.get("agent_start")({}, childContext);
  fixture.handlers.get("agent_settled")({}, childContext);

  assert.deepEqual(fixture.activity, []);
});

test("extension sends settled and keeps active heartbeats when parent settles without subagents", async (t) => {
  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  now += 5_000;
  intervals[0].callback();
  fixture.handlers.get("agent_settled")({}, fixture.context);
  now += 10_000;
  intervals[0].callback();

  // Settled alone must not clear the heartbeat: polling may still need to
  // repair missed native lifecycle events even with no known children.
  assert.equal(intervals[0].cleared, false);
  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 0 },
    { state: "settled", subagentCount: 0 },
  ]);
});

test("extension reports running native subagent counts in activity", async (t) => {
  const fixture = settledNotificationFixture(t);

  fixture.childEvents.get("subagents:started")({ id: "child-one" });
  fixture.childEvents.get("subagents:started")({ id: "child-two" });
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.childEvents.get("subagents:completed")({ id: "child-one" });
  fixture.childEvents.get("subagents:failed")({ id: "child-two" });

  assert.deepEqual(fixture.activity, [
    { state: "settled", subagentCount: 1 },
    { state: "settled", subagentCount: 2 },
    { state: "active", subagentCount: 2 },
    { state: "active", subagentCount: 1 },
    { state: "active", subagentCount: 0 },
  ]);
});

test("native start and terminal events do not publish lifecycle frames", async (t) => {
  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.childEvents.get("subagents:started")({
    id: "queued-child",
    type: "general-purpose",
    description: "Soak sleeper 1 2m",
    status: "queued",
  });
  fixture.childEvents.get("subagents:completed")({
    id: "terminal-first-child",
    description: "Soak sleeper 2 2.5m",
    status: "completed",
  });
  now += 5_000;
  intervals[0].callback();

  assert.deepEqual(fixture.lifecycle, []);
  assert.equal(fixture.activity.at(-1).subagentCount, 1, "queued native child must remain visible as running");
});

test("native terminal events keep local activity but publish no lifecycle frames", async (t) => {
  const fixture = settledNotificationFixture(t);
  const terminals = [
    ["subagents:completed", "completed"], ["subagents:failed", "failed"],
    ["subagents:canceled", "canceled"], ["subagents:cancelled", "canceled"],
    ["subagents:aborted", "aborted"], ["subagents:terminated", "terminated"],
    ["subagents:stopped", "stopped"], ["subagents:steered", "steered"],
  ];

  for (const [index, [eventName, terminalReason]] of terminals.entries()) {
    const id = `terminal-${index}`;
    fixture.childEvents.get("subagents:started")({ id, description: `Soak sleeper ${index}`, status: "running" });
    fixture.childEvents.get(eventName)({ id, description: `Soak sleeper ${index}`, status: terminalReason });
    assert.equal(fixture.lifecycle.length, 0);
  }
  assert.equal(fixture.activity.at(-1).subagentCount, 0);
  assert.deepEqual(fixture.lifecycle, []);
});

test("extension ignores no-op native subagent lifecycle events", async (t) => {
  const fixture = settledNotificationFixture(t);

  fixture.childEvents.get("subagents:started")({ id: "child-one" });
  fixture.childEvents.get("subagents:started")({ id: "child-one" });
  fixture.childEvents.get("subagents:completed")({ id: "unknown-child" });
  fixture.childEvents.get("subagents:failed")({ id: "unknown-child" });
  fixture.childEvents.get("subagents:completed")({ id: "child-one" });

  assert.deepEqual(fixture.activity, [
    { state: "settled", subagentCount: 1 },
    { state: "settled", subagentCount: 0 },
  ]);
});

test("extension keeps subagent count TTL fresh without sending every heartbeat tick", async (t) => {
  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.childEvents.get("subagents:started")({ id: "child-one" });
  assert.equal(intervals[0].delay, 2_000);
  now += 5_000;
  intervals[0].callback();
  now += 5_000;
  intervals[0].callback();
  fixture.childEvents.get("subagents:completed")({ id: "child-one" });
  intervals[0].callback();

  // A terminal lifecycle event drops the child, but settled alone must not
  // clear the heartbeat: polling may still need to repair other missed events.
  assert.equal(intervals[0].cleared, false);
  assert.deepEqual(fixture.activity, [
    { state: "settled", subagentCount: 1 },
    { state: "settled", subagentCount: 1 },
    { state: "settled", subagentCount: 0 },
  ]);
});

test("extension keeps long-running native subagent counts until a terminal event", async (t) => {
  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.childEvents.get("subagents:started")({ id: "child-one" });
  now += 10 * 60_000;
  intervals[0].callback();
  fixture.childEvents.get("subagents:completed")({ id: "child-one" });

  // A terminal lifecycle event drops the child, but settled alone must not
  // clear the heartbeat: polling may still need to repair other missed events.
  assert.equal(intervals[0].cleared, false);
  assert.deepEqual(fixture.activity, [
    { state: "settled", subagentCount: 1 },
    { state: "settled", subagentCount: 1 },
    { state: "settled", subagentCount: 0 },
  ]);
});

test("heartbeat reconciliation keeps event-tracked subagents missing from the native service list", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  delete globalThis[serviceSymbol];
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.childEvents.get("subagents:started")({
    id: "event-only-child",
    type: "general-purpose",
    description: "Started from lifecycle event",
  });
  globalThis[serviceSymbol] = {
    listAgents: () => [{
      id: "service-child",
      type: "general-purpose",
      description: "Reported by native service",
      status: "running",
    }],
  };

  now += 5_000;
  intervals[0].callback();

  assert.deepEqual(fixture.activity, [
    { state: "settled", subagentCount: 1, subagents: [{ id: "event-only-child", type: "general-purpose", description: "Started from lifecycle event" }] },
    { state: "settled", subagentCount: 2, subagents: [
      { id: "event-only-child", type: "general-purpose", description: "Started from lifecycle event" },
      { id: "service-child", type: "general-purpose", description: "Reported by native service" },
    ] },
  ]);
});

test("heartbeat reconciliation clears service-only subagents when the service drops them", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  let serviceRecords = [{
    id: "service-only-child",
    type: "general-purpose",
    description: "Only reported by native service",
    status: "running",
  }];
  globalThis[serviceSymbol] = { listAgents: () => serviceRecords };
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const fixture = settledNotificationFixture(t);

  fixture.handlers.get("agent_start")({}, fixture.context);
  serviceRecords = [];
  fixture.handlers.get("agent_settled")({}, fixture.context);

  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 1, subagents: [{ id: "service-only-child", type: "general-purpose", description: "Only reported by native service" }] },
    { state: "settled", subagentCount: 0 },
  ]);
});

test("event-tracked subagents age out after the native service misses them", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  globalThis[serviceSymbol] = { listAgents: () => [] };
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const intervals = [];
  let now = 0;
  let scheduler;
  const fixture = settledNotificationFixture(t, {
    createSettledScheduler: () => {
      scheduler = createSettledNotificationScheduler();
      return scheduler;
    },
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.childEvents.get("subagents:started")({
    id: "escaped-child",
    type: "general-purpose",
    description: "Child interrupted by Escape",
  });
  assert.equal(fixture.activity.at(-1).subagentCount, 1);

  now = 5_000;
  intervals[0].callback();
  assert.equal(fixture.activity.at(-1).subagentCount, 1, "ordinary service-list lag dropped a live child");

  now = 60_001;
  intervals[0].callback();
  assert.equal(fixture.activity.at(-1).subagentCount, 0, "missing child stayed visible forever");
  assert.equal(scheduler.activeChildCount(), 0, "aged-out child still blocked settled notifications");
});

test("lifecycle terminal events prevent stale native service resurrection until ID reuse", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  let serviceRecords = [];
  globalThis[serviceSymbol] = { listAgents: () => serviceRecords };
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.childEvents.get("subagents:started")({
    id: "reused-child",
    type: "general-purpose",
    description: "Original child",
  });
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.childEvents.get("subagents:completed")({ id: "reused-child" });
  serviceRecords = [{
    id: "reused-child",
    type: "general-purpose",
    description: "Original child",
    status: "running",
  }];
  now += 5_000;
  intervals[0].callback();
  fixture.childEvents.get("subagents:started")({
    id: "reused-child",
    type: "general-purpose",
    description: "Reused child",
  });

  assert.deepEqual(fixture.activity, [
    { state: "settled", subagentCount: 1, subagents: [{ id: "reused-child", type: "general-purpose", description: "Original child" }] },
    { state: "active", subagentCount: 1, subagents: [{ id: "reused-child", type: "general-purpose", description: "Original child" }] },
    { state: "active", subagentCount: 0 },
    { state: "active", subagentCount: 1, subagents: [{ id: "reused-child", type: "general-purpose", description: "Reused child" }] },
  ]);
});

test("extension reconciles late-published native subagent service on heartbeat", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  delete globalThis[serviceSymbol];
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });

  fixture.childEvents.get("subagents:started")({
    id: "foreground-finished",
    type: "general-purpose",
    description: "Foreground finished",
  });
  fixture.childEvents.get("subagents:started")({
    id: "child-queued",
    type: "general-purpose",
    description: "Queued child",
  });
  assert.equal(fixture.activity.at(-1).subagentCount, 2);

  globalThis[serviceSymbol] = {
    listAgents: () => [
      {
        id: "foreground-finished",
        type: "general-purpose",
        description: "Foreground finished",
        status: "error",
      },
      {
        id: "child-queued",
        type: "general-purpose",
        description: "Queued child",
        status: "queued",
      },
    ],
  };

  now += 5_000;
  intervals[0].callback();

  assert.deepEqual(fixture.activity, [
    { state: "settled", subagentCount: 1, subagents: [{ id: "foreground-finished", type: "general-purpose", description: "Foreground finished" }] },
    { state: "settled", subagentCount: 2, subagents: [
      { id: "child-queued", type: "general-purpose", description: "Queued child" },
      { id: "foreground-finished", type: "general-purpose", description: "Foreground finished" },
    ] },
    { state: "settled", subagentCount: 1, subagents: [{ id: "child-queued", type: "general-purpose", description: "Queued child" }] },
  ]);
});

function heartbeatIntervalFixture(t, { subagentsService, getSubagentsService, isIdle } = {}) {
  const intervals = [];
  let now = 0;
  const fixture = settledNotificationFixture(t, {
    subagentsService,
    getSubagentsService,
    isIdle,
    nowImpl: () => now,
    setIntervalImpl: (callback, delay) => {
      const timer = { delay, cleared: false, unref() {} };
      timer.callback = () => { if (!timer.cleared) callback(); };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => { timer.cleared = true; },
  });
  return { fixture, intervals, advance(ms) { now += ms; }, get now() { return now; } };
}

test("parent-bound native service wins when a child session replaces the global service", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  delete globalThis[serviceSymbol];
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const parentService = {
    listAgents: () => [{
      id: "parent-child",
      type: "general-purpose",
      description: "Reported by the captured parent service",
      status: "running",
    }],
  };
  const { fixture, intervals, advance } = heartbeatIntervalFixture(t, { subagentsService: parentService });

  fixture.handlers.get("agent_start")({}, fixture.context);
  // A child session that loads after the parent replaces the process-global
  // service handle with its own. The parent's activity must still reflect the
  // parent-bound captured service, not the child's replacement.
  globalThis[serviceSymbol] = {
    listAgents: () => [{
      id: "child-session-replacement",
      type: "general-purpose",
      description: "Reported by the child's global replacement",
      status: "running",
    }],
  };

  advance(5_000);
  intervals[0].callback();

  assert.deepEqual(fixture.activity.at(-1), {
    state: "active",
    subagentCount: 1,
    subagents: [{ id: "parent-child", type: "general-purpose", description: "Reported by the captured parent service" }],
  });
});

test("owner-scoped lookup keeps the parent service when a child is also registered", (t) => {
  const parentService = {
    getLifecycleSnapshots: () => [{
      id: "parent-child", type: "general-purpose", description: "Parent service child", status: "running",
    }],
  };
  const childService = { getLifecycleSnapshots: () => [] };
  const lookups = [];
  const { fixture, intervals, advance } = heartbeatIntervalFixture(t, {
    getSubagentsService(ownerSessionId) {
      lookups.push(ownerSessionId);
      return ownerSessionId === "parent-session" ? parentService : childService;
    },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  advance(5_000);
  intervals[0].callback();

  assert.ok(lookups.length >= 2);
  assert.deepEqual([...new Set(lookups)], ["parent-session"]);
  assert.deepEqual(fixture.activity.at(-1), {
    state: "active",
    subagentCount: 1,
    subagents: [{ id: "parent-child", type: "general-purpose", description: "Parent service child", status: "running" }],
  });
});

test("Sentinel delivery looks up the native service by its parent session ID", async (t) => {
  const calls = [];
  const parentService = { spawn: (...args) => { calls.push(args); return "parent-child"; } };
  const childService = { spawn: () => "child-service" };
  const lookups = [];
  const fixture = settledNotificationFixture(t, {
    getSubagentsService(ownerSessionId) {
      lookups.push(ownerSessionId);
      return ownerSessionId === "parent-session" ? parentService : childService;
    },
  });

  await fixture.client.onEnvelope({
    id: "owner-scoped-sentinel",
    to: "worker",
    body: "scheduled work",
    sentinel: { model: "gpt-5.6-sol" },
  }, { messageId: "owner-scoped-sentinel", attemptNumber: 1 });

  assert.deepEqual(lookups, ["parent-session"]);
  assert.deepEqual(calls, [["general-purpose", "scheduled work", {
    description: "Scheduled Sentinel work", model: "gpt-5.6-sol", inheritContext: true,
  }]]);
});

test("polling discovers a native subagent start missed by lifecycle events while the parent is settled", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  let serviceRecords = [];
  globalThis[serviceSymbol] = { listAgents: () => serviceRecords };
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const { fixture, intervals, advance } = heartbeatIntervalFixture(t);

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  // No lifecycle event fired; the native manager started a child that the
  // parent never observed. Polling while settled must repair that gap.
  serviceRecords = [{
    id: "missed-start-child",
    type: "general-purpose",
    description: "Started without a lifecycle event",
    status: "running",
  }];

  advance(5_000);
  intervals[0].callback();

  assert.deepEqual(fixture.activity.at(-1), {
    state: "settled",
    subagentCount: 1,
    subagents: [{ id: "missed-start-child", type: "general-purpose", description: "Started without a lifecycle event" }],
  });
});

test("polling discovers a native subagent finish missed by lifecycle events while the parent is settled", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  let serviceRecords = [{
    id: "missed-finish-child",
    type: "general-purpose",
    description: "Finished without a lifecycle event",
    status: "running",
  }];
  globalThis[serviceSymbol] = { listAgents: () => serviceRecords };
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const { fixture, intervals, advance } = heartbeatIntervalFixture(t);

  fixture.childEvents.get("subagents:started")({
    id: "missed-finish-child",
    type: "general-purpose",
    description: "Finished without a lifecycle event",
  });
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  // The native manager finished the child without emitting a terminal
  // lifecycle event. Polling while settled must converge the count to zero.
  serviceRecords = [{
    id: "missed-finish-child",
    type: "general-purpose",
    description: "Finished without a lifecycle event",
    status: "completed",
  }];

  advance(5_000);
  intervals[0].callback();

  assert.equal(fixture.activity.at(-1).state, "settled");
  assert.equal(fixture.activity.at(-1).subagentCount, 0);
});

test("polling continues while the parent is active or waiting for native subagent results", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  let serviceRecords = [];
  globalThis[serviceSymbol] = { listAgents: () => serviceRecords };
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const { fixture, intervals, advance } = heartbeatIntervalFixture(t);

  fixture.handlers.get("agent_start")({}, fixture.context);
  // The parent is active and waiting on a native subagent result. Polling
  // must keep running so a late-published service record is discovered.
  advance(5_000);
  intervals[0].callback();
  assert.equal(intervals[0].cleared, false, "heartbeat stopped while the parent was still active");

  serviceRecords = [{
    id: "late-active-child",
    type: "general-purpose",
    description: "Published while the parent was active",
    status: "running",
  }];
  advance(5_000);
  intervals[0].callback();

  assert.deepEqual(fixture.activity.at(-1), {
    state: "active",
    subagentCount: 1,
    subagents: [{ id: "late-active-child", type: "general-purpose", description: "Published while the parent was active" }],
  });
  assert.equal(intervals[0].cleared, false);
});

test("terminal native statuses including steered stop contributing to the running count", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  let serviceRecords = [];
  globalThis[serviceSymbol] = { listAgents: () => serviceRecords };
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const { fixture, intervals, advance } = heartbeatIntervalFixture(t);

  fixture.childEvents.get("subagents:started")({
    id: "steered-child",
    type: "general-purpose",
    description: "Steered away mid-run",
  });
  fixture.handlers.get("agent_start")({}, fixture.context);
  // The native manager reports the child as `steered`, a terminal status the
  // parent never observed via a lifecycle event. Reconciliation must remove it
  // from the running count just like completed/failed/error.
  serviceRecords = [{
    id: "steered-child",
    type: "general-purpose",
    description: "Steered away mid-run",
    status: "steered",
  }];

  advance(5_000);
  intervals[0].callback();

  assert.equal(fixture.activity.at(-1).state, "active");
  assert.equal(fixture.activity.at(-1).subagentCount, 0);
});

test("native service read failures do not stop polling and keep diagnostics bounded", async (t) => {
  const serviceSymbol = Symbol.for("@gotgenes/pi-subagents:service");
  const previousService = globalThis[serviceSymbol];
  let readCount = 0;
  let failureCount = 0;
  let elapsed = 0;
  globalThis[serviceSymbol] = {
    listAgents() {
      readCount += 1;
      if ([5_000, 10_000, 15_000, 55_000, 65_000].includes(elapsed)) {
        failureCount += 1;
        throw new Error(`service read failure ${readCount}`);
      }
      return [{
        id: "recovered-child",
        type: "general-purpose",
        description: "Recovered after transient service failures",
        status: "running",
      }];
    },
  };
  t.after(() => {
    if (previousService === undefined) delete globalThis[serviceSymbol];
    else globalThis[serviceSymbol] = previousService;
  });

  const { fixture, intervals, advance } = heartbeatIntervalFixture(t);

  fixture.handlers.get("agent_start")({}, fixture.context);
  for (let tick = 0; tick < 3; tick += 1) {
    elapsed += 5_000;
    advance(5_000);
    intervals[0].callback();
  }
  // The heartbeat must still be alive after repeated read failures so the
  // next successful read can repair the activity state.
  assert.equal(intervals[0].cleared, false, "heartbeat stopped after transient service read failures");
  assert.equal(failureCount, 3);
  assert.deepEqual(fixture.notifications, [["Native subagent service read failed; polling will retry.", "error"]]);

  // A failure just before the 60-second diagnostic window must remain rate-limited.
  elapsed += 40_000;
  advance(40_000);
  intervals[0].callback();
  assert.equal(failureCount, 4);
  assert.deepEqual(fixture.notifications, [["Native subagent service read failed; polling will retry.", "error"]]);

  // Once the window has elapsed, the next failed read gets a new diagnostic.
  elapsed += 10_000;
  advance(10_000);
  intervals[0].callback();
  assert.equal(failureCount, 5);
  assert.deepEqual(fixture.notifications, [
    ["Native subagent service read failed; polling will retry.", "error"],
    ["Native subagent service read failed; polling will retry.", "error"],
  ]);

  elapsed += 5_000;
  advance(5_000);
  intervals[0].callback();

  assert.deepEqual(fixture.activity.at(-1), {
    state: "active",
    subagentCount: 1,
    subagents: [{ id: "recovered-child", type: "general-purpose", description: "Recovered after transient service failures" }],
  });
});

test("compaction pauses activity without dropping mailbox resume", async (t) => {
  const calls = [];
  let idle = false;
  const fixture = settledNotificationFixture(t, { isIdle: () => idle });
  fixture.client.pause = () => calls.push("pause");
  fixture.client.resume = () => { calls.push("resume"); return Promise.resolve(); };

  fixture.handlers.get("agent_start")({}, fixture.context);
  idle = true;
  await fixture.handlers.get("session_before_compact")({ signal: new AbortController().signal, preparation: {} }, fixture.context);
  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 0 },
    { state: "compacting", subagentCount: 0 },
  ]);
  fixture.handlers.get("session_compact")({}, fixture.context);

  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 0 },
    { state: "compacting", subagentCount: 0 },
    { state: "settled", subagentCount: 0 },
  ]);
  assert.deepEqual(calls, []);
});

test("canceled compaction resumes mailbox activity instead of staying paused", async (t) => {
  const fixture = settledNotificationFixture(t);
  const controller = new AbortController();

  fixture.handlers.get("agent_start")({}, fixture.context);
  const beforeCompact = fixture.handlers.get("session_before_compact")({ signal: controller.signal, preparation: {} }, fixture.context);
  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 0 },
    { state: "compacting", subagentCount: 0 },
  ]);

  controller.abort();
  await beforeCompact;
  fixture.handlers.get("agent_settled")({}, fixture.context);

  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 0 },
    { state: "compacting", subagentCount: 0 },
    { state: "settled", subagentCount: 0 },
  ]);
});

test("compaction resume reconciles already-running native subagents", async (t) => {
  const calls = [];
  const fixture = settledNotificationFixture(t, {
    subagentsService: {
      listAgents: () => [{
        id: "child-survived",
        type: "general-purpose",
        description: "Survived compaction",
        status: "running",
        startedAt: 123,
      }],
    },
  });
  fixture.client.pause = () => calls.push("pause");
  fixture.client.resume = () => { calls.push("resume"); return Promise.resolve(); };

  fixture.handlers.get("agent_start")({}, fixture.context);
  await fixture.handlers.get("session_before_compact")({ signal: AbortSignal.timeout(100), preparation: {} }, fixture.context);
  fixture.handlers.get("session_compact")({}, fixture.context);
  await Promise.resolve();
  await Promise.resolve();

  assert.deepEqual(fixture.activity, [
    { state: "active", subagentCount: 1, subagents: [{ id: "child-survived", type: "general-purpose", description: "Survived compaction" }] },
    { state: "compacting", subagentCount: 0 },
    { state: "settled", subagentCount: 1, subagents: [{ id: "child-survived", type: "general-purpose", description: "Survived compaction" }] },
  ]);
  assert.deepEqual(calls, []);
});

test("settled notification sends one exact reply when top-level work settles", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  await enableSettledNotifications(fixture);
  assert.deepEqual(attempts, []);
  assert.equal(fixture.handlers.has("agent_end"), false);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.deepEqual(attempts, [{
    to: "supervisor",
    subject: "Pi worker settled",
    body: "Worker worker has settled and is idle.",
  }]);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 1);
});

test("self-compaction suppresses the settled reminder at an idle boundary", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });
  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    isIdle: () => true,
    compact: (options) => compactCalls.push(options),
  };

  await enableSettledNotifications(fixture);
  fixture.handlers.get("agent_start")({}, fixture.context);
  const result = await fixture.tools.get("mecha_context").execute("compact", { action: "compact" }, undefined, undefined, compactContext);
  assert.equal(result.details.compactState, "deferred_until_agent_settled");

  fixture.handlers.get("agent_settled")({}, compactContext);
  assert.equal(compactCalls.length, 1);
  assert.equal(attempts.length, 0);
});

test("deferred self-compaction suppresses reminders until its later idle settlement", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });
  const compactCalls = [];
  let idle = false;
  const compactContext = {
    ...fixture.context,
    isIdle: () => idle,
    compact: (options) => compactCalls.push(options),
  };

  await enableSettledNotifications(fixture);
  fixture.handlers.get("agent_start")({}, fixture.context);
  await fixture.tools.get("mecha_context").execute("compact", { action: "compact" }, undefined, undefined, compactContext);

  fixture.handlers.get("agent_settled")({}, compactContext);
  assert.equal(compactCalls.length, 0);
  assert.equal(attempts.length, 0);

  fixture.handlers.get("agent_start")({}, fixture.context);
  idle = true;
  fixture.handlers.get("agent_settled")({}, compactContext);
  assert.equal(compactCalls.length, 1);
  assert.equal(attempts.length, 0);
});

test("settled notification stays disabled without a valid opt-in", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);
});

test("mecha_settled_notifications defaults off, then enables and disables process-local settled notices", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });
  const tool = fixture.tools.get("mecha_settled_notifications");

  const initial = await tool.execute("status", { action: "status" });
  assert.equal(initial.details.enabled, false);
  assert.equal(initial.details.configured, true);
  assert.equal(initial.details.cooldownMs, 120_000);

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);

  const enabled = await tool.execute("enable", { action: "enable" });
  assert.equal(enabled.details.enabled, true);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 1);

  const disabled = await tool.execute("disable", { action: "disable" });
  assert.equal(disabled.details.enabled, false);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 1);
});

test("mecha_settled_notifications cannot enable an unconfigured process", async (t) => {
  const fixture = settledNotificationFixture(t);
  const result = await fixture.tools.get("mecha_settled_notifications").execute("enable", { action: "enable" });

  assert.equal(result.isError, true);
  assert.equal(result.details.configured, false);
  assert.match(result.content[0].text, /not configured/);
});

test("settled notification integration waits for the real cooldown", async (t) => {
  const clock = fakeTimers();
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    immediateScheduler: false,
    createSettledScheduler: () => createSettledNotificationScheduler({
      setTimeoutImpl: clock.setTimeout,
      clearTimeoutImpl: clock.clearTimeout,
    }),
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  await enableSettledNotifications(fixture);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  clock.advanceTo(119_999);
  assert.equal(attempts.length, 0);
  clock.advanceTo(120_000);
  assert.deepEqual(attempts, [{
    to: "supervisor",
    subject: "Pi worker settled",
    body: "Worker worker has settled and is idle.",
  }]);
});

test("settled notification integration disables and cancels a pending cooldown", async (t) => {
  const clock = fakeTimers();
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    immediateScheduler: false,
    createSettledScheduler: () => createSettledNotificationScheduler({
      setTimeoutImpl: clock.setTimeout,
      clearTimeoutImpl: clock.clearTimeout,
    }),
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  const tool = fixture.tools.get("mecha_settled_notifications");
  await enableSettledNotifications(fixture);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  await tool.execute("disable", { action: "disable" });
  clock.advanceTo(120_000);
  assert.equal(attempts.length, 0);
});

test("settled notification integration cancels the cooldown when child work starts", async (t) => {
  const clock = fakeTimers();
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    immediateScheduler: false,
    createSettledScheduler: () => createSettledNotificationScheduler({
      setTimeoutImpl: clock.setTimeout,
      clearTimeoutImpl: clock.clearTimeout,
    }),
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  await enableSettledNotifications(fixture);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  fixture.childEvents.get("subagents:started")({ id: "child-run" });
  clock.advanceTo(120_000);
  fixture.childEvents.get("subagents:completed")({ id: "child-run" });
  clock.advanceTo(600_000);
  assert.equal(attempts.length, 0);
});

test("settled notification excludes child sessions", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  await enableSettledNotifications(fixture);
  fixture.childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });
  const childContext = {
    ...fixture.context,
    sessionManager: { getSessionId: () => "child-session" },
  };
  fixture.handlers.get("session_start")({}, childContext);
  fixture.handlers.get("agent_start")({}, childContext);
  fixture.handlers.get("agent_settled")({}, childContext);
  assert.equal(attempts.length, 0);
  fixture.childEvents.get("subagents:child:disposed")({ sessionId: "child-session" });
});

test("native child lifecycle suppresses parent settled notices until a new parent settle", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  await enableSettledNotifications(fixture);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.childEvents.get("subagents:started")({ id: "child-run" });
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);

  fixture.childEvents.get("subagents:completed")({ id: "child-run" });
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 1);
});

test("native child failure releases parent suppression for the next parent settle", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  await enableSettledNotifications(fixture);
  fixture.childEvents.get("subagents:started")({ id: "child-run" });
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  fixture.childEvents.get("subagents:failed")({ id: "child-run" });
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);

  assert.equal(attempts.length, 1);
});

test("parent session shutdown disposes armed settled follow-ups", async (t) => {
  const calls = [];
  const scheduler = {
    onParentActivity() { calls.push("activity"); },
    onParentSettled(send) { calls.push("settled"); send(); },
    onChildStarted() {},
    onChildDisposed() {},
    dispose() { calls.push("dispose"); },
  };
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    createSettledScheduler: () => scheduler,
  });

  await enableSettledNotifications(fixture);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  let shutdownBranchReads = 0;
  fixture.context.sessionManager.getBranch = () => {
    shutdownBranchReads += 1;
    throw new Error("capture setup is outside this harness");
  };
  await fixture.handlers.get("session_shutdown")({}, fixture.context);

  assert.equal(shutdownBranchReads, 1);
  assert.deepEqual(calls, ["activity", "settled", "dispose"]);
});

test("child sessions do not schedule recall work", async (t) => {
  const fixture = settledNotificationFixture(t, { recipient: "supervisor" });
  const previousFetch = globalThis.fetch;
  let fetches = 0;
  globalThis.fetch = async () => { fetches += 1; throw new Error("child recall must not fetch policy"); };
  t.after(() => { globalThis.fetch = previousFetch; });

  await fixture.tools.get("mecha_recall").execute("enable-recall", { action: "enable", stores: ["default"] });
  fixture.childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });
  const childContext = { ...fixture.context, sessionManager: { getSessionId: () => "child-session" } };
  fixture.handlers.get("message_start")({}, childContext);
  fixture.handlers.get("agent_settled")({}, childContext);
  await nextTurn();

  assert.equal(fetches, 0);
  fixture.childEvents.get("subagents:child:disposed")({ sessionId: "child-session" });
});

test("an invalid settled recipient leaves notifications disabled", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "not a mailbox agent",
    sendReply: async (message) => { attempts.push(message); return { id: "reply" }; },
  });

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 0);
});

test("a rejected settled notification reports once and does not block a later run", async (t) => {
  const attempts = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply(message) {
      attempts.push(message);
      return attempts.length === 1 ? Promise.reject(new Error("router rejected")) : Promise.resolve({ id: "reply" });
    },
  });

  await enableSettledNotifications(fixture);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  await nextTurn();
  assert.deepEqual(fixture.notifications, [["Pi settled notification failed: router rejected", "error"]]);
  assert.equal(attempts.length, 1);

  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  await nextTurn();
  assert.equal(attempts.length, 2);
  assert.deepEqual(fixture.notifications, [["Pi settled notification failed: router rejected", "error"]]);
});

test("overlapping settled notification sends make independent attempts", async (t) => {
  const attempts = [];
  const deferred = [];
  const fixture = settledNotificationFixture(t, {
    recipient: "supervisor",
    sendReply(message) {
      attempts.push(message);
      return new Promise((resolve) => deferred.push(resolve));
    },
  });

  await enableSettledNotifications(fixture);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  fixture.handlers.get("agent_start")({}, fixture.context);
  fixture.handlers.get("agent_settled")({}, fixture.context);
  assert.equal(attempts.length, 2);

  deferred[0]({ id: "first" });
  await nextTurn();
  assert.equal(attempts.length, 2);
  deferred[1]({ id: "second" });
  await nextTurn();
  assert.equal(attempts.length, 2);
  assert.deepEqual(fixture.notifications, []);
});

test("context monitor injects direct threshold notifications and resets after compaction", async (t) => {
  isolateCompactionEnvironment(t);
  const handlers = new Map();
  const sent = [];
  const tools = new Map();
  let hardAbortCalls = 0;
  let hardCompactCalls = 0;
  const context = {
    sessionManager: { getSessionId: () => "parent-session" },
    ui: { notify() {}, setStatus() {} },
    getContextUsage: () => ({ tokens: 410_000, contextWindow: 1_000_000, percent: 41 }),
    abort: () => { hardAbortCalls += 1; },
    compact: () => { hardCompactCalls += 1; },
  };
  const pi = {
    events: { on() {} },
    on(type, handler) {
      const list = handlers.get(type) ?? [];
      list.push(handler);
      handlers.set(type, list);
    },
    registerCommand() {},
    registerTool(tool) { tools.set(tool.name, tool); },
    sendMessage: (...args) => sent.push(args),
    exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: () => ({ connect: () => Promise.resolve(), close() {}, sendReply: async () => ({}), sendReceipt() {} }),
  });
  const thresholdMessages = () => sent.filter(([message]) => message.customType === "mecha-context-threshold");
  for (const handler of handlers.get("session_start")) handler({}, context);
  for (const handler of handlers.get("context")) await handler({ messages: [] }, context);
  assert.equal(hardAbortCalls, 0);
  assert.equal(hardCompactCalls, 0);
  for (const handler of handlers.get("agent_settled")) await handler({}, context);
  assert.equal(thresholdMessages().length, 1);
  assert.equal(thresholdMessages()[0][0].details.threshold_percent, 40);
  assert.equal(thresholdMessages()[0][0].details.tier_max_context_tokens, 1_000_000);
  assert.deepEqual(thresholdMessages()[0][1], { deliverAs: "followUp", triggerTurn: true });

  for (const handler of handlers.get("agent_settled")) await handler({}, context);
  assert.equal(thresholdMessages().length, 1);
  for (const handler of handlers.get("session_compact")) handler({}, context);
  for (const handler of handlers.get("agent_settled")) await handler({}, context);
  assert.equal(thresholdMessages().length, 2);
});

test("mandatory context guard aborts, blocks tools, and waits for its compaction callback", async (t) => {
  isolateCompactionEnvironment(t);
  process.env.MECHA_PI_COMPACTION_POLICY = JSON.stringify({
    levels: [40, { percent: 50, mandatory: true }],
  });
  const handlers = new Map();
  const sent = [];
  const tools = new Map();
  const compactCalls = [];
  const notifications = [];
  let abortCalls = 0;
  const context = {
    sessionManager: { getSessionId: () => "parent-session" },
    ui: { notify: (...args) => notifications.push(args), setStatus() {} },
    model: { provider: "openai-codex", id: "test-model" },
    getContextUsage: () => ({ tokens: 600, contextWindow: 1_000, percent: 60 }),
    abort: () => { abortCalls += 1; },
    compact: (options) => compactCalls.push(options),
  };
  const pi = {
    events: { on() {} },
    on(type, handler) {
      const list = handlers.get(type) ?? [];
      list.push(handler);
      handlers.set(type, list);
    },
    registerCommand() {},
    registerTool(tool) { tools.set(tool.name, tool); },
    sendMessage: (...args) => sent.push(args),
    exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: () => ({ connect: () => Promise.resolve(), close() {}, sendReply: async () => ({}), sendReceipt() {} }),
  });
  for (const handler of handlers.get("session_start")) await handler({}, context);

  for (const handler of handlers.get("context")) await handler({ messages: [] }, context);
  assert.equal(abortCalls, 1);
  assert.equal(compactCalls.length, 1);
  assert.equal((await handlers.get("tool_call")[0]({ toolName: "read" }, context))?.block, true);

  for (const handler of handlers.get("context")) await handler({ messages: [] }, context);
  assert.equal(abortCalls, 2);
  assert.equal(compactCalls.length, 1);

  for (const handler of handlers.get("agent_settled")) await handler({}, context);
  assert.equal(sent.some(([, options]) => options?.deliverAs === "followUp" && options?.triggerTurn === true), false);

  compactCalls[0].onComplete({});
  assert.equal(await handlers.get("tool_call")[0]({ toolName: "read" }, context), undefined);
  for (const handler of handlers.get("context")) await handler({ messages: [] }, context);
  assert.equal(compactCalls.length, 2);

  compactCalls[1].onError(new Error("compaction canceled"));
  for (const handler of handlers.get("context")) await handler({ messages: [] }, context);
  assert.equal(compactCalls.length, 3);

  compactCalls[2].onComplete({});
  context.compact = () => { throw new Error("boom"); };
  for (const handler of handlers.get("context")) await handler({ messages: [] }, context);
  assert.deepEqual(notifications.at(-1), ["Hard context compaction failed: boom", "error"]);
  assert.equal(await handlers.get("tool_call")[0]({ toolName: "read" }, context), undefined);

  delete context.compact;
  for (const handler of handlers.get("context")) await handler({ messages: [] }, context);
  assert.deepEqual(notifications.at(-1), ["Pi context limit reached, but compaction is unavailable.", "error"]);
  assert.equal(await handlers.get("tool_call")[0]({ toolName: "read" }, context), undefined);

  context.compact = (options) => compactCalls.push(options);
  for (const handler of handlers.get("context")) await handler({ messages: [] }, context);
  assert.equal(compactCalls.length, 4);
});

test("mecha_context tool sets policy and defers self-compaction until agent_settled", async (t) => {
  const fixture = settledNotificationFixture(t);
  const contextTool = fixture.tools.get("mecha_context");
  assert.equal(typeof contextTool.execute, "function");
  assert.match(contextTool.description, /postCompactionNotification is optional/);
  assert.match(contextTool.description, /include it while you are in the middle of an active task, review, or decision loop/);
  assert.match(contextTool.parameters.properties.postCompactionNotification.description, /omit it only when clearly idle\/done and no details are needed/);

  const setResult = await contextTool.execute("set-policy", {
    action: "set_policy",
    policy: { tiers: [{ maxContextTokens: 300_000, levels: [{ percent: 45, guidance: "custom" }] }, { maxContextTokens: null, levels: [{ percent: 85, mandatory: true }] }] },
  }, undefined, undefined, { ...fixture.context, getContextUsage: () => ({ tokens: 45, contextWindow: 100, percent: 45 }) });
  assert.equal(setResult.details.policy.tiers[0].levels[0].percent, 45);
  assert.equal(setResult.details.selectedTier.maxContextTokens, 300_000);

  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    getContextUsage: () => ({ tokens: 45, contextWindow: 100, percent: 45 }),
    isIdle: () => true,
    compact: (options) => compactCalls.push(options),
  };
  const compactResult = await contextTool.execute("compact", {
    action: "compact",
    customInstructions: "preserve task state",
    postCompactionNotification: "Continue with the post-compact checklist.",
  }, undefined, undefined, compactContext);
  assert.equal(compactResult.content[0].text, "Self-compaction deferred until Pi reaches the idle settled boundary.");
  assert.equal(compactResult.details.compactState, "deferred_until_agent_settled");
  assert.equal(compactResult.details.postCompactionNotification, "Continue with the post-compact checklist.");
  assert.equal(compactCalls.length, 0);

  fixture.handlers.get("agent_settled")({}, compactContext);
  assert.equal(compactCalls.length, 1);
  assert.equal(compactCalls[0].customInstructions, "preserve task state");

  compactCalls[0].onComplete({ tokensBefore: 45, estimatedTokensAfter: 12, firstKeptEntryId: "entry-1", summary: "summary" });
  await new Promise((resolve) => setTimeout(resolve, 1));
  assert.equal(fixture.sent.length, 1);
  assert.equal(fixture.sent[0][0].customType, "mecha-post-compaction-handoff");
  assert.match(fixture.sent[0][0].content, /Continue with the post-compact checklist/);
  assert.equal(fixture.sent[0][0].details.tokens_before, 45);
  assert.deepEqual(fixture.sent[0][1], { deliverAs: "followUp", triggerTurn: true });
});

test("failed self-compaction resumes mailbox activity instead of staying paused", async (t) => {
  const fixture = settledNotificationFixture(t);
  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    isIdle: () => true,
    compact: (options) => compactCalls.push(options),
  };

  await fixture.tools.get("mecha_context").execute("compact", { action: "compact" }, undefined, undefined, compactContext);
  fixture.handlers.get("agent_settled")({}, compactContext);
  assert.equal(compactCalls.length, 1);

  await fixture.handlers.get("session_before_compact")({ signal: AbortSignal.timeout(100), preparation: {} }, compactContext);
  assert.deepEqual(fixture.activity, [
    { state: "compacting", subagentCount: 0 },
  ]);

  compactCalls[0].onError(new Error("canceled"));
  assert.deepEqual(fixture.activity, [
    { state: "compacting", subagentCount: 0 },
    { state: "settled", subagentCount: 0 },
  ]);
  assert.equal(fixture.notifications.at(-1)[0], "Self-compaction failed: canceled");
});

test("mecha_context does not compact at a non-idle settled boundary", async (t) => {
  const fixture = settledNotificationFixture(t);
  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    isIdle: () => false,
    compact: (options) => compactCalls.push(options),
  };

  const result = await fixture.tools.get("mecha_context").execute("compact", {
    action: "compact",
    customInstructions: "wait for safety",
  }, undefined, undefined, compactContext);

  assert.equal(result.details.compactState, "deferred_until_agent_settled");
  fixture.handlers.get("agent_settled")({}, compactContext);
  assert.equal(compactCalls.length, 0);
});

test("mecha_context uses configured default post-compaction handoff when command omits one", async (t) => {
  const fixture = settledNotificationFixture(t);
  const contextTool = fixture.tools.get("mecha_context");
  await contextTool.execute("set-policy", {
    action: "set_policy",
    policy: { postCompactionNotification: "Policy handoff note.", levels: [40, 90] },
  }, undefined, undefined, fixture.context);
  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    isIdle: () => true,
    compact: (options) => compactCalls.push(options),
  };
  await contextTool.execute("compact", { action: "compact" }, undefined, undefined, compactContext);
  assert.equal(compactCalls.length, 0);
  fixture.handlers.get("agent_settled")({}, compactContext);
  compactCalls[0].onComplete({ tokensBefore: 90, summary: "summary" });
  await new Promise((resolve) => setTimeout(resolve, 1));
  assert.equal(fixture.sent[0][0].customType, "mecha-post-compaction-handoff");
  assert.match(fixture.sent[0][0].content, /Policy handoff note/);
});

test("memory capture diagnostics are content-free and bounded", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-memory-diagnostics-"));
  const path = join(directory, "memory-capture.jsonl");
  appendMemoryCaptureDiagnostic({ timestamp: "2026-07-27T00:00:00.000Z", trigger: "compaction", status: "applied", reason: "memory-apply", records: 1, source: "do not log" }, { path, maxBytes: 260, keepBytes: 130 });
  appendMemoryCaptureDiagnostic({ timestamp: "2026-07-27T00:00:01.000Z", trigger: "shutdown", status: "failed", reason: "capture apply failed: secret source body" }, { path, maxBytes: 260, keepBytes: 130 });
  assert.equal(JSON.parse(readFileSync(path, "utf8").trim().split("\n").at(-1)).reason, "capture-failed");
  for (let index = 0; index < 10; index += 1) appendMemoryCaptureDiagnostic({ timestamp: `2026-07-27T00:00:${String(index + 2).padStart(2, "0")}.000Z`, trigger: "compaction", status: "noop", reason: "extractor-noop" }, { path, maxBytes: 260, keepBytes: 130 });

  const text = readFileSync(path, "utf8");
  assert.ok(Buffer.byteLength(text) <= 260);
  assert.doesNotMatch(text, /do not log|source body/);
  const records = text.trim().split("\n").map((line) => JSON.parse(line));
  assert.ok(records.length > 0);
  assert.ok(records.every((record) => Object.keys(record).every((key) => ["timestamp", "trigger", "status", "reason", "records"].includes(key))));
  assert.deepEqual(Object.keys(records.at(-1)).sort(), ["reason", "status", "timestamp", "trigger"].sort());
  await rm(directory, { recursive: true, force: true });
});

test("post-compaction handoff is deferred so send failures do not fail compaction", async (t) => {
  const deferred = [];
  const fixture = settledNotificationFixture(t, {
    setTimeoutImpl: (callback, delay) => { deferred.push({ callback, delay }); return deferred.length; },
    sendMessage: () => { throw new Error("Cannot read properties of undefined (reading signal)"); },
  });
  const compactCalls = [];
  const compactContext = {
    ...fixture.context,
    isIdle: () => true,
    compact: (options) => compactCalls.push(options),
  };
  await fixture.tools.get("mecha_context").execute("compact", {
    action: "compact",
    postCompactionNotification: "Do not fail compact.",
  }, undefined, undefined, compactContext);
  assert.equal(compactCalls.length, 0);
  fixture.handlers.get("agent_settled")({}, compactContext);

  assert.doesNotThrow(() => compactCalls[0].onComplete({ tokensBefore: 100, summary: "summary" }));
  assert.equal(deferred.length, 1);
  assert.equal(deferred[0].delay, 0);
  assert.doesNotThrow(() => deferred[0].callback());
});

test("extension forwards Pi message_end into the durable mailbox checkpoint", async (t) => {
  const message = envelope("message-extension-wiring");
  const session = await durableSession(message.id, t);
  const registeredHandlers = new Map();
  const handlers = {
    get(type) {
      const list = registeredHandlers.get(type);
      if (list === undefined) return undefined;
      return (...args) => {
        const invoked = type === "session_shutdown" ? list : list.slice(-1);
        const results = invoked.map((handler) => handler(...args));
        return results.length === 1 ? results[0] : Promise.all(results);
      };
    },
    has(type) { return registeredHandlers.has(type); },
  };
  const childEvents = new Map();
  const sent = [];
  const receipts = [];
  let clientOptions;
  let closeCount = 0;
  const client = {
    connect: () => Promise.resolve(),
    close() { closeCount += 1; },
    sendReply: async () => ({ id: "reply" }),
    sendReceipt(type, delivery) {
      receipts.push({ type, attemptNumber: delivery.attemptNumber });
    },
  };
  const pi = {
    events: { on(type, handler) { childEvents.set(type, handler); } },
    on(type, handler) {
      const list = registeredHandlers.get(type) ?? [];
      list.push(handler);
      registeredHandlers.set(type, list);
    },
    registerCommand() {},
    registerTool() {},
    sendMessage: (...args) => sent.push(args),
    exec: async () => ({ code: 0, stderr: "" }),
  };
  const context = {
    sessionManager: session.manager,
    ui: { notify() {}, setStatus() {} },
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: (options) => {
      clientOptions = options;
      return client;
    },
  });

  handlers.get("session_start")({}, context);
  sent.length = 0;
  clientOptions.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  assert.equal(sent.length, 1);
  assert.equal(sent[0][0].content, "Mailbox message from sender\n\ndurable body");
  assert.deepEqual(sent[0][0].details, {
    message_id: message.id,
    from: "sender",
    to: "worker",
  });
  assert.deepEqual(sent[0][1], { deliverAs: "steer", triggerTurn: true });
  assert.deepEqual(receipts, [{ type: "injection_requested", attemptNumber: 1 }]);

  session.entries.push(sessionEntry(message.id));
  await session.persist();
  handlers.get("message_end")({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  await nextTurn();

  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "ack", attemptNumber: 1 },
  ]);
  const childContext = {
    ...context,
    sessionManager: { ...session.manager, getSessionId: () => "child-session" },
  };
  childEvents.get("subagents:child:session-created")({ sessionId: "child-session" });
  await handlers.get("session_shutdown")({}, childContext);
  assert.equal(closeCount, 0);
  childEvents.get("subagents:child:disposed")({ sessionId: "child-session" });
  const shutdownContext = {
    ...context,
    sessionManager: {
      ...session.manager,
      getBranch: () => { throw new Error("capture setup is outside this harness"); },
    },
  };
  await handlers.get("session_shutdown")({}, shutdownContext);
  assert.equal(closeCount, 1);
  await session.close();
});

test("unverified session history is never reinjected or acknowledged", async (t) => {
  const message = envelope("message-2");
  const session = await durableSession(message.id, t);
  session.entries.push(sessionEntry(message.id));
  const sent = [];
  const status = [];
  const client = receiptClient();
  const context = {
    sessionManager: session.manager,
    ui: { setStatus: (...args) => status.push(args) },
  };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  await nextTurn();

  assert.equal(mailboxSessionDeliveryState(session.manager, message.id), "history_unverified");
  assert.deepEqual(sent, []);
  assert.deepEqual(client.receipts, []);
  assert.deepEqual(status, [["mecha-mailbox", "mailbox: waiting for durable session checkpoint"]]);
  coordinator.close();
  await session.close();
});

test("unreadable persisted history is reported without reinjection or ACK", async (t) => {
  const message = envelope("message-corrupt-history");
  const session = await durableSession(message.id, t);
  session.entries.push(sessionEntry(message.id));
  await writeFile(session.sessionFile, "not-json\n");
  const sent = [];
  const notices = [];
  const client = receiptClient();
  const context = {
    sessionManager: session.manager,
    ui: {
      setStatus() {},
      notify: (...args) => notices.push(args),
    },
  };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.deepEqual(sent, []);
  assert.deepEqual(client.receipts, []);
  assert.equal(notices.length, 1);
  assert.match(notices[0][0], /session checkpoint unreadable/);
  assert.equal(notices[0][1], "error");
  coordinator.close();
  await session.close();
});

test("replayed durable delivery ACKs without a second Pi message", async (t) => {
  const message = envelope("message-3");
  const session = await durableSession(message.id, t);
  await session.persist();
  const sent = [];
  const client = receiptClient();
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.deepEqual(sent, []);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "ack", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);
  coordinator.close();
  await session.close();
});

test("post-injection receipt failure retains the pending message for replay", async (t) => {
  const message = envelope("message-receipt-failure");
  const session = await durableSession(message.id, t);
  const sent = [];
  const receipts = [];
  let failReceipt = true;
  const client = {
    sendReceipt(type, delivery) {
      if (type === "injection_requested" && failReceipt) {
        failReceipt = false;
        throw new Error("socket closed");
      }
      receipts.push({ type, attemptNumber: delivery.attemptNumber });
    },
  };
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.equal(sent.length, 1);
  assert.deepEqual(receipts, [{ type: "injection_requested", attemptNumber: 2 }]);
  coordinator.close();
  await session.close();
});

test("stale pending replay reinjects when no session entry survived", async (t) => {
  const message = envelope("message-lost-across-compaction");
  const session = await durableSession(message.id, t);
  const sent = [];
  const client = receiptClient();
  let time = 1_000;
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
    pendingReinjectionDelayMs: 5_000,
    now: () => time,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });
  assert.equal(sent.length, 1);

  time += 5_000;
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 3 });

  assert.equal(sent.length, 2);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 3, reason: undefined },
  ]);
  coordinator.close();
  await session.close();
});

test("mailbox session delivery state fails closed across the durable-state matrix", () => {
  const entry = sessionEntry("state-matrix");
  const makeManager = ({ branch = [entry], persisted = true, sessionFile = "/tmp/session.jsonl" } = {}) => ({
    getBranch: () => branch,
    isPersisted: () => persisted,
    getSessionFile: () => sessionFile,
    getSessionId: () => "session-1",
  });
  const cases = [
    { name: "absent", manager: makeManager({ branch: [] }), expected: "absent" },
    { name: "unpersisted history", manager: makeManager({ persisted: false }), expected: "history_unverified" },
    { name: "missing session path", manager: makeManager({ sessionFile: undefined }), expected: "history_unverified" },
    {
      name: "persisted file without matching entry",
      manager: makeManager(),
      readSessionFile: () => `${JSON.stringify(sessionEntry("other-message"))}\n`,
      expected: "history_unverified",
    },
    {
      name: "missing persisted file",
      manager: makeManager(),
      readSessionFile: () => { throw Object.assign(new Error("session file missing"), { code: "ENOENT" }); },
      expected: "history_unverified",
    },
    {
      name: "unreadable persisted file",
      manager: makeManager(),
      readSessionFile: () => { throw new Error("checkpoint unreadable"); },
      expected: "history_unverified",
      persistenceError: "checkpoint unreadable",
    },
    {
      name: "durable matching entry",
      manager: makeManager(),
      readSessionFile: () => `${JSON.stringify(entry)}\n`,
      expected: "durable",
    },
  ];

  for (const stateCase of cases) {
    const persistenceErrors = [];
    const state = mailboxSessionDeliveryState(stateCase.manager, "state-matrix", {
      readSessionFile: stateCase.readSessionFile ?? (() => ""),
      onPersistenceError: (error) => persistenceErrors.push(error.message),
    });
    assert.equal(state, stateCase.expected, stateCase.name);
    assert.deepEqual(persistenceErrors, stateCase.persistenceError === undefined ? [] : [stateCase.persistenceError]);
  }
});

test("session disappearance before durable confirmation stays unacknowledged", () => {
  const message = envelope("message-durable-session-disappears");
  const entry = sessionEntry(message.id);
  let reads = 0;
  const manager = {
    getBranch: () => [entry],
    isPersisted: () => true,
    getSessionFile: () => "/tmp/session.jsonl",
    getSessionId: () => "session-1",
  };
  const receipts = [];
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: manager, ui: { setStatus() {} } },
    client: { sendReceipt: (type, delivery) => receipts.push({ type, delivery }) },
    readSessionFile: () => {
      reads += 1;
      if (reads === 1) return `${JSON.stringify(entry)}\n`;
      throw Object.assign(new Error("session file disappeared"), { code: "ENOENT" });
    },
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.equal(reads, 2);
  assert.deepEqual(receipts, []);
  coordinator.close();
});

test("session replacement before durable confirmation leaves the pending delivery unacknowledged", () => {
  const message = envelope("message-session-replaced");
  const entries = [];
  let currentSessionId = "session-1";
  const manager = {
    getBranch: () => entries,
    isPersisted: () => true,
    getSessionFile: () => "/tmp/session.jsonl",
    getSessionId: () => currentSessionId,
  };
  const deferred = [];
  const timers = [];
  const receipts = [];
  const context = { sessionManager: manager, ui: { setStatus() {} } };
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: context,
    client: { sendReceipt: (type, delivery) => receipts.push({ type, delivery }) },
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback, delay) => { const timer = { callback, delay }; timers.push(timer); return timer; },
    clearTimeoutImpl() {},
    readSessionFile: () => "",
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  entries.push(sessionEntry(message.id));
  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  currentSessionId = "session-2";
  deferred[0]();

  assert.deepEqual(receipts, [{ type: "injection_requested", delivery: { messageId: message.id, attemptNumber: 1 } }]);
  assert.deepEqual(timers, []);
  coordinator.close();
});

test("duplicate message_end callbacks schedule one checkpoint retry", () => {
  const message = envelope("message-duplicate-checkpoint");
  const entries = [];
  const manager = {
    getBranch: () => entries,
    isPersisted: () => true,
    getSessionFile: () => "/tmp/session.jsonl",
    getSessionId: () => "session-1",
  };
  const deferred = [];
  const timers = [];
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: manager, ui: { setStatus() {} } },
    client: receiptClient(),
    defer: (callback) => deferred.push(callback),
    setTimeoutImpl: (callback, delay) => { const timer = { callback, delay, cleared: false }; timers.push(timer); return timer; },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
    readSessionFile: () => "",
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  entries.push(sessionEntry(message.id));
  const event = { message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } } };
  coordinator.onMessageEnd(event, { sessionManager: manager });
  coordinator.onMessageEnd(event, { sessionManager: manager });
  deferred.forEach((callback) => callback());

  assert.equal(timers.length, 1);
  assert.equal(timers[0].delay, 250);
  coordinator.close();
  assert.equal(timers[0].cleared, true);
});

test("failed replay injection keeps the pending delivery retryable", () => {
  const message = envelope("message-failed-replay-injection");
  const sent = [];
  let injectionCalls = 0;
  let time = 1_000;
  const manager = {
    getBranch: () => [],
    isPersisted: () => true,
    getSessionFile: () => "/tmp/session.jsonl",
    getSessionId: () => "session-1",
  };
  const client = receiptClient();
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: {
      sendMessage: (...args) => {
        injectionCalls += 1;
        if (injectionCalls === 2) throw new Error("Pi rejected replay");
        sent.push(args);
      },
    },
    sessionContext: { sessionManager: manager, ui: { setStatus() {} } },
    client,
    pendingReinjectionDelayMs: 5_000,
    now: () => time,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  time = 6_000;
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 3 });

  assert.equal(injectionCalls, 3);
  assert.equal(sent.length, 2);
  assert.deepEqual(client.receipts.map(({ type, attemptNumber }) => ({ type, attemptNumber })), [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "injection_requested", attemptNumber: 2 },
    { type: "injection_requested", attemptNumber: 3 },
  ]);
  coordinator.close();
});

test("failed replay injection receipt keeps the pending delivery retryable", () => {
  const message = envelope("message-failed-replay-receipt");
  const sent = [];
  const receipts = [];
  let time = 1_000;
  const manager = {
    getBranch: () => [],
    isPersisted: () => true,
    getSessionFile: () => "/tmp/session.jsonl",
    getSessionId: () => "session-1",
  };
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: manager, ui: { setStatus() {} } },
    client: {
      sendReceipt(type, delivery) {
        if (type === "injection_requested" && delivery.attemptNumber === 2) throw new Error("socket closed");
        receipts.push({ type, attemptNumber: delivery.attemptNumber });
      },
    },
    pendingReinjectionDelayMs: 5_000,
    now: () => time,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  time = 6_000;
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });
  time = 11_000;
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 3 });

  assert.equal(sent.length, 3);
  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "injection_requested", attemptNumber: 3 },
  ]);
  coordinator.close();
});

test("message arrival failure does not suppress the injection receipt", () => {
  const message = envelope("message-arrival-failure", { source: { kind: "agent" } });
  const receipts = [];
  const manager = {
    getBranch: () => [],
    isPersisted: () => true,
    getSessionFile: () => "/tmp/session.jsonl",
    getSessionId: () => "session-1",
  };
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: manager, ui: { setStatus() {} } },
    client: {
      sendReceipt: (type, delivery) => receipts.push({ type, attemptNumber: delivery.attemptNumber }),
      sendMessageArrival() { throw new Error("arrival socket closed"); },
    },
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.deepEqual(receipts, [{ type: "injection_requested", attemptNumber: 1 }]);
  coordinator.close();
});

test("closed coordinator ignores later envelopes", () => {
  const sent = [];
  const receipts = [];
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: { getSessionId: () => "session-1" } },
    client: { sendReceipt: (...args) => receipts.push(args) },
  });
  coordinator.close();

  assert.equal(coordinator.onEnvelope(envelope("message-after-close"), { messageId: "message-after-close", attemptNumber: 1 }), undefined);
  assert.deepEqual(sent, []);
  assert.deepEqual(receipts, []);
});

test("normal injection NACK transport failure is contained", () => {
  const coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi: { sendMessage() { throw new Error("Pi rejected message"); } },
    sessionContext: {
      sessionManager: {
        getBranch: () => [],
        isPersisted: () => true,
        getSessionFile: () => "/tmp/session.jsonl",
        getSessionId: () => "session-1",
      },
    },
    client: { sendReceipt() { throw new Error("socket closed"); } },
  });

  assert.doesNotThrow(() => coordinator.onEnvelope(envelope("message-nack-transport"), {
    messageId: "message-nack-transport", attemptNumber: 1,
  }));
  coordinator.close();
});

test("lifecycle client negotiates capability, dual-sends legacy activity, and removes only correlated post-commit acknowledgments", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  const client = createMailboxClient({
    agentId: "worker",
    parentSessionId: "parent-session",
    producerEpoch: "11111111-1111-4111-8111-111111111111",
    onEnvelope: () => {},
    webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: true, json: async () => ({ subagent_lifecycle_v2: true }) }),
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
  });

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
  }) }));
  await connected;
  await nextTurn();
  assert.equal(socket.sent.at(-1).type, "subagent_sync_enable");
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "subagent_state_snapshot", router_version: 7, agents: [] }) }));
  await nextTurn();
  const initialSnapshot = socket.sent.find((frame) => frame.type === "subagent_lifecycle_snapshot");
  assert.ok(initialSnapshot, "enablement must begin lifecycle production with a complete snapshot");
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "subagent_lifecycle_acknowledged", request_id: initialSnapshot.frame_id, event_id: initialSnapshot.event_id,
    client_sequence: initialSnapshot.client_sequence, producer_epoch: initialSnapshot.producer_epoch,
    agent_id: "worker", instance_id: client.instanceId, parent_session_id: "parent-session", lease_id: "lease-1",
    router_version: 7, disposition: "accepted_noop",
  }) }));
  await nextTurn();

  client.sendActivity("active", { subagentCount: 1, subagents: [{ id: "child-1", description: "Legacy visible label" }] });
  const eventId = client.sendLifecycleEvent({
    lifecycleEvent: "started",
    subagent: { id: "child-1", type: "general-purpose", description: "Soak sleeper 1 2m", status: "running" },
    snapshot: [{ id: "child-1", type: "general-purpose", description: "Soak sleeper 1 2m", status: "running" }],
  });
  const lifecycle = socket.sent.find((frame) => frame.event_id === eventId);
  assert.deepEqual(lifecycle.subagent, { id: "child-1", type: "general-purpose", description: "Soak sleeper 1 2m", status: "running" });
  assert.deepEqual({
    type: lifecycle.type,
    agent: lifecycle.agent_id,
    instance: lifecycle.instance_id,
    parentSession: lifecycle.parent_session_id,
    lease: lifecycle.lease_id,
    epoch: lifecycle.producer_epoch,
    sequence: lifecycle.client_sequence,
  }, {
    type: "subagent_lifecycle_event", agent: "worker", instance: client.instanceId,
    parentSession: "parent-session", lease: "lease-1", epoch: "11111111-1111-4111-8111-111111111111", sequence: 2,
  });
  assert.equal(socket.sent.some((frame) => frame.type === "activity"), true, "legacy activity remains enabled during rollout");
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "subagent_lifecycle_acknowledged", request_id: "other-request", router_version: 8,
  }) }));
  await nextTurn();
  assert.equal(client.lifecycleStatus().pending, 1);
  // A request-id collision or an ack observed before the matching committed event must not
  // advance the replay cursor or drop a retry entry.
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "subagent_lifecycle_acknowledged", request_id: lifecycle.frame_id, event_id: "wrong-event",
    client_sequence: lifecycle.client_sequence, producer_epoch: lifecycle.producer_epoch,
    agent_id: "worker", instance_id: client.instanceId, parent_session_id: "parent-session", lease_id: "lease-1",
    router_version: 99, disposition: "applied",
  }) }));
  await nextTurn();
  const uncorrelatedAck = client.lifecycleStatus();
  assert.equal(uncorrelatedAck.supported, true);
  assert.equal(uncorrelatedAck.ready, true);
  assert.equal(uncorrelatedAck.routerVersion, 7);
  assert.equal(uncorrelatedAck.pending, 1);
  assert.ok(uncorrelatedAck.bytes > 0);
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "subagent_lifecycle_acknowledged", request_id: lifecycle.frame_id, event_id: lifecycle.event_id,
    client_sequence: lifecycle.client_sequence, producer_epoch: lifecycle.producer_epoch,
    agent_id: "worker", instance_id: client.instanceId, parent_session_id: "parent-session", lease_id: "lease-1",
    router_version: 9, disposition: "applied",
  }) }));
  await nextTurn();
  assert.deepEqual(client.lifecycleStatus(), { supported: true, ready: true, routerVersion: 9, pending: 0, bytes: 0 });
  client.close();
});

test("lifecycle retry repairs a replacement lease with stable event identity", async () => {
  const sockets = [];
  const reconnectTimers = [];
  const client = createMailboxClient({
    agentId: "worker",
    parentSessionId: "parent-session",
    producerEpoch: "22222222-2222-4222-8222-222222222222",
    onEnvelope: () => {},
    webSocketFactory: () => {
      const socket = new EventTarget();
      socket.readyState = 0;
      socket.sent = [];
      socket.send = (frame) => socket.sent.push(JSON.parse(frame));
      socket.close = () => { socket.readyState = 3; socket.dispatchEvent(new Event("close")); };
      sockets.push(socket);
      return socket;
    },
    fetchImpl: async () => ({ ok: true, json: async () => ({ subagent_lifecycle_v2: true }) }),
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
    reconnectInitialDelayMs: 2,
    reconnectMaxDelayMs: 2,
    random: () => 1,
    setTimeoutImpl: (callback) => { const timer = { callback, cleared: false }; reconnectTimers.push(timer); return timer; },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });
  const connectAndEnable = async (socket, lease) => {
    socket.readyState = 1;
    socket.dispatchEvent(new Event("open"));
    socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: lease }) }));
    await nextTurn();
    socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "subagent_state_snapshot", router_version: 1, agents: [] }) }));
    await nextTurn();
  };

  const connected = client.connect();
  await connectAndEnable(sockets[0], "lease-1");
  await connected;
  const initialSnapshot = sockets[0].sent.find((frame) => frame.type === "subagent_lifecycle_snapshot");
  sockets[0].dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "subagent_lifecycle_acknowledged", request_id: initialSnapshot.frame_id, event_id: initialSnapshot.event_id,
    client_sequence: initialSnapshot.client_sequence, producer_epoch: initialSnapshot.producer_epoch,
    agent_id: "worker", instance_id: client.instanceId, parent_session_id: "parent-session", lease_id: "lease-1", router_version: 1,
  }) }));
  await nextTurn();
  const eventId = client.sendLifecycleEvent({ lifecycleEvent: "started", subagent: { id: "child-1", description: "Native subagent" }, snapshot: [{ id: "child-1", description: "Native subagent" }] });
  const original = sockets[0].sent.find((frame) => frame.event_id === eventId);
  sockets[0].close();
  reconnectTimers[0].callback();
  await connectAndEnable(sockets[1], "lease-2");

  const repair = sockets[1].sent.find((frame) => frame.type === "subagent_lifecycle_snapshot");
  const retried = sockets[1].sent.find((frame) => frame.event_id === eventId);
  assert.ok(repair, "a replacement lease must receive a complete repair snapshot");
  assert.deepEqual(repair.subagents, [{ id: "child-1", description: "Native subagent" }]);
  assert.ok(sockets[1].sent.indexOf(repair) < sockets[1].sent.indexOf(retried), "the repair snapshot must precede older retries");
  assert.equal(retried.lease_id, "lease-2");
  assert.equal(retried.client_sequence, original.client_sequence);
  assert.notEqual(retried.frame_id, original.frame_id);
  sockets[1].dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "subagent_lifecycle_acknowledged", request_id: retried.frame_id, event_id: retried.event_id,
    client_sequence: retried.client_sequence, producer_epoch: retried.producer_epoch,
    agent_id: "worker", instance_id: client.instanceId, parent_session_id: "parent-session", lease_id: "lease-2",
    router_version: 2, disposition: "stale",
  }) }));
  await nextTurn();
  assert.equal(client.lifecycleStatus().pending, 1, "the repair snapshot remains unacknowledged");
  sockets[1].dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "subagent_lifecycle_acknowledged", request_id: repair.frame_id, event_id: repair.event_id,
    client_sequence: repair.client_sequence, producer_epoch: repair.producer_epoch,
    agent_id: "worker", instance_id: client.instanceId, parent_session_id: "parent-session", lease_id: "lease-2",
    router_version: 3, disposition: "applied",
  }) }));
  await nextTurn();
  assert.equal(client.lifecycleStatus().pending, 0);
  client.close();
});

test("lifecycle journal collapses at its item and age limits and fails closed for an oversized snapshot", async () => {
  let now = 0;
  const diagnostics = [];
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: true, json: async () => ({ subagent_lifecycle_v2: true }) }),
    now: () => now,
    onLifecycleDiagnostic: (event) => diagnostics.push(event.reason),
  });
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1" }) }));
  await connected;
  await nextTurn();
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({ type: "subagent_state_snapshot", router_version: 1, agents: [] }) }));
  await nextTurn();
  const initialSnapshot = socket.sent.find((frame) => frame.type === "subagent_lifecycle_snapshot");
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "subagent_lifecycle_acknowledged", request_id: initialSnapshot.frame_id, event_id: initialSnapshot.event_id,
    client_sequence: initialSnapshot.client_sequence, producer_epoch: initialSnapshot.producer_epoch,
    agent_id: "worker", instance_id: client.instanceId, parent_session_id: client.parentSessionId, lease_id: "lease-1", router_version: 1,
  }) }));
  await nextTurn();

  for (let index = 0; index < 256; index += 1) {
    client.sendLifecycleEvent({ lifecycleEvent: "terminal", subagent: { id: `child-${index}`, description: "Native subagent" }, snapshot: [] });
  }
  assert.equal(client.lifecycleStatus().pending, 256);
  client.sendLifecycleEvent({ lifecycleEvent: "terminal", subagent: { id: "collapse-child", description: "Native subagent" }, snapshot: [] });
  assert.equal(client.lifecycleStatus().pending, 1);
  assert.ok(diagnostics.includes("journal_collapsed"));
  const collapsedSnapshot = socket.sent.filter((frame) => frame.type === "subagent_lifecycle_snapshot").at(-1);
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "subagent_lifecycle_acknowledged", request_id: collapsedSnapshot.frame_id, event_id: collapsedSnapshot.event_id,
    client_sequence: collapsedSnapshot.client_sequence, producer_epoch: collapsedSnapshot.producer_epoch,
    agent_id: "worker", instance_id: client.instanceId, parent_session_id: client.parentSessionId, lease_id: "lease-1", router_version: 2,
  }) }));
  await nextTurn();

  // UTF-8 accounting, not JavaScript string length, is what enforces the total journal cap.
  let byteCollapseAt;
  for (let index = 0; index < 256; index += 1) {
    const pendingBefore = client.lifecycleStatus().pending;
    client.sendLifecycleEvent({ lifecycleEvent: "terminal", subagent: { id: `utf8-${index}`, description: "€".repeat(512) }, snapshot: [] });
    if (index > 0 && pendingBefore > 1 && client.lifecycleStatus().pending === 1) {
      byteCollapseAt = index;
      break;
    }
  }
  assert.ok(byteCollapseAt < 255, "serialized UTF-8 bytes must collapse before the item cap");
  assert.ok(client.lifecycleStatus().bytes <= 262_144);

  now += 10 * 60_000;
  client.sendLifecycleEvent({ lifecycleEvent: "started", subagent: { id: "expired-child", description: "Native subagent" }, snapshot: [] });
  assert.equal(client.lifecycleStatus().pending, 1);
  assert.ok(diagnostics.includes("journal_expired"));

  const framesBeforeOversize = socket.sent.length;
  client.sendLifecycleSnapshot(Array.from({ length: 999 }, (_, index) => ({ id: `large-${index}`, description: "x".repeat(512) })));
  assert.equal(socket.sent.length, framesBeforeOversize, "an oversized snapshot must not be sent or truncated");
  assert.deepEqual(client.lifecycleStatus(), { supported: true, ready: false, routerVersion: 2, pending: 0, bytes: 0 });
  assert.ok(diagnostics.includes("journal_oversize"));
  client.close();
});

test("lifecycle capability fallback remains legacy-only", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  const client = createMailboxClient({
    agentId: "worker", onEnvelope: () => {}, webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: false, json: async () => ({}) }),
  });
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", { data: JSON.stringify({
    type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
  }) }));
  await connected;
  await nextTurn();

  client.sendActivity("active", { subagentCount: 1, subagents: [{ id: "child-1", description: "Legacy label" }] });
  assert.equal(client.sendLifecycleSnapshot([{ id: "child-1", description: "Native subagent" }]), undefined);
  assert.equal(client.sendLifecycleEvent({ lifecycleEvent: "started", subagent: { id: "child-1", description: "Native subagent" }, snapshot: [{ id: "child-1", description: "Native subagent" }] }), undefined);
  assert.deepEqual(socket.sent.map((frame) => frame.type), ["register", "activity"]);
  assert.deepEqual(client.lifecycleStatus(), { supported: false, ready: false, routerVersion: 0, pending: 0, bytes: 0 });
  client.close();
});

test("client sends activity frames with its registered lease identity", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    webSocketFactory: () => socket,
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
  });

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;
  client.sendActivity("active", {
    subagentCount: 2,
    subagents: [{ id: "queued-child", type: "general-purpose", description: "Queued work", status: "queued" }],
  });
  client.sendActivity("settled");

  assert.deepEqual(socket.sent.map(({ type, agent_id, instance_id, lease_id, activity_state, subagent_count, subagents }) => ({ type, agent_id, instance_id, lease_id, activity_state, subagent_count, subagents })), [
    { type: "register", agent_id: "worker", instance_id: client.instanceId, lease_id: undefined, activity_state: undefined, subagent_count: undefined, subagents: undefined },
    { type: "activity", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1", activity_state: "active", subagent_count: 2, subagents: [{ id: "queued-child", type: "general-purpose", description: "Queued work", status: "queued" }] },
    { type: "activity", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1", activity_state: "settled", subagent_count: 0, subagents: [] },
  ]);
  client.close();
});

test("client serializes full message arrival body separately from capped preview", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    webSocketFactory: () => socket,
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
  });

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;

  const body = `first line\n${"long body ".repeat(40)}`;
  client.sendMessageArrival({
    id: "arrival-message",
    from: "supervisor",
    to: "worker",
    subject: "Full body",
    source: { kind: "agent" },
    body,
    created_at: "2030-01-01T00:00:00.000Z",
  });

  const frame = socket.sent.find(({ type }) => type === "message_arrival");
  assert.equal(frame.body, body);
  assert.equal(frame.preview.length, 140);
  assert.notEqual(frame.preview, body);
  assert.match(frame.preview, /…$/);
  client.close();
});

test("client message arrival preview does not split surrogate pairs at the cap", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    webSocketFactory: () => socket,
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
  });

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;

  const body = `${"a".repeat(138)}😀 trailing body`;
  client.sendMessageArrival({
    id: "arrival-emoji-boundary",
    from: "supervisor",
    to: "worker",
    subject: "Emoji boundary",
    source: { kind: "agent" },
    body,
    created_at: "2030-01-01T00:00:00.000Z",
  });

  const frame = socket.sent.find(({ type, message_id }) => type === "message_arrival" && message_id === "arrival-emoji-boundary");
  assert.equal(frame.body, body);
  assert.equal(frame.preview, `${"a".repeat(138)}…`);
  assert.ok(frame.preview.length <= 140);
  assert.doesNotThrow(() => validateClientFrame(frame));
  client.close();
});

test("batched Pi delivery waits for durable checkpoint with every item before ACK", async (t) => {
  const message = envelope("batch-durable-1", {
    body: "Mailbox message batch\n\nFrom: sender\n\nfirst\n\n[Reply via mailbox to sender.]\n\n---\n\nFrom: sender\n\nsecond\n\n[Reply via mailbox to sender.]",
  });
  const session = await durableSession(message.id, t);
  const sent = [];
  const client = receiptClient();
  let deferredCheckpoint;
  const context = {
    sessionManager: session.manager,
    ui: { setStatus() {} },
  };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (customMessage, options) => sent.push({ customMessage, options }) },
    sessionContext: context,
    client,
    defer: (callback) => { deferredCheckpoint = callback; },
  });
  const batchDelivery = {
    messageId: "batch-durable-1",
    attemptNumber: 1,
    items: [
      {
        messageId: "batch-durable-1",
        messageIncarnationId: "11111111-1111-4111-8111-111111111111",
        attemptNumber: 1,
        deliveryExpiresAt: "2026-07-13T00:00:30.000Z",
      },
      {
        messageId: "batch-durable-2",
        messageIncarnationId: "22222222-2222-4222-8222-222222222222",
        attemptNumber: 1,
        deliveryExpiresAt: "2026-07-13T00:00:30.000Z",
      },
    ],
  };

  coordinator.onEnvelope(message, batchDelivery);
  assert.equal(sent.length, 1);
  assert.deepEqual(sent[0].customMessage.details.delivery_items, batchDelivery.items);
  assert.deepEqual(client.receipts, [{
    type: "injection_requested",
    messageId: "batch-durable-1",
    attemptNumber: 1,
    items: batchDelivery.items,
    reason: undefined,
  }]);

  coordinator.onMessageEnd({
    message: {
      role: "custom",
      customType: "mecha-agent-message",
      details: { message_id: message.id, delivery_items: batchDelivery.items },
    },
  }, context);
  session.entries.push({
    type: "custom_message",
    customType: "mecha-agent-message",
    content: "message",
    display: true,
    details: { message_id: message.id, delivery_items: batchDelivery.items },
  });
  await writeFile(session.sessionFile, `${session.entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
  deferredCheckpoint();

  assert.deepEqual(client.receipts.at(-1), {
    type: "ack",
    messageId: "batch-durable-1",
    attemptNumber: 1,
    items: batchDelivery.items,
    reason: undefined,
  });
  coordinator.close();
  await session.close();
});

test("Pi durable replay requires matching delivery item metadata", async (t) => {
  const message = envelope("batch-durable-mismatch");
  const session = await durableSession(message.id, t);
  const sent = [];
  const client = receiptClient();
  const delivery = {
    messageId: message.id,
    attemptNumber: 2,
    items: [
      {
        messageId: message.id,
        messageIncarnationId: "11111111-1111-4111-8111-111111111111",
        attemptNumber: 2,
        deliveryExpiresAt: "2026-07-13T00:00:30.000Z",
      },
      {
        messageId: "batch-durable-mismatch-2",
        messageIncarnationId: "22222222-2222-4222-8222-222222222222",
        attemptNumber: 1,
        deliveryExpiresAt: "2026-07-13T00:00:30.000Z",
      },
    ],
  };
  const wrongItems = [delivery.items[0], { ...delivery.items[1], deliveryExpiresAt: "2026-07-13T00:00:31.000Z" }];
  session.entries.push({
    type: "custom_message",
    customType: "mecha-agent-message",
    content: "message",
    display: true,
    details: { message_id: message.id, delivery_items: wrongItems },
  });
  await writeFile(session.sessionFile, `${session.entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: { sessionManager: session.manager, ui: { setStatus() {} } },
    client,
  });

  coordinator.onEnvelope(message, delivery);

  assert.equal(sent.length, 0);
  assert.deepEqual(client.receipts, []);
  coordinator.close();
  await session.close();
});

test("client exposes one delivery item for single-message deliveries", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  let receivedDelivery;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: async (_message, delivery) => {
      receivedDelivery = delivery;
      client.sendReceipt("ack", delivery);
    },
    webSocketFactory: () => socket,
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
  });

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "message",
      message: envelope("client-single-1"),
      message_incarnation_id: "11111111-1111-4111-8111-111111111111",
      attempt_number: 3,
      delivery_expires_at: "2026-07-13T00:00:30.000Z",
    }),
  }));
  await nextTurn();

  assert.deepEqual(receivedDelivery.items, [{
    messageId: "client-single-1",
    messageIncarnationId: "11111111-1111-4111-8111-111111111111",
    attemptNumber: 3,
    deliveryExpiresAt: "2026-07-13T00:00:30.000Z",
  }]);
  assert.equal(socket.sent.find(({ type }) => type === "ack").delivery_items, undefined);
  client.close();
});

test("client sends delivery_items on batch receipts", async (t) => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  let receivedDelivery;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: async (_message, delivery) => {
      receivedDelivery = delivery;
      client.sendReceipt("injection_requested", delivery);
      client.sendReceipt("ack", delivery);
    },
    webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: false, status: 404 }),
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
  });
  t.after(() => client.close());

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "message",
      message: envelope("client-batch-1"),
      message_incarnation_id: "11111111-1111-4111-8111-111111111111",
      attempt_number: 2,
      delivery_expires_at: "2026-07-13T00:00:30.000Z",
      delivery_items: [
        {
          message_id: "client-batch-1",
          message_incarnation_id: "11111111-1111-4111-8111-111111111111",
          attempt_number: 2,
          delivery_expires_at: "2026-07-13T00:00:30.000Z",
        },
        {
          message_id: "client-batch-2",
          message_incarnation_id: "22222222-2222-4222-8222-222222222222",
          attempt_number: 1,
          delivery_expires_at: "2026-07-13T00:00:30.000Z",
        },
      ],
    }),
  }));
  await nextTurn();

  assert.deepEqual(receivedDelivery.items.map(({ messageId, attemptNumber }) => ({ messageId, attemptNumber })), [
    { messageId: "client-batch-1", attemptNumber: 2 },
    { messageId: "client-batch-2", attemptNumber: 1 },
  ]);
  assert.deepEqual(socket.sent.filter(({ type }) => type !== "register").map(({ type, message_id: messageId, attempt_number: attemptNumber, delivery_items: items }) => ({
    type,
    messageId,
    attemptNumber,
    items: items.map(({ message_id: id, attempt_number: attempt }) => ({ id, attempt })),
  })), [
    {
      type: "injection_requested",
      messageId: "client-batch-1",
      attemptNumber: 2,
      items: [{ id: "client-batch-1", attempt: 2 }, { id: "client-batch-2", attempt: 1 }],
    },
    {
      type: "ack",
      messageId: "client-batch-1",
      attemptNumber: 2,
      items: [{ id: "client-batch-1", attempt: 2 }, { id: "client-batch-2", attempt: 1 }],
    },
  ]);
});

test("client does not NACK a Pi injection when its receipt send fails", async (t) => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => {
    const parsed = JSON.parse(frame);
    if (parsed.type === "injection_requested") throw new Error("socket write failed");
    socket.sent.push(parsed);
  };
  socket.close = () => { socket.readyState = 3; };
  const context = {
    sessionManager: {
      getBranch: () => [],
      isPersisted: () => false,
      getSessionFile: () => undefined,
      getSessionId: () => "session-1",
    },
    ui: { setStatus() {} },
  };
  const sent = [];
  let coordinator;
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: (message, delivery) => coordinator.onEnvelope(message, delivery),
    webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: false, status: 404 }),
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
  });
  t.after(() => client.close());
  coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });
  t.after(() => coordinator.close());

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({ type: "message", message: envelope("message-client-receipt"), attempt_number: 1 }),
  }));
  await nextTurn();

  assert.equal(sent.length, 1);
  assert.deepEqual(socket.sent.map(({ type }) => type), ["register"]);
});

test("Pi injection failure NACKs without acknowledging the delivery", async (t) => {
  const message = envelope("message-injection-failure");
  const session = await durableSession(message.id, t);
  const client = receiptClient();
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: () => { throw new Error("Pi rejected message"); } },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.deepEqual(client.receipts, [
    { type: "nack", messageId: message.id, attemptNumber: 1, reason: "injection_failed" },
  ]);
  coordinator.close();
  await session.close();
});

test("failed durable ACK is retried from persisted session history without reinjection", async (t) => {
  const message = envelope("message-ack-failure");
  const session = await durableSession(message.id, t);
  await session.persist();
  const sent = [];
  const receipts = [];
  let failAck = true;
  const client = {
    sendReceipt(type, delivery) {
      if (type === "ack" && failAck) {
        failAck = false;
        throw new Error("socket closed");
      }
      receipts.push({ type, attemptNumber: delivery.attemptNumber });
    },
  };
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.deepEqual(sent, []);
  assert.deepEqual(receipts, [
    { type: "injection_requested", attemptNumber: 1 },
    { type: "injection_requested", attemptNumber: 2 },
    { type: "ack", attemptNumber: 2 },
  ]);
  coordinator.close();
  await session.close();
});

test("message arrival event is emitted when a received message is injected", async (t) => {
  const message = envelope("arrival-message", { source: { kind: "agent" }, subject: "Hello", body: "hello ".repeat(40) });
  const session = await durableSession(message.id, t);
  const sent = [];
  const arrivals = [];
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client: { ...receiptClient(), sendMessageArrival: (arrival) => arrivals.push(arrival) },
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.equal(sent.length, 1);
  assert.equal(arrivals.length, 1);
  assert.equal(arrivals[0].id, message.id);
  assert.equal(arrivals[0].from, "sender");
  assert.equal(arrivals[0].subject, "Hello");
  assert.equal(arrivals[0].body, message.body);
  coordinator.close();
  await session.close();
});

test("message arrival suppresses self-sender deliveries", async (t) => {
  const message = envelope("arrival-self-sender", { from: "worker", to: "worker", source: { kind: "agent" } });
  const session = await durableSession(message.id, t);
  const arrivals = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: session.manager, ui: { setStatus() {} } },
    client: { ...receiptClient(), sendMessageArrival: (arrival) => arrivals.push(arrival) },
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.deepEqual(arrivals, []);
  coordinator.close();
  await session.close();
});

test("message arrival suppresses duplicate delivery for the same message id", async (t) => {
  const message = envelope("arrival-duplicate", { source: { kind: "agent" } });
  const session = await durableSession(message.id, t);
  const arrivals = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: session.manager, ui: { setStatus() {} } },
    client: { ...receiptClient(), sendMessageArrival: (arrival) => arrivals.push(arrival) },
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

  assert.equal(arrivals.length, 1);
  coordinator.close();
  await session.close();
});

test("message arrival dedupe survives retry replay after reconnect within the session", async (t) => {
  const message = envelope("arrival-reconnect-replay", { source: { kind: "agent" } });
  const session = await durableSession(message.id, t);
  const arrivals = [];
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage() {} },
    sessionContext: { sessionManager: session.manager, ui: { setStatus() {} } },
    client: { ...receiptClient(), sendMessageArrival: (arrival) => arrivals.push(arrival) },
    now: () => 1_000,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  // The router replays an unacknowledged durable delivery with a later attempt after socket reconnect.
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });

  assert.equal(arrivals.length, 1);
  coordinator.close();
  await session.close();
});

for (const kind of ["web_ui", "android", "system", "unknown"]) {
  test(`message arrival suppresses ${kind} source`, async (t) => {
    const message = envelope(`arrival-source-${kind.replace("_", "-")}`, { source: { kind } });
    const session = await durableSession(message.id, t);
    const arrivals = [];
    const coordinator = createPiMailboxDeliveryCoordinator({
      pi: { sendMessage() {} },
      sessionContext: { sessionManager: session.manager, ui: { setStatus() {} } },
      client: { ...receiptClient(), sendMessageArrival: (arrival) => arrivals.push(arrival) },
    });

    coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });

    assert.deepEqual(arrivals, []);
    coordinator.close();
    await session.close();
  });
}

test("replayed in-flight delivery does not create a second Pi injection", async (t) => {
  const message = envelope("message-4");
  const session = await durableSession(message.id, t);
  const sent = [];
  const client = receiptClient();
  const context = { sessionManager: session.manager, ui: { setStatus() {} } };
  const coordinator = createPiMailboxDeliveryCoordinator({
    pi: { sendMessage: (...args) => sent.push(args) },
    sessionContext: context,
    client,
  });

  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 1 });
  coordinator.onEnvelope(message, { messageId: message.id, attemptNumber: 2 });
  await session.persist();
  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: message.id } },
  }, context);
  await nextTurn();

  assert.equal(sent.length, 1);
  assert.deepEqual(client.receipts, [
    { type: "injection_requested", messageId: message.id, attemptNumber: 1, reason: undefined },
    { type: "injection_requested", messageId: message.id, attemptNumber: 2, reason: undefined },
    { type: "ack", messageId: message.id, attemptNumber: 2, reason: undefined },
  ]);
  coordinator.close();
  await session.close();
});

test("isolated Mailbox V2 E2E covers durable checkpoints, router restart replay, and Sentinel handoff", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-integration-"));
  const databasePath = join(directory, "router.sqlite");
  const sessionFile = join(directory, "session.jsonl");
  const entries = [];
  const sockets = [];
  const events = [];
  const injections = [];
  const statusMessages = [];
  const sentinelCalls = [];
  const checkpointTimers = [];
  let repository;
  let router;
  let client;
  let coordinator;
  let heldFirstAck;
  let holdFirstAck = true;
  const manager = {
    getBranch: () => entries,
    isPersisted: () => true,
    getSessionFile: () => sessionFile,
    getSessionId: () => "integration-session",
  };
  const context = { sessionManager: manager, ui: { setStatus() {} } };
  const persistPiEntry = (message) => {
    entries.push({ type: "custom_message", ...message });
    writeFileSync(sessionFile, `${entries.map((entry) => JSON.stringify(entry)).join("\n")}\n`);
  };
  const openRouter = (port = 0) => {
    repository = openMessageRepository({ databasePath });
    router = createAgentRouter({
      repository,
      port,
      deliveryLeaseDurationMs: 5_000,
      maxDeliveryAttempts: 1,
      deliveryOfferCoalesceMs: 0,
      timeoutSweepIntervalMs: 60_000,
      eventLogger: (event, fields) => events.push({ event, ...fields }),
    });
    return router.listen();
  };

  t.after(async () => {
    try { coordinator?.close(); } catch {}
    try { client?.close(); } catch {}
    try { await router?.close(); } catch {}
    try { repository?.close(); } catch {}
    await rm(directory, { recursive: true, force: true }).catch(() => {});
  });

  const websocketUrl = await openRouter();
  const routerUrl = websocketUrl.replace(/^ws:/, "http:").replace(/\/v1\/agents$/, "");
  const routerPort = Number(new URL(websocketUrl).port);
  const replayMessage = envelope("integration-replay", { to: "worker", body: "persist this once" });
  const checkpointMessage = envelope("integration-checkpoint-failure", { to: "worker", body: "retry this checkpoint" });
  const sentinelSuccess = envelope("integration-sentinel-success", {
    from: "supervisor", to: "worker", body: "run the successful scheduled task", sentinel: { model: "gpt-5.6-terra" },
  });
  const sentinelFailure = envelope("integration-sentinel-failure", {
    from: "supervisor", to: "worker", body: "run the failed scheduled task", sentinel: { model: "gpt-5.6-terra" },
  });
  const pi = {
    sendMessage(messageToInject, options) {
      if (messageToInject.customType === "mecha-agent-message") injections.push(messageToInject);
      else statusMessages.push({ message: messageToInject, options });
    },
  };
  client = createMailboxClient({
    agentId: "worker",
    routerUrl,
    onEnvelope: (envelopeMessage, delivery) => coordinator.onEnvelope(envelopeMessage, delivery),
    webSocketFactory: (url) => {
      const socket = new WebSocket(url);
      const send = socket.send.bind(socket);
      socket.send = (frame) => {
        const parsed = JSON.parse(frame.toString());
        if (parsed.type === "ack" && holdFirstAck) {
          holdFirstAck = false;
          heldFirstAck = parsed;
          return;
        }
        send(frame);
      };
      sockets.push(socket);
      return socket;
    },
    renewIntervalMs: 60_000,
    reconnectInitialDelayMs: 60_000,
    reconnectMaxDelayMs: 60_000,
    fetchImpl: fetch,
  });
  coordinator = createDirectPiMailboxDeliveryCoordinator({
    pi,
    sessionContext: context,
    client,
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      checkpointTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
    spawnSubagent: async (message) => {
      sentinelCalls.push(message);
      if (message.id === sentinelFailure.id) throw new Error("native handoff failed");
      return "native-success-child";
    },
  });

  await client.connect();
  router.enqueueMessage(replayMessage);
  await waitFor(() => injections.some(({ details }) => details?.message_id === replayMessage.id), 100);
  await waitFor(() => repository.messageFor(replayMessage.id)?.state === "injection_requested", 100);

  const replayInjection = injections.find(({ details }) => details?.message_id === replayMessage.id);
  persistPiEntry(replayInjection);
  assert.equal(mailboxSessionDeliveryState(manager, replayMessage.id), "durable");
  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: replayMessage.id } },
  }, context);
  await nextTurn();

  assert.equal(heldFirstAck.type, "ack");
  assert.equal(repository.messageFor(replayMessage.id).state, "injection_requested");
  assert.equal(events.filter(({ event, message_id: messageId }) => event === "delivery_acknowledged" && messageId === replayMessage.id).length, 0);

  const firstSocketClosed = once(sockets[0], "close");
  client.pause();
  await firstSocketClosed;
  const previousRouter = router;
  router = undefined;
  await previousRouter.close();
  const previousRepository = repository;
  repository = undefined;
  previousRepository.close();
  await openRouter(routerPort);
  await client.resume();

  await waitFor(() => sockets.length === 2, 100);
  await waitFor(() => repository.messageFor(replayMessage.id) === undefined, 100);
  assert.equal(injections.filter(({ details }) => details?.message_id === replayMessage.id).length, 1);
  assert.deepEqual(events.filter(({ event, message_id: messageId }) => [
    "delivery_offered",
    "delivery_injection_requested",
    "delivery_acknowledged",
  ].includes(event) && messageId === replayMessage.id).map(({ event, attempt }) => ({ event, attempt })), [
    { event: "delivery_offered", attempt: 1 },
    { event: "delivery_injection_requested", attempt: 1 },
    { event: "delivery_offered", attempt: 1 },
    { event: "delivery_injection_requested", attempt: 1 },
    { event: "delivery_acknowledged", attempt: 1 },
  ]);

  router.enqueueMessage(checkpointMessage);
  await waitFor(() => injections.some(({ details }) => details?.message_id === checkpointMessage.id), 100);
  await waitFor(() => repository.messageFor(checkpointMessage.id)?.state === "injection_requested", 100);
  coordinator.onMessageEnd({
    message: { role: "custom", customType: "mecha-agent-message", details: { message_id: checkpointMessage.id } },
  }, context);
  await nextTurn();
  assert.deepEqual(checkpointTimers.map(({ delay, cleared }) => ({ delay, cleared })), [{ delay: 250, cleared: false }]);
  assert.equal(repository.messageFor(checkpointMessage.id).state, "injection_requested");

  const checkpointInjection = injections.find(({ details }) => details?.message_id === checkpointMessage.id);
  persistPiEntry(checkpointInjection);
  assert.equal(mailboxSessionDeliveryState(manager, checkpointMessage.id), "durable");
  checkpointTimers[0].callback();
  await waitFor(() => repository.messageFor(checkpointMessage.id) === undefined, 100);
  assert.equal(injections.filter(({ details }) => details?.message_id === checkpointMessage.id).length, 1);

  router.enqueueMessage(sentinelSuccess);
  await waitFor(() => sentinelCalls.some(({ id }) => id === sentinelSuccess.id), 100);
  await waitFor(() => repository.messageFor(sentinelSuccess.id) === undefined, 100);
  assert.equal(statusMessages.length, 1);
  assert.equal(statusMessages[0].message.display, false);
  assert.equal(injections.some(({ details }) => details?.message_id === sentinelSuccess.id), false);

  router.enqueueMessage(sentinelFailure);
  await waitFor(() => events.some(({ event, message_id: messageId, state }) => event === "delivery_nacked" && messageId === sentinelFailure.id && state === "dead_letter"), 100);
  assert.equal(sentinelCalls.filter(({ id }) => id === sentinelFailure.id).length, 1);
  assert.equal(statusMessages.length, 1);
  assert.equal(repository.messageFor(sentinelFailure.id).state, "dead_letter");
});

test("mailbox client pause closes without reconnecting until resume", async () => {
  const sockets = [];
  const reconnectTimers = [];
  class FakeSocket extends EventTarget {
    readyState = 0;
    sent = [];

    send(frame) {
      this.sent.push(JSON.parse(frame));
    }

    close() {
      this.readyState = 3;
      this.dispatchEvent(new Event("close"));
    }
  }
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    webSocketFactory: () => {
      const socket = new FakeSocket();
      sockets.push(socket);
      return socket;
    },
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
    reconnectInitialDelayMs: 2,
    reconnectMaxDelayMs: 4,
    random: () => 1,
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      reconnectTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  const connected = client.connect();
  sockets[0].readyState = 1;
  sockets[0].dispatchEvent(new Event("open"));
  sockets[0].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;

  client.pause();
  assert.equal(sockets.length, 1);
  assert.deepEqual(reconnectTimers, []);
  await client.resume();
  assert.equal(sockets.length, 2);
  client.close();
});

test("extension pauses mailbox delivery across session compaction", async (t) => {
  isolateCompactionEnvironment(t);
  const handlers = new Map();
  const clientCalls = [];
  const client = {
    connect: () => { clientCalls.push("connect"); return Promise.resolve(); },
    close: () => { clientCalls.push("close"); },
    pause: () => { clientCalls.push("pause"); },
    resume: () => { clientCalls.push("resume"); return Promise.resolve(); },
    sendReply: async () => ({}),
    sendReceipt() {},
  };
  const pi = {
    events: { on() {} },
    on(type, handler) {
      const list = handlers.get(type) ?? [];
      list.push(handler);
      handlers.set(type, list);
    },
    registerCommand() {},
    registerTool() {},
    sendMessage() {},
    exec: async () => ({ code: 0, stderr: "" }),
  };
  const context = {
    sessionManager: { getSessionId: () => "parent-session", getBranch: () => [], isPersisted: () => false },
    ui: { setStatus: (...args) => clientCalls.push(["status", ...args]), notify: (...args) => clientCalls.push(["notify", ...args]) },
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "worker",
    createClient: () => client,
  });
  for (const handler of handlers.get("session_start")) handler({}, context);

  for (const handler of handlers.get("session_before_compact")) {
    await handler({ signal: AbortSignal.timeout(1), preparation: { messagesToSummarize: [], turnPrefixMessages: [] } }, context);
  }
  assert.ok(clientCalls.some((call) => Array.isArray(call) && call[2] === "mailbox: compacting"));
  assert.equal(clientCalls.includes("pause"), false);
  assert.equal(clientCalls.includes("close"), false);

  for (const handler of handlers.get("session_compact")) handler({}, context);
  assert.equal(clientCalls.includes("resume"), false);
  assert.ok(clientCalls.some((call) => Array.isArray(call) && call[2] === "mailbox: connected"));
});

test("mailbox client reconnects after close and stops retrying after shutdown", async () => {
  const sockets = [];
  const reconnectTimers = [];
  const kickstarts = [];
  class FakeSocket extends EventTarget {
    readyState = 0;
    sent = [];

    send(frame) {
      this.sent.push(JSON.parse(frame));
    }

    close() {
      this.readyState = 3;
      this.dispatchEvent(new Event("close"));
    }
  }
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    kickstart: () => { kickstarts.push("kickstart"); },
    webSocketFactory: () => {
      const socket = new FakeSocket();
      sockets.push(socket);
      return socket;
    },
    generateId: (() => { let index = 0; return () => `id-${++index}`; })(),
    reconnectInitialDelayMs: 2,
    reconnectMaxDelayMs: 4,
    random: () => 1,
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      reconnectTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  const connected = client.connect();
  sockets[0].readyState = 1;
  sockets[0].dispatchEvent(new Event("open"));
  sockets[0].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-1",
    }),
  }));
  await connected;

  sockets[0].close();
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
  ]);
  assert.deepEqual(kickstarts, []);
  reconnectTimers[0].callback();
  assert.equal(sockets.length, 2);
  sockets[1].readyState = 1;
  sockets[1].dispatchEvent(new Event("open"));
  sockets[1].dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "worker", instance_id: client.instanceId, lease_id: "lease-2",
    }),
  }));
  sockets[1].close();
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
    { delay: 2, cleared: false },
  ]);
  assert.deepEqual(kickstarts, []);
  client.close();
  assert.equal(reconnectTimers[1].cleared, true);
  reconnectTimers[1].callback();
  assert.equal(sockets.length, 2);
});

test("connection refusal requests launchd once per scheduled retry", () => {
  const sockets = [];
  const createSocket = () => {
    const socket = new EventTarget();
    socket.readyState = 0;
    socket.close = () => { socket.readyState = 3; };
    sockets.push(socket);
    return socket;
  };
  const kickstarts = [];
  const reconnectTimers = [];
  const client = createMailboxClient({
    agentId: "worker",
    onEnvelope: () => {},
    kickstart: () => { kickstarts.push("kickstart"); },
    webSocketFactory: createSocket,
    reconnectInitialDelayMs: 2,
    reconnectMaxDelayMs: 4,
    random: () => 1,
    setTimeoutImpl: (callback, delay) => {
      const timer = { callback, delay, cleared: false };
      reconnectTimers.push(timer);
      return timer;
    },
    clearTimeoutImpl: (timer) => { timer.cleared = true; },
  });

  void client.connect().catch(() => {});
  const refusal = new Event("error");
  Object.defineProperty(refusal, "error", {
    value: Object.assign(new Error("connection refused"), { code: "ECONNREFUSED" }),
  });
  sockets[0].dispatchEvent(refusal);

  assert.deepEqual(kickstarts, ["kickstart"]);
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
  ]);
  reconnectTimers[0].callback();
  assert.equal(sockets.length, 2);
  const secondRefusal = new Event("error");
  Object.defineProperty(secondRefusal, "error", {
    value: Object.assign(new Error("connection refused"), { code: "ECONNREFUSED" }),
  });
  sockets[1].dispatchEvent(secondRefusal);
  assert.deepEqual(kickstarts, ["kickstart", "kickstart"]);
  assert.deepEqual(reconnectTimers.map(({ delay, cleared }) => ({ delay, cleared })), [
    { delay: 2, cleared: false },
    { delay: 4, cleared: false },
  ]);
  client.close();
  assert.equal(reconnectTimers[1].cleared, true);
});

test("same instance reconnect registration replaces the old live socket without duplicate_identity", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-reconnect-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let firstSocket;
  let secondSocket;
  let frameId = 0;
  const send = (socket, frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `client-${++frameId}`,
    ...frame,
  }));
  try {
    const websocketUrl = await router.listen();
    firstSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      firstSocket.once("open", resolve);
      firstSocket.once("error", reject);
    });
    const firstRegistered = nextRouterFrame(firstSocket);
    send(firstSocket, { type: "register", agent_id: "worker", instance_id: "same-instance" });
    const firstRegistration = await firstRegistered;
    assert.equal(firstRegistration.type, "registered");

    const firstClosed = new Promise((resolve) => firstSocket.once("close", resolve));
    secondSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      secondSocket.once("open", resolve);
      secondSocket.once("error", reject);
    });
    const secondRegistered = nextRouterFrame(secondSocket);
    send(secondSocket, { type: "register", agent_id: "worker", instance_id: "same-instance" });
    const secondRegistration = await secondRegistered;

    assert.equal(secondRegistration.type, "registered");
    assert.notEqual(secondRegistration.lease_id, firstRegistration.lease_id);
    const activeLease = repository.leaseFor("worker");
    assert.equal(activeLease.agent_id, "worker");
    assert.equal(activeLease.instance_id, "same-instance");
    assert.equal(activeLease.lease_id, secondRegistration.lease_id);
    assert.equal(activeLease.expires_at, secondRegistration.expires_at);
    await firstClosed;
  } finally {
    firstSocket?.terminate();
    secondSocket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("unchanged live duplicate messages are suppressed until settlement", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-live-dedupe-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const base = envelope("dedupe-1", {
    from: "supervisor",
    to: "busy-worker",
    subject: "Still parked",
    body: "Unchanged reminder while the worker is busy.",
  });
  const identity = {
    agentId: "busy-worker",
    instanceId: "busy-instance",
    leaseId: "busy-lease",
  };
  try {
    const first = repository.enqueue(base);
    assert.equal(first.created, true);
    const queuedDuplicate = repository.enqueue({ ...base, id: "dedupe-queued" });
    assert.equal(queuedDuplicate.created, false);
    assert.equal(queuedDuplicate.deduped, true);
    assert.equal(queuedDuplicate.message.id, "dedupe-1");
    assert.deepEqual(repository.queuedFor("busy-worker").map(({ id }) => id), ["dedupe-1"]);

    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    const delivery = repository.claimNextMessage(identity);
    assert.equal(delivery.message.id, "dedupe-1");
    const leasedDuplicate = repository.enqueue({ ...base, id: "dedupe-leased" });
    assert.equal(leasedDuplicate.deduped, true);
    assert.equal(leasedDuplicate.message.state, "leased");

    repository.recordInjectionRequested({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
    });
    const injectionDuplicate = repository.enqueue({ ...base, id: "dedupe-injection" });
    assert.equal(injectionDuplicate.deduped, true);
    assert.equal(injectionDuplicate.message.state, "injection_requested");

    repository.acknowledgeDelivery({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
    });
    const afterAck = repository.enqueue({ ...base, id: "dedupe-after-ack" });
    assert.equal(afterAck.created, true);
    assert.equal(afterAck.message.id, "dedupe-after-ack");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("changed duplicate evidence still enqueues separately", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-dedupe-evidence-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const base = envelope("evidence-1", {
    from: "supervisor",
    to: "busy-worker",
    subject: "Status",
    body: "Still waiting.",
  });
  try {
    repository.enqueue(base, { disableBatching: true });
    assert.equal(repository.enqueue({ ...base, id: "evidence-body", body: "New blocker found." }, { disableBatching: true }).created, true);
    assert.equal(repository.enqueue({ ...base, id: "evidence-subject", subject: "Blocked" }, { disableBatching: true }).created, true);
    assert.deepEqual(repository.queuedFor("busy-worker").map(({ id }) => id), [
      "evidence-1",
      "evidence-body",
      "evidence-subject",
    ]);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("sentinel metadata participates in live duplicate matching", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-sentinel-dedupe-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const base = envelope("sentinel-dedupe-1", {
    from: "supervisor",
    to: "busy-worker",
    body: "Launch this native task.",
    sentinel: { model: "gpt-5.5" },
  });
  try {
    assert.equal(repository.enqueue(base).created, true);
    const duplicate = repository.enqueue({ ...base, id: "sentinel-dedupe-2" });
    assert.equal(duplicate.deduped, true);
    assert.equal(duplicate.message.id, "sentinel-dedupe-1");

    const changedSentinel = repository.enqueue({
      ...base,
      id: "sentinel-dedupe-3",
      sentinel: { model: "gpt-5.6-terra" },
    });
    assert.equal(changedSentinel.created, true);
    assert.deepEqual(repository.queuedFor("busy-worker").map(({ id }) => id), [
      "sentinel-dedupe-1",
      "sentinel-dedupe-3",
    ]);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("deduped conversation sends record their own conversation message", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-conversation-dedupe-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const base = envelope("conversation-dedupe-1", {
    from: "shared-user",
    to: "peer",
    body: "Same visible text.",
  });
  try {
    assert.equal(repository.enqueue(base, { conversation: { body: "Same visible text.", attachments: [] } }).created, true);
    const duplicate = repository.enqueue({ ...base, id: "conversation-dedupe-2" }, {
      conversation: { body: "Same visible text.", attachments: [] },
    });
    assert.equal(duplicate.deduped, true);
    assert.equal(duplicate.conversation_created, true);
    assert.equal(duplicate.conversation_message.id, "conversation-dedupe-2");
    assert.deepEqual(repository.queuedFor("peer").map(({ id }) => id), ["conversation-dedupe-1"]);
    assert.deepEqual(repository.conversationHistory({ owner: "shared-user", peer: "peer" }).map(({ id }) => id), [
      "conversation-dedupe-1",
      "conversation-dedupe-2",
    ]);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("duplicate recurring reminders link to one live mailbox message", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-recurring-dedupe-"));
  let now = Date.parse("2026-07-13T00:00:00.000Z");
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date(now),
  });
  const identity = {
    agentId: "busy-worker",
    instanceId: "busy-instance",
    leaseId: "busy-lease",
  };
  try {
    const router = createAgentRouter({ repository, clock: () => new Date(now), timeoutSweepIntervalMs: 60_000 });
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    for (const id of ["recurring-a", "recurring-b"]) {
      repository.createReminder({
        id,
        target: identity.agentId,
        creator: "tester",
        subject: "Busy worker reminder",
        body: "Same recurring reminder text.",
        created_at: new Date(now).toISOString(),
        next_due_at: new Date(now).toISOString(),
        interval_ms: 15 * 60 * 1000,
      });
    }

    const fired = router.sweepDueReminders();
    assert.equal(fired.length, 2);
    assert.deepEqual(repository.queuedFor(identity.agentId).map(({ id }) => id), ["reminder-recurring-a-0"]);
    assert.equal(repository.reminderFor("recurring-a").linked_message_id, "reminder-recurring-a-0");
    assert.equal(repository.reminderFor("recurring-b").linked_message_id, "reminder-recurring-a-0");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router emits deduped enqueue events for suppressed messages", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-dedupe-events-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const events = [];
  const router = createAgentRouter({ repository, eventLogger: (event, fields) => events.push({ event, ...fields }) });
  const base = envelope("event-dedupe-1", {
    from: "supervisor",
    to: "busy-worker",
    body: "Same body.",
  });
  try {
    router.enqueueMessage(base);
    router.enqueueMessage({ ...base, id: "event-dedupe-2" });
    assert.deepEqual(events.filter(({ event }) => event === "message_enqueued"), [
      {
        event: "message_enqueued",
        message_id: "event-dedupe-1",
        to: "busy-worker",
        agent_id: "busy-worker",
        state: "queued",
        created: true,
      },
      {
        event: "message_enqueued",
        message_id: "event-dedupe-1",
        to: "busy-worker",
        agent_id: "busy-worker",
        state: "queued",
        created: false,
        deduped: true,
      },
    ]);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("duplicate one-shot reminders link to one live mailbox message", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-reminder-dedupe-"));
  let now = Date.parse("2026-07-13T00:00:00.000Z");
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date(now),
  });
  try {
    const router = createAgentRouter({ repository, clock: () => new Date(now), timeoutSweepIntervalMs: 60_000 });
    for (const id of ["duplicate-a", "duplicate-b"]) {
      repository.createReminder({
        id,
        target: "busy-worker",
        creator: "tester",
        subject: "Busy worker reminder",
        body: "Same reminder text.",
        created_at: new Date(now).toISOString(),
        next_due_at: new Date(now + 1_000).toISOString(),
        interval_ms: null,
      });
    }

    now += 1_000;
    const fired = router.sweepDueReminders();
    assert.equal(fired.length, 2);
    assert.deepEqual(repository.queuedFor("busy-worker").map(({ id }) => id), ["reminder-duplicate-a-0"]);
    assert.equal(repository.reminderFor("duplicate-a").linked_message_id, "reminder-duplicate-a-0");
    assert.equal(repository.reminderFor("duplicate-b").linked_message_id, "reminder-duplicate-a-0");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router batches roughly ten mixed-sender idle notifications into one delivery pull", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  try {
    const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    for (let index = 1; index <= 10; index += 1) {
      const sender = `worker-${index}`;
      const result = router.enqueueMessage(envelope(`idle-${index}`, {
        from: sender,
        to: "supervisor",
        subject: index % 2 === 0 ? "Pi worker settled" : "Status update",
        body: `Worker ${sender} has settled and is idle.`,
      }));
      assert.equal(result.message.id, `idle-${index}`);
      assert.equal(result.message.to, "supervisor");
      assert.equal(result.batched, undefined);
    }

    assert.equal(repository.queuedFor("supervisor").length, 10);
    repository.acquireAgentLease({ agentId: "supervisor", instanceId: "supervisor-instance", leaseId: "supervisor-lease", leaseDurationMs: 60_000 });
    const delivery = repository.claimNextMessage({ agentId: "supervisor", instanceId: "supervisor-instance", leaseId: "supervisor-lease" });
    assert.equal(delivery.message.id, "idle-1");
    assert.equal(delivery.items.length, 10);
    assert.match(delivery.message.body, /^Mailbox message batch\n\n/);
    for (let index = 1; index <= 10; index += 1) {
      assert.match(delivery.message.body, new RegExp(`From: worker-${index}`));
      assert.match(delivery.message.body, new RegExp(`Worker worker-${index} has settled and is idle\\.`));
      assert.match(delivery.message.body, new RegExp(`\\[Reply via mailbox to worker-${index}\\.\\]`));
    }
    assert.match(delivery.message.body, /Subject: Status update/);
    assert.match(delivery.message.body, /Subject: Pi worker settled/);
    assert.equal((delivery.message.body.match(/\n\n---\n\n/g) ?? []).length, 9);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("due reminders batch into one delivery pull", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-reminder-batch-"));
  let now = Date.parse("2026-07-13T00:00:00.000Z");
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date(now),
  });
  try {
    const router = createAgentRouter({ repository, clock: () => new Date(now), timeoutSweepIntervalMs: 60_000 });
    for (const [id, body] of [["reminder-a", "first due reminder"], ["reminder-b", "second due reminder"]]) {
      repository.createReminder({
        id,
        target: "supervisor",
        creator: "tester",
        subject: "General supervisor sweep",
        body,
        created_at: new Date(now).toISOString(),
        next_due_at: new Date(now + 1_000).toISOString(),
        interval_ms: null,
      });
    }

    now += 1_000;
    const fired = router.sweepDueReminders();
    assert.equal(fired.length, 2);
    assert.equal(fired[0].message.id, "reminder-reminder-a-0");
    assert.equal(fired[1].message.id, "reminder-reminder-b-0");

    const queued = repository.queuedFor("supervisor");
    assert.equal(queued.length, 2);
    repository.acquireAgentLease({ agentId: "supervisor", instanceId: "supervisor-instance", leaseId: "supervisor-lease", leaseDurationMs: 60_000 });
    const delivery = repository.claimNextMessage({ agentId: "supervisor", instanceId: "supervisor-instance", leaseId: "supervisor-lease" });
    assert.equal(delivery.message.id, "reminder-reminder-a-0");
    assert.match(delivery.message.body, /^Mailbox message batch\n\n/);
    assert.match(delivery.message.body, /From: reminders/);
    assert.match(delivery.message.body, /Subject: General supervisor sweep/);
    assert.match(delivery.message.body, /first due reminder/);
    assert.match(delivery.message.body, /second due reminder/);
    assert.equal((delivery.message.body.match(/\n\n---\n\n/g) ?? []).length, 1);
    assert.equal(repository.reminderFor("reminder-a").linked_message_id, "reminder-reminder-a-0");
    assert.equal(repository.reminderFor("reminder-b").linked_message_id, "reminder-reminder-b-0");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("batched delivery carries per-item reply hints without one global sender footer", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-delivery-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  let senderSocket;
  let receiverSocket;
  let frameId = 0;
  const send = (socket, frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `client-${++frameId}`,
    ...frame,
  }));
  try {
    const websocketUrl = await router.listen();
    senderSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      senderSocket.once("open", resolve);
      senderSocket.once("error", reject);
    });
    const senderRegistered = nextRouterFrame(senderSocket);
    send(senderSocket, { type: "register", agent_id: "worker-1", instance_id: "sender-instance" });
    assert.equal((await senderRegistered).type, "registered");

    router.enqueueMessage(envelope("batch-delivery-1", {
      from: "worker-1",
      to: "supervisor",
      subject: "Pi worker settled",
      body: "Worker worker-1 has settled and is idle.",
    }));
    router.enqueueMessage(envelope("batch-delivery-2", {
      from: "worker-2",
      to: "supervisor",
      subject: "Status update",
      body: "Worker worker-2 has settled and is idle.",
    }));

    receiverSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      receiverSocket.once("open", resolve);
      receiverSocket.once("error", reject);
    });
    const receiverFrames = [];
    receiverSocket.on("message", (payload) => receiverFrames.push(JSON.parse(payload.toString("utf8"))));
    send(receiverSocket, { type: "register", agent_id: "supervisor", instance_id: "receiver-instance", delivery_receipts: "delivery_items_v1" });
    await waitFor(() => receiverFrames.some((frame) => frame.type === "message"));
    assert.equal(receiverFrames[0].type, "registered");
    const delivery = receiverFrames.find((frame) => frame.type === "message");

    assert.equal(delivery.type, "message");
    assert.match(delivery.message.body, /^Mailbox message batch\n\n/);
    assert.equal((delivery.message.body.match(/\[Reply via mailbox to worker-1\.\]/g) ?? []).length, 1);
    assert.equal((delivery.message.body.match(/\[Reply via mailbox to worker-2\.\]/g) ?? []).length, 1);
    assert.doesNotMatch(delivery.message.body, /\[Reply via mailbox to worker-1\.\]$/);
  } finally {
    senderSocket?.terminate();
    receiverSocket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("legacy registration accepts exact delivery items for a multi-item batch", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-legacy-reconnect-"));
  let now = Date.parse("2026-07-13T00:00:00.000Z");
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock: () => new Date(now) });
  const router = createAgentRouter({
    repository,
    clock: () => new Date(now),
    deliveryLeaseDurationMs: 1_000,
    timeoutSweepIntervalMs: 60_000,
  });
  let capableSocket;
  let legacySocket;
  let frameId = 0;
  const send = (socket, frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `legacy-reconnect-${++frameId}`,
    ...frame,
  }));
  try {
    const websocketUrl = await router.listen();
    router.enqueueMessage(envelope("legacy-reconnect-1", { to: "worker", body: "first" }));
    router.enqueueMessage(envelope("legacy-reconnect-2", { to: "worker", body: "second" }));

    capableSocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      capableSocket.once("open", resolve);
      capableSocket.once("error", reject);
    });
    const capableFrames = [];
    capableSocket.on("message", (payload) => capableFrames.push(JSON.parse(payload.toString("utf8"))));
    send(capableSocket, {
      type: "register",
      agent_id: "worker",
      instance_id: "worker-instance",
      ack_confirmation: "correlated_v1",
      delivery_receipts: "delivery_items_v1",
    });
    await waitFor(() => capableFrames.some((frame) => frame.type === "message"));
    const activeBatch = capableFrames.find((frame) => frame.type === "message");
    assert.equal(activeBatch.delivery_items.length, 2);
    capableSocket.terminate();

    legacySocket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      legacySocket.once("open", resolve);
      legacySocket.once("error", reject);
    });
    const legacyFrames = [];
    legacySocket.on("message", (payload) => legacyFrames.push(JSON.parse(payload.toString("utf8"))));
    send(legacySocket, {
      type: "register",
      agent_id: "worker",
      instance_id: "worker-instance",
      delivery_receipts: "delivery_items_v1",
    });
    await waitFor(() => legacyFrames.some((frame) => frame.type === "registered"));
    const legacyRegistered = legacyFrames.find((frame) => frame.type === "registered");
    assert.equal(legacyRegistered.ack_confirmation, undefined);
    await waitFor(() => legacyFrames.some((frame) => frame.type === "message"));
    const replay = legacyFrames.find((frame) => frame.type === "message");
    assert.equal(replay.message.id, "legacy-reconnect-1");
    assert.equal(replay.message_incarnation_id, undefined);
    assert.equal(replay.delivery_items.length, 2);
    assert.deepEqual(replay.delivery_items.map(({ message_id }) => message_id), [
      "legacy-reconnect-1",
      "legacy-reconnect-2",
    ]);
    assert.match(replay.message.body, /second/);
    const legacyLeaseId = legacyFrames.find((frame) => frame.type === "registered").lease_id;
    const deliveryItems = replay.delivery_items;
    send(legacySocket, {
      type: "injection_requested",
      agent_id: "worker",
      instance_id: "worker-instance",
      lease_id: legacyLeaseId,
      message_id: replay.message.id,
      attempt_number: replay.attempt_number,
      delivery_items: deliveryItems,
    });
    send(legacySocket, {
      type: "ack",
      agent_id: "worker",
      instance_id: "worker-instance",
      lease_id: legacyLeaseId,
      message_id: replay.message.id,
      attempt_number: replay.attempt_number,
      delivery_items: deliveryItems,
    });
    await new Promise((resolve) => setTimeout(resolve, 25));
    assert.equal(repository.messageFor("legacy-reconnect-1"), undefined);
    assert.equal(repository.messageFor("legacy-reconnect-2"), undefined);
    assert.equal(legacyFrames.filter((frame) => frame.type === "message").length, 1);
    assert.equal(legacyFrames.some((frame) => frame.type === "error"), false);
  } finally {
    capableSocket?.terminate();
    legacySocket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("legacy null-batch active replay does not merge unrelated rows", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-null-active-replay-"));
  const databasePath = join(directory, "router.sqlite");
  const repository = openMessageRepository({ databasePath });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  let database;
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    repository.enqueue(envelope("legacy-null-active-1", { to: "supervisor", body: "first" }));
    repository.enqueue(envelope("legacy-null-active-2", { to: "supervisor", body: "second" }));
    const original = repository.claimNextMessage(identity);
    assert.deepEqual(original.items.map(({ message_id: id }) => id), ["legacy-null-active-1", "legacy-null-active-2"]);

    database = new DatabaseSync(databasePath);
    database.prepare("UPDATE delivery_attempts SET delivery_batch_id = NULL WHERE message_id IN (?, ?)")
      .run("legacy-null-active-1", "legacy-null-active-2");

    const replay = repository.claimNextMessage(identity);
    assert.deepEqual(replay.items.map(({ message_id: id }) => id), ["legacy-null-active-1"]);
    assert.equal(replay.message.body, "first");
  } finally {
    database?.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("delivery claim returns one row-preserving batch with all available messages", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-row-batch-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    for (let index = 1; index <= 10; index += 1) {
      repository.enqueue(envelope(`row-batch-${index}`, {
        from: `sender-${index}`,
        to: identity.agentId,
        body: `body ${index}`,
      }), { disableBatching: true });
    }

    const delivery = repository.claimNextMessage(identity);
    assert.equal(delivery.message.id, "row-batch-1");
    assert.equal(delivery.items.length, 10);
    assert.deepEqual(delivery.items.map(({ message_id: id }) => id), Array.from({ length: 10 }, (_, index) => `row-batch-${index + 1}`));
    assert.equal((delivery.message.body.match(/\n\n---\n\n/g) ?? []).length, 9);
    assert.ok(delivery.message.body.indexOf("body 1") < delivery.message.body.indexOf("body 10"));
    assert.deepEqual(Array.from({ length: 10 }, (_, index) => repository.messageFor(`row-batch-${index + 1}`).state), Array(10).fill("leased"));
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("batch ACK settles every item and rejects missing delivery_items", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-ack-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    repository.enqueue(envelope("ack-batch-1", { to: identity.agentId, body: "first" }), { disableBatching: true });
    repository.enqueue(envelope("ack-batch-2", { to: identity.agentId, body: "second" }), { disableBatching: true });

    const delivery = repository.claimNextMessage(identity);
    repository.recordInjectionRequested({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
      deliveryItems: delivery.items,
    });
    assert.throws(() => repository.acknowledgeDelivery({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
    }), /not active|delivery/i);

    repository.acknowledgeDelivery({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
      deliveryItems: delivery.items,
    });

    assert.equal(repository.messageFor("ack-batch-1"), undefined);
    assert.equal(repository.messageFor("ack-batch-2"), undefined);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("reconnect replay adopts the same active batch for the new lease", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-replay-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const firstIdentity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease-1",
  };
  const secondIdentity = { ...firstIdentity, leaseId: "receiver-lease-2" };
  try {
    repository.acquireAgentLease({ ...firstIdentity, leaseDurationMs: 60_000 });
    repository.enqueue(envelope("replay-batch-1", { to: firstIdentity.agentId, body: "first" }), { disableBatching: true });
    repository.enqueue(envelope("replay-batch-2", { to: firstIdentity.agentId, body: "second" }), { disableBatching: true });

    const firstDelivery = repository.claimNextMessage(firstIdentity);
    repository.recordInjectionRequested({
      ...firstIdentity,
      messageId: firstDelivery.message.id,
      attemptNumber: firstDelivery.attempt_number,
      deliveryItems: firstDelivery.items,
    });
    repository.acquireAgentLease({ ...secondIdentity, leaseDurationMs: 60_000 });
    const replay = repository.claimNextMessage(secondIdentity);

    assert.deepEqual(deliveryItems(replay), deliveryItems(firstDelivery));
    repository.acknowledgeDelivery({
      ...secondIdentity,
      messageId: replay.message.id,
      attemptNumber: replay.attempt_number,
      deliveryItems: replay.items,
    });
    assert.equal(repository.messageFor("replay-batch-1"), undefined);
    assert.equal(repository.messageFor("replay-batch-2"), undefined);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("timed-out batch retry group claims alone and preserves per-item dead-letter state", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-timeout-"));
  let now = Date.parse("2026-07-13T00:00:00.000Z");
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock: () => new Date(now) });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  let database;
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    repository.enqueue(envelope("timeout-batch-1", { to: identity.agentId, body: "retry me" }), { disableBatching: true });
    repository.enqueue(envelope("timeout-batch-2", { to: identity.agentId, body: "dead letter me" }), { disableBatching: true });
    database = new DatabaseSync(join(directory, "router.sqlite"));
    database.prepare(`
      INSERT INTO delivery_attempts (
        message_id, attempt_number, lease_id, owner_lease_id, state,
        created_at, updated_at, delivery_expires_at, failure_reason
      ) VALUES ('timeout-batch-2', 1, NULL, '', 'nacked', ?, ?, ?, 'injection_failed')
    `).run("2026-07-12T23:59:00.000Z", "2026-07-12T23:59:00.000Z", "2026-07-12T23:59:30.000Z");

    const delivery = repository.claimNextMessage({ ...identity, deliveryLeaseDurationMs: 1_000 });
    repository.recordInjectionRequested({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
      deliveryItems: delivery.items,
    });
    repository.enqueue(envelope("timeout-unrelated", { to: identity.agentId, body: "do not mix" }), { disableBatching: true });
    now += 2_000;
    repository.recoverTimedOutDeliveries({ maxDeliveryAttempts: 2 });

    assert.equal(repository.messageFor("timeout-batch-1").state, "queued");
    assert.equal(repository.messageFor("timeout-batch-2").state, "dead_letter");
    const retryDelivery = repository.claimNextMessage(identity);
    assert.deepEqual(retryDelivery.items.map(({ message_id: id }) => id), ["timeout-batch-1"]);
    assert.equal(repository.messageFor("timeout-unrelated").state, "queued");
  } finally {
    database?.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("delivery claim keeps retry groups separate from unrelated queued messages", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-claim-batch-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    repository.enqueue(envelope("claim-batch-1", {
      from: "worker-1",
      to: identity.agentId,
      subject: "First update",
      body: "first body",
    }));
    const firstDelivery = repository.claimNextMessage(identity);
    repository.enqueue(envelope("claim-batch-2", {
      from: "worker-2",
      to: identity.agentId,
      subject: "Second update",
      body: "second body",
    }));
    assert.deepEqual(repository.queuedFor(identity.agentId).map(({ id }) => id), ["claim-batch-2"]);

    repository.nackDelivery({
      ...identity,
      messageId: firstDelivery.message.id,
      attemptNumber: firstDelivery.attempt_number,
      reason: "injection_failed",
    });
    assert.deepEqual(repository.queuedFor(identity.agentId).map(({ id }) => id), ["claim-batch-1", "claim-batch-2"]);

    const retryDelivery = repository.claimNextMessage(identity);
    assert.equal(retryDelivery.message.id, "claim-batch-1");
    assert.equal(retryDelivery.attempt_number, 2);
    assert.equal(retryDelivery.message.body, "first body");
    assert.deepEqual(retryDelivery.items.map(({ message_id: id }) => id), ["claim-batch-1"]);
    assert.equal(repository.messageFor("claim-batch-2").state, "queued");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("delivery claim preserves merged prior attempts for dead-letter decisions", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-claim-attempts-"));
  const databasePath = join(directory, "router.sqlite");
  const repository = openMessageRepository({ databasePath });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  let database;
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    repository.enqueue(envelope("attempt-batch-1", {
      from: "worker-1",
      to: identity.agentId,
      body: "first body",
    }), { disableBatching: true });
    repository.enqueue(envelope("attempt-batch-2", {
      from: "worker-2",
      to: identity.agentId,
      body: "second body",
    }), { disableBatching: true });
    database = new DatabaseSync(databasePath);
    database.prepare(`
      INSERT INTO delivery_attempts (
        message_id, attempt_number, lease_id, owner_lease_id, state,
        created_at, updated_at, delivery_expires_at, failure_reason
      ) VALUES (?, 1, NULL, '', 'nacked', ?, ?, ?, 'injection_failed')
    `).run(
      "attempt-batch-2",
      "2026-07-13T00:00:00.000Z",
      "2026-07-13T00:00:00.000Z",
      "2026-07-13T00:00:30.000Z",
    );

    const delivery = repository.claimNextMessage(identity);
    assert.equal(delivery.message.id, "attempt-batch-1");
    assert.equal(delivery.attempt_number, 1);
    assert.deepEqual(delivery.items.map(({ message_id: id, attempt_number: attempt }) => ({ id, attempt })), [
      { id: "attempt-batch-1", attempt: 1 },
      { id: "attempt-batch-2", attempt: 2 },
    ]);
    assert.match(delivery.message.body, /^Mailbox message batch\n\n/);
    repository.recordInjectionRequested({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
      deliveryItems: delivery.items,
    });
    const failed = repository.nackDelivery({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
      deliveryItems: delivery.items,
      reason: "injection_failed",
      maxDeliveryAttempts: 2,
    });

    assert.equal(failed.state, "dead_letter");
    assert.equal(repository.messageFor("attempt-batch-1").state, "queued");
    assert.equal(repository.messageFor("attempt-batch-2").state, "dead_letter");
  } finally {
    database?.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("delivery claim re-arms every recurring reminder linked into a dead-lettered batch", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-claim-recurring-reminders-"));
  const databasePath = join(directory, "router.sqlite");
  let now = Date.parse("2026-07-13T00:00:00.000Z");
  const repository = openMessageRepository({
    databasePath,
    clock: () => new Date(now),
  });
  const router = createAgentRouter({ repository, clock: () => new Date(now), timeoutSweepIntervalMs: 60_000 });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  let database;
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    for (const [id, body] of [["recurring-a", "first recurring body"], ["recurring-b", "second recurring body"]]) {
      repository.createReminder({
        id,
        target: identity.agentId,
        creator: "tester",
        subject: "Recurring sweep",
        body,
        created_at: new Date(now).toISOString(),
        next_due_at: new Date(now).toISOString(),
        interval_ms: 15 * 60 * 1000,
      });
      repository.enqueue(envelope(`reminder-${id}-0`, {
        from: "reminders",
        to: identity.agentId,
        subject: "Recurring sweep",
        body,
      }), { disableBatching: true });
    }
    database = new DatabaseSync(databasePath);
    database.prepare("UPDATE reminders SET linked_message_id = ? WHERE id = ?")
      .run("reminder-recurring-a-0", "recurring-a");
    database.prepare("UPDATE reminders SET linked_message_id = ? WHERE id = ?")
      .run("reminder-recurring-b-0", "recurring-b");

    const delivery = repository.claimNextMessage(identity);
    repository.recordInjectionRequested({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
      deliveryItems: delivery.items,
    });
    const failed = repository.nackDelivery({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
      deliveryItems: delivery.items,
      reason: "injection_failed",
      maxDeliveryAttempts: 1,
    });

    assert.equal(failed.state, "dead_letter");
    assert.deepEqual(failed.recurring_reminder_rearmed.map(({ reminder_id }) => reminder_id), ["recurring-a", "recurring-b"]);
    for (const id of ["recurring-a", "recurring-b"]) {
      const reminder = repository.reminderFor(id);
      assert.equal(reminder.linked_message_id, null);
      assert.equal(reminder.attempt_count, 1);
      assert.ok(reminder.next_due_at > new Date(now).toISOString());
    }
    assert.doesNotThrow(() => router.sweepDeliveryTimeouts());
  } finally {
    database?.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("delivery claim batches three queued messages in enqueue order", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-claim-three-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    for (const [id, body] of [["three-1", "first"], ["three-2", "second"], ["three-3", "third"]]) {
      repository.enqueue(envelope(id, { to: identity.agentId, body }), { disableBatching: true });
    }

    const delivery = repository.claimNextMessage(identity);
    assert.equal(delivery.message.id, "three-1");
    assert.ok(delivery.message.body.indexOf("first") < delivery.message.body.indexOf("second"));
    assert.ok(delivery.message.body.indexOf("second") < delivery.message.body.indexOf("third"));
    assert.equal((delivery.message.body.match(/\n\n---\n\n/g) ?? []).length, 2);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("delivery claim ignores queued-byte limits after admission", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-claim-byte-limit-"));
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    maxQueuedBytes: 100,
  });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    repository.enqueue(envelope("qb-1", { from: "a", to: identity.agentId, body: "one" }), { disableBatching: true });
    repository.enqueue(envelope("qb-2", { from: "b", to: identity.agentId, body: "two" }), { disableBatching: true });

    const delivery = repository.claimNextMessage(identity);
    assert.equal(delivery.message.id, "qb-1");
    assert.match(delivery.message.body, /^Mailbox message batch\n\n/);
    assert.deepEqual(delivery.items.map(({ message_id: id }) => id), ["qb-1", "qb-2"]);
    assert.equal(repository.messageFor("qb-2").state, "leased");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("delivery claim preserves conversation delivery mapping for batched rows", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-claim-conversation-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    repository.enqueue(envelope("conversation-batch-1", {
      from: "worker-1",
      to: identity.agentId,
      body: "first conversation body",
    }), { conversation: { body: "first canonical body" }, disableBatching: true });
    repository.enqueue(envelope("conversation-batch-2", {
      from: "worker-2",
      to: identity.agentId,
      body: "second conversation body",
    }), { conversation: { body: "second canonical body" }, disableBatching: true });

    const delivery = repository.claimNextMessage(identity);
    assert.equal(delivery.message.id, "conversation-batch-1");
    assert.deepEqual(delivery.items.map(({ message_id: id }) => id), ["conversation-batch-1", "conversation-batch-2"]);
    assert.equal(repository.messageFor("conversation-batch-2").state, "leased");

    const duplicate = repository.enqueue(envelope("conversation-batch-2", {
      from: "worker-2",
      to: identity.agentId,
      body: "second conversation body",
    }), { conversation: { body: "second canonical body" } });
    assert.equal(duplicate.created, false);
    assert.equal(duplicate.message.id, "conversation-batch-2");
    assert.equal(duplicate.conversation_message.id, "conversation-batch-2");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("delivery claim does not batch across queued sentinel messages", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-claim-sentinel-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    repository.enqueue(envelope("before-sentinel", {
      to: identity.agentId,
      body: "before sentinel",
    }), { disableBatching: true });
    repository.enqueue(envelope("sentinel-message", {
      to: identity.agentId,
      body: "sentinel body",
      sentinel: { model: "gpt-5.5" },
    }), { disableBatching: true });
    repository.enqueue(envelope("after-sentinel", {
      to: identity.agentId,
      body: "after sentinel",
    }));

    const delivery = repository.claimNextMessage(identity);
    assert.equal(delivery.message.id, "before-sentinel");
    assert.equal(delivery.message.body, "before sentinel");
    assert.equal(repository.messageFor("after-sentinel").state, "queued");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("delivery claim preserves reminder links when queued reminder messages are batched", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-claim-reminder-batch-"));
  const databasePath = join(directory, "router.sqlite");
  const repository = openMessageRepository({ databasePath });
  const identity = {
    agentId: "supervisor",
    instanceId: "receiver-instance",
    leaseId: "receiver-lease",
  };
  let database;
  try {
    repository.acquireAgentLease({ ...identity, leaseDurationMs: 60_000 });
    for (const [id, body] of [["reminder-a", "first reminder body"], ["reminder-b", "second reminder body"]]) {
      repository.createReminder({
        id,
        target: identity.agentId,
        creator: "tester",
        subject: "General supervisor sweep",
        body,
        created_at: "2026-07-13T00:00:00.000Z",
        next_due_at: "2026-07-13T00:00:01.000Z",
        interval_ms: null,
      });
      repository.enqueue(envelope(`reminder-${id}-0`, {
        from: "reminders",
        to: identity.agentId,
        subject: "General supervisor sweep",
        body,
      }), { disableBatching: true });
    }
    database = new DatabaseSync(databasePath);
    database.prepare("UPDATE reminders SET state = 'message_queued', linked_message_id = ? WHERE id = ?")
      .run("reminder-reminder-a-0", "reminder-a");
    database.prepare("UPDATE reminders SET state = 'message_queued', linked_message_id = ? WHERE id = ?")
      .run("reminder-reminder-b-0", "reminder-b");

    const delivery = repository.claimNextMessage(identity);
    assert.equal(delivery.message.id, "reminder-reminder-a-0");
    assert.match(delivery.message.body, /^Mailbox message batch\n\n/);
    assert.ok(delivery.message.body.indexOf("first reminder body") < delivery.message.body.indexOf("second reminder body"));
    assert.equal(repository.reminderFor("reminder-a").linked_message_id, "reminder-reminder-a-0");
    assert.equal(repository.reminderFor("reminder-b").linked_message_id, "reminder-reminder-b-0");

    repository.recordInjectionRequested({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
      deliveryItems: delivery.items,
    });
    repository.acknowledgeDelivery({
      ...identity,
      messageId: delivery.message.id,
      attemptNumber: delivery.attempt_number,
      deliveryItems: delivery.items,
    });
    assert.equal(repository.reminderFor("reminder-a"), undefined);
    assert.equal(repository.reminderFor("reminder-b"), undefined);
  } finally {
    database?.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router honors the configured maximum batched body character count", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-batch-limit-"));
  const batched = "Mailbox message batch\n\nFrom: worker-a\nSubject: Pi worker settled\n\nfirst\n\n[Reply via mailbox to worker-a.]\n\n---\n\nFrom: worker-b\nSubject: Another subject\n\nsecond\n\n[Reply via mailbox to worker-b.]";
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    maxBatchBodyChars: batched.length - 1,
  });
  try {
    const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    router.enqueueMessage(envelope("batch-limit-1", {
      from: "worker-a",
      to: "supervisor",
      subject: "Pi worker settled",
      body: "first",
    }));
    const result = router.enqueueMessage(envelope("batch-limit-2", {
      from: "worker-b",
      to: "supervisor",
      subject: "Another subject",
      body: "second",
    }));

    assert.equal(result.batched, undefined);
    assert.deepEqual(repository.queuedFor("supervisor").map(({ id }) => id), ["batch-limit-1", "batch-limit-2"]);

    repository.acquireAgentLease({
      agentId: "supervisor",
      instanceId: "receiver-instance",
      leaseId: "receiver-lease",
      leaseDurationMs: 60_000,
    });
    const delivery = repository.claimNextMessage({
      agentId: "supervisor",
      instanceId: "receiver-instance",
      leaseId: "receiver-lease",
    });
    assert.deepEqual(delivery.items.map(({ message_id: id }) => id), ["batch-limit-1"]);
    assert.equal(repository.messageFor("batch-limit-2").state, "queued");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router rejects the sixteenth active unbatchable mailbox message by default", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-capacity-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), maxBatchBodyChars: 0 });
  try {
    const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
    for (let index = 1; index <= 15; index += 1) {
      router.enqueueMessage(envelope(`message-${index}`, {
        from: "worker-a",
        to: "supervisor",
        subject: `unique-${index}`,
        body: `message ${index}`,
      }));
    }

    assert.equal(repository.queueUsageFor("supervisor").message_count, 15);
    assert.throws(() => router.enqueueMessage(envelope("message-16", {
      from: "worker-a",
      to: "supervisor",
      subject: "unique-16",
      body: "message 16",
    })), QueueLimitError);
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router and SQLite recover timed-out delivery attempts and complete one-shot reminders", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-mailbox-router-"));
  const databasePath = join(directory, "router.sqlite");
  let now = Date.parse("2026-07-13T00:00:00.000Z");
  let repository;
  let router;
  const openRouter = () => {
    repository = openMessageRepository({
      databasePath,
      clock: () => new Date(now),
    });
    router = createAgentRouter({
      repository,
      clock: () => new Date(now),
      leaseDurationMs: 10_000,
      deliveryLeaseDurationMs: 100,
      timeoutSweepIntervalMs: 60_000,
    });
  };
  openRouter();
  let socket;
  let instanceId = "worker-instance";
  let frameId = 0;
  const send = (frame) => socket.send(JSON.stringify({
    version: "v1",
    frame_id: `client-${++frameId}`,
    ...frame,
  }));
  const receipt = (type, messageId, attemptNumber, leaseId) => send({
    type,
    agent_id: "worker",
    instance_id: instanceId,
    lease_id: leaseId,
    message_id: messageId,
    attempt_number: attemptNumber,
  });
  const drainAck = async () => {
    const pong = nextRouterFrame(socket);
    send({ type: "ping" });
    assert.equal((await pong).type, "pong");
  };
  try {
    const websocketUrl = await router.listen();
    assert.match(websocketUrl, /^ws:\/\/127\.0\.0\.1:/);
    socket = new WebSocket(websocketUrl);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });

    const registered = nextRouterFrame(socket);
    send({ type: "register", agent_id: "worker", instance_id: instanceId });
    const registration = await registered;
    assert.equal(registration.type, "registered");
    assert.match(registration.frame_id, /^[A-Za-z0-9]{21}$/);
    const leaseId = registration.lease_id;
    assert.match(leaseId, /^[A-Za-z0-9]{21}$/);
    assert.notEqual(registration.frame_id, leaseId);

    const firstOffer = nextRouterFrame(socket);
    router.enqueueMessage(envelope("delivery-1", { body: "first delivery body" }));
    const firstDelivery = await firstOffer;
    assert.deepEqual(
      { type: firstDelivery.type, messageId: firstDelivery.message.id, attempt: firstDelivery.attempt_number },
      { type: "message", messageId: "delivery-1", attempt: 1 },
    );
    receipt("injection_requested", "delivery-1", firstDelivery.attempt_number, leaseId);
    await drainAck();
    router.enqueueMessage(envelope("delivery-2", { body: "second delivery body" }));

    repository.createReminder({
      id: "reminder-1",
      target: "worker",
      creator: "tester",
      body: "reminder body",
      created_at: new Date(now).toISOString(),
      next_due_at: new Date(now + 50).toISOString(),
      interval_ms: null,
    });
    socket.terminate();
    await router.close();
    repository.close();
    openRouter();
    const restartedUrl = await router.listen();
    socket = new WebSocket(restartedUrl);
    await new Promise((resolve, reject) => {
      socket.once("open", resolve);
      socket.once("error", reject);
    });
    instanceId = "worker-instance-restarted";
    const initialOffer = nextMessageFrame(socket);
    const restartedRegistration = nextRouterFrame(socket);
    send({ type: "register", agent_id: "worker", instance_id: instanceId });
    const restartedLease = await restartedRegistration;
    assert.equal(restartedLease.type, "registered");
    const recoveredLeaseId = restartedLease.lease_id;
    // Registration offers the still-active delivery immediately. Consume that
    // legacy offer before timeout recovery, which must be attempt 2.
    await initialOffer;

    now += 101;
    const replayOffer = nextRouterFrame(socket);
    assert.deepEqual(router.sweepDeliveryTimeouts().map((outcome) => outcome.state), ["queued"]);
    const replay = await replayOffer;
    assert.deepEqual(
      { messageId: replay.message.id, attempt: replay.attempt_number },
      { messageId: "delivery-1", attempt: 2 },
    );
    assert.equal(replay.message.body, "first delivery body");
    receipt("injection_requested", "delivery-1", replay.attempt_number, recoveredLeaseId);
    await drainAck();
    const secondOffer = nextRouterFrame(socket);
    receipt("ack", "delivery-1", replay.attempt_number, recoveredLeaseId);
    const secondDelivery = await secondOffer;
    assert.equal(secondDelivery.message.id, "delivery-2");
    receipt("injection_requested", "delivery-2", secondDelivery.attempt_number, recoveredLeaseId);
    await drainAck();
    receipt("ack", "delivery-2", secondDelivery.attempt_number, recoveredLeaseId);
    await drainAck();
    assert.equal(repository.messageFor("delivery-1"), undefined);
    assert.equal(repository.messageFor("delivery-2"), undefined);

    const reminderOffer = nextRouterFrame(socket);
    const fired = router.sweepDueReminders();
    assert.equal(fired.length, 1);
    const reminderDelivery = await reminderOffer;
    assert.equal(reminderDelivery.message.id, fired[0].message.id);
    receipt("injection_requested", reminderDelivery.message.id, reminderDelivery.attempt_number, recoveredLeaseId);
    receipt("ack", reminderDelivery.message.id, reminderDelivery.attempt_number, recoveredLeaseId);
    await drainAck();
    assert.equal(repository.reminderFor("reminder-1"), undefined);
  } finally {
    socket?.terminate();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});
