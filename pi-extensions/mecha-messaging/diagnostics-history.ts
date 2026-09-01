import { createHash } from "node:crypto";
import { appendFileSync, existsSync, mkdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { appendFile, chmod, mkdir, rename, rm, stat } from "node:fs/promises";
import { dirname } from "node:path";

import { summarizeNativeSubagents } from "./activity-lifecycle.js";
import type { NativeSubagent } from "./activity-lifecycle.js";

export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonObject | JsonValue[] | undefined;
export type JsonObject = { [key: string]: JsonValue };

export type ContentBlock = unknown;
export type HistoryMessage = {
  [key: string]: unknown;
  role?: string;
  stopReason?: string;
  toolCallId?: string;
  toolName?: string;
  content?: unknown;
};

export type MalformedToolCall = {
  messageIndex: number;
  blockIndex: number;
  toolName: string | undefined;
  toolNameLength: number | undefined;
  toolCallId: string | undefined;
};

export type OrphanToolResult = {
  messageIndex: number;
  toolCallId: string | undefined;
  providerToolCallId: string | undefined;
  toolName: string | undefined;
  previousRole: string | undefined;
  nextRole: string | undefined;
  nearestAssistantIndex: number | undefined;
  nearestAssistantStopReason: string | undefined;
  nearestAssistantToolCallIds: string[];
  matchingToolCallLater: boolean;
};

export type ContextHistoryEvent = { messages?: HistoryMessage[] };

export type ContextHistorySanitization = {
  messages: HistoryMessage[];
  // Pi observes the malformed-filtered event list, while provider work receives orphan removal.
  messagesBeforeOrphanQuarantine: HistoryMessage[];
  malformed: MalformedToolCall[];
  orphaned: OrphanToolResult[];
};

export type ExtensionTrace = {
  enabled: boolean;
  record: (event: string, fields?: object) => boolean;
};

type Environment = NodeJS.ProcessEnv;
const MEMORY_CAPTURE_DIAGNOSTIC_MAX_BYTES = 128 * 1024;
const MEMORY_CAPTURE_DIAGNOSTIC_KEEP_BYTES = 64 * 1024;
const ORPHAN_TOOL_RESULT_DIAGNOSTIC_MAX_BYTES = 256 * 1024;
const ORPHAN_TOOL_RESULT_DIAGNOSTIC_KEEP_BYTES = 128 * 1024;
const EXTENSION_TRACE_PATH_ENV = "MECHA_PI_EXTENSION_TRACE_PATH";
const LEGACY_NATIVE_SUBAGENT_TRACE_PATH_ENV = "MECHA_PI_NATIVE_SUBAGENT_TRACE_PATH";
const EXTENSION_TRACE_MAX_BYTES = 512 * 1024;

const NATIVE_SUBAGENT_TRACE_STRING_FIELDS = new Set([
  "agent_id", "instance_id", "parent_session_id", "session_id", "subagent_id", "event_id", "frame_id", "request_id", "ack_frame_id", "producer_epoch",
  "status", "reason", "error_code", "error_name", "source", "frame_type", "disposition", "state", "phase", "activity_state", "receipt_state", "lifecycle_event",
  "control_id", "source_agent_id", "target_agent_id", "lease_id", "action",
  "requested_provider", "requested_model_id", "current_provider", "current_model_id",
]);
const NATIVE_SUBAGENT_TRACE_NUMBER_FIELDS = new Set([
  "client_sequence", "router_version", "subagent_count", "snapshot_count", "pending", "bytes", "description_length",
  "recipient_count", "attempt", "heartbeat_interval_ms", "available_count", "model_count", "source_model_count",
  "frame_bytes", "connection_generation", "source_count", "started_count", "disposed_count", "cleared_count", "reconciled_count", "resumed_count", "remaining_count",
]);
const NATIVE_SUBAGENT_TRACE_BOOLEAN_FIELDS = new Set([
  "available", "registry_available", "lifecycle_service", "connected", "ready", "reconnect", "published",
  "accepted", "switched", "truncated",
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function memoryCaptureDiagnosticsPath(environment: Environment = process.env): string | undefined {
  if (typeof environment.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH === "string" && environment.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH.trim().length > 0) return environment.MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH.trim();
  const home = environment.HOME;
  return typeof home === "string" && home.length > 0 ? `${home}/.mecha-developer/logs/memory-capture.jsonl` : undefined;
}

function normalizeMemoryCaptureDiagnosticReason(reason: string | undefined): string {
  return typeof reason === "string" && /^[a-z0-9-]{1,120}$/.test(reason) ? reason : "capture-failed";
}

// Memory and orphan records keep a bounded tail in the active file. Trace rotation is separate.
function appendBoundedJsonl(record: JsonObject, { path, maxBytes, keepBytes }: { path?: string; maxBytes: number; keepBytes: number }): void {
  if (path === undefined) return;
  try {
    mkdirSync(path.replace(/\/[^/]*$/, ""), { recursive: true });
    appendFileSync(path, `${JSON.stringify(record)}\n`, { encoding: "utf8", mode: 0o600 });
    if (statSync(path).size <= maxBytes) return;
    const lines = readFileSync(path, "utf8").slice(-keepBytes).split("\n").slice(1).filter(Boolean);
    writeFileSync(path, `${lines.join("\n")}\n`, { encoding: "utf8", mode: 0o600 });
  } catch {
    // Diagnostics are best-effort and must never expose source text or break runtime flow.
  }
}

export function appendMemoryCaptureDiagnostic(
  event: object,
  { path = memoryCaptureDiagnosticsPath(), maxBytes = MEMORY_CAPTURE_DIAGNOSTIC_MAX_BYTES, keepBytes = MEMORY_CAPTURE_DIAGNOSTIC_KEEP_BYTES }: { path?: string; maxBytes?: number; keepBytes?: number } = {},
): void {
  const diagnostic = isRecord(event) ? event : {};
  const record: JsonObject = {
    timestamp: typeof diagnostic.timestamp === "string" ? diagnostic.timestamp : new Date().toISOString(),
    trigger: typeof diagnostic.trigger === "string" && ["compaction", "shutdown"].includes(diagnostic.trigger) ? diagnostic.trigger : "unknown",
    status: typeof diagnostic.status === "string" && ["noop", "applied", "failed"].includes(diagnostic.status) ? diagnostic.status : "failed",
    reason: normalizeMemoryCaptureDiagnosticReason(typeof diagnostic.reason === "string" ? diagnostic.reason : undefined),
    records: typeof diagnostic.records === "number" && Number.isFinite(diagnostic.records) ? diagnostic.records : undefined,
  };
  appendBoundedJsonl(record, { path, maxBytes, keepBytes });
}

function summarizeTraceSubagents(value: unknown): Array<{ id: string; type?: string; status?: string; description_length: number }> {
  if (!Array.isArray(value)) return [];
  const subagents: NativeSubagent[] = [];
  for (const item of value) {
    if (!isRecord(item)) continue;
    const subagent: NativeSubagent = {};
    if (typeof item.id === "string") subagent.id = item.id;
    if (typeof item.type === "string") subagent.type = item.type;
    if (typeof item.status === "string") subagent.status = item.status;
    if (typeof item.description === "string") subagent.description = item.description;
    subagents.push(subagent);
  }
  return summarizeNativeSubagents(subagents);
}

export function createExtensionTrace({
  environment = process.env,
  path = typeof environment?.[EXTENSION_TRACE_PATH_ENV] === "string" && environment[EXTENSION_TRACE_PATH_ENV].trim().length > 0
    ? environment[EXTENSION_TRACE_PATH_ENV].trim()
    : typeof environment?.[LEGACY_NATIVE_SUBAGENT_TRACE_PATH_ENV] === "string"
      && environment[LEGACY_NATIVE_SUBAGENT_TRACE_PATH_ENV].trim().length > 0
      ? environment[LEGACY_NATIVE_SUBAGENT_TRACE_PATH_ENV].trim()
      : undefined,
  maxBytes = EXTENSION_TRACE_MAX_BYTES,
  now = () => new Date(),
}: { environment?: Environment; path?: string; maxBytes?: number; now?: () => Date } = {}): ExtensionTrace {
  if (path === undefined) return { enabled: false, record: (_event: string, _fields?: object) => false };
  if (typeof path !== "string" || path.length === 0) throw new TypeError("extension trace path is invalid");
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) throw new TypeError("extension trace maxBytes must be a positive safe integer");
  if (typeof now !== "function") throw new TypeError("extension trace clock must be a function");
  return {
    enabled: true,
    record(event: string, fields: object = {}) {
      try {
        if (typeof event !== "string" || !/^[a-z][a-z0-9_.-]{0,80}$/.test(event)) throw new TypeError("extension trace event is invalid");
        if (fields === null || typeof fields !== "object" || Array.isArray(fields)) throw new TypeError("extension trace fields are invalid");
        const timestamp = now();
        if (!(timestamp instanceof Date) || Number.isNaN(timestamp.getTime())) throw new TypeError("extension trace clock is invalid");
        const record: JsonObject = { timestamp: timestamp.toISOString(), event };
        for (const [key, value] of Object.entries(fields)) {
          if (NATIVE_SUBAGENT_TRACE_STRING_FIELDS.has(key) && typeof value === "string") record[key] = value.slice(0, 128);
          else if (NATIVE_SUBAGENT_TRACE_NUMBER_FIELDS.has(key) && typeof value === "number" && Number.isSafeInteger(value) && value >= 0) record[key] = value;
          else if (NATIVE_SUBAGENT_TRACE_BOOLEAN_FIELDS.has(key) && typeof value === "boolean") record[key] = value;
          // Keep this summary behind the same bounded activity helper as lifecycle traces.
          else if (key === "subagents") record.subagents = summarizeTraceSubagents(value);
        }
        mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
        try {
          if (statSync(path).size >= maxBytes) {
            rmSync(`${path}.1`, { force: true });
            renameSync(path, `${path}.1`);
          }
        } catch (error) {
          if (isRecord(error) && error.code !== "ENOENT") throw error;
        }
        appendFileSync(path, `${JSON.stringify(record)}\n`, { encoding: "utf8", mode: 0o600 });
        return true;
      } catch {
        // Diagnostics cannot change extension behavior or reveal failure details.
        return false;
      }
    },
  };
}

export const createNativeSubagentTrace: typeof createExtensionTrace = createExtensionTrace;

function orphanToolResultDiagnosticsPath(environment: Environment = process.env): string | undefined {
  if (typeof environment.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH === "string" && environment.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH.trim().length > 0) return environment.MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH.trim();
  const home = environment.HOME;
  return typeof home === "string" && home.length > 0 ? `${home}/.mecha-developer/logs/orphan-tool-results.jsonl` : undefined;
}

function providerToolCallId(toolCallId: string | undefined): string | undefined {
  return typeof toolCallId === "string" && toolCallId.length > 0 ? toolCallId.split("|", 1)[0] : undefined;
}

function safeMessageRole(message: HistoryMessage | undefined): string | undefined {
  return typeof message?.role === "string" ? message.role : undefined;
}

function safeStopReason(message: HistoryMessage | undefined): string | undefined {
  return typeof message?.stopReason === "string" ? message.stopReason : undefined;
}

function messageToolCallIds(message: HistoryMessage | undefined): string[] {
  if (!Array.isArray(message?.content)) return [];
  return message.content
    .map((block) => isRecord(block) && block.type === "toolCall" && typeof block.id === "string" ? providerToolCallId(block.id) : undefined)
    .filter((toolCallId): toolCallId is string => toolCallId !== undefined);
}

export function appendOrphanToolResultDiagnostic(
  event: JsonObject | null | undefined,
  { path = orphanToolResultDiagnosticsPath(), maxBytes = ORPHAN_TOOL_RESULT_DIAGNOSTIC_MAX_BYTES, keepBytes = ORPHAN_TOOL_RESULT_DIAGNOSTIC_KEEP_BYTES }: { path?: string; maxBytes?: number; keepBytes?: number } = {},
): void {
  try {
    const diagnostic = event ?? {};
    const diagnosticOrphans: Array<Record<string, unknown>> = [];
    if (Array.isArray(diagnostic.orphans)) {
      for (const orphan of diagnostic.orphans) {
        if (isRecord(orphan)) diagnosticOrphans.push(orphan);
      }
    }
    const record: JsonObject = {
      timestamp: new Date().toISOString(),
      session_id: typeof diagnostic.sessionId === "string" ? diagnostic.sessionId : undefined,
      session_file: typeof diagnostic.sessionFile === "string" ? diagnostic.sessionFile : undefined,
      model: typeof diagnostic.model === "string" ? diagnostic.model : undefined,
      agent_id: typeof diagnostic.agentId === "string" ? diagnostic.agentId : undefined,
      total_messages: Number.isInteger(diagnostic.totalMessages) ? diagnostic.totalMessages : undefined,
      quarantined_count: Number.isInteger(diagnostic.quarantinedCount) ? diagnostic.quarantinedCount : undefined,
      orphans: diagnosticOrphans.map((orphan): JsonObject => ({
        message_index: typeof orphan.messageIndex === "number" && Number.isInteger(orphan.messageIndex) ? orphan.messageIndex : undefined,
        tool_call_id: typeof orphan.toolCallId === "string" ? orphan.toolCallId : undefined,
        provider_tool_call_id: typeof orphan.providerToolCallId === "string" ? orphan.providerToolCallId : undefined,
        tool_name: typeof orphan.toolName === "string" ? orphan.toolName : undefined,
        previous_role: typeof orphan.previousRole === "string" ? orphan.previousRole : undefined,
        next_role: typeof orphan.nextRole === "string" ? orphan.nextRole : undefined,
        nearest_assistant_index: typeof orphan.nearestAssistantIndex === "number" && Number.isInteger(orphan.nearestAssistantIndex) ? orphan.nearestAssistantIndex : undefined,
        nearest_assistant_stop_reason: typeof orphan.nearestAssistantStopReason === "string" ? orphan.nearestAssistantStopReason : undefined,
        nearest_assistant_tool_call_ids: Array.isArray(orphan.nearestAssistantToolCallIds) ? orphan.nearestAssistantToolCallIds : undefined,
        matching_tool_call_later: orphan.matchingToolCallLater === true,
      })),
    };
    appendBoundedJsonl(record, { path, maxBytes, keepBytes });
  } catch {
    // Orphan diagnostics are best-effort and must never affect provider flow.
  }
}

const VALID_TOOL_NAME_PATTERN = /^[a-zA-Z0-9_-]+$/;
const MAX_TOOL_NAME_LENGTH = 64;

function isValidToolName(name: string | undefined): boolean {
  return typeof name === "string"
    && name.length > 0
    && name.length <= MAX_TOOL_NAME_LENGTH
    && VALID_TOOL_NAME_PATTERN.test(name);
}

export function quarantineMalformedToolCalls(messages: HistoryMessage[]): { messages: HistoryMessage[]; malformed: MalformedToolCall[] } {
  const sanitized: HistoryMessage[] = [];
  const malformed: MalformedToolCall[] = [];

  for (const [index, message] of messages.entries()) {
    if (message?.role === "assistant" && Array.isArray(message.content)) {
      const sanitizedContent: ContentBlock[] = [];
      for (const [blockIndex, block] of message.content.entries()) {
        if (isRecord(block) && (block.type === "toolCall" || block.type === "tool_use")) {
          const name = typeof block.name === "string" ? block.name : undefined;
          if (!isValidToolName(name)) {
            malformed.push({
              messageIndex: index,
              blockIndex,
              toolName: name,
              toolNameLength: typeof name === "string" ? name.length : undefined,
              toolCallId: typeof block.id === "string" ? block.id : undefined,
            });
            continue;
          }
        }
        sanitizedContent.push(block);
      }
      sanitized.push({ ...message, content: sanitizedContent });
    } else {
      sanitized.push(message);
    }
  }

  return { messages: sanitized, malformed };
}

function assistantToolCallIds(message: HistoryMessage | undefined): string[] {
  if (message?.role !== "assistant"
    // Failed calls are retained for diagnostics but cannot establish provider ownership.
    || message.stopReason === "error"
    || message.stopReason === "aborted"
    || !Array.isArray(message.content)) return [];
  return message.content
    // tool_use is a validation alias only; ownership remains on the historical toolCall shape.
    .map((block) => isRecord(block) && block.type === "toolCall" && typeof block.id === "string" ? providerToolCallId(block.id) : undefined)
    .filter((toolCallId): toolCallId is string => toolCallId !== undefined);
}

export function quarantineOrphanToolResults(messages: HistoryMessage[]): { messages: HistoryMessage[]; orphaned: OrphanToolResult[] } {
  const seenToolCalls = new Set<string>();
  const orphaned: OrphanToolResult[] = [];
  const sanitized: HistoryMessage[] = [];
  let nearestAssistant: HistoryMessage | undefined;
  let nearestAssistantIndex: number | undefined;

  for (const [index, message] of messages.entries()) {
    if (message?.role === "toolResult") {
      const toolCallId = providerToolCallId(message.toolCallId);
      // An empty normalized provider ID from "|suffix" is usable. Only undefined means no ID.
      if (toolCallId !== undefined && !seenToolCalls.has(toolCallId)) {
        orphaned.push({
          messageIndex: index,
          toolCallId: message.toolCallId,
          providerToolCallId: toolCallId,
          toolName: message.toolName,
          previousRole: safeMessageRole(messages[index - 1]),
          nextRole: safeMessageRole(messages[index + 1]),
          nearestAssistantIndex,
          nearestAssistantStopReason: safeStopReason(nearestAssistant),
          nearestAssistantToolCallIds: messageToolCallIds(nearestAssistant),
          matchingToolCallLater: messages.slice(index + 1).some((candidate) => messageToolCallIds(candidate).includes(toolCallId)),
        });
        continue;
      }
    }

    sanitized.push(message);
    if (message?.role === "assistant") {
      nearestAssistant = message;
      nearestAssistantIndex = index;
    }
    for (const seenToolCallId of assistantToolCallIds(message)) seenToolCalls.add(seenToolCallId);
  }

  return orphaned.length === 0
    ? { messages, orphaned }
    : { messages: sanitized, orphaned };
}

export function sanitizeContextHistory(event: ContextHistoryEvent): ContextHistorySanitization {
  // Keep malformed filtering ahead of orphan filtering: removing a call can create the orphan.
  const { messages: validMessages, malformed } = quarantineMalformedToolCalls(event.messages ?? []);
  const { messages, orphaned } = quarantineOrphanToolResults(validMessages);
  return { messages, messagesBeforeOrphanQuarantine: validMessages, malformed, orphaned };
}

export function appendMalformedToolCallDiagnostic(
  { sessionId, sessionFile, model, agentId, totalMessages, malformedCount, malformed }: {
    sessionId: string;
    sessionFile?: string;
    model?: unknown;
    agentId?: string;
    totalMessages?: number;
    malformedCount: number;
    malformed: MalformedToolCall[];
  },
  trace: (event: string, fields: object) => unknown,
  options: { now?: () => Date } = {},
): void {
  try {
    const ts = (options.now ?? (() => new Date()))();
    const payload = {
      timestamp: ts.toISOString(),
      sessionId,
      sessionFile,
      model,
      agentId,
      totalMessages,
      malformedCount,
      malformed: malformed.slice(0, 20).map((m) => ({
        messageIndex: m.messageIndex,
        blockIndex: m.blockIndex,
        toolName: typeof m.toolName === "string" ? m.toolName.slice(0, 100) : String(m.toolName),
        toolNameLength: m.toolNameLength,
        toolCallId: m.toolCallId,
      })),
    };
    trace("malformed-tool-call", payload);
  } catch {
    // A malformed diagnostic cannot affect context sanitation or provider work.
  }
}

/**
 * Process-local diagnostics are deliberately separate from provider history. The global data
 * property gives independently loaded extension instances one writer, sequence, and queue.
 */
export type DiagnosticJournalRecord = {
  timestamp: string;
  sequence: number;
  source: string;
  event: string;
  [key: string]: JsonValue;
};

/**
 * Process-local journal counters. `accepted` counts records queued by record();
 * `written` counts records successfully appended. Both counters saturate so
 * long-lived Pi sessions cannot turn health reporting into another unbounded store.
 */
export type DiagnosticJournalHealth = {
  accepted: number;
  written: number;
  dropped: number;
  writeFailures: number;
  queueDepth: number;
};

export type DiagnosticJournal = {
  readonly path: string | undefined;
  readonly closed: boolean;
  readonly closing: boolean;
  readonly reusable: boolean;
  record: (source: string, event: string, fields?: object) => boolean;
  flush: () => Promise<void>;
  close: () => Promise<void>;
  health: () => DiagnosticJournalHealth;
};

type DiagnosticJournalOptions = {
  environment?: Environment;
  path?: string;
  windowId?: string;
  processId?: number;
  now?: () => Date;
  maxBytes?: number;
  queueLimit?: number;
  rotationAgeMs?: number;
  pruneAgeMs?: number;
};

type DiagnosticJournalGlobal = {
  version: 1;
  record: (source: string, event: string, fields?: object) => boolean;
  flush?: () => Promise<void>;
  close?: () => Promise<void>;
};

type ManagedDiagnosticJournal = DiagnosticJournal & {
  readonly stale: boolean;
  invalidate: () => void;
};

type DiagnosticJournalState = {
  journals: Map<string, ManagedDiagnosticJournal>;
  defaultJournal?: ManagedDiagnosticJournal;
};

const DIAGNOSTIC_JOURNAL_GLOBAL = Symbol.for("mecha.pi.diagnostic-journal.v1");
const DIAGNOSTIC_JOURNAL_STATE = Symbol.for("mecha.pi.diagnostic-journal.state.v1");
const DIAGNOSTIC_JOURNAL_PATH_ENV = "MECHA_PI_DIAGNOSTIC_JOURNAL_PATH";
const DIAGNOSTIC_JOURNAL_MAX_BYTES = 512 * 1024;
const DIAGNOSTIC_JOURNAL_QUEUE_LIMIT = 256;
const DIAGNOSTIC_JOURNAL_ROTATION_AGE_MS = 24 * 60 * 60 * 1000;
const DIAGNOSTIC_JOURNAL_PRUNE_AGE_MS = 48 * 60 * 60 * 1000;
const DIAGNOSTIC_STRING_LIMIT = 128;
const DIAGNOSTIC_SOURCE_PATTERN = /^[a-z][a-z0-9_.-]{0,40}$/;
const DIAGNOSTIC_EVENT_PATTERN = /^[a-z][a-z0-9_.-]{0,80}$/;
const DIAGNOSTIC_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
// Keep this aligned with pi-subagents' bounded diagnostic-token contract.
const DIAGNOSTIC_KIND_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
const DIAGNOSTIC_ERROR_NAMES = new Set([
  "AggregateError", "AbortError", "Error", "EvalError", "RangeError", "ReferenceError", "SyntaxError", "TypeError", "URIError",
]);
const DIAGNOSTIC_ID_FIELDS = new Set([
  "agent_id", "instance_id", "parent_session_id", "session_id", "subagent_id", "event_id", "frame_id", "request_id", "ack_frame_id", "producer_epoch",
  "control_id", "source_agent_id", "target_agent_id", "lease_id", "worker_id",
]);

const DIAGNOSTIC_STRING_FIELDS = new Set([
  "agent_id", "instance_id", "parent_session_id", "session_id", "subagent_id", "event_id", "frame_id", "request_id", "ack_frame_id", "producer_epoch",
  "status", "reason", "error_code", "error_name", "source", "frame_type", "disposition", "state", "phase", "activity_state", "receipt_state", "lifecycle_event",
  "control_id", "source_agent_id", "target_agent_id", "lease_id", "worker_id", "action", "operation", "component", "kind", "outcome",
  "requested_provider", "requested_model_id", "current_provider", "current_model_id",
]);
const DIAGNOSTIC_NUMBER_FIELDS = new Set([
  "client_sequence", "router_version", "subagent_count", "snapshot_count", "pending", "bytes", "description_length",
  "recipient_count", "attempt", "heartbeat_interval_ms", "available_count", "model_count", "source_model_count",
  "frame_bytes", "revision", "percent", "tokens", "context_window", "threshold_percent", "connection_generation", "source_count", "started_count", "disposed_count", "cleared_count", "reconciled_count", "resumed_count", "remaining_count", "journal_queue_depth", "journal_dropped", "journal_write_failures",
]);
const DIAGNOSTIC_BOOLEAN_FIELDS = new Set([
  "available", "registry_available", "lifecycle_service", "connected", "ready", "reconnect", "published",
  "accepted", "switched", "truncated", "mandatory", "changed", "enabled",
]);

function isDiagnosticJournalGlobal(value: unknown): value is DiagnosticJournalGlobal {
  return value !== null && typeof value === "object"
    && Object.getOwnPropertyDescriptor(value, "version")?.value === 1
    && typeof Object.getOwnPropertyDescriptor(value, "record")?.value === "function";
}

function isDiagnosticJournalState(value: unknown): value is DiagnosticJournalState {
  return value !== null && typeof value === "object"
    && Object.getOwnPropertyDescriptor(value, "journals")?.value instanceof Map;
}

function isManagedDiagnosticJournal(value: unknown): value is ManagedDiagnosticJournal {
  return value !== null && typeof value === "object"
    && typeof Object.getOwnPropertyDescriptor(value, "invalidate")?.value === "function"
    && typeof Object.getOwnPropertyDescriptor(value, "stale")?.get === "function";
}

function nearestExistingDirectory(path: string): string | undefined {
  let candidate = path;
  while (true) {
    try {
      if (statSync(candidate).isDirectory()) return candidate;
    } catch {
      // Walk toward a stable ancestor when the configured journal directory is new.
    }
    const parent = dirname(candidate);
    if (parent === candidate) return undefined;
    candidate = parent;
  }
}

function discardJournal(state: DiagnosticJournalState, key: string, journal: DiagnosticJournal): void {
  if (isManagedDiagnosticJournal(journal)) journal.invalidate();
  else void journal.close();
  if (state.journals.get(key) === journal) state.journals.delete(key);
  if (state.defaultJournal === journal) state.defaultJournal = undefined;
}

function discardStaleJournals(state: DiagnosticJournalState): void {
  for (const [key, journal] of state.journals) {
    if (journal.closed || !isManagedDiagnosticJournal(journal) || journal.stale) discardJournal(state, key, journal);
  }
  if (state.defaultJournal !== undefined && (state.defaultJournal.closed || state.defaultJournal.stale)) {
    state.defaultJournal.invalidate();
    state.defaultJournal = undefined;
  }
}

function diagnosticJournalState(): DiagnosticJournalState {
  const existing = Object.getOwnPropertyDescriptor(globalThis, DIAGNOSTIC_JOURNAL_STATE)?.value;
  if (isDiagnosticJournalState(existing)) return existing;
  const state: DiagnosticJournalState = { journals: new Map() };
  try {
    Object.defineProperty(globalThis, DIAGNOSTIC_JOURNAL_STATE, {
      value: state,
      configurable: false,
      enumerable: false,
      writable: false,
    });
  } catch {
    const installed = Object.getOwnPropertyDescriptor(globalThis, DIAGNOSTIC_JOURNAL_STATE)?.value;
    if (isDiagnosticJournalState(installed)) return installed;
  }
  return state;
}

function ensureDiagnosticJournalGlobal(state: DiagnosticJournalState): DiagnosticJournalGlobal | undefined {
  const existing = Object.getOwnPropertyDescriptor(globalThis, DIAGNOSTIC_JOURNAL_GLOBAL)?.value;
  if (isDiagnosticJournalGlobal(existing)) return existing;
  const contract: DiagnosticJournalGlobal = {
    version: 1,
    record: (source, event, fields = {}) => {
      try { return state.defaultJournal?.record(source, event, fields) === true; } catch { return false; }
    },
    flush: async () => {
      try { await state.defaultJournal?.flush(); } catch { /* Diagnostics are best-effort at teardown. */ }
    },
    close: async () => {
      try { await state.defaultJournal?.close(); } catch { /* Diagnostics are best-effort at teardown. */ }
    },
  };
  try {
    Object.defineProperty(globalThis, DIAGNOSTIC_JOURNAL_GLOBAL, {
      value: contract,
      configurable: true,
      enumerable: false,
      writable: false,
    });
    return contract;
  } catch {
    const installed = Object.getOwnPropertyDescriptor(globalThis, DIAGNOSTIC_JOURNAL_GLOBAL)?.value;
    return isDiagnosticJournalGlobal(installed) ? installed : undefined;
  }
}

function diagnosticJournalPath(environment: Environment, explicitPath: string | undefined, processId: number, windowId: string): string | undefined {
  const configured = explicitPath ?? environment[DIAGNOSTIC_JOURNAL_PATH_ENV];
  const basePath = typeof configured === "string" && configured.trim().length > 0
    ? configured.trim()
    : typeof environment.HOME === "string" && environment.HOME.length > 0
      ? `${environment.HOME}/.mecha-developer/logs/diagnostics-history.jsonl`
      : undefined;
  if (basePath === undefined) return undefined;
  const suffix = createHash("sha256").update(`${processId}\u0000${windowId}`).digest("hex").slice(0, 16);
  return `${basePath}.${suffix}`;
}

function diagnosticJournalWindow(environment: Environment, explicitWindow: string | undefined): string {
  const window = explicitWindow ?? environment.MECHA_TMUX_WINDOW ?? environment.TMUX_PANE ?? environment.TMUX ?? "process";
  return typeof window === "string" && window.length > 0 ? window : "process";
}

function diagnosticFields(fields: object | undefined): JsonObject {
  const output: JsonObject = {};
  if (fields === undefined || fields === null || typeof fields !== "object" || Array.isArray(fields)) return output;
  for (const [key, value] of Object.entries(fields)) {
    if (DIAGNOSTIC_ID_FIELDS.has(key)) {
      if (typeof value === "string" && DIAGNOSTIC_ID_PATTERN.test(value)) output[key] = value;
    } else if (key === "kind") {
      if (typeof value === "string" && DIAGNOSTIC_KIND_PATTERN.test(value)) output[key] = value;
    } else if (key === "error_name") {
      if (typeof value === "string" && DIAGNOSTIC_ERROR_NAMES.has(value)) output[key] = value;
    } else if (DIAGNOSTIC_STRING_FIELDS.has(key) && typeof value === "string") output[key] = value.slice(0, DIAGNOSTIC_STRING_LIMIT);
    else if (DIAGNOSTIC_NUMBER_FIELDS.has(key) && typeof value === "number" && Number.isSafeInteger(value) && value >= 0) output[key] = value;
    else if (DIAGNOSTIC_BOOLEAN_FIELDS.has(key) && typeof value === "boolean") output[key] = value;
    else if (key === "subagents" && Array.isArray(value)) {
      output.subagents = value.slice(0, 100).flatMap((item): JsonObject[] => {
        if (!isRecord(item) || typeof item.id !== "string" || !DIAGNOSTIC_ID_PATTERN.test(item.id)) return [];
        return [{
          id: item.id,
          ...(typeof item.type === "string" ? { type: item.type.slice(0, DIAGNOSTIC_STRING_LIMIT) } : {}),
          ...(typeof item.status === "string" ? { status: item.status.slice(0, DIAGNOSTIC_STRING_LIMIT) } : {}),
          ...(typeof item.description_length === "number" && Number.isSafeInteger(item.description_length) && item.description_length >= 0
            ? { description_length: item.description_length } : {}),
        }];
      });
    }
  }
  return output;
}

function createDiagnosticJournal(options: DiagnosticJournalOptions = {}): DiagnosticJournal {
  const environment = options.environment ?? process.env;
  const processId = typeof options.processId === "number" && Number.isSafeInteger(options.processId) ? options.processId : process.pid;
  const windowId = diagnosticJournalWindow(environment, options.windowId);
  const configuredPath = options.path ?? environment[DIAGNOSTIC_JOURNAL_PATH_ENV];
  const home = typeof environment.HOME === "string" ? environment.HOME : undefined;
  const key = JSON.stringify([processId, windowId, home, configuredPath]);
  const state = diagnosticJournalState();
  ensureDiagnosticJournalGlobal(state);
  discardStaleJournals(state);
  const existing = state.journals.get(key);
  if (existing !== undefined) return existing;

  const path = diagnosticJournalPath(environment, options.path, processId, windowId);
  const now = options.now ?? (() => new Date());
  const maxBytes = options.maxBytes ?? DIAGNOSTIC_JOURNAL_MAX_BYTES;
  const queueLimit = options.queueLimit ?? DIAGNOSTIC_JOURNAL_QUEUE_LIMIT;
  const rotationAgeMs = options.rotationAgeMs ?? DIAGNOSTIC_JOURNAL_ROTATION_AGE_MS;
  const pruneAgeMs = options.pruneAgeMs ?? DIAGNOSTIC_JOURNAL_PRUNE_AGE_MS;
  if (typeof now !== "function" || !Number.isSafeInteger(maxBytes) || maxBytes <= 0 || !Number.isSafeInteger(queueLimit) || queueLimit <= 0
    || !Number.isSafeInteger(rotationAgeMs) || rotationAgeMs <= 0 || !Number.isSafeInteger(pruneAgeMs) || pruneAgeMs <= 0) {
    throw new TypeError("diagnostic journal options are invalid");
  }

  const rootDirectory = path === undefined ? undefined : nearestExistingDirectory(dirname(path));
  const rootIdentity = rootDirectory === undefined ? undefined : (() => {
    try {
      const rootStat = statSync(rootDirectory);
      return rootStat.isDirectory() ? { dev: rootStat.dev, ino: rootStat.ino } : undefined;
    } catch {
      return undefined;
    }
  })();
  let sequence = 0;
  let acceptedCount = 0;
  let closing = false;
  let closed = false;
  let invalidated = false;
  let hasWritten = false;
  let writtenCount = 0;
  let droppedCount = 0;
  let writeFailureCount = 0;
  const queue: string[] = [];
  let draining = false;
  const increment = (value: number): number => Math.min(Number.MAX_SAFE_INTEGER, value + 1);
  const waiters: Array<() => void> = [];

  const pathTornDown = (): boolean => {
    if (path === undefined) return false;
    try {
      if (rootDirectory !== undefined) {
        const currentRoot = statSync(rootDirectory);
        if (!currentRoot.isDirectory() || rootIdentity === undefined
          || currentRoot.dev !== rootIdentity.dev || currentRoot.ino !== rootIdentity.ino) return true;
      }
    } catch {
      return true;
    }
    // Rotation temporarily removes the active file while the drain owns it.
    return hasWritten && !draining && !existsSync(path);
  };
  const finishWaiters = () => {
    if (draining || queue.length > 0) return;
    if (pathTornDown()) journal.invalidate();
    for (const resolve of waiters.splice(0)) resolve();
  };
  const pruneAndRotate = async (timestamp: Date): Promise<void> => {
    if (path === undefined) return;
    const active = await stat(path).catch(() => undefined);
    const rotated = `${path}.1`;
    const rotatedStat = await stat(rotated).catch(() => undefined);
    if (rotatedStat !== undefined && timestamp.getTime() - rotatedStat.mtimeMs >= pruneAgeMs) await rm(rotated, { force: true });
    if (active === undefined) return;
    if (timestamp.getTime() - active.mtimeMs < rotationAgeMs && active.size < maxBytes) return;
    await rm(rotated, { force: true });
    await rename(path, rotated);
  };
  function shouldStopDrain(): boolean {
    if (invalidated || closed) return true;
    if (pathTornDown()) {
      journal.invalidate();
      return true;
    }
    return false;
  }
  const drain = async (): Promise<void> => {
    if (draining || path === undefined) return;
    draining = true;
    try {
      while (queue.length > 0) {
        if (shouldStopDrain()) {
          queue.length = 0;
          break;
        }
        const line = queue.shift();
        if (line === undefined) continue;
        try {
          if (shouldStopDrain()) {
            queue.length = 0;
            break;
          }
          const timestamp = now();
          if (!(timestamp instanceof Date) || Number.isNaN(timestamp.getTime())) {
            droppedCount = increment(droppedCount);
            continue;
          }
          await mkdir(dirname(path), { recursive: true, mode: 0o700 });
          await chmod(dirname(path), 0o700).catch(() => undefined);
          if (shouldStopDrain()) continue;
          await pruneAndRotate(timestamp);
          if (shouldStopDrain()) continue;
          await appendFile(path, line, { encoding: "utf8", mode: 0o600 });
          await chmod(path, 0o600).catch(() => undefined);
          hasWritten = true;
          writtenCount = increment(writtenCount);
        } catch {
          writeFailureCount = increment(writeFailureCount);
          // The journal is diagnostic only. A filesystem failure cannot affect Mecha.
        }
      }
    } finally {
      draining = false;
      finishWaiters();
    }
  };
  let journal: ManagedDiagnosticJournal;
  const detach = () => {
    for (const [entryKey, entry] of state.journals) {
      if (entry === journal) state.journals.delete(entryKey);
    }
    if (state.defaultJournal === journal) state.defaultJournal = undefined;
  };
  journal = {
    path,
    get closed() { return closed; },
    get closing() { return closing; },
    get reusable() { return !closing && !closed && !draining && queue.length === 0; },
    get stale() { return invalidated || pathTornDown(); },
    invalidate() {
      if (closed && invalidated) return;
      invalidated = true;
      closing = true;
      queue.length = 0;
      closed = true;
      detach();
    },
    record(source, event, fields = {}) {
      try {
        if (path === undefined || closing || closed) return false;
        if (pathTornDown()) {
          journal.invalidate();
          return false;
        }
        if (!DIAGNOSTIC_SOURCE_PATTERN.test(source) || !DIAGNOSTIC_EVENT_PATTERN.test(event)) {
          droppedCount = increment(droppedCount);
          return false;
        }
        const timestamp = now();
        if (!(timestamp instanceof Date) || Number.isNaN(timestamp.getTime())) {
          droppedCount = increment(droppedCount);
          return false;
        }
        if (queue.length >= queueLimit) {
          droppedCount = increment(droppedCount);
          return false;
        }
        const record: DiagnosticJournalRecord = {
          timestamp: timestamp.toISOString(),
          sequence: ++sequence,
          source,
          event,
          ...(droppedCount > 0 ? { journal_dropped: droppedCount } : {}),
          ...(writeFailureCount > 0 ? { journal_write_failures: writeFailureCount } : {}),
          ...(queue.length > 0 || droppedCount > 0 || writeFailureCount > 0 ? { journal_queue_depth: queue.length } : {}),
          ...diagnosticFields(fields),
        };
        queue.push(`${JSON.stringify(record)}\n`);
        acceptedCount = increment(acceptedCount);
        void drain();
        return true;
      } catch {
        droppedCount = increment(droppedCount);
        return false;
      }
    },
    flush() {
      if (!draining && queue.length === 0) {
        if (pathTornDown()) journal.invalidate();
        return Promise.resolve();
      }
      return new Promise<void>((resolve) => waiters.push(resolve));
    },
    async close() {
      if (closed) {
        detach();
        return;
      }
      closing = true;
      if (pathTornDown()) {
        journal.invalidate();
        return;
      }
      await journal.flush();
      closed = true;
      detach();
    },
    health() {
      return {
        accepted: acceptedCount,
        written: writtenCount,
        dropped: droppedCount,
        writeFailures: writeFailureCount,
        queueDepth: queue.length,
      };
    },
  };
  state.journals.set(key, journal);
  // The data-property contract has no window argument. It points at the first
  // journal for this process, which is the root/child journal in normal use.
  if (state.defaultJournal === undefined && path !== undefined) state.defaultJournal = journal;
  ensureDiagnosticJournalGlobal(state);
  return journal;
}

export { createDiagnosticJournal };
export const getDiagnosticJournal = createDiagnosticJournal;
