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
  it.each([
    ...[null, "2", true, -1, 1.5, Number.MAX_SAFE_INTEGER + 1].map((revision) => ({ ...embedded, revision })),
    { ...embedded, unknown: 1 },
    { ...embedded, desired_revision: "2" },
    { ...embedded, applied_revision: -1 },
    { ...embedded, applied_revision: 3 },
    { ...embedded, projection_state: "unknown" },
    { ...DEFAULT_GOAL_STATE, desired_revision: 1 },
    { ...embedded, outcome_counts: { COMPLETED: 0, NOT_DONE: 0, UNKNOWN: 0 } },
    { ...embedded, outcome_counts: { COMPLETED: 0 } },
    { ...embedded, snooze_duration_ms: "60000" },
  ])("rejects malformed and unknown fields: %j", async (state) => {
    await expect(consume(state)).rejects.toMatchObject({ code: "invalid_response" });
  });
});
