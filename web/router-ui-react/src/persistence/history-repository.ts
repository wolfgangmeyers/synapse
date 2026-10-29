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

// saveRecordsForPeer removed as conversation_messages is no longer written.

async function allThumbnailRecords(db: IDBDatabase): Promise<unknown[]> {
  const transaction = db.transaction(IMAGE_STORE_NAME, "readonly");
  const result: unknown = await requestResult<unknown[]>(transaction.objectStore(IMAGE_STORE_NAME).getAll());
  return isUnknownArray(result) ? result : [];
}

export async function purgeConversationMessages(db: IDBDatabase): Promise<void> {
  if (!db.objectStoreNames.contains(HISTORY_STORE_NAME)) return;
  const transaction = db.transaction(HISTORY_STORE_NAME, "readwrite");
  transaction.objectStore(HISTORY_STORE_NAME).clear();
  await transactionDone(transaction);
}

export async function pruneHistoryDatabase(
  _db: IDBDatabase,
  _now = Date.now(),
): Promise<void> {
  // Deprecated: conversation caching is eliminated. We clear instead.
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
    await purgeConversationMessages(db);
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

  public async load(_scope: HistoryScope, now = Date.now()): Promise<HistoryByPeer> {
    const db = await openHistoryDb(this.factory);
    await purgeConversationMessages(db);
    await pruneThumbnailDatabase(db, now);
    return {};
  }

  public async purge(): Promise<void> {
    const db = await openHistoryDb(this.factory);
    await purgeConversationMessages(db);
  }

  public async savePeer(
    _peer: string,
    _scope: HistoryScope,
    _messages: readonly MessageRecord[],
    _now = Date.now(),
  ): Promise<void> {
    // Conversation caching in IndexedDB is eliminated. No-op.
  }

  public async replacePeer(
    _peer: string,
    _scope: HistoryScope,
    _messages: readonly MessageRecord[],
    _now = Date.now(),
  ): Promise<void> {
    // Conversation caching in IndexedDB is eliminated. No-op.
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
  _peer: string,
  _scope: HistoryScope,
  _messages: readonly MessageRecord[],
  _factory?: IndexedDbFactory,
  _now = Date.now(),
): Promise<void> {
  // Conversation caching in IndexedDB is eliminated. No-op.
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
