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

describe("managed history reducer", () => {
  it.each(["quiet", "complete"] as const)("ignores active and terminal entries in the active scope for %s", (view) => {
    const initial = createInitialState(scope);
    let state = initial;
    for (const outcome of ["active", "success", "failed", "interrupted"] as const) {
      state = reducer(state, { type: "managed/history", scope, view, entry: entry("history:1", outcome) });
      expect(state).toBe(initial);
      expect(state.managedHistory).toEqual({});
    }
  });

  it.each([
    { routerUrl: "http://other-router.test", agentId: "wolfgang" },
    { routerUrl: scope.routerUrl, agentId: "other-agent" },
  ])("ignores history from an inactive scope: $routerUrl / $agentId", (staleScope) => {
    const initial = createInitialState(scope);
    for (const view of ["quiet", "complete"] as const) {
      const result = reducer(initial, { type: "managed/history", scope: staleScope, view, entry: entry("history:1") });
      expect(result).toBe(initial);
      expect(result.managedHistory).toEqual({});
    }
    // Verify the scope fence independently of the history action's unconditional no-op.
    expect(reducer(initial, { type: "filter/set", scope: staleScope, filter: "stale" })).toBe(initial);
    expect(reducer(initial, { type: "filter/set", scope, filter: "current" }).filter).toBe("current");
  });

  it("retains no history after multiple entries, transitions, and scope resets", () => {
    let state = createInitialState(scope);
    for (let index = 0; index < 100; index += 1) {
      for (const view of ["quiet", "complete"] as const) {
        state = reducer(state, { type: "managed/history", scope, view, entry: entry(`history:${index}`) });
        state = reducer(state, { type: "managed/history", scope, view, entry: entry(`history:${index}`, "success") });
      }
    }
    expect(state.managedHistory).toEqual({});
    const otherScope = { routerUrl: scope.routerUrl, agentId: "other-agent" };
    state = reducer(state, { type: "scope/reset", scope: otherScope });
    state = reducer(state, { type: "managed/history", scope, view: "complete", entry: entry("stale") });
    state = reducer(state, { type: "managed/history", scope: otherScope, view: "complete", entry: entry("current") });
    expect(state.managedHistory).toEqual({});
    state = reducer(state, { type: "scope/reset", scope });
    expect(state.managedHistory).toEqual({});
  });
});
