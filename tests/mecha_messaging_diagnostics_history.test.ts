import assert from "node:assert/strict";
import { chmodSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  appendMalformedToolCallDiagnostic,
  appendMemoryCaptureDiagnostic,
  appendOrphanToolResultDiagnostic,
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
