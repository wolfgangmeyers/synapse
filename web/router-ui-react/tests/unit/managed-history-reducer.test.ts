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

  it("caps inserted history at 200 while replacements keep their timeline position", () => {
    let state = createInitialState(scope);
    for (let index = 0; index < 200; index += 1) state = append(state, entry(`complete:${index}`));

    const updated = append(state, entry("complete:0", "success"));
    const updatedEntries = updated.managedHistory?.["managed-alpha"]?.complete ?? [];
    expect(updatedEntries).toHaveLength(200);
    expect(updatedEntries[0]).toEqual(entry("complete:0", "success"));
    expect(updatedEntries.map((item) => item.id)).toEqual(Array.from({ length: 200 }, (_, index) => `complete:${index}`));

    const inserted = append(updated, entry("complete:200", "success"));
    const insertedEntries = inserted.managedHistory?.["managed-alpha"]?.complete ?? [];
    expect(insertedEntries).toHaveLength(200);
    expect(insertedEntries.map((item) => item.id)).toEqual(Array.from({ length: 200 }, (_, index) => `complete:${index + 1}`));
  });
});
