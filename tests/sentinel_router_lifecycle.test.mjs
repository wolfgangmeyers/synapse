import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

function nextFrame(socket) {
  return new Promise((resolve, reject) => {
    const settle = (callback, value) => {
      socket.off("message", onMessage);
      socket.off("error", onError);
      callback(value);
    };
    const onMessage = (payload) => settle(resolve, JSON.parse(payload.toString("utf8")));
    const onError = (error) => settle(reject, error);
    socket.once("message", onMessage);
    socket.once("error", onError);
  });
}

function assertState(frame, requestId, state, sequence, childIdentity, occurrenceId = "occurrence") {
  assert.equal(Object.hasOwn(frame, "body"), false);
  assert.equal(frame.type, "sentinel_state");
  assert.equal(frame.request_id, requestId);
  assert.equal(frame.occurrence_id, occurrenceId);
  assert.equal(frame.state, state);
  assert.equal(frame.transition_sequence, sequence);
  assert.equal(frame.child_identity, childIdentity);
}

test("live WebSocket delivery carries Sentinel metadata and records lease-bound lifecycle frames", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-router-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const now = "2030-01-01T00:00:00.000Z";
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date(now),
  });
  const router = createAgentRouter({
    repository,
    clock: () => new Date(now),
    sentinelLaunchDeadlineMs: 60_000,
    timeoutSweepIntervalMs: 60_000,
  });
  let socket;
  let otherSocket;
  t.after(async () => {
    otherSocket?.terminate();
    socket?.terminate();
    await router.close();
    repository.close();
  });

  const websocketUrl = await router.listen();
  socket = new WebSocket(websocketUrl);
  await new Promise((resolve, reject) => {
    socket.once("open", resolve);
    socket.once("error", reject);
  });
  let frameId = 0;
  const send = (fields) => {
    const id = `frame-${++frameId}`;
    socket.send(JSON.stringify({ version: "v1", frame_id: id, ...fields }));
    return id;
  };
  const registered = nextFrame(socket);
  send({ type: "register", agent_id: "receiver", instance_id: "instance" });
  const lease = await registered;
  assert.equal(lease.type, "registered");

  const offered = nextFrame(socket);
  router.enqueueMessage({
    id: "sentinel-message",
    from: "supervisor",
    to: "receiver",
    body: "opaque prompt",
    created_at: now,
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
  });
  const delivery = await offered;
  assert.deepEqual(delivery.message, {
    id: "sentinel-message",
    from: "supervisor",
    to: "receiver",
    body: "opaque prompt",
    created_at: now,
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
  });

  const receipt = {
    agent_id: "receiver",
    instance_id: "instance",
    lease_id: lease.lease_id,
    message_id: delivery.message.id,
    attempt_number: delivery.attempt_number,
  };
  send({ type: "injection_requested", ...receipt });
  const identity = { ...receipt, occurrence_id: "occurrence", attempt_id: "attempt" };
  otherSocket = new WebSocket(websocketUrl);
  await new Promise((resolve, reject) => {
    otherSocket.once("open", resolve);
    otherSocket.once("error", reject);
  });
  const otherRegistered = nextFrame(otherSocket);
  otherSocket.send(JSON.stringify({
    version: "v1", frame_id: "other-register", type: "register", agent_id: "other", instance_id: "other-instance",
  }));
  assert.equal((await otherRegistered).type, "registered");
  const wrongSession = nextFrame(otherSocket);
  otherSocket.send(JSON.stringify({ version: "v1", frame_id: "other-claim", type: "sentinel_claim", ...identity }));
  assert.equal((await wrongSession).code, "lease_not_active");

  const rejected = nextFrame(socket);
  const rejectedRequest = send({ type: "sentinel_claim", ...identity, attempt_number: 2 });
  const rejection = await rejected;
  assert.equal(rejection.type, "error");
  assert.equal(rejection.request_id, rejectedRequest);
  assert.equal(rejection.code, "delivery_not_active");

  const claimed = nextFrame(socket);
  const claimRequest = send({ type: "sentinel_claim", ...identity });
  assertState(await claimed, claimRequest, "claimed", 1, undefined);

  const outOfOrderExit = nextFrame(socket);
  const outOfOrderExitRequest = send({
    type: "sentinel_exited",
    agent_id: identity.agent_id,
    instance_id: identity.instance_id,
    lease_id: identity.lease_id,
    occurrence_id: identity.occurrence_id,
    attempt_id: identity.attempt_id,
    exit_code: 0,
  });
  assertState(await outOfOrderExit, outOfOrderExitRequest, "claimed", 1, undefined);
  assert.equal(repository.messageFor("sentinel-message").body, "opaque prompt");

  const outOfOrderSpawnFailure = nextFrame(socket);
  const outOfOrderSpawnFailureRequest = send({ type: "sentinel_spawn_failed", ...identity });
  assertState(await outOfOrderSpawnFailure, outOfOrderSpawnFailureRequest, "claimed", 1, undefined);

  const prematureAck = nextFrame(socket);
  const prematureAckRequest = send({ type: "ack", ...receipt });
  const prematureAckResponse = await prematureAck;
  assert.equal(prematureAckResponse.request_id, prematureAckRequest);
  assert.equal(prematureAckResponse.code, "delivery_not_active");

  const invalidLaunching = nextFrame(socket);
  const invalidLaunchingRequest = send({ type: "sentinel_launching", ...identity, attempt_number: 2 });
  const invalidLaunchingResponse = await invalidLaunching;
  assert.equal(invalidLaunchingResponse.request_id, invalidLaunchingRequest);
  assert.equal(invalidLaunchingResponse.code, "delivery_not_active");

  const launching = nextFrame(socket);
  const launchingRequest = send({ type: "sentinel_launching", ...identity });
  assertState(await launching, launchingRequest, "launching", 2, undefined);

  const launched = nextFrame(socket);
  const launchedRequest = send({ type: "sentinel_launched", ...identity, child_identity: "child" });
  assertState(await launched, launchedRequest, "launched", 3, "child");

  assert.equal(repository.deliveryAttemptsFor("sentinel-message")[0].state, "acknowledged");
  assert.deepEqual(router.sweepDeliveryTimeouts(), []);
  const manualAck = nextFrame(socket);
  const manualAckRequest = send({ type: "ack", ...receipt });
  const manualAckResponse = await manualAck;
  assert.equal(manualAckResponse.request_id, manualAckRequest);
  assert.equal(manualAckResponse.code, "delivery_not_active");
  assert.equal(repository.messageFor("sentinel-message").state, "acknowledged");

  const exited = nextFrame(socket);
  const exitedRequest = send({
    type: "sentinel_exited",
    agent_id: identity.agent_id,
    instance_id: identity.instance_id,
    lease_id: identity.lease_id,
    occurrence_id: identity.occurrence_id,
    attempt_id: identity.attempt_id,
    exit_code: 0,
  });
  const exitedState = await exited;
  assertState(exitedState, exitedRequest, "exited", 4, "child");
  assert.equal(exitedState.exit_code, 0);
  assert.equal(exitedState.outcome, "succeeded");

  const retryOffer = nextFrame(socket);
  router.enqueueMessage({
    id: "sentinel-retry-message",
    from: "supervisor",
    to: "receiver",
    body: "opaque retry prompt",
    created_at: now,
    sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
  });
  const retryDelivery = await retryOffer;
  const retryReceipt = { ...receipt, message_id: retryDelivery.message.id, attempt_number: retryDelivery.attempt_number };
  send({ type: "injection_requested", ...retryReceipt });
  const retryIdentity = { ...retryReceipt, occurrence_id: "retry-occurrence", attempt_id: "retry-attempt" };
  const retryClaimed = nextFrame(socket);
  const retryClaimRequest = send({ type: "sentinel_claim", ...retryIdentity });
  assertState(await retryClaimed, retryClaimRequest, "claimed", 1, undefined, "retry-occurrence");
  const retryLaunching = nextFrame(socket);
  const retryLaunchingRequest = send({ type: "sentinel_launching", ...retryIdentity });
  assertState(await retryLaunching, retryLaunchingRequest, "launching", 2, undefined, "retry-occurrence");
  const spawnFailed = nextFrame(socket);
  const spawnFailedRequest = send({ type: "sentinel_spawn_failed", ...retryIdentity });
  assertState(await spawnFailed, spawnFailedRequest, "retryable", 3, undefined, "retry-occurrence");
  assert.equal(repository.messageFor("sentinel-retry-message").state, "queued");
});
