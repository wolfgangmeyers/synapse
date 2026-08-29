import { createHash, randomUUID } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { chmodSync, existsSync, readFileSync } from "node:fs";

import {
  ABSOLUTE_MAX_BODY_BYTES,
  DEFAULT_MAX_BODY_BYTES,
  ProtocolValidationError,
  validateDeliveryIdentity,
  validateEnvelope,
  validateLeaseIdentity,
} from "./protocol.js";

const ONE_HOUR_MS = 60 * 60 * 1000;
const REMINDER_FAILURE_RETRY_MS = 60 * 1000;
export const DEFAULT_SERVER_JOB_MAX_ATTEMPTS = 3;
export const SERVER_JOB_FAILURE_RETRY_MS = 60 * 1000;
const MAX_UTC_TIMESTAMP_MS = Date.parse("9999-12-31T23:59:59.999Z");
const DEFAULT_BUSY_TIMEOUT_MS = 5_000;
const MESSAGE_INCARNATION_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
export const DEFAULT_LEASE_DURATION_MS = 30_000;
export const DEFAULT_DELIVERY_LEASE_DURATION_MS = 30_000;
export const DEFAULT_MAX_DELIVERY_ATTEMPTS = 3;
export const DEFAULT_MAX_QUEUED_MESSAGES = 15;
export const DEFAULT_MAX_QUEUED_BYTES = 256 * 1024;
export const DEFAULT_MAX_BATCH_BODY_CHARS = 4_096;
const DEFAULT_MAX_BATCH_MESSAGES = 10;
export const DEFAULT_CONVERSATION_HISTORY_LIMIT = 200;
export const DEFAULT_CONVERSATION_RETENTION_MS = 30 * 24 * 60 * 60 * 1_000;
const MESSAGE_SOURCE_KINDS = new Set(["agent", "web_ui", "android", "discord", "reminder", "system", "unknown"]);
const migrations = Object.freeze([
  {
    id: "001_initial",
    sql: readFileSync(new URL("./migrations/001_initial.sql", import.meta.url), "utf8"),
  },
  {
    id: "002_delivery_attempt_deadline",
    sql: readFileSync(
      new URL("./migrations/002_delivery_attempt_deadline.sql", import.meta.url),
      "utf8",
    ),
  },
  {
    id: "003_reminders",
    sql: readFileSync(new URL("./migrations/003_reminders.sql", import.meta.url), "utf8"),
  },
  {
    id: "004_server_jobs",
    sql: readFileSync(new URL("./migrations/004_server_jobs.sql", import.meta.url), "utf8"),
  },
  {
    id: "005_message_incarnation",
    sql: readFileSync(new URL("./migrations/005_message_incarnation.sql", import.meta.url), "utf8"),
  },
  {
    id: "006_sentinel",
    sql: readFileSync(new URL("./migrations/006_sentinel.sql", import.meta.url), "utf8"),
  },
  {
    id: "007_sentinel_occurrences",
    sql: readFileSync(new URL("./migrations/007_sentinel_occurrences.sql", import.meta.url), "utf8"),
  },
  {
    id: "008_sentinel_deadline",
    sql: readFileSync(new URL("./migrations/008_sentinel_deadline.sql", import.meta.url), "utf8"),
  },
  {
    id: "009_sentinel_tombstones",
    sql: readFileSync(new URL("./migrations/009_sentinel_tombstones.sql", import.meta.url), "utf8"),
  },
  {
    id: "010_sentinel_exit_outcome",
    sql: readFileSync(new URL("./migrations/010_sentinel_exit_outcome.sql", import.meta.url), "utf8"),
  },
  {
    id: "011_sentinel_relay_capabilities",
    sql: readFileSync(new URL("./migrations/011_sentinel_relay_capabilities.sql", import.meta.url), "utf8"),
  },
  {
    id: "012_sentinel_native_cleanup",
    sql: readFileSync(new URL("./migrations/012_sentinel_native_cleanup.sql", import.meta.url), "utf8"),
  },
  {
    id: "013_conversation_history",
    sql: readFileSync(new URL("./migrations/013_conversation_history.sql", import.meta.url), "utf8"),
  },
  {
    id: "014_reminder_batch_links",
    sql: readFileSync(new URL("./migrations/014_reminder_batch_links.sql", import.meta.url), "utf8"),
  },
  {
    id: "015_conversation_source",
    sql: readFileSync(new URL("./migrations/015_conversation_source.sql", import.meta.url), "utf8"),
  },
  {
    id: "016_delivery_batches",
    sql: readFileSync(new URL("./migrations/016_delivery_batches.sql", import.meta.url), "utf8"),
  },
  {
    id: "017_subagent_lifecycle",
    sql: readFileSync(new URL("./migrations/017_subagent_lifecycle.sql", import.meta.url), "utf8"),
  },
  {
    id: "018_subagent_lifecycle_status",
    sql: readFileSync(new URL("./migrations/018_subagent_lifecycle_status.sql", import.meta.url), "utf8"),
  },
  {
    id: "019_goal_control_desired_state",
    sql: readFileSync(new URL("./migrations/019_goal_control_desired_state.sql", import.meta.url), "utf8"),
  },
  {
    id: "020_prompt_templates",
    sql: readFileSync(new URL("./migrations/020_prompt_templates.sql", import.meta.url), "utf8"),
  },
]);

export class IdempotencyConflictError extends Error {
  constructor(id) {
    super(`message ID ${id} already belongs to a different envelope`);
    this.name = "IdempotencyConflictError";
    this.code = "idempotency_conflict";
  }
}

export class QueueLimitError extends Error {
  constructor(recipient) {
    super(`recipient ${recipient} has reached its queue limit`);
    this.name = "QueueLimitError";
    this.code = "queue_full";
  }
}

export class DuplicateAgentLeaseError extends Error {
  constructor(agentId, activeInstanceId) {
    super(`agent ${agentId} already has an active lease`);
    this.name = "DuplicateAgentLeaseError";
    this.code = "duplicate_identity";
    this.agentId = agentId;
    this.activeInstanceId = activeInstanceId;
  }
}

export class LeaseNotActiveError extends Error {
  constructor(agentId) {
    super(`agent ${agentId} does not own an active lease`);
    this.name = "LeaseNotActiveError";
    this.code = "lease_not_active";
    this.agentId = agentId;
  }
}

export class DeliveryNotActiveError extends Error {
  constructor(messageId, attemptNumber) {
    super(`delivery attempt ${messageId}/${attemptNumber} is not active`);
    this.name = "DeliveryNotActiveError";
    this.code = "delivery_not_active";
    this.messageId = messageId;
    this.attemptNumber = attemptNumber;
  }
}

export class ReminderIdempotencyConflictError extends Error {
  constructor(id) {
    super(`reminder ID ${id} already belongs to a different reminder`);
    this.name = "ReminderIdempotencyConflictError";
    this.code = "reminder_idempotency_conflict";
  }
}

export class ReminderCancellationConflictError extends Error {
  constructor(id, state, linkedMessageState) {
    super(`reminder ${id} is ${state} and cannot be canceled`);
    this.name = "ReminderCancellationConflictError";
    this.code = "reminder_not_cancelable";
    this.id = id;
    this.state = state;
    this.linkedMessageState = linkedMessageState;
  }
}

export class ReminderValidationError extends Error {
  constructor(message = "invalid reminder") {
    super(message);
    this.name = "ReminderValidationError";
    this.code = "invalid_reminder";
  }
}

export class PromptTemplateValidationError extends Error {
  constructor(message = "invalid prompt template") {
    super(message);
    this.name = "PromptTemplateValidationError";
    this.code = "invalid_prompt_template";
  }
}

export class PromptTemplateNameConflictError extends Error {
  constructor(name) {
    super(`prompt template name ${name} already exists`);
    this.name = "PromptTemplateNameConflictError";
    this.code = "prompt_template_name_conflict";
  }
}

export class PromptTemplateCapacityError extends Error {
  constructor() {
    super("prompt template collection is full");
    this.name = "PromptTemplateCapacityError";
    this.code = "prompt_template_capacity_exceeded";
  }
}

const PROMPT_TEMPLATE_MAX_NAME_BYTES = 128;
const PROMPT_TEMPLATE_MAX_BODY_BYTES = 64 * 1024;
const PROMPT_TEMPLATE_MAX_ID_BYTES = 1_024;
const PROMPT_TEMPLATE_ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const PROMPT_TEMPLATE_TIMESTAMP_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
const MAX_PROMPT_TEMPLATES = 100;
const MAX_GOAL_CONTROL_DESIRED_CONFIG_BYTES = 16_384;

function assertGoalControlDesiredIdentity(agentId, instanceId) {
  if (typeof agentId !== "string" || agentId.length === 0 || agentId.length > 128
    || typeof instanceId !== "string" || instanceId.length === 0 || instanceId.length > 128) {
    throw new TypeError("goal control desired state needs an agent and instance identity");
  }
}

function assertGoalControlDesiredTimestamp(value, label) {
  if (typeof value !== "string" || !Number.isFinite(Date.parse(value))) {
    throw new TypeError(`${label} must be an ISO timestamp`);
  }
}

function serializeGoalControlDesiredConfig(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError("goal control desired config must be an object");
  }
  let serialized;
  try {
    serialized = JSON.stringify(value);
  } catch {
    throw new TypeError("goal control desired config must be serializable");
  }
  if (typeof serialized !== "string" || Buffer.byteLength(serialized, "utf8") > MAX_GOAL_CONTROL_DESIRED_CONFIG_BYTES) {
    throw new TypeError("goal control desired config is too large");
  }
  return serialized;
}

function goalControlDesiredFromRow(row) {
  if (row === undefined) return undefined;
  try {
    const config = JSON.parse(row.config_json);
    if (!config || typeof config !== "object" || Array.isArray(config)) return undefined;
    return {
      agent_id: row.agent_id,
      instance_id: row.instance_id,
      revision: row.revision,
      config,
      updated_at: row.updated_at,
      expires_at: row.expires_at,
    };
  } catch {
    return undefined;
  }
}

export function openMessageRepository({
  databasePath,
  clock = () => new Date(),
  busyTimeoutMs = DEFAULT_BUSY_TIMEOUT_MS,
  maxBodyBytes = DEFAULT_MAX_BODY_BYTES,
  maxQueuedMessages = DEFAULT_MAX_QUEUED_MESSAGES,
  maxQueuedBytes = DEFAULT_MAX_QUEUED_BYTES,
  maxBatchBodyChars = DEFAULT_MAX_BATCH_BODY_CHARS,
  // Test fixtures may inject a sequenced source. Production keeps random UUIDs.
  idSource,
  migrations: migrationPlan = migrations,
} = {}) {
  if (typeof databasePath !== "string" || databasePath.length === 0) {
    throw new TypeError("databasePath is required");
  }
  if (databasePath === ":memory:") {
    throw new TypeError("databasePath must identify a persistent SQLite file");
  }
  if (!Number.isSafeInteger(busyTimeoutMs) || busyTimeoutMs < 0) {
    throw new TypeError("busyTimeoutMs must be a non-negative safe integer");
  }
  assertPositiveLimit(maxBodyBytes, "maxBodyBytes", ABSOLUTE_MAX_BODY_BYTES);
  assertPositiveLimit(maxQueuedMessages, "maxQueuedMessages");
  assertPositiveLimit(maxQueuedBytes, "maxQueuedBytes");
  assertNonNegativeLimit(maxBatchBodyChars, "maxBatchBodyChars");

  const database = new DatabaseSync(databasePath, {
    timeout: busyTimeoutMs,
    enableForeignKeyConstraints: true,
  });
  try {
    configureDatabase(database, busyTimeoutMs);
    restrictStateFiles(databasePath);
    applyMigrations(database, migrationPlan, clock, databasePath);
    restrictStateFiles(databasePath);
    return new MessageRepository(database, clock, databasePath, {
      maxBodyBytes,
      maxQueuedMessages,
      maxQueuedBytes,
      maxBatchBodyChars,
      idSource,
    });
  } catch (error) {
    database.close();
    throw error;
  }
}

class MessageRepository {
  #database;
  #clock;
  #databasePath;
  #maxBodyBytes;
  #maxQueuedMessages;
  #maxQueuedBytes;
  #maxBatchBodyChars;
  #idSource;
  #reminderIncarnations = new Map();

  constructor(database, clock, databasePath, { maxBodyBytes, maxQueuedMessages, maxQueuedBytes, maxBatchBodyChars, idSource }) {
    this.#database = database;
    this.#clock = clock;
    this.#databasePath = databasePath;
    this.#maxBodyBytes = maxBodyBytes;
    this.#maxQueuedMessages = maxQueuedMessages;
    this.#maxQueuedBytes = maxQueuedBytes;
    this.#maxBatchBodyChars = maxBatchBodyChars;
    this.#idSource = idSource ?? randomUUID;
    if (typeof this.#idSource !== "function") throw new TypeError("idSource must be a function");
  }

  #nextId() {
    const id = this.#idSource();
    if (typeof id !== "string" || id.length === 0) throw new TypeError("idSource must return a non-empty string");
    return id;
  }

  #nextPromptTemplateId() {
    const id = this.#nextId();
    if (!PROMPT_TEMPLATE_ID_PATTERN.test(id)) throw new PromptTemplateValidationError();
    return id;
  }

  close() {
    this.#reminderIncarnations.clear();
    this.#database.close();
  }

  enqueue(envelope, { conversation, disableBatching = false } = {}) {
    if (typeof disableBatching !== "boolean") throw new TypeError("disableBatching must be a boolean");
    const candidate = prepareEnvelope(validateEnvelope(envelope, {
      maxBodyBytes: this.#maxBodyBytes,
    }));
    const conversationCandidate = prepareConversationCandidate(candidate, conversation);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const existingConversation = selectLatestConversationMessage(this.#database, candidate.id);
      if (existingConversation !== undefined) {
        if (!sameConversationCandidate(existingConversation, conversationCandidate)) {
          throw new IdempotencyConflictError(candidate.id);
        }
        const deliveryMessage = selectMessage(this.#database, existingConversation.delivery_message_id)
          ?? messageFromConversationCandidate(candidate, existingConversation);
        return {
          created: false,
          message: deliveryMessage,
          conversation_message: toConversationMessage(existingConversation),
        };
      }
      const result = enqueueCandidate(this.#database, candidate, {
        firstSeenAt: clockTimestamp(this.#clock),
        maxBodyBytes: this.#maxBodyBytes,
        maxQueuedMessages: this.#maxQueuedMessages,
        maxQueuedBytes: this.#maxQueuedBytes,
        maxBatchBodyChars: disableBatching ? 0 : this.#maxBatchBodyChars,
        idSource: () => this.#nextId(),
      });
      if (result.created || result.batched || result.deduped) {
        const messageIncarnationId = result.message.id === candidate.id
          ? result.message.message_incarnation_id
          : this.#nextId();
        insertConversationMessage(this.#database, conversationCandidate, {
          messageIncarnationId,
          deliveryMessageId: result.message.id,
          recordedAt: clockTimestamp(this.#clock),
        });
        result.conversation_message = selectConversationMessage(
          this.#database,
          candidate.id,
          messageIncarnationId,
        );
        result.conversation_created = true;
      }
      return result;
    });
  }

  createReminder(reminder) {
    const candidate = validateReminder(reminder, { maxBodyBytes: this.#maxBodyBytes });
    const now = clockTimestamp(this.#clock);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const existing = selectReminder(this.#database, candidate.id);
      if (existing !== undefined) {
        if (!sameReminder(existing, candidate)) {
          throw new ReminderIdempotencyConflictError(candidate.id);
        }
        if (!this.#reminderIncarnations.has(candidate.id)) this.#reminderIncarnations.set(candidate.id, this.#nextId());
        return { created: false, reminder: existing };
      }
      this.#database.prepare(`
        INSERT INTO agents (agent_id, first_seen_at)
        VALUES (?, ?)
        ON CONFLICT (agent_id) DO NOTHING
      `).run(candidate.target, now);
      this.#database.prepare(`
        INSERT INTO reminders (
          id, target, creator, subject, body, created_at, next_due_at,
          interval_ms, sentinel_json, state, linked_message_id, occurrence_number,
          attempt_count, last_attempt_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'scheduled', NULL, 0, 0, NULL)
      `).run(
        candidate.id,
        candidate.target,
        candidate.creator,
        candidate.subject,
        candidate.body,
        candidate.created_at,
        candidate.next_due_at,
        candidate.interval_ms,
        candidate.sentinel === undefined ? null : JSON.stringify(candidate.sentinel),
      );
      this.#reminderIncarnations.set(candidate.id, this.#nextId());
      return { created: true, reminder: selectReminder(this.#database, candidate.id) };
    });
  }

  reminderFor(reminderId) {
    return selectReminder(this.#database, reminderId);
  }

  promptTemplates() {
    return this.#database.prepare(`
      SELECT id, name, body, created_at, updated_at
      FROM prompt_templates
      ORDER BY created_at ASC, id ASC
    `).all().map(toPromptTemplate);
  }

  promptTemplateFor(promptTemplateId) {
    return selectPromptTemplate(this.#database, promptTemplateId);
  }

  createPromptTemplate(promptTemplate) {
    const candidate = validatePromptTemplate(promptTemplate);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const existing = this.#database.prepare(`
        SELECT id
        FROM prompt_templates
        WHERE name = ?
      `).get(candidate.name);
      if (existing !== undefined) throw new PromptTemplateNameConflictError(candidate.name);
      const count = this.#database.prepare("SELECT COUNT(*) AS count FROM prompt_templates").get().count;
      if (count >= MAX_PROMPT_TEMPLATES) throw new PromptTemplateCapacityError();
      const existingId = candidate.id === undefined
        ? undefined
        : this.#database.prepare("SELECT id FROM prompt_templates WHERE id = ?").get(candidate.id);
      if (existingId !== undefined) throw new PromptTemplateValidationError();
      const id = candidate.id ?? this.#nextPromptTemplateId();
      const createdAt = clockTimestamp(this.#clock);
      const updatedAt = candidate.updated_at ?? createdAt;
      this.#database.prepare(`
        INSERT INTO prompt_templates (id, name, body, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)
      `).run(id, candidate.name, candidate.body, createdAt, updatedAt);
      return { created: true, prompt_template: selectPromptTemplate(this.#database, id) };
    });
  }

  updatePromptTemplate(promptTemplateId, patch) {
    const candidatePatch = validatePromptTemplatePatch(patch);
    if (!isPortablePromptTemplateId(promptTemplateId)) return undefined;
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const existing = selectPromptTemplate(this.#database, promptTemplateId);
      if (existing === undefined) return undefined;
      const candidate = { ...existing, ...candidatePatch };
      validatePromptTemplate({ name: candidate.name, body: candidate.body });
      const conflict = this.#database.prepare(`
        SELECT id
        FROM prompt_templates
        WHERE name = ? AND id <> ?
      `).get(candidate.name, promptTemplateId);
      if (conflict !== undefined) throw new PromptTemplateNameConflictError(candidate.name);
      const updatedAt = candidatePatch.updated_at ?? clockTimestamp(this.#clock);
      this.#database.prepare(`
        UPDATE prompt_templates
        SET name = ?, body = ?, updated_at = ?
        WHERE id = ?
      `).run(candidate.name, candidate.body, updatedAt, promptTemplateId);
      return { prompt_template: selectPromptTemplate(this.#database, promptTemplateId) };
    });
  }

  deletePromptTemplate(promptTemplateId) {
    if (!isPortablePromptTemplateId(promptTemplateId)) return false;
    return immediateTransaction(this.#database, this.#databasePath, () => (
      this.#database.prepare("DELETE FROM prompt_templates WHERE id = ?").run(promptTemplateId).changes === 1
    ));
  }

  reminderIncarnationFor(reminderId) {
    if (selectReminder(this.#database, reminderId) === undefined) {
      this.#reminderIncarnations.delete(reminderId);
      return undefined;
    }
    let incarnation = this.#reminderIncarnations.get(reminderId);
    if (incarnation === undefined) {
      incarnation = this.#nextId();
      this.#reminderIncarnations.set(reminderId, incarnation);
    }
    return incarnation;
  }

  reminders() {
    return this.#database.prepare(`
      SELECT
        reminders.id,
        reminders.target,
        reminders.creator,
        reminders.subject,
        reminders.body,
        reminders.created_at,
        reminders.next_due_at,
        reminders.interval_ms,
        reminders.sentinel_json,
        reminders.state,
        reminders.linked_message_id,
        messages.state AS linked_message_state,
        reminders.occurrence_number,
        reminders.attempt_count,
        reminders.last_attempt_at
      FROM reminders
      LEFT JOIN messages ON messages.id = reminders.linked_message_id
      ORDER BY reminders.next_due_at ASC, reminders.id ASC
    `).all().map(toReminder);
  }

  cancelReminder(reminderId) {
    assertReminderId(reminderId);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const reminder = selectReminder(this.#database, reminderId);
      if (reminder === undefined) return undefined;
      const cancelable = reminder.state === "failed"
        || (reminder.state === "scheduled" && reminder.linked_message_id === null);
      if (!cancelable) {
        throw new ReminderCancellationConflictError(
          reminder.id,
          reminder.state,
          reminder.linked_message_state,
        );
      }
      const result = this.#database.prepare(`
        DELETE FROM reminders
        WHERE id = ?
          AND (state = 'failed' OR (state = 'scheduled' AND linked_message_id IS NULL))
      `).run(reminderId);
      if (result.changes !== 1) {
        const current = selectReminder(this.#database, reminderId);
        if (current === undefined) return undefined;
        throw new ReminderCancellationConflictError(
          current.id,
          current.state,
          current.linked_message_state,
        );
      }
      this.#reminderIncarnations.delete(reminderId);
      return { canceled: true, reminder };
    });
  }

  cancelAllReminders() {
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const linked = this.#database.prepare(`
        SELECT
          reminders.id,
          reminders.state,
          messages.state AS linked_message_state
        FROM reminders
        LEFT JOIN messages ON messages.id = reminders.linked_message_id
        WHERE reminders.state NOT IN ('scheduled', 'failed')
          OR (reminders.state = 'scheduled' AND reminders.linked_message_id IS NOT NULL)
        ORDER BY reminders.id ASC
        LIMIT 1
      `).get();
      if (linked !== undefined) {
        throw new ReminderCancellationConflictError(
          linked.id,
          linked.state,
          linked.linked_message_state,
        );
      }
      const canceledIds = this.#database.prepare(`
        SELECT id
        FROM reminders
        WHERE state = 'failed' OR (state = 'scheduled' AND linked_message_id IS NULL)
      `).all().map(({ id }) => id);
      const result = this.#database.prepare(`
        DELETE FROM reminders
        WHERE state = 'failed' OR (state = 'scheduled' AND linked_message_id IS NULL)
      `).run();
      for (const reminderId of canceledIds) this.#reminderIncarnations.delete(reminderId);
      return { canceled: result.changes };
    });
  }

  updateReminder(reminderId, patch) {
    assertReminderId(reminderId);
    if (patch === null || typeof patch !== "object" || Array.isArray(patch)) {
      throw new ReminderValidationError();
    }
    // PATCH intentionally does not accept `state`: scheduling transitions are
    // driven by the delivery lifecycle, and allowing an arbitrary state write
    // here could desync a reminder from its linked message. Callers mutate
    // cadence/content/target only.
    const allowedPatchKeys = new Set(["interval_ms", "next_due_at", "body", "subject", "target"]);
    const patchKeys = Object.keys(patch);
    if (patchKeys.length === 0 || patchKeys.some((key) => !allowedPatchKeys.has(key))) {
      throw new ReminderValidationError();
    }
    const now = clockTimestamp(this.#clock);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const existing = selectReminder(this.#database, reminderId);
      if (existing === undefined) return undefined;
      const nextDueAtSupplied = Object.prototype.hasOwnProperty.call(patch, "next_due_at");
      const candidate = {
        id: existing.id,
        target: existing.target,
        creator: existing.creator,
        body: existing.body,
        created_at: existing.created_at,
        next_due_at: existing.next_due_at,
        interval_ms: existing.interval_ms,
        ...(existing.subject === undefined ? {} : { subject: existing.subject }),
        ...(existing.sentinel === undefined ? {} : { sentinel: existing.sentinel }),
        ...patch,
      };
      // subject:null clears the subject. validateReminder treats an absent
      // subject as null, while the envelope schema rejects an explicit null,
      // so drop the key to represent "cleared".
      if (candidate.subject === null) delete candidate.subject;
      // Validate the merged reminder with the same rules as create before any
      // recompute, so a bad interval/next_due_at/body surfaces as a
      // ReminderValidationError rather than a downstream RangeError.
      const validated = validateReminder(candidate, { maxBodyBytes: this.#maxBodyBytes });
      const intervalChanged = validated.interval_ms !== existing.interval_ms;
      let nextDueAt = validated.next_due_at;
      // When the cadence changes and the caller did not name a new due time,
      // re-arm from "now" so the new interval takes effect immediately instead
      // of firing once at the stale time. Setting interval_ms to null keeps
      // the existing next_due_at (one-shot) unless the caller overrides it.
      if (intervalChanged && !nextDueAtSupplied && validated.interval_ms !== null) {
        nextDueAt = timestampAfter(now, validated.interval_ms);
      }
      const schedulingChanged = intervalChanged || nextDueAt !== existing.next_due_at;
      // A changed target must resolve to a known agent row before the reminder
      // can fire there, matching the createReminder precondition.
      this.#database.prepare(`
        INSERT INTO agents (agent_id, first_seen_at)
        VALUES (?, ?)
        ON CONFLICT (agent_id) DO NOTHING
      `).run(validated.target, now);
      // Preserve id, created_at, creator, occurrence_number, and sentinel.
      // Reset retry accounting whenever the schedule moved so a freshly re-armed
      // reminder is not immediately treated as a stuck failure.
      this.#database.prepare(`
        UPDATE reminders
        SET target = ?,
            subject = ?,
            body = ?,
            next_due_at = ?,
            interval_ms = ?,
            attempt_count = CASE WHEN ? THEN 0 ELSE attempt_count END,
            last_attempt_at = CASE WHEN ? THEN NULL ELSE last_attempt_at END
        WHERE id = ?
      `).run(
        validated.target,
        validated.subject,
        validated.body,
        nextDueAt,
        validated.interval_ms,
        schedulingChanged ? 1 : 0,
        schedulingChanged ? 1 : 0,
        reminderId,
      );
      return {
        schedulingChanged,
        reminder: selectReminder(this.#database, reminderId),
      };
    });
  }

  fireDueReminders({ onAttempt } = {}) {
    if (onAttempt !== undefined && typeof onAttempt !== "function") {
      throw new TypeError("onAttempt must be a function");
    }
    const now = clockTimestamp(this.#clock);
    const due = this.#database.prepare(`
      SELECT id, next_due_at, last_attempt_at
      FROM reminders
      WHERE state = 'scheduled'
        AND next_due_at <= ?
      ORDER BY next_due_at ASC, id ASC
    `).all(now);
    const outcomes = [];
    for (const { id, next_due_at: nextDueAt, last_attempt_at: lastAttemptAt } of due) {
      if (!reminderRetryIsDue(lastAttemptAt, now)) continue;
      try {
        const fired = immediateTransaction(this.#database, this.#databasePath, () => (
          fireDueReminder(this.#database, id, now, {
            maxBodyBytes: this.#maxBodyBytes,
            maxQueuedMessages: this.#maxQueuedMessages,
            maxQueuedBytes: this.#maxQueuedBytes,
            maxBatchBodyChars: this.#maxBatchBodyChars,
            idSource: () => this.#nextId(),
            onAttempt: () => {
              try {
                onAttempt?.({ reminderId: id, scheduledFor: nextDueAt });
              } catch {
                // An observer cannot alter durable reminder scheduling.
              }
            },
          })
        ));
        if (fired !== undefined) outcomes.push(fired);
      } catch (error) {
        try {
          immediateTransaction(this.#database, this.#databasePath, () => {
            this.#database.prepare(`
              UPDATE reminders
              SET attempt_count = attempt_count + 1,
                  last_attempt_at = ?
              WHERE id = ? AND state = 'scheduled'
            `).run(now, id);
          });
        } catch {
          // The original failure is still reported; a failed retry marker must not stop later due rows.
        }
        outcomes.push({
          reminder_id: id,
          error_code: reminderFireErrorCode(error),
        });
      }
    }
    return outcomes;
  }

  synchronizeServerJobs(definitions) {
    const jobs = validateServerJobDefinitions(definitions);
    const byKey = new Map(jobs.map((job) => [job.key, job]));
    const now = clockTimestamp(this.#clock);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const persisted = this.#database.prepare(`
        SELECT
          job_key,
          schedule_interval_hours,
          schedule_anchor_hour,
          schedule_minute,
          schedule_time_zone,
          definition_fingerprint
        FROM server_jobs
        ORDER BY job_key ASC
      `).all();
      for (const row of persisted) {
        const definition = byKey.get(row.job_key);
        if (definition === undefined) {
          throw new Error(`unknown persisted server job ${row.job_key}`);
        }
        if (row.schedule_interval_hours !== definition.scheduleIntervalHours
          || row.schedule_anchor_hour !== definition.scheduleAnchorHour
          || row.schedule_minute !== definition.scheduleMinute
          || row.schedule_time_zone !== definition.scheduleTimeZone
          || row.definition_fingerprint !== definition.definitionFingerprint) {
          throw new Error(`persisted server job ${row.job_key} differs from its static definition`);
        }
      }
      for (const job of jobs) {
        this.#database.prepare(`
          INSERT INTO server_jobs (
            job_key, schedule_interval_hours, schedule_anchor_hour,
            schedule_minute, schedule_time_zone, definition_fingerprint, next_due_at
          ) VALUES (?, ?, ?, ?, ?, ?, ?)
          ON CONFLICT (job_key) DO NOTHING
        `).run(
          job.key,
          job.scheduleIntervalHours,
          job.scheduleAnchorHour,
          job.scheduleMinute,
          job.scheduleTimeZone,
          job.definitionFingerprint,
          firstServerJobDueAt(job, now),
        );
      }
      return jobs.map((job) => selectServerJob(this.#database, job.key));
    });
  }

  dueServerJobs() {
    const now = clockTimestamp(this.#clock);
    const due = this.#database.prepare(`
      SELECT
        job_key,
        schedule_interval_hours,
        schedule_anchor_hour,
        schedule_minute,
        schedule_time_zone,
        definition_fingerprint,
        next_due_at
      FROM server_jobs
      WHERE next_due_at <= ?
      ORDER BY next_due_at ASC, job_key ASC
    `).all(now).map(toServerJob);
    return due.filter((job) => {
      const latest = selectLatestServerJobRun(this.#database, job.job_key, job.next_due_at);
      return latest === undefined || (latest.outcome === "failed" && latest.retry_at <= now);
    });
  }

  completeServerJobRun({ jobKey, scheduledFor, envelopeForNow }) {
    assertServerJobKey(jobKey);
    assertTimestamp(scheduledFor, "scheduledFor");
    if (typeof envelopeForNow !== "function") {
      throw new TypeError("envelopeForNow must be a function");
    }
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const now = clockTimestamp(this.#clock);
      const job = selectServerJob(this.#database, jobKey);
      if (!serverJobRunIsDue(this.#database, job, scheduledFor, now)) return undefined;
      const latest = selectLatestServerJobRun(this.#database, jobKey, scheduledFor);
      const candidate = prepareEnvelope(validateEnvelope(envelopeForNow(now), {
        maxBodyBytes: this.#maxBodyBytes,
      }));
      const message = enqueueCandidate(this.#database, candidate, {
        firstSeenAt: now,
        maxQueuedMessages: this.#maxQueuedMessages,
        maxQueuedBytes: this.#maxQueuedBytes,
      });
      if (!message.created) throw new IdempotencyConflictError(candidate.id);
      const attemptNumber = (latest?.attempt_number ?? 0) + 1;
      this.#database.prepare(`
        INSERT INTO server_job_runs (
          job_key, scheduled_for, attempt_number, outcome, attempted_at,
          retry_at, error_code, message_id
        ) VALUES (?, ?, ?, 'succeeded', ?, NULL, NULL, ?)
      `).run(jobKey, scheduledFor, attemptNumber, now, message.message.id);
      const advanced = this.#database.prepare(`
        UPDATE server_jobs
        SET next_due_at = ?
        WHERE job_key = ? AND next_due_at = ?
      `).run(
        nextServerJobDueAt(job, now),
        jobKey,
        scheduledFor,
      );
      if (advanced.changes !== 1) {
        throw new Error(`server job ${jobKey} changed while its run was completing`);
      }
      return {
        job: selectServerJob(this.#database, jobKey),
        run: selectServerJobRun(this.#database, jobKey, scheduledFor, attemptNumber),
        message: message.message,
      };
    });
  }

  failServerJobRun({ jobKey, scheduledFor, errorCode }) {
    assertServerJobKey(jobKey);
    assertTimestamp(scheduledFor, "scheduledFor");
    assertFailureReason(errorCode);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const now = clockTimestamp(this.#clock);
      const job = selectServerJob(this.#database, jobKey);
      if (!serverJobRunIsDue(this.#database, job, scheduledFor, now)) return undefined;
      const latest = selectLatestServerJobRun(this.#database, jobKey, scheduledFor);
      const attemptNumber = (latest?.attempt_number ?? 0) + 1;
      const terminal = attemptNumber >= DEFAULT_SERVER_JOB_MAX_ATTEMPTS;
      const retryAt = terminal ? null : timestampAfter(now, serverJobRetryDelayMs(attemptNumber));
      this.#database.prepare(`
        INSERT INTO server_job_runs (
          job_key, scheduled_for, attempt_number, outcome, attempted_at,
          retry_at, error_code, message_id
        ) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
      `).run(
        jobKey,
        scheduledFor,
        attemptNumber,
        terminal ? "failed_terminal" : "failed",
        now,
        retryAt,
        errorCode,
      );
      if (terminal) {
        const advanced = this.#database.prepare(`
          UPDATE server_jobs
          SET next_due_at = ?
          WHERE job_key = ? AND next_due_at = ?
        `).run(
          nextServerJobDueAt(job, now),
          jobKey,
          scheduledFor,
        );
        if (advanced.changes !== 1) {
          throw new Error(`server job ${jobKey} changed while its terminal failure was recording`);
        }
      }
      return {
        job: selectServerJob(this.#database, jobKey),
        run: selectServerJobRun(this.#database, jobKey, scheduledFor, attemptNumber),
      };
    });
  }

  acquireAgentLease({ agentId, instanceId, leaseId, leaseDurationMs = DEFAULT_LEASE_DURATION_MS }) {
    validateLeaseIdentity({
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
    });
    assertLeaseDuration(leaseDurationMs);
    const now = clockTimestamp(this.#clock);
    const expiresAt = timestampAfter(now, leaseDurationMs);

    return immediateTransaction(this.#database, this.#databasePath, () => {
      const existing = selectLease(this.#database, agentId);
      if (existing !== undefined && !leaseExpired(existing, now) && existing.instance_id !== instanceId) {
        throw new DuplicateAgentLeaseError(agentId, existing.instance_id);
      }

      const recovered = existing !== undefined && leaseExpired(existing, now);
      if (existing !== undefined) {
        deleteLease(this.#database, agentId);
      }
      this.#database.prepare(`
        INSERT INTO agents (agent_id, first_seen_at, last_connected_at, disconnected_at)
        VALUES (?, ?, ?, NULL)
        ON CONFLICT (agent_id) DO UPDATE SET
          last_connected_at = excluded.last_connected_at,
          disconnected_at = NULL
      `).run(agentId, now, now);
      this.#database.prepare(`
        INSERT INTO agent_leases (lease_id, agent_id, instance_id, acquired_at, expires_at)
        VALUES (?, ?, ?, ?, ?)
      `).run(leaseId, agentId, instanceId, now, expiresAt);

      return {
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: leaseId,
        acquired_at: now,
        expires_at: expiresAt,
        recovered,
      };
    });
  }

  renewAgentLease({ agentId, instanceId, leaseId, leaseDurationMs = DEFAULT_LEASE_DURATION_MS }) {
    validateLeaseIdentity({
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
    });
    assertLeaseDuration(leaseDurationMs);
    const now = clockTimestamp(this.#clock);
    const expiresAt = timestampAfter(now, leaseDurationMs);

    return immediateTransaction(this.#database, this.#databasePath, () => {
      const existing = selectLease(this.#database, agentId);
      if (existing === undefined
        || existing.lease_id !== leaseId
        || existing.instance_id !== instanceId) {
        throw new LeaseNotActiveError(agentId);
      }
      if (leaseExpired(existing, now)) {
        deleteLease(this.#database, agentId);
        markDisconnected(this.#database, agentId, now);
        throw new LeaseNotActiveError(agentId);
      }

      this.#database.prepare(`
        UPDATE agent_leases
        SET expires_at = ?
        WHERE agent_id = ?
      `).run(expiresAt, agentId);
      return {
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: leaseId,
        acquired_at: existing.acquired_at,
        expires_at: expiresAt,
        recovered: false,
      };
    });
  }

  releaseAgentLease({ agentId, instanceId, leaseId }) {
    validateLeaseIdentity({
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
    });
    const now = clockTimestamp(this.#clock);

    return immediateTransaction(this.#database, this.#databasePath, () => {
      const result = this.#database.prepare(`
        DELETE FROM agent_leases
        WHERE agent_id = ? AND instance_id = ? AND lease_id = ?
      `).run(agentId, instanceId, leaseId);
      if (result.changes === 1) {
        markDisconnected(this.#database, agentId, now);
        return true;
      }
      return false;
    });
  }

  goalControlDesiredFor({ agentId, instanceId }) {
    assertGoalControlDesiredIdentity(agentId, instanceId);
    const now = clockTimestamp(this.#clock);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const row = this.#database.prepare(`
        SELECT agent_id, instance_id, revision, config_json, updated_at, expires_at
        FROM goal_control_desired_state
        WHERE agent_id = ? AND instance_id = ?
      `).get(agentId, instanceId);
      if (row === undefined) return undefined;
      if (row.expires_at <= now) {
        this.#database.prepare(`
          DELETE FROM goal_control_desired_state
          WHERE agent_id = ? AND instance_id = ? AND expires_at <= ?
        `).run(agentId, instanceId, now);
        return undefined;
      }
      return goalControlDesiredFromRow(row);
    });
  }

  upsertGoalControlDesired({ agentId, instanceId, config, expiresAt }) {
    assertGoalControlDesiredIdentity(agentId, instanceId);
    const configJson = serializeGoalControlDesiredConfig(config);
    assertGoalControlDesiredTimestamp(expiresAt, "expiresAt");
    const now = clockTimestamp(this.#clock);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const existing = this.#database.prepare(`
        SELECT revision FROM goal_control_desired_state
        WHERE agent_id = ? AND instance_id = ?
      `).get(agentId, instanceId);
      const revision = (existing?.revision ?? 0) + 1;
      this.#database.prepare(`
        INSERT INTO goal_control_desired_state (
          agent_id, instance_id, revision, config_json, updated_at, expires_at
        ) VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT (agent_id, instance_id) DO UPDATE SET
          revision = excluded.revision,
          config_json = excluded.config_json,
          updated_at = excluded.updated_at,
          expires_at = excluded.expires_at
      `).run(agentId, instanceId, revision, configJson, now, expiresAt);
      return goalControlDesiredFromRow(this.#database.prepare(`
        SELECT agent_id, instance_id, revision, config_json, updated_at, expires_at
        FROM goal_control_desired_state
        WHERE agent_id = ? AND instance_id = ?
      `).get(agentId, instanceId));
    });
  }

  deleteGoalControlDesired({ agentId, instanceId }) {
    assertGoalControlDesiredIdentity(agentId, instanceId);
    return immediateTransaction(this.#database, this.#databasePath, () => (
      this.#database.prepare(`
        DELETE FROM goal_control_desired_state
        WHERE agent_id = ? AND instance_id = ?
      `).run(agentId, instanceId).changes
    ));
  }

  deleteGoalControlDesiredExcept({ agentId, instanceId }) {
    assertGoalControlDesiredIdentity(agentId, instanceId);
    return immediateTransaction(this.#database, this.#databasePath, () => (
      this.#database.prepare(`
        DELETE FROM goal_control_desired_state
        WHERE agent_id = ? AND instance_id != ?
      `).run(agentId, instanceId).changes
    ));
  }

  pruneGoalControlDesired() {
    const now = clockTimestamp(this.#clock);
    return immediateTransaction(this.#database, this.#databasePath, () => (
      this.#database.prepare(`DELETE FROM goal_control_desired_state WHERE expires_at <= ?`).run(now).changes
    ));
  }

  recoverStaleLeases() {
    const now = clockTimestamp(this.#clock);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const stale = this.#database.prepare(`
        SELECT agent_id
        FROM agent_leases
        WHERE expires_at <= ?
        ORDER BY agent_id ASC
      `).all(now);
      for (const { agent_id: agentId } of stale) {
        deleteLease(this.#database, agentId);
        markDisconnected(this.#database, agentId, now);
      }
      return stale.map(({ agent_id: agentId }) => agentId);
    });
  }

  recoverStaleAgentResources() {
    const now = clockTimestamp(this.#clock);
    return immediateTransaction(this.#database, this.#databasePath, () => (
      cleanupStaleAgentResources(this.#database, now)
    ));
  }

  claimNextMessage({
    agentId,
    instanceId,
    leaseId,
    deliveryLeaseDurationMs = DEFAULT_DELIVERY_LEASE_DURATION_MS,
  }) {
    validateLeaseIdentity({
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
    });
    assertLeaseDuration(deliveryLeaseDurationMs, "deliveryLeaseDurationMs");
    const now = clockTimestamp(this.#clock);
    const deliveryExpiresAt = timestampAfter(now, deliveryLeaseDurationMs);

    return immediateTransaction(this.#database, this.#databasePath, () => {
      requireActiveAgentLease(this.#database, { agentId, instanceId, leaseId }, now);
      const activeDelivery = selectActiveDeliveryBatch(this.#database, agentId, leaseId);
      if (activeDelivery !== undefined) return activeDelivery;

      const rows = selectQueuedDeliveryRows(this.#database, agentId, now, {
        maxBatchBodyChars: this.#maxBatchBodyChars,
        maxBodyBytes: this.#maxBodyBytes,
      });
      if (rows.length === 0) return undefined;
      const batchId = this.#nextId();
      const items = [];
      for (const row of rows) {
        if (!MESSAGE_INCARNATION_ID.test(row.message_incarnation_id ?? "")) {
          const incarnationId = this.#nextId();
          this.#database.prepare(`
            UPDATE messages
            SET message_incarnation_id = ?
            WHERE id = ? AND state = 'queued'
          `).run(incarnationId, row.id);
          row.message_incarnation_id = incarnationId;
        }
        const attemptNumber = nextAttemptNumber(this.#database, row.id);
        this.#database.prepare(`
          UPDATE messages
          SET state = 'leased', claimed_at = COALESCE(claimed_at, ?), retry_batch_id = NULL
          WHERE id = ? AND state = 'queued'
        `).run(now, row.id);
        this.#database.prepare(`
          INSERT INTO delivery_attempts (
            message_id,
            attempt_number,
            lease_id,
            owner_lease_id,
            state,
            created_at,
            updated_at,
            delivery_expires_at,
            delivery_batch_id
          ) VALUES (?, ?, ?, ?, 'leased', ?, ?, ?, ?)
        `).run(
          row.id,
          attemptNumber,
          leaseId,
          leaseId,
          now,
          now,
          deliveryExpiresAt,
          batchId,
        );
        items.push(deliveryItem(row, attemptNumber, deliveryExpiresAt));
      }

      return deliveryFromRows(this.#database, rows, items, deliveryExpiresAt);

    });
  }

  recordInjectionRequested({
    agentId,
    instanceId,
    leaseId,
    messageId,
    attemptNumber,
    deliveryItems,
    allowBatchWithoutDeliveryItems = false,
    maxDeliveryAttempts = DEFAULT_MAX_DELIVERY_ATTEMPTS,
  }) {
    validateDeliveryReceipt({ agentId, instanceId, leaseId, messageId, attemptNumber });
    assertMaxDeliveryAttempts(maxDeliveryAttempts);
    const now = clockTimestamp(this.#clock);

    const result = immediateTransaction(this.#database, this.#databasePath, () => {
      requireActiveAgentLease(this.#database, { agentId, instanceId, leaseId }, now);
      const deliveries = requireActiveDeliveryBatch(
        this.#database,
        { agentId, leaseId, messageId, attemptNumber, deliveryItems, allowBatchWithoutDeliveryItems },
      );
      if (expireDeliveryBatchAtDeadline(this.#database, deliveries, now, maxDeliveryAttempts)) {
        return { timed_out: true };
      }
      if (deliveries.every((delivery) => delivery.attempt_state === "injection_requested")) {
        return { message: selectMessage(this.#database, messageId), attempt_number: attemptNumber, items: deliveryItemsForDeliveries(deliveries) };
      }
      if (!deliveries.every((delivery) => delivery.attempt_state === "leased" && delivery.message_state === "leased")) {
        throw new DeliveryNotActiveError(messageId, attemptNumber);
      }

      for (const delivery of deliveries) {
        this.#database.prepare(`
          UPDATE delivery_attempts
          SET state = 'injection_requested', updated_at = ?
          WHERE message_id = ? AND attempt_number = ? AND state = 'leased'
        `).run(now, delivery.message_id, delivery.attempt_number);
        this.#database.prepare(`
          UPDATE messages
          SET state = 'injection_requested'
          WHERE id = ? AND state = 'leased'
        `).run(delivery.message_id);
      }
      return { message: selectMessage(this.#database, messageId), attempt_number: attemptNumber, items: deliveryItemsForDeliveries(deliveries) };
    });
    if (result.timed_out) throw new DeliveryNotActiveError(messageId, attemptNumber);
    return result;
  }

  acknowledgeDelivery({
    agentId,
    instanceId,
    leaseId,
    messageId,
    attemptNumber,
    deliveryItems,
    allowBatchWithoutDeliveryItems = false,
    maxDeliveryAttempts = DEFAULT_MAX_DELIVERY_ATTEMPTS,
  }) {
    validateDeliveryReceipt({ agentId, instanceId, leaseId, messageId, attemptNumber });
    assertMaxDeliveryAttempts(maxDeliveryAttempts);
    const now = clockTimestamp(this.#clock);

    const result = immediateTransaction(this.#database, this.#databasePath, () => {
      requireActiveAgentLease(this.#database, { agentId, instanceId, leaseId }, now);
      const deliveries = requireActiveDeliveryBatch(
        this.#database,
        { agentId, leaseId, messageId, attemptNumber, deliveryItems, allowBatchWithoutDeliveryItems },
      );
      if (expireDeliveryBatchAtDeadline(this.#database, deliveries, now, maxDeliveryAttempts)) {
        return { timed_out: true };
      }
      if (!deliveries.every((delivery) => delivery.attempt_state === "injection_requested"
        && delivery.message_state === "injection_requested")) {
        throw new DeliveryNotActiveError(messageId, attemptNumber);
      }
      for (const delivery of deliveries) {
        this.#database.prepare(`
          UPDATE messages
          SET state = 'acknowledged'
          WHERE id = ?
        `).run(delivery.message_id);
        completeLinkedReminder(this.#database, delivery.message_id, this.#reminderIncarnations);
        this.#database.prepare("DELETE FROM messages WHERE id = ?").run(delivery.message_id);
      }
      return {
        message_id: messageId,
        message_incarnation_id: deliveries[0].message_incarnation_id,
      };
    });
    if (result.timed_out) throw new DeliveryNotActiveError(messageId, attemptNumber);
    return result;
  }

  nackDelivery({
    agentId,
    instanceId,
    leaseId,
    messageId,
    attemptNumber,
    deliveryItems,
    allowBatchWithoutDeliveryItems = false,
    reason,
    maxDeliveryAttempts = DEFAULT_MAX_DELIVERY_ATTEMPTS,
  }) {
    validateDeliveryReceipt({ agentId, instanceId, leaseId, messageId, attemptNumber });
    assertFailureReason(reason);
    assertMaxDeliveryAttempts(maxDeliveryAttempts);
    const now = clockTimestamp(this.#clock);

    const result = immediateTransaction(this.#database, this.#databasePath, () => {
      requireActiveAgentLease(this.#database, { agentId, instanceId, leaseId }, now);
      const deliveries = requireActiveDeliveryBatch(
        this.#database,
        { agentId, leaseId, messageId, attemptNumber, deliveryItems, allowBatchWithoutDeliveryItems },
      );
      if (expireDeliveryBatchAtDeadline(this.#database, deliveries, now, maxDeliveryAttempts)) {
        return { timed_out: true };
      }
      if (!deliveries.every((delivery) => ["leased", "injection_requested"].includes(delivery.attempt_state)
        && ["leased", "injection_requested"].includes(delivery.message_state))) {
        throw new DeliveryNotActiveError(messageId, attemptNumber);
      }
      const outcomes = deliveries.map((delivery) => failDeliveryAttempt(
        this.#database,
        delivery,
        { attemptState: "nacked", reason, now, maxDeliveryAttempts, retryBatchId: delivery.delivery_batch_id },
      ));
      return combineFailureOutcomes(messageId, outcomes);
    });
    if (result.timed_out) throw new DeliveryNotActiveError(messageId, attemptNumber);
    return result;
  }

  recoverTimedOutDeliveries({ maxDeliveryAttempts = DEFAULT_MAX_DELIVERY_ATTEMPTS } = {}) {
    assertMaxDeliveryAttempts(maxDeliveryAttempts);
    const now = clockTimestamp(this.#clock);

    return immediateTransaction(this.#database, this.#databasePath, () => {
      const timedOut = this.#database.prepare(`
        SELECT
          messages.id AS message_id,
          messages.recipient AS agent_id,
          messages.state AS message_state,
          delivery_attempts.attempt_number,
          delivery_attempts.owner_lease_id,
          delivery_attempts.delivery_batch_id,
          delivery_attempts.state AS attempt_state
        FROM delivery_attempts
        JOIN messages ON messages.id = delivery_attempts.message_id
        WHERE delivery_attempts.state IN ('leased', 'injection_requested')
          AND messages.state IN ('leased', 'injection_requested')
          AND delivery_attempts.delivery_expires_at <= ?
        ORDER BY messages.enqueue_sequence ASC
      `).all(now);
      return timedOut.map((delivery) => ({
        ...failDeliveryAttempt(
          this.#database,
          delivery,
          {
            attemptState: "timed_out",
            reason: "delivery_timeout",
            now,
            maxDeliveryAttempts,
            retryBatchId: delivery.delivery_batch_id,
          },
        ),
        agent_id: delivery.agent_id,
        attempt: delivery.attempt_number,
        lease_id: delivery.owner_lease_id,
      }));
    });
  }

  recoverExpiredUnclaimedMessages() {
    const now = clockTimestamp(this.#clock);
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const expired = this.#database.prepare(`
        SELECT id
        FROM messages
        WHERE state = 'queued'
          AND claimed_at IS NULL
          AND expires_at <= ?
        ORDER BY enqueue_sequence ASC
      `).all(now).map(({ id }) => id);
      for (const id of expired) {
        completeLinkedReminder(this.#database, id, this.#reminderIncarnations);
        this.#database.prepare(`
          DELETE FROM messages
          WHERE id = ?
            AND state = 'queued'
            AND claimed_at IS NULL
            AND expires_at <= ?
        `).run(id, now);
      }
      return expired;
    });
  }

  retryDeadLetter(messageId) {
    if (typeof messageId !== "string" || messageId.length === 0) {
      throw new TypeError("messageId is required");
    }
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const message = selectMessage(this.#database, messageId);
      if (message === undefined || message.state !== "dead_letter") return undefined;
      const usage = selectLiveQueueUsage(this.#database, message.to);
      if (usage.message_count >= this.#maxQueuedMessages
        || usage.queued_bytes + envelopeByteLength(message) > this.#maxQueuedBytes) {
        throw new QueueLimitError(message.to);
      }
      this.#database.prepare("DELETE FROM delivery_attempts WHERE message_id = ?").run(messageId);
      this.#database.prepare(`
        UPDATE messages
        SET state = 'queued'
        WHERE id = ? AND state = 'dead_letter'
      `).run(messageId);
      rearmFailedLinkedReminderForRetry(this.#database, messageId, clockTimestamp(this.#clock));
      return selectMessage(this.#database, messageId);
    });
  }

  dismissDeadLetter(messageId) {
    if (typeof messageId !== "string" || messageId.length === 0) {
      throw new TypeError("messageId is required");
    }
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const message = selectMessage(this.#database, messageId);
      if (message === undefined || message.state !== "dead_letter") return false;
      const failedReminderIds = this.#database.prepare(`
        SELECT id
        FROM reminders
        WHERE linked_message_id = ? AND state = 'failed'
      `).all(messageId).map(({ id }) => id);
      this.#database.prepare(`
        DELETE FROM reminders
        WHERE linked_message_id = ? AND state = 'failed'
      `).run(messageId);
      for (const reminderId of failedReminderIds) this.#reminderIncarnations.delete(reminderId);
      const result = this.#database.prepare(`
        DELETE FROM messages
        WHERE id = ? AND state = 'dead_letter'
      `).run(messageId);
      return result.changes === 1;
    });
  }

  agentFor(agentId) {
    const agent = this.#database.prepare(`
      SELECT agent_id, first_seen_at, last_connected_at, disconnected_at
      FROM agents
      WHERE agent_id = ?
    `).get(agentId);
    return agent === undefined ? undefined : { ...agent };
  }

  leaseFor(agentId) {
    const lease = selectLease(this.#database, agentId);
    return lease === undefined ? undefined : { ...lease };
  }

  messageFor(messageId) {
    return selectMessage(this.#database, messageId);
  }

  conversationHistory({ owner, peer }) {
    assertConversationOwner(owner);
    assertConversationPeer(peer);
    const [participantA, participantB] = conversationParticipants(owner, peer);
    return this.#database.prepare(`
      SELECT
        conversation_sequence,
        message_id,
        message_incarnation_id,
        sender,
        recipient,
        subject,
        body,
        created_at,
        recorded_at,
        attachments_json,
        source_kind
      FROM conversation_messages
      WHERE participant_a = ? AND participant_b = ?
      ORDER BY conversation_sequence ASC
    `).all(participantA, participantB).map(toConversationMessage);
  }

  conversationHighWaterSequence() {
    return this.#database.prepare(`
      SELECT COALESCE(
        (SELECT seq FROM sqlite_sequence WHERE name = 'conversation_messages'),
        0
      ) AS high_water_sequence
    `).get().high_water_sequence;
  }

  conversationAttachmentReferenced(attachmentId) {
    if (typeof attachmentId !== "string" || attachmentId.length === 0) return false;
    return this.#database.prepare(`
      SELECT attachments_json
      FROM conversation_messages
      WHERE attachments_json != '[]'
    `).all().some((row) => conversationAttachments(row.attachments_json)
      .some((attachment) => attachment.id === attachmentId));
  }

  pruneConversationHistory({
    retentionMs = DEFAULT_CONVERSATION_RETENTION_MS,
    historyLimit = DEFAULT_CONVERSATION_HISTORY_LIMIT,
  } = {}) {
    assertPositiveLimit(retentionMs, "retentionMs");
    assertPositiveLimit(historyLimit, "historyLimit");
    return immediateTransaction(this.#database, this.#databasePath, () => {
      const cutoff = timestampAfter(clockTimestamp(this.#clock), -retentionMs);
      const rows = this.#database.prepare(`
        SELECT conversation_sequence, participant_a, participant_b, attachments_json, recorded_at
        FROM conversation_messages
        ORDER BY participant_a ASC, participant_b ASC, conversation_sequence DESC
      `).all();
      const deleted = new Set();
      const counts = new Map();
      for (const row of rows) {
        const pair = JSON.stringify([row.participant_a, row.participant_b]);
        const count = counts.get(pair) ?? 0;
        if (row.recorded_at < cutoff || count >= historyLimit) deleted.add(row.conversation_sequence);
        else counts.set(pair, count + 1);
      }
      const candidateAttachmentIds = new Set();
      const remove = this.#database.prepare("DELETE FROM conversation_messages WHERE conversation_sequence = ?");
      for (const row of rows) {
        if (!deleted.has(row.conversation_sequence)) continue;
        for (const attachment of conversationAttachments(row.attachments_json)) {
          candidateAttachmentIds.add(attachment.id);
        }
        remove.run(row.conversation_sequence);
      }
      const unreferencedAttachmentIds = [...candidateAttachmentIds]
        .filter((attachmentId) => !this.conversationAttachmentReferenced(attachmentId))
        .sort();
      return {
        deleted_count: deleted.size,
        unreferenced_attachment_ids: unreferencedAttachmentIds,
      };
    });
  }

  queueUsageFor(recipient) {
    return selectLiveQueueUsage(this.#database, recipient);
  }

  admissionLimits() {
    return {
      maxBodyBytes: this.#maxBodyBytes,
      maxQueuedMessages: this.#maxQueuedMessages,
      maxQueuedBytes: this.#maxQueuedBytes,
      maxBatchBodyChars: this.#maxBatchBodyChars,
    };
  }

  deliveryAttemptsFor(messageId) {
    return this.#database.prepare(`
      SELECT
        attempt_number,
        owner_lease_id,
        state,
        created_at,
        updated_at,
        delivery_expires_at,
        failure_reason
      FROM delivery_attempts
      WHERE message_id = ?
      ORDER BY attempt_number ASC
    `).all(messageId).map((attempt) => ({ ...attempt }));
  }

  queuedFor(recipient) {
    return this.#database.prepare(`
      SELECT
        enqueue_sequence,
        id,
        sender AS "from",
        recipient AS "to",
        subject,
        body,
        created_at,
        expires_at,
        message_incarnation_id,
        state,
        sentinel_json,
        source_kind
      FROM messages
      WHERE recipient = ? AND state = 'queued'
      ORDER BY enqueue_sequence ASC
    `).all(recipient).map(toMessage);
  }

  serverJobFor(jobKey) {
    assertServerJobKey(jobKey);
    return selectServerJob(this.#database, jobKey);
  }

  serverJobRunsFor(jobKey) {
    assertServerJobKey(jobKey);
    return this.#database.prepare(`
      SELECT
        job_key,
        scheduled_for,
        attempt_number,
        outcome,
        attempted_at,
        retry_at,
        error_code,
        message_id
      FROM server_job_runs
      WHERE job_key = ?
      ORDER BY scheduled_for ASC, attempt_number ASC
    `).all(jobKey).map((run) => ({ ...run }));
  }

  applySubagentLifecycleEvent(frame, { allowLeaseRebind = false } = {}) {
    return this.#applySubagentLifecycle(frame, { allowLeaseRebind, snapshot: false });
  }

  applySubagentLifecycleSnapshot(frame, { allowLeaseRebind = false } = {}) {
    return this.#applySubagentLifecycle(frame, { allowLeaseRebind, snapshot: true });
  }

  #applySubagentLifecycle(frame, { allowLeaseRebind, snapshot }) {
    const candidate = validateLifecycleCandidate(frame, { snapshot });
    return immediateTransaction(this.#database, this.#databasePath, () => applyLifecycleCandidate(
      this.#database,
      candidate,
      { allowLeaseRebind, snapshot, now: clockTimestamp(this.#clock) },
    ));
  }

  subagentLifecycleSnapshot({ parentAgentId } = {}) {
    if (parentAgentId !== undefined) assertLifecycleText(parentAgentId, "parentAgentId", 128);
    return readLifecycleSnapshot(this.#database, parentAgentId);
  }

  subagentLifecycleProducerFor(parentAgentId) {
    assertLifecycleText(parentAgentId, "parentAgentId", 128);
    const row = selectLifecycleProducer(this.#database, parentAgentId);
    return row === undefined ? undefined : {
      parent_agent_id: row.parent_agent_id,
      instance_id: row.instance_id,
      parent_session_id: row.parent_session_id,
      producer_epoch: row.producer_epoch,
      lease_id: row.lease_id,
      authority: row.authority,
      highest_client_sequence: row.highest_client_sequence,
      last_event_id: row.last_event_id,
      last_snapshot_sequence: row.last_snapshot_sequence,
    };
  }

  replaySubagentLifecycle({ afterVersion = 0 } = {}) {
    if (!Number.isSafeInteger(afterVersion) || afterVersion < 0) throw new TypeError("afterVersion must be a non-negative safe integer");
    const latest = lifecycleVersion(this.#database);
    const oldest = this.#database.prepare("SELECT MIN(router_version) AS version FROM subagent_lifecycle_events").get().version;
    if ((oldest !== null && afterVersion < oldest - 1) || (oldest === null && afterVersion < latest)) {
      return { mode: "snapshot", ...readLifecycleSnapshot(this.#database) };
    }
    const updates = this.#database.prepare(`
      SELECT router_version, parent_agent_id, subagent_id, state, type, description, status, terminal_reason
      FROM subagent_lifecycle_events
      WHERE router_version > ?
      ORDER BY router_version ASC
    `).all(afterVersion).map((row) => ({
      router_version: row.router_version,
      parent_agent_id: row.parent_agent_id,
      subagent_id: row.subagent_id,
      state: row.state,
      ...(row.type === null ? {} : { type: row.type }),
      ...(row.description === null ? {} : { description: row.description }),
      ...(row.status === null ? {} : { status: row.status }),
      ...(row.terminal_reason === null ? {} : { terminal_reason: row.terminal_reason }),
    }));
    return { mode: "replay", router_version: latest, updates };
  }

  pruneSubagentLifecycle() {
    const now = clockTimestamp(this.#clock);
    const terminalCutoff = new Date(new Date(now).getTime() - 24 * 60 * 60 * 1000).toISOString();
    const ledgerCutoff = new Date(new Date(now).getTime() - 7 * 24 * 60 * 60 * 1000).toISOString();
    const producerCutoff = ledgerCutoff;
    return immediateTransaction(this.#database, this.#databasePath, () => {
      // A live lease owns a running child. Once its producer has been absent for
      // seven days, turn every remaining child into a short-lived tombstone so a
      // disconnected process cannot retain a visible child forever. This uses the
      // normal transition writer: a UI reader must receive the same monotonic
      // version it would receive for a producer-sent terminal event.
      const expiredLifecycle = expireRunningLifecycleChildren(
        this.#database,
        { now, producerCutoff },
      );
      const expiredRunning = expiredLifecycle.count;
      const tombstones = this.#database.prepare(`DELETE FROM subagent_lifecycle_state WHERE state = 'terminal' AND terminal_at <= ?`).run(terminalCutoff).changes;
      const threshold = this.#database.prepare(`
        SELECT router_version FROM subagent_lifecycle_events
        ORDER BY router_version DESC LIMIT 1 OFFSET 99999
      `).get()?.router_version;
      const ledger = this.#database.prepare(`
        DELETE FROM subagent_lifecycle_events
        WHERE received_at <= ? OR (? IS NOT NULL AND router_version < ?)
      `).run(ledgerCutoff, threshold ?? null, threshold ?? null).changes;
      const producers = this.#database.prepare(`
        DELETE FROM subagent_lifecycle_producers
        WHERE last_seen_at <= ?
          AND NOT EXISTS (
            SELECT 1 FROM agent_leases lease
            WHERE lease.agent_id = subagent_lifecycle_producers.parent_agent_id
              AND lease.expires_at > ?
          )
          AND NOT EXISTS (SELECT 1 FROM subagent_lifecycle_state s WHERE s.parent_agent_id = subagent_lifecycle_producers.parent_agent_id)
          AND NOT EXISTS (SELECT 1 FROM subagent_lifecycle_events e WHERE e.parent_agent_id = subagent_lifecycle_producers.parent_agent_id)
      `).run(producerCutoff, now).changes;
      return { expiredRunning, tombstones, ledger, producers, updates: expiredLifecycle.updates };
    });
  }

  migrationIds() {
    return this.#database.prepare(`
      SELECT id
      FROM schema_migrations
      ORDER BY id ASC
    `).all().map((row) => row.id);
  }

  settings() {
    const journalMode = this.#database.prepare("PRAGMA journal_mode").get().journal_mode;
    const synchronous = this.#database.prepare("PRAGMA synchronous").get().synchronous;
    const foreignKeys = this.#database.prepare("PRAGMA foreign_keys").get().foreign_keys;
    const busyTimeoutMs = this.#database.prepare("PRAGMA busy_timeout").get().timeout;
    return {
      journalMode,
      synchronous: synchronousName(synchronous),
      foreignKeys: foreignKeys === 1,
      busyTimeoutMs,
    };
  }
}

const LIFECYCLE_EPOCH = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const LIFECYCLE_REASON = /^[a-z][a-z0-9_]{0,63}$/;

function validateLifecycleCandidate(frame, { snapshot }) {
  if (frame === null || typeof frame !== "object" || Array.isArray(frame)) throw new TypeError("lifecycle frame must be an object");
  const parentAgentId = frame.agent_id;
  assertLifecycleText(parentAgentId, "agent_id", 128);
  assertLifecycleText(frame.instance_id, "instance_id", 128);
  assertLifecycleText(frame.parent_session_id, "parent_session_id", 128);
  assertLifecycleText(frame.lease_id, "lease_id", 128);
  assertLifecycleText(frame.producer_epoch, "producer_epoch", 36);
  if (!LIFECYCLE_EPOCH.test(frame.producer_epoch)) throw new TypeError("producer_epoch must be a UUID");
  assertLifecycleText(frame.event_id, "event_id", 128);
  if (!Number.isSafeInteger(frame.client_sequence) || frame.client_sequence < 1) throw new TypeError("client_sequence must be a positive safe integer");
  if (snapshot) {
    if (!Array.isArray(frame.subagents) || frame.subagents.length > 999) throw new TypeError("snapshot subagents must contain at most 999 items");
    const children = frame.subagents.map((child) => lifecycleChild(child));
    const ids = new Set(children.map((child) => child.id));
    if (ids.size !== children.length) throw new TypeError("snapshot subagent IDs must be unique");
    return lifecycleCandidate(frame, { subagents: children, eventKind: "snapshot" });
  }
  if (!['started', 'terminal'].includes(frame.lifecycle_event)) throw new TypeError("lifecycle_event must be started or terminal");
  const child = lifecycleChild(frame.subagent);
  if (frame.lifecycle_event === "terminal") {
    if (typeof frame.terminal_reason !== "string" || !LIFECYCLE_REASON.test(frame.terminal_reason)) throw new TypeError("terminal_reason is invalid");
  }
  return lifecycleCandidate(frame, {
    subagent: child,
    eventKind: frame.lifecycle_event,
    terminalReason: frame.lifecycle_event === "terminal" ? frame.terminal_reason : undefined,
  });
}

function lifecycleCandidate(frame, extra) {
  const candidate = {
    parentAgentId: frame.agent_id,
    instanceId: frame.instance_id,
    parentSessionId: frame.parent_session_id,
    leaseId: frame.lease_id,
    epoch: frame.producer_epoch,
    sequence: frame.client_sequence,
    eventId: frame.event_id,
    ...extra,
  };
  // A replacement socket must present a new lease, but a retry keeps its
  // event identity and lifecycle payload. Lease ownership is fenced by the
  // router before this repository method, so it is not part of idempotency.
  const { leaseId: _leaseId, ...idempotencyCandidate } = candidate;
  candidate.payloadHash = createHash("sha256").update(JSON.stringify(idempotencyCandidate)).digest("hex");
  return candidate;
}

function lifecycleChild(child) {
  if (child === null || typeof child !== "object" || Array.isArray(child)) throw new TypeError("subagent must be an object");
  assertLifecycleText(child.id, "subagent.id", 128);
  const type = child.type === undefined ? undefined : lifecycleOptionalText(child.type, "subagent.type", 64);
  const description = child.description === undefined ? undefined : lifecycleOptionalText(child.description, "subagent.description", 512);
  const status = child.status === undefined ? undefined : lifecycleSubagentStatus(child.status);
  return {
    id: child.id.trim(),
    ...(type === undefined ? {} : { type }),
    ...(description === undefined ? {} : { description }),
    ...(status === undefined ? {} : { status }),
  };
}

function lifecycleSubagentStatus(value) {
  if (!["queued", "running", "completed", "steered", "aborted", "stopped", "error"].includes(value)) {
    throw new TypeError("subagent.status is invalid");
  }
  return value;
}

function lifecycleOptionalText(value, name, limit) {
  assertLifecycleText(value, name, limit);
  return value.trim();
}

function assertLifecycleText(value, name, maxBytes) {
  if (typeof value !== "string" || value.trim() === "" || Buffer.byteLength(value, "utf8") > maxBytes) throw new TypeError(`${name} is invalid`);
}

function applyLifecycleCandidate(database, candidate, { allowLeaseRebind, snapshot, now }) {
  const producer = selectLifecycleProducer(database, candidate.parentAgentId);
  const identity = reconcileLifecycleProducer(producer, candidate, { allowLeaseRebind, snapshot });
  if (identity.disposition !== undefined) return lifecycleNoMutation(database, candidate.parentAgentId, identity.disposition, now);

  const priorEvent = database.prepare(`
    SELECT router_version, payload_hash FROM subagent_lifecycle_events
    WHERE parent_agent_id = ? AND producer_epoch = ? AND event_id = ?
  `).get(candidate.parentAgentId, candidate.epoch, candidate.eventId);
  if (priorEvent !== undefined) {
    return lifecycleNoMutation(database, candidate.parentAgentId,
      priorEvent.payload_hash === candidate.payloadHash ? "duplicate" : "conflict", now, priorEvent.router_version);
  }
  if (producer !== undefined && !identity.replacement) {
    if (candidate.sequence < producer.highest_client_sequence) return lifecycleNoMutation(database, candidate.parentAgentId, "stale", now);
    if (candidate.sequence === producer.highest_client_sequence) {
      return lifecycleNoMutation(database, candidate.parentAgentId,
        producer.last_event_id === candidate.eventId && producer.last_event_hash === candidate.payloadHash ? "duplicate" : "conflict", now);
    }
  }

  const transitions = [];
  if (identity.replacement) {
    const oldRunning = database.prepare(`
      SELECT * FROM subagent_lifecycle_state WHERE parent_agent_id = ? AND state = 'running' ORDER BY subagent_id ASC
    `).all(candidate.parentAgentId);
    for (const row of oldRunning) transitions.push({ child: { id: row.subagent_id }, state: "terminal", reason: "producer_replaced", kind: "snapshot" });
  }
  if (snapshot) {
    const supplied = new Map(candidate.subagents.map((child) => [child.id, child]));
    const existing = database.prepare(`SELECT * FROM subagent_lifecycle_state WHERE parent_agent_id = ? ORDER BY subagent_id ASC`).all(candidate.parentAgentId);
    const byId = new Map(existing.map((row) => [row.subagent_id, row]));
    for (const child of candidate.subagents) {
      const prior = byId.get(child.id);
      if (prior?.state === "terminal" && prior.producer_epoch === candidate.epoch) continue;
      if (prior?.state === "running" && prior.producer_epoch === candidate.epoch
        && prior.type === (child.type ?? null) && prior.description === (child.description ?? null)
        && prior.status === (child.status ?? null)) continue;
      transitions.push({ child, state: "running", kind: "snapshot" });
    }
    for (const row of existing) {
      if (row.state === "running" && row.producer_epoch === candidate.epoch && !supplied.has(row.subagent_id)) {
        transitions.push({ child: { id: row.subagent_id }, state: "terminal", reason: "snapshot_omitted", kind: "snapshot" });
      }
    }
  } else {
    const prior = database.prepare(`SELECT * FROM subagent_lifecycle_state WHERE parent_agent_id = ? AND subagent_id = ?`).get(candidate.parentAgentId, candidate.subagent.id);
    if (!(prior?.state === "terminal" && prior.producer_epoch === candidate.epoch)
      && !(prior?.state === "running" && candidate.eventKind === "started"
        && prior.producer_epoch === candidate.epoch && prior.type === (candidate.subagent.type ?? null)
        && prior.description === (candidate.subagent.description ?? null)
        && prior.status === (candidate.subagent.status ?? null))) {
      transitions.push({ child: candidate.subagent, state: candidate.eventKind === "started" ? "running" : "terminal", reason: candidate.terminalReason, kind: candidate.eventKind });
    }
  }

  const nextProducer = { ...candidate, authority: "v2" };
  let lastVersion = lifecycleVersion(database);
  for (let index = 0; index < transitions.length; index += 1) {
    lastVersion = writeLifecycleTransition(database, nextProducer, transitions[index], now,
      index === 0 ? candidate.eventId : derivedLifecycleEventId(candidate.eventId, index), index);
  }
  upsertLifecycleProducer(database, nextProducer, now, {
    lastSnapshotSequence: snapshot ? candidate.sequence : producer?.last_snapshot_sequence ?? 0,
  });
  if (transitions.length === 0) return lifecycleNoMutation(database, candidate.parentAgentId, "accepted_noop", now, lastVersion);
  return {
    disposition: "applied",
    router_version: lastVersion,
    snapshot: readLifecycleSnapshot(database, candidate.parentAgentId),
  };
}

function expireRunningLifecycleChildren(database, { now, producerCutoff }) {
  const rows = database.prepare(`
    SELECT
      state.parent_agent_id,
      state.subagent_id,
      producer.instance_id,
      producer.parent_session_id,
      producer.producer_epoch
    FROM subagent_lifecycle_state state
    JOIN subagent_lifecycle_producers producer ON producer.parent_agent_id = state.parent_agent_id
    WHERE state.state = 'running'
      AND producer.last_seen_at <= ?
      AND NOT EXISTS (
        SELECT 1
        FROM agent_leases lease
        WHERE lease.agent_id = producer.parent_agent_id
          AND lease.expires_at > ?
      )
    ORDER BY state.parent_agent_id ASC, state.subagent_id ASC
  `).all(producerCutoff, now);
  const updates = new Map();
  for (const row of rows) {
    const sequence = nextRetentionLifecycleSequence(database, row.parent_agent_id, row.producer_epoch);
    const eventId = createHash("sha256")
      .update(`producer_expired:${row.parent_agent_id}:${row.subagent_id}:${now}`)
      .digest("hex");
    const version = writeLifecycleTransition(database, {
      parentAgentId: row.parent_agent_id,
      instanceId: row.instance_id,
      parentSessionId: row.parent_session_id,
      epoch: row.producer_epoch,
      sequence,
      payloadHash: createHash("sha256").update("producer_expired").digest("hex"),
    }, {
      child: { id: row.subagent_id },
      state: "terminal",
      reason: "producer_expired",
      kind: "terminal",
    }, now, eventId);
    updates.set(row.parent_agent_id, version);
  }
  return {
    count: rows.length,
    updates: [...updates].map(([parentAgentId, routerVersion]) => ({ parentAgentId, routerVersion })),
  };
}

function nextRetentionLifecycleSequence(database, parentAgentId, epoch) {
  // Normal client transitions are stored as sequence * 1000 plus their snapshot
  // index. Reserve the next sequence block for this router-owned terminal event.
  const latest = database.prepare(`
    SELECT MAX(client_sequence) AS sequence
    FROM subagent_lifecycle_events
    WHERE parent_agent_id = ? AND producer_epoch = ?
  `).get(parentAgentId, epoch).sequence ?? 0;
  return Math.floor(latest / 1000) + 1;
}

function reconcileLifecycleProducer(producer, candidate, { allowLeaseRebind, snapshot }) {
  if (producer === undefined) return snapshot ? { replacement: false } : { disposition: "conflict" };
  const sameIdentity = producer.instance_id === candidate.instanceId && producer.parent_session_id === candidate.parentSessionId;
  if (producer.producer_epoch !== candidate.epoch) {
    if (sameIdentity) return { disposition: "conflict" };
    return snapshot ? { replacement: true } : { disposition: "conflict" };
  }
  if (!sameIdentity) return { disposition: "conflict" };
  if (producer.lease_id !== candidate.leaseId && !(snapshot && allowLeaseRebind)) return { disposition: "stale" };
  return { replacement: false };
}

function writeLifecycleTransition(database, producer, transition, now, eventId, transitionIndex = 0) {
  const state = transition.state;
  const metadata = state === "running" ? transition.child : {};
  const insert = database.prepare(`
    INSERT INTO subagent_lifecycle_events (
      parent_agent_id, instance_id, parent_session_id, producer_epoch, subagent_id,
      event_id, client_sequence, event_kind, state, type, description, status, terminal_reason, payload_hash, received_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    producer.parentAgentId, producer.instanceId, producer.parentSessionId, producer.epoch, transition.child.id,
    eventId, producer.sequence * 1000 + transitionIndex, transition.kind, state, metadata.type ?? null, metadata.description ?? null,
    metadata.status ?? null, transition.reason ?? null, producer.payloadHash, now,
  );
  const version = Number(insert.lastInsertRowid);
  database.prepare(`
    INSERT INTO subagent_lifecycle_state (
      parent_agent_id, subagent_id, instance_id, parent_session_id, producer_epoch,
      type, description, status, state, started_at, updated_at, terminal_at, terminal_reason, router_version
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (parent_agent_id, subagent_id) DO UPDATE SET
      instance_id = excluded.instance_id, parent_session_id = excluded.parent_session_id,
      producer_epoch = excluded.producer_epoch, type = excluded.type, description = excluded.description, status = excluded.status,
      state = excluded.state, started_at = CASE WHEN excluded.state = 'running' THEN excluded.started_at ELSE subagent_lifecycle_state.started_at END,
      updated_at = excluded.updated_at, terminal_at = excluded.terminal_at,
      terminal_reason = excluded.terminal_reason, router_version = excluded.router_version
  `).run(
    producer.parentAgentId, transition.child.id, producer.instanceId, producer.parentSessionId, producer.epoch,
    metadata.type ?? null, metadata.description ?? null, metadata.status ?? null, state, state === "running" ? now : null, now,
    state === "terminal" ? now : null, transition.reason ?? null, version,
  );
  return version;
}

function upsertLifecycleProducer(database, producer, now, { lastSnapshotSequence }) {
  database.prepare(`
    INSERT INTO subagent_lifecycle_producers (
      parent_agent_id, instance_id, parent_session_id, producer_epoch, lease_id,
      highest_client_sequence, last_event_id, last_event_hash, last_snapshot_sequence,
      authority, last_seen_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'v2', ?, ?, ?)
    ON CONFLICT (parent_agent_id) DO UPDATE SET
      instance_id = excluded.instance_id, parent_session_id = excluded.parent_session_id,
      producer_epoch = excluded.producer_epoch, lease_id = excluded.lease_id,
      highest_client_sequence = excluded.highest_client_sequence, last_event_id = excluded.last_event_id,
      last_event_hash = excluded.last_event_hash, last_snapshot_sequence = excluded.last_snapshot_sequence,
      authority = 'v2', last_seen_at = excluded.last_seen_at, updated_at = excluded.updated_at
  `).run(
    producer.parentAgentId, producer.instanceId, producer.parentSessionId, producer.epoch, producer.leaseId,
    producer.sequence, producer.eventId, producer.payloadHash, lastSnapshotSequence, now, now, now,
  );
}

function lifecycleNoMutation(database, parentAgentId, disposition, now, appliedVersion = undefined) {
  incrementLifecycleDiagnostic(database, parentAgentId, disposition, now);
  return {
    disposition,
    router_version: lifecycleVersion(database),
    ...(appliedVersion === undefined ? {} : { applied_version: appliedVersion }),
    snapshot: readLifecycleSnapshot(database, parentAgentId),
  };
}

function incrementLifecycleDiagnostic(database, parentAgentId, reason, now) {
  database.prepare(`
    INSERT INTO subagent_lifecycle_diagnostics (parent_agent_id, reason_code, count, last_router_version, updated_at)
    VALUES (?, ?, 1, ?, ?)
    ON CONFLICT (parent_agent_id, reason_code) DO UPDATE SET
      count = MIN(10000, subagent_lifecycle_diagnostics.count + 1),
      last_router_version = excluded.last_router_version, updated_at = excluded.updated_at
  `).run(parentAgentId, reason, lifecycleVersion(database), now);
}

function selectLifecycleProducer(database, parentAgentId) {
  return database.prepare(`SELECT * FROM subagent_lifecycle_producers WHERE parent_agent_id = ?`).get(parentAgentId);
}

function lifecycleVersion(database) {
  // AUTOINCREMENT keeps the committed lifecycle clock monotonic even after
  // retained ledger rows are pruned. MAX(router_version) would rewind to zero.
  return database.prepare("SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'subagent_lifecycle_events'), 0) AS version").get().version;
}

function readLifecycleSnapshot(database, parentAgentId = undefined) {
  const rows = database.prepare(`
    SELECT parent_agent_id, subagent_id, type, description, status, router_version
    FROM subagent_lifecycle_state
    WHERE state = 'running' ${parentAgentId === undefined ? "" : "AND parent_agent_id = ?"}
    ORDER BY parent_agent_id ASC, subagent_id ASC
  `).all(...(parentAgentId === undefined ? [] : [parentAgentId]));
  const agents = new Map();
  for (const row of rows) {
    const agent = agents.get(row.parent_agent_id) ?? { agent_id: row.parent_agent_id, subagent_version: row.router_version, subagent_count: 0, subagents: [] };
    agent.subagent_version = Math.max(agent.subagent_version, row.router_version);
    agent.subagents.push({
      id: row.subagent_id,
      ...(row.type === null ? {} : { type: row.type }),
      ...(row.description === null ? {} : { description: row.description }),
      ...(row.status === null ? {} : { status: row.status }),
    });
    agent.subagent_count = agent.subagents.length;
    agents.set(row.parent_agent_id, agent);
  }
  if (parentAgentId !== undefined && !agents.has(parentAgentId)) {
    const version = database.prepare(`SELECT MAX(router_version) AS version FROM subagent_lifecycle_state WHERE parent_agent_id = ?`).get(parentAgentId).version ?? lifecycleVersion(database);
    agents.set(parentAgentId, { agent_id: parentAgentId, subagent_version: version, subagent_count: 0, subagents: [] });
  }
  return { router_version: lifecycleVersion(database), agents: [...agents.values()] };
}

function derivedLifecycleEventId(eventId, index) {
  return createHash("sha256").update(`${eventId}:${index}`).digest("hex").slice(0, 128);
}

function configureDatabase(database, busyTimeoutMs) {
  const journalMode = database.prepare("PRAGMA journal_mode = WAL").get().journal_mode;
  database.exec("PRAGMA foreign_keys = ON");
  database.exec("PRAGMA synchronous = FULL");

  const settings = {
    journalMode,
    synchronous: database.prepare("PRAGMA synchronous").get().synchronous,
    foreignKeys: database.prepare("PRAGMA foreign_keys").get().foreign_keys,
    busyTimeoutMs: database.prepare("PRAGMA busy_timeout").get().timeout,
  };
  if (settings.journalMode !== "wal"
    || settings.synchronous !== 2
    || settings.foreignKeys !== 1
    || settings.busyTimeoutMs !== busyTimeoutMs) {
    throw new Error("SQLite connection did not accept required durability settings");
  }
}

function applyMigrations(database, migrationPlan, clock, databasePath) {
  for (const migration of migrationPlan) {
    immediateTransaction(database, databasePath, () => {
      database.exec(`
        CREATE TABLE IF NOT EXISTS schema_migrations (
          id TEXT PRIMARY KEY,
          applied_at TEXT NOT NULL
        )
      `);
      const applied = database.prepare(
        "SELECT 1 FROM schema_migrations WHERE id = ?",
      ).get(migration.id);
      if (applied !== undefined) return;

      database.exec(migration.sql);
      if (migration.id === "005_message_incarnation") backfillMessageIncarnations(database);
      database.prepare(
        "INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)",
      ).run(migration.id, clock().toISOString());
    });
  }
}

function backfillMessageIncarnations(database) {
  const rows = database.prepare(`
    SELECT id
    FROM messages
    WHERE message_incarnation_id IS NULL
    ORDER BY enqueue_sequence ASC
  `).all();
  const update = database.prepare(`
    UPDATE messages
    SET message_incarnation_id = ?
    WHERE id = ? AND message_incarnation_id IS NULL
  `);
  for (const { id } of rows) update.run(randomUUID(), id);
}

function immediateTransaction(database, databasePath, operation) {
  database.exec("BEGIN IMMEDIATE");
  let open = true;
  try {
    const result = operation();
    database.exec("COMMIT");
    open = false;
    restrictStateFiles(databasePath);
    return result;
  } catch (error) {
    if (open) {
      try {
        database.exec("ROLLBACK");
      } catch {
        // Preserve the operation error. A caller can retry its idempotency key.
      }
    }
    throw error;
  }
}

function restrictStateFiles(databasePath) {
  for (const path of [databasePath, `${databasePath}-wal`, `${databasePath}-shm`]) {
    if (existsSync(path)) chmodSync(path, 0o600);
  }
}

function selectMessage(database, id) {
  const row = database.prepare(`
    SELECT
      enqueue_sequence,
      id,
      sender AS "from",
      recipient AS "to",
      subject,
      body,
      created_at,
      expires_at,
      message_incarnation_id,
      state,
      sentinel_json,
      source_kind
    FROM messages
    WHERE id = ?
  `).get(id);
  return row === undefined ? undefined : toMessage(row);
}

function selectLiveQueueUsage(database, recipient) {
  const usage = database.prepare(`
    SELECT
      COUNT(*) AS message_count,
      COALESCE(SUM(
        length(CAST(id AS BLOB))
        + length(CAST(sender AS BLOB))
        + length(CAST(recipient AS BLOB))
        + length(CAST(COALESCE(subject, '') AS BLOB))
        + length(CAST(body AS BLOB))
        + length(CAST(created_at AS BLOB))
      ), 0) AS queued_bytes
    FROM messages
    WHERE recipient = ?
      AND state IN ('queued', 'leased', 'injection_requested')
  `).get(recipient);
  return {
    message_count: usage.message_count,
    queued_bytes: usage.queued_bytes,
  };
}

function envelopeByteLength({ id, from, to, subject, body, created_at: createdAt }) {
  return Buffer.byteLength(id, "utf8")
    + Buffer.byteLength(from, "utf8")
    + Buffer.byteLength(to, "utf8")
    + Buffer.byteLength(subject ?? "", "utf8")
    + Buffer.byteLength(body, "utf8")
    + Buffer.byteLength(createdAt, "utf8");
}

const BATCH_HEADER = "Mailbox message batch";
const BATCH_SEPARATOR = "\n\n---\n\n";

function batchItem(message) {
  return [
    `From: ${message.from}`,
    ...(message.subject === null ? [] : [`Subject: ${message.subject}`]),
    "",
    message.body,
    "",
    `[Reply via mailbox to ${message.from}.]`,
  ].join("\n");
}

function isBatchedBody(body) {
  return body.startsWith(`${BATCH_HEADER}\n\n`);
}

function batchItemCount(message) {
  if (!isBatchedBody(message.body)) return 1;
  return (message.body.match(new RegExp(BATCH_SEPARATOR, "g")) ?? []).length + 1;
}

function batchContent(message) {
  if (!isBatchedBody(message.body)) return batchItem(message);
  return message.body.slice(`${BATCH_HEADER}\n\n`.length);
}

function batchedBody(target, candidate) {
  const existing = isBatchedBody(target.body)
    ? target.body
    : `${BATCH_HEADER}\n\n${batchItem(target)}`;
  return `${existing}${BATCH_SEPARATOR}${batchContent(candidate)}`;
}


function selectActiveDeliveryBatch(database, recipient, leaseId) {
  const active = database.prepare(`
    SELECT
      messages.enqueue_sequence,
      messages.id,
      messages.sender AS "from",
      messages.recipient AS "to",
      messages.subject,
      messages.body,
      messages.created_at,
      messages.expires_at,
      messages.message_incarnation_id,
      messages.state,
      messages.sentinel_json,
      delivery_attempts.attempt_number,
      delivery_attempts.delivery_expires_at,
      delivery_attempts.delivery_batch_id,
      delivery_attempts.owner_lease_id
    FROM messages
    JOIN delivery_attempts ON delivery_attempts.message_id = messages.id
    WHERE messages.recipient = ?
      AND messages.state IN ('leased', 'injection_requested')
      AND delivery_attempts.state IN ('leased', 'injection_requested')
    ORDER BY messages.enqueue_sequence ASC
  `).all(recipient);
  if (active.length === 0) return undefined;
  const batchId = active[0].delivery_batch_id;
  const rows = batchId === null
    ? [active[0]]
    : active.filter((row) => row.delivery_batch_id === batchId);
  if (rows.some((row) => row.owner_lease_id !== leaseId)) {
    if (batchId === null) {
      database.prepare(`
        UPDATE delivery_attempts
        SET lease_id = ?, owner_lease_id = ?
        WHERE message_id = ?
          AND state IN ('leased', 'injection_requested')
      `).run(leaseId, leaseId, rows[0].id);
    } else {
      database.prepare(`
        UPDATE delivery_attempts
        SET lease_id = ?, owner_lease_id = ?
        WHERE delivery_batch_id = ?
          AND state IN ('leased', 'injection_requested')
      `).run(leaseId, leaseId, batchId);
    }
    for (const row of rows) row.owner_lease_id = leaseId;
  }
  const items = rows.map((row) => deliveryItem(row, row.attempt_number, row.delivery_expires_at));
  return deliveryFromRows(database, rows, items, items[0].delivery_expires_at);
}

function selectQueuedDeliveryRows(database, recipient, now, { maxBatchBodyChars, maxBodyBytes }) {
  const rows = database.prepare(`
    SELECT
      enqueue_sequence,
      id,
      sender AS "from",
      recipient AS "to",
      subject,
      body,
      created_at,
      expires_at,
      message_incarnation_id,
      state,
      sentinel_json,
      source_kind,
      retry_batch_id
    FROM messages
    WHERE recipient = ?
      AND state = 'queued'
      AND (claimed_at IS NOT NULL OR expires_at > ?)
    ORDER BY enqueue_sequence ASC
  `).all(recipient, now);
  if (rows.length === 0) return [];
  const first = rows[0];
  if (first.sentinel_json !== null) return [first];
  const selected = [];
  const retryBatchId = first.retry_batch_id;
  let body = first.body;
  for (const row of rows) {
    if (row.sentinel_json !== null) break;
    if (retryBatchId !== null && row.retry_batch_id !== retryBatchId) continue;
    if (retryBatchId === null && row.retry_batch_id !== null) break;
    if (selected.length === 0) {
      selected.push(row);
      continue;
    }
    const nextBody = batchedBody({ ...selected[0], body }, row);
    if (nextBody.length > maxBatchBodyChars || Buffer.byteLength(nextBody, "utf8") > maxBodyBytes) break;
    body = nextBody;
    selected.push(row);
  }
  return selected;
}

function deliveryItem(row, attemptNumber, deliveryExpiresAt) {
  return {
    message_id: row.id,
    message_incarnation_id: row.message_incarnation_id,
    attempt_number: attemptNumber,
    delivery_expires_at: deliveryExpiresAt,
  };
}

function deliveryFromRows(database, rows, items, deliveryExpiresAt) {
  const first = selectMessage(database, rows[0].id) ?? toMessage(rows[0]);
  let body = first.body;
  if (rows.length > 1 && first.sentinel === undefined) {
    body = rows.slice(1).reduce((current, row) => batchedBody({ ...rows[0], body: current }, row), first.body);
  }
  const message = rows.length > 1 ? { ...first, body } : first;
  return {
    message,
    attempt_number: items[0].attempt_number,
    delivery_expires_at: deliveryExpiresAt,
    items,
  };
}

function prepareEnvelope(envelope) {
  const subject = envelope.subject === undefined ? null : envelope.subject;
  return {
    id: envelope.id,
    from: envelope.from,
    to: envelope.to,
    subject,
    body: envelope.body,
    created_at: envelope.created_at,
    sentinel: envelope.sentinel,
    sentinel_json: envelope.sentinel === undefined ? null : JSON.stringify(envelope.sentinel),
    source: normalizeMessageSource(envelope.source),
  };
}

function messageFromConversationCandidate(candidate, conversationRow) {
  return {
    id: candidate.id,
    from: candidate.from,
    to: candidate.to,
    ...(candidate.subject === null ? {} : { subject: candidate.subject }),
    body: candidate.body,
    created_at: candidate.created_at,
    expires_at: oneHourAfter(candidate.created_at),
    message_incarnation_id: conversationRow.message_incarnation_id,
    state: "acknowledged",
    ...(candidate.sentinel === undefined ? {} : { sentinel: candidate.sentinel }),
  };
}

function prepareConversationCandidate(candidate, conversation) {
  if (conversation !== undefined && (conversation === null || typeof conversation !== "object" || Array.isArray(conversation))) {
    throw new TypeError("conversation must be an object");
  }
  const body = conversation?.body ?? candidate.body;
  if (typeof body !== "string") throw new TypeError("conversation body must be a string");
  const attachments = conversation?.attachments ?? [];
  if (!Array.isArray(attachments)) throw new TypeError("conversation attachments must be an array");
  const source = normalizeMessageSource(conversation?.source ?? candidate.source);
  return { ...candidate, body, attachments, source };
}

function normalizeMessageSource(source) {
  const kind = source !== undefined && source !== null && typeof source === "object" && !Array.isArray(source) ? source.kind : undefined;
  return { kind: typeof kind === "string" && MESSAGE_SOURCE_KINDS.has(kind) ? kind : "unknown" };
}

function insertConversationMessage(database, candidate, { messageIncarnationId, deliveryMessageId, recordedAt }) {
  const [participantA, participantB] = conversationParticipants(candidate.from, candidate.to);
  database.prepare(`
    INSERT OR IGNORE INTO conversation_messages (
      message_id,
      message_incarnation_id,
      delivery_message_id,
      sender,
      recipient,
      participant_a,
      participant_b,
      subject,
      body,
      created_at,
      recorded_at,
      attachments_json,
      source_kind
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    candidate.id,
    messageIncarnationId,
    deliveryMessageId,
    candidate.from,
    candidate.to,
    participantA,
    participantB,
    candidate.subject,
    candidate.body,
    candidate.created_at,
    recordedAt,
    JSON.stringify(candidate.attachments),
    candidate.source.kind,
  );
}

function conversationParticipants(left, right) {
  return left < right ? [left, right] : [right, left];
}

function conversationAttachments(value) {
  try {
    const attachments = JSON.parse(value);
    return Array.isArray(attachments) ? attachments : [];
  } catch {
    return [];
  }
}

function selectLatestConversationMessage(database, messageId) {
  return database.prepare(`
    SELECT
      conversation_sequence,
      message_id,
      message_incarnation_id,
      delivery_message_id,
      sender,
      recipient,
      subject,
      body,
      created_at,
      recorded_at,
      attachments_json,
      source_kind
    FROM conversation_messages
    WHERE message_id = ?
    ORDER BY conversation_sequence DESC
    LIMIT 1
  `).get(messageId);
}

function sameConversationCandidate(row, candidate) {
  return row.message_id === candidate.id
    && row.sender === candidate.from
    && row.recipient === candidate.to
    && row.subject === candidate.subject
    && row.body === candidate.body
    && row.created_at === candidate.created_at
    && JSON.stringify(conversationAttachments(row.attachments_json)) === JSON.stringify(candidate.attachments)
    && ((row.source_kind ?? "unknown") === "unknown" || (row.source_kind ?? "unknown") === candidate.source.kind);
}

function selectConversationMessage(database, messageId, messageIncarnationId) {
  const row = database.prepare(`
    SELECT
      conversation_sequence,
      message_id,
      message_incarnation_id,
      delivery_message_id,
      sender,
      recipient,
      subject,
      body,
      created_at,
      recorded_at,
      attachments_json,
      source_kind
    FROM conversation_messages
    WHERE message_id = ? AND message_incarnation_id = ?
  `).get(messageId, messageIncarnationId);
  return row === undefined ? undefined : toConversationMessage(row);
}

function toConversationMessage(row) {
  return {
    sequence: row.conversation_sequence,
    id: row.message_id,
    message_incarnation_id: row.message_incarnation_id,
    from: row.sender,
    to: row.recipient,
    ...(row.subject === null ? {} : { subject: row.subject }),
    body: row.body,
    created_at: row.created_at,
    recorded_at: row.recorded_at,
    attachments: conversationAttachments(row.attachments_json),
    source: { kind: row.source_kind ?? "unknown" },
  };
}

function assertConversationOwner(value) {
  if (typeof value !== "string" || !/^(?!.*@)[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(value)) {
    throw new TypeError("owner must be an agent ID");
  }
}

function assertConversationPeer(value) {
  if (typeof value !== "string" || value.length === 0 || value.length > 128) {
    throw new TypeError("peer must be a protocol sender");
  }
}

function enqueueCandidate(
  database,
  candidate,
  { firstSeenAt, maxBodyBytes, maxQueuedMessages, maxQueuedBytes, maxBatchBodyChars = DEFAULT_MAX_BATCH_BODY_CHARS, idSource = randomUUID },
) {
  const existing = selectMessage(database, candidate.id);
  if (existing !== undefined) {
    if (!sameEnvelope(existing, candidate)) {
      throw new IdempotencyConflictError(candidate.id);
    }
    return { created: false, message: existing };
  }

  const duplicate = selectLiveDuplicateMessage(database, candidate);
  if (duplicate !== undefined) {
    return { created: false, deduped: true, message: duplicate };
  }

  const usage = selectLiveQueueUsage(database, candidate.to);
  const candidateBytes = envelopeByteLength(candidate);
  if (usage.message_count >= maxQueuedMessages
    || usage.queued_bytes + candidateBytes > maxQueuedBytes) {
    throw new QueueLimitError(candidate.to);
  }

  database.prepare(`
    INSERT INTO agents (agent_id, first_seen_at)
    VALUES (?, ?)
    ON CONFLICT (agent_id) DO NOTHING
  `).run(candidate.to, firstSeenAt);
  database.prepare(`
    INSERT INTO messages (
      id, sender, recipient, subject, body, created_at, expires_at, message_incarnation_id, sentinel_json, source_kind, state
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued')
  `).run(
    candidate.id,
    candidate.from,
    candidate.to,
    candidate.subject,
    candidate.body,
    candidate.created_at,
    oneHourAfter(firstSeenAt),
    idSource(),
    candidate.sentinel_json,
    candidate.source.kind,
  );
  return {
    created: true,
    message: selectMessage(database, candidate.id),
  };
}

function selectLiveDuplicateMessage(database, candidate) {
  const row = database.prepare(`
    SELECT id
    FROM messages
    WHERE id <> ?
      AND sender = ?
      AND recipient = ?
      AND subject IS ?
      AND body = ?
      AND sentinel_json IS ?
      AND state IN ('queued', 'leased', 'injection_requested')
    ORDER BY enqueue_sequence ASC
    LIMIT 1
  `).get(
    candidate.id,
    candidate.from,
    candidate.to,
    candidate.subject,
    candidate.body,
    candidate.sentinel_json,
  );
  return row === undefined ? undefined : selectMessage(database, row.id);
}

export function isPortablePromptTemplateId(value) {
  return typeof value === "string"
    && value.length > 0
    && !value.includes("\u0000")
    && Buffer.byteLength(value, "utf8") <= PROMPT_TEMPLATE_MAX_ID_BYTES;
}

function validatePromptTemplateTimestamp(value) {
  if (typeof value !== "string"
    || !PROMPT_TEMPLATE_TIMESTAMP_PATTERN.test(value)
    || !Number.isFinite(Date.parse(value))) {
    throw new PromptTemplateValidationError();
  }
  try {
    if (new Date(value).toISOString() !== value) throw new PromptTemplateValidationError();
  } catch {
    throw new PromptTemplateValidationError();
  }
}

function validatePromptTemplate(promptTemplate) {
  if (promptTemplate === null || typeof promptTemplate !== "object" || Array.isArray(promptTemplate)) {
    throw new PromptTemplateValidationError();
  }
  const expectedKeys = new Set(["id", "name", "body", "updated_at"]);
  if (Object.keys(promptTemplate).some((key) => !expectedKeys.has(key))
    || typeof promptTemplate.name !== "string"
    || promptTemplate.name.includes("\u0000")
    || promptTemplate.name.trim() === ""
    || Buffer.byteLength(promptTemplate.name, "utf8") > PROMPT_TEMPLATE_MAX_NAME_BYTES
    || typeof promptTemplate.body !== "string"
    || promptTemplate.body.includes("\u0000")
    || promptTemplate.body.trim() === ""
    || Buffer.byteLength(promptTemplate.body, "utf8") > PROMPT_TEMPLATE_MAX_BODY_BYTES) {
    throw new PromptTemplateValidationError();
  }
  const candidate = { name: promptTemplate.name, body: promptTemplate.body };
  if (Object.hasOwn(promptTemplate, "id")) {
    if (!isPortablePromptTemplateId(promptTemplate.id)) throw new PromptTemplateValidationError();
    candidate.id = promptTemplate.id;
  }
  if (Object.hasOwn(promptTemplate, "updated_at")) {
    validatePromptTemplateTimestamp(promptTemplate.updated_at);
    candidate.updated_at = promptTemplate.updated_at;
  }
  return candidate;
}

function validatePromptTemplatePatch(patch) {
  if (patch === null || typeof patch !== "object" || Array.isArray(patch)) {
    throw new PromptTemplateValidationError();
  }
  const patchKeys = Object.keys(patch);
  if (patchKeys.length === 0 || patchKeys.some((key) => !["name", "body", "updated_at"].includes(key))) {
    throw new PromptTemplateValidationError();
  }
  if (Object.hasOwn(patch, "name") && (typeof patch.name !== "string"
    || patch.name.includes("\u0000")
    || patch.name.trim() === ""
    || Buffer.byteLength(patch.name, "utf8") > PROMPT_TEMPLATE_MAX_NAME_BYTES)) {
    throw new PromptTemplateValidationError();
  }
  if (Object.hasOwn(patch, "body") && (typeof patch.body !== "string"
    || patch.body.includes("\u0000")
    || patch.body.trim() === ""
    || Buffer.byteLength(patch.body, "utf8") > PROMPT_TEMPLATE_MAX_BODY_BYTES)) {
    throw new PromptTemplateValidationError();
  }
  if (Object.hasOwn(patch, "updated_at")) validatePromptTemplateTimestamp(patch.updated_at);
  return { ...patch };
}

export function validateReminder(reminder, { maxBodyBytes = DEFAULT_MAX_BODY_BYTES } = {}) {
  assertPositiveLimit(maxBodyBytes, "maxBodyBytes", ABSOLUTE_MAX_BODY_BYTES);
  if (reminder === null || typeof reminder !== "object" || Array.isArray(reminder)) {
    throw new ReminderValidationError();
  }
  const expectedKeys = new Set([
    "id",
    "target",
    "creator",
    "subject",
    "body",
    "created_at",
    "next_due_at",
    "interval_ms",
    "sentinel",
  ]);
  if (Object.keys(reminder).some((key) => !expectedKeys.has(key))) {
    throw new ReminderValidationError();
  }
  const {
    id,
    target,
    creator,
    subject,
    body,
    created_at: createdAt,
    next_due_at: nextDueAt,
    interval_ms: intervalMs,
    sentinel,
  } = reminder;
  assertReminderId(id);
    if (!(intervalMs === null || (Number.isSafeInteger(intervalMs) && intervalMs > 0))) {
    throw new ReminderValidationError("interval_ms must be null or a positive safe integer");
  }
  try {
    validateEnvelope({
      id: reminderMessageId(id, 0),
      from: creator,
      to: target,
      ...(subject === undefined ? {} : { subject }),
      ...(sentinel === undefined ? {} : { sentinel }),
      body,
      created_at: createdAt,
    }, { maxBodyBytes });
    validateEnvelope({
      id: reminderMessageId(id, 0),
      from: creator,
      to: target,
      body: "",
      created_at: nextDueAt,
    }, { maxBodyBytes });
  } catch (error) {
    if (error instanceof ProtocolValidationError) throw new ReminderValidationError();
    throw error;
  }
  if (intervalMs !== null && Date.parse(nextDueAt) + intervalMs > MAX_UTC_TIMESTAMP_MS) {
    throw new ReminderValidationError("interval_ms cannot advance beyond supported UTC timestamps");
  }
  return {
    id,
    target,
    creator,
    subject: subject === undefined ? null : subject,
    body,
    created_at: createdAt,
    next_due_at: nextDueAt,
    interval_ms: intervalMs,
    ...(sentinel === undefined ? {} : { sentinel }),
  };
}

function fireDueReminder(database, reminderId, now, limits) {
  const reminder = selectReminder(database, reminderId);
  if (reminder === undefined
    || reminder.state !== "scheduled"
    || reminder.next_due_at > now) {
    return undefined;
  }
  if (reminder.interval_ms === null) {
    return fireDueOneShotReminder(database, reminder, now, limits);
  }
  return fireDueRecurringReminder(database, reminder, now, limits);
}

function enqueueReminderMessage(database, reminder, now, limits) {
  const candidate = prepareEnvelope(validateEnvelope({
    id: reminderMessageId(reminder.id, reminder.occurrence_number),
    from: "reminders",
    to: reminder.target,
    ...(reminder.subject === undefined ? {} : { subject: reminder.subject }),
    ...(reminder.sentinel === undefined ? {} : { sentinel: reminder.sentinel }),
    body: reminder.body,
    created_at: now,
    source: { kind: "reminder" },
  }, { maxBodyBytes: limits.maxBodyBytes }));
  const result = enqueueCandidate(
    database,
    candidate,
    {
      firstSeenAt: now,
      maxBodyBytes: limits.maxBodyBytes,
      maxQueuedMessages: limits.maxQueuedMessages,
      maxQueuedBytes: limits.maxQueuedBytes,
      maxBatchBodyChars: limits.maxBatchBodyChars,
      idSource: limits.idSource,
    },
  );
  if (!result.created && !result.batched && !result.deduped) {
    throw new IdempotencyConflictError(result.message.id);
  }
  const messageIncarnationId = result.message.id === candidate.id
    ? result.message.message_incarnation_id
    : limits.idSource();
  insertConversationMessage(database, prepareConversationCandidate(candidate), {
    messageIncarnationId,
    deliveryMessageId: result.message.id,
    recordedAt: now,
  });
  return {
    ...result,
    conversation_message: selectConversationMessage(database, candidate.id, messageIncarnationId),
    conversation_created: true,
  };
}

function fireDueOneShotReminder(database, reminder, now, limits) {
  limits.onAttempt();
  const message = enqueueReminderMessage(database, reminder, now, limits);
  const updated = database.prepare(`
    UPDATE reminders
    SET state = 'message_queued', linked_message_id = ?
    WHERE id = ?
      AND state = 'scheduled'
      AND interval_ms IS NULL
      AND next_due_at <= ?
  `).run(message.message.id, reminder.id, now);
  if (updated.changes !== 1) {
    throw new Error(`reminder ${reminder.id} changed while its due message was firing`);
  }
  return {
    reminder: selectReminder(database, reminder.id),
    message: message.message,
    conversation_message: message.conversation_message,
    conversation_created: message.conversation_created,
  };
}

function fireDueRecurringReminder(database, reminder, now, limits) {
  const agent = selectAgent(database, reminder.target);
  if (agent === undefined) return undefined;
  const lease = selectLease(database, reminder.target);
  if (agent.last_connected_at === null) {
    removeAgentResources(database, reminder.target);
    return undefined;
  }
  const nextDueAt = nextRecurringDueAt(reminder.next_due_at, reminder.interval_ms, now);
  if (lease === undefined || leaseExpired(lease, now)) {
    if (agent.last_connected_at <= oneHourBefore(now)) {
      removeAgentResources(database, reminder.target);
      return undefined;
    }
    database.prepare(`
      UPDATE reminders
      SET next_due_at = ?
      WHERE id = ? AND state = 'scheduled'
    `).run(nextDueAt, reminder.id);
    return undefined;
  }
  if (reminder.linked_message_id !== null) {
    database.prepare(`
      UPDATE reminders
      SET next_due_at = ?
      WHERE id = ? AND state = 'scheduled'
    `).run(nextDueAt, reminder.id);
    return undefined;
  }
  if (timestampAfter(reminder.next_due_at, reminder.interval_ms) <= now) {
    database.prepare(`
      UPDATE reminders
      SET next_due_at = ?
      WHERE id = ? AND state = 'scheduled' AND linked_message_id IS NULL
    `).run(nextDueAt, reminder.id);
    return undefined;
  }
  limits.onAttempt();
  const message = enqueueReminderMessage(database, reminder, now, limits);
  const updated = database.prepare(`
    UPDATE reminders
    SET linked_message_id = ?,
        occurrence_number = occurrence_number + 1,
        next_due_at = ?
    WHERE id = ?
      AND state = 'scheduled'
      AND interval_ms IS NOT NULL
      AND linked_message_id IS NULL
      AND next_due_at <= ?
  `).run(message.message.id, nextDueAt, reminder.id, now);
  if (updated.changes !== 1) {
    throw new Error(`reminder ${reminder.id} changed while its due message was firing`);
  }
  return {
    reminder: selectReminder(database, reminder.id),
    message: message.message,
    conversation_message: message.conversation_message,
    conversation_created: message.conversation_created,
  };
}

function completeLinkedReminder(database, messageId, reminderIncarnations) {
  const completedReminderIds = database.prepare(`
    SELECT id
    FROM reminders
    WHERE linked_message_id = ?
      AND interval_ms IS NULL
      AND state = 'message_queued'
  `).all(messageId).map(({ id }) => id);
  database.prepare(`
    DELETE FROM reminders
    WHERE linked_message_id = ?
      AND interval_ms IS NULL
      AND state = 'message_queued'
  `).run(messageId);
  for (const reminderId of completedReminderIds) reminderIncarnations.delete(reminderId);
  database.prepare(`
    UPDATE reminders
    SET state = 'scheduled', linked_message_id = NULL
    WHERE linked_message_id = ?
      AND interval_ms IS NOT NULL
      AND state IN ('scheduled', 'message_queued')
  `).run(messageId);
}

function selectPromptTemplate(database, promptTemplateId) {
  const promptTemplate = database.prepare(`
    SELECT id, name, body, created_at, updated_at
    FROM prompt_templates
    WHERE id = ?
  `).get(promptTemplateId);
  return promptTemplate === undefined ? undefined : toPromptTemplate(promptTemplate);
}

function selectReminder(database, reminderId) {
  const reminder = database.prepare(`
    SELECT
      reminders.id,
      reminders.target,
      reminders.creator,
      reminders.subject,
      reminders.body,
      reminders.created_at,
      reminders.next_due_at,
      reminders.interval_ms,
      reminders.sentinel_json,
      reminders.state,
      reminders.linked_message_id,
      messages.state AS linked_message_state,
      reminders.occurrence_number,
      reminders.attempt_count,
      reminders.last_attempt_at
    FROM reminders
    LEFT JOIN messages ON messages.id = reminders.linked_message_id
    WHERE reminders.id = ?
  `).get(reminderId);
  return reminder === undefined ? undefined : toReminder(reminder);
}

function selectServerJob(database, jobKey) {
  const job = database.prepare(`
    SELECT
      job_key,
      schedule_interval_hours,
      schedule_anchor_hour,
      schedule_minute,
      schedule_time_zone,
      definition_fingerprint,
      next_due_at
    FROM server_jobs
    WHERE job_key = ?
  `).get(jobKey);
  return job === undefined ? undefined : toServerJob(job);
}

function selectLatestServerJobRun(database, jobKey, scheduledFor) {
  const run = database.prepare(`
    SELECT
      job_key,
      scheduled_for,
      attempt_number,
      outcome,
      attempted_at,
      retry_at,
      error_code,
      message_id
    FROM server_job_runs
    WHERE job_key = ? AND scheduled_for = ?
    ORDER BY attempt_number DESC
    LIMIT 1
  `).get(jobKey, scheduledFor);
  return run === undefined ? undefined : { ...run };
}

function selectServerJobRun(database, jobKey, scheduledFor, attemptNumber) {
  const run = database.prepare(`
    SELECT
      job_key,
      scheduled_for,
      attempt_number,
      outcome,
      attempted_at,
      retry_at,
      error_code,
      message_id
    FROM server_job_runs
    WHERE job_key = ? AND scheduled_for = ? AND attempt_number = ?
  `).get(jobKey, scheduledFor, attemptNumber);
  return run === undefined ? undefined : { ...run };
}

function serverJobRunIsDue(database, job, scheduledFor, now) {
  if (job === undefined || job.next_due_at !== scheduledFor || scheduledFor > now) return false;
  const latest = selectLatestServerJobRun(database, job.job_key, scheduledFor);
  return latest === undefined || (latest.outcome === "failed" && latest.retry_at <= now);
}

function validateServerJobDefinitions(definitions) {
  if (!Array.isArray(definitions) || definitions.length === 0) {
    throw new TypeError("server job definitions must be a non-empty array");
  }
  const seen = new Set();
  return definitions.map((definition) => {
    if (definition === null || typeof definition !== "object" || Array.isArray(definition)) {
      throw new TypeError("server job definition is invalid");
    }
    const {
      key,
      scheduleIntervalHours,
      scheduleAnchorHour,
      scheduleMinute,
      scheduleTimeZone,
      definitionFingerprint,
    } = definition;
    assertServerJobKey(key);
    if (seen.has(key)) throw new TypeError(`server job definition ${key} is duplicated`);
    if (!Number.isSafeInteger(scheduleIntervalHours)
      || scheduleIntervalHours <= 0
      || 24 % scheduleIntervalHours !== 0) {
      throw new TypeError("scheduleIntervalHours must evenly divide one day");
    }
    if (!Number.isSafeInteger(scheduleAnchorHour)
      || scheduleAnchorHour < 0
      || scheduleAnchorHour >= 24
      || !Number.isSafeInteger(scheduleMinute)
      || scheduleMinute < 0
      || scheduleMinute >= 60
      || !isTimeZone(scheduleTimeZone)) {
      throw new TypeError("server job wall-clock schedule is invalid");
    }
    if (typeof definitionFingerprint !== "string" || !/^[a-f0-9]{64}$/.test(definitionFingerprint)) {
      throw new TypeError("server job definition fingerprint is invalid");
    }
    seen.add(key);
    return {
      key,
      scheduleIntervalHours,
      scheduleAnchorHour,
      scheduleMinute,
      scheduleTimeZone,
      definitionFingerprint,
    };
  });
}

function firstServerJobDueAt(job, now) {
  return nextServerJobDueAt(job, now, true);
}

function nextServerJobDueAt(job, now, inclusive = false) {
  const current = Date.parse(now);
  let candidate = Math.floor(current / 60_000) * 60_000;
  if (!inclusive || candidate < current) candidate += 60_000;
  const formatter = new Intl.DateTimeFormat("en-US", {
    timeZone: job.schedule_time_zone ?? job.scheduleTimeZone,
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });
  const anchorHour = job.schedule_anchor_hour ?? job.scheduleAnchorHour;
  const minute = job.schedule_minute ?? job.scheduleMinute;
  const intervalHours = job.schedule_interval_hours ?? job.scheduleIntervalHours;
  const limit = candidate + (26 * 60 * 60 * 1000);
  while (candidate <= limit) {
    const parts = Object.fromEntries(formatter.formatToParts(new Date(candidate))
      .filter((part) => part.type === "hour" || part.type === "minute")
      .map((part) => [part.type, Number(part.value)]));
    if (parts.minute === minute
      && (parts.hour - anchorHour + 24) % intervalHours === 0) {
      return new Date(candidate).toISOString();
    }
    candidate += 60_000;
  }
  throw new Error(`server job ${job.job_key ?? job.key} has no next wall-clock slot`);
}

function serverJobRetryDelayMs(attemptNumber) {
  return SERVER_JOB_FAILURE_RETRY_MS * (2 ** (attemptNumber - 1));
}

function selectAgent(database, agentId) {
  return database.prepare(`
    SELECT agent_id, first_seen_at, last_connected_at, disconnected_at
    FROM agents
    WHERE agent_id = ?
  `).get(agentId);
}

function cleanupStaleAgentResources(database, now) {
  const threshold = oneHourBefore(now);
  const stale = database.prepare(`
    SELECT agent_id
    FROM agents
    WHERE (
      (
        last_connected_at IS NULL
        AND first_seen_at <= ?
        AND NOT EXISTS (
          SELECT 1
          FROM reminders
          WHERE reminders.target = agents.agent_id
            AND (
              reminders.state = 'scheduled'
              OR (reminders.interval_ms IS NULL AND reminders.linked_message_id IS NOT NULL)
            )
        )
      )
      OR (disconnected_at IS NOT NULL AND disconnected_at <= ?)
    )
    ORDER BY agent_id ASC
  `).all(threshold, threshold).map(({ agent_id: agentId }) => agentId);
  for (const agentId of stale) removeAgentResources(database, agentId);
  return stale;
}

function removeAgentResources(database, agentId) {
  database.prepare("DELETE FROM agents WHERE agent_id = ?").run(agentId);
  return true;
}

function sameReminder(reminder, candidate) {
  return reminder.id === candidate.id
    && reminder.target === candidate.target
    && reminder.creator === candidate.creator
    && (reminder.subject ?? null) === candidate.subject
    && reminder.body === candidate.body
    && reminder.created_at === candidate.created_at
    && reminder.next_due_at === candidate.next_due_at
    && reminder.interval_ms === candidate.interval_ms
    && sameSentinelDescriptor(reminder.sentinel, candidate.sentinel);
}

function reminderMessageId(reminderId, occurrenceNumber) {
  return `reminder-${reminderId}-${occurrenceNumber}`;
}

function nextRecurringDueAt(priorDueAt, intervalMs, now) {
  const priorMilliseconds = Date.parse(priorDueAt);
  const nowMilliseconds = Date.parse(now);
  const skipped = Math.floor((nowMilliseconds - priorMilliseconds) / intervalMs) + 1;
  const nextMilliseconds = priorMilliseconds + (skipped * intervalMs);
  if (!Number.isSafeInteger(nextMilliseconds) || nextMilliseconds > MAX_UTC_TIMESTAMP_MS) {
    throw new ReminderValidationError("recurrence cannot advance beyond supported UTC timestamps");
  }
  return new Date(nextMilliseconds).toISOString();
}

function oneHourBefore(timestamp) {
  return timestampAfter(timestamp, -ONE_HOUR_MS);
}

function assertReminderId(reminderId) {
  if (typeof reminderId !== "string"
    || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$/.test(reminderId)) {
    throw new ReminderValidationError("reminder id is invalid");
  }
}

function assertServerJobKey(jobKey) {
  if (typeof jobKey !== "string" || !/^[a-z][a-z0-9-]{0,63}$/.test(jobKey)) {
    throw new TypeError("server job key is invalid");
  }
}

function assertTimestamp(timestamp, name) {
  if (typeof timestamp !== "string" || !Number.isFinite(Date.parse(timestamp))) {
    throw new TypeError(`${name} must be a valid timestamp`);
  }
}

function assertExitCode(exitCode) {
  if (!Number.isSafeInteger(exitCode) || exitCode < 0 || exitCode > 255) {
    throw new TypeError("exitCode must be an integer from 0 through 255");
  }
}

function isTimeZone(value) {
  if (typeof value !== "string" || value.length === 0) return false;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: value });
    return true;
  } catch {
    return false;
  }
}

function reminderRetryIsDue(lastAttemptAt, now) {
  return lastAttemptAt === null
    || Date.parse(lastAttemptAt) + REMINDER_FAILURE_RETRY_MS <= Date.parse(now);
}

function reminderFireErrorCode(error) {
  return typeof error?.code === "string" && /^[a-z][a-z0-9_]{0,63}$/.test(error.code)
    ? error.code
    : "internal_error";
}

function selectLease(database, agentId) {
  return database.prepare(`
    SELECT lease_id, agent_id, instance_id, acquired_at, expires_at
    FROM agent_leases
    WHERE agent_id = ?
  `).get(agentId);
}

function requireActiveAgentLease(database, { agentId, instanceId, leaseId }, now) {
  const lease = selectLease(database, agentId);
  if (lease === undefined || lease.instance_id !== instanceId || lease.lease_id !== leaseId) {
    throw new LeaseNotActiveError(agentId);
  }
  if (leaseExpired(lease, now)) {
    deleteLease(database, agentId);
    markDisconnected(database, agentId, now);
    throw new LeaseNotActiveError(agentId);
  }
  return lease;
}

function selectInFlightDelivery(database, agentId) {
  return database.prepare(`
    SELECT messages.id
    FROM messages
    JOIN delivery_attempts ON delivery_attempts.message_id = messages.id
    WHERE messages.recipient = ?
      AND messages.state IN ('leased', 'injection_requested')
      AND delivery_attempts.state IN ('leased', 'injection_requested')
    LIMIT 1
  `).get(agentId);
}

function selectOldestQueuedMessage(database, agentId, now) {
  return database.prepare(`
    SELECT id, message_incarnation_id
    FROM messages
    WHERE recipient = ?
      AND state = 'queued'
      AND (claimed_at IS NOT NULL OR expires_at > ?)
    ORDER BY enqueue_sequence ASC
    LIMIT 1
  `).get(agentId, now);
}

function nextAttemptNumber(database, messageId) {
  return database.prepare(`
    SELECT COALESCE(MAX(attempt_number), 0) + 1 AS attempt_number
    FROM delivery_attempts
    WHERE message_id = ?
  `).get(messageId).attempt_number;
}

function requireActiveDelivery(database, { agentId, leaseId, messageId, attemptNumber }) {
  const delivery = database.prepare(`
    SELECT
      messages.id AS message_id,
      messages.recipient AS agent_id,
      messages.state AS message_state,
      messages.message_incarnation_id,
      delivery_attempts.attempt_number,
      delivery_attempts.owner_lease_id,
      delivery_attempts.delivery_expires_at,
      delivery_attempts.delivery_batch_id,
      delivery_attempts.state AS attempt_state
    FROM delivery_attempts
    JOIN messages ON messages.id = delivery_attempts.message_id
    WHERE delivery_attempts.message_id = ? AND delivery_attempts.attempt_number = ?
  `).get(messageId, attemptNumber);
  if (delivery === undefined
    || delivery.agent_id !== agentId
    || delivery.owner_lease_id !== leaseId
    || !["leased", "injection_requested"].includes(delivery.message_state)
    || !["leased", "injection_requested"].includes(delivery.attempt_state)) {
    throw new DeliveryNotActiveError(messageId, attemptNumber);
  }
  return delivery;
}

function requireActiveDeliveryBatch(database, { agentId, leaseId, messageId, attemptNumber, deliveryItems, allowBatchWithoutDeliveryItems = false }) {
  const first = requireActiveDelivery(database, { agentId, leaseId, messageId, attemptNumber });
  const rows = database.prepare(`
    SELECT
      messages.id AS message_id,
      messages.recipient AS agent_id,
      messages.state AS message_state,
      messages.message_incarnation_id,
      delivery_attempts.attempt_number,
      delivery_attempts.owner_lease_id,
      delivery_attempts.delivery_expires_at,
      delivery_attempts.delivery_batch_id,
      delivery_attempts.state AS attempt_state
    FROM delivery_attempts
    JOIN messages ON messages.id = delivery_attempts.message_id
    WHERE messages.recipient = ?
      AND messages.state IN ('leased', 'injection_requested')
      AND delivery_attempts.state IN ('leased', 'injection_requested')
      AND (
        (delivery_attempts.delivery_batch_id IS NOT NULL AND delivery_attempts.delivery_batch_id = ?)
        OR (delivery_attempts.delivery_batch_id IS NULL AND delivery_attempts.message_id = ?)
      )
    ORDER BY messages.enqueue_sequence ASC
  `).all(first.agent_id, first.delivery_batch_id, first.message_id);
  if (rows.length === 0 || rows.some((row) => row.owner_lease_id !== leaseId)) {
    throw new DeliveryNotActiveError(messageId, attemptNumber);
  }
  const expected = deliveryItemsForDeliveries(rows);
  if (messageId !== expected[0].message_id || attemptNumber !== expected[0].attempt_number) {
    throw new DeliveryNotActiveError(messageId, attemptNumber);
  }
  if ((rows.length > 1 && !allowBatchWithoutDeliveryItems) || deliveryItems !== undefined) {
    if (!sameDeliveryItems(deliveryItems, expected)) {
      throw new DeliveryNotActiveError(messageId, attemptNumber);
    }
  }
  return rows;
}

function deliveryItemsForDeliveries(deliveries) {
  return deliveries.map((delivery) => ({
    message_id: delivery.message_id,
    message_incarnation_id: delivery.message_incarnation_id,
    attempt_number: delivery.attempt_number,
    delivery_expires_at: delivery.delivery_expires_at,
  }));
}

function sameDeliveryItems(actual, expected) {
  if (!Array.isArray(actual) || actual.length !== expected.length) return false;
  return actual.every((item, index) => {
    const wanted = expected[index];
    return item.message_id === wanted.message_id
      && item.message_incarnation_id === wanted.message_incarnation_id
      && item.attempt_number === wanted.attempt_number
      && item.delivery_expires_at === wanted.delivery_expires_at;
  });
}

function expireDeliveryBatchAtDeadline(database, deliveries, now, maxDeliveryAttempts) {
  let expired = false;
  for (const delivery of deliveries) {
    expired = expireDeliveryAtDeadline(database, delivery, now, maxDeliveryAttempts) || expired;
  }
  return expired;
}

function combineFailureOutcomes(messageId, outcomes) {
  const recurring = outcomes.flatMap((outcome) => outcome.recurring_reminder_rearmed ?? []);
  return {
    message_id: messageId,
    state: outcomes.some((outcome) => outcome.state === "dead_letter") ? "dead_letter" : "queued",
    ...(recurring.length === 0 ? {} : { recurring_reminder_rearmed: recurring }),
  };
}

function expireDeliveryAtDeadline(database, delivery, now, maxDeliveryAttempts) {
  if (delivery.delivery_expires_at > now) return false;
  failDeliveryAttempt(
    database,
    delivery,
    {
      attemptState: "timed_out",
      reason: "delivery_timeout",
      now,
      maxDeliveryAttempts,
      retryBatchId: delivery.delivery_batch_id,
    },
  );
  return true;
}

function failDeliveryAttempt(
  database,
  delivery,
  { attemptState, reason, now, maxDeliveryAttempts, retryBatchId },
) {
  database.prepare(`
    UPDATE delivery_attempts
    SET state = ?, updated_at = ?, failure_reason = ?
    WHERE message_id = ?
      AND attempt_number = ?
      AND state IN ('leased', 'injection_requested')
  `).run(attemptState, now, reason, delivery.message_id, delivery.attempt_number);
  const state = delivery.attempt_number >= maxDeliveryAttempts ? "dead_letter" : "queued";
  database.prepare(`
    UPDATE messages
    SET state = ?, retry_batch_id = ?
    WHERE id = ? AND state = ?
  `).run(state, state === "queued" ? retryBatchId : null, delivery.message_id, delivery.message_state);
  let recurringReminderRearmed;
  if (state === "dead_letter") {
    recurringReminderRearmed = rearmRecurringRemindersAfterDeadLetter(database, delivery.message_id, now);
    database.prepare(`
      UPDATE reminders
      SET state = 'failed',
          attempt_count = attempt_count + 1,
          last_attempt_at = ?
      WHERE linked_message_id = ?
        AND interval_ms IS NULL
        AND state = 'message_queued'
    `).run(now, delivery.message_id);
  }
  return {
    message_id: delivery.message_id,
    state,
    ...(recurringReminderRearmed === undefined ? {} : { recurring_reminder_rearmed: recurringReminderRearmed }),
  };
}

function rearmRecurringRemindersAfterDeadLetter(database, messageId, now) {
  const reminders = database.prepare(`
    SELECT id, next_due_at, interval_ms
    FROM reminders
    WHERE linked_message_id = ?
      AND interval_ms IS NOT NULL
      AND state = 'scheduled'
    ORDER BY id ASC
  `).all(messageId);
  const rearmed = [];
  for (const reminder of reminders) {
    const nextDueAt = recurringDueAtAfterNow(reminder.next_due_at, reminder.interval_ms, now);
    const updated = database.prepare(`
      UPDATE reminders
      SET linked_message_id = NULL,
          next_due_at = ?,
          attempt_count = attempt_count + 1,
          last_attempt_at = ?
      WHERE id = ?
        AND linked_message_id = ?
        AND interval_ms IS NOT NULL
        AND state = 'scheduled'
    `).run(nextDueAt, now, reminder.id, messageId);
    if (updated.changes === 1) rearmed.push({ reminder_id: reminder.id, next_due_at: nextDueAt });
  }
  return rearmed.length === 0 ? undefined : rearmed;
}

function rearmFailedLinkedReminderForRetry(database, messageId, now) {
  const reminders = database.prepare(`
    SELECT id, next_due_at, interval_ms
    FROM reminders
    WHERE linked_message_id = ? AND state = 'failed'
  `).all(messageId);
  for (const reminder of reminders) {
    if (reminder.interval_ms === null) {
      database.prepare(`
        UPDATE reminders
        SET state = 'message_queued'
        WHERE id = ? AND linked_message_id = ? AND state = 'failed'
      `).run(reminder.id, messageId);
      continue;
    }
    const nextDueAt = recurringDueAtAfterNow(reminder.next_due_at, reminder.interval_ms, now);
    database.prepare(`
      UPDATE reminders
      SET state = 'scheduled',
          linked_message_id = NULL,
          next_due_at = ?
      WHERE id = ? AND linked_message_id = ? AND state = 'failed'
    `).run(nextDueAt, reminder.id, messageId);
  }
}

function recurringDueAtAfterNow(priorDueAt, intervalMs, now) {
  return priorDueAt > now ? priorDueAt : nextRecurringDueAt(priorDueAt, intervalMs, now);
}

function deleteLease(database, agentId) {
  database.prepare("DELETE FROM agent_leases WHERE agent_id = ?").run(agentId);
}

function markDisconnected(database, agentId, disconnectedAt) {
  database.prepare(`
    UPDATE agents
    SET disconnected_at = ?
    WHERE agent_id = ?
  `).run(disconnectedAt, agentId);
}

function leaseExpired(lease, now) {
  return lease.expires_at <= now;
}

function assertLeaseDuration(leaseDurationMs, name = "leaseDurationMs") {
  if (!Number.isSafeInteger(leaseDurationMs) || leaseDurationMs <= 0) {
    throw new TypeError(`${name} must be a positive safe integer`);
  }
}

function assertMaxDeliveryAttempts(maxDeliveryAttempts) {
  if (!Number.isSafeInteger(maxDeliveryAttempts) || maxDeliveryAttempts <= 0) {
    throw new TypeError("maxDeliveryAttempts must be a positive safe integer");
  }
}

function assertPositiveLimit(value, name, maximum = Number.MAX_SAFE_INTEGER) {
  if (!Number.isSafeInteger(value) || value <= 0 || value > maximum) {
    throw new TypeError(`${name} must be a positive safe integer no greater than ${maximum}`);
  }
}

function assertNonNegativeLimit(value, name, maximum = Number.MAX_SAFE_INTEGER) {
  if (!Number.isSafeInteger(value) || value < 0 || value > maximum) {
    throw new TypeError(`${name} must be a non-negative safe integer no greater than ${maximum}`);
  }
}

function assertFailureReason(reason) {
  if (typeof reason !== "string" || !/^[a-z][a-z0-9_]*$/.test(reason) || reason.length > 64) {
    throw new TypeError("reason must be a safe failure code");
  }
}

function validateDeliveryReceipt({ agentId, instanceId, leaseId, messageId, attemptNumber }) {
  validateDeliveryIdentity({
    agent_id: agentId,
    instance_id: instanceId,
    lease_id: leaseId,
    message_id: messageId,
    attempt_number: attemptNumber,
  });
}

function clockTimestamp(clock) {
  const now = clock();
  if (!(now instanceof Date) || Number.isNaN(now.getTime())) {
    throw new TypeError("clock must return a valid Date");
  }
  return now.toISOString();
}

function timestampAfter(timestamp, milliseconds) {
  return new Date(Date.parse(timestamp) + milliseconds).toISOString();
}

function sameEnvelope(message, envelope) {
  return message.id === envelope.id
    && message.from === envelope.from
    && message.to === envelope.to
    && (message.subject ?? null) === envelope.subject
    && message.body === envelope.body
    && message.created_at === envelope.created_at
    && sameSentinelDescriptor(message.sentinel, envelope.sentinel);
}

function sameSentinelDescriptor(left, right) {
  return left === undefined
    ? right === undefined
    : right !== undefined
      && left.model === right.model;
}

function oneHourAfter(createdAt) {
  const milliseconds = Date.parse(createdAt);
  if (Number.isNaN(milliseconds)) {
    throw new TypeError("created_at must be a valid timestamp");
  }
  return new Date(milliseconds + ONE_HOUR_MS).toISOString();
}

function toMessage(row) {
  return {
    enqueue_sequence: row.enqueue_sequence,
    id: row.id,
    from: row.from,
    to: row.to,
    ...(row.subject === null ? {} : { subject: row.subject }),
    body: row.body,
    created_at: row.created_at,
    expires_at: row.expires_at,
    message_incarnation_id: row.message_incarnation_id,
    state: row.state,
    ...(row.sentinel_json === null ? {} : { sentinel: JSON.parse(row.sentinel_json) }),
    source: { kind: row.source_kind ?? "unknown" },
  };
}

function toPromptTemplate(row) {
  return {
    id: row.id,
    name: row.name,
    body: row.body,
    created_at: row.created_at,
    updated_at: row.updated_at,
  };
}

function toReminder(row) {
  return {
    id: row.id,
    target: row.target,
    creator: row.creator,
    ...(row.subject === null ? {} : { subject: row.subject }),
    body: row.body,
    created_at: row.created_at,
    next_due_at: row.next_due_at,
    interval_ms: row.interval_ms,
    ...(row.sentinel_json === null ? {} : { sentinel: JSON.parse(row.sentinel_json) }),
    state: row.state,
    linked_message_id: row.linked_message_id,
    linked_message_state: row.linked_message_state,
    occurrence_number: row.occurrence_number,
    attempt_count: row.attempt_count,
    last_attempt_at: row.last_attempt_at,
  };
}

function toServerJob(row) {
  return {
    job_key: row.job_key,
    schedule_interval_hours: row.schedule_interval_hours,
    schedule_anchor_hour: row.schedule_anchor_hour,
    schedule_minute: row.schedule_minute,
    schedule_time_zone: row.schedule_time_zone,
    definition_fingerprint: row.definition_fingerprint,
    next_due_at: row.next_due_at,
  };
}

function synchronousName(value) {
  if (value === 2) return "full";
  if (value === 1) return "normal";
  if (value === 0) return "off";
  return "extra";
}
