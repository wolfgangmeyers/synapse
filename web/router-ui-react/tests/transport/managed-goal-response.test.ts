import { describe, expect, it } from "vitest";
import { DEFAULT_GOAL_STATE } from "../../src/domain/goals";
import { createHttpClient } from "../../src/transport/http-client";

// Exercise the real HTTP consumer, not a parallel test-only schema.
const consume = (goal_state: unknown) => createHttpClient("http://127.0.0.1:18765", async (url, init) => {
  expect(String(url)).toBe("http://127.0.0.1:18765/v1/managed-agents/fixture/goal");
  expect(init?.credentials).toBe("omit");
  return new Response(JSON.stringify({ goal_state }), { headers: { "content-type": "application/json" } });
}).managedGoal("fixture");

const embedded = { ...DEFAULT_GOAL_STATE, revision: 2, desired_revision: 2, applied_revision: 1, projection_state: "pending" };
describe("standalone managed goal response", () => {
  it.each([DEFAULT_GOAL_STATE, { ...DEFAULT_GOAL_STATE, revision: 0 }, { ...DEFAULT_GOAL_STATE, revision: 2 }, embedded])("accepts legacy and typed revision metadata: %j", async (state) => {
    expect(await consume(state)).toEqual({ goal_state: state });
  });
  it("accepts last_evaluation metadata", async () => {
    const state = {
      ...embedded,
      last_evaluation: {
        verdict: "DONE",
        reason: "Goal achieved",
        completed_at: "2026-10-04T12:00:00.000Z",
        goal_revision: 1,
      },
    };
    expect(await consume(state)).toEqual({ goal_state: state });
  });
  it("accepts the real backend goal payload for workflows with COMPLETED verdict and open schema fields", async () => {
    const realPayload = {
      applied_revision: 30,
      desired_revision: 30,
      enabled: true,
      goal: "Using google search directly (no subagents), look up 5 different cookie recipes, pick the best one and present to the user.",
      goal_lock_enabled: true,
      last_evaluation: {
        completed_at: "2026-10-12T23:55:37.115Z",
        goal_revision: 30,
        reason: "Directly looked up five distinct cookie recipes using Google search, selected the best one with detailed rationale, and presented all recipes and the winning selection to the user.",
        verdict: "COMPLETED",
      },
      outcome_counts: {
        COMPLETED: 7,
        NOT_DONE: 493,
        EXTRA_KEY: 0,
      },
      projection_state: "applied",
      snooze_budget_cap_ms: null,
      snooze_duration_ms: 60000,
      snooze_enabled: true,
      snooze_refill_elapsed_ms: 60000,
      snooze_refill_yield_ms: 30000,
      snooze_turn_limit: null,
      unknown_future_field: "ignored gracefully per Postel's Law",
    };
    const res = await consume(realPayload);
    expect(res.goal_state.last_evaluation?.verdict).toBe("COMPLETED");
    expect(res.goal_state.outcome_counts.COMPLETED).toBe(7);
    expect(res.goal_state.outcome_counts.NOT_DONE).toBe(493);
    expect(res.goal_state.enabled).toBe(true);
  });
  it.each([
    ...[null, "2", true, -1, 1.5, Number.MAX_SAFE_INTEGER + 1].map((revision) => ({ ...embedded, revision })),
    { ...embedded, desired_revision: "2" },
    { ...embedded, applied_revision: -1 },
    { ...embedded, applied_revision: 3 },
    { ...embedded, projection_state: "unknown" },
    { ...DEFAULT_GOAL_STATE, desired_revision: 1 },
    { ...embedded, outcome_counts: { COMPLETED: -1, NOT_DONE: 0 } },
    { ...embedded, goal: "a".repeat(4001) },
  ])("rejects malformed fields: %j", async (state) => {
    await expect(consume(state)).rejects.toMatchObject({ code: "invalid_response" });
  });
});
