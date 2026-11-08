import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFile, spawn } from "node:child_process";
import { cp, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const TSX = resolve(ROOT, "pi-extensions/mecha-messaging/node_modules/tsx/dist/cli.mjs");
const BASELINE_COMMIT = "a2c43e700c59d1fb563a01412d991f2be9fb65c7";
const BASELINE_INDEX_SHA256 = "c6824f8241652b6d4b8d6225079bb71c0e00d18446300d441cfccb56791ad387";
const BASELINE_INDEX_BLOB = "0aca6ef0c52aac1a57819fe06e7e1dd854224e3e";
const BASELINE_INDEX_BYTES = 108015;
// The fixture fixes every other value. Only a root-generated malformed trace timestamp may differ.
const DIFFERENTIAL_ALLOWLIST = Object.freeze([
  Object.freeze({ kind: "trace", event: "malformed-tool-call", path: "$.fields.timestamp" }),
] as const);
const SCENARIOS = [
  "clean-history",
  "malformed-matrix",
  "malformed-causes-orphan",
  "orphan-ownership",
  "later-match",
  "repeat-and-scope",
  "trace-and-writers",
  "diagnostic-failure",
  "context-order",
] as const;

const CHILD_RUNNER = String.raw`
import { existsSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const scenario = process.argv[2];
const root = process.argv[3];
if (typeof scenario !== "string" || typeof root !== "string") throw new Error("scenario and root are required");
const fixedTime = Date.parse("2026-01-01T00:00:00.000Z");
const RealDate = Date;
class FixedDate extends RealDate {
  constructor(...args) { super(...(args.length === 0 ? [fixedTime] : args)); }
  static now() { return fixedTime; }
}
globalThis.Date = FixedDate;

const observations = [];
const unexpectedCalls = [];
const fetchCalls = [];
const timeoutHandles = [];
const intervalHandles = [];
let nextHandle = 1;
function clone(value) {
  return value === undefined ? undefined : structuredClone(value);
}
function observe(value) { observations.push(clone(value)); }
function invariant(value, message) { if (!value) throw new Error(message); }
function unexpected(boundary, details) {
  const call = { boundary, ...(details === undefined ? {} : { details }) };
  unexpectedCalls.push(call);
  throw new Error("unexpected external call: " + boundary);
}
function mode(path) { return statSync(path).mode & 0o777; }
function jsonl(path) {
  if (!existsSync(path)) return [];
  return readFileSync(path, "utf8").trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
}
function fileSnapshot(writer, path, includeParentMode = false) {
  const snapshot = {
    kind: "file-record",
    writer,
    exists: existsSync(path),
    records: existsSync(path) ? jsonl(path) : [],
    size: existsSync(path) ? statSync(path).size : 0,
    mode: existsSync(path) ? mode(path) : undefined,
    rotation: existsSync(path + ".1") ? readFileSync(path + ".1", "utf8") : undefined,
  };
  if (includeParentMode && existsSync(path)) snapshot.parentMode = mode(join(path, ".."));
  observe(snapshot);
  return snapshot;
}
function call(name, callback) {
  observe({ kind: "operation", name });
  const value = callback();
  observe({ kind: "operation-result", name, value });
  return value;
}
function tryCall(name, callback) {
  observe({ kind: "operation", name });
  try {
    const value = callback();
    observe({ kind: "operation-result", name, value });
    return value;
  } catch (error) {
    observe({ kind: "operation-error", name, error: { name: error?.name, message: error?.message } });
    return undefined;
  }
}
function add(map, name, handler) {
  const handlers = map.get(name) ?? [];
  handlers.push(handler);
  map.set(name, handlers);
}
function createPi() {
  const handlers = new Map();
  const eventHandlers = new Map();
  const tools = new Map();
  const commands = new Map();
  return {
    handlers,
    eventHandlers,
    tools,
    commands,
    events: {
      on(name, handler) { add(eventHandlers, name, handler); },
      emit(name, event) {
        observe({ kind: "pi-event", action: "emit", name });
        for (const handler of eventHandlers.get(name) ?? []) handler(event);
      },
    },
    on(name, handler) { add(handlers, name, handler); },
    registerTool(tool) { tools.set(tool.name, tool); },
    registerCommand(name, command) { commands.set(name, command); },
    appendEntry() { unexpected("pi.appendEntry"); },
    sendMessage() { unexpected("pi.sendMessage"); },
    exec() { unexpected("pi.exec"); },
  };
}
function createTimers() {
  function setTimeoutImpl(callback, delay) {
    const handle = { id: nextHandle++, callback, delay, unref() {} };
    timeoutHandles.push(handle);
    observe({ kind: "timer", action: "set-timeout", id: handle.id, delay });
    unexpected("setTimeout", { id: handle.id, delay });
  }
  function clearTimeoutImpl(handle) {
    observe({ kind: "timer", action: "clear-timeout", id: handle?.id });
    if (handle !== undefined) handle.cleared = true;
  }
  function setIntervalImpl(callback, delay) {
    const handle = { id: nextHandle++, callback, delay, unref() {} };
    intervalHandles.push(handle);
    observe({ kind: "timer", action: "set-interval", id: handle.id, delay });
    unexpected("setInterval", { id: handle.id, delay });
  }
  function clearIntervalImpl(handle) {
    observe({ kind: "timer", action: "clear-interval", id: handle?.id });
    if (handle !== undefined) handle.cleared = true;
  }
  return { setTimeoutImpl, clearTimeoutImpl, setIntervalImpl, clearIntervalImpl };
}
function fakeFetch(input) {
  const url = String(input);
  fetchCalls.push(url);
  unexpectedCalls.push({ boundary: "fetch", url });
  return Promise.reject(new Error("unexpected fetch request"));
}
function createContext(sessionId, usage) {
  return {
    sessionManager: {
      getSessionId: () => sessionId,
      getBranch: () => [],
      isPersisted: () => false,
      getSessionFile: () => "sessions/" + sessionId + ".jsonl",
    },
    model: { provider: "fixed-provider", id: "fixed-model", name: "Fixed Model", contextWindow: 4096, maxTokens: 256 },
    ui: {
      notify(message, level) { observe({ kind: "ui", action: "notify", message, level, sessionId }); },
      setStatus(key, value) { observe({ kind: "ui", action: "status", key, value, sessionId }); },
    },
    getContextUsage: () => {
      observe({ kind: "usage-read", sessionId, usage });
      return usage;
    },
    compact(options) {
      observe({ kind: "compact", action: "registered", sessionId, hasComplete: typeof options?.onComplete === "function", hasError: typeof options?.onError === "function" });
    },
    abort() { observe({ kind: "context", action: "abort", sessionId }); },
  };
}
function historyEvent(label, messages, orphanDiagnosticPath) {
  let current = messages;
  return Object.defineProperty({}, "messages", {
    enumerable: true,
    get() {
      observe({ kind: "message-read", label });
      return current;
    },
    set(value) {
      current = value;
      observe({ kind: "event-assignment", label, messages: value });
      if (orphanDiagnosticPath !== undefined) {
        observe({ kind: "orphan-writer-visible-at-assignment", exists: existsSync(orphanDiagnosticPath) });
      }
    },
  });
}
async function contextCall(pi, label, context, messages, orphanDiagnosticPath) {
  observe({ kind: "operation", name: label });
  const event = historyEvent(label, messages, orphanDiagnosticPath);
  const handler = pi.handlers.get("context")?.[0];
  invariant(typeof handler === "function", "context handler is not registered");
  const result = await handler(event, context);
  observe({ kind: "context-return", name: label, result });
  return result;
}
function assistant(content, stopReason) {
  return { role: "assistant", ...(stopReason === undefined ? {} : { stopReason }), content };
}
function callBlock(id, name, type = "toolCall") { return { type, id, name, arguments: {} }; }
function resultBlock(toolCallId, toolName = "bash") { return { role: "toolResult", toolCallId, toolName, content: [] }; }
function malformedHistory(rawMalformedId = "bad", rawOrphanId = "orphan") {
  return [
    assistant([callBlock("valid", "bash"), callBlock(rawMalformedId, "bad name")]),
    resultBlock(rawOrphanId),
  ];
}
function traceObservation(eventName) {
  return observations.find((row) => row.kind === "trace" && row.event === eventName);
}
function contextResult(label) {
  return observations.findLast((row) => row.kind === "context-return" && row.name === label);
}
async function createFixture(implementation) {
  await mkdir(join(root, "home"), { recursive: true });
  await mkdir(join(root, "pi-agent"), { recursive: true });
  await mkdir(join(root, "sessions"), { recursive: true });
  await writeFile(join(root, "sessions", "parent-session.jsonl"), "parent\\n", { mode: 0o600 });
  await writeFile(join(root, "sessions", "child-session.jsonl"), "child\\n", { mode: 0o600 });
  globalThis.fetch = fakeFetch;
  const exportKeys = Object.keys(implementation).sort();
  invariant(!exportKeys.includes("quarantineMalformedToolCalls") && !exportKeys.includes("sanitizeContextHistory"), "internal sanitation export leaked from the root");
  observe({ kind: "exports", keys: exportKeys });
  const timers = createTimers();
  const pi = createPi();
  implementation.createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "fixed-agent",
    createClient: () => unexpected("mailbox-client.create"),
    createCoordinator: () => unexpected("delivery-coordinator.create"),
    createSettledScheduler: () => ({
      onParentActivity: () => unexpected("settled-scheduler.onParentActivity"),
      onParentSettled: () => unexpected("settled-scheduler.onParentSettled"),
      onChildStarted: () => unexpected("settled-scheduler.onChildStarted"),
      onChildDisposed: () => unexpected("settled-scheduler.onChildDisposed"),
      dispose: () => unexpected("settled-scheduler.dispose"),
      activeChildCount: () => 0,
    }),
    ...timers,
    nowImpl: () => fixedTime,
    getSubagentsService: () => undefined,
    nativeSubagentTrace: {
      enabled: true,
      record(event, fields = {}) {
        observe({ kind: "trace", event, fields });
        return true;
      },
    },
    operationalTools: { fetchImpl: fakeFetch, environment: process.env },
  });
  return { implementation, pi, timers };
}
async function runCleanHistory(fixture) {
  const parent = createContext("parent-session", undefined);
  await contextCall(fixture.pi, "empty-history", parent, []);
  await contextCall(fixture.pi, "clean-history", parent, [
    { role: "user", content: [{ type: "text", text: "clean user" }] },
    { role: "assistant", content: [{ type: "text", text: "clean assistant" }] },
    resultBlock(""),
    resultBlock(undefined, "missing-id"),
  ]);
  invariant(contextResult("empty-history")?.result === undefined, "clean empty history returned a value");
  invariant(contextResult("clean-history")?.result === undefined, "clean history returned a value");
  invariant(observations.filter((row) => row.kind === "ui" && row.action === "notify").length === 0, "clean history emitted a notice");
}
async function runMalformedMatrix(fixture) {
  const blocks = [
    null,
    "ordinary text",
    callBlock("valid", "valid_name"),
    callBlock("alias", "valid-name", "tool_use"),
    callBlock("empty", ""),
    callBlock("space", "has space"),
    callBlock("punctuation", "has.dot"),
    callBlock("number-id", "number-id"),
    { type: "toolCall", id: 42, name: "number-id" },
    { type: "toolCall", name: 42 },
  ];
  for (let index = 0; index < 21; index += 1) {
    blocks.push(callBlock("matrix-" + index, index % 2 === 0 ? "n".repeat(101) : "bad name", index % 2 === 0 ? "toolCall" : "tool_use"));
  }
  const parent = createContext("parent-session", undefined);
  await contextCall(fixture.pi, "malformed-matrix", parent, [
    assistant(blocks),
    { role: "user", content: [{ type: "toolCall", id: "user-call", name: "bad name" }] },
    { role: "assistant", content: "non-array content" },
    { role: "assistant" },
  ]);
  const trace = traceObservation("malformed-tool-call");
  invariant(trace?.fields?.malformedCount === 25, "malformed count changed");
  invariant(trace?.fields?.malformed?.length === 20, "malformed trace cap changed");
  invariant(trace.fields.malformed.some((entry) => entry.toolName?.length === 100), "malformed trace tool name cap changed");
  const assignment = observations.find((row) => row.kind === "event-assignment" && row.label === "malformed-matrix");
  invariant(JSON.stringify(assignment?.messages?.[0]?.content) === JSON.stringify([
    null,
    "ordinary text",
    callBlock("valid", "valid_name"),
    callBlock("alias", "valid-name", "tool_use"),
    callBlock("number-id", "number-id"),
    { type: "toolCall", id: 42, name: "number-id" },
  ]), "malformed matrix retained the wrong blocks");
  invariant(contextResult("malformed-matrix")?.result === undefined, "malformed matrix returned an orphan result");
}
async function runMalformedCausesOrphan(fixture) {
  const parent = createContext("parent-session", undefined);
  const orphanDiagnosticPath = join(root, "diagnostics", "orphan-tool-results.jsonl");
  await contextCall(fixture.pi, "malformed-causes-orphan", parent, malformedHistory("bad", "bad"), orphanDiagnosticPath);
  const traceIndex = observations.findIndex((row) => row.kind === "trace" && row.event === "malformed-tool-call");
  const assignmentIndex = observations.findIndex((row) => row.kind === "event-assignment" && row.label === "malformed-causes-orphan");
  const orphanVisibility = observations.find((row) => row.kind === "orphan-writer-visible-at-assignment");
  const orphanNoticeIndex = observations.findIndex((row) => row.kind === "ui" && row.message === "Quarantined orphan tool result(s) before provider request: bad" && row.level === "error");
  const malformedNoticeIndex = observations.findIndex((row) => row.kind === "ui" && row.message === "Quarantined malformed tool call(s): bad name" && row.level === "error");
  invariant(traceIndex >= 0 && malformedNoticeIndex > traceIndex && assignmentIndex > malformedNoticeIndex && orphanNoticeIndex > assignmentIndex, "malformed and orphan reporting order changed");
  invariant(orphanVisibility?.exists === false, "orphan writer ran before event.messages assignment");
  invariant(contextResult("malformed-causes-orphan")?.result?.messages?.length === 1, "malformed-causes-orphan retained the orphan");
  fileSnapshot("malformed-causes-orphan", orphanDiagnosticPath);
}
async function runOrphanOwnership(fixture) {
  const parent = createContext("parent-session", undefined);
  const messages = [
    resultBlock("before|provider", "before"),
    assistant([
      callBlock("plain", "plain"),
      callBlock("owned|first|second", "owned"),
      callBlock("|call", "empty-provider-id"),
      callBlock("", "raw-empty-id"),
    ]),
    resultBlock("plain|ignored-suffix", "plain-result"),
    resultBlock("owned|other", "owned-result"),
    resultBlock("|suffix", "empty-provider-result"),
    resultBlock("", "raw-empty-result"),
    resultBlock(undefined, "missing-id"),
    resultBlock("plain|duplicate", "duplicate"),
    assistant([callBlock("error-id", "bash")], "error"),
    resultBlock("error-id", "error-result"),
    assistant([callBlock("aborted-id", "bash")], "aborted"),
    resultBlock("aborted-id", "aborted-result"),
    assistant([callBlock("alias-id", "bash", "tool_use")], "stop"),
    resultBlock("alias-id", "alias-result"),
  ];
  await contextCall(fixture.pi, "orphan-ownership", parent, messages);
  const result = contextResult("orphan-ownership")?.result;
  const retainedIds = result?.messages?.filter((message) => message.role === "toolResult").map((message) => message.toolCallId);
  invariant(JSON.stringify(retainedIds) === JSON.stringify([
    "plain|ignored-suffix", "owned|other", "|suffix", "", undefined, "plain|duplicate",
  ]), "orphan ownership retained the wrong results");
  const file = fileSnapshot("orphan-ownership", join(root, "diagnostics", "orphan-tool-results.jsonl"));
  const orphanIds = file.records?.[0]?.orphans?.map((orphan) => orphan.tool_call_id);
  invariant(JSON.stringify(orphanIds) === JSON.stringify(["before|provider", "error-id", "aborted-id", "alias-id"]), "orphan ownership reported the wrong incidents");
}
async function runLaterMatch(fixture) {
  const parent = createContext("parent-session", undefined);
  await contextCall(fixture.pi, "later-match", parent, [
    resultBlock("later|result", "bash"),
    assistant([callBlock("later|call", "bash")]),
  ]);
  const file = fileSnapshot("later-match", join(root, "diagnostics", "orphan-tool-results.jsonl"));
  invariant(file.records?.[0]?.orphans?.[0]?.matching_tool_call_later === true, "a later matching call rescued the orphan");
  invariant(contextResult("later-match")?.result?.messages?.length === 1, "later-match retained the orphan");
}
async function runRepeatAndScope(fixture) {
  const parent = createContext("parent-session", undefined);
  const other = createContext("other-session", undefined);
  await contextCall(fixture.pi, "repeat-first", parent, malformedHistory("bad|one", "orphan|one"));
  await contextCall(fixture.pi, "repeat-same", parent, malformedHistory("bad|one", "orphan|one"));
  await contextCall(fixture.pi, "repeat-suffix", parent, malformedHistory("bad|two", "orphan|two"));
  await contextCall(fixture.pi, "repeat-other-session", other, malformedHistory("bad|one", "orphan|one"));
  const missingIdHistory = [assistant([{ type: "toolCall", name: "bad name" }])];
  await contextCall(fixture.pi, "repeat-missing-first", parent, missingIdHistory);
  await contextCall(fixture.pi, "repeat-missing-same", parent, missingIdHistory);
  invariant(observations.filter((row) => row.kind === "trace" && row.event === "malformed-tool-call").length === 4, "malformed deduplication scope changed");
  invariant(observations.filter((row) => row.kind === "ui" && row.action === "notify" && row.message.startsWith("Quarantined malformed")).length === 4, "malformed notices were not deduplicated");
  const file = fileSnapshot("repeat-and-scope", join(root, "diagnostics", "orphan-tool-results.jsonl"));
  invariant(file.records.length === 3, "orphan deduplication scope changed");
  invariant(contextResult("repeat-same")?.result?.messages?.length === 1, "deduplication restored a quarantined result");
}
async function runTraceAndWriters(fixture) {
  const implementation = fixture.implementation;
  const traceDirectory = join(root, "trace-files");
  const extensionPath = join(traceDirectory, "extension", "trace.jsonl");
  const legacyPath = join(traceDirectory, "legacy", "trace.jsonl");
  const selected = implementation.createExtensionTrace({
    environment: { MECHA_PI_EXTENSION_TRACE_PATH: " " + extensionPath + " ", MECHA_PI_NATIVE_SUBAGENT_TRACE_PATH: legacyPath },
    now: () => new Date(fixedTime),
  });
  observe({ kind: "operation", name: "trace-selected" });
  observe({ kind: "operation-result", name: "trace-selected", value: selected.enabled && selected.record("selected-event") });
  const selectedSnapshot = fileSnapshot("trace-selected", extensionPath, true);
  invariant(selectedSnapshot.mode === 0o600 && selectedSnapshot.parentMode === 0o700, "trace selected permissions changed");
  observe({ kind: "operation", name: "trace-legacy-untouched" });
  observe({ kind: "operation-result", name: "trace-legacy-untouched", value: existsSync(legacyPath) });

  const fallback = implementation.createExtensionTrace({
    environment: { MECHA_PI_NATIVE_SUBAGENT_TRACE_PATH: legacyPath },
    now: () => new Date(fixedTime),
  });
  observe({ kind: "operation", name: "trace-legacy" });
  observe({ kind: "operation-result", name: "trace-legacy", value: fallback.record("legacy-event") });
  const legacySnapshot = fileSnapshot("trace-legacy", legacyPath, true);
  invariant(legacySnapshot.mode === 0o600 && legacySnapshot.parentMode === 0o700, "trace legacy permissions changed");

  observe({ kind: "operation", name: "trace-alias" });
  observe({ kind: "operation-result", name: "trace-alias", value: implementation.createNativeSubagentTrace === implementation.createExtensionTrace });
  const disabled = implementation.createExtensionTrace({ path: undefined });
  observe({ kind: "operation", name: "trace-disabled" });
  observe({ kind: "operation-result", name: "trace-disabled", value: disabled.enabled || disabled.record("disabled-event") });
  tryCall("trace-invalid-path", () => implementation.createExtensionTrace({ path: "" }));
  tryCall("trace-invalid-limit", () => implementation.createExtensionTrace({ path: join(root, "invalid-limit.jsonl"), maxBytes: 0 }));
  tryCall("trace-invalid-clock", () => implementation.createExtensionTrace({ path: join(root, "invalid-clock.jsonl"), now: "bad" }));

  const filteredPath = join(traceDirectory, "filtered", "trace.jsonl");
  const filtered = implementation.createExtensionTrace({ path: filteredPath, now: () => new Date(fixedTime) });
  observe({ kind: "operation", name: "trace-filtered" });
  observe({ kind: "operation-result", name: "trace-filtered", value: filtered.record("filtered-event", {
    agent_id: "a".repeat(200),
    bytes: 4,
    available: true,
    negative: -1,
    fraction: 1.5,
    unknown: "secret trace marker",
    subagents: [
      { id: " child ", type: " worker ", status: "running", description: "secret prompt" },
      { id: "" },
      "bad",
    ],
  }) });
  const filteredSnapshot = fileSnapshot("trace-filtered", filteredPath, true);
  invariant(!JSON.stringify(filteredSnapshot).includes("secret"), "trace leaked source text");
  tryCall("trace-invalid-event", () => filtered.record("Bad event"));
  tryCall("trace-invalid-fields", () => filtered.record("filtered-event", null));

  const rotationPath = join(traceDirectory, "rotation", "trace.jsonl");
  await mkdir(join(traceDirectory, "rotation"), { recursive: true });
  writeFileSync(rotationPath, "x".repeat(10), { mode: 0o600 });
  const rotating = implementation.createExtensionTrace({ path: rotationPath, maxBytes: 10, now: () => new Date(fixedTime) });
  observe({ kind: "operation", name: "trace-rotation" });
  observe({ kind: "operation-result", name: "trace-rotation", value: rotating.record("rotated-event") });
  fileSnapshot("trace-rotation", rotationPath, true);
  const badTarget = join(traceDirectory, "bad-target");
  writeFileSync(badTarget, "target");
  const failing = implementation.createExtensionTrace({ path: join(badTarget, "trace.jsonl") });
  observe({ kind: "operation", name: "trace-write-failure" });
  observe({ kind: "operation-result", name: "trace-write-failure", value: failing.record("failed-event") });

  const memoryPath = join(root, "writer-files", "memory.jsonl");
  call("memory-diagnostic-initial", () => implementation.appendMemoryCaptureDiagnostic({ timestamp: "2026-01-01T00:00:00.000Z", trigger: "compaction", status: "applied", reason: "memory-apply", records: 2, source: "secret memory marker", prompt: "secret prompt", error: "secret error" }, { path: memoryPath }));
  for (let index = 0; index < 20; index += 1) call("memory-diagnostic-tail-" + index, () => implementation.appendMemoryCaptureDiagnostic({ timestamp: "2026-01-01T00:00:" + String(index).padStart(2, "0") + ".000Z", trigger: "shutdown", status: "noop", reason: "bounded" }, { path: memoryPath, maxBytes: 260, keepBytes: 130 }));
  const memorySnapshot = fileSnapshot("memory", memoryPath);
  invariant(memorySnapshot.mode === 0o600 && memorySnapshot.size <= 260 && memorySnapshot.rotation === undefined, "memory writer permissions, bound, or rotation changed");
  invariant(!JSON.stringify(memorySnapshot).includes("secret"), "memory writer leaked source text");

  const orphanPath = join(root, "writer-files", "orphan.jsonl");
  call("orphan-diagnostic-initial", () => implementation.appendOrphanToolResultDiagnostic({ sessionId: "session", sessionFile: "sessions/parent-session.jsonl", model: "model", agentId: "agent", totalMessages: 5, quarantinedCount: 1, source: "secret orphan marker", orphans: [{ messageIndex: 2, toolCallId: "raw|suffix", providerToolCallId: "raw", toolName: "bash", previousRole: "assistant", nextRole: "user", nearestAssistantIndex: 1, nearestAssistantStopReason: "error", nearestAssistantToolCallIds: ["raw"], matchingToolCallLater: true, output: "secret output" }] }, { path: orphanPath }));
  for (let index = 0; index < 20; index += 1) call("orphan-diagnostic-tail-" + index, () => implementation.appendOrphanToolResultDiagnostic({ sessionId: "session", orphans: [{ toolCallId: "call-" + index, toolName: "bash" }] }, { path: orphanPath, maxBytes: 260, keepBytes: 130 }));
  const orphanSnapshot = fileSnapshot("orphan", orphanPath);
  invariant(orphanSnapshot.mode === 0o600 && orphanSnapshot.size <= 260 && orphanSnapshot.rotation === undefined, "orphan writer permissions, bound, or rotation changed");
  invariant(!JSON.stringify(orphanSnapshot).includes("secret"), "orphan writer leaked source text");

  const direct = call("quarantine-orphan", () => implementation.quarantineOrphanToolResults([resultBlock("missing"), assistant([callBlock("missing", "bash")]), resultBlock("missing|suffix")]));
  observe({ kind: "direct-result", name: "quarantine-orphan", value: direct });
}
async function runDiagnosticFailure(fixture) {
  const memoryPath = process.env.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH;
  const orphanPath = process.env.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH;
  invariant(typeof memoryPath === "string" && typeof orphanPath === "string", "diagnostic paths are missing");
  await mkdir(memoryPath, { recursive: true });
  await mkdir(orphanPath, { recursive: true });
  call("memory-writer-failure", () => fixture.implementation.appendMemoryCaptureDiagnostic({ source: "secret" }));
  call("orphan-writer-failure", () => fixture.implementation.appendOrphanToolResultDiagnostic({ orphans: [] }));
  observe({ kind: "writer-error-outcome", writer: "memory", returned: true, remainsDirectory: statSync(memoryPath).isDirectory() });
  observe({ kind: "writer-error-outcome", writer: "orphan", returned: true, remainsDirectory: statSync(orphanPath).isDirectory() });
  const parent = createContext("parent-session", undefined);
  await contextCall(fixture.pi, "diagnostic-failure-context", parent, malformedHistory("bad", "orphan"));
  invariant(contextResult("diagnostic-failure-context")?.result?.messages?.length === 1, "diagnostic failure changed provider-facing messages");
  invariant(observations.some((row) => row.kind === "ui" && row.message === "Quarantined malformed tool call(s): bad name"), "malformed notice was lost after writer failure");
  invariant(observations.some((row) => row.kind === "ui" && row.message === "Quarantined orphan tool result(s) before provider request: orphan"), "orphan notice was lost after writer failure");
}
async function runContextOrder(fixture) {
  const usage = { percent: 50, tokens: 2048, contextWindow: 4096 };
  const parent = createContext("parent-session", usage);
  const child = createContext("child-session", usage);
  call("mark-child-session", () => fixture.pi.events.emit("subagents:child:session-created", { sessionId: "child-session" }));
  await contextCall(fixture.pi, "context-order-parent", parent, malformedHistory("bad", "bad"));
  fileSnapshot("context-order-parent-orphan", join(root, "diagnostics", "orphan-tool-results.jsonl"));
  await contextCall(fixture.pi, "context-order-child", child, malformedHistory("child-bad", "child-orphan"));
  fileSnapshot("context-order-child-orphan", join(root, "diagnostics", "orphan-tool-results.jsonl"));
  const rows = observations;
  const index = (predicate, from = 0) => rows.findIndex((row, rowIndex) => rowIndex >= from && predicate(row));
  const usageIndex = index((row) => row.kind === "usage-read" && row.sessionId === "parent-session");
  const compactIndex = index((row) => row.kind === "compact" && row.sessionId === "parent-session");
  const abortIndex = index((row) => row.kind === "context" && row.action === "abort" && row.sessionId === "parent-session");
  const parentReadIndex = index((row) => row.kind === "message-read" && row.label === "context-order-parent");
  const childReadIndex = index((row) => row.kind === "message-read" && row.label === "context-order-child");
  invariant(usageIndex >= 0 && abortIndex > usageIndex && compactIndex > abortIndex && parentReadIndex > compactIndex, "hard compaction ordering changed");
  invariant(childReadIndex >= 0, "child sanitation did not run");
  invariant(!rows.some((row) => row.kind === "compact" && row.sessionId === "child-session"), "child hard compaction ran");
  invariant(!rows.some((row) => row.kind === "context" && row.action === "abort" && row.sessionId === "child-session"), "child abort ran");
  invariant(!rows.some((row) => row.kind === "usage-read" && row.sessionId === "child-session"), "child usage gate ran");
}
async function runScenario(fixture) {
  if (scenario === "clean-history") return runCleanHistory(fixture);
  if (scenario === "malformed-matrix") return runMalformedMatrix(fixture);
  if (scenario === "malformed-causes-orphan") return runMalformedCausesOrphan(fixture);
  if (scenario === "orphan-ownership") return runOrphanOwnership(fixture);
  if (scenario === "later-match") return runLaterMatch(fixture);
  if (scenario === "repeat-and-scope") return runRepeatAndScope(fixture);
  if (scenario === "trace-and-writers") return runTraceAndWriters(fixture);
  if (scenario === "diagnostic-failure") return runDiagnosticFailure(fixture);
  if (scenario === "context-order") return runContextOrder(fixture);
  throw new Error("unknown scenario: " + scenario);
}

try {
  const sourceRoot = root;
  const implementation = await import(pathToFileURL(join(sourceRoot, "pi-extensions", "mecha-messaging", "index.ts")).href);
  const fixture = await createFixture(implementation);
  await runScenario(fixture);
  invariant(unexpectedCalls.length === 0, "unexpected external calls: " + JSON.stringify(unexpectedCalls));
  invariant(fetchCalls.length === 0, "unexpected fetch calls: " + JSON.stringify(fetchCalls));
  invariant(timeoutHandles.every((handle) => handle.cleared !== true), "timeout handle was not cleared");
  invariant(intervalHandles.every((handle) => handle.cleared !== true), "interval handle was not cleared");
  observe({ kind: "final", unexpectedCalls, fetchCalls, activeTimeouts: timeoutHandles.filter((handle) => handle.cleared !== true).length, activeIntervals: intervalHandles.filter((handle) => handle.cleared !== true).length });
  process.stdout.write(JSON.stringify({ scenario, observations, unexpectedCalls }) + "\n");
} catch (error) {
  process.stderr.write(String(error?.stack ?? error));
  process.exitCode = 1;
}
`;

type ChildOutput = {
  scenario: string;
  observations: unknown[];
  unexpectedCalls: unknown[];
};

type RecordValue = Record<string, unknown>;

function isRecord(value: unknown): value is RecordValue {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function describe(value: unknown): string {
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function recordAt(value: unknown, key: string): RecordValue {
  assert.ok(isRecord(value), `expected a record before reading ${key}`);
  const nested = value[key];
  assert.ok(isRecord(nested), `expected record field ${key}`);
  return nested;
}

function arrayAt(value: unknown, key: string): unknown[] {
  assert.ok(isRecord(value), `expected a record before reading ${key}`);
  const nested = value[key];
  assert.ok(Array.isArray(nested), `expected array field ${key}`);
  return nested;
}

function setField(value: unknown, key: string, next: unknown): void {
  assert.ok(isRecord(value), `expected a record before writing ${key}`);
  value[key] = next;
}

function observationAt(output: ChildOutput, index: number): RecordValue {
  const observation = output.observations[index];
  assert.ok(isRecord(observation), `expected observation ${index} to be a record`);
  return observation;
}

function mismatch(scenario: string, observationIndex: number, path: string, left: unknown, right: unknown, observation: unknown): never {
  const operation = isRecord(observation)
    ? String(observation.name ?? observation.event ?? observation.action ?? observation.kind ?? "unknown")
    : "unknown";
  throw new Error(`${scenario}: mismatch at observation ${observationIndex}, operation ${operation}, field ${path}: baseline=${describe(left)} current=${describe(right)}`);
}

function compareValue(
  scenario: string,
  observationIndex: number,
  path: string,
  left: unknown,
  right: unknown,
  observation: unknown,
): void {
  const allowlisted = DIFFERENTIAL_ALLOWLIST.some((entry) => path === entry.path
    && isRecord(observation)
    && observation.kind === entry.kind
    && observation.event === entry.event);
  if (allowlisted) {
    if (typeof left !== "string" || typeof right !== "string") mismatch(scenario, observationIndex, path, left, right, observation);
    return;
  }
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) mismatch(scenario, observationIndex, path, left, right, observation);
    for (let index = 0; index < left.length; index += 1) compareValue(scenario, observationIndex, `${path}[${index}]`, left[index], right[index], observation);
    return;
  }
  if (isRecord(left) || isRecord(right)) {
    if (!isRecord(left) || !isRecord(right)) mismatch(scenario, observationIndex, path, left, right, observation);
    const leftKeys = Object.keys(left).sort();
    const rightKeys = Object.keys(right).sort();
    if (describe(leftKeys) !== describe(rightKeys)) mismatch(scenario, observationIndex, path, leftKeys, rightKeys, observation);
    for (const key of leftKeys) compareValue(scenario, observationIndex, `${path}.${key}`, left[key], right[key], observation);
    return;
  }
  if (!Object.is(left, right)) mismatch(scenario, observationIndex, path, left, right, observation);
}

function compareScenario(scenario: string, baseline: ChildOutput, current: ChildOutput): void {
  compareValue(scenario, -1, "$.scenario", baseline.scenario, current.scenario, undefined);
  compareValue(scenario, -1, "$.unexpectedCalls", baseline.unexpectedCalls, current.unexpectedCalls, undefined);
  if (baseline.observations.length !== current.observations.length) {
    throw new Error(`${scenario}: observation count differs at index ${Math.min(baseline.observations.length, current.observations.length)}: baseline=${baseline.observations.length} current=${current.observations.length}`);
  }
  for (let index = 0; index < baseline.observations.length; index += 1) {
    compareValue(scenario, index, "$", baseline.observations[index], current.observations[index], baseline.observations[index]);
  }
}

function parseChildOutput(stdout: string, scenario: string): ChildOutput {
  const lines = stdout.trim().split("\n").filter(Boolean);
  assert.ok(lines.length > 0, `${scenario}: child produced no final JSON observation`);
  const parsed: unknown = JSON.parse(lines.at(-1) ?? "null");
  assert.ok(isRecord(parsed), `${scenario}: child result is not an object`);
  assert.equal(parsed.scenario, scenario);
  assert.ok(Array.isArray(parsed.observations), `${scenario}: child observations are missing`);
  assert.ok(Array.isArray(parsed.unexpectedCalls), `${scenario}: child unexpected calls are missing`);
  return parsed as ChildOutput;
}

async function runChild(runnerPath: string, scenario: string, childRoot: string): Promise<ChildOutput> {
  // Each scenario gets a fresh runtime state while retaining its isolated source tree.
  for (const runtimePath of ["home", "diagnostics", "sessions", "writer-files", "trace-files"]) {
    await rm(join(childRoot, runtimePath), { recursive: true, force: true });
  }
  const environment = {
    ...process.env,
    HOME: join(childRoot, "home"),
    PI_CODING_AGENT_DIR: join(childRoot, "pi-agent"),
    MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH: join(childRoot, "diagnostics", "memory-capture.jsonl"),
    MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH: join(childRoot, "diagnostics", "orphan-tool-results.jsonl"),
    MECHA_PI_EXTENSION_TRACE_PATH: join(childRoot, "diagnostics", "extension-trace.jsonl"),
    MECHA_PI_NATIVE_SUBAGENT_TRACE_PATH: join(childRoot, "diagnostics", "legacy-trace.jsonl"),
    MECHA_PI_COMPACTION_POLICY: JSON.stringify({ levels: [{ percent: 50, mandatory: true }] }),
    MECHA_ROUTER_URL: "http://127.0.0.1:8765",
  };
  let result: { stdout: string; stderr: string };
  try {
    result = await execFileAsync(process.execPath, [TSX, runnerPath, scenario, childRoot], {
      cwd: childRoot,
      env: environment,
      timeout: 60_000,
      maxBuffer: 8 * 1024 * 1024,
    }) as { stdout: string; stderr: string };
  } catch (error) {
    const details = isRecord(error) ? error : { error };
    throw new Error(`${scenario}: child failed: ${String(details.stderr ?? details.message ?? details.error ?? error)}`);
  }
  return parseChildOutput(result.stdout, scenario);
}

async function archiveBaseline(destination: string): Promise<void> {
  await new Promise<void>((resolveArchive, reject) => {
    const child = spawn("git", ["archive", "--format=tar", BASELINE_COMMIT, "pi-extensions/mecha-messaging", "pi-extensions/mecha-goal"], {
      cwd: ROOT,
      stdio: ["ignore", "pipe", "pipe"],
    });
    const chunks: Buffer[] = [];
    const errors: Buffer[] = [];
    child.stdout.on("data", (chunk: Buffer) => chunks.push(Buffer.from(chunk)));
    child.stderr.on("data", (chunk: Buffer) => errors.push(Buffer.from(chunk)));
    child.on("error", reject);
    child.on("close", (code) => {
      if (code !== 0) {
        reject(new Error(`git archive failed (${code}): ${Buffer.concat(errors).toString("utf8")}`));
        return;
      }
      writeFile(destination, Buffer.concat(chunks)).then(() => resolveArchive(), reject);
    });
  });
}

async function copyPackageTree(source: string, destination: string): Promise<void> {
  await mkdir(destination, { recursive: true });
  for (const entry of await readdir(source, { withFileTypes: true })) {
    if (entry.name === "node_modules") continue;
    await cp(join(source, entry.name), join(destination, entry.name), { recursive: entry.isDirectory() });
  }
}

async function materializeRoots(runDirectory: string): Promise<{ baselineRoot: string; currentRoot: string }> {
  const baselineRoot = join(runDirectory, "baseline-root");
  const currentRoot = join(runDirectory, "current-root");
  const archivePath = join(runDirectory, "baseline.tar");
  await mkdir(baselineRoot, { recursive: true });
  await archiveBaseline(archivePath);
  await execFileAsync("tar", ["-xf", archivePath, "-C", baselineRoot]);
  await copyPackageTree(join(ROOT, "pi-extensions", "mecha-messaging"), join(currentRoot, "pi-extensions", "mecha-messaging"));
  const baselineIndexPath = join(baselineRoot, "pi-extensions", "mecha-messaging", "index.ts");
  const baselineIndex = await readFile(baselineIndexPath, "utf8");
  assert.equal(createHash("sha256").update(baselineIndex).digest("hex"), BASELINE_INDEX_SHA256);
  assert.equal(Buffer.byteLength(baselineIndex), BASELINE_INDEX_BYTES);
  assert.match(baselineIndex, /function quarantineMalformedToolCalls/);
  assert.match(baselineIndex, /function createExtensionTrace|export function createExtensionTrace/);
  const blob = (await execFileAsync("git", ["hash-object", baselineIndexPath], { cwd: baselineRoot })).stdout.trim();
  assert.equal(blob, BASELINE_INDEX_BLOB);
  return { baselineRoot, currentRoot };
}

function testComparatorSelfChecks(): void {
  const baseline: ChildOutput = {
    scenario: "self",
    unexpectedCalls: [],
    observations: [
      { kind: "trace", event: "malformed-tool-call", fields: { timestamp: "baseline-time", marker: "same" } },
      { kind: "context-return", result: { messages: [{ role: "assistant", content: ["same"] }] } },
      { kind: "file-record", writer: "orphan", records: [{ timestamp: "orphan-a" }] },
      { kind: "file-record", writer: "memory", records: [{ timestamp: "memory-a" }] },
    ],
  };
  const timestampOnly = structuredClone(baseline);
  timestampOnly.observations[0] = { kind: "trace", event: "malformed-tool-call", fields: { timestamp: "current-time", marker: "same" } };
  compareScenario("comparator-timestamp-allowlist", baseline, timestampOnly);

  const changedMessage = structuredClone(baseline);
  arrayAt(arrayAt(recordAt(observationAt(changedMessage, 1), "result"), "messages")[0], "content")[0] = "changed";
  assert.throws(() => compareScenario("comparator-message", baseline, changedMessage), /observation 1.*content\[0\]/);

  const extraNotice = structuredClone(baseline);
  extraNotice.observations.splice(2, 0, { kind: "ui", action: "notify", message: "extra", level: "error" });
  assert.throws(() => compareScenario("comparator-extra-notice", baseline, extraNotice), /observation count differs/);

  const changedTraceField = structuredClone(baseline);
  recordAt(observationAt(changedTraceField, 0), "fields").marker = "changed";
  assert.throws(() => compareScenario("comparator-trace-field", baseline, changedTraceField), /fields\.marker/);

  const reordered = structuredClone(baseline);
  [reordered.observations[1], reordered.observations[2]] = [reordered.observations[2], reordered.observations[1]];
  assert.throws(() => compareScenario("comparator-order", baseline, reordered), /observation 1/);

  const changedOrphanTimestamp = structuredClone(baseline);
  setField(arrayAt(observationAt(changedOrphanTimestamp, 2), "records")[0], "timestamp", "orphan-b");
  assert.throws(() => compareScenario("comparator-orphan-timestamp", baseline, changedOrphanTimestamp), /records\[0\]\.timestamp/);

  const changedMemoryTimestamp = structuredClone(baseline);
  setField(arrayAt(observationAt(changedMemoryTimestamp, 3), "records")[0], "timestamp", "memory-b");
  assert.throws(() => compareScenario("comparator-memory-timestamp", baseline, changedMemoryTimestamp), /records\[0\]\.timestamp/);

  const nestedArrayMutation = structuredClone(baseline);
  arrayAt(arrayAt(recordAt(observationAt(nestedArrayMutation, 1), "result"), "messages")[0], "content").push("nested");
  assert.throws(() => compareScenario("comparator-nested-array", baseline, nestedArrayMutation), /content/);

  const otherTraceBaseline = structuredClone(baseline);
  setField(observationAt(otherTraceBaseline, 0), "event", "other-event");
  const otherTraceEvent = structuredClone(otherTraceBaseline);
  setField(recordAt(observationAt(otherTraceEvent, 0), "fields"), "timestamp", "other-time");
  assert.throws(() => compareScenario("comparator-other-trace-event", otherTraceBaseline, otherTraceEvent), /fields\.timestamp/);

  const nestedMalformedTimestamp = structuredClone(baseline);
  setField(recordAt(observationAt(nestedMalformedTimestamp, 0), "fields"), "nested", { timestamp: "nested-time" });
  const nestedMalformedCurrent = structuredClone(nestedMalformedTimestamp);
  setField(recordAt(recordAt(observationAt(nestedMalformedCurrent, 0), "fields"), "nested"), "timestamp", "changed-nested-time");
  assert.throws(() => compareScenario("comparator-nested-timestamp", nestedMalformedTimestamp, nestedMalformedCurrent), /fields\.nested\.timestamp/);
}

test("the differential comparator has a closed, failing normalization allowlist", () => {
  testComparatorSelfChecks();
});

test("diagnostics and history match the immutable pre-slice baseline", { timeout: 180_000 }, async () => {
  const runDirectory = await mkdtemp(join(tmpdir(), "mecha-diagnostics-history-differential-"));
  const runnerPath = join(runDirectory, "child-runner.mjs");
  try {
    await writeFile(runnerPath, CHILD_RUNNER, { mode: 0o600 });
    const { baselineRoot, currentRoot } = await materializeRoots(runDirectory);
    for (const scenario of SCENARIOS) {
      const baseline = await runChild(runnerPath, scenario, baselineRoot);
      const current = await runChild(runnerPath, scenario, currentRoot);
      assert.deepEqual(baseline.unexpectedCalls, []);
      assert.deepEqual(current.unexpectedCalls, []);
      compareScenario(scenario, baseline, current);
    }
  } finally {
    await rm(runDirectory, { recursive: true, force: true });
  }
});
