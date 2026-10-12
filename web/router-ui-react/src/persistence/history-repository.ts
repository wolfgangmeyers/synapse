import {
  HISTORY_STORE_NAME,
  IMAGE_STORE_NAME,
  OWNER_SCOPE_INDEX,
  PEER_SCOPE_INDEX,
  getAllByIndex,
  getAllKeysByIndex,
  openHistoryDb,
  requestResult,
  transactionDone,
  type IndexedDbFactory,
} from "./indexed-db";
import {
  messageForStorage,
  messageKey,
  messageTime,
  normalizeThumbnailRecord,
  pruneHistoryMessages,
  isRecord,
  pruneThumbnailRecords,
  type MessageRecord,
  type ThumbnailRecord,
} from "./retention";
import { secureUuid } from "../platform/secure-uuid";
import {
  DEFAULT_ROUTER_URL,
  normalizeRouterUrl,
  ownerScope,
  peerScope,
  removeDeprecatedLocalStorage,
  type StorageAdapter,
} from "./settings-repository";

export {
  messageForStorage,
  messageKey,
  messageTime,
  ownerScope,
  peerScope,
  normalizeRouterUrl,
};
export type { MessageRecord, ThumbnailRecord } from "./retention";

export interface HistoryScope {
  routerUrl: string;
  agentId: string;
}

export type HistoryByPeer = Record<string, MessageRecord[]>;

export function thumbnailCacheKey(
  routerUrl: string,
  agentId: string,
  peer: string,
  messageId: string,
  attachmentId: string | number,
): string {
  return `${peerScope(routerUrl, agentId, peer)}\u001f${messageId}\u001f${attachmentId}`;
}

export const cacheKeyForAttachment = thumbnailCacheKey;

const persistenceInitializations = new WeakMap<IndexedDbFactory, Map<StorageAdapter | undefined, Promise<void>>>();

function randomKey(): string {
  return secureUuid();
}

function isUnknownArray(value: unknown): value is unknown[] {
  return Array.isArray(value);
}

function messageContainsDataUrl(message: MessageRecord): boolean {
  return Array.isArray(message.attachments) && message.attachments.some((attachment) => (
    isRecord(attachment) && typeof attachment.url === "string" && attachment.url.startsWith("data:image/")
  ));
}

function historyRecord(value: unknown): { peer: string; message: MessageRecord } | undefined {
  if (!isRecord(value) || typeof value.peer !== "string" || !isRecord(value.message)) return undefined;
  return { peer: value.peer, message: value.message };
}

async function saveRecordsForPeer(
  db: IDBDatabase,
  scope: HistoryScope,
  peer: string,
  messages: readonly MessageRecord[],
  now: number,
): Promise<void> {
  const routerUrl = normalizeRouterUrl(scope.routerUrl);
  const owner = ownerScope(routerUrl, scope.agentId);
  const peerKey = peerScope(routerUrl, scope.agentId, peer);
  const existingKeys = await getAllKeysByIndex(db, HISTORY_STORE_NAME, PEER_SCOPE_INDEX, peerKey);
  const transaction = db.transaction(HISTORY_STORE_NAME, "readwrite");
  const store = transaction.objectStore(HISTORY_STORE_NAME);
  for (const key of existingKeys) store.delete(key);
  for (const message of messages) {
    const identity = messageKey(message) || randomKey();
    store.put({
      key: `${peerKey}\u001f${identity}`,
      ownerScope: owner,
      peerScope: peerKey,
      routerUrl,
      agentId: scope.agentId,
      peer,
      message: messageForStorage(message),
      createdAtMs: messageTime(message, now),
      savedAtMs: now,
    });
  }
  await transactionDone(transaction);
}

async function allThumbnailRecords(db: IDBDatabase): Promise<unknown[]> {
  const transaction = db.transaction(IMAGE_STORE_NAME, "readonly");
  const result: unknown = await requestResult<unknown[]>(transaction.objectStore(IMAGE_STORE_NAME).getAll());
  return isUnknownArray(result) ? result : [];
}

export async function pruneHistoryDatabase(
  db: IDBDatabase,
  now = Date.now(),
): Promise<void> {
  const transaction = db.transaction(HISTORY_STORE_NAME, "readonly");
  const result: unknown = await requestResult(transaction.objectStore(HISTORY_STORE_NAME).getAll());
  if (!Array.isArray(result)) return;
  const byPeer = new Map<string, { record: Record<string, unknown>; peer: string; message: MessageRecord }[]>();
  for (const value of result) {
    if (typeof value !== "object" || value === null || Array.isArray(value)) continue;
    if (!isRecord(value)) continue;
    const record = value;
    const parsed = historyRecord(value);
    if (parsed === undefined) continue;
    const owner = typeof record.ownerScope === "string" ? record.ownerScope : "";
    const groupKey = `${owner}\u001f${parsed.peer}`;
    const group = byPeer.get(groupKey) ?? [];
    group.push({ record, peer: parsed.peer, message: parsed.message });
    byPeer.set(groupKey, group);
  }
  for (const records of byPeer.values()) {
    const first = records[0];
    if (first === undefined) continue;
    const routerUrl = typeof first.record.routerUrl === "string" ? first.record.routerUrl : "";
    const agentId = typeof first.record.agentId === "string" ? first.record.agentId : "";
    if (routerUrl === "" || agentId === "") continue;
    await saveRecordsForPeer(db, { routerUrl, agentId }, first.peer, pruneHistoryMessages(records.map((item) => item.message), now), now);
  }
}

export async function pruneThumbnailDatabase(db: IDBDatabase, now = Date.now()): Promise<void> {
  const values = await allThumbnailRecords(db);
  const retained = pruneThumbnailRecords(values, now);
  const retainedKeys = new Set(retained.map((record) => record.key));
  const transaction = db.transaction(IMAGE_STORE_NAME, "readwrite");
  const store = transaction.objectStore(IMAGE_STORE_NAME);
  for (const value of values) {
    const normalized = normalizeThumbnailRecord(value);
    if (normalized === undefined || !retainedKeys.has(normalized.key)) store.delete(normalized?.key ?? "");
  }
  for (const record of retained) store.put(record);
  await transactionDone(transaction);
}

export function initializePersistence(
  factory?: IndexedDbFactory,
  storage?: StorageAdapter,
  now = Date.now(),
): Promise<void> {
  const targetFactory = factory ?? globalThis.indexedDB;
  if (targetFactory === undefined) return Promise.reject(new Error("IndexedDB is unavailable"));
  const byStorage = persistenceInitializations.get(targetFactory) ?? new Map<StorageAdapter | undefined, Promise<void>>();
  const existing = byStorage.get(storage);
  if (existing !== undefined) return existing;
  const initialization = (async (): Promise<void> => {
    removeDeprecatedLocalStorage(storage);
    const db = await openHistoryDb(targetFactory);
    await pruneHistoryDatabase(db, now);
    await pruneThumbnailDatabase(db, now);
  })();
  byStorage.set(storage, initialization);
  persistenceInitializations.set(targetFactory, byStorage);
  void initialization.catch(() => {
    if (byStorage.get(storage) === initialization) byStorage.delete(storage);
    if (byStorage.size === 0) persistenceInitializations.delete(targetFactory);
  });
  return initialization;
}

export class HistoryRepository {
  private readonly factory: IndexedDbFactory | undefined;
  private readonly storage: StorageAdapter | undefined;
  private readonly saveQueues = new Map<string, Promise<void>>();

  public constructor(factory?: IndexedDbFactory, storage?: StorageAdapter) {
    this.factory = factory;
    this.storage = storage;
  }

  public async load(scope: HistoryScope, now = Date.now()): Promise<HistoryByPeer> {
    const routerUrl = normalizeRouterUrl(scope.routerUrl);
    const db = await openHistoryDb(this.factory);
    let values = await getAllByIndex(db, HISTORY_STORE_NAME, OWNER_SCOPE_INDEX, ownerScope(routerUrl, scope.agentId));
    if (values.length === 0 && routerUrl === DEFAULT_ROUTER_URL) {
      const legacy8787 = await getAllByIndex(db, HISTORY_STORE_NAME, OWNER_SCOPE_INDEX, ownerScope("http://127.0.0.1:8787", scope.agentId));
      if (legacy8787.length > 0) {
        values = legacy8787;
      }
    }
    const messages: HistoryByPeer = {};
    for (const value of values) {
      const parsed = historyRecord(value);
      if (parsed === undefined) continue;
      (messages[parsed.peer] ??= []).push(parsed.message);
    }
    await pruneThumbnailDatabase(db, now);
    for (const [peer, peerMessages] of Object.entries(messages)) {
      const retained = pruneHistoryMessages(peerMessages, now);
      messages[peer] = retained;
      const needsRewrite = retained.length !== peerMessages.length || peerMessages.some(messageContainsDataUrl);
      if (needsRewrite) {
        await saveRecordsForPeer(db, { routerUrl, agentId: scope.agentId }, peer, retained, now);
      }
    }
    return messages;
  }

  private async enqueuePeer(
    peer: string,
    scope: HistoryScope,
    messages: readonly MessageRecord[],
    replace: boolean,
    now: number,
  ): Promise<void> {
    const routerUrl = normalizeRouterUrl(scope.routerUrl);
    const queueKey = `${peerScope(routerUrl, scope.agentId, peer)}`;
    const previous = this.saveQueues.get(queueKey) ?? Promise.resolve();
    const next = previous.catch(() => undefined).then(async () => {
      const db = await openHistoryDb(this.factory);
      const existing = replace ? [] : await getAllByIndex(db, HISTORY_STORE_NAME, PEER_SCOPE_INDEX, queueKey);
      const byKey = new Map<string, MessageRecord>();
      for (const value of existing) {
        const parsed = historyRecord(value);
        if (parsed !== undefined) byKey.set(messageKey(parsed.message), parsed.message);
      }
      for (const message of messages) {
        const key = messageKey(message);
        byKey.set(key, { ...byKey.get(key), ...message });
      }
      await saveRecordsForPeer(db, { routerUrl, agentId: scope.agentId }, peer, pruneHistoryMessages([...byKey.values()], now), now);
      await pruneThumbnailDatabase(db, now);
    });
    this.saveQueues.set(queueKey, next);
    try { await next; } finally { if (this.saveQueues.get(queueKey) === next) this.saveQueues.delete(queueKey); }
  }

  public async savePeer(
    peer: string,
    scope: HistoryScope,
    messages: readonly MessageRecord[],
    now = Date.now(),
  ): Promise<void> {
    await this.enqueuePeer(peer, scope, messages, false, now);
  }

  public async replacePeer(
    peer: string,
    scope: HistoryScope,
    messages: readonly MessageRecord[],
    now = Date.now(),
  ): Promise<void> {
    await this.enqueuePeer(peer, scope, messages, true, now);
  }

  public async loadThumbnail(key: string, now = Date.now()): Promise<ThumbnailRecord | undefined> {
    const db = await openHistoryDb(this.factory);
    await pruneThumbnailDatabase(db, now);
    const transaction = db.transaction(IMAGE_STORE_NAME, "readonly");
    const value: unknown = await requestResult(transaction.objectStore(IMAGE_STORE_NAME).get(key));
    const normalized = normalizeThumbnailRecord(value);
    if (normalized !== undefined && value !== normalized) {
      const write = db.transaction(IMAGE_STORE_NAME, "readwrite");
      write.objectStore(IMAGE_STORE_NAME).put(normalized);
      await transactionDone(write);
    }
    return normalized;
  }

  public async saveThumbnail(value: unknown, now = Date.now()): Promise<void> {
    const normalized = normalizeThumbnailRecord(value);
    if (normalized === undefined) throw new Error("Invalid thumbnail record");
    const db = await openHistoryDb(this.factory);
    const transaction = db.transaction(IMAGE_STORE_NAME, "readwrite");
    transaction.objectStore(IMAGE_STORE_NAME).put({ ...normalized, createdAtMs: normalized.createdAtMs });
    await transactionDone(transaction);
    await pruneThumbnailDatabase(db, now);
  }

  public async initialize(now = Date.now()): Promise<void> {
    await initializePersistence(this.factory, this.storage, now);
  }
}

export async function loadMessages(
  routerUrl: string,
  agentId: string,
  factory?: IndexedDbFactory,
  now = Date.now(),
): Promise<HistoryByPeer> {
  return new HistoryRepository(factory).load({ routerUrl, agentId }, now);
}

export async function savePeerMessages(
  peer: string,
  scope: HistoryScope,
  messages: readonly MessageRecord[],
  factory?: IndexedDbFactory,
  now = Date.now(),
): Promise<void> {
  await new HistoryRepository(factory).savePeer(peer, scope, messages, now);
}

export async function saveCachedImage(
  value: unknown,
  factory?: IndexedDbFactory,
  now = Date.now(),
): Promise<void> {
  await new HistoryRepository(factory).saveThumbnail(value, now);
}

export async function cachedImage(
  key: string,
  factory?: IndexedDbFactory,
  now = Date.now(),
): Promise<ThumbnailRecord | undefined> {
  return new HistoryRepository(factory).loadThumbnail(key, now);
}
