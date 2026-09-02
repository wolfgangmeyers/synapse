import assert from "node:assert/strict";
import { chmodSync, existsSync, mkdirSync, readdirSync, readFileSync, statSync, utimesSync, writeFileSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import test from "node:test";

import {
  appendMalformedToolCallDiagnostic,
  appendMemoryCaptureDiagnostic,
  appendOrphanToolResultDiagnostic,
  createDiagnosticJournal,
  createExtensionTrace,
  createNativeSubagentTrace,
  quarantineMalformedToolCalls,
  quarantineOrphanToolResults,
  sanitizeContextHistory,
} from "../pi-extensions/mecha-messaging/diagnostics-history.ts";

async function temporaryDirectory(prefix: string): Promise<string> {
  return mkdtemp(join(tmpdir(), prefix));
}

function mode(path: string): number {
  return statSync(path).mode & 0o777;
}

function recordsAt(path: string): Array<Record<string, unknown>> {
  return readFileSync(path, "utf8").trim().split("\n").filter(Boolean).map((line) => JSON.parse(line) as Record<string, unknown>);
}

test("malformed tool calls remove only invalid aliases and preserve block order", () => {
  const overlong = "x".repeat(65);
  const validCall = { type: "toolCall", id: "valid", name: "valid_name" };
  const validAlias = { type: "tool_use", id: "alias", name: "valid-name" };
  const messages = [
    {
      role: "assistant",
      content: [
        null,
        "ordinary text",
        validCall,
        { type: "toolCall", id: "empty", name: "" },
        validAlias,
        { type: "tool_use", id: "long", name: overlong },
        { type: "toolCall", id: "space", name: "has space" },
        { type: "toolCall", id: "punctuation", name: "has.dot" },
        { type: "toolCall", id: 42, name: "number-id" },
        { type: "toolCall", name: 42 },
      ],
    },
    { role: "user", content: "malformed non-array content" },
    { role: "assistant", content: "also non-array" },
    { role: "assistant" },
  ];

  const result = quarantineMalformedToolCalls(messages);

  assert.deepEqual(result.messages, [
    { role: "assistant", content: [null, "ordinary text", validCall, validAlias, { type: "toolCall", id: 42, name: "number-id" }] },
    messages[1],
    messages[2],
    messages[3],
  ]);
  assert.deepEqual(result.malformed, [
    { messageIndex: 0, blockIndex: 3, toolName: "", toolNameLength: 0, toolCallId: "empty" },
    { messageIndex: 0, blockIndex: 5, toolName: overlong, toolNameLength: 65, toolCallId: "long" },
    { messageIndex: 0, blockIndex: 6, toolName: "has space", toolNameLength: 9, toolCallId: "space" },
    { messageIndex: 0, blockIndex: 7, toolName: "has.dot", toolNameLength: 7, toolCallId: "punctuation" },
    { messageIndex: 0, blockIndex: 9, toolName: undefined, toolNameLength: undefined, toolCallId: undefined },
  ]);
});

test("orphan filtering preserves ownership, missing IDs, and exact history order", () => {
  const messages = [
    { role: "toolResult", toolCallId: "before|provider", toolName: "before", content: [] },
    {
      role: "assistant",
      stopReason: "stop",
      content: [
        { type: "toolCall", id: "plain", name: "plain" },
        { type: "toolCall", id: "owned|first|second", name: "owned" },
        { type: "toolCall", id: "|call", name: "empty-provider-id" },
        { type: "toolCall", id: "", name: "raw-empty-id" },
      ],
    },
    { role: "toolResult", toolCallId: "plain|ignored-suffix", toolName: "plain-result", content: [] },
    { role: "toolResult", toolCallId: "owned|other", toolName: "owned-result", content: [] },
    { role: "toolResult", toolCallId: "|suffix", toolName: "empty-provider-result", content: [] },
    { role: "toolResult", toolCallId: "", toolName: "raw-empty-result", content: [] },
    { role: "toolResult", toolCallId: undefined, toolName: "missing-id", content: [] },
    { role: "toolResult", toolCallId: "plain|duplicate", toolName: "duplicate", content: [] },
    { role: "user", content: [{ type: "text", text: "after results" }] },
  ];

  const result = quarantineOrphanToolResults(messages);

  assert.deepEqual(result.messages, [messages[1], messages[2], messages[3], messages[4], messages[5], messages[6], messages[7], messages[8]]);
  assert.deepEqual(result.orphaned, [{
    messageIndex: 0,
    toolCallId: "before|provider",
    providerToolCallId: "before",
    toolName: "before",
    previousRole: undefined,
    nextRole: "assistant",
    nearestAssistantIndex: undefined,
    nearestAssistantStopReason: undefined,
    nearestAssistantToolCallIds: [],
    matchingToolCallLater: false,
  }]);
});

test("a raw |suffix orphan keeps its empty normalized provider ID", () => {
  const result = quarantineOrphanToolResults([{ role: "toolResult", toolCallId: "|suffix", toolName: "bash", content: [] }]);

  assert.deepEqual(result.messages, []);
  assert.equal(result.orphaned[0]?.providerToolCallId, "");
});

test("errored and aborted assistants do not own results, and tool_use stays diagnostic-only", () => {
  const errored = { role: "assistant", stopReason: "error", content: [{ type: "toolCall", id: "error-id", name: "bash" }] };
  const aborted = { role: "assistant", stopReason: "aborted", content: [{ type: "toolCall", id: "aborted-id", name: "bash" }] };
  const alias = { role: "assistant", stopReason: "stop", content: [{ type: "tool_use", id: "alias-id", name: "bash" }] };
  const result = quarantineOrphanToolResults([
    errored,
    { role: "toolResult", toolCallId: "error-id", toolName: "error-result", content: [] },
    aborted,
    { role: "toolResult", toolCallId: "aborted-id", toolName: "aborted-result", content: [] },
    alias,
    { role: "toolResult", toolCallId: "alias-id", toolName: "alias-result", content: [] },
  ]);

  assert.deepEqual(result.messages, [errored, aborted, alias]);
  assert.deepEqual(result.orphaned.map(({ toolCallId, nearestAssistantIndex, nearestAssistantStopReason, nearestAssistantToolCallIds }) => ({
    toolCallId,
    nearestAssistantIndex,
    nearestAssistantStopReason,
    nearestAssistantToolCallIds,
  })), [
    { toolCallId: "error-id", nearestAssistantIndex: 0, nearestAssistantStopReason: "error", nearestAssistantToolCallIds: ["error-id"] },
    { toolCallId: "aborted-id", nearestAssistantIndex: 2, nearestAssistantStopReason: "aborted", nearestAssistantToolCallIds: ["aborted-id"] },
    { toolCallId: "alias-id", nearestAssistantIndex: 4, nearestAssistantStopReason: "stop", nearestAssistantToolCallIds: [] },
  ]);
});

test("a later matching call does not rescue an orphan result", () => {
  const later = { role: "assistant", content: [{ type: "toolCall", id: "later|provider", name: "bash" }] };
  const result = quarantineOrphanToolResults([
    { role: "toolResult", toolCallId: "later|other", toolName: "bash", content: [] },
    later,
  ]);

  assert.deepEqual(result.messages, [later]);
  assert.equal(result.orphaned[0]?.matchingToolCallLater, true);
});

test("context sanitation runs malformed removal before orphan removal", () => {
  const valid = { type: "toolCall", id: "valid", name: "bash" };
  const malformed = { type: "toolCall", id: "bad", name: "bad name" };
  const result = sanitizeContextHistory({
    messages: [
      { role: "assistant", content: [valid, malformed] },
      { role: "toolResult", toolCallId: "bad", toolName: "bash", content: [] },
      { role: "toolResult", toolCallId: "valid", toolName: "bash", content: [] },
    ],
  });

  assert.deepEqual(result.messages, [
    { role: "assistant", content: [valid] },
    { role: "toolResult", toolCallId: "valid", toolName: "bash", content: [] },
  ]);
  assert.deepEqual(result.messagesBeforeOrphanQuarantine, [
    { role: "assistant", content: [valid] },
    { role: "toolResult", toolCallId: "bad", toolName: "bash", content: [] },
    { role: "toolResult", toolCallId: "valid", toolName: "bash", content: [] },
  ]);
  assert.deepEqual(result.malformed, [{ messageIndex: 0, blockIndex: 1, toolName: "bad name", toolNameLength: 8, toolCallId: "bad" }]);
  assert.equal(result.orphaned.length, 1);
  assert.equal(result.orphaned[0]?.messageIndex, 1);
  assert.equal(result.orphaned[0]?.nearestAssistantIndex, 0);
});

test("clean context sanitation keeps all values and returns both incident lists", () => {
  const messages = [{ role: "user", content: [{ type: "text", text: "hello" }] }];
  const result = sanitizeContextHistory({ messages });

  assert.deepEqual(result, { messages, messagesBeforeOrphanQuarantine: messages, malformed: [], orphaned: [] });
  assert.deepEqual(sanitizeContextHistory({}), { messages: [], messagesBeforeOrphanQuarantine: [], malformed: [], orphaned: [] });
});

test("memory diagnostics are allowlisted, bounded, redacted, and mode 0600", async () => {
  const directory = await temporaryDirectory("mecha-memory-diagnostic-contract-");
  const path = join(directory, "nested", "memory.jsonl");
  try {
    appendMemoryCaptureDiagnostic({
      timestamp: "2026-01-01T00:00:00.000Z",
      trigger: "compaction",
      status: "applied",
      reason: "memory-apply",
      records: 2,
      source: "secret source text",
      prompt: "secret prompt",
      error: "secret error",
    }, { path });
    assert.deepEqual(recordsAt(path), [{
      timestamp: "2026-01-01T00:00:00.000Z",
      trigger: "compaction",
      status: "applied",
      reason: "memory-apply",
      records: 2,
    }]);
    assert.equal(mode(path), 0o600);

    appendMemoryCaptureDiagnostic({ trigger: "other", status: "other", reason: "not a slug", records: Infinity }, { path });
    const fallback = recordsAt(path).at(-1);
    assert.deepEqual(fallback, { timestamp: fallback?.timestamp, trigger: "unknown", status: "failed", reason: "capture-failed" });

    for (let index = 0; index < 20; index += 1) {
      appendMemoryCaptureDiagnostic({ timestamp: `2026-01-01T00:00:${String(index).padStart(2, "0")}.000Z`, trigger: "shutdown", status: "noop", reason: "bounded" }, { path, maxBytes: 260, keepBytes: 130 });
    }
    assert.ok(statSync(path).size <= 260);
    assert.equal(statSync(`${path}.1`, { throwIfNoEntry: false })?.isFile(), undefined);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("diagnostic default paths use configured environment paths and HOME fallbacks", async () => {
  const directory = await temporaryDirectory("mecha-diagnostic-default-paths-");
  const previousMemoryPath = process.env.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH;
  const previousOrphanPath = process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH;
  const previousHome = process.env.HOME;
  try {
    const memoryPath = join(directory, "configured-memory.jsonl");
    const orphanPath = join(directory, "configured-orphan.jsonl");
    process.env.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH = memoryPath;
    process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH = orphanPath;
    appendMemoryCaptureDiagnostic({ timestamp: "memory" });
    appendOrphanToolResultDiagnostic({ orphans: [] });
    assert.equal(statSync(memoryPath).isFile(), true);
    assert.equal(statSync(orphanPath).isFile(), true);

    delete process.env.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH;
    delete process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH;
    process.env.HOME = directory;
    appendMemoryCaptureDiagnostic({ timestamp: "memory-home" });
    appendOrphanToolResultDiagnostic({ orphans: [] });
    assert.equal(statSync(join(directory, ".mecha-developer", "logs", "memory-capture.jsonl")).isFile(), true);
    assert.equal(statSync(join(directory, ".mecha-developer", "logs", "orphan-tool-results.jsonl")).isFile(), true);

    process.env.HOME = "";
    assert.doesNotThrow(() => appendMemoryCaptureDiagnostic({ timestamp: "no-home" }));
    assert.doesNotThrow(() => appendOrphanToolResultDiagnostic({ orphans: [] }));
  } finally {
    if (previousMemoryPath === undefined) delete process.env.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH;
    else process.env.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH = previousMemoryPath;
    if (previousOrphanPath === undefined) delete process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH;
    else process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH = previousOrphanPath;
    if (previousHome === undefined) delete process.env.HOME;
    else process.env.HOME = previousHome;
    await rm(directory, { recursive: true, force: true });
  }
});

test("memory diagnostics handle malformed event values without throwing", async () => {
  const directory = await temporaryDirectory("mecha-memory-diagnostic-values-");
  const path = join(directory, "memory.jsonl");
  try {
    assert.doesNotThrow(() => appendMemoryCaptureDiagnostic(null as never, { path }));
    assert.doesNotThrow(() => appendMemoryCaptureDiagnostic([] as never, { path }));
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("orphan diagnostics narrow fields and exclude source text", async () => {
  const directory = await temporaryDirectory("mecha-orphan-diagnostic-contract-");
  const path = join(directory, "orphan.jsonl");
  try {
    appendOrphanToolResultDiagnostic({
      sessionId: "session",
      sessionFile: "/tmp/session",
      model: "model",
      agentId: "agent",
      totalMessages: 5,
      quarantinedCount: 1,
      source: "secret source",
      orphans: [{
        messageIndex: 2,
        toolCallId: "raw|suffix",
        providerToolCallId: "raw",
        toolName: "bash",
        previousRole: "assistant",
        nextRole: "user",
        nearestAssistantIndex: 1,
        nearestAssistantStopReason: "error",
        nearestAssistantToolCallIds: ["raw"],
        matchingToolCallLater: true,
        output: "secret output",
      }],
    }, { path });

    const record = recordsAt(path)[0];
    assert.deepEqual(record, {
      timestamp: record?.timestamp,
      session_id: "session",
      session_file: "/tmp/session",
      model: "model",
      agent_id: "agent",
      total_messages: 5,
      quarantined_count: 1,
      orphans: [{
        message_index: 2,
        tool_call_id: "raw|suffix",
        provider_tool_call_id: "raw",
        tool_name: "bash",
        previous_role: "assistant",
        next_role: "user",
        nearest_assistant_index: 1,
        nearest_assistant_stop_reason: "error",
        nearest_assistant_tool_call_ids: ["raw"],
        matching_tool_call_later: true,
      }],
    });
    assert.equal(mode(path), 0o600);

    for (let index = 0; index < 20; index += 1) appendOrphanToolResultDiagnostic({ sessionId: "session", orphans: [{ toolCallId: `call-${index}`, toolName: "bash" }] }, { path, maxBytes: 260, keepBytes: 130 });
    assert.ok(statSync(path).size <= 260);
    assert.equal(statSync(`${path}.1`, { throwIfNoEntry: false })?.isFile(), undefined);

    appendOrphanToolResultDiagnostic({ sessionId: 42, totalMessages: "bad", orphans: ["bad", { messageIndex: "bad", matchingToolCallLater: false }] }, { path });
    const narrowed = recordsAt(path)[1];
    assert.equal(narrowed?.session_id, undefined);
    assert.equal(narrowed?.total_messages, undefined);
    assert.deepEqual(narrowed?.orphans, [{ matching_tool_call_later: false }]);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("trace is disabled, validates inputs, filters fields, clips values, and summarizes subagents", async () => {
  const directory = await temporaryDirectory("mecha-trace-contract-");
  const path = join(directory, "trace.jsonl");
  try {
    assert.equal(createNativeSubagentTrace, createExtensionTrace);
    const disabled = createExtensionTrace({ path: undefined });
    assert.equal(disabled.enabled, false);
    assert.equal(disabled.record("extension_initialized"), false);

    assert.throws(() => createExtensionTrace({ path: "" }), /path is invalid/);
    assert.throws(() => createExtensionTrace({ path, maxBytes: 0 }), /maxBytes/);
    assert.throws(() => createExtensionTrace({ path, now: "bad" as never }), /clock/);

    const trace = createExtensionTrace({ path, now: () => new Date("2026-01-01T00:00:00.000Z") });
    const invalidClock = createExtensionTrace({ path: join(directory, "invalid-clock.jsonl"), now: () => new Date("invalid") });
    assert.equal(invalidClock.record("valid-event"), false);
    assert.equal(trace.enabled, true);
    assert.equal(trace.record("Bad event"), false);
    assert.equal(trace.record("valid-event", null as never), false);
    assert.equal(trace.record("subagents-empty", { subagents: "malformed" }), true);
    assert.equal(trace.record("valid-event", {
      agent_id: "a".repeat(200),
      bytes: 4,
      available: true,
      negative: -1,
      fraction: 1.5,
      unknown: "drop me",
      subagents: [
        { id: " child ", type: " worker ", status: "running", description: "secret prompt" },
        { id: "" },
        "bad",
      ],
    }), true);
    assert.deepEqual(recordsAt(path), [{
      timestamp: "2026-01-01T00:00:00.000Z",
      event: "subagents-empty",
      subagents: [],
    }, {
      timestamp: "2026-01-01T00:00:00.000Z",
      event: "valid-event",
      agent_id: "a".repeat(128),
      bytes: 4,
      available: true,
      subagents: [{ id: "child", type: "worker", status: "running", description_length: 13 }],
    }]);
    assert.equal(mode(path), 0o600);
    assert.equal(mode(directory), 0o700);
    const nestedTracePath = join(directory, "nested", "trace.jsonl");
    const nestedTrace = createExtensionTrace({ path: nestedTracePath, now: () => new Date("2026-01-01T00:00:00.000Z") });
    assert.equal(nestedTrace.record("nested-event"), true);
    assert.equal(mode(join(directory, "nested")), 0o700);

    writeFileSync(join(directory, "bad-target"), "target");
    const failing = createExtensionTrace({ path: join(directory, "bad-target", "trace.jsonl") });
    assert.equal(failing.record("valid-event"), false);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("trace path precedence, legacy fallback, rotation, and malformed payload caps stay fixed", async () => {
  const directory = await temporaryDirectory("mecha-trace-rotation-contract-");
  const extensionPath = join(directory, "extension.jsonl");
  const legacyPath = join(directory, "legacy.jsonl");
  try {
    const selected = createExtensionTrace({ environment: {
      MECHA_PI_EXTENSION_TRACE_PATH: ` ${extensionPath} `,
      MECHA_PI_NATIVE_SUBAGENT_TRACE_PATH: legacyPath,
    } });
    assert.equal(selected.record("selected"), true);
    assert.equal(readFileSync(extensionPath, "utf8").includes('"event":"selected"'), true);
    assert.equal(statSync(legacyPath, { throwIfNoEntry: false })?.isFile(), undefined);

    const fallback = createExtensionTrace({ environment: { MECHA_PI_NATIVE_SUBAGENT_TRACE_PATH: legacyPath } });
    assert.equal(fallback.record("legacy-selected"), true);
    assert.equal(readFileSync(legacyPath, "utf8").includes('"event":"legacy-selected"'), true);

    const rotationPath = join(directory, "rotation", "trace.jsonl");
    mkdirSync(join(directory, "rotation"), { recursive: true });
    writeFileSync(rotationPath, "x".repeat(10));
    const rotating = createExtensionTrace({ path: rotationPath, maxBytes: 10, now: () => new Date("2026-01-01T00:00:00.000Z") });
    assert.equal(rotating.record("after-rotation"), true);
    assert.equal(readFileSync(`${rotationPath}.1`, "utf8"), "x".repeat(10));
    assert.equal(recordsAt(rotationPath)[0]?.event, "after-rotation");

    const failedRotationPath = join(directory, "failed-rotation", "trace.jsonl");
    mkdirSync(join(directory, "failed-rotation"), { recursive: true });
    writeFileSync(failedRotationPath, "x".repeat(10));
    mkdirSync(`${failedRotationPath}.1`);
    const failedRotation = createExtensionTrace({ path: failedRotationPath, maxBytes: 10 });
    assert.equal(failedRotation.record("cannot-rotate"), false);

    const malformed: Array<{ messageIndex: number; blockIndex: number; toolName: string; toolNameLength: number; toolCallId: string }> = [];
    for (let index = 0; index < 21; index += 1) malformed.push({ messageIndex: index, blockIndex: 0, toolName: "n".repeat(101), toolNameLength: 101, toolCallId: `call-${index}` });
    const observations: Array<{ event: string; fields: Record<string, unknown> }> = [];
    appendMalformedToolCallDiagnostic({ sessionId: "session", malformedCount: 21, malformed }, (event, fields) => {
      observations.push({ event, fields: fields as Record<string, unknown> });
    }, { now: () => new Date("2026-01-01T00:00:00.000Z") });
    assert.equal(observations.length, 1);
    assert.equal(observations[0]?.event, "malformed-tool-call");
    assert.equal(observations[0]?.fields.timestamp, "2026-01-01T00:00:00.000Z");
    assert.equal(observations[0]?.fields.malformedCount, 21);
    const payload = observations[0]?.fields.malformed;
    assert.ok(Array.isArray(payload));
    assert.equal(payload.length, 20);
    assert.equal((payload[0] as Record<string, unknown>).toolName, "n".repeat(100));

    const filtered: Array<Record<string, unknown>> = [];
    const defaultTrace = createExtensionTrace({ path: join(directory, "default.jsonl"), now: () => new Date("2026-01-01T00:00:00.000Z") });
    const originalRecord = defaultTrace.record;
    assert.equal(originalRecord("malformed-tool-call", observations[0]?.fields), true);
    filtered.push(recordsAt(join(directory, "default.jsonl"))[0] ?? {});
    assert.deepEqual(filtered[0], { timestamp: "2026-01-01T00:00:00.000Z", event: "malformed-tool-call" });
    assert.doesNotThrow(() => appendMalformedToolCallDiagnostic({ sessionId: "session", malformedCount: 0, malformed: [] }, () => { throw new Error("diagnostic sink failed"); }));
    let missingName: Record<string, unknown> | undefined;
    appendMalformedToolCallDiagnostic({ sessionId: "session", malformedCount: 1, malformed: [{ messageIndex: 0, blockIndex: 0, toolName: undefined, toolNameLength: undefined, toolCallId: undefined }] }, (_event, fields) => { missingName = fields; });
    assert.equal((missingName?.malformed as Array<Record<string, unknown>>)[0]?.toolName, "undefined");
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("orphan diagnostic outer catch contains event access failures", () => {
  const throwingEvent = Object.defineProperty({}, "orphans", {
    get() { throw new Error("diagnostic event access failed"); },
  });
  assert.doesNotThrow(() => appendOrphanToolResultDiagnostic(throwingEvent, { path: "unused.jsonl" }));
});

test("diagnostic writers contain nullish events and unusable paths", () => {
  const directory = "\u0000";
  assert.doesNotThrow(() => appendMemoryCaptureDiagnostic({ source: "secret" }, { path: directory }));
  assert.doesNotThrow(() => appendOrphanToolResultDiagnostic(null, { path: directory }));
  assert.doesNotThrow(() => appendOrphanToolResultDiagnostic(undefined, { path: directory }));
  assert.doesNotThrow(() => appendOrphanToolResultDiagnostic({ orphans: [] }, { path: directory }));
});

test("the process-global diagnostic journal keys writers by path and writes bounded source-labelled records", async () => {
  const directory = await temporaryDirectory("mecha-diagnostic-journal-contract-");
  const firstPath = join(directory, "journal.jsonl");
  const secondPath = join(directory, "other.jsonl");
  const windowId = firstPath;
  try {
    const first = createDiagnosticJournal({ path: firstPath, windowId, processId: process.pid, queueLimit: 8 });
    const second = createDiagnosticJournal({ path: secondPath, windowId, processId: process.pid });
    assert.notEqual(first, second);
    assert.notEqual(first.path, firstPath);
    assert.notEqual(second.path, first.path);
    assert.equal(createDiagnosticJournal({ path: firstPath, windowId, processId: process.pid }), first);
    const otherWindow = createDiagnosticJournal({ path: firstPath, windowId: `${windowId}:other`, processId: process.pid });
    assert.notEqual(otherWindow, first);
    assert.notEqual(otherWindow.path, first.path);
    const globalValue = Object.getOwnPropertyDescriptor(globalThis, Symbol.for("mecha.pi.diagnostic-journal.v1"))?.value;
    assert.equal(globalValue !== null && typeof globalValue === "object", true);
    if (globalValue === null || typeof globalValue !== "object") throw new Error("journal contract was not published");
    assert.equal(Reflect.get(globalValue, "version"), 1);
    const globalRecord = Reflect.get(globalValue, "record");
    assert.equal(typeof globalRecord, "function");
    if (typeof globalRecord !== "function") throw new Error("journal record contract was not published");
    assert.equal(Reflect.apply(globalRecord, globalValue, ["activity", "direct_probe"]), true);
    assert.equal(first.record("activity", "snapshot_read", {
      state: "running",
      snapshot_count: 2,
      frame_id: "frame",
      request_id: "request",
      ack_frame_id: "ack",
      producer_epoch: "11111111-1111-4111-8111-111111111111",
      connection_generation: 2,
      prompt: "private prompt",
      output: "private output",
      error: "private error",
      goal: "private goal",
      path: "/private/path",
      unknown: "drop me",
    }), true);
    assert.equal(second.record("compaction", "capture_failed", { reason: "capture_unavailable" }), true);
    assert.equal(first.record("bad source", "bad event"), false);
    await first.flush();
    await second.flush();

    const journalPath = first.path;
    assert.equal(typeof journalPath, "string");
    if (typeof journalPath !== "string") throw new Error("journal path was not derived");
    const records = recordsAt(journalPath);
    assert.deepEqual(records.map(({ sequence, source, event, state, snapshot_count, reason }) => ({ sequence, source, event, state, snapshot_count, reason })), [
      { sequence: 1, source: "activity", event: "direct_probe", state: undefined, snapshot_count: undefined, reason: undefined },
      { sequence: 2, source: "activity", event: "snapshot_read", state: "running", snapshot_count: 2, reason: undefined },
    ]);
    const secondJournalPath = second.path;
    assert.equal(typeof secondJournalPath, "string");
    if (typeof secondJournalPath !== "string") throw new Error("second journal path was not derived");
    assert.deepEqual(recordsAt(secondJournalPath).map(({ sequence, source, event, reason }) => ({ sequence, source, event, reason })), [
      { sequence: 1, source: "compaction", event: "capture_failed", reason: "capture_unavailable" },
    ]);
    assert.deepEqual({
      frame_id: records[1]?.frame_id,
      request_id: records[1]?.request_id,
      ack_frame_id: records[1]?.ack_frame_id,
      producer_epoch: records[1]?.producer_epoch,
      connection_generation: records[1]?.connection_generation,
    }, {
      frame_id: "frame",
      request_id: "request",
      ack_frame_id: "ack",
      producer_epoch: "11111111-1111-4111-8111-111111111111",
      connection_generation: 2,
    });
    const text = readFileSync(journalPath, "utf8");
    assert.equal(text.includes("private prompt"), false);
    assert.equal(text.includes("private output"), false);
    assert.equal(text.includes("private error"), false);
    assert.equal(text.includes("private goal"), false);
    assert.equal(text.includes("/private/path"), false);
    assert.equal(mode(journalPath), 0o600);
    assert.equal(mode(directory), 0o700);
    assert.equal(statSync(secondPath, { throwIfNoEntry: false })?.isFile(), undefined);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("the diagnostic journal key includes path inputs and evicts deleted test roots", async () => {
  const firstRoot = await temporaryDirectory("mecha-diagnostic-journal-reuse-");
  const secondRoot = await temporaryDirectory("mecha-diagnostic-journal-reuse-");
  const windowId = "diagnostic-journal-reuse-window";
  const firstEnvironment = {
    HOME: firstRoot,
    MECHA_TMUX_WINDOW: windowId,
    MECHA_PI_DIAGNOSTIC_JOURNAL_PATH: join(firstRoot, "journal.jsonl"),
  };
  try {
    const first = createDiagnosticJournal({ environment: firstEnvironment, processId: process.pid });
    const sibling = createDiagnosticJournal({
      environment: { ...firstEnvironment, MECHA_PI_DIAGNOSTIC_JOURNAL_PATH: join(firstRoot, "sibling.jsonl") },
      windowId: `${windowId}:sibling`,
      processId: process.pid,
    });
    assert.notEqual(sibling, first);
    const differentHome = createDiagnosticJournal({
      environment: { ...firstEnvironment, HOME: secondRoot },
      processId: process.pid,
    });
    assert.notEqual(differentHome, first);
    const differentConfiguredPath = createDiagnosticJournal({
      environment: { ...firstEnvironment, MECHA_PI_DIAGNOSTIC_JOURNAL_PATH: join(secondRoot, "journal.jsonl") },
      processId: process.pid,
    });
    assert.notEqual(differentConfiguredPath, first);

    assert.equal(first.record("activity", "before_cleanup"), true);
    assert.equal(sibling.record("activity", "before_cleanup"), true);
    await first.flush();
    await sibling.flush();
    await rm(firstRoot, { recursive: true, force: true });

    assert.equal(first.record("activity", "after_cleanup"), false);
    const globalValue = Object.getOwnPropertyDescriptor(globalThis, Symbol.for("mecha.pi.diagnostic-journal.v1"))?.value;
    assert.equal(globalValue !== null && typeof globalValue === "object", true);
    if (globalValue === null || typeof globalValue !== "object") throw new Error("journal contract was not published");
    const globalRecord = Reflect.get(globalValue, "record");
    assert.equal(typeof globalRecord, "function");
    if (typeof globalRecord !== "function") throw new Error("journal record contract was not published");
    assert.equal(Reflect.apply(globalRecord, globalValue, ["activity", "after_cleanup"]), false);

    const laterRoot = createDiagnosticJournal({ environment: firstEnvironment, processId: process.pid });
    assert.notEqual(laterRoot, first);
    assert.equal(sibling.closed, true);
    assert.equal(laterRoot.record("activity", "after_cleanup"), true);
    await laterRoot.flush();
    assert.equal(readFileSync(laterRoot.path ?? "", "utf8").includes("after_cleanup"), true);
  } finally {
    await rm(firstRoot, { recursive: true, force: true });
    await rm(secondRoot, { recursive: true, force: true });
  }
});

test("a pending diagnostic drain cannot recreate a removed root", async () => {
  const root = await temporaryDirectory("mecha-diagnostic-journal-race-");
  const journal = createDiagnosticJournal({
    path: join(root, "logs", "journal.jsonl"),
    windowId: "diagnostic-journal-race-window",
    processId: process.pid,
  });
  try {
    assert.equal(journal.record("activity", "before_cleanup"), true);
    await rm(root, { recursive: true, force: true });
    await journal.flush();
    assert.equal(existsSync(root), false);
    assert.equal(journal.closed, true);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("the diagnostic journal retains bounded pi-subagents kinds", async () => {
  const directory = await temporaryDirectory("mecha-diagnostic-journal-kinds-");
  const path = join(directory, "journal.jsonl");
  try {
    const journal = createDiagnosticJournal({ path, windowId: path, processId: process.pid });
    assert.equal(journal.record("pi-subagents", "subagents.created", {
      kind: "general-purpose",
    }), true);
    assert.equal(journal.record("pi-subagents", "subagents.created", {
      kind: "Plan",
    }), true);
    assert.equal(journal.record("pi-subagents", "subagents.created", {
      kind: `${"x".repeat(128)}!`,
    }), true);
    await journal.flush();
    const records = recordsAt(journal.path ?? "");
    assert.equal(records[0]?.kind, "general-purpose");
    assert.equal(records[1]?.kind, "Plan");
    assert.equal(Object.hasOwn(records[2] ?? {}, "kind"), false);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("the diagnostic journal rejects malformed activity identities, kinds, and error names without truncating them", async () => {
  const directory = await temporaryDirectory("mecha-diagnostic-journal-validation-");
  const basePath = join(directory, "journal.jsonl");
  try {
    const journal = createDiagnosticJournal({ path: basePath, windowId: basePath, processId: process.pid });
    assert.equal(journal.record("mecha-goal", "activity_gate_rejected", {
      kind: "tool-completed",
      event_id: "tool-completed:read-1",
      session_id: "session-1",
      error_name: "TypeError",
    }), true);
    assert.equal(journal.record("mecha-goal", "activity_gate_rejected", {
      kind: "tool-completed private",
      event_id: "tool-completed:private id",
      session_id: "private session",
      error_name: "PrivateError",
    }), true);
    await journal.flush();
    assert.equal(typeof journal.path, "string");
    if (typeof journal.path !== "string") throw new Error("diagnostic journal path is unavailable");
    const records = recordsAt(journal.path);
    assert.deepEqual(records[0], {
      timestamp: records[0]?.timestamp,
      sequence: 1,
      source: "mecha-goal",
      event: "activity_gate_rejected",
      kind: "tool-completed",
      event_id: "tool-completed:read-1",
      session_id: "session-1",
      error_name: "TypeError",
    });
    assert.equal(Object.hasOwn(records[1] ?? {}, "kind"), false);
    assert.equal(Object.hasOwn(records[1] ?? {}, "event_id"), false);
    assert.equal(Object.hasOwn(records[1] ?? {}, "session_id"), false);
    assert.equal(Object.hasOwn(records[1] ?? {}, "error_name"), false);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("the diagnostic journal reports bounded queue drops and filesystem write failures without leaking errors", async () => {
  const directory = await temporaryDirectory("mecha-diagnostic-journal-health-");
  const saturatedPath = join(directory, "saturated.jsonl");
  const blockedParent = join(directory, "blocked");
  try {
    const saturated = createDiagnosticJournal({ path: saturatedPath, windowId: saturatedPath, processId: process.pid, queueLimit: 2 });
    // The queue cap is exercised synchronously before the asynchronous drain can run.
    for (let index = 0; index < 40; index += 1) saturated.record("activity", "heartbeat", { state: "active" });
    await saturated.flush();
    const saturatedHealth = saturated.health();
    assert.equal(saturatedHealth.accepted + saturatedHealth.dropped, 40);
    assert.ok(saturatedHealth.dropped > 0);
    assert.equal(saturatedHealth.writeFailures, 0);

    writeFileSync(blockedParent, "not a directory");
    const failing = createDiagnosticJournal({ path: join(blockedParent, "journal.jsonl"), windowId: blockedParent, processId: process.pid });
    assert.equal(failing.record("activity", "heartbeat", { state: "active" }), true);
    await failing.flush();
    const failedHealth = failing.health();
    assert.equal(failedHealth.accepted, 1);
    assert.equal(failedHealth.written, 0);
    assert.equal(failedHealth.writeFailures, 1);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("the diagnostic journal prunes prior-process active and segment files in the same journal directory", async () => {
  const directory = await temporaryDirectory("mecha-diagnostic-journal-prior-process-");
  const path = join(directory, "journal.jsonl");
  const nowMs = Date.parse("2026-03-03T00:00:00.000Z");
  const oldMs = nowMs - (49 * 60 * 60 * 1000);
  const now = () => new Date(nowMs);
  try {
    const prior = createDiagnosticJournal({ path, windowId: "same-window", processId: 41001, now });
    const priorPath = prior.path;
    assert.equal(typeof priorPath, "string");
    if (typeof priorPath !== "string") throw new Error("prior journal path was not derived");
    writeFileSync(priorPath, `${JSON.stringify({ timestamp: new Date(oldMs).toISOString(), event: "prior-active" })}\n`, { mode: 0o600 });
    const priorSegment = `${priorPath}.segment-${oldMs}`;
    writeFileSync(priorSegment, `${JSON.stringify({ timestamp: new Date(oldMs).toISOString(), event: "prior-segment" })}\n`, { mode: 0o600 });
    await prior.close();

    const current = createDiagnosticJournal({ path, windowId: "same-window", processId: 41002, now });
    assert.equal(current.record("activity", "current"), true);
    await current.flush();
    assert.equal(existsSync(priorPath), false);
    assert.equal(existsSync(priorSegment), false);
    assert.match(readFileSync(current.path ?? "", "utf8"), /\"event\":\"current\"/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("the diagnostic journal drops expired active data before appending a new record", async () => {
  const directory = await temporaryDirectory("mecha-diagnostic-journal-expired-active-");
  const path = join(directory, "journal.jsonl");
  const nowMs = Date.parse("2026-03-03T00:00:00.000Z");
  const oldMs = nowMs - (49 * 60 * 60 * 1000);
  try {
    const journal = createDiagnosticJournal({ path, windowId: "expired-active", processId: 41003, now: () => new Date(nowMs) });
    const journalPath = journal.path;
    assert.equal(typeof journalPath, "string");
    if (typeof journalPath !== "string") throw new Error("journal path was not derived");
    writeFileSync(journalPath, `${JSON.stringify({ timestamp: new Date(oldMs).toISOString(), event: "expired" })}\n`, { mode: 0o600 });
    assert.equal(journal.record("activity", "fresh"), true);
    await journal.flush();
    const contents = readFileSync(journalPath, "utf8");
    assert.equal(contents.includes('"event":"expired"'), false);
    assert.match(contents, /\"event\":\"fresh\"/);
    assert.equal(readdirSync(directory).some((entry) => entry.includes(".segment-")), false);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("the diagnostic journal keeps high-rate incident segments readable for 48 hours", async () => {
  const directory = await temporaryDirectory("mecha-diagnostic-journal-retention-");
  const path = join(directory, "journal.jsonl");
  let nowMs = Date.parse("2026-01-01T00:00:00.000Z");
  const now = () => new Date(nowMs);
  try {
    const journal = createDiagnosticJournal({
      path,
      windowId: path,
      processId: process.pid,
      maxBytes: 256,
      rotationAgeMs: 24 * 60 * 60 * 1000,
      pruneAgeMs: 48 * 60 * 60 * 1000,
      now,
    });
    const journalPath = journal.path;
    assert.equal(typeof journalPath, "string");
    if (typeof journalPath !== "string") throw new Error("journal path was not derived");

    assert.equal(journal.record("activity", "incident", { state: "active" }), true);
    await journal.flush();
    for (let index = 0; index < 120; index += 1) {
      nowMs += 60 * 1000;
      assert.equal(journal.record("activity", "heartbeat", { state: "active" }), true);
      await journal.flush();
    }

    const segmentPrefix = `${basename(journalPath)}.segment-`;
    const segments = () => readdirSync(directory)
      .filter((entry) => entry.startsWith(segmentPrefix))
      .map((entry) => join(directory, entry));
    const segmentsBeforeExpiry = segments();
    const incidentSegment = segmentsBeforeExpiry.find((segment) => readFileSync(segment, "utf8").includes('"event":"incident"'));
    assert.equal(typeof incidentSegment, "string", "the incident must be in a rotated segment");
    if (typeof incidentSegment !== "string") throw new Error("incident segment was not created");
    assert.equal(mode(incidentSegment), 0o600);
    assert.equal(mode(directory), 0o700);
    assert.ok(segments().length > 10, "the test must exercise repeated size rotation");

    nowMs = Date.parse("2026-01-02T23:59:00.000Z");
    assert.equal(journal.record("activity", "before_expiry", { state: "active" }), true);
    await journal.flush();
    assert.ok(segmentsBeforeExpiry.every((segment) => existsSync(segment)), "all segments must remain during the 48-hour window");
    assert.equal(existsSync(incidentSegment), true, "the incident segment must remain during the 48-hour window");
    assert.match(readFileSync(incidentSegment, "utf8"), /\"event\":\"incident\"/);

    nowMs = Date.parse("2026-01-03T00:00:00.000Z");
    assert.equal(journal.record("activity", "after_expiry", { state: "active" }), true);
    await journal.flush();
    assert.equal(existsSync(incidentSegment), false, "segments at the retention boundary must be pruned");
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("the diagnostic journal ages the active segment from its first record, not mtime", async () => {
  const directory = await temporaryDirectory("mecha-diagnostic-journal-active-age-");
  const path = join(directory, "journal.jsonl");
  let nowMs = Date.parse("2026-02-01T00:00:00.000Z");
  const now = () => new Date(nowMs);
  try {
    const journal = createDiagnosticJournal({
      path,
      windowId: path,
      processId: process.pid,
      maxBytes: 4096,
      rotationAgeMs: 24 * 60 * 60 * 1000,
      pruneAgeMs: 48 * 60 * 60 * 1000,
      now,
    });
    const journalPath = journal.path;
    assert.equal(typeof journalPath, "string");
    if (typeof journalPath !== "string") throw new Error("journal path was not derived");

    assert.equal(journal.record("activity", "first", { state: "active" }), true);
    await journal.flush();
    const oldMtime = (nowMs - (72 * 60 * 60 * 1000)) / 1000;
    utimesSync(journalPath, oldMtime, oldMtime);
    await journal.close();
    const reopened = createDiagnosticJournal({
      path,
      windowId: path,
      processId: process.pid,
      maxBytes: 4096,
      rotationAgeMs: 24 * 60 * 60 * 1000,
      pruneAgeMs: 48 * 60 * 60 * 1000,
      now,
    });

    nowMs += 60 * 60 * 1000;
    assert.equal(reopened.record("activity", "still_active", { state: "active" }), true);
    await reopened.flush();
    assert.equal(readdirSync(directory).some((entry) => entry.startsWith(`${basename(journalPath)}.segment-`)), false);

    nowMs += 23 * 60 * 60 * 1000;
    assert.equal(reopened.record("activity", "aged_out", { state: "active" }), true);
    await reopened.flush();
    const rotated = readdirSync(directory).filter((entry) => entry.startsWith(`${basename(journalPath)}.segment-`));
    assert.equal(rotated.length, 1, "the active segment should rotate at 24 hours from its first record");
    assert.match(readFileSync(join(directory, rotated[0] ?? ""), "utf8"), /\"event\":\"first\"/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
