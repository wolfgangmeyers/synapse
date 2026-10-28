import { secureUuid } from "../platform/secure-uuid";

export const HISTORY_LIMIT = 5000;
export const HISTORY_RETENTION_MS = 30 * 24 * 60 * 60 * 1000;

export interface MessageAttachment { url?: string; id?: string; mime_type?: string; size_bytes?: number; name?: string; cacheKey?: string }
export interface HistoryMessage {
  id: string;
  message_incarnation_id?: string;
  sequence?: number;
  recorded_at?: string;
  created_at?: string;
  createdAt?: string;
  from?: string;
  to?: string;
  body?: string;
  attachments?: MessageAttachment[];
  direction?: "incoming" | "outgoing";
  pending?: boolean;
  failed?: boolean;
  [key: string]: unknown;
}

export function messageKey(message: Pick<HistoryMessage, "id" | "message_incarnation_id">): string {
  return message.id;
}

export function messageTime(message: Pick<HistoryMessage, "recorded_at" | "created_at" | "createdAt">, fallback = Date.now()): number {
  const parsed = Date.parse(message.recorded_at ?? message.created_at ?? message.createdAt ?? "");
  return Number.isFinite(parsed) ? parsed : fallback;
}

export function compareMessages(left: HistoryMessage, right: HistoryMessage): number {
  return (left.sequence ?? 0) - (right.sequence ?? 0) || messageTime(left) - messageTime(right);
}

export function mergeMessages(existing: HistoryMessage[], incoming: HistoryMessage[]): HistoryMessage[] {
  const byKey = new Map(existing.map((message) => [message.id, message]));
  for (const message of incoming) byKey.set(message.id, { ...byKey.get(message.id), ...message });
  return [...byKey.values()].sort(compareMessages);
}

function pendingMatchesCanonical(pending: HistoryMessage, canonical: HistoryMessage): boolean {
  if (!pending.pending) return false;
  // The router preserves the client message ID. This is the authoritative match,
  // including when stale optimistic rows carry different incarnation metadata.
  if (pending.id === canonical.id) return true;
  if (pending.from !== canonical.from || pending.to !== canonical.to || pending.body !== canonical.body) return false;
  const pendingTime = pending.created_at ?? pending.createdAt;
  const canonicalTime = canonical.created_at ?? canonical.createdAt;
  return pendingTime !== undefined && canonicalTime !== undefined && pendingTime === canonicalTime;
}

function removeReconciledPending(existing: HistoryMessage[], snapshot: HistoryMessage[]): HistoryMessage[] {
  const canonical = snapshot.filter((message) => !message.pending);
  return existing.filter((message) => !message.pending || !canonical.some((candidate) => pendingMatchesCanonical(message, candidate)));
}

export function reconcileHistoryMessages(existing: HistoryMessage[], snapshot: HistoryMessage[], highWaterSequence: number): HistoryMessage[] {
  const snapshotKeys = new Set(snapshot.map(messageKey));
  const live = removeReconciledPending(existing.filter((message) => {
    const sequence = Number(message.sequence ?? 0);
    return sequence === 0 || sequence > highWaterSequence || snapshotKeys.has(messageKey(message));
  }), snapshot);
  const merged = mergeMessages(live, snapshot);
  return removeReconciledPending(merged, merged.filter((message) => !message.pending));
}

export function pruneHistoryMessages(messages: HistoryMessage[], now = Date.now()): HistoryMessage[] {
  const cutoff = now - HISTORY_RETENTION_MS;
  return [...messages].filter((message) => messageTime(message, now) >= cutoff).sort((a, b) => messageTime(a, now) - messageTime(b, now)).slice(-HISTORY_LIMIT);
}

export function stripDataUrls(message: HistoryMessage): HistoryMessage {
  if (!Array.isArray(message.attachments)) return { ...message };
  return { ...message, attachments: message.attachments.filter((attachment) => !attachment.url?.startsWith("data:image/")).map(({ url, id, mime_type, size_bytes, name, cacheKey }) => ({ ...(url !== undefined ? { url } : {}), ...(id !== undefined ? { id } : {}), ...(mime_type !== undefined ? { mime_type } : {}), ...(size_bytes !== undefined ? { size_bytes } : {}), ...(name !== undefined ? { name } : {}), ...(cacheKey !== undefined ? { cacheKey } : {}) })) };
}

export function pendingMessage(body: string, peer: string, from: string, images: MessageAttachment[] = [], id = secureUuid(), createdAt = new Date().toISOString()): HistoryMessage {
  const text = body.trim();
  const visibleBody = images.length === 0 ? body : `${text}${text ? "\n\n" : ""}Attached image${images.length === 1 ? "" : "s"}:\n${images.map((image, index) => `${index + 1}. ${image.name || "image.jpg"}`).join("\n")}`;
  return { id, from, to: peer, body: visibleBody, attachments: images, created_at: createdAt, direction: "outgoing", pending: true };
}

export function settlePendingMessages(messages: HistoryMessage[], localId: string, ok: boolean): HistoryMessage[] {
  return messages.map((message) => message.id === localId ? { ...message, pending: false, failed: !ok } : message);
}

export function replacePendingMessage(messages: HistoryMessage[], localId: string, accepted: HistoryMessage): HistoryMessage[] {
  const withoutLocal = messages.filter((message) => message.id !== localId);
  return mergeMessages(removeReconciledPending(withoutLocal, [accepted]), [{ ...accepted, direction: "outgoing" }]);
}
