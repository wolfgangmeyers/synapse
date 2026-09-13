import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";

import {
  canonicalizeJson,
  hashCanonicalJson,
  validateArguments,
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

test("revision two manifest retains the 16 baseline definitions and adds source-backed web search", () => {
  const desired = manifest();
  const validated = validateDesiredManifest(desired, { requireBaselineCapabilities: true });

  assert.equal(validated.revision, 2);
  assert.equal(validated.tools.length, 17);
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
    "web_search__4f4ca2a3ae8ea237531b8c5264d53d15474c0848fc8d7eeada0fef5b85b139ac",
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
    "mecha-post-compaction-notification-v1",
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
  assert.equal(installed.tools.length, 17);
  assert.equal(pi.tools.length, 17);

  const target = structuredClone(desired) as RecordValue;
  const tools = target.tools as RecordValue[];
  const extra = {
    capability_id: "future_capability",
    label: "Future capability",
    description: "A bounded future capability.",
    parameters: { type: "object", additionalProperties: false, required: [], properties: {} },
    operation_id: "future.operation",
    prompt_guidelines: [],
  };
  const definitionHash = hashCanonicalJson(extra);
  const extraName = `future_capability__${definitionHash}`;
  tools.push({ ...extra, name: extraName, definition_sha256: definitionHash, definition_id: `def_${definitionHash}` });
  tools.sort((left, right) => Buffer.compare(Buffer.from(String(left.capability_id), "utf8"), Buffer.from(String(right.capability_id), "utf8")));
  target.revision = 3;
  const added = registry.installSupplemental({
    protocol: "mecha.tools-install/v1",
    delivery_id: "123e4567-e89b-42d3-a456-426614174002",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    session_epoch: 1,
    base_manifest_sha256: hashCanonicalJson(desired),
    desired_manifest: target,
    manifest_sha256: hashCanonicalJson(target),
    added_name: extraName,
  });
  assert.equal(added.tools.length, 18);
  assert.equal(pi.tools.length, 18);

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
  assert.equal(pi.tools.length, 18);
  assert.deepEqual(emitted, []);
});

test("registry exceptions at positions 1, 8, and 17 make every partial manifest terminal", async () => {
  const desired = manifest();
  for (const position of [1, 8, 17]) {
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

test("root dispatch acknowledges before its Pi send promise settles", async () => {
  const pi = fakePi();
  const pending = new Set<string>();
  let prepared = false;
  let emissionCalls = 0;
  const emission = Promise.withResolvers<void>();
  const registry = createToolRegistry(pi, {
    emitControl: (request) => {
      assert.equal(prepared, true, "root request must be registered before emission");
      emissionCalls++;
      return emission.promise;
    },
    getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: hashCanonicalJson(manifest()), sessionEpoch: 1 }),
    getContextRef: () => "root",
    onRootRequestPrepared: (request) => {
      prepared = true;
      assert.equal(typeof request.request_id, "string");
      pending.add(String(request.request_id));
    },
    onRootRequestSendFailed: (request) => { pending.delete(String(request.request_id)); },
  });
  const desired = manifest();
  registry.installFull({
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174060",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: desired, manifest_sha256: hashCanonicalJson(desired), restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
  });
  const tool = pi.tools.find((candidate) => candidate.name === "goal");
  assert.ok(tool);
  registry.captureRaw("root-call", "goal", { action: "status" });
  const execute = tool!.execute as (toolCallId: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>;
  const queued = await execute("root-call", { action: "status" }, undefined, undefined, {});
  assert.equal(queued.isError, undefined);
  assert.equal(queued.details !== undefined && (queued.details as RecordValue).status, "queued");
  assert.equal(emissionCalls, 1);
  assert.equal(pending.size, 1);
  emission.resolve();
  await new Promise<void>((resolve) => setImmediate(resolve));
  assert.equal(emissionCalls, 1, "send settlement must not emit a second request");
  assert.equal(pending.size, 1, "successful send settlement leaves correlation for the domain result");
});

test("root send rejection rolls back only its pending request", async () => {
  const pi = fakePi();
  const pending = new Set<string>();
  const failed: string[] = [];
  const emissions = new Map<string, PromiseWithResolvers<void>>();
  const registry = createToolRegistry(pi, {
    emitControl: (request) => {
      const requestId = String(request.request_id);
      const deferred = Promise.withResolvers<void>();
      emissions.set(requestId, deferred);
      return deferred.promise;
    },
    getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: hashCanonicalJson(manifest()), sessionEpoch: 1 }),
    getContextRef: () => "root",
    onRootRequestPrepared: (request) => { pending.add(String(request.request_id)); },
    onRootRequestSendFailed: (request) => {
      const requestId = String(request.request_id);
      if (pending.delete(requestId)) failed.push(requestId);
    },
  });
  const desired = manifest();
  registry.installFull({
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174061",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: desired, manifest_sha256: hashCanonicalJson(desired), restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
  });
  const tool = pi.tools.find((candidate) => candidate.name === "goal");
  assert.ok(tool);
  const execute = tool!.execute as (toolCallId: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>;
  registry.captureRaw("first-root-call", "goal", { action: "status" });
  registry.captureRaw("second-root-call", "goal", { action: "status" });
  await execute("first-root-call", { action: "status" }, undefined, undefined, {});
  await execute("second-root-call", { action: "status" }, undefined, undefined, {});
  const requestIds = [...emissions.keys()];
  assert.equal(requestIds.length, 2);
  emissions.get(requestIds[0]!)!.reject(new Error("send failed"));
  await new Promise<void>((resolve) => setImmediate(resolve));
  assert.deepEqual([...pending], [requestIds[1]]);
  assert.deepEqual(failed, [requestIds[0]]);
  emissions.get(requestIds[1]!)!.resolve();
  await new Promise<void>((resolve) => setImmediate(resolve));
  assert.deepEqual([...pending], [requestIds[1]], "the other request remains correlated until its result");
});

test("child dispatch remains awaiting its Pi send before queue acknowledgement", async () => {
  const pi = fakePi();
  const emission = Promise.withResolvers<void>();
  let queued = false;
  const registry = createToolRegistry(pi, {
    emitControl: () => emission.promise,
    getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: hashCanonicalJson(manifest()), sessionEpoch: 1 }),
    getContextRef: () => "ctx1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    onRequestQueued: () => { queued = true; },
  });
  const desired = manifest();
  registry.installFull({
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174062",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: desired, manifest_sha256: hashCanonicalJson(desired), restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
  });
  const tool = pi.tools.find((candidate) => candidate.name === "goal");
  assert.ok(tool);
  registry.captureRaw("child-call", "goal", { action: "status" });
  const execute = tool!.execute as (toolCallId: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>;
  let settled = false;
  const result = execute("child-call", { action: "status" }, undefined, undefined, {}).then((value) => { settled = true; return value; });
  await new Promise<void>((resolve) => setImmediate(resolve));
  assert.equal(settled, false);
  assert.equal(queued, false);
  emission.resolve();
  const acknowledged = await result;
  assert.equal(acknowledged.isError, undefined);
  assert.equal(queued, true);
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

function manifestTool(capabilityId: string, description = "A bounded future capability."): RecordValue {
  const definition = {
    capability_id: capabilityId,
    label: `Label ${capabilityId}`,
    description,
    parameters: { type: "object", additionalProperties: false, required: [], properties: {} },
    operation_id: `future.${capabilityId}`,
  };
  const definitionSha = hashCanonicalJson(definition);
  return {
    ...definition,
    name: `${capabilityId}__${definitionSha}`,
    definition_id: `def_${definitionSha}`,
    definition_sha256: definitionSha,
  };
}

function sortedManifest(tools: RecordValue[], revision: number): RecordValue {
  return {
    protocol: "mecha.tools/v1",
    revision,
    tools: tools.sort((left, right) => Buffer.compare(
      Buffer.from(String(left.capability_id), "utf8"),
      Buffer.from(String(right.capability_id), "utf8"),
    )),
  };
}

test("fresh current manifests accept valid 18 through 32 tool states and changed definitions use hash names", () => {
  const baseline = manifest();
  const baseTools = structuredClone(baseline.tools) as RecordValue[];
  const current18 = sortedManifest([...baseTools, manifestTool("future_capability")], 3);
  assert.equal(validateDesiredManifest(current18, { requireBaselineCapabilities: true }).tools.length, 18);

  const current32 = sortedManifest([
    ...baseTools,
    ...Array.from({ length: 15 }, (_, index) => manifestTool(`future_${String(index).padStart(2, "0")}`)),
  ], 3);
  assert.equal(validateDesiredManifest(current32, { requireBaselineCapabilities: true }).tools.length, 32);

  const changedTools = structuredClone(baseline.tools) as RecordValue[];
  const changed = changedTools.find((tool) => tool.capability_id === "goal")!;
  const changedDefinition = {
    capability_id: changed.capability_id,
    label: changed.label,
    description: "Changed goal definition.",
    parameters: changed.parameters,
    operation_id: changed.operation_id,
    ...(changed.prompt_snippet === undefined ? {} : { prompt_snippet: changed.prompt_snippet }),
    ...(changed.prompt_guidelines === undefined ? {} : { prompt_guidelines: changed.prompt_guidelines }),
  };
  const changedSha = hashCanonicalJson(changedDefinition);
  Object.assign(changed, {
    ...changedDefinition,
    name: `goal__${changedSha}`,
    definition_id: `def_${changedSha}`,
    definition_sha256: changedSha,
  });
  const restarted = sortedManifest(changedTools, 3);
  assert.doesNotThrow(() => validateDesiredManifest(restarted, { requireBaselineCapabilities: true }));
  assert.equal((restarted.tools as RecordValue[]).find((tool) => tool.capability_id === "goal")!.name, `goal__${changedSha}`);

  const incorrectlyNamedAdditional = structuredClone(current18) as RecordValue;
  const additional = (incorrectlyNamedAdditional.tools as RecordValue[]).find((tool) => tool.capability_id === "future_capability")!;
  additional.name = "future_capability";
  assert.throws(() => validateDesiredManifest(incorrectlyNamedAdditional, { requireBaselineCapabilities: true }));

  const incorrectlyNamedChange = structuredClone(restarted) as RecordValue;
  const changedAgain = (incorrectlyNamedChange.tools as RecordValue[]).find((tool) => tool.capability_id === "goal")!;
  changedAgain.name = "goal";
  assert.throws(() => validateDesiredManifest(incorrectlyNamedChange, { requireBaselineCapabilities: true }));

  const overLimit = sortedManifest([
    ...(current32.tools as RecordValue[]),
    manifestTool("future_16"),
  ], 3);
  assert.throws(() => validateDesiredManifest(overLimit, { requireBaselineCapabilities: true }));
});

test("TypeBox-built schemas enforce string enum length bounds at runtime", () => {
  const withValue = (schema: RecordValue): RecordValue => ({
    type: "object", additionalProperties: false, required: ["value"], properties: { value: schema },
  });
  for (const schema of [
    { type: "string", minLength: 2, maxLength: 3, enum: ["x", "valid"] },
    { type: "string", minLength: 0, maxLength: 3, enum: ["valid", "four"] },
  ]) {
    assert.throws(() => validateParametersSchema(withValue(schema)), JSON.stringify(schema));
  }
  const bounded = validateParametersSchema(withValue({ type: "string", minLength: 2, maxLength: 3, enum: ["ok", "yes"] }));
  assert.equal(validateArguments({ value: "ok" }, bounded), true);
  assert.equal(validateArguments({ value: "x" }, bounded), false);
  assert.equal(validateArguments({ value: "long" }, bounded), false);
  assert.equal(validateArguments({ value: "no" }, bounded), false);
});

test("canonical JSON follows RFC 8785 readiness-source vectors", () => {
  const vectors: Array<[RecordValue, string, string]> = [
    [{ compaction: { deferred: false }, goal_lock: { enabled: false }, inline: { credits: {}, enabled: false }, recall: { content: "" }, settled: { enabled: false } }, '{"compaction":{"deferred":false},"goal_lock":{"enabled":false},"inline":{"credits":{},"enabled":false},"recall":{"content":""},"settled":{"enabled":false}}', "dcb17869020df00dd27a11e14cc0facada376f729a13002185a3d4b87f3c3648"],
    [{ "\ue000": 1, "😀": 2 }, '{"😀":2,"":1}', "28c95d1bbb2209223307e62f489020e8f9e0cfa16adf2daf6d88127a1e8dd22a"],
    [{ z: "\u000f\n\"\\", a: "€" }, '{"a":"€","z":"\\u000f\\n\\\"\\\\"}', "98f26eda458b7d2c608d427a3cc241632efa23705871286bd5d95bde7917cca7"],
    [{ literal: "\\uD800", paired: "😀" }, '{"literal":"\\\\uD800","paired":"😀"}', "79077a6609d12b2b3f6572139f10b86fcb6dfe86381f473c39cf7d9b512a3a65"],
    [{ a: -0, b: 1e30, c: 2e-3, d: 333333333.33333329, e: 1e-27, f: 0.000001, g: 100000000000000000000, h: 1e21 }, '{"a":0,"b":1e+30,"c":0.002,"d":333333333.3333333,"e":1e-27,"f":0.000001,"g":100000000000000000000,"h":1e+21}', "c94f76fd49312072de4b95bcd57daa68330cb61b46cec8fb1ac5a55af5ad2855"],
  ];
  for (const [input, canonical, hash] of vectors) {
    assert.equal(canonicalizeJson(input), canonical);
    assert.equal(hashCanonicalJson(input), hash);
  }

  // The U+1F600 high surrogate sorts before U+E000 by UTF-16 code units,
  // while UTF-8 byte ordering incorrectly puts the private-use character first.
  // Expected string and SHA-256 come from the independent RFC 8785 fixture
  // recorded in the Phase 1 acceptance comparison, not this implementation.
  const fixture = { "😀": 2, "\ue000": 1 };
  assert.equal(canonicalizeJson(fixture), '{"😀":2,"\ue000":1}');
  assert.equal(hashCanonicalJson(fixture), "28c95d1bbb2209223307e62f489020e8f9e0cfa16adf2daf6d88127a1e8dd22a");
  assert.throws(() => canonicalizeJson("\ud800"));
  assert.throws(() => canonicalizeJson({ "\udc00": true }));
  assert.throws(() => validateParametersSchema({
    type: "object", additionalProperties: false, required: [], properties: { "\ud800": { type: "boolean" } },
  }));
});

test("manifest schemas reject every unlisted keyword and enum boundary", () => {
  const withValue = (schema: RecordValue): RecordValue => ({
    type: "object", additionalProperties: false, required: ["value"], properties: { value: schema },
  });
  for (const keyword of ["$ref", "$defs", "oneOf", "anyOf", "allOf", "not", "const", "format", "pattern", "if", "then", "else", "dependentRequired", "default", "examples", "title"]) {
    assert.throws(() => validateParametersSchema(withValue({ type: "boolean", [keyword]: keyword === "$ref" ? "#/value" : true })), keyword);
  }
  for (const schema of [
    { type: "string", maxLength: 3, enum: ["only"] },
    { type: "string", minLength: 2, maxLength: 3, enum: ["x", "yes"] },
    { type: "string", minLength: 0, maxLength: 3, enum: ["yes", "four"] },
    { type: "string", maxLength: 3, enum: ["x", "x"] },
    { type: "string", maxLength: 3, enum: Array.from({ length: 65 }, (_, index) => String(index)) },
    { type: "integer", minimum: 0, maximum: 2, enum: [0, 0] },
    { type: "integer", minimum: 0, maximum: 2, enum: [0, 1.5] },
    { type: "number", minimum: 0, maximum: 2, enum: [0, 3] },
  ]) {
    assert.throws(() => validateParametersSchema(withValue(schema)), JSON.stringify(schema));
  }
});

test("manifest ordering, reserved-name collisions, and supplemental capacity fail before registration", () => {
  const base = manifest();
  const unordered = structuredClone(base) as RecordValue;
  (unordered.tools as RecordValue[]).reverse();
  assert.throws(() => validateDesiredManifest(unordered, { requireBaselineCapabilities: true }));

  const pi = fakePi();
  const registry = createToolRegistry(pi, {
    emitControl: async () => {},
    getRuntime: () => ({ runtimeGeneration: "123e4567-e89b-42d3-a456-426614174000", manifestSha256: "a".repeat(64), sessionEpoch: 1 }),
    getContextRef: () => "root",
  });
  registry.installFull({
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174050",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: base, manifest_sha256: hashCanonicalJson(base), restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
  });
  const full32 = sortedManifest([
    ...(structuredClone(base.tools) as RecordValue[]),
    ...Array.from({ length: 16 }, (_, index) => manifestTool(`future_${String(index).padStart(2, "0")}`)),
  ], 2);
  const rejectedCount = pi.tools.length;
  assert.throws(() => registry.installSupplemental({
    protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174051",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    base_manifest_sha256: hashCanonicalJson(base), desired_manifest: full32,
    manifest_sha256: hashCanonicalJson(full32), added_name: (full32.tools as RecordValue[]).at(-1)!.name,
  }));
  assert.equal(pi.tools.length, rejectedCount);
});
