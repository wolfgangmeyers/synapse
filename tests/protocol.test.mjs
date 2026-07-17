import assert from "node:assert/strict";
import test from "node:test";

import { createEnvelope, validateClientFrame, validateEnvelope, validateServerFrame } from "../harness/protocol.js";

const nanoid = /^[A-Za-z0-9]{21}$/;

test("Sentinel descriptor accepts only caller-selected model metadata", () => {
  const base = createEnvelope({ from: "unknown-sender", to: "different-target", body: "opaque" });
  assert.doesNotThrow(() => validateEnvelope({ ...base, sentinel: { model: "gpt-5.6-terra" } }));
  assert.throws(() => validateEnvelope({ ...base, sentinel: {} }));
  assert.throws(() => validateEnvelope({ ...base, sentinel: { model: "gpt-5.6-terra", receiver_policy: "no" } }));
});

test("the shared protocol rejects retired Sentinel lifecycle frames", () => {
  assert.throws(() => validateClientFrame({
    version: "v1", frame_id: "frame", type: "sentinel_claim", agent_id: "receiver",
    instance_id: "instance", lease_id: "lease", message_id: "message", attempt_number: 1,
  }));
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
