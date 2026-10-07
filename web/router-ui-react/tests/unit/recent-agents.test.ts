import { describe, expect, it, vi } from "vitest";
import { loadRecentAgents, normalizeRecentAgents, RECENT_AGENTS_KEY, RECENT_AGENTS_LIMIT, RECENT_AGENT_SCOPES_LIMIT, saveRecentAgents } from "../../src/persistence/recent-agents";
import { ownerScope, type StorageAdapter } from "../../src/persistence/settings-repository";

const scope = ownerScope("http://router.test", "web");
function storageWith(value: string | null): StorageAdapter {
  return { getItem: () => value, setItem: vi.fn((_, next: string) => { value = next; }), removeItem: vi.fn() };
}

function storedMap(storage: StorageAdapter): Record<string, unknown> {
  const parsed: unknown = JSON.parse(storage.getItem(RECENT_AGENTS_KEY) ?? "null");
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Expected scoped map");
  return Object.fromEntries(Object.entries(parsed));
}

describe("recent agent persistence", () => {
  it("normalizes invalid values and duplicates without merging sources", () => {
    expect(normalizeRecentAgents([null, 1, {}, "", "worker", "other:worker", "tmux:", "tmux: ", "tmux:worker", "router-managed:worker", "tmux:worker"]))
      .toEqual(["tmux:worker", "router-managed:worker"]);
  });

  it("bounds loaded and saved histories to ten unique source-qualified picks", () => {
    const picks = Array.from({ length: 25 }, (_, i) => `tmux:worker-${i}`);
    const storage = storageWith(JSON.stringify({ [scope]: picks }));
    expect(loadRecentAgents(storage, scope)).toEqual(picks.slice(0, RECENT_AGENTS_LIMIT));
    saveRecentAgents(storage, scope, picks);
    expect(storage.setItem).toHaveBeenCalledWith(RECENT_AGENTS_KEY, JSON.stringify({ [scope]: picks.slice(0, RECENT_AGENTS_LIMIT) }));
  });

  it.each([ownerScope("http://second.test", "web"), ownerScope("http://router.test", "other-owner")])("isolates identical agent IDs for %s", (otherScope) => {
    const storage = storageWith(null);
    saveRecentAgents(storage, scope, ["tmux:worker", "router-managed:worker"]);
    expect(loadRecentAgents(storage, otherScope)).toEqual([]);
    saveRecentAgents(storage, otherScope, ["router-managed:worker", "tmux:worker"]);
    expect(loadRecentAgents(storage, scope)).toEqual(["tmux:worker", "router-managed:worker"]);
    expect(loadRecentAgents(storage, otherScope)).toEqual(["router-managed:worker", "tmux:worker"]);
  });

  it("retains only the most recently saved scopes and refreshes a reused scope", () => {
    const storage = storageWith(null);
    const scopes = Array.from({ length: RECENT_AGENT_SCOPES_LIMIT }, (_, i) => ownerScope(`http://router-${i}.test`, "web"));
    for (const key of scopes) saveRecentAgents(storage, key, ["tmux:worker"]);
    saveRecentAgents(storage, scopes[0]!, ["tmux:refreshed"]);
    saveRecentAgents(storage, scope, ["tmux:new"]);
    expect(Object.keys(storedMap(storage))).toHaveLength(RECENT_AGENT_SCOPES_LIMIT);
    expect(loadRecentAgents(storage, scopes[0]!)).toEqual(["tmux:refreshed"]);
    expect(loadRecentAgents(storage, scopes[1]!)).toEqual([]);
  });

  it("bounds an oversized stored map and drops malformed entries on save", () => {
    const scopes = Array.from({ length: RECENT_AGENT_SCOPES_LIMIT + 3 }, (_, i) => ownerScope(`http://router-${i}.test`, "web"));
    const storage = storageWith(JSON.stringify({ bad: ["tmux:worker"], [scope]: null, ...Object.fromEntries(scopes.map((key) => [key, ["tmux:worker"]])) }));
    expect(loadRecentAgents(storage, scopes[0]!)).toEqual([]);
    saveRecentAgents(storage, scope, ["tmux:new"]);
    const parsed = storedMap(storage);
    expect(Object.keys(parsed)).toHaveLength(RECENT_AGENT_SCOPES_LIMIT);
    expect(parsed.bad).toBeUndefined();
    expect(parsed[scope]).toEqual(["tmux:new"]);
  });

  it.each([null, "not-json", "{}", "null", "123", '"tmux:worker"', '["tmux:worker"]'])("ignores malformed or unscoped saved history %s", (value) => {
    expect(loadRecentAgents(storageWith(value), scope)).toEqual([]);
  });

  it("tolerates missing storage and read/write failures", () => {
    const broken: StorageAdapter = { getItem() { throw new Error("read"); }, setItem() { throw new Error("write"); }, removeItem: vi.fn() };
    expect(loadRecentAgents(undefined, scope)).toEqual([]);
    expect(loadRecentAgents(broken, scope)).toEqual([]);
    expect(() => saveRecentAgents(undefined, scope, ["tmux:worker"])).not.toThrow();
    expect(() => saveRecentAgents(broken, scope, ["tmux:worker"])).not.toThrow();
  });
});
