import assert from "node:assert/strict";
import test from "node:test";

import { createEnvelope, ProtocolValidationError, validateClientFrame, validateEnvelope, validateServerFrame } from "../harness/protocol.js";

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

test("activity frames carry lease-scoped worker state", () => {
  const active = {
    version: "v1",
    frame_id: "activity-1",
    type: "activity",
    agent_id: "worker",
    instance_id: "instance",
    lease_id: "lease",
    activity_state: "active",
  };
  assert.equal(validateClientFrame(active), active);
  assert.equal(validateClientFrame({ ...active, subagent_count: 2 }).subagent_count, 2);
  assert.deepEqual(validateClientFrame({ ...active, subagents: [{ id: "subagent-one", type: "audit", description: "checking the diff" }] }).subagents, [{ id: "subagent-one", type: "audit", description: "checking the diff" }]);
  assert.equal(validateClientFrame({ ...active, activity_state: "settled" }).activity_state, "settled");
  assert.equal(validateClientFrame({ ...active, activity_state: "compacting" }).activity_state, "compacting");
  assert.throws(() => validateClientFrame({ ...active, subagent_count: -1 }), ProtocolValidationError);
  assert.throws(() => validateClientFrame({ ...active, subagent_count: 1.5 }), ProtocolValidationError);
  assert.throws(() => validateClientFrame({ ...active, activity_state: "idle" }), ProtocolValidationError);
  assert.throws(() => validateClientFrame({ ...active, lease_id: undefined }), ProtocolValidationError);

  const event = {
    version: "v1",
    frame_id: "activity-event-1",
    type: "agent_activity",
    agent_id: "worker",
    activity_state: "active",
    generated_at: "2030-01-01T00:00:00.000Z",
  };
  assert.equal(validateServerFrame(event), event);
  assert.equal(validateServerFrame({ ...event, subagent_count: 3 }).subagent_count, 3);
  assert.deepEqual(validateServerFrame({ ...event, subagents: [{ id: "subagent-one", description: "checking the diff" }] }).subagents, [{ id: "subagent-one", description: "checking the diff" }]);
  assert.equal(validateServerFrame({ ...event, activity_state: "idle" }).activity_state, "idle");
  assert.equal(validateServerFrame({ ...event, activity_state: "compacting" }).activity_state, "compacting");
  assert.throws(() => validateServerFrame({ ...event, subagent_count: -1 }), ProtocolValidationError);
  assert.throws(() => validateServerFrame({ ...event, activity_state: "settled" }), ProtocolValidationError);
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

test("conversation sync is additive to registration and has a canonical server event", () => {
  assert.doesNotThrow(() => validateClientFrame({
    version: "v1",
    frame_id: "register-sync",
    type: "register",
    agent_id: "shared-user",
    instance_id: "web-one",
    conversation_sync: "v1",
  }));
  assert.doesNotThrow(() => validateServerFrame({
    version: "v1",
    frame_id: "conversation-event",
    type: "conversation_message",
    message: {
      sequence: 1,
      id: "message-1",
      message_incarnation_id: "550e8400-e29b-41d4-a716-446655440000",
      from: "shared-user",
      to: "peer",
      body: "image",
      created_at: "2030-01-01T00:00:00.000Z",
      recorded_at: "2030-01-01T00:00:00.001Z",
      attachments: [{
        id: "message-1-1-hash.webp",
        url: "/v1/attachments/message-1-1-hash.webp",
        mime_type: "image/webp",
        size_bytes: 16,
      }],
      source: { kind: "agent" },
    },
  }));
  assert.throws(() => validateClientFrame({
    version: "v1",
    frame_id: "register-unknown-sync",
    type: "register",
    agent_id: "shared-user",
    instance_id: "web-two",
    conversation_sync: "v2",
  }));
});

test("new envelopes use URL-safe NanoIDs while existing UUID IDs remain valid", () => {
  const envelope = createEnvelope({ from: "sender", to: "recipient", body: "hello" });

  assert.match(envelope.id, nanoid);
  assert.equal(new Set(Array.from({ length: 100 }, () => createEnvelope({
    from: "sender", to: "recipient", body: "hello",
  }).id)).size, 100);
  const legacyId = "6f2b54ae-6ff5-4bf7-a770-ba2c25ef7b31";
  assert.doesNotThrow(() => validateEnvelope({ ...envelope, id: legacyId }));
  assert.doesNotThrow(() => validateEnvelope({ ...envelope, source: { kind: "web_ui" } }));
  assert.doesNotThrow(() => validateEnvelope({ ...envelope, source: { kind: "reminder" } }));
  assert.throws(() => validateEnvelope({ ...envelope, source: { kind: "browser" } }), ProtocolValidationError);
  assert.doesNotThrow(() => validateClientFrame({
    version: "v1",
    frame_id: legacyId,
    type: "lease_renew",
    agent_id: "worker",
    instance_id: legacyId,
    lease_id: legacyId,
  }));
});
