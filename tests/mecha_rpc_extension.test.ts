import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import test from "node:test";

import { BoundedChildDeltaBuffer, MAX_BUFFERED_CHILD_DELTAS } from "../pi-extensions/mecha-rpc/child-delta-buffer.ts";
import { createMechaRpcExtension } from "../pi-extensions/mecha-rpc/index.ts";
import { hashCanonicalJson, validateDesiredManifest } from "../pi-extensions/mecha-rpc/manifest.ts";
import { decodeHiddenEnvelope, encodeHiddenEnvelope } from "../pi-extensions/mecha-rpc/protocol.ts";
import managedRpcReleaseProbe from "../pi-extensions/mecha-rpc/tests/fixtures/managed-rpc-release-probe.mjs";

type RecordValue = Record<string, unknown>;
type Command = { handler: (argumentsText: string, context: RecordValue) => Promise<unknown> | unknown };

function fixture(
  sessionId = "root-session",
  parentSessionId?: string,
  installedToolNames: readonly string[] = [],
) {
  const commands = new Map<string, Command>();
  const tools = new Map<string, RecordValue>();
  for (const name of installedToolNames) tools.set(name, { name });
  const handlers = new Map<string, (event: RecordValue, context: RecordValue) => unknown>();
  const sent: Array<{ message: RecordValue; options: RecordValue | undefined }> = [];
  const transcript: RecordValue[] = [];
  const events = new Map<string, (event: RecordValue) => void>();
  let activeTools: string[] = [];
  let transcriptSequence = 0;
  const pi = {
    getAllTools: () => [...tools.values()],
    getActiveTools: () => [...activeTools],
    setActiveTools(names: string[]) { activeTools = [...names]; },
    getCommands: () => [...commands.keys()],
    registerTool(tool: RecordValue) { tools.set(String(tool.name), tool); },
    registerCommand(name: string, command: Command) { commands.set(name, command); },
    on(name: string, handler: (event: RecordValue, context: RecordValue) => unknown) { handlers.set(name, handler); },
    events: { on(name: string, listener: (event: RecordValue) => void) { events.set(name, listener); } },
    sendMessage(message: RecordValue, options?: RecordValue): unknown {
      sent.push({ message, options });
      transcript.push({
        id: `entry-${++transcriptSequence}`,
        parentId: null,
        timestamp: transcriptSequence,
        type: "custom_message",
        ...structuredClone(message),
      });
      return undefined;
    },
  };
  const context = {
    sessionManager: {
      getSessionId: () => sessionId,
      getHeader: () => parentSessionId === undefined ? {} : { parentSession: parentSessionId },
      getEntries: () => transcript,
    },
    ui: { notify() { throw new Error("managed bridge must not use UI console"); } },
  };
  const extension = createMechaRpcExtension(pi, { getRuntimeGeneration: () => "123e4567-e89b-42d3-a456-426614174000", getSessionEpoch: () => 1 });
  handlers.get("session_start")!({}, context);
  return { activeTools, commands, context, events, extension, handlers, pi, sent, tools, transcript };
}

test("managed recovery receipt is owned by the stock extension and deferred past agent_end", async () => {
  const previousTrial = process.env.MECHA_ACCEPTANCE_TRIAL_ID;
  const previousToken = process.env.MECHA_MANAGED_RECOVERY_TOKEN;
  const previousCompletionFile = process.env.MECHA_MANAGED_RECOVERY_COMPLETION_FILE;
  const previousFetch = globalThis.fetch;
  const current = fixture("managed-recovery-session");
  const requests: Array<{ url: string; body: RecordValue }> = [];
  const tempDirectory = mkdtempSync(join(tmpdir(), "mecha-rpc-recovery-"));
  const completionFile = join(tempDirectory, "completion.json");
  delete process.env.MECHA_ACCEPTANCE_TRIAL_ID;
  delete process.env.MECHA_MANAGED_RECOVERY_TOKEN;
  delete process.env.MECHA_MANAGED_RECOVERY_COMPLETION_FILE;
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), body: JSON.parse(String(init?.body)) as RecordValue });
    return new Response(null, { status: 204 });
  };
  try {
    current.handlers.get("agent_end")!({}, current.context);
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(requests, []);
    assert.equal(existsSync(completionFile), false);

    process.env.MECHA_ACCEPTANCE_TRIAL_ID = "trial-1";
    process.env.MECHA_MANAGED_RECOVERY_TOKEN = "token-1";
    process.env.MECHA_MANAGED_RECOVERY_COMPLETION_FILE = completionFile;
    current.handlers.get("agent_end")!({}, current.context);
    assert.deepEqual(requests, []);
    assert.equal(existsSync(completionFile), false, "completion signal must wait until agent_end returns");
    await new Promise((resolve) => setImmediate(resolve));

    const expected = {
      event: "managed_recovery_complete",
      trial_id: "trial-1",
      token: "token-1",
      pi_session_id: "managed-recovery-session",
    };
    assert.deepEqual(requests, []);
    assert.deepEqual(JSON.parse(readFileSync(completionFile, "utf8")), expected);
    assert.equal(statSync(completionFile).mode & 0o777, 0o600);

    process.env.MECHA_ACCEPTANCE_TRIAL_ID = "trial-2";
    process.env.MECHA_MANAGED_RECOVERY_TOKEN = "token-2";
    current.handlers.get("agent_end")!({}, current.context);
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(requests, []);
    assert.deepEqual(JSON.parse(readFileSync(completionFile, "utf8")), expected, "completion signal must not be overwritten");
    assert.deepEqual(readdirSync(tempDirectory), [basename(completionFile)], "completion signal must be written once");
  } finally {
    globalThis.fetch = previousFetch;
    if (previousTrial === undefined) delete process.env.MECHA_ACCEPTANCE_TRIAL_ID;
    else process.env.MECHA_ACCEPTANCE_TRIAL_ID = previousTrial;
    if (previousToken === undefined) delete process.env.MECHA_MANAGED_RECOVERY_TOKEN;
    else process.env.MECHA_MANAGED_RECOVERY_TOKEN = previousToken;
    if (previousCompletionFile === undefined) delete process.env.MECHA_MANAGED_RECOVERY_COMPLETION_FILE;
    else process.env.MECHA_MANAGED_RECOVERY_COMPLETION_FILE = previousCompletionFile;
    rmSync(tempDirectory, { recursive: true, force: true });
  }
});

test("managed recovery session_start does not fetch the transcript or trigger a turn", async () => {
  const symbol = Symbol.for("@gotgenes/pi-subagents:service-registry");
  const previousRegistry = Object.getOwnPropertyDescriptor(globalThis, symbol);
  const previousMode = process.env.MECHA_TEST_FIXTURE_MODE;
  const handlers = new Map<string, (event: RecordValue, context: RecordValue) => unknown>();
  const sent: Array<{ message: RecordValue; options: RecordValue | undefined }> = [];
  let transcriptReads = 0;
  let childRegistrations = 0;
  Object.defineProperty(globalThis, symbol, {
    configurable: true,
    value: { services: new Map([["managed-recovery-session", {
      registerChildExtensionV1() { childRegistrations += 1; },
    }]]) },
  });
  process.env.MECHA_TEST_FIXTURE_MODE = "managed-recovery";
  try {
    managedRpcReleaseProbe({
      on(name: string, handler: (event: RecordValue, context: RecordValue) => unknown) { handlers.set(name, handler); },
      registerCommand() {},
      sendMessage(message: RecordValue, options?: RecordValue) { sent.push({ message, options }); },
    });
    await handlers.get("session_start")!({}, {
      sessionManager: {
        getSessionId: () => "managed-recovery-session",
        getEntries() { transcriptReads += 1; return []; },
      },
    });
    assert.equal(childRegistrations, 1);
    assert.equal(transcriptReads, 0);
    assert.deepEqual(sent, []);
  } finally {
    if (previousMode === undefined) delete process.env.MECHA_TEST_FIXTURE_MODE;
    else process.env.MECHA_TEST_FIXTURE_MODE = previousMode;
    if (previousRegistry === undefined) delete (globalThis as Record<PropertyKey, unknown>)[symbol];
    else Object.defineProperty(globalThis, symbol, previousRegistry);
  }
});

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
  desired.revision = Number(desired.revision) + 1;
  return desired;
}

function bootstrap(current: ReturnType<typeof fixture>) {
  const desired = manifest();
  const payload = {
    protocol: "mecha.bootstrap/v1", delivery_id: "123e4567-e89b-42d3-a456-426614174001",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    desired_manifest: desired, manifest_sha256: current.extension.hashManifest(desired),
    restored_request_ids: [], inline_policy: { enabled: false, credits: {} },
    recall_projection: { enabled: false, stores: [], content: "" },
  };
  return current.commands.get("mecha-bootstrap-v1")!.handler(encodeHiddenEnvelope(payload), current.context);
}

function goalPolicy(action: "set" | "off", revision: number): RecordValue {
  return {
    goal: {
      action, control_id: `goal-${revision}`, target_instance_id: "goal-agent", revision,
      ...(action === "set" ? { goal: `finish revision ${revision}` } : {}),
      settings: { snoozeEnabled: true, snoozeDurationMs: 60_000, snoozeTurnLimit: null, snoozeBudgetCapMs: null, snoozeRefillYieldMs: 30_000, snoozeRefillElapsedMs: 60_000 },
    },
  };
}

test("child delta delivery retries in order and bounds overflow into a recoverable gap", async () => {
  const delivered: number[] = [];
  let firstAttempts = 0;
  const buffer = new BoundedChildDeltaBuffer(
    async (wire) => {
      const sequence = wire.sequence;
      if (typeof sequence !== "number") throw new Error("test sequence is invalid");
      if (sequence === 1 && firstAttempts++ < 2) throw new Error("transient append failure");
      delivered.push(sequence);
    },
    () => true,
  );
  buffer.enqueue("owner", { sequence: 1 });
  buffer.enqueue("owner", { sequence: 2 });
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(delivered, [1, 2]);

  const overflow: number[] = [];
  const blocked = Promise.withResolvers<void>();
  const bounded = new BoundedChildDeltaBuffer(
    async (wire) => {
      const sequence = wire.sequence;
      if (typeof sequence !== "number") throw new Error("test sequence is invalid");
      overflow.push(sequence);
      if (sequence === 1) await blocked.promise;
    },
    () => true,
  );
  for (let sequence = 1; sequence <= MAX_BUFFERED_CHILD_DELTAS + 2; sequence++) {
    bounded.enqueue("owner", { sequence });
  }
  await new Promise((resolve) => setTimeout(resolve, 0));
  blocked.resolve();
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(overflow[0], 1);
  assert.equal(overflow.at(-1), MAX_BUFFERED_CHILD_DELTAS + 2);
  assert.equal(overflow.length, MAX_BUFFERED_CHILD_DELTAS, "overflow replaces queued newest input and leaves one recovery gap");
});

test("the owner completion hook waits for the exact terminal delta before publishing completion", async () => {
  const symbol = Symbol.for("@gotgenes/pi-subagents:service-registry");
  const previous = Object.getOwnPropertyDescriptor(globalThis, symbol);
  let completionHook: ((record: { id: string }) => void | Promise<void>) | undefined;
  const service = {
    registerBeforeCompletionHookV1(hook: (record: { id: string }) => void | Promise<void>) {
      completionHook = hook;
      return () => { completionHook = undefined; };
    },
  };
  Object.defineProperty(globalThis, symbol, { configurable: true, value: { services: new Map([["root-session", service]]) } });
  try {
    const current = fixture();
    const releaseAppend = Promise.withResolvers<void>();
    const originalSend = current.pi.sendMessage;
    current.pi.sendMessage = (message: RecordValue, options?: RecordValue) => {
      current.sent.push({ message, options });
      if (message.customType === "mecha.child.delta.v1") return releaseAppend.promise;
      return originalSend(message, options);
    };
    const terminal = {
      protocol: "mecha.children/v1", owner_session_id: "root-session", sequence: 9,
      task_id: "task-1", run_id: "run-1", parent_entry_id: "entry-1", context_ref: null,
      changes: { lifecycle_state: "completed" },
    };
    current.events.get("subagents:lifecycle-v2")!(terminal);
    assert.ok(completionHook);
    const persisted: string[] = [];
    const publishCompletion = async (): Promise<void> => {
      await completionHook!({ id: "task-1" });
      persisted.push("subagents:record");
      current.sent.push({ message: { customType: "subagents:record" }, options: undefined });
    };
    const completion = publishCompletion();
    await new Promise<void>((resolve) => setImmediate(resolve));
    assert.deepEqual(persisted, []);
    assert.equal(current.sent.filter(({ message }) => message.customType === "mecha.child.delta.v1").length, 1);

    releaseAppend.resolve();
    await completion;
    assert.deepEqual(persisted, ["subagents:record"]);
    const terminalIndex = current.sent.findIndex(({ message }) => message.customType === "mecha.child.delta.v1");
    const completionIndex = current.sent.findIndex(({ message }) => message.customType === "subagents:record");
    assert.equal(completionIndex, terminalIndex + 1);
    assert.equal(current.sent.filter(({ message }) => message.customType === "subagents:record").length, 1);
  } finally {
    if (previous === undefined) delete (globalThis as Record<PropertyKey, unknown>)[symbol];
    else Object.defineProperty(globalThis, symbol, previous);
  }
});

test("terminal completion is suppressed truthfully when the delta append fails or closes", async () => {
  const symbol = Symbol.for("@gotgenes/pi-subagents:service-registry");
  const previous = Object.getOwnPropertyDescriptor(globalThis, symbol);
  let completionHook: ((record: { id: string }) => void | Promise<void>) | undefined;
  const service = {
    registerBeforeCompletionHookV1(hook: (record: { id: string }) => void | Promise<void>) {
      completionHook = hook;
      return () => { completionHook = undefined; };
    },
  };
  Object.defineProperty(globalThis, symbol, { configurable: true, value: { services: new Map([["root-session", service]]) } });
  try {
    const current = fixture();
    const originalSend = current.pi.sendMessage;
    current.pi.sendMessage = (message: RecordValue, options?: RecordValue) => {
      if (message.customType === "mecha.child.delta.v1") return Promise.reject(new Error("append rejected"));
      return originalSend(message, options);
    };
    current.events.get("subagents:lifecycle-v2")!({
      protocol: "mecha.children/v1", owner_session_id: "root-session", sequence: 10,
      task_id: "task-failed", run_id: "run-failed", parent_entry_id: "entry-failed", context_ref: null,
      changes: { lifecycle_state: "error" },
    });
    await assert.rejects(() => completionHook!({ id: "task-failed" }), /not durably appended/);
    assert.equal(current.sent.some(({ message }) => message.customType === "subagents:record"), false);

    const releaseAppend = Promise.withResolvers<void>();
    current.pi.sendMessage = (message: RecordValue, options?: RecordValue) => {
      if (message.customType === "mecha.child.delta.v1") return releaseAppend.promise;
      return originalSend(message, options);
    };
    current.events.get("subagents:lifecycle-v2")!({
      protocol: "mecha.children/v1", owner_session_id: "root-session", sequence: 11,
      task_id: "task-closed", run_id: "run-closed", parent_entry_id: "entry-closed", context_ref: null,
      changes: { lifecycle_state: "stopped" },
    });
    const closing = completionHook!({ id: "task-closed" });
    current.handlers.get("session_shutdown")!({}, current.context);
    await assert.rejects(() => closing, /not durably appended/);
    await assert.rejects(() => completionHook!({ id: "task-closed" }), /session closed/);
  } finally {
    if (previous === undefined) delete (globalThis as Record<PropertyKey, unknown>)[symbol];
    else Object.defineProperty(globalThis, symbol, previous);
  }
});

test("clearing an old in-flight child delivery restarts draining for the replacement owner", async () => {
  const firstStarted = Promise.withResolvers<void>();
  const releaseFirst = Promise.withResolvers<void>();
  const replacementDelivered = Promise.withResolvers<void>();
  let currentOwner = "old-owner";
  const delivered: number[] = [];
  const buffer = new BoundedChildDeltaBuffer(
    async (wire) => {
      const sequence = wire.sequence;
      if (typeof sequence !== "number") throw new Error("test sequence is invalid");
      delivered.push(sequence);
      if (sequence === 1) {
        firstStarted.resolve();
        await releaseFirst.promise;
      }
      if (sequence === 2) replacementDelivered.resolve();
    },
    (ownerSessionID) => ownerSessionID === currentOwner,
  );

  buffer.enqueue("old-owner", { sequence: 1 });
  await firstStarted.promise;
  currentOwner = "replacement-owner";
  buffer.clear();
  buffer.enqueue("replacement-owner", { sequence: 2 });
  releaseFirst.resolve();

  await Promise.race([
    replacementDelivered.promise,
    new Promise<void>((_resolve, reject) => setTimeout(() => reject(new Error("replacement delta was stranded")), 100)),
  ]);
  assert.deepEqual(delivered, [1, 2]);
});

test("the extension preflights all 18 command names and registers no Mailbox bridge", () => {
  const current = fixture();
  assert.deepEqual([...current.commands.keys()], [
    "mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-control-result-reconcile-v1", "mecha-mailbox-deliver-v1", "mecha-mailbox-reconcile-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1", "mecha-post-compaction-notification-v1",
    "goal", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply",
  ]);
  assert.equal(current.commands.has("mecha-mailbox"), false);
  assert.equal(current.tools.size, 0);
});

test("a managed goal policy acknowledges the validated policy revision", async () => {
  const current = fixture();
  await current.commands.get("mecha-policy-apply-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.policy/v1", runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    manifest_sha256: current.extension.hashManifest(manifest()),
    policy: {
      goal: {
        action: "set", control_id: "goal-control", target_instance_id: "goal-agent", revision: 1, goal: "finish the migration",
        settings: { snoozeEnabled: true, snoozeDurationMs: 60_000, snoozeTurnLimit: null, snoozeBudgetCapMs: null, snoozeRefillYieldMs: 30_000, snoozeRefillElapsedMs: 60_000 },
      },
    },
  }), current.context);
  const receipt = current.sent.find(({ message }) => message.customType === "mecha.policy.applied.v1");
  assert.deepEqual(receipt?.message.details, {
    protocol: "mecha.policy/v1", runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    applied: {
      inline_enabled: false, inline_credits: {}, recall_content: "", settled_enabled: false,
      compaction_deferred: false, goal_revision: 1,
    },
  });
});

test("a managed goal policy rejects malformed goal settings before it acknowledges the revision", async () => {
  const current = fixture();
  await assert.rejects(() => current.commands.get("mecha-policy-apply-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.policy/v1", runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    manifest_sha256: current.extension.hashManifest(manifest()),
    policy: { goal: { action: "set", control_id: "goal-control", target_instance_id: "goal-agent", revision: 1, goal: "finish the migration", settings: {} } },
  }), current.context));
  assert.equal(current.sent.some(({ message }) => message.customType === "mecha.policy.applied.v1"), false);
});

test("every legacy command preserves raw input and root or child availability in one typed request", async () => {
  const current = fixture();
  for (const [index, name] of [...current.commands.keys()].slice(9).entries()) {
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
  assert.equal(current.sent.length, 8);
});

test("accepted receipt reports the runtime active tools and manifest revision", async () => {
  const current = fixture();
  await bootstrap(current);
  const receipt = current.sent.at(-1)!;
  assert.equal(receipt.message.customType, "mecha.tools.accepted.v1");
  const details = receipt.message.details as RecordValue;
  assert.equal(details.manifest_revision, 2);
  assert.deepEqual(details.active_tools, [...current.pi.getActiveTools!()].sort());
  assert.equal((details.active_tools as unknown[]).length, 16);
});

test("managed active selection exposes Pi coding tools without turning them into manifest tools", async () => {
  const previous = process.env.MECHA_MANAGED_ACTIVE_ONLY;
  process.env.MECHA_MANAGED_ACTIVE_ONLY = "1";
  try {
    const codingTools = ["read", "bash", "edit", "write"];
    const fixedSubagentTools = ["subagent", "get_subagent_result", "steer_subagent"];
    const current = fixture("root-session", undefined, [...codingTools, ...fixedSubagentTools, "ambient_extension_tool"]);
    await bootstrap(current);

    const manifestNames = (manifest().tools as RecordValue[]).map((tool) => String(tool.name));
    assert.deepEqual(new Set(current.pi.getActiveTools!()), new Set([...codingTools, ...fixedSubagentTools, ...manifestNames]));
    assert.equal(current.pi.getActiveTools!().includes("ambient_extension_tool"), false);
    assert.equal(current.tools.has("read"), true);
    assert.equal((manifest().tools as RecordValue[]).some((tool) => tool.name === "read"), false);

    const hook = current.handlers.get("tool_call")!;
    assert.equal(hook({ toolCallId: "read", toolName: "read" }, current.context), undefined);
    for (const name of ["bash", "edit", "write"] as const) {
      assert.deepEqual(hook({ toolCallId: `${name}-blocked`, toolName: name }, current.context), {
        block: true,
        reason: "This managed runtime requires an inline-tool allowance.",
      });
      await current.commands.get("mecha-policy-apply-v1")!.handler(encodeHiddenEnvelope({
        protocol: "mecha.policy/v1", runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
        manifest_sha256: current.extension.hashManifest(manifest()), policy: { inline: { enabled: false, credits: { [name]: 1 } } },
      }), current.context);
      assert.equal(hook({ toolCallId: `${name}-allowed`, toolName: name }, current.context), undefined);
      assert.deepEqual(hook({ toolCallId: `${name}-exhausted`, toolName: name }, current.context), {
        block: true,
        reason: "This managed runtime requires an inline-tool allowance.",
      });
    }
  } finally {
    if (previous === undefined) delete process.env.MECHA_MANAGED_ACTIVE_ONLY;
    else process.env.MECHA_MANAGED_ACTIVE_ONLY = previous;
  }
});

test("bootstrap is atomic before model tools exist and a raw coercive tool call fails before control traffic", async () => {
  const current = fixture();
  await bootstrap(current);
  assert.equal(current.tools.size, 16);
  assert.equal(current.tools.has("mecha_worker"), false);
  assert.equal(current.tools.has("mecha_mailbox_send"), true);
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
  const appendResponse = await current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.control/v1", result_id: resultId, request_id: queuedRequest.request_id,
    target_session_epoch: 1, runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    manifest_sha256: current.extension.hashManifest(manifest()), status: "ok", content: "Complete", details: {},
  }), current.context);
  assert.deepEqual(appendResponse, { kind: "accepted", result_id: resultId });
  const completion = current.sent.at(-1)!;
  assert.equal(completion.message.customType, "mecha.control.result.v1");
  assert.equal(completion.message.display, false);
  assert.deepEqual(completion.options, { triggerTurn: false });
  assert.deepEqual(Object.keys(completion.message.details as RecordValue).sort(), ["details", "manifest_sha256", "protocol", "request_id", "result_id", "runtime_generation", "status", "target_session_epoch"]);
  assert.equal(JSON.stringify({ message: completion.message, options: completion.options }).includes("mcr1_"), false);
  await assert.rejects(async () => current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.control/v1", result_id: "123e4567-e89b-42d3-a456-426614174003", request_id: queuedRequest.request_id,
    target_session_epoch: 1, runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    manifest_sha256: current.extension.hashManifest(manifest()), status: "ok", content: "duplicate", details: {},
  }), current.context));
});

test("stock control result append returns void and reconciliation proves exactly one transcript entry", async () => {
  const current = fixture();
  const transcript: RecordValue[] = [];
  let entrySequence = 0;
  let transcriptReads = 0;
  current.pi.sendMessage = (message: RecordValue, options?: RecordValue) => {
    current.sent.push({ message, options });
    transcript.push({
      id: `entry-${++entrySequence}`,
      parentId: null,
      timestamp: entrySequence,
      type: "custom_message",
      ...structuredClone(message),
    });
    return undefined;
  };
  const context = {
    ...current.context,
    sessionManager: {
      ...current.context.sessionManager,
      getEntries() {
        transcriptReads += 1;
        return transcript.slice(-64);
      },
    },
  };

  await bootstrap({ ...current, context });
  const goal = current.tools.get("goal");
  assert.ok(goal);
  const execute = goal.execute as (id: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>;
  current.handlers.get("message_end")!({ message: { role: "assistant", content: [{ type: "toolCall", id: "stock-result", name: "goal", arguments: { action: "status" } }] } }, context);
  await execute("stock-result", { action: "status" }, undefined, undefined, context);
  const request = current.sent.at(-1)!.message.details as RecordValue;
  const payload = {
    protocol: "mecha.control/v1",
    result_id: "123e4567-e89b-42d3-a456-426614174004",
    request_id: request.request_id,
    target_session_epoch: 1,
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    manifest_sha256: current.extension.hashManifest(manifest()),
    status: "ok",
    content: "Persisted by stock Pi",
    details: {},
  };
  const payloadHash = hashCanonicalJson(payload);
  const appendCommand = current.commands.get("mecha-control-result-v1")!;

  await appendCommand.handler(encodeHiddenEnvelope(payload), context);
  const matchingResults = () => transcript.filter((entry) => entry.type === "custom_message"
    && entry.customType === "mecha.control.result.v1"
    && (entry.details as RecordValue | undefined)?.request_id === payload.request_id
    && (entry.details as RecordValue | undefined)?.result_id === payload.result_id);
  assert.equal(matchingResults().length, 1);
  const appendWire = current.sent.findLast(({ message }) => message.customType === "mecha.control.result.v1");
  assert.ok(appendWire);
  assert.deepEqual(appendWire.options, { triggerTurn: false });

  const reconcileCommand = current.commands.get("mecha-control-result-reconcile-v1")!;
  const receipt = await reconcileCommand.handler(encodeHiddenEnvelope({
    protocol: "mecha.control-result-reconcile/v1",
    request_id: payload.request_id,
    result_id: payload.result_id,
    payload_hash: payloadHash,
    runtime_generation: payload.runtime_generation,
    session_epoch: payload.target_session_epoch,
    manifest_sha256: payload.manifest_sha256,
  }), context) as RecordValue;
  assert.equal(receipt.protocol, "mecha.control.result.persisted.v1");
  assert.equal(receipt.request_id, payload.request_id);
  assert.equal(receipt.result_id, payload.result_id);
  assert.equal(receipt.payload_hash, payloadHash);
  assert.equal(receipt.runtime_generation, payload.runtime_generation);
  assert.equal(receipt.session_epoch, payload.target_session_epoch);
  assert.equal(receipt.manifest_sha256, payload.manifest_sha256);
  assert.match(String(receipt.receipt_id), /^mcrr1_[0-9a-f]{64}$/i);

  const appendedBeforeReplay = matchingResults().length;
  await appendCommand.handler(encodeHiddenEnvelope(payload), context);
  assert.equal(matchingResults().length, appendedBeforeReplay);
  assert.ok(transcriptReads >= 3);
  assert.doesNotMatch(JSON.stringify(current.sent), /"(?:get_entry|get_entries)"/);
});

test("a root result accepts an exact transcript replay without appending again", async () => {
  const current = fixture();
  await bootstrap(current);
  const goal = current.tools.get("goal");
  assert.ok(goal);
  const execute = goal.execute as (id: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>;
  current.handlers.get("message_end")!({ message: { role: "assistant", content: [{ type: "toolCall", id: "direct-seam", name: "goal", arguments: { action: "status" } }] } }, current.context);
  await execute("direct-seam", { action: "status" }, undefined, undefined, current.context);
  const request = current.sent.at(-1)!.message.details as RecordValue;
  const payload = {
    protocol: "mecha.control/v1", result_id: "123e4567-e89b-42d3-a456-426614174004", request_id: request.request_id,
    target_session_epoch: 1, runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    manifest_sha256: current.extension.hashManifest(manifest()), status: "ok", content: "Already persisted", details: {},
  };
  current.transcript.push({
    id: "preexisting-result",
    parentId: null,
    timestamp: current.transcript.length + 1,
    type: "custom_message",
    customType: "mecha.control.result.v1",
    display: false,
    content: payload.content,
    details: Object.fromEntries(Object.entries(payload).filter(([key]) => key !== "content")),
  });
  const resultCount = () => current.sent.filter(({ message }) => message.customType === "mecha.control.result.v1").length;
  const beforeReplay = resultCount();
  const response = await current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope(payload), current.context);
  assert.deepEqual(response, { kind: "already_present", result_id: payload.result_id });
  assert.equal(resultCount(), beforeReplay);
  const replay = await current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope(payload), current.context);
  assert.deepEqual(replay, { kind: "already_present", result_id: payload.result_id });
  assert.equal(resultCount(), beforeReplay);
});

test("root registration exists before emission settles and send failure rolls it back", async () => {
  const current = fixture();
  await bootstrap(current);
  const goal = current.tools.get("goal");
  assert.ok(goal);
  const execute = goal.execute as (id: string, input: RecordValue, signal: undefined, update: undefined, context: RecordValue) => Promise<RecordValue>;
  let settle: (() => void) | undefined;
  const originalSend = current.pi.sendMessage;
  current.pi.sendMessage = (message: RecordValue, options?: RecordValue) => {
    current.sent.push({ message, options });
    if (message.customType === "mecha.control.request.v1") return new Promise<{ kind: "accepted"; entryId: string }>((resolve) => { settle = () => resolve({ kind: "accepted", entryId: "" }); });
    return originalSend(message, options);
  };
  current.handlers.get("message_end")!({ message: { role: "assistant", content: [{ type: "toolCall", id: "early-result", name: "goal", arguments: { action: "status" } }] } }, current.context);
  const pending = execute("early-result", { action: "status" }, undefined, undefined, current.context);
  const request = current.sent.at(-1)!.message.details as RecordValue;
  await current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.control/v1", result_id: "123e4567-e89b-42d3-a456-426614174005", request_id: request.request_id,
    target_session_epoch: 1, runtime_generation: "123e4567-e89b-42d3-a456-426614174000", manifest_sha256: current.extension.hashManifest(manifest()), status: "ok", content: "early", details: {},
  }), current.context);
  assert.ok(settle);
  settle();
  assert.equal((await pending).isError, undefined);

  current.pi.sendMessage = (message: RecordValue, options?: RecordValue) => {
    current.sent.push({ message, options });
    if (message.customType === "mecha.control.request.v1") return Promise.reject(new Error("send failed"));
    return originalSend(message, options);
  };
  current.handlers.get("message_end")!({ message: { role: "assistant", content: [{ type: "toolCall", id: "failed-send", name: "goal", arguments: { action: "status" } }] } }, current.context);
  const failed = await execute("failed-send", { action: "status" }, undefined, undefined, current.context);
  assert.equal(failed.isError, undefined, "root dispatch acknowledges before an asynchronous send failure");
  const failedRequest = current.sent.at(-1)!.message.details as RecordValue;
  await new Promise<void>((resolve) => setImmediate(resolve));
  await assert.rejects(async () => current.commands.get("mecha-control-result-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.control/v1", result_id: "123e4567-e89b-42d3-a456-426614174006", request_id: failedRequest.request_id,
    target_session_epoch: 1, runtime_generation: "123e4567-e89b-42d3-a456-426614174000", manifest_sha256: current.extension.hashManifest(manifest()), status: "ok", content: "failed", details: {},
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

test("a native post-compaction command emits a visible Pi handoff only after its fence validates", async () => {
  const current = fixture();
  await bootstrap(current);
  await current.commands.get("mecha-post-compaction-notification-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.post-compaction/v1",
    notification: "COMPACTION-CERT-DONE",
    runtime_generation: "123e4567-e89b-42d3-a456-426614174000",
    session_epoch: 1,
    manifest_sha256: current.extension.hashManifest(manifest()),
  }), current.context);
  const handoff = current.sent.at(-1);
  assert.ok(handoff);
  assert.equal(handoff!.message.customType, "mecha.post_compaction.v1");
  assert.equal(handoff!.message.display, true);
  assert.equal(handoff!.message.content, "COMPACTION-CERT-DONE");
  assert.deepEqual(handoff!.options, { deliverAs: "followUp", triggerTurn: true });
});

test("four non-manifest hidden commands reject unknown envelope fields before side effects", async () => {
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
  await assert.rejects(async () => current.commands.get("mecha-post-compaction-notification-v1")!.handler(encodeHiddenEnvelope({
    protocol: "mecha.post-compaction/v1", notification: "handoff", ...shared,
  }), current.context));
  assert.equal(current.sent.filter(({ message }) => message.customType === "mecha.mailbox.appended.v1" || message.customType === "mecha.policy.applied.v1" || message.customType === "mecha.child.snapshot.v1" || message.customType === "mecha.post_compaction.v1").length, 0);
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
    manifest_sha256: current.extension.hashManifest(manifest()), policy: { inline: { enabled: false, credits: { bash: 1 } } },
  }), current.context);
  assert.equal(hook({ toolCallId: "bash-two", toolName: "bash" }, current.context), undefined);
  const exhausted = hook({ toolCallId: "bash-three", toolName: "bash" }, current.context) as RecordValue;
  assert.equal(exhausted.block, true);
});

test("a root bootstrap remains usable when the public service lacks optional child registration", async () => {
  const symbol = Symbol.for("@gotgenes/pi-subagents:service-registry");
  const previous = Object.getOwnPropertyDescriptor(globalThis, symbol);
  const service = { getLifecycleSnapshots: () => [] };
  Object.defineProperty(globalThis, symbol, { configurable: true, value: { services: new Map([["root-session", service]]) } });
  try {
    const current = fixture();
    await bootstrap(current);
    const snapshot = await current.commands.get("mecha-child-snapshot-v1")!.handler(encodeHiddenEnvelope({
      protocol: "mecha.child-snapshot/v1", runtime_generation: "123e4567-e89b-42d3-a456-426614174000", session_epoch: 1,
    }), {});
    assert.deepEqual(snapshot, {
      protocol: "mecha.children/v1", snapshot_id: "initial-empty", owner_session_id: "root-session", sequence: 0, runs: [],
    });
    const receipt = current.sent.at(-1)?.message;
    assert.equal(receipt?.customType, "mecha.child.snapshot.v1");
    assert.deepEqual(receipt?.details, snapshot);
  } finally {
    if (previous === undefined) delete (globalThis as Record<PropertyKey, unknown>)[symbol];
    else Object.defineProperty(globalThis, symbol, previous);
  }
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
    inline_policy: { enabled: false, credits: {} }, recall_projection: { enabled: false, stores: [], content: "" },
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
