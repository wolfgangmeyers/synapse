import assert from "node:assert/strict";
import test from "node:test";

import {
  decodeHiddenEnvelope,
  validateControlResultPayload,
  validateCustomMessageDetails,
} from "../pi-extensions/mecha-rpc/protocol.ts";

type RecordValue = Record<string, unknown>;

const GENERATION = "123e4567-e89b-42d3-a456-426614174000";
const REQUEST_ID = "123e4567-e89b-42d3-a456-426614174001";
const RESULT_ID = "123e4567-e89b-42d3-a456-426614174002";
const MANIFEST_SHA = "a".repeat(64);

function encodeRaw(value: RecordValue): string {
  return Buffer.from(JSON.stringify(value), "utf8").toString("base64url");
}

function controlResult(overrides: RecordValue = {}): RecordValue {
  return {
    protocol: "mecha.control/v1",
    result_id: RESULT_ID,
    request_id: REQUEST_ID,
    target_session_epoch: 1,
    runtime_generation: GENERATION,
    manifest_sha256: MANIFEST_SHA,
    status: "ok",
    content: "Complete",
    details: {},
    ...overrides,
  };
}

function sourceChild(overrides: RecordValue = {}): RecordValue {
  return {
    task_id: "task",
    run_id: "run",
    parent_entry_id: "entry",
    description: "Child task",
    model: { provider: "openai-codex", id: "gpt-5.6", name: "GPT 5.6" },
    lifecycle_state: "running",
    started_at: "2026-09-06T12:00:00.000Z",
    finished_at: null,
    duration_ms: null,
    compaction: { state: "idle", count: 0, started_at: null, last_outcome: null },
    sequence: 1,
    ...overrides,
  };
}

function policyApplied(): RecordValue {
  return {
    inline_enabled: true,
    inline_credits: { bash: 1 },
    recall_content: "",
    settled_enabled: false,
    goal_lock_enabled: false,
    compaction_deferred: false,
  };
}

test("control results accept their documented 16 KiB content and stored-details boundaries", () => {
  const details = { x: "d".repeat(16_376) };
  assert.equal(Buffer.byteLength(JSON.stringify(details), "utf8"), 16 * 1024);
  const payload = controlResult({ content: "c".repeat(16 * 1024), details });
  assert.deepEqual(validateControlResultPayload(decodeHiddenEnvelope(encodeRaw(payload))), payload);

  assert.throws(() => validateControlResultPayload(controlResult({ content: "c".repeat(16 * 1024 + 1) })));
  assert.throws(() => validateControlResultPayload(controlResult({ details: { x: "d".repeat(16_377) } })));
  assert.throws(() => validateControlResultPayload(controlResult({ unexpected: true })));
});

test("raw hidden envelopes reject malformed UTF-8 and preserve valid size boundaries", () => {
  assert.throws(() => decodeHiddenEnvelope(Buffer.from([0xc3, 0x28]).toString("base64url")));
  assert.deepEqual(decodeHiddenEnvelope(encodeRaw({ value: "x".repeat(16 * 1024) })), { value: "x".repeat(16 * 1024) });
  assert.throws(() => decodeHiddenEnvelope(encodeRaw({ value: "x".repeat(16 * 1024 + 1) })));
});

test("hidden envelopes reject lone surrogates and deep nesting before parser stack exhaustion", () => {
  const loneSurrogateValue = Buffer.from('{"value":"\\ud800"}', "utf8").toString("base64url");
  const loneSurrogateKey = Buffer.from('{"\\udc00":true}', "utf8").toString("base64url");
  assert.throws(() => decodeHiddenEnvelope(loneSurrogateValue), TypeError);
  assert.throws(() => decodeHiddenEnvelope(loneSurrogateKey), TypeError);

  const deeplyNested = `{"value":${"[".repeat(12_000)}0${"]".repeat(12_000)}}`;
  const encoded = Buffer.from(deeplyNested, "utf8").toString("base64url");
  assert.ok(Buffer.byteLength(deeplyNested, "utf8") < 131_072);
  assert.throws(() => decodeHiddenEnvelope(encoded), (error: unknown) => {
    assert.ok(error instanceof TypeError);
    assert.doesNotMatch(String(error), /maximum call stack|RangeError/i);
    return true;
  });
});

test("hidden envelope decoding accepts only canonical unpadded base64url", () => {
  const canonical = encodeRaw({ value: "canonical" });
  assert.deepEqual(decodeHiddenEnvelope(canonical), { value: "canonical" });
  assert.throws(() => decodeHiddenEnvelope(`${canonical}=`), "padding is not base64url");
  assert.throws(() => decodeHiddenEnvelope(`+${canonical.slice(1)}`), "standard base64 alphabet is not accepted");
  assert.throws(() => decodeHiddenEnvelope("eyJ2YWx1ZSI6ImNhbm9uaWNhbCJ9A"), "a decodable noncanonical tail is rejected");
});

test("all custom details are closed, including legacy availability and nested source rows", () => {
  const valid: Array<[string, RecordValue]> = [
    ["mecha.tools.accepted.v1", { protocol: "mecha.tools/v1", manifest_sha256: MANIFEST_SHA, runtime_generation: GENERATION, session_epoch: 1 }],
    ["mecha.tools.rejected.v1", { protocol: "mecha.tools/v1", reason: "rejected", runtime_generation: GENERATION, session_epoch: 1 }],
    ["mecha.control.request.v1", { protocol: "mecha.control/v1", request_id: REQUEST_ID, operation_id: "legacy.goal", legacy_command: "goal", raw_arguments: "status", runtime_generation: GENERATION, session_epoch: 1, availability: { session_id: "root-session", kind: "root" } }],
    ["mecha.control.result.v1", { protocol: "mecha.control/v1", result_id: RESULT_ID, request_id: REQUEST_ID, target_session_epoch: 1, runtime_generation: GENERATION, manifest_sha256: MANIFEST_SHA, status: "ok", details: {} }],
    ["mecha.mailbox.appended.v1", { protocol: "mecha.mailbox/v1", message_id: "message", runtime_generation: GENERATION, session_epoch: 1, manifest_sha256: MANIFEST_SHA }],
    ["mecha.policy.applied.v1", { protocol: "mecha.policy/v1", runtime_generation: GENERATION, session_epoch: 1, applied: policyApplied() }],
    ["mecha.child.snapshot.v1", { protocol: "mecha.children/v1", snapshot_id: "snapshot", owner_session_id: "owner", sequence: 1, runs: [sourceChild()] }],
    ["mecha.child.delta.v1", { protocol: "mecha.children/v1", owner_session_id: "owner", sequence: 2, task_id: "task", run_id: "run", parent_entry_id: "entry", changes: { description: "Changed" } }],
  ];
  for (const [type, details] of valid) {
    assert.deepEqual(validateCustomMessageDetails(type as never, details), details, type);
  }

  assert.throws(() => validateCustomMessageDetails("mecha.unknown.v1" as never, {}));
  assert.throws(() => validateCustomMessageDetails("mecha.mailbox.appended.v1", {
    protocol: "mecha.mailbox/v1", message_id: "message", runtime_generation: GENERATION, session_epoch: 1,
  }));
  assert.throws(() => validateCustomMessageDetails("mecha.mailbox.appended.v1", {
    ...valid[4]![1], manifest_sha256: "not-a-sha",
  }));
  assert.throws(() => validateCustomMessageDetails("mecha.control.request.v1", {
    ...valid[2]![1], availability: { session_id: "root-session", kind: "root", evil: true },
  }));
  assert.doesNotThrow(() => validateCustomMessageDetails("mecha.policy.applied.v1", {
    ...valid[5]![1], applied: { ...policyApplied(), recall_content: "r".repeat(24_000) },
  }));
  assert.throws(() => validateCustomMessageDetails("mecha.policy.applied.v1", {
    ...valid[5]![1], applied: { ...policyApplied(), recall_content: "r".repeat(24_001) },
  }));
  assert.throws(() => validateCustomMessageDetails("mecha.policy.applied.v1", {
    ...valid[5]![1], applied: { ...policyApplied(), evil: true },
  }));
  assert.throws(() => validateCustomMessageDetails("mecha.child.snapshot.v1", {
    ...valid[6]![1], runs: [sourceChild({ evil: true })],
  }));
  assert.throws(() => validateCustomMessageDetails("mecha.child.delta.v1", {
    ...valid[7]![1], changes: { evil: true },
  }));
});
