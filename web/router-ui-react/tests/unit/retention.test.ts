import { describe, expect, it } from "vitest";
import {
  HISTORY_LIMIT,
  HISTORY_RETENTION_MS,
  THUMBNAIL_BYTE_LIMIT,
  THUMBNAIL_LIMIT,
  THUMBNAIL_RETENTION_MS,
  messageForStorage,
  normalizeThumbnailRecord,
  pruneHistoryMessages,
  pruneThumbnailRecords,
} from "../../src/persistence/retention";

describe("history retention", () => {
  it("removes old messages and keeps the newest per-peer limit", () => {
    const now = Date.parse("2026-01-31T00:00:00.000Z");
    const messages = Array.from({ length: HISTORY_LIMIT + 2 }, (_, index) => ({
      id: `message-${index}`,
      created_at: new Date(now - index * 1_000).toISOString(),
    }));
    messages.push({ id: "old", created_at: new Date(now - HISTORY_RETENTION_MS - 1).toISOString() });

    const retained = pruneHistoryMessages(messages, now);

    expect(retained).toHaveLength(HISTORY_LIMIT);
    expect(retained[0]?.id).toBe("message-199");
    expect(retained.at(-1)?.id).toBe("message-0");
    expect(retained.some((message) => message.id === "old")).toBe(false);
  });

  it("removes data URLs while preserving router references and cache keys", () => {
    const stored = messageForStorage({
      id: "message-1",
      attachments: [
        { url: "data:image/jpeg;base64,secret", name: "local.jpg", size_bytes: 10 },
        { url: "/v1/attachments/remote", id: "remote-id", mime_type: "image/jpeg", cacheKey: "thumbnail-key" },
      ],
    });

    expect(stored.attachments).toEqual([
      { url: "/v1/attachments/remote", id: "remote-id", mime_type: "image/jpeg", size_bytes: undefined, name: undefined, cacheKey: "thumbnail-key" },
    ]);
  });
});

describe("thumbnail retention", () => {
  const thumbnail = (key: string, createdAtMs: number, byteSize: number) => ({
    key,
    blob: new Blob([new Uint8Array(byteSize)], { type: "image/jpeg" }),
    createdAtMs,
    byteSize,
  });

  it("normalizes the legacy savedAtMs and Blob.size fields", () => {
    const blob = new Blob(["legacy"], { type: "image/jpeg" });
    const normalized = normalizeThumbnailRecord({ key: "legacy-key", blob, savedAtMs: 1234 });

    expect(normalized).toMatchObject({ key: "legacy-key", blob, createdAtMs: 1234, byteSize: blob.size });
  });

  it("prunes thumbnails independently by age", () => {
    const now = Date.parse("2026-01-31T00:00:00.000Z");
    const retained = pruneThumbnailRecords([
      thumbnail("old", now - THUMBNAIL_RETENTION_MS - 1, 1),
      thumbnail("new", now - 1, 1),
    ], now);

    expect(retained.map((record) => record.key)).toEqual(["new"]);
  });

  it("prunes the oldest thumbnails when the count bound is exceeded", () => {
    const now = Date.now();
    const records = Array.from({ length: THUMBNAIL_LIMIT + 1 }, (_, index) => thumbnail(`key-${index}`, now - index, 1));

    const retained = pruneThumbnailRecords(records, now);

    expect(retained).toHaveLength(THUMBNAIL_LIMIT);
    expect(retained.some((record) => record.key === `key-${THUMBNAIL_LIMIT}`)).toBe(false);
  });

  it("prunes oldest thumbnails until the encoded-byte budget is met", () => {
    const now = Date.now();
    const records = [
      thumbnail("old", now - 3, THUMBNAIL_BYTE_LIMIT),
      thumbnail("middle", now - 2, THUMBNAIL_BYTE_LIMIT),
      thumbnail("new", now - 1, 1),
    ];

    const retained = pruneThumbnailRecords(records, now);

    expect(retained.map((record) => record.key)).toEqual(["new"]);
    expect(retained.reduce((total, record) => total + record.byteSize, 0)).toBeLessThanOrEqual(THUMBNAIL_BYTE_LIMIT);
  });
});
