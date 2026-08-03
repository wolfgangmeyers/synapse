import { randomUUID } from "node:crypto";
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
export const DEFAULT_CONVERSATION_HISTORY_LIMIT = 200;
export const DEFAULT_CONVERSATION_RETENTION_MS = 30 * 24 * 60 * 60 * 1_000;
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

export function openMessageRepository({
  databasePath,
  clock = () => new Date(),
  busyTimeoutMs = DEFAULT_BUSY_TIMEOUT_MS,
  maxBodyBytes = DEFAULT_MAX_BODY_BYTES,
  maxQueuedMessages = DEFAULT_MAX_QUEUED_MESSAGES,
  maxQueuedBytes = DEFAULT_MAX_QUEUED_BYTES,
  maxBatchBodyChars = DEFAULT_MAX_BATCH_BODY_CHARS,
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

  constructor(database, clock, databasePath, { maxBodyBytes, maxQueuedMessages, maxQueuedBytes, maxBatchBodyChars }) {
    this.#database = database;
    this.#clock = clock;
    this.#databasePath = databasePath;
    this.#maxBodyBytes = maxBodyBytes;
    this.#maxQueuedMessages = maxQueuedMessages;
    this.#maxQueuedBytes = maxQueuedBytes;
    this.#maxBatchBodyChars = maxBatchBodyChars;
  }

  close() {
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
        const deliveryMessage = selectMessage(this.#database, existingConversation.delivery_message_id);
        if (deliveryMessage !== undefined) {
          if (!sameConversationCandidate(existingConversation, conversationCandidate)) {
            throw new IdempotencyConflictError(candidate.id);
          }
          return {
            created: false,
            message: deliveryMessage,
            conversation_message: toConversationMessage(existingConversation),
          };
        }
      }
      const result = enqueueCandidate(this.#database, candidate, {
        firstSeenAt: clockTimestamp(this.#clock),
        maxBodyBytes: this.#maxBodyBytes,
        maxQueuedMessages: this.#maxQueuedMessages,
        maxQueuedBytes: this.#maxQueuedBytes,
        maxBatchBodyChars: disableBatching ? 0 : this.#maxBatchBodyChars,
      });
      if (result.created || result.batched) {
        const messageIncarnationId = result.message.id === candidate.id
          ? result.message.message_incarnation_id
          : randomUUID();
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
      return { created: true, reminder: selectReminder(this.#database, candidate.id) };
    });
  }

  reminderFor(reminderId) {
    return selectReminder(this.#database, reminderId);
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
      const result = this.#database.prepare(`
        DELETE FROM reminders
        WHERE state = 'failed' OR (state = 'scheduled' AND linked_message_id IS NULL)
      `).run();
      return { canceled: result.changes };
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
      if (selectInFlightDelivery(this.#database, agentId) !== undefined) return undefined;

      const message = selectOldestQueuedMessage(this.#database, agentId, now);
      if (message === undefined) return undefined;
      if (!MESSAGE_INCARNATION_ID.test(message.message_incarnation_id ?? "")) {
        this.#database.prepare(`
          UPDATE messages
          SET message_incarnation_id = ?
          WHERE id = ? AND state = 'queued'
        `).run(randomUUID(), message.id);
      }
      const attemptNumber = nextAttemptNumber(this.#database, message.id);
      this.#database.prepare(`
        UPDATE messages
        SET state = 'leased', claimed_at = COALESCE(claimed_at, ?)
        WHERE id = ? AND state = 'queued'
      `).run(now, message.id);
      this.#database.prepare(`
        INSERT INTO delivery_attempts (
          message_id,
          attempt_number,
          lease_id,
          owner_lease_id,
          state,
          created_at,
          updated_at,
          delivery_expires_at
        ) VALUES (?, ?, ?, ?, 'leased', ?, ?, ?)
      `).run(
        message.id,
        attemptNumber,
        leaseId,
        leaseId,
        now,
        now,
        deliveryExpiresAt,
      );

      return {
        message: selectMessage(this.#database, message.id),
        attempt_number: attemptNumber,
        delivery_expires_at: deliveryExpiresAt,
      };
    });
  }

  recordInjectionRequested({
    agentId,
    instanceId,
    leaseId,
    messageId,
    attemptNumber,
    maxDeliveryAttempts = DEFAULT_MAX_DELIVERY_ATTEMPTS,
  }) {
    validateDeliveryReceipt({ agentId, instanceId, leaseId, messageId, attemptNumber });
    assertMaxDeliveryAttempts(maxDeliveryAttempts);
    const now = clockTimestamp(this.#clock);

    const result = immediateTransaction(this.#database, this.#databasePath, () => {
      requireActiveAgentLease(this.#database, { agentId, instanceId, leaseId }, now);
      const delivery = requireActiveDelivery(
        this.#database,
        { agentId, leaseId, messageId, attemptNumber },
      );
      if (expireDeliveryAtDeadline(this.#database, delivery, now, maxDeliveryAttempts)) {
        return { timed_out: true };
      }
      if (delivery.attempt_state === "injection_requested") {
        return { message: selectMessage(this.#database, messageId), attempt_number: attemptNumber };
      }
      if (delivery.attempt_state !== "leased" || delivery.message_state !== "leased") {
        throw new DeliveryNotActiveError(messageId, attemptNumber);
      }

      this.#database.prepare(`
        UPDATE delivery_attempts
        SET state = 'injection_requested', updated_at = ?
        WHERE message_id = ? AND attempt_number = ? AND state = 'leased'
      `).run(now, messageId, attemptNumber);
      this.#database.prepare(`
        UPDATE messages
        SET state = 'injection_requested'
        WHERE id = ? AND state = 'leased'
      `).run(messageId);
      return { message: selectMessage(this.#database, messageId), attempt_number: attemptNumber };
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
    maxDeliveryAttempts = DEFAULT_MAX_DELIVERY_ATTEMPTS,
  }) {
    validateDeliveryReceipt({ agentId, instanceId, leaseId, messageId, attemptNumber });
    assertMaxDeliveryAttempts(maxDeliveryAttempts);
    const now = clockTimestamp(this.#clock);

    const result = immediateTransaction(this.#database, this.#databasePath, () => {
      requireActiveAgentLease(this.#database, { agentId, instanceId, leaseId }, now);
      const delivery = requireActiveDelivery(
        this.#database,
        { agentId, leaseId, messageId, attemptNumber },
      );
      if (expireDeliveryAtDeadline(this.#database, delivery, now, maxDeliveryAttempts)) {
        return { timed_out: true };
      }
      if (delivery.attempt_state !== "injection_requested"
        || delivery.message_state !== "injection_requested") {
        throw new DeliveryNotActiveError(messageId, attemptNumber);
      }
      this.#database.prepare(`
        UPDATE messages
        SET state = 'acknowledged'
        WHERE id = ?
      `).run(messageId);
      completeLinkedReminder(this.#database, messageId);
      this.#database.prepare("DELETE FROM messages WHERE id = ?").run(messageId);
      return {
        message_id: messageId,
        message_incarnation_id: delivery.message_incarnation_id,
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
    reason,
    maxDeliveryAttempts = DEFAULT_MAX_DELIVERY_ATTEMPTS,
  }) {
    validateDeliveryReceipt({ agentId, instanceId, leaseId, messageId, attemptNumber });
    assertFailureReason(reason);
    assertMaxDeliveryAttempts(maxDeliveryAttempts);
    const now = clockTimestamp(this.#clock);

    const result = immediateTransaction(this.#database, this.#databasePath, () => {
      requireActiveAgentLease(this.#database, { agentId, instanceId, leaseId }, now);
      const delivery = requireActiveDelivery(
        this.#database,
        { agentId, leaseId, messageId, attemptNumber },
      );
      if (expireDeliveryAtDeadline(this.#database, delivery, now, maxDeliveryAttempts)) {
        return { timed_out: true };
      }
      if (!(["leased", "injection_requested"].includes(delivery.attempt_state)
        && ["leased", "injection_requested"].includes(delivery.message_state))) {
        throw new DeliveryNotActiveError(messageId, attemptNumber);
      }
      return failDeliveryAttempt(
        this.#database,
        delivery,
        { attemptState: "nacked", reason, now, maxDeliveryAttempts },
      );
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
        completeLinkedReminder(this.#database, id);
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
      this.#database.prepare(`
        DELETE FROM reminders
        WHERE linked_message_id = ? AND state = 'failed'
      `).run(messageId);
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
        attachments_json
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
        sentinel_json
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
      sentinel_json
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

function batchedBody(target, candidate) {
  const existing = isBatchedBody(target.body)
    ? target.body
    : `${BATCH_HEADER}\n\n${batchItem(target)}`;
  return `${existing}${BATCH_SEPARATOR}${batchItem(candidate)}`;
}

function selectBatchTarget(database, candidate, { maxBatchBodyChars, maxBodyBytes, maxQueuedBytes, queuedBytes }) {
  if (maxBatchBodyChars === 0 || candidate.sentinel !== undefined) return undefined;
  const rows = database.prepare(`
    SELECT
      id,
      sender AS "from",
      subject,
      body
    FROM messages
    WHERE recipient = ?
      AND state = 'queued'
      AND sentinel_json IS NULL
    ORDER BY enqueue_sequence DESC
  `).all(candidate.to);
  return rows.find((row) => {
    const body = batchedBody(row, candidate);
    const previousBytes = Buffer.byteLength(row.body, "utf8");
    const nextBytes = Buffer.byteLength(body, "utf8");
    return body.length <= maxBatchBodyChars
      && nextBytes <= maxBodyBytes
      && queuedBytes + nextBytes - previousBytes <= maxQueuedBytes;
  });
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
  return { ...candidate, body, attachments };
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
      attachments_json
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
      attachments_json
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
    && JSON.stringify(conversationAttachments(row.attachments_json)) === JSON.stringify(candidate.attachments);
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
      attachments_json
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
  { firstSeenAt, maxBodyBytes, maxQueuedMessages, maxQueuedBytes, maxBatchBodyChars },
) {
  const existing = selectMessage(database, candidate.id);
  if (existing !== undefined) {
    if (!sameEnvelope(existing, candidate)) {
      throw new IdempotencyConflictError(candidate.id);
    }
    return { created: false, message: existing };
  }

  const usage = selectLiveQueueUsage(database, candidate.to);
  const batchTarget = selectBatchTarget(database, candidate, {
    maxBatchBodyChars,
    maxBodyBytes,
    maxQueuedBytes,
    queuedBytes: usage.queued_bytes,
  });
  if (batchTarget !== undefined) {
    database.prepare(`
      UPDATE messages
      SET body = ?
      WHERE id = ? AND state = 'queued'
    `).run(batchedBody(batchTarget, candidate), batchTarget.id);
    return { created: false, batched: true, message: selectMessage(database, batchTarget.id) };
  }

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
      id, sender, recipient, subject, body, created_at, expires_at, message_incarnation_id, sentinel_json, state
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued')
  `).run(
    candidate.id,
    candidate.from,
    candidate.to,
    candidate.subject,
    candidate.body,
    candidate.created_at,
    oneHourAfter(firstSeenAt),
    randomUUID(),
    candidate.sentinel_json,
  );
  return {
    created: true,
    message: selectMessage(database, candidate.id),
  };
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

function fireDueOneShotReminder(database, reminder, now, limits) {
  limits.onAttempt();
  const message = enqueueCandidate(
    database,
    prepareEnvelope(validateEnvelope({
      id: reminderMessageId(reminder.id, reminder.occurrence_number),
      from: "reminders",
      to: reminder.target,
      ...(reminder.subject === undefined ? {} : { subject: reminder.subject }),
      ...(reminder.sentinel === undefined ? {} : { sentinel: reminder.sentinel }),
      body: reminder.body,
      created_at: now,
    }, { maxBodyBytes: limits.maxBodyBytes })),
    {
      firstSeenAt: now,
      maxQueuedMessages: limits.maxQueuedMessages,
      maxQueuedBytes: limits.maxQueuedBytes,
    },
  );
  if (!message.created) {
    throw new IdempotencyConflictError(message.message.id);
  }
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
  const message = enqueueCandidate(
    database,
    prepareEnvelope(validateEnvelope({
      id: reminderMessageId(reminder.id, reminder.occurrence_number),
      from: "reminders",
      to: reminder.target,
      ...(reminder.subject === undefined ? {} : { subject: reminder.subject }),
      ...(reminder.sentinel === undefined ? {} : { sentinel: reminder.sentinel }),
      body: reminder.body,
      created_at: now,
    }, { maxBodyBytes: limits.maxBodyBytes })),
    {
      firstSeenAt: now,
      maxQueuedMessages: limits.maxQueuedMessages,
      maxQueuedBytes: limits.maxQueuedBytes,
    },
  );
  if (!message.created) throw new IdempotencyConflictError(message.message.id);
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
  };
}

function completeLinkedReminder(database, messageId) {
  database.prepare(`
    DELETE FROM reminders
    WHERE linked_message_id = ?
      AND interval_ms IS NULL
      AND state = 'message_queued'
  `).run(messageId);
  database.prepare(`
    UPDATE reminders
    SET state = 'scheduled', linked_message_id = NULL
    WHERE linked_message_id = ?
      AND interval_ms IS NOT NULL
      AND state IN ('scheduled', 'message_queued')
  `).run(messageId);
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
    },
  );
  return true;
}

function failDeliveryAttempt(
  database,
  delivery,
  { attemptState, reason, now, maxDeliveryAttempts },
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
    SET state = ?
    WHERE id = ? AND state = ?
  `).run(state, delivery.message_id, delivery.message_state);
  let recurringReminderRearmed;
  if (state === "dead_letter") {
    recurringReminderRearmed = rearmRecurringReminderAfterDeadLetter(database, delivery.message_id, now);
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

function rearmRecurringReminderAfterDeadLetter(database, messageId, now) {
  const reminder = database.prepare(`
    SELECT id, next_due_at, interval_ms
    FROM reminders
    WHERE linked_message_id = ?
      AND interval_ms IS NOT NULL
      AND state = 'scheduled'
  `).get(messageId);
  if (reminder === undefined) return undefined;
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
  if (updated.changes !== 1) return undefined;
  return { reminder_id: reminder.id, next_due_at: nextDueAt };
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
