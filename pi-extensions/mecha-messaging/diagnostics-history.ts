import { appendFileSync, mkdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
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
  "agent_id", "instance_id", "parent_session_id", "session_id", "subagent_id", "event_id",
  "status", "reason", "error_code", "error_name", "source", "frame_type", "disposition", "state", "phase",
  "control_id", "source_agent_id", "target_agent_id", "lease_id", "action",
  "requested_provider", "requested_model_id", "current_provider", "current_model_id",
]);
const NATIVE_SUBAGENT_TRACE_NUMBER_FIELDS = new Set([
  "client_sequence", "router_version", "subagent_count", "snapshot_count", "pending", "bytes", "description_length",
  "recipient_count", "attempt", "heartbeat_interval_ms", "available_count", "model_count", "source_model_count",
  "frame_bytes",
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
