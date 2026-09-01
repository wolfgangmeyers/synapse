import { readFileSync } from "node:fs";

const DEFAULT_SESSION_CHECKPOINT_RETRY_MS = 250;
const DEFAULT_PENDING_REINJECTION_DELAY_MS = 5_000;
const MAILBOX_MESSAGE_TYPE = "mecha-agent-message";
const SENTINEL_STATUS_TYPE = "mecha-sentinel-status";

type JsonPrimitive = string | number | boolean | null;
type JsonValue = JsonPrimitive | JsonObject | JsonValue[] | undefined;
type JsonObject = { [key: string]: JsonValue };
type ErrorLike = Error | { code?: string; message?: string; name?: string; cause?: ErrorLike };
type ModelInput = { provider?: string; id?: string; name?: string; contextWindow?: number; maxTokens?: number };
type Timer = ReturnType<typeof setTimeout>;
type SessionFileReader = (file: string, encoding: BufferEncoding) => string;

type DeliveryItem = JsonObject & {
  messageId: string | undefined;
  messageIncarnationId: string | undefined;
  attemptNumber: number | undefined;
  deliveryExpiresAt: string | undefined;
};
type MailboxMessage = {
  id: string;
  from?: string;
  to?: string;
  body?: string;
  subject?: string;
  source?: { kind?: string };
  sentinel?: { model?: ModelInput };
};
type MailboxDelivery = { messageId: string; attemptNumber: number; items: DeliveryItem[] };
type MessageEndEvent = { message?: JsonObject };
type SessionEntry = JsonObject & { type?: string; customType?: string; details?: JsonObject };

type SessionManager = {
  getBranch: () => SessionEntry[];
  getSessionId: () => string;
  isPersisted: () => boolean;
  getSessionFile: () => string | undefined;
};
type MailboxUi = {
  setStatus: (key: string, value: string | undefined) => void;
  notify: (message: string, level?: string) => void;
};
type PiContext = { sessionManager: SessionManager; ui?: MailboxUi };
type MailboxPi = {
  sendMessage: (message: JsonObject, options?: { deliverAs?: string; triggerTurn?: boolean }) => unknown;
};
type MailboxClient = {
  sendReceipt: (type: string, delivery: MailboxDelivery, reason?: string) => unknown;
  sendMessageArrival?: (message: MailboxMessage) => unknown;
};
type PendingDelivery = {
  delivery: MailboxDelivery;
  checkpointTimer?: Timer;
  lastInjectedAt: number;
  lastInjectedAttemptNumber: number;
};
type NativeSubagentService = {
  spawn?: (type: string, prompt: string, options: { description: string; model?: ModelInput; inheritContext: boolean }) => Promise<string> | string;
};
type ServiceResolver = () => Promise<NativeSubagentService | undefined>;
type SpawnSubagent = (message: MailboxMessage) => Promise<string>;
type SentinelHandoff = {
  key: string;
  messageId: string;
  messageIncarnationId: string | undefined;
  delivery: MailboxDelivery;
  cleanupTimer?: Timer;
  subagentId: string;
  statusRecorded: boolean;
  promise?: Promise<void>;
};

export type DeliveryCoordinator = {
  onEnvelope: (message: MailboxMessage, delivery: MailboxDelivery) => void | Promise<void>;
  onMessageEnd: (event: MessageEndEvent, context: PiContext) => void;
  close: () => void;
};

export type DeliveryCoordinatorOptions = {
  pi: MailboxPi;
  sessionContext: PiContext;
  client: MailboxClient;
  defer?: typeof setImmediate;
  setTimeoutImpl?: typeof setTimeout;
  clearTimeoutImpl?: typeof clearTimeout;
  checkpointRetryDelayMs?: number;
  pendingReinjectionDelayMs?: number;
  readSessionFile?: SessionFileReader;
  spawnSubagent?: SpawnSubagent;
  now?: () => number;
};

function isRecord<T>(value: T): value is T & JsonObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function errorCode<T>(error: T): string | undefined {
  return isRecord(error) && typeof error.code === "string" ? error.code : undefined;
}

function errorMessage<T>(error: T): string {
  return error instanceof Error ? error.message : isRecord(error) && typeof error.message === "string" ? error.message : String(error);
}

function isDeliveryItem(value: JsonValue | undefined): value is DeliveryItem {
  return isRecord(value)
    && (value.messageId === undefined || typeof value.messageId === "string")
    && (value.messageIncarnationId === undefined || typeof value.messageIncarnationId === "string")
    && (value.attemptNumber === undefined || typeof value.attemptNumber === "number")
    && (value.deliveryExpiresAt === undefined || typeof value.deliveryExpiresAt === "string");
}

function sameDeliveryItem(left: DeliveryItem | undefined, right: DeliveryItem | undefined): boolean {
  return left?.messageId === right?.messageId
    && left?.messageIncarnationId === right?.messageIncarnationId
    && left?.attemptNumber === right?.attemptNumber
    && left?.deliveryExpiresAt === right?.deliveryExpiresAt;
}

function sameDeliveryIdentity(left: DeliveryItem | undefined, right: DeliveryItem | undefined): boolean {
  return left?.messageId === right?.messageId
    && left?.messageIncarnationId === right?.messageIncarnationId;
}

function legacyMessageIncarnationId(entry: SessionEntry): string | undefined {
  const detailsValue = entry.details?.message_incarnation_id;
  if (typeof detailsValue === "string") return detailsValue;
  const entryValue = entry.message_incarnation_id;
  return typeof entryValue === "string" ? entryValue : undefined;
}

function isMailboxSessionEntry(entry: SessionEntry, messageId: string, deliveryItems: DeliveryItem[] | undefined = undefined): boolean {
  if (entry?.type !== "custom_message"
    || entry.customType !== MAILBOX_MESSAGE_TYPE
    || entry.details?.message_id !== messageId) return false;
  if (deliveryItems === undefined) return true;
  if (deliveryItems.length === 0) return false;
  const persistedItems = entry.details?.delivery_items;
  if (Array.isArray(persistedItems)) {
    return persistedItems.length === deliveryItems.length
      && deliveryItems.every((item, index) => sameDeliveryItem(isDeliveryItem(persistedItems[index]) ? persistedItems[index] : undefined, item));
  }
  // Old single-message entries had no item array. Their ID alone is not enough:
  // an enqueue can reuse that ID after settlement, so require the incarnation
  // carried by the current delivery and the legacy entry.
  const item = deliveryItems.length === 1 ? deliveryItems[0] : undefined;
  return item?.messageId === messageId
    && typeof item.messageIncarnationId === "string"
    && legacyMessageIncarnationId(entry) === item.messageIncarnationId;
}

function readPersistedSessionEntries(sessionFile: string, readSessionFile: SessionFileReader): SessionEntry[] {
  const source = readSessionFile(sessionFile, "utf8");
  return source.split("\n")
    .filter((line) => line.length > 0)
    .map((line) => JSON.parse(line));
}

function isRetryOfPersistedDelivery(entry: SessionEntry, messageId: string, deliveryItems: DeliveryItem[]): boolean {
  if (!isMailboxSessionEntry(entry, messageId)) return false;
  const persistedItems = entry.details?.delivery_items;
  if (persistedItems === undefined) {
    const item = deliveryItems.length === 1 ? deliveryItems[0] : undefined;
    return item?.messageId === messageId
      && typeof item.messageIncarnationId === "string"
      && legacyMessageIncarnationId(entry) === item.messageIncarnationId;
  }
  return Array.isArray(persistedItems)
    && persistedItems.length === deliveryItems.length
    && deliveryItems.every((item, index) => sameDeliveryIdentity(
      isDeliveryItem(persistedItems[index]) ? persistedItems[index] : undefined,
      item,
    ));
}

export function mailboxSessionDeliveryState(sessionManager: SessionManager, messageId: string, {
  deliveryItems = undefined,
  readSessionFile = readFileSync,
  onPersistenceError = () => {},
}: {
  deliveryItems?: DeliveryItem[];
  readSessionFile?: SessionFileReader;
  onPersistenceError?: (error: ErrorLike) => void;
} = {}): "absent" | "history_unverified" | "durable" {
  const branch = sessionManager.getBranch();
  const hasBranchMessage = branch.some((entry) => isMailboxSessionEntry(entry, messageId));
  if (!hasBranchMessage) return "absent";
  if (!branch.some((entry) => isMailboxSessionEntry(entry, messageId, deliveryItems))) return "history_unverified";
  if (!sessionManager.isPersisted()) return "history_unverified";
  const sessionFile = sessionManager.getSessionFile();
  if (typeof sessionFile !== "string" || sessionFile.length === 0) return "history_unverified";
  try {
    return readPersistedSessionEntries(sessionFile, readSessionFile)
      .some((entry) => isMailboxSessionEntry(entry, messageId, deliveryItems))
      ? "durable"
      : "history_unverified";
  } catch (error) {
    if (errorCode(error) !== "ENOENT") onPersistenceError(error instanceof Error ? error : new Error(errorMessage(error)));
    return "history_unverified";
  }
}

function mailboxCustomMessage(message: MailboxMessage, delivery: MailboxDelivery | undefined = undefined): JsonObject {
  const subject = message.subject === undefined ? "" : `\nSubject: ${message.subject}`;
  return {
    customType: MAILBOX_MESSAGE_TYPE,
    content: `Mailbox message from ${message.from}${subject}\n\n${message.body}`,
    display: true,
    details: {
      message_id: message.id,
      ...(Array.isArray(delivery?.items) && delivery.items.length > 0 ? { delivery_items: delivery.items } : {}),
      ...(delivery?.items?.length === 1 && typeof delivery.items[0]?.messageIncarnationId === "string"
        ? { message_incarnation_id: delivery.items[0].messageIncarnationId } : {}),
      from: message.from,
      to: message.to,
      ...(message.subject === undefined ? {} : { subject: message.subject }),
    },
  };
}

export function routeMailboxEnvelope(envelope: MailboxMessage, inject: (envelope: MailboxMessage) => void): void {
  inject(envelope);
}

export async function spawnNativeSentinel(message: MailboxMessage, resolveService?: ServiceResolver): Promise<string> {
  const service = await resolveService?.();
  if (service === undefined || typeof service.spawn !== "function" || typeof message.body !== "string" || message.sentinel === undefined) {
    throw new Error("Pi native subagent service is unavailable");
  }
  return service.spawn("general-purpose", message.body, {
    description: "Scheduled Sentinel work",
    model: message.sentinel.model,
    inheritContext: true,
  });
}

export function captureNativeSentinelSpawner(resolveService: ServiceResolver): SpawnSubagent {
  return (message) => spawnNativeSentinel(message, resolveService);
}

/**
 * Coordinates the durable boundary between a router lease and Pi's session.
 * The router receives an injection receipt after Pi accepts the message, but ACK
 * waits for the matching custom entry to be present in the persisted session file.
 */
export function createPiMailboxDeliveryCoordinator({
  pi,
  sessionContext,
  client,
  defer = setImmediate,
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
  checkpointRetryDelayMs = DEFAULT_SESSION_CHECKPOINT_RETRY_MS,
  pendingReinjectionDelayMs = DEFAULT_PENDING_REINJECTION_DELAY_MS,
  readSessionFile = readFileSync,
  spawnSubagent = spawnNativeSentinel,
  now = Date.now,
}: DeliveryCoordinatorOptions): DeliveryCoordinator {
  if (pi === undefined || typeof pi.sendMessage !== "function") {
    throw new TypeError("pi.sendMessage is required");
  }
  if (sessionContext?.sessionManager === undefined) {
    throw new TypeError("a Pi session context is required");
  }
  if (client === undefined || typeof client.sendReceipt !== "function") {
    throw new TypeError("a mailbox client with sendReceipt is required");
  }
  if (typeof defer !== "function") throw new TypeError("defer must be a function");
  if (typeof setTimeoutImpl !== "function" || typeof clearTimeoutImpl !== "function") {
    throw new TypeError("session checkpoint timers are required");
  }
  if (!Number.isSafeInteger(checkpointRetryDelayMs) || checkpointRetryDelayMs <= 0) {
    throw new TypeError("checkpointRetryDelayMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(pendingReinjectionDelayMs) || pendingReinjectionDelayMs <= 0) {
    throw new TypeError("pendingReinjectionDelayMs must be a positive safe integer");
  }
  if (typeof readSessionFile !== "function") throw new TypeError("readSessionFile must be a function");
  if (typeof now !== "function") throw new TypeError("now must be a function");
  if (typeof spawnSubagent !== "function") throw new TypeError("spawnSubagent must be a function");

  const sessionId = sessionContext.sessionManager.getSessionId();
  const pending = new Map<string, PendingDelivery>();
  const announcedArrivals = new Set<string>();
  // Keep a successful handoff until the router's retry can be acknowledged and
  // the ownership marker is durable. The key includes the message incarnation
  // so a later enqueue that reuses an ID can still create a new child.
  const sentinelHandoffs = new Map<string, SentinelHandoff>();
  const reportedPersistenceErrors = new Set<string>();
  let active = true;

  function receipt(type: string, delivery: MailboxDelivery, reason: string | undefined = undefined): void {
    client.sendReceipt(type, delivery, reason);
  }

  function persistenceUnavailable(context: PiContext): void {
    context.ui?.setStatus("mecha-mailbox", "mailbox: waiting for durable session checkpoint");
  }

  function sessionDeliveryState(context: PiContext, messageId: string, deliveryItems: DeliveryItem[] | undefined = undefined) {
    return mailboxSessionDeliveryState(context.sessionManager, messageId, {
      deliveryItems,
      readSessionFile,
      onPersistenceError: (error) => {
        if (reportedPersistenceErrors.has(messageId)) return;
        reportedPersistenceErrors.add(messageId);
        context.ui?.notify(`Mailbox v2 session checkpoint unreadable: ${errorMessage(error)}`, "error");
      },
    });
  }

  function acknowledgeKnownDelivery(delivery: MailboxDelivery, context: PiContext): boolean {
    try {
      receipt("injection_requested", delivery);
      receipt("ack", delivery);
      context.ui?.setStatus("mecha-mailbox", "mailbox: connected");
      return true;
    } catch {
      // The persisted session entry makes the next router replay safe to acknowledge.
      return false;
    }
  }

  function hasDurableRetryEntry(context: PiContext, delivery: MailboxDelivery): boolean {
    const manager = context.sessionManager;
    if (!manager.getBranch().some((entry) => isMailboxSessionEntry(entry, delivery.messageId))) return false;
    if (!manager.isPersisted()) return false;
    const sessionFile = manager.getSessionFile();
    if (typeof sessionFile !== "string" || sessionFile.length === 0) return false;
    try {
      return readPersistedSessionEntries(sessionFile, readSessionFile)
        .some((entry) => isRetryOfPersistedDelivery(entry, delivery.messageId, delivery.items));
    } catch (error) {
      if (errorCode(error) !== "ENOENT") {
        const reported = reportedPersistenceErrors.has(delivery.messageId);
        if (!reported) {
          reportedPersistenceErrors.add(delivery.messageId);
          context.ui?.notify(`Mailbox v2 session checkpoint unreadable: ${errorMessage(error)}`, "error");
        }
      }
      return false;
    }
  }

  function clearCheckpointTimer(pendingDelivery: PendingDelivery): void {
    if (pendingDelivery.checkpointTimer !== undefined) {
      clearTimeoutImpl(pendingDelivery.checkpointTimer);
      pendingDelivery.checkpointTimer = undefined;
    }
  }

  function checkDurableCheckpoint(messageId: string, context: PiContext): void {
    if (!active || context.sessionManager.getSessionId() !== sessionId) return;
    const latest = pending.get(messageId);
    if (latest === undefined) return;
    const state = sessionDeliveryState(context, messageId, latest.delivery.items);
    const retryEntryKnown = state === "history_unverified" && hasDurableRetryEntry(context, latest.delivery);
    if (state === "durable" || retryEntryKnown) {
      clearCheckpointTimer(latest);
      pending.delete(messageId);
      reportedPersistenceErrors.delete(messageId);
      try {
        receipt("ack", latest.delivery);
        context.ui?.setStatus("mecha-mailbox", "mailbox: connected");
      } catch {
        // The persisted session entry makes the next router replay safe to acknowledge.
      }
      return;
    }
    persistenceUnavailable(context);
    if (latest.checkpointTimer !== undefined) return;
    // A checkpoint can lag behind Pi's message_end event. Keep the lease pending
    // until the file catches up, so a transient write failure remains recoverable.
    latest.checkpointTimer = setTimeoutImpl(() => {
      latest.checkpointTimer = undefined;
      checkDurableCheckpoint(messageId, context);
    }, checkpointRetryDelayMs);
  }

  function maybeSendMessageArrival(message: MailboxMessage): void {
    if (announcedArrivals.has(message.id)) return;
    const sourceKind = message?.source?.kind;
    if (message.from === message.to || typeof sourceKind !== "string" || !["agent", "discord", "reminder"].includes(sourceKind)) return;
    if (typeof client.sendMessageArrival !== "function") return;
    client.sendMessageArrival(message);
    announcedArrivals.add(message.id);
  }

  function injectMailboxMessage(message: MailboxMessage, delivery: MailboxDelivery): void {
    routeMailboxEnvelope(message, (envelope) => {
      pi.sendMessage(mailboxCustomMessage(envelope, delivery), { deliverAs: "steer", triggerTurn: true });
    });
  }

  function shouldReinjectPending(pendingDelivery: PendingDelivery, delivery: MailboxDelivery, state: "absent" | "history_unverified" | "durable"): boolean {
    // Replays are delayed to give the original Pi append and checkpoint a chance
    // to finish. History that is present but unverifiable is never duplicated.
    return state === "absent"
      && delivery.attemptNumber > pendingDelivery.lastInjectedAttemptNumber
      && now() - pendingDelivery.lastInjectedAt >= pendingReinjectionDelayMs;
  }

  function deliveryIncarnationId(messageId: string, delivery: MailboxDelivery): string | undefined {
    const items = Array.isArray(delivery?.items) ? delivery.items : [];
    const item = items.find((candidate) => candidate.messageId === messageId)
      ?? (items.length === 1 ? items[0] : undefined);
    return typeof item?.messageIncarnationId === "string" ? item.messageIncarnationId : undefined;
  }

  function sentinelDeliveryKey(messageId: string, delivery: MailboxDelivery): string {
    return `${messageId}\u0000${deliveryIncarnationId(messageId, delivery) ?? ""}`;
  }

  function isSentinelStatusEntry(entry: SessionEntry, messageId: string, messageIncarnationId: string | undefined): boolean {
    if (entry?.type !== "custom_message" || entry.customType !== SENTINEL_STATUS_TYPE) return false;
    const details = entry.details;
    if (details?.message_id !== messageId || details.state !== "handed_off" || typeof details.subagent_id !== "string") return false;
    const persistedIncarnation = typeof details.message_incarnation_id === "string" ? details.message_incarnation_id : undefined;
    return persistedIncarnation === messageIncarnationId;
  }

  function sentinelOwnershipDurable(messageId: string, delivery: MailboxDelivery): boolean {
    const manager = sessionContext.sessionManager;
    if (typeof manager.getBranch !== "function" || typeof manager.isPersisted !== "function"
      || typeof manager.getSessionFile !== "function") return false;
    const messageIncarnationId = deliveryIncarnationId(messageId, delivery);
    if (!manager.getBranch().some((entry) => isSentinelStatusEntry(entry, messageId, messageIncarnationId))) return false;
    if (!manager.isPersisted()) return false;
    const sessionFile = manager.getSessionFile();
    if (typeof sessionFile !== "string" || sessionFile.length === 0) return false;
    try {
      return readPersistedSessionEntries(sessionFile, readSessionFile)
        .some((entry) => isSentinelStatusEntry(entry, messageId, messageIncarnationId));
    } catch {
      // Ownership is only confirmed when the session file can be read and matches.
      return false;
    }
  }

  function recordSentinelStatus(message: MailboxMessage, delivery: MailboxDelivery, subagentId: string): boolean {
    // The receipt identifies the handoff. This hidden status deliberately carries
    // neither the scheduled prompt nor derived text from the native child.
    const messageIncarnationId = deliveryIncarnationId(message.id, delivery);
    try {
      pi.sendMessage({
        customType: SENTINEL_STATUS_TYPE,
        content: "Sentinel work handed off to a native subagent.",
        display: false,
        details: {
          message_id: message.id,
          ...(messageIncarnationId === undefined ? {} : { message_incarnation_id: messageIncarnationId }),
          subagent_id: subagentId,
          state: "handed_off",
        },
      }, { deliverAs: "followUp", triggerTurn: false });
      return true;
    } catch {
      return false;
    }
  }

  function clearSentinelCleanupTimer(record: SentinelHandoff): void {
    if (record.cleanupTimer !== undefined) {
      clearTimeoutImpl(record.cleanupTimer);
      record.cleanupTimer = undefined;
    }
  }

  function forgetSentinelHandoff(record: SentinelHandoff): void {
    clearSentinelCleanupTimer(record);
    if (sentinelHandoffs.get(record.key) === record) sentinelHandoffs.delete(record.key);
  }

  function scheduleSentinelCleanup(record: SentinelHandoff): void {
    if (!active || sentinelHandoffs.get(record.key) !== record || record.cleanupTimer !== undefined) return;
    let timer: Timer;
    timer = setTimeoutImpl(() => {
      if (record.cleanupTimer === timer) record.cleanupTimer = undefined;
      if (!active || sentinelHandoffs.get(record.key) !== record) return;
      if (sentinelOwnershipDurable(record.messageId, record.delivery)) {
        forgetSentinelHandoff(record);
        return;
      }
      scheduleSentinelCleanup(record);
    }, checkpointRetryDelayMs);
    record.cleanupTimer = timer;
    timer?.unref?.();
  }

  function maybeForgetSentinelHandoff(record: SentinelHandoff): void {
    // Keep the fence while the ownership marker is still in flight. A lost ACK
    // can replay before that marker is durable, so cleanup must wait for proof.
    if (sentinelOwnershipDurable(record.messageId, record.delivery)) {
      forgetSentinelHandoff(record);
      return;
    }
    scheduleSentinelCleanup(record);
  }

  function checkSentinelHandoff(messageId: string, messageIncarnationId: string | undefined): void {
    const record = [...sentinelHandoffs.values()].find((candidate) => candidate.messageId === messageId
      && candidate.messageIncarnationId === messageIncarnationId);
    if (record !== undefined) maybeForgetSentinelHandoff(record);
  }

  async function acknowledgeSentinelHandoff(record: SentinelHandoff, message: MailboxMessage, delivery: MailboxDelivery): Promise<void> {
    if (!active) return;
    if (!record.statusRecorded) record.statusRecorded = recordSentinelStatus(message, delivery, record.subagentId);
    if (!active) return;
    try {
      receipt("injection_requested", delivery);
      if (!active) return;
      receipt("ack", delivery);
    } catch {
      // The native child already exists. Keep the handoff for the router retry.
    }
    maybeForgetSentinelHandoff(record);
  }

  async function onSentinelEnvelope(message: MailboxMessage, delivery: MailboxDelivery): Promise<void> {
    const key = sentinelDeliveryKey(message.id, delivery);
    const existing = sentinelHandoffs.get(key);
    if (existing !== undefined) {
      existing.delivery = delivery;
      if (existing.promise !== undefined) return existing.promise;
      return acknowledgeSentinelHandoff(existing, message, delivery);
    }
    // A coordinator can be recreated after the first receipt was lost. A durable
    // status entry proves that this exact incarnation already owns a child.
    if (sentinelOwnershipDurable(message.id, delivery)) {
      try {
        receipt("injection_requested", delivery);
        receipt("ack", delivery);
      } catch {
        // The durable status makes a future retry safe even when this receipt is lost.
      }
      return;
    }

    const handoff: SentinelHandoff = {
      key,
      messageId: message.id,
      messageIncarnationId: deliveryIncarnationId(message.id, delivery),
      delivery,
      subagentId: "",
      statusRecorded: false,
    };
    // Register before awaiting spawn so concurrent deliveries of this exact
    // incarnation share one native child. A different incarnation gets a new key.
    sentinelHandoffs.set(key, handoff);
    let promise: Promise<void>;
    promise = (async () => {
      let subagentId: string;
      try {
        subagentId = await spawnSubagent(message);
      } catch {
        sentinelHandoffs.delete(key);
        if (!active) return;
        try { receipt("nack", delivery, "injection_failed"); } catch { /* normal delivery timeout preserves replay */ }
        return;
      }
      if (!active) return;
      if (typeof subagentId !== "string" || subagentId.length === 0) {
        sentinelHandoffs.delete(key);
        try { receipt("nack", delivery, "injection_failed"); } catch { /* normal delivery timeout preserves replay */ }
        return;
      }
      handoff.subagentId = subagentId;
      handoff.statusRecorded = recordSentinelStatus(message, delivery, subagentId);
      await acknowledgeSentinelHandoff(handoff, message, delivery);
    })();
    handoff.promise = promise;
    await promise;
    handoff.promise = undefined;
    maybeForgetSentinelHandoff(handoff);
  }

  function onEnvelope(message: MailboxMessage, delivery: MailboxDelivery): void | Promise<void> {
    if (!active) return undefined;
    if (message.sentinel !== undefined) return onSentinelEnvelope(message, delivery);
    const currentContext = sessionContext;
    const state = sessionDeliveryState(currentContext, message.id, delivery.items);
    const retryEntryKnown = state === "history_unverified" && hasDurableRetryEntry(currentContext, delivery);
    if (state === "durable" || retryEntryKnown) {
      const existing = pending.get(message.id);
      if (acknowledgeKnownDelivery(delivery, currentContext)) {
        if (existing !== undefined) {
          clearCheckpointTimer(existing);
          pending.delete(message.id);
        }
      }
      return;
    }

    const existing = pending.get(message.id);
    if (existing !== undefined) {
      existing.delivery = delivery;
      if (shouldReinjectPending(existing, delivery, state)) {
        try {
          injectMailboxMessage(message, delivery);
          existing.lastInjectedAt = now();
          existing.lastInjectedAttemptNumber = delivery.attemptNumber;
        } catch {
          // Keep the original pending delivery; the router lease timeout preserves retry.
        }
      }
      try {
        receipt("injection_requested", delivery);
      } catch {
        // The original Pi injection remains pending; do not NACK it as a failed injection.
      }
      return;
    }
    if (state === "history_unverified") {
      persistenceUnavailable(currentContext);
      return;
    }

    pending.set(message.id, {
      delivery,
      checkpointTimer: undefined,
      lastInjectedAt: now(),
      lastInjectedAttemptNumber: delivery.attemptNumber,
    });
    try {
      injectMailboxMessage(message, delivery);
    } catch {
      pending.delete(message.id);
      try {
        receipt("nack", delivery, "injection_failed");
      } catch {
        // The router's lease timeout preserves retry when the socket is gone.
      }
      return;
    }
    try {
      maybeSendMessageArrival(message);
    } catch {
      // Arrival indicators are ephemeral UI hints; delivery receipts own replay safety.
    }
    // Keep this pending on a receipt-send failure: Pi may have queued the message already.
    try {
      receipt("injection_requested", delivery);
    } catch {
      // The router lease will time out or the reconnecting client will receive a replay.
    }
  }

  function onMessageEnd(event: MessageEndEvent, context: PiContext): void {
    const details = isRecord(event.message?.details) ? event.message.details : undefined;
    const messageId = typeof details?.message_id === "string" ? details.message_id : undefined;
    if (event.message?.role !== "custom" || typeof messageId !== "string") return;
    // Pi can deliver a late event from another session. The fence prevents it
    // from acknowledging state owned by this coordinator.
    if (context.sessionManager.getSessionId() !== sessionId) return;
    if (event.message.customType === SENTINEL_STATUS_TYPE) {
      // The status event can precede the session-file append. Check after the
      // event returns so ownership is tested against the durable entry.
      const messageIncarnationId = typeof details?.message_incarnation_id === "string" ? details.message_incarnation_id : undefined;
      defer(() => checkSentinelHandoff(messageId, messageIncarnationId));
      return;
    }
    if (event.message.customType !== MAILBOX_MESSAGE_TYPE) return;
    const pendingDelivery = pending.get(messageId);
    if (pendingDelivery === undefined) return;

    defer(() => checkDurableCheckpoint(messageId, context));
  }

  return {
    onEnvelope,
    onMessageEnd,

    close() {
      // Shutdown owns timer cancellation. Deferred callbacks may still run, but
      // active=false makes them harmless and clears all retained delivery state.
      active = false;
      for (const pendingDelivery of pending.values()) clearCheckpointTimer(pendingDelivery);
      for (const handoff of sentinelHandoffs.values()) clearSentinelCleanupTimer(handoff);
      pending.clear();
      announcedArrivals.clear();
      sentinelHandoffs.clear();
      reportedPersistenceErrors.clear();
    },
  };
}
