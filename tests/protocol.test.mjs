import assert from "node:assert/strict";
import test from "node:test";

import { createEnvelope, validateEnvelope } from "../harness/protocol.js";

const nanoid = /^[A-Za-z0-9_-]{21}$/;

test("new envelopes use URL-safe NanoIDs while existing UUID IDs remain valid", () => {
  const envelope = createEnvelope({ from: "sender", to: "recipient", body: "hello" });

  assert.match(envelope.id, nanoid);
  assert.doesNotThrow(() => validateEnvelope({
    ...envelope,
    id: "6f2b54ae-6ff5-4bf7-a770-ba2c25ef7b31",
  }));
});
