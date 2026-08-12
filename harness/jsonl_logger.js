import { appendFileSync, chmodSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

import { rotateSizeCappedLog } from "./log_rotation.js";

// Router event-log retention. The live log rotates to `<log>.1` once it reaches this
// size and only one rotated generation is kept, so on-disk usage is bounded at 2x this
// value (32 MiB by default) plus one record per generation.
//
// Why 16 MiB: measured on the deployed router, delivery events — the ones needed to
// explain a lost message — run about 0.35 MB/day, but `agent_lease_renewed` adds
// ~1.5 MB per connected agent per day at one renewal per agent per 10s and dominates
// the file. 16 MiB therefore retains roughly three days of history at three connected
// agents and about a day at ten, which is the window worth keeping for mail-loss
// triage. Raise it if worker counts grow. Spec: harness_spec.md 15.1.
export const DEFAULT_MAX_LOG_BYTES = 16 * 1024 * 1024;

const STRING_FIELDS = new Set([
  "message_id",
  "occurrence_id",
  "attempt_id",
  "agent_id",
  "instance_id",
  "lease_id",
  "from",
  "to",
  "state",
  "reason",
  "error_code",
  "reminder_id",
  "linked_message_id",
  "job_key",
  "scheduled_for",
  "migration_id",
  "definition_fingerprint",
  "delivery_expires_at",
  "next_due_at",
  "signal",
  "host",
  "router_url",
]);
const NUMBER_FIELDS = new Set([
  "attempt",
  "port",
  "stale_agent_count",
  "expired_message_count",
  "timed_out_delivery_count",
  "reminder_success_count",
  "reminder_failure_count",
  "server_job_success_count",
  "server_job_failure_count",
]);
const BOOLEAN_FIELDS = new Set(["created", "deduped", "recovered", "released", "terminal"]);
const EVENT_PATTERN = /^[a-z][a-z0-9_]{0,63}$/;
const EVENT_SCHEMAS = Object.freeze({
  schema_migration: schema(["migration_id"]),
  server_job_registered: schema(["job_key", "definition_fingerprint", "scheduled_for"]),
  router_started: schema(["host", "port", "router_url"]),
  router_stopped: schema(),
  lifecycle_sweep_completed: schema([
    "stale_agent_count",
    "expired_message_count",
    "timed_out_delivery_count",
    "reminder_success_count",
    "reminder_failure_count",
    "server_job_success_count",
    "server_job_failure_count",
  ]),
  router_error: schema(["error_code"], ["message_id", "agent_id", "instance_id", "lease_id"]),
  agent_registered: schema(["agent_id", "instance_id", "lease_id", "recovered"]),
  agent_disconnected: schema(["agent_id", "instance_id", "lease_id", "released"]),
  agent_registration_rejected: schema(["agent_id", "instance_id", "error_code"]),
  agent_lease_renewed: schema(["agent_id", "instance_id", "lease_id"]),
  message_enqueued: schema(["message_id", "from", "to", "agent_id", "state", "created"], ["deduped"]),
  delivery_offered: schema(["message_id", "agent_id", "attempt", "lease_id", "delivery_expires_at"]),
  delivery_injection_requested: schema(["message_id", "agent_id", "attempt", "lease_id"]),
  delivery_acknowledged: schema(["message_id", "agent_id", "attempt", "lease_id"]),
  delivery_nacked: schema(["message_id", "agent_id", "attempt", "lease_id", "reason", "state"]),
  message_dead_lettered: schema(["message_id", "reason"], ["agent_id", "attempt", "lease_id"]),
  delivery_timed_out: schema(["message_id", "agent_id", "attempt", "lease_id", "state", "reason"]),
  message_expired: schema(["message_id"]),
  reminder_started: schema(["reminder_id", "scheduled_for"]),
  reminder_succeeded: schema(["reminder_id", "scheduled_for", "linked_message_id", "agent_id"]),
  reminder_failed: schema(["reminder_id", "scheduled_for", "error_code"]),
  recurring_reminder_rearmed: schema(["reminder_id", "message_id", "next_due_at", "reason"]),
  server_job_started: schema(["job_key", "scheduled_for"]),
  server_job_succeeded: schema(["job_key", "scheduled_for", "attempt", "message_id"]),
  server_job_failed: schema(["job_key", "scheduled_for", "attempt", "error_code", "terminal"]),
  message_retried: schema(["message_id", "agent_id", "state"]),
});

export function rotateJsonlLog(logPath, maxBytes = DEFAULT_MAX_LOG_BYTES) {
  return rotateSizeCappedLog(logPath, maxBytes);
}

export function createJsonlLogger({ logPath, clock = () => new Date(), maxBytes = DEFAULT_MAX_LOG_BYTES } = {}) {
  if (typeof logPath !== "string" || logPath.length === 0) {
    throw new TypeError("logPath is required");
  }
  if (typeof clock !== "function") throw new TypeError("clock must be a function");
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) throw new TypeError("maxBytes must be a positive safe integer");

  let rotationFailureLogged = false;

  return {
    log(event, fields = {}) {
      try {
        const record = toRecord(event, fields, clock);
        mkdirSync(dirname(logPath), { recursive: true, mode: 0o700 });
        // Retention is best effort. A rotation that cannot complete — an obstructed
        // `<log>.1`, a permissions change — must not take the write down with it: the
        // log is over its limit either way, and dropping the record would silence the
        // event log permanently, which is far worse than an oversized file. Record the
        // first failure so a log past its documented bound is diagnosable in-band
        // instead of only visible to whoever thinks to run `ls`.
        const failure = rotateOrReportFailure();
        const pending = failure === undefined ? [record] : [failure, record];
        appendFileSync(
          logPath,
          pending.map((entry) => `${JSON.stringify(entry)}\n`).join(""),
          { encoding: "utf8", mode: 0o600 },
        );
        chmodSync(logPath, 0o600);
        return true;
      } catch {
        return false;
      }
    },
    close() {
      return true;
    },
  };

  // Returns a record describing the first rotation failure, or undefined.
  function rotateOrReportFailure() {
    try {
      rotateJsonlLog(logPath, maxBytes);
      return undefined;
    } catch {
      if (rotationFailureLogged) return undefined;
      rotationFailureLogged = true;
      try {
        return toRecord("router_error", { error_code: "log_rotation_failed" }, clock);
      } catch {
        return undefined;
      }
    }
  }
}

function toRecord(event, fields, clock) {
  if (typeof event !== "string" || !EVENT_PATTERN.test(event)) {
    throw new TypeError("event is invalid");
  }
  const schemaForEvent = EVENT_SCHEMAS[event];
  if (schemaForEvent === undefined) throw new TypeError("event is not declared");
  if (fields === null || typeof fields !== "object" || Array.isArray(fields)) {
    throw new TypeError("fields must be an object");
  }
  const now = clock();
  if (!(now instanceof Date) || Number.isNaN(now.getTime())) {
    throw new TypeError("clock must return a valid Date");
  }
  const record = { timestamp: now.toISOString(), event };
  for (const [key, value] of Object.entries(fields)) {
    if (!schemaForEvent.allowed.has(key) || !isSafeFieldValue(key, value)) {
      throw new TypeError(`field ${key} is invalid for ${event}`);
    }
    record[key] = value;
  }
  for (const key of schemaForEvent.required) {
    if (!(key in record)) throw new TypeError(`field ${key} is required for ${event}`);
  }
  return record;
}

function schema(required = [], optional = []) {
  return Object.freeze({
    required: Object.freeze([...required]),
    allowed: new Set([...required, ...optional]),
  });
}

function isSafeFieldValue(key, value) {
  if (STRING_FIELDS.has(key)) return typeof value === "string";
  if (NUMBER_FIELDS.has(key)) return Number.isSafeInteger(value) && value >= 0;
  if (BOOLEAN_FIELDS.has(key)) return typeof value === "boolean";
  return false;
}
