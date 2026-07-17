import { appendFileSync, chmodSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

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
const BOOLEAN_FIELDS = new Set(["created", "recovered", "released", "terminal"]);
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
  message_enqueued: schema(["message_id", "from", "to", "agent_id", "state", "created"]),
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
  server_job_started: schema(["job_key", "scheduled_for"]),
  server_job_succeeded: schema(["job_key", "scheduled_for", "attempt", "message_id"]),
  server_job_failed: schema(["job_key", "scheduled_for", "attempt", "error_code", "terminal"]),
  message_retried: schema(["message_id", "agent_id", "state"]),
});

export function createJsonlLogger({ logPath, clock = () => new Date() } = {}) {
  if (typeof logPath !== "string" || logPath.length === 0) {
    throw new TypeError("logPath is required");
  }
  if (typeof clock !== "function") throw new TypeError("clock must be a function");

  return {
    log(event, fields = {}) {
      try {
        const record = toRecord(event, fields, clock);
        mkdirSync(dirname(logPath), { recursive: true, mode: 0o700 });
        appendFileSync(logPath, `${JSON.stringify(record)}\n`, { encoding: "utf8", mode: 0o600 });
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
