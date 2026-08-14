import { appendFileSync, chmodSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

import { rotateSizeCappedLog } from "./log_rotation.js";

export const NATIVE_SUBAGENT_TRACE_PATH_ENV = "MECHA_ROUTER_NATIVE_SUBAGENT_TRACE_PATH";
export const DEFAULT_NATIVE_SUBAGENT_TRACE_MAX_BYTES = 512 * 1024;

const SAFE_STRING_FIELDS = new Set([
  "agent_id", "instance_id", "parent_session_id", "subagent_id", "event_id",
  "status", "reason", "error_code", "error_name", "source", "frame_type", "disposition", "state", "phase",
]);
const SAFE_NUMBER_FIELDS = new Set([
  "client_sequence", "router_version", "subagent_count", "snapshot_count", "pending", "bytes",
  "description_length", "recipient_count", "attempt", "heartbeat_interval_ms",
]);
const SAFE_BOOLEAN_FIELDS = new Set(["available", "lifecycle_service", "connected", "ready", "reconnect", "published"]);
const EVENT_PATTERN = /^[a-z][a-z0-9_.-]{0,80}$/;

export function nativeSubagentTracePath(environment = process.env) {
  const configured = environment?.[NATIVE_SUBAGENT_TRACE_PATH_ENV];
  return typeof configured === "string" && configured.trim().length > 0 ? configured.trim() : undefined;
}

export function summarizeNativeSubagents(subagents) {
  if (!Array.isArray(subagents)) return [];
  return subagents.slice(0, 999).flatMap((subagent) => {
    const id = typeof subagent?.id === "string" ? subagent.id.trim().slice(0, 128) : "";
    if (id.length === 0) return [];
    const type = typeof subagent.type === "string" ? subagent.type.trim().slice(0, 64) : "";
    const status = typeof subagent.status === "string" ? subagent.status.trim().slice(0, 64) : "";
    const descriptionLength = typeof subagent.description === "string" ? subagent.description.length : 0;
    return [{ id, ...(type.length > 0 ? { type } : {}), ...(status.length > 0 ? { status } : {}), description_length: descriptionLength }];
  });
}

export function createNativeSubagentTrace({
  environment = process.env,
  path = nativeSubagentTracePath(environment),
  maxBytes = DEFAULT_NATIVE_SUBAGENT_TRACE_MAX_BYTES,
  clock = () => new Date(),
} = {}) {
  if (path === undefined) return { enabled: false, record: () => false };
  if (typeof path !== "string" || path.length === 0) throw new TypeError("native subagent trace path is invalid");
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) throw new TypeError("native subagent trace maxBytes must be a positive safe integer");
  if (typeof clock !== "function") throw new TypeError("native subagent trace clock must be a function");

  return {
    enabled: true,
    record(event, fields = {}) {
      try {
        const record = traceRecord(event, fields, clock);
        mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
        rotateSizeCappedLog(path, maxBytes);
        appendFileSync(path, `${JSON.stringify(record)}\n`, { encoding: "utf8", mode: 0o600 });
        chmodSync(path, 0o600);
        return true;
      } catch {
        // The trace is never part of lifecycle persistence or delivery.
        return false;
      }
    },
  };
}

function traceRecord(event, fields, clock) {
  if (typeof event !== "string" || !EVENT_PATTERN.test(event)) throw new TypeError("native subagent trace event is invalid");
  if (fields === null || typeof fields !== "object" || Array.isArray(fields)) throw new TypeError("native subagent trace fields are invalid");
  const timestamp = clock();
  if (!(timestamp instanceof Date) || Number.isNaN(timestamp.getTime())) throw new TypeError("native subagent trace clock is invalid");
  const record = { timestamp: timestamp.toISOString(), event };
  for (const [key, value] of Object.entries(fields)) {
    if (SAFE_STRING_FIELDS.has(key) && typeof value === "string") record[key] = value.slice(0, 128);
    else if (SAFE_NUMBER_FIELDS.has(key) && Number.isSafeInteger(value) && value >= 0) record[key] = value;
    else if (SAFE_BOOLEAN_FIELDS.has(key) && typeof value === "boolean") record[key] = value;
    else if (key === "subagents") record.subagents = summarizeNativeSubagents(value);
  }
  return record;
}
