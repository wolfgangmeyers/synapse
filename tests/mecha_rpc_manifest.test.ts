import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";

import {
  canonicalizeJson,
  hashCanonicalJson,
  validateDesiredManifest,
  validateParametersSchema,
} from "../pi-extensions/mecha-rpc/manifest.ts";
import { createToolRegistry } from "../pi-extensions/mecha-rpc/tool-registry.ts";
import {
  CUSTOM_MESSAGE_TYPES,
  HIDDEN_COMMAND_NAMES,
  LEGACY_COMMAND_NAMES,
} from "../pi-extensions/mecha-rpc/protocol.ts";

type RecordValue = Record<string, unknown>;

function manifest(): RecordValue {
  return JSON.parse(readFileSync(resolve("router/contract/managed-tools-v1.json"), "utf8")) as RecordValue;
}

function fakePi(existingTools: string[] = [], failOnRegistration?: number) {
  const tools: Array<RecordValue> = existingTools.map((name) => ({ name }));
  let registrations = 0;
  return {
    getAllTools: () => tools,
    registerTool(tool: RecordValue) {
      registrations++;
      if (registrations === failOnRegistration) throw new Error("simulated Pi registry failure");
      tools.push(tool);
    },
    tools,
  };
}

test("the source-mapped manifest generator has no byte drift", () => {
  const result = spawnSync(process.execPath, ["tools/generate_managed_tools.mjs", "--check"], { cwd: resolve("."), encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
});

test("revision one manifest has exactly the authorized 16 definitions and stable canonical identity", () => {
  const desired = manifest();
  const validated = validateDesiredManifest(desired, { requireBaseline: true });

  assert.equal(validated.tools.length, 16);
  assert.deepEqual(validated.tools.map((tool) => tool.name).sort(), [
    "goal",
    "goal_snooze",
    "mecha_context",
    "mecha_git",
    "mecha_mailbox_send",
    "mecha_parent_message",
    "mecha_pr",
    "mecha_priority",
    "mecha_recall",
    "mecha_reminder",
    "mecha_request_inline_tool_allowance",
    "mecha_router",
    "mecha_settled_notifications",
    "mecha_state",
    "mecha_verification_handoff",
    "mecha_worker",
  ]);
  assert.equal(hashCanonicalJson(validated), hashCanonicalJson(JSON.parse(canonicalizeJson(validated))));
  assert.equal(canonicalizeJson({ z: 1, a: [true, "x"] }), '{"a":[true,"x"],"z":1}');
});

test("protocol inventories stay closed and exact", () => {
  assert.deepEqual(HIDDEN_COMMAND_NAMES, [
    "mecha-bootstrap-v1",
    "mecha-install-tools-v1",
    "mecha-control-result-v1",
    "mecha-mailbox-deliver-v1",
    "mecha-policy-apply-v1",
    "mecha-child-snapshot-v1",
  ]);
  assert.deepEqual(LEGACY_COMMAND_NAMES, [
    "goal",
    "goal-lock",
    "goal-snooze",
    "goal-snooze-turn-limit",
    "goal-snooze-budget",
    "goal-snooze-budget-refill",
    "mecha-parent-message",
    "inline-tools",
    "mecha-reply",
  ]);
  assert.deepEqual(CUSTOM_MESSAGE_TYPES, [
    "mecha.tools.accepted.v1",
    "mecha.tools.rejected.v1",
    "mecha.control.request.v1",
    "mecha.control.result.v1",
    "mecha.mailbox.appended.v1",
    "mecha.policy.applied.v1",
    "mecha.child.snapshot.v1",
    "mecha.child.delta.v1",
  ]);
});

test("closed schema validation rejects coercive, open, and unbounded forms", () => {
  const valid = { type: "object", additionalProperties: false, required: ["count"], properties: {
    count: { type: "integer", minimum: 1, maximum: 5 },
  } };
  assert.deepEqual(validateParametersSchema(valid), valid);
  for (const schema of [
    { type: "object", additionalProperties: true, required: [], properties: {} },
    { type: "string", minLength: 0 },
    { type: "array", items: { type: "string", maxLength: 3 } },
    { type: "object", additionalProperties: false, required: [], properties: { x: { $ref: "#x" } } },
  ]) {
    assert.throws(() => validateParametersSchema(schema));
  }
});

test("schema grammar rejects malformed raw constructs while retaining every declared boundary", () => {
  const parameter = (schema: RecordValue): RecordValue => ({
    type: "object", additionalProperties: false, required: ["value"], properties: { value: schema },
  });
  const properties64 = Object.fromEntries(Array.from({ length: 64 }, (_, index) => [`p${index}`, { type: "boolean" }]));
  let atDepthEight: RecordValue = { type: "boolean" };
  for (let depth = 0; depth < 6; depth++) atDepthEight = { type: "array", items: atDepthEight, maxItems: 0 };
  assert.doesNotThrow(() => validateParametersSchema(parameter({ type: "string", minLength: 0, maxLength: 8_192, enum: ["", "x"] })));
  assert.doesNotThrow(() => validateParametersSchema(parameter({ type: "array", items: { type: "boolean" }, minItems: 0, maxItems: 256 })));
  assert.doesNotThrow(() => validateParametersSchema(parameter({ type: "integer", minimum: -1, maximum: 1, enum: [-1, 1] })));
  assert.doesNotThrow(() => validateParametersSchema(parameter({ type: "number", minimum: -1, maximum: 1, enum: [-1, 1] })));
  assert.doesNotThrow(() => validateParametersSchema({ type: "object", additionalProperties: false, required: [], properties: properties64 }));
  assert.doesNotThrow(() => validateParametersSchema(parameter(atDepthEight)));

  const atDepthNine = { type: "array", items: atDepthEight, maxItems: 0 };
  for (const invalid of [
    parameter({ type: "null" }),
    parameter({ type: "string", maxLength: 1, pattern: ".*" }),
    parameter({ type: "string", minLength: 2, maxLength: 1 }),
    parameter({ type: "string", minLength: 0 }),
    parameter({ type: "array", items: { type: "boolean" } }),
    parameter({ type: "array", items: { type: "boolean" }, minItems: 2, maxItems: 1 }),
    parameter({ type: "integer", maximum: 1 }),
    parameter({ type: "number", minimum: 0 }),
    parameter({ type: "integer", minimum: 2, maximum: 1 }),
    parameter({ type: "object", additionalProperties: {}, required: [], properties: {} }),
    parameter({ type: "object", additionalProperties: false, required: [], properties: { x: { $ref: "#x" } } }),
    parameter({ type: "object", additionalProperties: false, required: [], properties: { x: { type: "boolean", const: true } } }),
    parameter({ type: "object", additionalProperties: false, required: [], properties: { x: { type: "boolean", anyOf: [] } } }),
    { type: "object", additionalProperties: false, required: [], properties: Object.fromEntries(Array.from({ length: 65 }, (_, index) => [`p${index}`, { type: "boolean" }])) },
    parameter(atDepthNine),
  ]) {
    assert.throws(() => validateParametersSchema(invalid));
  }
});

test("full installs preflight atomically and supplemental installation is one unused immutable name", async () => {
  const rejectedPi = fakePi();
  const rejectedRegistry = createToolRegistry(rejectedPi, {
    emitControl: async () => {},
    getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: "".padStart(64, "a"), sessionEpoch: 1 }),
    getContextRef: () => "root",
    onRequestQueued: () => {},
  });
  const untrusted = manifest();
  assert.throws(() => rejectedRegistry.installFull({
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174001",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: untrusted, manifest_sha256: hashCanonicalJson(untrusted), restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" }, unexpected: true,
  }));
  assert.equal(rejectedPi.tools.length, 0);

  const pi = fakePi();
  const emitted: RecordValue[] = [];
  const registry = createToolRegistry(pi, {
    emitControl: async (request) => { emitted.push(request); },
    getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: "".padStart(64, "a"), sessionEpoch: 1 }),
    getContextRef: () => "root",
  });
  const desired = manifest();
  const installed = registry.installFull({
    protocol: "mecha.bootstrap/v1",
    delivery_id: "123e4567-e89b-42d3-a456-426614174001",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    session_epoch: 1,
    desired_manifest: desired,
    manifest_sha256: hashCanonicalJson(desired),
    restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} },
    recall_projection: { enabled: false, stores: [], content: "" },
  });
  assert.equal(installed.tools.length, 16);
  assert.equal(pi.tools.length, 16);

  const target = structuredClone(desired) as RecordValue;
  const tools = target.tools as RecordValue[];
  const extra = {
    capability_id: "future_capability",
    name: "future_capability__" + "b".repeat(64),
    label: "Future capability",
    description: "A bounded future capability.",
    parameters: { type: "object", additionalProperties: false, required: [], properties: {} },
    operation_id: "future.operation",
    prompt_guidelines: [],
  };
  const definition = { ...extra };
  delete (definition as RecordValue).name;
  const definitionHash = hashCanonicalJson(definition);
  tools.push({ ...extra, definition_sha256: definitionHash, definition_id: `def_${definitionHash}` });
  tools.sort((left, right) => String(left.capability_id).localeCompare(String(right.capability_id)));
  target.revision = 2;
  const added = registry.installSupplemental({
    protocol: "mecha.tools-install/v1",
    delivery_id: "123e4567-e89b-42d3-a456-426614174002",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    session_epoch: 1,
    base_manifest_sha256: hashCanonicalJson(desired),
    desired_manifest: target,
    manifest_sha256: hashCanonicalJson(target),
    added_name: extra.name,
  });
  assert.equal(added.tools.length, 17);
  assert.equal(pi.tools.length, 17);

  await assert.rejects(async () => registry.installSupplemental({
    protocol: "mecha.tools-install/v1",
    delivery_id: "123e4567-e89b-42d3-a456-426614174003",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    session_epoch: 1,
    base_manifest_sha256: hashCanonicalJson(target),
    desired_manifest: target,
    manifest_sha256: hashCanonicalJson(target),
    added_name: extra.name,
  }));
  assert.equal(pi.tools.length, 17);
  assert.deepEqual(emitted, []);
});

test("registry exceptions at positions 1, 8, and 16 make every partial manifest terminal", async () => {
  const desired = manifest();
  for (const position of [1, 8, 16]) {
    const pi = fakePi([], position);
    let fatal: unknown;
    const registry = createToolRegistry(pi, {
      emitControl: async () => {},
      getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: "".padStart(64, "a"), sessionEpoch: 1 }),
      getContextRef: () => "root",
      onRequestQueued: () => {},
      onFatalRegistrationFailure: (error) => { fatal = error; },
    });
    assert.throws(() => registry.installFull({
      protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174001",
      runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
      desired_manifest: desired, manifest_sha256: hashCanonicalJson(desired), restored_request_ids: [],
      inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
    }));
    assert.ok(fatal instanceof Error);
    assert.equal(pi.tools.length, position - 1);
    assert.equal(registry.acceptedManifest(), undefined);
  }
});

test("a registry exception makes a partial manifest terminal before model use", async () => {
  const pi = fakePi([], 8);
  let fatal: unknown;
  const registry = createToolRegistry(pi, {
    emitControl: async () => {},
    getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: "".padStart(64, "a"), sessionEpoch: 1 }),
    getContextRef: () => "root",
    onFatalRegistrationFailure: (error) => { fatal = error; },
  });
  const desired = manifest();
  const envelope = {
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174001",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: desired, manifest_sha256: hashCanonicalJson(desired), restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
  };
  assert.throws(() => registry.installFull(envelope));
  assert.ok(fatal instanceof Error);
  assert.equal((fatal as Error).message, "simulated Pi registry failure");
  assert.equal(pi.tools.length, 7);
  assert.equal(registry.acceptedManifest(), undefined);
  const partial = pi.tools[0]!;
  const result = await (partial.execute as (toolCallId: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>)("partial-tool", {}, undefined, undefined, {});
  assert.equal(result.isError, true);
  assert.match(String((result.content as Array<RecordValue>)[0]!.text), /unavailable after a registration failure/);
});

test("a collision or one malformed tool leaves no partial full installation", () => {
  const collisionPi = fakePi(["goal"]);
  const collision = createToolRegistry(collisionPi, {
    emitControl: async () => {},
    getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: "".padStart(64, "a"), sessionEpoch: 1 }),
    getContextRef: () => "root",
  });
  const desired = manifest();
  assert.throws(() => collision.installFull({
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174001",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: desired, manifest_sha256: hashCanonicalJson(desired), restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
  }));
  assert.equal(collisionPi.tools.length, 1);

  const malformed = structuredClone(desired) as RecordValue;
  ((malformed.tools as RecordValue[])[2]!.parameters as RecordValue).additionalProperties = true;
  const emptyPi = fakePi();
  const registry = createToolRegistry(emptyPi, {
    emitControl: async () => {},
    getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: "".padStart(64, "a"), sessionEpoch: 1 }),
    getContextRef: () => "root",
  });
  assert.throws(() => registry.installFull({
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174001",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: malformed, manifest_sha256: hashCanonicalJson(malformed), restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
  }));
  assert.equal(emptyPi.tools.length, 0);
});
