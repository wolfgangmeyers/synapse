import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import { createMechaRpcExtension } from "../pi-extensions/mecha-rpc/index.ts";
import { hashCanonicalJson, validateDesiredManifest } from "../pi-extensions/mecha-rpc/manifest.ts";
import { decodeHiddenEnvelope, encodeHiddenEnvelope } from "../pi-extensions/mecha-rpc/protocol.ts";

type RecordValue = Record<string, unknown>;
type Command = { handler: (argumentsText: string, context: RecordValue) => Promise<unknown> | unknown };

function fixture(sessionId = "root-session", parentSessionId?: string) {
  const commands = new Map<string, Command>();
  const tools = new Map<string, RecordValue>();
  const handlers = new Map<string, (event: RecordValue, context: RecordValue) => unknown>();
  const sent: Array<{ message: RecordValue; options: RecordValue | undefined }> = [];
  const events = new Map<string, (event: RecordValue) => void>();
  const pi = {
    getAllTools: () => [...tools.values()],
    getCommands: () => [...commands.keys()],
    registerTool(tool: RecordValue) { tools.set(String(tool.name), tool); },
    registerCommand(name: string, command: Command) { commands.set(name, command); },
    on(name: string, handler: (event: RecordValue, context: RecordValue) => unknown) { handlers.set(name, handler); },
    events: { on(name: string, listener: (event: RecordValue) => void) { events.set(name, listener); } },
    sendMessage(message: RecordValue, options?: RecordValue) { sent.push({ message, options }); },
  };
  const context = {
    sessionManager: {
      getSessionId: () => sessionId,
      getHeader: () => parentSessionId === undefined ? {} : { parentSession: parentSessionId },
    },
    ui: { notify() { throw new Error("managed bridge must not use UI console"); } },
  };
  const extension = createMechaRpcExtension(pi, { getRuntimeGeneration: () => "123e4567-e89b-42d3-a456-426614174000", getSessionEpoch: () => 1 });
  handlers.get("session_start")!({}, context);
  return { commands, context, events, extension, handlers, pi, sent, tools };
}

function manifest(): RecordValue {
  return JSON.parse(readFileSync("router/contract/managed-tools-v1.json", "utf8")) as RecordValue;
}

function manifestHash(value: RecordValue): string {
  return hashCanonicalJson(validateDesiredManifest(value));
}

function supplementalManifest(base: RecordValue): RecordValue {
  const desired = structuredClone(base);
  const template = (desired.tools as RecordValue[])[0]!;
  const definition = {
    capability_id: "zz_supplemental",
    label: "Supplemental tool",
    description: "A bounded supplemental managed tool.",
    parameters: template.parameters,
    operation_id: "mecha.supplemental",
  };
  const definitionSha = hashCanonicalJson(definition);
  (desired.tools as RecordValue[]).push({
    ...definition,
    name: `zz_supplemental__${definitionSha}`,
    definition_id: `def_${definitionSha}`,
    definition_sha256: definitionSha,
  });
  desired.revision = 2;
  return desired;
}

function bootstrap(current: ReturnType<typeof fixture>) {
  const desired = manifest();
  const payload = {
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174001",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: desired, manifest_sha256: current.extension.hashManifest(desired),
    restored_request_ids: [], inline_policy: { enabled: true, credits: {} },
    recall_projection: { enabled: false, stores: [], content: "" },
  };
  return current.commands.get("mecha-bootstrap-v1")!.handler(encodeHiddenEnvelope(payload), current.context);
}

test("the extension preflights all 15 command names and registers no Mailbox bridge", () => {
  const current = fixture();
  assert.deepEqual([...current.commands.keys()], [
    "mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1",
    "goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply",
  ]);
  assert.equal(current.commands.has("mecha-mailbox"), false);
  assert.equal(current.tools.size, 0);
});

test("every legacy command preserves raw input and root or child availability in one typed request", async () => {
  const current = fixture();
  for (const [index, name] of [...current.commands.keys()].slice(6).entries()) {
    const raw = ` raw input ${index}  `;
    await current.commands.get(name)!.handler(raw, current.context);
    const call = current.sent.at(-1);
    assert.ok(call);
    assert.equal(call!.message.customType, "mecha.control.request.v1");
    assert.equal(call!.message.display, false);
    assert.deepEqual(call!.options, { triggerTurn: false });
    const details = call!.message.details as RecordValue;
    assert.equal(details.legacy_command, name);
    assert.equal(details.raw_arguments, raw);
    assert.deepEqual(details.availability, { session_id: "root-session", kind: "root" });
  }
  assert.equal(current.sent.length, 9);
});

test("bootstrap is atomic before model tools exist and a raw coercive tool call fails before control traffic", async () => {
  const current = fixture();
  await bootstrap(current);
  assert.equal(current.tools.size, 16);
  const receipt = current.sent.at(-1)!;
  assert.equal(receipt.message.customType, "mecha.tools.accepted.v1");

  const countTool = [...current.tools.values()].find((tool) => tool.name === "mecha_request_inline_tool_allowance");
  assert.ok(countTool);
  const execute = countTool!.execute as (id: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>;
  current.handlers.get("message_end")!({ message: { role: "assistant", content: [{ type: "toolCall", id: "raw-id", name: "mecha_request_inline_tool_allowance", arguments: { toolName: "bash", usageCount: "2" } }] } }, current.context);
  const result = await execute("raw-id", { toolName: "bash", usageCount: 2 }, undefined, undefined, current.context);
  assert.equal(result.isError, true);
  assert.equal(current.sent.filter(({ message }) => message.customType === "mecha.control.request.v1").length, 0);

  current.handlers.get("message_end")!({ message: { role: "assistant", content: [{ type: "toolCall", id: "good-id", name: "mecha_request_inline_tool_allowance", arguments: { toolName: "bash", usageCount: 2 } }] } }, current.context);
  const queued = await execute("good-id", { toolName: "bash", usageCount: 2 }, undefined, undefined, current.context);
  assert.equal(queued.isError, undefined);
  assert.equal(current.sent.filter(({ message }) => message.customType === "mecha.control.request.v1").length, 1);
  const queuedRequest = current.sent.at(-1)!.message.details as RecordValue;
  const resultId = "123e4567-e89b-42d3-a456-426614174002";
  await current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.control/v1", result_id: resultId, request_id: queuedRequest.request_id,
    target_session_epoch: 1, runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    manifest_sha256: current.extension.hashManifest(manifest()), status: "ok", content: "Complete", details: {},
  }), current.context);
  const completion = current.sent.at(-1)!;
  assert.equal(completion.message.customType, "mecha.control.result.v1");
  assert.equal(completion.message.display, false);
  assert.deepEqual(completion.options, { triggerTurn: false });
  assert.deepEqual(Object.keys(completion.message.details as RecordValue).sort(), ["details", "manifest_sha256", "protocol", "request_id", "result_id", "runtime_generation", "status", "target_session_epoch"]);
  await assert.rejects(async () => current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.control/v1", result_id: "123e4567-e89b-42d3-a456-426614174003", request_id: queuedRequest.request_id,
    target_session_epoch: 1, runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    manifest_sha256: current.extension.hashManifest(manifest()), status: "ok", content: "duplicate", details: {},
  }), current.context));
});

test("raw managed arguments reject boolean coercion, unknown keys, deep values, oversized strings, and oversized arrays before control traffic", async () => {
  const current = fixture();
  await bootstrap(current);
  const invokeRejected = async (name: string, argumentsValue: RecordValue, id: string): Promise<void> => {
    const tool = current.tools.get(name);
    assert.ok(tool, `missing ${name}`);
    const execute = tool.execute as (toolCallId: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>;
    current.handlers.get("message_end")!({ message: { role: "assistant", content: [{ type: "toolCall", id, name, arguments: argumentsValue }] } }, current.context);
    const result = await execute(id, argumentsValue, undefined, undefined, current.context);
    assert.equal(result.isError, true, id);
  };
  const deepValue: RecordValue = { action: "set" };
  let nested: unknown = [];
  for (let depth = 0; depth < 10; depth++) nested = [nested];
  deepValue.goal = nested;
  await invokeRejected("mecha_context", { action: "set_policy", policy: { levels: [{ percent: 50, mandatory: "true" }] } }, "boolean-string");
  await invokeRejected("goal", { action: "status", unexpected: true }, "unknown-key");
  await invokeRejected("goal", deepValue, "deep-value");
  await invokeRejected("goal", { action: "set", goal: "x".repeat(4_001) }, "oversized-string");
  await invokeRejected("mecha_context", {
    action: "set_policy",
    policy: { levels: Array.from({ length: 65 }, () => ({ percent: 50 })) },
  }, "oversized-array");
  assert.equal(current.sent.filter((entry) => entry.message.customType === "mecha.control.request.v1").length, 0);
});

test("three non-manifest hidden commands reject unknown envelope fields before side effects", async () => {
  const current = fixture();
  await bootstrap(current);
  const shared = {
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    session_epoch: 1,
    manifest_sha256: current.extension.hashManifest(manifest()),
    unexpected: true,
  };
  await assert.rejects(async () => current.commands.get("mecha-mailbox-deliver-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.mailbox-deliver/v1", message_id: "message", content: "hello", ...shared,
  }), current.context));
  await assert.rejects(async () => current.commands.get("mecha-policy-apply-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.policy/v1", policy: {}, ...shared,
  }), current.context));
  await assert.rejects(async () => current.commands.get("mecha-child-snapshot-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.child-snapshot/v1", ...shared,
  }), current.context));
  assert.equal(current.sent.filter(({ message }) => message.customType === "mecha.mailbox.appended.v1" || message.customType === "mecha.policy.applied.v1" || message.customType === "mecha.child.snapshot.v1").length, 0);
});

test("tool-call hooks fail closed without raw managed input and consume only granted root inline credits", async () => {
  const current = fixture();
  await bootstrap(current);
  const hook = current.handlers.get("tool_call")!;
  const blockedRaw = hook({ toolCallId: "missing", toolName: "goal" }, current.context) as RecordValue;
  assert.deepEqual(blockedRaw, { block: true, reason: "Managed tool arguments were not captured as a valid raw value." });
  const blockedInline = hook({ toolCallId: "bash-one", toolName: "bash" }, current.context) as RecordValue;
  assert.deepEqual(blockedInline, { block: true, reason: "This managed runtime requires an inline-tool allowance." });
  await current.commands.get("mecha-policy-apply-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.policy/v1", runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    manifest_sha256: current.extension.hashManifest(manifest()), policy: { inline: { enabled: true, credits: { bash: 1 } } },
  }), current.context);
  assert.equal(hook({ toolCallId: "bash-two", toolName: "bash" }, current.context), undefined);
  const exhausted = hook({ toolCallId: "bash-three", toolName: "bash" }, current.context) as RecordValue;
  assert.equal(exhausted.block, true);
});

test("child bridge uses service-only context references and strips them from wire snapshots and deltas", async () => {
  const symbol = Symbol.for("@gotgenes/pi-subagents:service-registry");
  const previous = Object.getOwnPropertyDescriptor(globalThis, symbol);
  const appended: Array<RecordValue> = [];
  let releaseAppend: (() => void) | undefined;
  const service = {
    getLifecycleSnapshotV2(ownerSessionId: string) {
      return { protocol: "mecha.children/v1", snapshot_id: "snapshot", owner_session_id: ownerSessionId, sequence: 1, runs: [{
        ...sourceChild(), context_ref: "ctx1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      }] };
    },
    async appendControlResultV1(contextRef: string, payload: RecordValue) {
      appended.push({ contextRef, payload });
      return new Promise((resolve) => { releaseAppend = () => resolve({ kind: "accepted", result_id: payload.result_id }); });
    },
  };
  Object.defineProperty(globalThis, symbol, { configurable: true, value: { services: new Map([["root-session", service]]) } });
  try {
    const current = fixture();
    const snapshot = await current.commands.get("mecha-child-snapshot-v1")!.handler(encodeHiddenEnvelope({
      protocol: "mecha.child-snapshot/v1", runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    }), current.context);
    assert.ok(snapshot);
    const wire = current.sent.at(-1)!.message;
    assert.equal(wire.customType, "mecha.child.snapshot.v1");
    assert.equal(JSON.stringify(wire).includes("context_ref"), false);

    current.extension.bindPendingRequest({ requestId: "123e4567-e89b-42d3-a456-426614174010", taskId: "task", runId: "run", parentEntryId: "entry" });
    const payload = {
      protocol: "mecha.control/v1", result_id: "123e4567-e89b-42d3-a456-426614174011", request_id: "123e4567-e89b-42d3-a456-426614174010",
      target_session_epoch: 1, runtime_generation: "123e4567-e89b-42d3-a456-426614174000", manifest_sha256: "a".repeat(64), status: "ok", content: "Done", details: {},
    };
    const appendDuringStream = current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope(payload), current.context);
    await Promise.resolve();
    assert.equal(appended.length, 1);
    assert.equal(current.sent.filter(({ message }) => message.customType === "mecha.control.result.v1").length, 0);
    assert.ok(releaseAppend);
    releaseAppend();
    await appendDuringStream;
    assert.equal(appended[0]!.contextRef, "ctx1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
    // A terminal service append consumes the correlation, so a duplicate router
    // delivery cannot re-enter the same child context.
    await assert.rejects(async () => current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope(payload), current.context));
    assert.equal(appended.length, 1);

    current.extension.clearChildBindings();
    await assert.rejects(async () => current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope(payload), current.context));
    assert.equal(appended.length, 1);
  } finally {
    if (previous === undefined) delete (globalThis as Record<PropertyKey, unknown>)[symbol];
    else Object.defineProperty(globalThis, symbol, previous);
  }
});

test("an accepted parent manifest installs one owner-bound child factory before the child binds", async () => {
  const symbol = Symbol.for("@gotgenes/pi-subagents:service-registry");
  const previous = Object.getOwnPropertyDescriptor(globalThis, symbol);
  const registrations: Array<{ name: string; factory: (pi: RecordValue) => void | Promise<void> }> = [];
  const activeFactories = new Map<string, { name: string; factory: (pi: RecordValue) => void | Promise<void> }>();
  let factoryDisposals = 0;
  const service = {
    registerChildExtensionV1(registration: { name: string; factory: (pi: RecordValue) => void | Promise<void> }) {
      if (activeFactories.has(registration.name)) throw new Error("duplicate child factory");
      registrations.push(registration);
      activeFactories.set(registration.name, registration);
      let active = true;
      return () => {
        if (!active) return;
        active = false;
        factoryDisposals++;
        if (activeFactories.get(registration.name) === registration) activeFactories.delete(registration.name);
      };
    },
  };
  Object.defineProperty(globalThis, symbol, { configurable: true, value: { services: new Map([["root-session", service]]) } });
  try {
    const parent = fixture();
    await bootstrap(parent);
    assert.equal(registrations.length, 1);
    assert.equal(activeFactories.size, 1);
    assert.equal(registrations[0]!.name, "mecha-rpc-managed-child-v1");

    const childCommands = new Map<string, Command>();
    const childTools = new Map<string, RecordValue>();
    const childHandlers = new Map<string, (event: RecordValue, context: RecordValue) => unknown>();
    const childPi = {
      getAllTools: () => [...childTools.values()],
      getAllCommands: () => [...childCommands.keys()],
      registerTool(tool: RecordValue) { childTools.set(String(tool.name), tool); },
      registerCommand(name: string, command: Command) { childCommands.set(name, command); },
      on(name: string, handler: (event: RecordValue, context: RecordValue) => unknown) { childHandlers.set(name, handler); },
      sendMessage() {},
    };
    await registrations[0]!.factory(childPi);
    await childHandlers.get("session_start")!({}, {
      sessionManager: { getSessionId: () => "child-session", getHeader: () => ({ parentSession: "root-session" }) },
    });
    assert.equal(childTools.size, 16);
    assert.deepEqual(new Set(childTools.keys()), new Set((manifest().tools as Array<RecordValue>).map((tool) => tool.name)));
    assert.equal(childCommands.has("goal"), false, "the child tool must not be shadowed by a legacy command");
    assert.equal(childCommands.has("mecha-bootstrap-v1"), true);

    const supplemental = supplementalManifest(manifest());
    await parent.commands.get("mecha-install-tools-v1")!.handler(encodeHiddenEnvelope({
      protocol: "mecha.tools-install/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174099",
      runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
      base_manifest_sha256: String((parent.sent.at(-1)!.message.details as RecordValue).manifest_sha256),
      desired_manifest: supplemental, manifest_sha256: manifestHash(supplemental),
      added_name: (supplemental.tools as RecordValue[]).at(-1)!.name,
    }), parent.context);
    assert.equal(registrations.length, 2, "supplemental acceptance replaces only the future-child factory");
    assert.equal(factoryDisposals, 1, "the first factory is disposed before its replacement registers");
    assert.equal(activeFactories.size, 1);

    const freshChildTools = new Map<string, RecordValue>();
    const freshChildHandlers = new Map<string, (event: RecordValue, context: RecordValue) => unknown>();
    await registrations[1]!.factory({
      getAllTools: () => [...freshChildTools.values()], getAllCommands: () => [],
      registerTool(tool: RecordValue) { freshChildTools.set(String(tool.name), tool); },
      registerCommand() {}, on(name: string, handler: (event: RecordValue, context: RecordValue) => unknown) { freshChildHandlers.set(name, handler); },
      sendMessage() {},
    });
    await freshChildHandlers.get("session_start")!({}, {
      sessionManager: { getSessionId: () => "fresh-child-session", getHeader: () => ({ parentSession: "root-session" }) },
    });
    assert.equal(freshChildTools.size, 17);

    parent.handlers.get("session_shutdown")!({}, parent.context);
    assert.equal(factoryDisposals, 2, "parent shutdown releases its remaining owner-bound factory");
    assert.equal(activeFactories.size, 0);
  } finally {
    if (previous === undefined) delete (globalThis as Record<PropertyKey, unknown>)[symbol];
    else Object.defineProperty(globalThis, symbol, previous);
  }
});

test("hidden envelopes reject duplicate keys and raw values outside the bounded encoding", () => {
  assert.throws(() => decodeHiddenEnvelope("{"));
  const duplicate = Buffer.from('{"protocol":"x","protocol":"x"}', "utf8").toString("base64url");
  assert.throws(() => decodeHiddenEnvelope(duplicate));
  assert.throws(() => decodeHiddenEnvelope("a".repeat(174_764)));
});

test("a real UUID child session gets a manager-issued context, routes its result through the parent, and remains inline-exempt", async () => {
  const symbol = Symbol.for("@gotgenes/pi-subagents:service-registry");
  const previous = Object.getOwnPropertyDescriptor(globalThis, symbol);
  const contextRef = "ctx1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
  const childSessionId = "0198d10a-9eed-7d1b-9876-123456789abc";
  const appended: Array<RecordValue> = [];
  let childContextIsLive = true;
  const service = {
    getChildContextRefV1(receivedChildSessionId: string) {
      return childContextIsLive && receivedChildSessionId === childSessionId ? contextRef : undefined;
    },
    getLifecycleSnapshotV2(ownerSessionId: string) {
      return { protocol: "mecha.children/v1", snapshot_id: "snapshot", owner_session_id: ownerSessionId, sequence: 1, runs: [
        { ...sourceChild(), context_ref: contextRef },
      ] };
    },
    async appendControlResultV1(receivedContextRef: string, payload: RecordValue) {
      appended.push({ receivedContextRef, payload });
      return { kind: "accepted", result_id: payload.result_id };
    },
  };
  Object.defineProperty(globalThis, symbol, { configurable: true, value: { services: new Map([["root-session", service]]) } });
  try {
    const root = fixture();
    await bootstrap(root);
    const child = fixture(childSessionId, "root-session");
    await bootstrap(child);
    const childContext = child.context;
    const goalTool = child.tools.get("goal");
    assert.ok(goalTool);
    const execute = goalTool!.execute as (id: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>;
    child.handlers.get("message_end")!({ message: { role: "assistant", content: [{ type: "toolCall", id: "child-goal", name: "goal", arguments: { action: "status" } }] } }, childContext);
    const queued = await execute("child-goal", { action: "status" }, undefined, undefined, childContext);
    assert.equal(queued.isError, undefined);
    const request = child.sent.at(-1)!.message.details as RecordValue;
    assert.equal(request.context_ref, contextRef);

    await child.commands.get("goal")!.handler("status", childContext);
    const legacy = child.sent.at(-1)!.message.details as RecordValue;
    assert.deepEqual(legacy.availability, { session_id: childSessionId, kind: "child" });
    assert.equal(child.handlers.get("tool_call")!({ toolCallId: "child-bash", toolName: "bash" }, childContext), undefined);

    await root.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope({
      protocol: "mecha.control/v1", result_id: "123e4567-e89b-42d3-a456-426614174012", request_id: request.request_id,
      target_session_epoch: 1, runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
      manifest_sha256: root.extension.hashManifest(manifest()), status: "ok", content: "Child complete", details: {},
    }), root.context);
    assert.equal(appended.length, 1);
    assert.equal(appended[0]!.receivedContextRef, contextRef);
    assert.equal((appended[0]!.payload as RecordValue).request_id, request.request_id);

    childContextIsLive = false;
    child.handlers.get("message_end")!({ message: { role: "assistant", content: [{ type: "toolCall", id: "stale-child", name: "goal", arguments: { action: "status" } }] } }, childContext);
    const stale = await execute("stale-child", { action: "status" }, undefined, undefined, childContext);
    assert.equal(stale.isError, true);
    assert.equal(appended.length, 1);
  } finally {
    if (previous === undefined) delete (globalThis as Record<PropertyKey, unknown>)[symbol];
    else Object.defineProperty(globalThis, symbol, previous);
  }
});

function sourceChild(): RecordValue {
  return {
    task_id: "task", run_id: "run", parent_entry_id: "entry", description: "child", model: null,
    lifecycle_state: "running", started_at: "2026-09-06T12:00:00.000Z", finished_at: null, duration_ms: null,
    compaction: { state: "idle", count: 0, started_at: null, last_outcome: null }, sequence: 1,
  };
}

function deliveryId(index: number): string {
  return `123e4567-e89b-42d3-a456-${index.toString(16).padStart(12, "0")}`;
}

function bootstrapEnvelope(current: ReturnType<typeof fixture>, delivery = deliveryId(1), desired = manifest()): RecordValue {
  return {
    protocol: "mecha.bootstrap/v1", delivery_id: delivery,
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: desired, manifest_sha256: manifestHash(desired), restored_request_ids: [],
    inline_policy: { enabled: true, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
  };
}

test("full and supplemental installation retain bounded accepted and rejected delivery receipts", async () => {
  const current = fixture();
  const full = bootstrapEnvelope(current);
  const command = current.commands.get("mecha-bootstrap-v1")!;
  await command.handler(encodeHiddenEnvelope(full), current.context);
  await command.handler(encodeHiddenEnvelope(full), current.context);
  assert.equal(current.tools.size, 16, "accepted full replay must not register a second manifest");
  assert.equal(current.sent.filter(({ message }) => message.customType === "mecha.tools.accepted.v1").length, 2);

  const supplemental = supplementalManifest(manifest());
  const supplementalEnvelope = {
    protocol: "mecha.tools-install/v1", delivery_id: deliveryId(2),
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    base_manifest_sha256: manifestHash(manifest()), desired_manifest: supplemental,
    manifest_sha256: manifestHash(supplemental), added_name: (supplemental.tools as RecordValue[]).at(-1)!.name,
  };
  const supplementalCommand = current.commands.get("mecha-install-tools-v1")!;
  await supplementalCommand.handler(encodeHiddenEnvelope(supplementalEnvelope), current.context);
  await supplementalCommand.handler(encodeHiddenEnvelope(supplementalEnvelope), current.context);
  assert.equal(current.tools.size, 17, "accepted supplemental replay must not register the new name twice");

  const invalidFull = { ...bootstrapEnvelope(current, deliveryId(3)), unexpected: true };
  await assert.rejects(async () => command.handler(encodeHiddenEnvelope(invalidFull), current.context));
  const firstRejected = current.sent.at(-1)!.message;
  await assert.rejects(async () => command.handler(encodeHiddenEnvelope(invalidFull), current.context));
  const secondRejected = current.sent.at(-1)!.message;
  assert.deepEqual(secondRejected, firstRejected, "rejected full replay must return its original receipt");

  const invalidSupplemental = { ...supplementalEnvelope, delivery_id: deliveryId(4), added_name: "wrong_name" };
  await assert.rejects(async () => supplementalCommand.handler(encodeHiddenEnvelope(invalidSupplemental), current.context));
  const rejectedSupplemental = current.sent.at(-1)!.message;
  await assert.rejects(async () => supplementalCommand.handler(encodeHiddenEnvelope(invalidSupplemental), current.context));
  assert.deepEqual(current.sent.at(-1)!.message, rejectedSupplemental, "rejected supplemental replay must return its original receipt");
  assert.equal(current.tools.size, 17);

  for (let index = 5; index < 133; index++) {
    const rejected = { ...bootstrapEnvelope(current, deliveryId(index)), unexpected: true };
    await assert.rejects(async () => command.handler(encodeHiddenEnvelope(rejected), current.context));
  }
  await assert.rejects(async () => command.handler(encodeHiddenEnvelope(full), current.context));
  assert.equal(current.tools.size, 17, "receipt eviction must not change accepted runtime state");

  current.handlers.get("session_shutdown")!({}, current.context);
  await assert.rejects(async () => command.handler(encodeHiddenEnvelope(supplementalEnvelope), current.context));
  assert.equal(current.tools.size, 17, "shutdown clears receipts but cannot revive a terminated registry");
});
