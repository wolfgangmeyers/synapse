import assert from "node:assert/strict";
import test from "node:test";

import { createEnvelope, validateClientFrame, validateEnvelope, validateSentinelAdmission, validateServerFrame } from "../harness/protocol.js";

const nanoid = /^[A-Za-z0-9]{21}$/;

test("Sentinel descriptor is optional, closed, and requires policy and model", () => {
  const base = createEnvelope({ from: "sender", to: "recipient", body: "opaque" });
  assert.doesNotThrow(() => validateEnvelope({
    ...base,
    sentinel: { receiver_policy: "managed-receiver", model: "gpt-5.6-terra" },
  }));
  assert.throws(() => validateEnvelope({
    ...base,
    sentinel: { receiver_policy: "managed-receiver" },
  }));
  assert.throws(() => validateEnvelope({
    ...base,
    sentinel: { receiver_policy: "managed-receiver", model: "gpt-5.6-terra", prompt: "no" },
  }));
  assert.throws(() => validateEnvelope({
    ...base,
    sentinel: { receiver_policy: "managed-receiver", model: "gpt-5.6-terra", sentinel_child: { occurrence_id: "occurrence" } },
  }));
});

test("Sentinel admission binds policy to receiver, sender, and model", () => {
  const envelope = createEnvelope({ from: "supervisor", to: "receiver", body: "opaque", sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" } });
  const policies = { managed: { receiver: "receiver", senders: ["supervisor"], models: ["gpt-5.6-terra"], execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: [] } } };
  assert.doesNotThrow(() => validateSentinelAdmission(envelope, policies));
  assert.throws(() => validateSentinelAdmission(envelope, {
    managed: { ...policies.managed, execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: ["sentinel_reply"] } },
  }));
  assert.throws(() => validateSentinelAdmission(envelope, {
    managed: { ...policies.managed, execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: ["other_tool"] } },
  }));
  assert.throws(() => validateSentinelAdmission({ ...envelope, to: "other-receiver" }, policies));
  assert.throws(() => validateSentinelAdmission({ ...envelope, from: "other-sender" }, policies));
  assert.throws(() => validateSentinelAdmission(envelope, { managed: { ...policies.managed, models: [] } }));
  assert.throws(() => validateSentinelAdmission({
    ...envelope,
    sentinel: { ...envelope.sentinel, origin_sender: "forged" },
  }, policies));
});

test("Sentinel claim frames are lease-bound and body-free", () => {
  assert.doesNotThrow(() => validateClientFrame({
    version: "v1", frame_id: "frame", type: "sentinel_claim", agent_id: "receiver",
    instance_id: "instance", lease_id: "lease", message_id: "message", attempt_number: 1, occurrence_id: "occurrence", attempt_id: "attempt",
  }));
  assert.throws(() => validateClientFrame({
    version: "v1", frame_id: "frame", type: "sentinel_claim", agent_id: "receiver",
    instance_id: "instance", lease_id: "lease", message_id: "message", attempt_number: 1, occurrence_id: "occurrence", attempt_id: "attempt", body: "opaque",
  }));
});

test("Sentinel exit frames require a bounded observable exit code", () => {
  const frame = {
    version: "v1", frame_id: "frame", type: "sentinel_exited", agent_id: "receiver",
    instance_id: "instance", lease_id: "lease", occurrence_id: "occurrence", attempt_id: "attempt", exit_code: 0,
  };
  assert.doesNotThrow(() => validateClientFrame(frame));
  assert.throws(() => validateClientFrame({ ...frame, exit_code: 256 }));
  const { exit_code, ...withoutExitCode } = frame;
  assert.throws(() => validateClientFrame(withoutExitCode));
});

test("correlated delivery frames require a valid router-generated incarnation", () => {
  const frame = {
    version: "v1", frame_id: "frame", type: "message", attempt_number: 1,
    delivery_expires_at: "2030-01-01T00:00:00.000Z",
    message: { id: "message", from: "sender", to: "receiver", body: "opaque", created_at: "2030-01-01T00:00:00.000Z" },
    message_incarnation_id: "550e8400-e29b-41d4-a716-446655440000",
  };
  assert.doesNotThrow(() => validateServerFrame(frame));
  assert.throws(() => validateServerFrame({ ...frame, message_incarnation_id: "not-a-uuid" }));
});

test("new envelopes use URL-safe NanoIDs while existing UUID IDs remain valid", () => {
  const envelope = createEnvelope({ from: "sender", to: "recipient", body: "hello" });

  assert.match(envelope.id, nanoid);
  assert.equal(new Set(Array.from({ length: 100 }, () => createEnvelope({
    from: "sender", to: "recipient", body: "hello",
  }).id)).size, 100);
  const legacyId = "6f2b54ae-6ff5-4bf7-a770-ba2c25ef7b31";
  assert.doesNotThrow(() => validateEnvelope({ ...envelope, id: legacyId }));
  assert.doesNotThrow(() => validateClientFrame({
    version: "v1",
    frame_id: legacyId,
    type: "lease_renew",
    agent_id: "worker",
    instance_id: legacyId,
    lease_id: legacyId,
  }));
});
