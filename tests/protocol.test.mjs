import assert from "node:assert/strict";
import test from "node:test";

import { createEnvelope, validateClientFrame, validateEnvelope } from "../harness/protocol.js";

const nanoid = /^[A-Za-z0-9]{21}$/;

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
