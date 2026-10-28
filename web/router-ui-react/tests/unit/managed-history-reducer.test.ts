import { describe, expect, it } from "vitest";
import { createInitialState } from "../../src/state/app-state";
import { reducer } from "../../src/state/reducer";

const scope = { routerUrl: "http://router.test", agentId: "wolfgang" };

function entry(id: string, outcome: "active" | "success" | "failed" | "interrupted" = "active") {
  return {
    id,
    agent_id: "managed-alpha",
    at: "2030-01-01T00:00:00.000Z",
    level: outcome === "active" || outcome === "success" ? "info" : "error",
    message: outcome === "active" ? "Running read" : outcome === "success" ? "Ran read" : "Read interrupted",
    presentation: { kind: "tool" as const, tool_name: "read", outcome, summary: outcome === "active" ? "Running" : "Completed successfully" },
  };
}

function append(state: ReturnType<typeof createInitialState>, next = entry("complete:1"), actionScope = scope) {
  return reducer(state, { type: "managed/history", scope: actionScope, view: "complete", entry: next });
}

describe("managed Complete history reducer", () => {
  it("replaces a same-ID transition in place and ignores duplicate replays", () => {
    const initial = append(createInitialState(scope), entry("complete:1"));
    const withLaterEntry = append(initial, entry("complete:2", "success"));
    const terminal = entry("complete:1", "interrupted");

    const replaced = append(withLaterEntry, terminal);

    expect(replaced.managedHistory?.["managed-alpha"]?.complete).toEqual([terminal, entry("complete:2", "success")]);
    expect(append(replaced, terminal)).toBe(replaced);
  });

  it("does not let another history fence mutate the current entry", () => {
    const current = append(createInitialState(scope), entry("complete:1"));
    const staleFence = { routerUrl: "http://other-router.test", agentId: "wolfgang" };

    const rejected = append(current, entry("complete:1", "interrupted"), staleFence);

    expect(rejected).toBe(current);
    expect(rejected.managedHistory?.["managed-alpha"]?.complete).toEqual([entry("complete:1")]);
  });

  it("caps inserted history at 5000 while replacements keep their timeline position", () => {
    let state = createInitialState(scope);
    for (let index = 0; index < 5000; index += 1) state = append(state, entry(`complete:${index}`));

    const updated = append(state, entry("complete:0", "success"));
    const updatedEntries = updated.managedHistory?.["managed-alpha"]?.complete ?? [];
    expect(updatedEntries).toHaveLength(5000);
    expect(updatedEntries[0]).toEqual(entry("complete:0", "success"));
    expect(updatedEntries.map((item) => item.id)).toEqual(Array.from({ length: 5000 }, (_, index) => `complete:${index}`));

    const inserted = append(updated, entry("complete:5000", "success"));
    const insertedEntries = inserted.managedHistory?.["managed-alpha"]?.complete ?? [];
    expect(insertedEntries).toHaveLength(5000);
    expect(insertedEntries.map((item) => item.id)).toEqual(Array.from({ length: 5000 }, (_, index) => `complete:${index + 1}`));
  });

  it("maintains chronological order and protects newest entries when page arrives descending", () => {
    let state = createInitialState(scope);
    // Simulate HTTP response where entries are returned descending: 4999 down to 0
    for (let index = 4999; index >= 0; index -= 1) {
      state = append(state, entry(`complete:${index}`));
    }
    const entries = state.managedHistory?.["managed-alpha"]?.complete ?? [];
    expect(entries).toHaveLength(5000);
    expect(entries.map((item) => item.id)).toEqual(Array.from({ length: 5000 }, (_, index) => `complete:${index}`));

    // When new live frames (complete:5000, 5001) arrive after context reset,
    // the oldest entries (complete:0, 1) are evicted, NOT the recent entries (complete:4998, 4999)
    state = append(state, entry("complete:5000"));
    state = append(state, entry("complete:5001"));

    const afterLive = state.managedHistory?.["managed-alpha"]?.complete ?? [];
    expect(afterLive).toHaveLength(5000);
    expect(afterLive[0]?.id).toBe("complete:2");
    expect(afterLive.some((item) => item.id === "complete:4998")).toBe(true);
    expect(afterLive.some((item) => item.id === "complete:4999")).toBe(true);
    expect(afterLive.some((item) => item.id === "complete:5000")).toBe(true);
    expect(afterLive[afterLive.length - 1]?.id).toBe("complete:5001");
  });
});
