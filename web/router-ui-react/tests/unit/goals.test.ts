import { describe, expect, it } from "vitest";
import { goalControlPayload, goalSettingsFromEditor, normalizeGoalState, validateGoalControlSettings } from "../../src/domain/goals";

describe("goals", () => {
  const settings = {
    goal_lock_enabled: false,
    snooze_enabled: true,
    snooze_duration_ms: 1000,
    snooze_turn_limit: 1,
    snooze_budget_cap_ms: null,
    snooze_refill_yield_ms: 2000,
    snooze_refill_elapsed_ms: 3000,
  };

  it("validates complete settings", () => {
    expect(validateGoalControlSettings(settings)).toEqual(settings);
    expect(validateGoalControlSettings({ ...settings, snooze_turn_limit: 21 })).toBeUndefined();
  });

  it("converts editor units exactly and supports off", () => {
    expect(goalSettingsFromEditor({
      enabled: true,
      goal: "x",
      goal_lock_enabled: false,
      snooze_enabled: true,
      snooze_duration_seconds: 1.5,
      snooze_turn_limit: null,
      snooze_budget_minutes: 2,
      snooze_refill_yield_seconds: 3,
      snooze_refill_elapsed_seconds: 4,
    })).toMatchObject({ snooze_duration_ms: 1500, snooze_budget_cap_ms: 120000 });

    expect(goalControlPayload({
      enabled: false,
      goal: "ignored",
      ...({
        goal_lock_enabled: false,
        snooze_enabled: true,
        snooze_duration_seconds: 1,
        snooze_turn_limit: null,
        snooze_budget_minutes: null,
        snooze_refill_yield_seconds: 1,
        snooze_refill_elapsed_seconds: 1,
      }),
    }).goal).toBe("");
  });

  it("normalizes valid goal state", () => {
    expect(normalizeGoalState({ enabled: false, goal: "", ...settings, outcome_counts: { COMPLETED: 1, NOT_DONE: 2 } })?.outcome_counts.NOT_DONE).toBe(2);
  });

  it("normalizes valid goal state with last_evaluation", () => {
    const valid = normalizeGoalState({
      enabled: true,
      goal: "Test goal",
      ...settings,
      outcome_counts: { COMPLETED: 1, NOT_DONE: 2 },
      last_evaluation: {
        verdict: "NOT_DONE",
        reason: "Need more work on step 2",
        completed_at: "2026-10-04T12:00:00.000Z",
        goal_revision: 1,
      },
    });
    expect(valid?.last_evaluation?.verdict).toBe("NOT_DONE");
    expect(valid?.last_evaluation?.reason).toBe("Need more work on step 2");
    expect(valid?.last_evaluation?.goal_revision).toBe(1);

    expect(normalizeGoalState({
      enabled: true,
      goal: "Test goal",
      ...settings,
      outcome_counts: { COMPLETED: 1, NOT_DONE: 2 },
      last_evaluation: {
        verdict: "INVALID",
        completed_at: "2026-10-04T12:00:00.000Z",
        goal_revision: 1,
      },
    })).toBeUndefined();
  });

  it("normalizes goal state with COMPLETED verdict and open schema fields", () => {
    const valid = normalizeGoalState({
      enabled: true,
      goal: "Search 5 cookie recipes",
      ...settings,
      extra_metadata: "extra value",
      outcome_counts: { COMPLETED: 7, NOT_DONE: 493, OTHER: 1 },
      last_evaluation: {
        verdict: "COMPLETED",
        reason: "Found 5 cookie recipes",
        completed_at: "2026-10-12T23:55:37.115Z",
        goal_revision: 30,
        extra_eval_field: true,
      },
    });
    expect(valid).toBeDefined();
    expect(valid?.last_evaluation?.verdict).toBe("COMPLETED");
    expect(valid?.last_evaluation?.reason).toBe("Found 5 cookie recipes");
    expect(valid?.outcome_counts.COMPLETED).toBe(7);
    expect(valid?.outcome_counts.NOT_DONE).toBe(493);
  });
});
