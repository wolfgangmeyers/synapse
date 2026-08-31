export const HISTORY_DB_NAME = "mecha-router-web-ui";
export const HISTORY_DB_VERSION = 2;
export const HISTORY_STORE_NAME = "conversation_messages";
export const IMAGE_STORE_NAME = "image_attachments";
export const OWNER_SCOPE_INDEX = "ownerScope";
export const PEER_SCOPE_INDEX = "peerScope";
export const CREATED_AT_INDEX = "createdAtMs";

export interface IndexedDbFactory {
  open(name: string, version?: number): IDBOpenDBRequest;
}

function browserIndexedDb(): IndexedDbFactory {
  if (typeof globalThis.indexedDB === "undefined") throw new Error("IndexedDB is unavailable");
  return globalThis.indexedDB;
}

export function openHistoryDb(factory: IndexedDbFactory = browserIndexedDb()): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = factory.open(HISTORY_DB_NAME, HISTORY_DB_VERSION);
    request.onupgradeneeded = () => {
      const db = request.result;
      const transaction = request.transaction;
      if (transaction === null) {
        reject(new Error("IndexedDB upgrade transaction is unavailable"));
        return;
      }
      const historyStore = db.objectStoreNames.contains(HISTORY_STORE_NAME)
        ? transaction.objectStore(HISTORY_STORE_NAME)
        : db.createObjectStore(HISTORY_STORE_NAME, { keyPath: "key" });
      if (!historyStore.indexNames.contains(OWNER_SCOPE_INDEX)) {
        historyStore.createIndex(OWNER_SCOPE_INDEX, OWNER_SCOPE_INDEX, { unique: false });
      }
      if (!historyStore.indexNames.contains(PEER_SCOPE_INDEX)) {
        historyStore.createIndex(PEER_SCOPE_INDEX, PEER_SCOPE_INDEX, { unique: false });
      }
      if (!historyStore.indexNames.contains(CREATED_AT_INDEX)) {
        historyStore.createIndex(CREATED_AT_INDEX, CREATED_AT_INDEX, { unique: false });
      }
      if (!db.objectStoreNames.contains(IMAGE_STORE_NAME)) {
        db.createObjectStore(IMAGE_STORE_NAME, { keyPath: "key" });
      }
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error("IndexedDB open failed"));
    request.onblocked = () => reject(new Error("IndexedDB upgrade is blocked"));
  });
}

export function requestResult<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error ?? new Error("IndexedDB request failed"));
  });
}

export function transactionDone(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error ?? new Error("IndexedDB transaction failed"));
    transaction.onabort = () => reject(transaction.error ?? new Error("IndexedDB transaction aborted"));
  });
}

export async function getAllByIndex(
  db: IDBDatabase,
  storeName: string,
  indexName: string,
  value: IDBValidKey,
): Promise<unknown[]> {
  const transaction = db.transaction(storeName, "readonly");
  const request = transaction.objectStore(storeName).index(indexName).getAll(value);
  const result: unknown = await requestResult<unknown[]>(request);
  return isUnknownArray(result) ? result : [];
}

export async function getAllKeysByIndex(
  db: IDBDatabase,
  storeName: string,
  indexName: string,
  value: IDBValidKey,
): Promise<IDBValidKey[]> {
  const transaction = db.transaction(storeName, "readonly");
  const request = transaction.objectStore(storeName).index(indexName).getAllKeys(value);
  const result = await requestResult<IDBValidKey[]>(request);
  return result.filter((key): key is IDBValidKey => isValidKey(key));
}

function isUnknownArray(value: unknown): value is unknown[] {
  return Array.isArray(value);
}

function isValidKey(value: unknown): value is IDBValidKey {
  if (typeof value === "string" || typeof value === "number") return true;
  if (value instanceof Date) return !Number.isNaN(value.valueOf());
  return Array.isArray(value) && value.every(isValidKey);
}
