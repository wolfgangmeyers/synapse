import { describe, expect, it } from "vitest";
import {
  DRAFT_LIMIT,
  DRAFT_RETENTION_MS,
  createDraftRepository,
  draftScope,
  ownerScope,
} from "../../src/persistence/draft-repository";

class MemoryStorage {
  private readonly values = new Map<string, string>();

  getItem(key: string): string | null {
    return this.values.get(key) ?? null;
  }

  setItem(key: string, value: string): void {
    this.values.set(key, value);
  }

  removeItem(key: string): void {
    this.values.delete(key);
  }
}

describe("draft repository", () => {
  it("treats malformed JSON as an empty state", () => {
    const storage = new MemoryStorage();
    storage.setItem("mecha.web.drafts", "not json");

    const repository = createDraftRepository(storage);

    expect(repository.loadAll()).toEqual({});
  });

  it("scopes drafts by the normalized router, agent, and peer", () => {
    const storage = new MemoryStorage();
    const repository = createDraftRepository(storage);

    repository.save("http://router.example/path", "web", "peer", "draft body", 1000);

    expect(repository.load("router.example", "web", "peer", 1000)).toBe("draft body");
    expect(repository.load("http://router.example", "other", "peer", 1000)).toBe("");
    expect(repository.peers("http://router.example", "web", 1000)).toEqual(new Set(["peer"]));
    expect(ownerScope("router.example", "web")).toBe("http://router.example\u001fweb");
    expect(draftScope("router.example", "web", "peer")).toBe("http://router.example\u001fweb\u001fpeer");
  });

  it("keeps only the newest 200 non-empty drafts", () => {
    const storage = new MemoryStorage();
    const repository = createDraftRepository(storage);
    const now = Date.parse("2026-01-31T00:00:00.000Z");

    for (let index = 0; index < DRAFT_LIMIT + 1; index += 1) {
      repository.save("router.example", "web", `peer-${index}`, `body-${index}`, now + index);
    }

    const drafts = repository.loadAll(now + DRAFT_LIMIT + 1);

    expect(Object.keys(drafts)).toHaveLength(DRAFT_LIMIT);
    expect(drafts[draftScope("router.example", "web", "peer-0")]).toBeUndefined();
    expect(drafts[draftScope("router.example", "web", "peer-200")]).toMatchObject({ body: "body-200" });
  });

  it("removes drafts older than thirty days during reads", () => {
    const storage = new MemoryStorage();
    const repository = createDraftRepository(storage);
    const now = Date.parse("2026-01-31T00:00:00.000Z");

    repository.save("router.example", "web", "old", "old body", now - DRAFT_RETENTION_MS - 1);
    repository.save("router.example", "web", "new", "new body", now);

    expect(repository.loadAll(now)).toEqual({
      [draftScope("router.example", "web", "new")]: { body: "new body", updatedAtMs: now },
    });
  });

  it("deletes an empty draft", () => {
    const storage = new MemoryStorage();
    const repository = createDraftRepository(storage);

    repository.save("router.example", "web", "peer", "body", 1000);
    repository.save("router.example", "web", "peer", "", 1001);

    expect(repository.load("router.example", "web", "peer", 1001)).toBe("");
  });
});
