import { randomUUID } from "node:crypto";

import type { JsonRecord, JsonValue } from "./manifest.js";

export const HIDDEN_COMMAND_NAMES = Object.freeze([
  "mecha-bootstrap-v1",
  "mecha-install-tools-v1",
  "mecha-control-result-v1",
  "mecha-mailbox-deliver-v1",
  "mecha-policy-apply-v1",
  "mecha-child-snapshot-v1",
  "mecha-post-compaction-notification-v1",
] as const);

export const LEGACY_COMMAND_NAMES = Object.freeze([
  "goal",
  "goal-lock",
  "goal-snooze",
  "goal-snooze-turn-limit",
  "goal-snooze-budget",
  "goal-snooze-budget-refill",
  "mecha-parent-message",
  "inline-tools",
  "mecha-reply",
] as const);

export const CUSTOM_MESSAGE_TYPES = Object.freeze([
  "mecha.tools.accepted.v1",
  "mecha.tools.rejected.v1",
  "mecha.control.request.v1",
  "mecha.control.result.v1",
  "mecha.mailbox.appended.v1",
  "mecha.policy.applied.v1",
  "mecha.child.snapshot.v1",
  "mecha.child.delta.v1",
] as const);

export type HiddenCommandName = typeof HIDDEN_COMMAND_NAMES[number];
export type LegacyCommandName = typeof LEGACY_COMMAND_NAMES[number];
export type CustomMessageType = typeof CUSTOM_MESSAGE_TYPES[number];
export type LegacyCommandDefinition = { readonly name: LegacyCommandName; readonly operationId: string; readonly description: string };

export const legacyCommandDefinitions: Readonly<Record<LegacyCommandName, LegacyCommandDefinition>> = Object.freeze({
  goal: { name: "goal", operationId: "legacy.goal", description: "Preserve /goal raw arguments through the managed control bridge." },
  "goal-lock": { name: "goal-lock", operationId: "legacy.goal_lock", description: "Preserve /goal-lock raw arguments through the managed control bridge." },
  "goal-snooze": { name: "goal-snooze", operationId: "legacy.goal_snooze", description: "Preserve /goal-snooze raw arguments through the managed control bridge." },
  "goal-snooze-turn-limit": { name: "goal-snooze-turn-limit", operationId: "legacy.goal_snooze_turn_limit", description: "Preserve /goal-snooze-turn-limit raw arguments through the managed control bridge." },
  "goal-snooze-budget": { name: "goal-snooze-budget", operationId: "legacy.goal_snooze_budget", description: "Preserve /goal-snooze-budget raw arguments through the managed control bridge." },
  "goal-snooze-budget-refill": { name: "goal-snooze-budget-refill", operationId: "legacy.goal_snooze_budget_refill", description: "Preserve /goal-snooze-budget-refill raw arguments through the managed control bridge." },
  "mecha-parent-message": { name: "mecha-parent-message", operationId: "legacy.parent_message", description: "Preserve /mecha-parent-message raw arguments through the managed control bridge." },
  "inline-tools": { name: "inline-tools", operationId: "legacy.inline_tools", description: "Preserve /inline-tools raw arguments through the managed control bridge." },
  "mecha-reply": { name: "mecha-reply", operationId: "legacy.reply", description: "Preserve /mecha-reply raw arguments through the managed control bridge." },
});

const MAX_ENCODED_BYTES = 174_763;
const MAX_DECODED_BYTES = 131_072;
const MAX_CONTROL_RESULT_BYTES = 65_536;
const MAX_CONTENT_BYTES = 16 * 1024;
const MAX_DETAILS_BYTES = 16 * 1024;
const MAX_JSON_DEPTH = 32;
const MAX_JSON_NODES = 2_048;
const MAX_JSON_STRING_BYTES = 8 * 1024;
// Control result content and serialized details each have a distinct 16 KiB
// contract. The generic transport parser must not reject those values first.
const MAX_ENVELOPE_STRING_BYTES = 16 * 1024;
const BASE64_URL = /^[A-Za-z0-9_-]+$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const SHA256 = /^[a-f0-9]{64}$/;

export type ControlResultPayload = {
  protocol: "mecha.control/v1";
  result_id: string;
  request_id: string;
  target_session_epoch: number;
  runtime_generation: string;
  manifest_sha256: string;
  status: "ok" | "error";
  content: string;
  details: JsonRecord;
  error?: { code: string; message: string; retryable: boolean };
};

export function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function protocolError(message: string): never {
  throw new TypeError(`Invalid mecha RPC protocol value: ${message}`);
}

function assertExactKeys(value: JsonRecord, required: readonly string[], optional: readonly string[] = []): void {
  const allowed = new Set([...required, ...optional]);
  for (const key of Object.keys(value)) if (!allowed.has(key)) protocolError(`unexpected field ${key}`);
  for (const key of required) if (!Object.hasOwn(value, key)) protocolError(`missing field ${key}`);
}

function hasLoneSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return true;
      index++;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true;
    }
  }
  return false;
}

function boundedString(value: unknown, label: string, maximum: number, allowEmpty = false): string {
  if (typeof value !== "string" || (!allowEmpty && value.length === 0) || hasLoneSurrogate(value) || Buffer.byteLength(value, "utf8") > maximum) protocolError(`${label} is invalid`);
  return value;
}

function safeInteger(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) protocolError(`${label} is invalid`);
  return value;
}

function validateJsonValue(value: unknown, depth: number, state: { nodes: number }, maximumStringBytes = MAX_JSON_STRING_BYTES): value is JsonValue {
  if (depth > MAX_JSON_DEPTH || ++state.nodes > MAX_JSON_NODES) return false;
  if (value === null || typeof value === "boolean") return true;
  if (typeof value === "string") return !hasLoneSurrogate(value) && Buffer.byteLength(value, "utf8") <= maximumStringBytes;
  if (typeof value === "number") return Number.isFinite(value) && Math.abs(value) <= Number.MAX_SAFE_INTEGER;
  if (Array.isArray(value)) return value.length <= 256 && value.every((entry) => validateJsonValue(entry, depth + 1, state, maximumStringBytes));
  if (!isRecord(value) || Object.keys(value).length > 256) return false;
  for (const [key, entry] of Object.entries(value)) {
    if (key === "__proto__" || key === "prototype" || key === "constructor" || hasLoneSurrogate(key) || Buffer.byteLength(key, "utf8") > MAX_JSON_STRING_BYTES) return false;
    if (!validateJsonValue(entry, depth + 1, state, maximumStringBytes)) return false;
  }
  return true;
}

function validateDetails(value: unknown): JsonRecord {
  if (!isRecord(value) || !validateJsonValue(value, 1, { nodes: 0 }, MAX_ENVELOPE_STRING_BYTES) || Buffer.byteLength(JSON.stringify(value), "utf8") > MAX_DETAILS_BYTES) {
    protocolError("details is not a bounded object");
  }
  return structuredClone(value);
}

class StrictJsonParser {
  private index = 0;

  constructor(private readonly text: string) {}

  parse(): JsonValue {
    const value = this.value(1);
    this.whitespace();
    if (this.index !== this.text.length) protocolError("trailing JSON input");
    return value;
  }

  private whitespace(): void {
    while (this.index < this.text.length && /[\u0020\u000a\u000d\u0009]/.test(this.text[this.index]!)) this.index++;
  }

  private value(depth: number): JsonValue {
    if (depth > MAX_JSON_DEPTH) protocolError("JSON depth exceeds limit");
    this.whitespace();
    const current = this.text[this.index];
    if (current === "{") return this.object(depth);
    if (current === "[") return this.array(depth);
    if (current === '"') return this.string();
    if (this.text.startsWith("true", this.index)) { this.index += 4; return true; }
    if (this.text.startsWith("false", this.index)) { this.index += 5; return false; }
    if (this.text.startsWith("null", this.index)) { this.index += 4; return null; }
    return this.number();
  }

  private object(depth: number): JsonRecord {
    this.index++;
    const result: JsonRecord = {};
    const names = new Set<string>();
    this.whitespace();
    if (this.text[this.index] === "}") { this.index++; return result; }
    while (true) {
      this.whitespace();
      if (this.text[this.index] !== '"') protocolError("object key is not a JSON string");
      const key = this.string();
      if (names.has(key)) protocolError(`duplicate JSON key ${key}`);
      names.add(key);
      this.whitespace();
      if (this.text[this.index++] !== ":") protocolError("object key lacks colon");
      const value = this.value(depth + 1);
      Object.defineProperty(result, key, { value, enumerable: true, configurable: false, writable: false });
      this.whitespace();
      const separator = this.text[this.index++];
      if (separator === "}") return result;
      if (separator !== ",") protocolError("object separator is invalid");
    }
  }

  private array(depth: number): JsonValue[] {
    this.index++;
    const result: JsonValue[] = [];
    this.whitespace();
    if (this.text[this.index] === "]") { this.index++; return result; }
    while (true) {
      result.push(this.value(depth + 1));
      this.whitespace();
      const separator = this.text[this.index++];
      if (separator === "]") return result;
      if (separator !== ",") protocolError("array separator is invalid");
    }
  }

  private string(): string {
    const start = this.index;
    this.index++;
    let escaped = false;
    while (this.index < this.text.length) {
      const character = this.text[this.index++]!;
      if (escaped) { escaped = false; continue; }
      if (character === "\\") { escaped = true; continue; }
      if (character === '"') {
        const token = this.text.slice(start, this.index);
        try {
          const parsed: unknown = JSON.parse(token);
          if (typeof parsed !== "string" || hasLoneSurrogate(parsed)) protocolError("invalid JSON string");
          return parsed;
        } catch {
          protocolError("invalid JSON string");
        }
      }
      if (character.charCodeAt(0) < 0x20) protocolError("control character in JSON string");
    }
    protocolError("unterminated JSON string");
  }

  private number(): number {
    const match = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(this.text.slice(this.index));
    if (match === null) protocolError("invalid JSON value");
    this.index += match[0].length;
    const result = Number(match[0]);
    if (!Number.isFinite(result) || Math.abs(result) > Number.MAX_SAFE_INTEGER) protocolError("JSON number is not safe");
    return result;
  }
}

/** Decodes the one raw base64url argument only after byte bounds and duplicate-key checks. */
export function decodeHiddenEnvelope(raw: string): JsonRecord {
  if (typeof raw !== "string" || raw.length === 0 || raw.length > MAX_ENCODED_BYTES || !BASE64_URL.test(raw)) protocolError("encoded command is invalid");
  const decoded = Buffer.from(raw, "base64url");
  if (decoded.length === 0 || decoded.length > MAX_DECODED_BYTES || decoded.toString("base64url") !== raw) protocolError("encoded command is not canonical base64url");
  const text = new TextDecoder("utf-8", { fatal: true }).decode(decoded);
  const value = new StrictJsonParser(text).parse();
  if (!isRecord(value) || !validateJsonValue(value, 1, { nodes: 0 }, MAX_ENVELOPE_STRING_BYTES)) protocolError("decoded command is not a bounded object");
  return value;
}

export function encodeHiddenEnvelope(value: JsonRecord): string {
  if (!validateJsonValue(value, 1, { nodes: 0 }, MAX_ENVELOPE_STRING_BYTES)) protocolError("cannot encode an unbounded value");
  const encoded = Buffer.from(JSON.stringify(value), "utf8").toString("base64url");
  if (encoded.length > MAX_ENCODED_BYTES) protocolError("encoded command exceeds limit");
  return encoded;
}

export function validateControlResultPayload(value: unknown): ControlResultPayload {
  if (!isRecord(value) || Buffer.byteLength(JSON.stringify(value), "utf8") > MAX_CONTROL_RESULT_BYTES) protocolError("control result is invalid");
  assertExactKeys(value, ["protocol", "result_id", "request_id", "target_session_epoch", "runtime_generation", "manifest_sha256", "status", "content", "details"], ["error"]);
  if (value.protocol !== "mecha.control/v1") protocolError("control result protocol is invalid");
  const resultId = boundedString(value.result_id, "result_id", 64);
  const requestId = boundedString(value.request_id, "request_id", 64);
  const runtimeGeneration = boundedString(value.runtime_generation, "runtime_generation", 64);
  const manifestSha = boundedString(value.manifest_sha256, "manifest_sha256", 64);
  if (!UUID.test(resultId) || !UUID.test(requestId) || !UUID.test(runtimeGeneration) || !SHA256.test(manifestSha)) protocolError("control result identity is invalid");
  const sessionEpoch = safeInteger(value.target_session_epoch, "target_session_epoch");
  if (value.status !== "ok" && value.status !== "error") protocolError("control result status is invalid");
  const content = boundedString(value.content, "content", MAX_CONTENT_BYTES, true);
  const details = validateDetails(value.details);
  if (value.status === "ok") {
    if (value.error !== undefined) protocolError("successful control result cannot contain error");
    return { protocol: "mecha.control/v1", result_id: resultId, request_id: requestId, target_session_epoch: sessionEpoch, runtime_generation: runtimeGeneration, manifest_sha256: manifestSha, status: "ok", content, details };
  }
  if (!isRecord(value.error)) protocolError("error result requires error");
  assertExactKeys(value.error, ["code", "message", "retryable"]);
  const code = boundedString(value.error.code, "error code", 128);
  const message = boundedString(value.error.message, "error message", 2_000, true);
  if (typeof value.error.retryable !== "boolean") protocolError("error retryable is invalid");
  return { protocol: "mecha.control/v1", result_id: resultId, request_id: requestId, target_session_epoch: sessionEpoch, runtime_generation: runtimeGeneration, manifest_sha256: manifestSha, status: "error", content, details, error: { code, message, retryable: value.error.retryable } };
}

function validateHiddenCommandFence(value: JsonRecord): void {
  if (typeof value.runtime_generation !== "string" || !UUID.test(value.runtime_generation)
    || typeof value.session_epoch !== "number" || !Number.isSafeInteger(value.session_epoch) || value.session_epoch < 1) {
    protocolError("hidden command runtime fence is invalid");
  }
  if (value.manifest_sha256 !== undefined && (typeof value.manifest_sha256 !== "string" || !SHA256.test(value.manifest_sha256))) {
    protocolError("hidden command manifest fence is invalid");
  }
}

/** Closes the direct managed-mail command before it can append a session message. */
export function validateMailboxDeliverEnvelope(value: unknown): JsonRecord {
  if (!isRecord(value)) protocolError("mailbox delivery envelope is invalid");
  assertExactKeys(value, ["protocol", "message_id", "content", "runtime_generation", "session_epoch"], ["manifest_sha256"]);
  if (value.protocol !== "mecha.mailbox-deliver/v1") protocolError("mailbox delivery protocol is invalid");
  boundedString(value.message_id, "mailbox message ID", 128);
  boundedString(value.content, "mailbox content", MAX_ENVELOPE_STRING_BYTES, true);
  validateHiddenCommandFence(value);
  return value;
}

/** Closes the router-owned policy projection command before applying local mechanics. */
export function validatePolicyApplyEnvelope(value: unknown): JsonRecord {
  if (!isRecord(value)) protocolError("policy envelope is invalid");
  assertExactKeys(value, ["protocol", "policy", "runtime_generation", "session_epoch"], ["manifest_sha256", "evaluator"]);
  if (value.protocol !== "mecha.policy/v1" || !isRecord(value.policy)) protocolError("policy envelope is invalid");
  validateHiddenCommandFence(value);
  if (value.evaluator !== undefined) {
    if (!isRecord(value.evaluator)) protocolError("goal evaluator envelope is invalid");
    assertExactKeys(value.evaluator, ["evaluation_id", "goal_revision", "settled_sequence", "goal"]);
    if (!UUID.test(boundedString(value.evaluator.evaluation_id, "goal evaluation ID", 64))
      || safeInteger(value.evaluator.goal_revision, "goal revision") < 1
      || safeInteger(value.evaluator.settled_sequence, "settled sequence") < 1) protocolError("goal evaluator envelope is invalid");
    boundedString(value.evaluator.goal, "goal evaluator goal", 4_000);
  }
  return value;
}

/** Closes the lifecycle snapshot command; source rows still come only from pi-subagents. */
export function validateChildSnapshotEnvelope(value: unknown): JsonRecord {
  if (!isRecord(value)) protocolError("child snapshot envelope is invalid");
  assertExactKeys(value, ["protocol", "runtime_generation", "session_epoch"], ["manifest_sha256"]);
  if (value.protocol !== "mecha.child-snapshot/v1") protocolError("child snapshot protocol is invalid");
  validateHiddenCommandFence(value);
  return value;
}

function validateRuntimeFence(value: JsonRecord, required: readonly string[], optional: readonly string[] = []): void {
  assertExactKeys(value, required, optional);
  if (typeof value.runtime_generation !== "string" || !UUID.test(value.runtime_generation)
    || typeof value.session_epoch !== "number" || !Number.isSafeInteger(value.session_epoch) || value.session_epoch < 1) {
    protocolError("custom message runtime fence is invalid");
  }
}

const CONTEXT_REF = /^ctx1_[A-Za-z0-9_-]{43}$/;
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const SOURCE_LIFECYCLE_STATES = new Set(["queued", "running", "completed", "steered", "aborted", "stopped", "error"]);
const COMPACTION_OUTCOMES = new Set(["completed", "failed", "aborted", "reconciled_unknown"]);

function validateTimestamp(value: unknown, label: string, allowNull = false): void {
  if (value === null && allowNull) return;
  if (typeof value !== "string" || !RFC3339.test(value) || Number.isNaN(Date.parse(value))) protocolError(`${label} is invalid`);
}

function validateSourceModel(value: unknown): void {
  if (value === null) return;
  if (!isRecord(value)) protocolError("child model is invalid");
  assertExactKeys(value, ["provider", "id", "name"]);
  boundedString(value.provider, "child model provider", MAX_JSON_STRING_BYTES, true);
  boundedString(value.id, "child model ID", MAX_JSON_STRING_BYTES, true);
  boundedString(value.name, "child model name", MAX_JSON_STRING_BYTES, true);
}

function validateCompaction(value: unknown): void {
  if (!isRecord(value)) protocolError("child compaction is invalid");
  assertExactKeys(value, ["state", "count", "started_at", "last_outcome"]);
  if (value.state !== "idle" && value.state !== "compacting") protocolError("child compaction state is invalid");
  safeInteger(value.count, "child compaction count");
  validateTimestamp(value.started_at, "child compaction start", true);
  if (value.last_outcome !== null && (typeof value.last_outcome !== "string" || !COMPACTION_OUTCOMES.has(value.last_outcome))) {
    protocolError("child compaction outcome is invalid");
  }
}

function validateSourceField(name: string, value: unknown): void {
  if (name === "description") {
    boundedString(value, "child description", MAX_JSON_STRING_BYTES, true);
  } else if (name === "model") {
    validateSourceModel(value);
  } else if (name === "lifecycle_state") {
    if (typeof value !== "string" || !SOURCE_LIFECYCLE_STATES.has(value)) protocolError("child lifecycle state is invalid");
  } else if (name === "started_at") {
    validateTimestamp(value, "child start");
  } else if (name === "finished_at") {
    validateTimestamp(value, "child finish", true);
  } else if (name === "duration_ms") {
    if (value !== null) safeInteger(value, "child duration");
  } else if (name === "compaction") {
    validateCompaction(value);
  } else {
    protocolError(`child change ${name} is invalid`);
  }
}

function validateSourceChild(value: unknown): void {
  if (!isRecord(value)) protocolError("child source row is invalid");
  assertExactKeys(value, ["task_id", "run_id", "parent_entry_id", "description", "model", "lifecycle_state", "started_at", "finished_at", "duration_ms", "compaction", "sequence"]);
  boundedString(value.task_id, "child task ID", MAX_JSON_STRING_BYTES);
  boundedString(value.run_id, "child run ID", MAX_JSON_STRING_BYTES);
  boundedString(value.parent_entry_id, "child parent entry ID", MAX_JSON_STRING_BYTES);
  for (const name of ["description", "model", "lifecycle_state", "started_at", "finished_at", "duration_ms", "compaction"]) {
    validateSourceField(name, value[name]);
  }
  safeInteger(value.sequence, "child sequence");
}

function validateSourceChanges(value: unknown): void {
  if (!isRecord(value)) protocolError("child changes are invalid");
  for (const [name, change] of Object.entries(value)) validateSourceField(name, change);
}

function validateAvailability(value: unknown): void {
  if (!isRecord(value)) protocolError("legacy control availability is invalid");
  assertExactKeys(value, ["session_id", "kind"]);
  boundedString(value.session_id, "legacy control session ID", MAX_JSON_STRING_BYTES);
  if (value.kind !== "root" && value.kind !== "child") protocolError("legacy control availability kind is invalid");
}

function validatePolicyApplied(value: unknown): void {
  if (!isRecord(value)) protocolError("policy application details are invalid");
  assertExactKeys(value, ["inline_enabled", "inline_credits", "recall_content", "settled_enabled", "goal_lock_enabled", "compaction_deferred"], ["goal_revision"]);
  if (typeof value.inline_enabled !== "boolean" || typeof value.settled_enabled !== "boolean"
    || typeof value.goal_lock_enabled !== "boolean" || typeof value.compaction_deferred !== "boolean") {
    protocolError("policy application flags are invalid");
  }
  if (!isRecord(value.inline_credits)) protocolError("policy application credits are invalid");
  assertExactKeys(value.inline_credits, [], ["bash", "edit", "write"]);
  for (const credit of Object.values(value.inline_credits)) {
    if (typeof credit !== "number" || !Number.isSafeInteger(credit) || credit < 0 || credit > 5) protocolError("policy application credit is invalid");
  }
  boundedString(value.recall_content, "policy application recall", 24_000, true);
  if (value.goal_revision !== undefined && (typeof value.goal_revision !== "number" || !Number.isSafeInteger(value.goal_revision) || value.goal_revision < 1)) {
    protocolError("policy application goal revision is invalid");
  }
}

/** Validates the exact hidden custom-message vocabulary before Pi persists it. */
export function validateCustomMessageDetails(customType: CustomMessageType, details: unknown): JsonRecord {
  if (!isCustomMessageType(customType)) protocolError("custom message type is invalid");
  const maxStringBytes = customType === "mecha.control.result.v1"
    ? MAX_ENVELOPE_STRING_BYTES
    : customType === "mecha.policy.applied.v1" ? 24_000 : MAX_JSON_STRING_BYTES;
  if (!isRecord(details) || !validateJsonValue(details, 1, { nodes: 0 }, maxStringBytes)) protocolError("custom message details are invalid");
  if (customType === "mecha.tools.accepted.v1") {
    validateRuntimeFence(details, ["protocol", "manifest_sha256", "runtime_generation", "session_epoch"], ["manifest_revision", "active_tools"]);
    if (details.protocol !== "mecha.tools/v1" || typeof details.manifest_sha256 !== "string" || !SHA256.test(details.manifest_sha256)) protocolError("tool acceptance details are invalid");
    if (details.manifest_revision !== undefined && (typeof details.manifest_revision !== "number" || !Number.isSafeInteger(details.manifest_revision) || details.manifest_revision < 1)) protocolError("tool acceptance revision is invalid");
    if (details.active_tools !== undefined && (!Array.isArray(details.active_tools) || details.active_tools.some((name) => typeof name !== "string" || name.length === 0))) protocolError("tool acceptance active tools are invalid");
  } else if (customType === "mecha.tools.rejected.v1") {
    validateRuntimeFence(details, ["protocol", "reason", "runtime_generation", "session_epoch"]);
    if (details.protocol !== "mecha.tools/v1") protocolError("tool rejection protocol is invalid");
    boundedString(details.reason, "tool rejection reason", 2_000, true);
  } else if (customType === "mecha.control.request.v1") {
    if (typeof details.legacy_command === "string") {
      assertExactKeys(details, ["protocol", "request_id", "operation_id", "legacy_command", "raw_arguments", "runtime_generation", "session_epoch", "availability"]);
      if (details.protocol !== "mecha.control/v1" || typeof details.request_id !== "string" || !UUID.test(details.request_id)
        || typeof details.operation_id !== "string" || !LEGACY_COMMAND_NAMES.some((name) => name === details.legacy_command)
        || typeof details.raw_arguments !== "string") protocolError("legacy control request details are invalid");
      validateAvailability(details.availability);
      validateRuntimeFence(details, ["protocol", "request_id", "operation_id", "legacy_command", "raw_arguments", "runtime_generation", "session_epoch", "availability"]);
    } else {
      assertExactKeys(details, ["protocol", "request_id", "tool_call_id", "capability_id", "tool_name", "definition_id", "definition_sha256", "operation_id", "manifest_sha256", "runtime_generation", "session_epoch", "context_ref", "arguments"]);
      if (details.protocol !== "mecha.control/v1" || typeof details.request_id !== "string" || !UUID.test(details.request_id)
        || typeof details.tool_call_id !== "string" || typeof details.capability_id !== "string" || typeof details.tool_name !== "string"
        || typeof details.definition_id !== "string" || typeof details.definition_sha256 !== "string" || !SHA256.test(details.definition_sha256)
        || details.definition_id !== `def_${details.definition_sha256}` || typeof details.operation_id !== "string" || typeof details.manifest_sha256 !== "string"
        || !SHA256.test(details.manifest_sha256) || typeof details.context_ref !== "string"
        || (details.context_ref !== "root" && !CONTEXT_REF.test(details.context_ref))) protocolError("control request details are invalid");
      validateRuntimeFence(details, ["protocol", "request_id", "tool_call_id", "capability_id", "tool_name", "definition_id", "definition_sha256", "operation_id", "manifest_sha256", "runtime_generation", "session_epoch", "context_ref", "arguments"]);
    }
  } else if (customType === "mecha.control.result.v1") {
    assertExactKeys(details, ["protocol", "result_id", "request_id", "target_session_epoch", "runtime_generation", "manifest_sha256", "status", "details"], ["error"]);
    if (details.protocol !== "mecha.control/v1" || typeof details.result_id !== "string" || !UUID.test(details.result_id)
      || typeof details.request_id !== "string" || !UUID.test(details.request_id) || typeof details.target_session_epoch !== "number"
      || !Number.isSafeInteger(details.target_session_epoch) || details.target_session_epoch < 1 || typeof details.runtime_generation !== "string"
      || !UUID.test(details.runtime_generation) || typeof details.manifest_sha256 !== "string" || !SHA256.test(details.manifest_sha256)
      || (details.status !== "ok" && details.status !== "error")) protocolError("control result details are invalid");
    validateDetails(details.details);
    if (details.status === "ok" && Object.hasOwn(details, "error")) protocolError("successful control result details contain error");
    if (details.status === "error") {
      if (!isRecord(details.error)) protocolError("error result details lack error");
      assertExactKeys(details.error, ["code", "message", "retryable"]);
      boundedString(details.error.code, "error code", 128);
      boundedString(details.error.message, "error message", 2_000, true);
      if (typeof details.error.retryable !== "boolean") protocolError("error retryable is invalid");
    }
  } else if (customType === "mecha.mailbox.appended.v1") {
    validateRuntimeFence(details, ["protocol", "message_id", "runtime_generation", "session_epoch"]);
    if (details.protocol !== "mecha.mailbox/v1") protocolError("mailbox append protocol is invalid");
    boundedString(details.message_id, "message ID", 128);
  } else if (customType === "mecha.policy.applied.v1") {
    if (details.evaluator !== undefined) {
      validateRuntimeFence(details, ["protocol", "runtime_generation", "session_epoch", "evaluator"]);
      if (details.protocol !== "mecha.policy/v1" || !isRecord(details.evaluator)) protocolError("goal evaluator receipt is invalid");
      assertExactKeys(details.evaluator, ["evaluation_id", "goal_revision", "settled_sequence", "phase"], ["outcome", "reason"]);
      if (!UUID.test(boundedString(details.evaluator.evaluation_id, "goal evaluation ID", 64))
        || safeInteger(details.evaluator.goal_revision, "goal revision") < 1
        || safeInteger(details.evaluator.settled_sequence, "settled sequence") < 1
        || (details.evaluator.phase !== "requested" && details.evaluator.phase !== "result")) protocolError("goal evaluator receipt is invalid");
      if (details.evaluator.phase === "requested" && (details.evaluator.outcome !== undefined || details.evaluator.reason !== undefined)) protocolError("goal evaluator request receipt is invalid");
      if (details.evaluator.phase === "result") {
        if ((details.evaluator.outcome !== "DONE" && details.evaluator.outcome !== "NOT_DONE") || typeof details.evaluator.reason !== "string") protocolError("goal evaluator result receipt is invalid");
        boundedString(details.evaluator.reason, "goal evaluator reason", 512, true);
      }
    } else {
      validateRuntimeFence(details, ["protocol", "runtime_generation", "session_epoch", "applied"]);
      if (details.protocol !== "mecha.policy/v1") protocolError("policy application protocol is invalid");
      validatePolicyApplied(details.applied);
    }
  } else if (customType === "mecha.child.snapshot.v1") {
    assertExactKeys(details, ["protocol", "snapshot_id", "owner_session_id", "sequence", "runs"]);
    if (details.protocol !== "mecha.children/v1" || !Array.isArray(details.runs)) protocolError("child snapshot details are invalid");
    boundedString(details.snapshot_id, "child snapshot ID", MAX_JSON_STRING_BYTES);
    boundedString(details.owner_session_id, "child owner session ID", MAX_JSON_STRING_BYTES);
    safeInteger(details.sequence, "child snapshot sequence");
    for (const run of details.runs) validateSourceChild(run);
  } else {
    assertExactKeys(details, ["protocol", "owner_session_id", "sequence", "task_id", "run_id", "parent_entry_id", "changes"]);
    if (details.protocol !== "mecha.children/v1") protocolError("child delta details are invalid");
    boundedString(details.owner_session_id, "child owner session ID", MAX_JSON_STRING_BYTES);
    safeInteger(details.sequence, "child delta sequence");
    boundedString(details.task_id, "child delta task ID", MAX_JSON_STRING_BYTES);
    boundedString(details.run_id, "child delta run ID", MAX_JSON_STRING_BYTES);
    boundedString(details.parent_entry_id, "child delta parent entry ID", MAX_JSON_STRING_BYTES);
    validateSourceChanges(details.changes);
  }
  return structuredClone(details);
}

export function isCustomMessageType(value: unknown): value is CustomMessageType {
  return typeof value === "string" && CUSTOM_MESSAGE_TYPES.some((entry) => entry === value);
}

export function isHiddenCommandName(value: unknown): value is HiddenCommandName {
  return typeof value === "string" && HIDDEN_COMMAND_NAMES.some((entry) => entry === value);
}

export function createRequestId(): string {
  return randomUUID();
}
