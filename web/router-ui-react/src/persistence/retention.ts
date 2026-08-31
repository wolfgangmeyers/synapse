export const HISTORY_LIMIT = 200;
export const HISTORY_RETENTION_MS = 30 * 24 * 60 * 60 * 1000;
export const THUMBNAIL_LIMIT = 600;
export const THUMBNAIL_RETENTION_MS = HISTORY_RETENTION_MS;
export const THUMBNAIL_BYTE_LIMIT = 64 * 1024 * 1024;
export const DRAFT_LIMIT = 200;
export const DRAFT_RETENTION_MS = HISTORY_RETENTION_MS;

export type MessageRecord = Record<string, unknown>;

export interface DraftRecord {
  body: string;
  updatedAtMs: number;
}

export type ThumbnailRecord = {
  key: string;
  blob: Blob;
  createdAtMs: number;
  byteSize: number;
  [key: string]: unknown;
};

export function isRecord(value: unknown): value is MessageRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function messageKey(message: MessageRecord): string {
  const id = typeof message.id === "string" || typeof message.id === "number" ? String(message.id) : "";
  const incarnation = typeof message.message_incarnation_id === "string"
    ? message.message_incarnation_id
    : "";
  return `${id}\u001f${incarnation}`;
}

export function messageTime(message: MessageRecord, fallbackMs = Date.now()): number {
  const numeric = message.createdAtMs;
  if (typeof numeric === "number" && Number.isFinite(numeric)) return numeric;
  for (const field of ["recorded_at", "created_at", "createdAt"]) {
    const value = message[field];
    if (typeof value === "string") {
      const timestamp = Date.parse(value);
      if (Number.isFinite(timestamp)) return timestamp;
    }
  }
  return fallbackMs;
}

export function mergeMessages<T extends MessageRecord>(existing: readonly T[], incoming: readonly T[]): T[] {
  const byKey = new Map<string, T>();
  for (const message of existing) byKey.set(messageKey(message), message);
  for (const message of incoming) {
    const key = messageKey(message);
    const previous = byKey.get(key);
    byKey.set(key, previous === undefined ? message : { ...previous, ...message });
  }
  return [...byKey.values()].sort((left, right) => {
    const leftSequence = typeof left.sequence === "number" ? left.sequence : 0;
    const rightSequence = typeof right.sequence === "number" ? right.sequence : 0;
    return leftSequence - rightSequence || messageTime(left) - messageTime(right);
  });
}

export function reconcileHistoryMessages<T extends MessageRecord>(
  existing: readonly T[],
  snapshot: readonly T[],
  highWaterSequence: number,
): T[] {
  const snapshotKeys = new Set(snapshot.map(messageKey));
  const retained = existing.filter((message) => {
    const sequence = typeof message.sequence === "number" ? message.sequence : 0;
    return sequence === 0 || sequence > highWaterSequence || snapshotKeys.has(messageKey(message));
  });
  return mergeMessages(retained, snapshot);
}

export function pruneHistoryMessages<T extends MessageRecord>(messages: readonly T[], now = Date.now()): T[] {
  const cutoff = now - HISTORY_RETENTION_MS;
  return [...messages]
    .filter((message) => messageTime(message, now) >= cutoff)
    .sort((left, right) => messageTime(left, now) - messageTime(right, now))
    .slice(-HISTORY_LIMIT);
}

export function pruneDrafts(drafts: Record<string, unknown>, now = Date.now()): Record<string, DraftRecord> {
  const cutoff = now - DRAFT_RETENTION_MS;
  const normalized: Array<[string, DraftRecord]> = [];
  for (const [key, value] of Object.entries(drafts)) {
    if (typeof value === "string") {
      if (value !== "") normalized.push([key, { body: value, updatedAtMs: now }]);
      continue;
    }
    if (!isRecord(value) || typeof value.body !== "string" || value.body === "") continue;
    if (!finiteNonNegative(value.updatedAtMs)) continue;
    if (value.updatedAtMs < cutoff) continue;
    normalized.push([key, { body: value.body, updatedAtMs: value.updatedAtMs }]);
  }
  normalized.sort(([, left], [, right]) => left.updatedAtMs - right.updatedAtMs);
  return Object.fromEntries(normalized.slice(-DRAFT_LIMIT));
}

export function messageForStorage(message: MessageRecord): MessageRecord {
  const attachments = message.attachments;
  if (!Array.isArray(attachments)) return message;

  const storedAttachments: unknown[] = [];
  for (const attachment of attachments) {
    if (!isRecord(attachment)) continue;
    const url = typeof attachment.url === "string" ? attachment.url : "";
    if (url.startsWith("data:image/")) continue;
    storedAttachments.push({
      url: attachment.url,
      id: attachment.id,
      mime_type: attachment.mime_type,
      size_bytes: attachment.size_bytes,
      name: attachment.name,
      cacheKey: attachment.cacheKey,
    });
  }
  return { ...message, attachments: storedAttachments };
}

function isBlob(value: unknown): value is Blob {
  if (typeof Blob !== "undefined" && value instanceof Blob) return true;
  if (!isRecord(value)) return false;
  return typeof value.size === "number" && Number.isFinite(value.size) && typeof value.type === "string";
}

function finiteNonNegative(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0;
}

export function normalizeThumbnailRecord(value: unknown): ThumbnailRecord | undefined {
  if (!isRecord(value) || typeof value.key !== "string" || !isBlob(value.blob)) return undefined;
  const createdAtMs = finiteNonNegative(value.createdAtMs)
    ? value.createdAtMs
    : (finiteNonNegative(value.savedAtMs) ? value.savedAtMs : undefined);
  if (createdAtMs === undefined) return undefined;
  const byteSize = finiteNonNegative(value.byteSize) ? value.byteSize : value.blob.size;
  return { ...value, key: value.key, blob: value.blob, createdAtMs, byteSize };
}

export function pruneThumbnailRecords(values: readonly unknown[], now = Date.now()): ThumbnailRecord[] {
  const cutoff = now - THUMBNAIL_RETENTION_MS;
  const records = values
    .map(normalizeThumbnailRecord)
    .filter((record): record is ThumbnailRecord => record !== undefined)
    .filter((record) => record.createdAtMs >= cutoff)
    .sort((left, right) => left.createdAtMs - right.createdAtMs);

  let totalBytes = records.reduce((total, record) => total + record.byteSize, 0);
  while (records.length > THUMBNAIL_LIMIT || totalBytes > THUMBNAIL_BYTE_LIMIT) {
    const removed = records.shift();
    if (removed === undefined) break;
    totalBytes -= removed.byteSize;
  }
  return records;
}
