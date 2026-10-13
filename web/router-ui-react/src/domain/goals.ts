export type GoalAction = "set" | "off";
export interface GoalEvaluationSummary {
  verdict: "DONE" | "COMPLETED" | "NOT_DONE" | string;
  reason?: string;
  completed_at: string;
  goal_revision?: number;
}
export interface GoalControlSettings {
  goal_lock_enabled: boolean;
  snooze_enabled: boolean;
  snooze_duration_ms: number;
  snooze_turn_limit: number | null;
  snooze_budget_cap_ms: number | null;
  snooze_refill_yield_ms: number;
  snooze_refill_elapsed_ms: number;
}
export interface GoalState extends GoalControlSettings {
  enabled: boolean;
  goal: string;
  outcome_counts: { COMPLETED: number; NOT_DONE: number };
  last_evaluation?: GoalEvaluationSummary;
  revision?: number;
  desired_revision?: number;
  applied_revision?: number;
  projection_state?: "pending" | "applied" | "failed";
}
export interface GoalEditorValues {
  enabled: boolean;
  goal: string;
  goal_lock_enabled: boolean;
  snooze_enabled: boolean;
  snooze_duration_seconds: number;
  snooze_turn_limit: number | null;
  snooze_budget_minutes: number | null;
  snooze_refill_yield_seconds: number;
  snooze_refill_elapsed_seconds: number;
}
export const DEFAULT_GOAL_SETTINGS: GoalControlSettings = {
  goal_lock_enabled: false,
  snooze_enabled: true,
  snooze_duration_ms: 60_000,
  snooze_turn_limit: null,
  snooze_budget_cap_ms: null,
  snooze_refill_yield_ms: 30_000,
  snooze_refill_elapsed_ms: 60_000,
};
export const DEFAULT_GOAL_STATE: GoalState = {
  enabled: false,
  goal: "",
  ...DEFAULT_GOAL_SETTINGS,
  outcome_counts: { COMPLETED: 0, NOT_DONE: 0 },
};
function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function safeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value);
}
function positive(value: unknown): value is number {
  return safeInteger(value) && value > 0;
}
function positiveOrZero(value: unknown): value is number {
  return safeInteger(value) && value >= 0;
}
function nullablePositive(value: unknown): value is number | null {
  return value === null || positive(value);
}
export function validateGoalControlSettings(value: unknown): GoalControlSettings | undefined {
  if (!record(value) || typeof value.goal_lock_enabled !== "boolean" || typeof value.snooze_enabled !== "boolean" || !positive(value.snooze_duration_ms) || !nullablePositive(value.snooze_turn_limit) || !nullablePositive(value.snooze_budget_cap_ms) || !positive(value.snooze_refill_yield_ms) || !positive(value.snooze_refill_elapsed_ms) || (value.snooze_turn_limit !== null && value.snooze_turn_limit > 20)) return undefined;
  return {
    goal_lock_enabled: value.goal_lock_enabled,
    snooze_enabled: value.snooze_enabled,
    snooze_duration_ms: value.snooze_duration_ms,
    snooze_turn_limit: value.snooze_turn_limit,
    snooze_budget_cap_ms: value.snooze_budget_cap_ms,
    snooze_refill_yield_ms: value.snooze_refill_yield_ms,
    snooze_refill_elapsed_ms: value.snooze_refill_elapsed_ms,
  };
}
export function normalizeGoalState(value: unknown): GoalState | undefined {
  if (!record(value)) return undefined;
  if (typeof value.enabled !== "boolean" || typeof value.goal !== "string" || value.goal.length > 4000) return undefined;
  const enabled = value.enabled;
  const goal = value.goal;

  const metadata: Pick<GoalState, "revision" | "desired_revision" | "applied_revision" | "projection_state"> = {};
  if (Object.hasOwn(value, "revision")) {
    if (!positiveOrZero(value.revision)) return undefined;
    metadata.revision = value.revision;
  }
  if (["desired_revision", "applied_revision", "projection_state"].some((key) => Object.hasOwn(value, key))) {
    if (!positiveOrZero(value.desired_revision) || !positiveOrZero(value.applied_revision) || (value.applied_revision as number) > (value.desired_revision as number) || (value.projection_state !== "pending" && value.projection_state !== "applied" && value.projection_state !== "failed")) return undefined;
    metadata.desired_revision = value.desired_revision as number;
    metadata.applied_revision = value.applied_revision as number;
    if (value.projection_state === "pending" || value.projection_state === "applied" || value.projection_state === "failed") {
      metadata.projection_state = value.projection_state;
    }
  }

  let last_evaluation: GoalEvaluationSummary | undefined = undefined;
  if (Object.hasOwn(value, "last_evaluation") && value.last_evaluation !== null && value.last_evaluation !== undefined) {
    const raw = value.last_evaluation;
    if (!record(raw) || typeof raw.verdict !== "string" || !["DONE", "COMPLETED", "NOT_DONE"].includes(raw.verdict) || typeof raw.completed_at !== "string" || (raw.goal_revision !== undefined && !positiveOrZero(raw.goal_revision)) || (raw.reason !== undefined && typeof raw.reason !== "string")) return undefined;
    last_evaluation = {
      verdict: raw.verdict,
      completed_at: raw.completed_at,
      ...(positiveOrZero(raw.goal_revision) ? { goal_revision: raw.goal_revision } : {}),
      ...(typeof raw.reason === "string" ? { reason: raw.reason } : {}),
    };
  }

  const settings = validateGoalControlSettings(value) ?? DEFAULT_GOAL_SETTINGS;

  let completedCount = 0;
  let notDoneCount = 0;
  if (record(value.outcome_counts)) {
    if (!positiveOrZero(value.outcome_counts.COMPLETED) || !positiveOrZero(value.outcome_counts.NOT_DONE)) return undefined;
    completedCount = value.outcome_counts.COMPLETED;
    notDoneCount = value.outcome_counts.NOT_DONE;
  }

  return {
    enabled,
    goal,
    ...settings,
    ...metadata,
    ...(last_evaluation ? { last_evaluation } : {}),
    outcome_counts: { COMPLETED: completedCount, NOT_DONE: notDoneCount },
  };
}
export function normalizeGoalProjection(value: unknown): { enabled: boolean; goal: string } | undefined { if (!record(value) || typeof value.enabled !== "boolean" || typeof value.goal !== "string" || value.goal.length > 4000 || (value.enabled && value.goal.length === 0)) return undefined; return { enabled: value.enabled, goal: value.enabled ? value.goal : "" }; }
export function goalSettingsFromEditor(editor: GoalEditorValues): GoalControlSettings { const seconds = (value: number) => Math.round(value * 1000); const minutes = (value: number | null) => value === null ? null : Math.round(value * 60_000); return { goal_lock_enabled: editor.goal_lock_enabled, snooze_enabled: editor.snooze_enabled, snooze_duration_ms: seconds(editor.snooze_duration_seconds), snooze_turn_limit: editor.snooze_turn_limit, snooze_budget_cap_ms: minutes(editor.snooze_budget_minutes), snooze_refill_yield_ms: seconds(editor.snooze_refill_yield_seconds), snooze_refill_elapsed_ms: seconds(editor.snooze_refill_elapsed_seconds) }; }
export function goalControlPayload(editor: GoalEditorValues): { action: GoalAction; goal: string; settings: GoalControlSettings } { const action: GoalAction = editor.enabled ? "set" : "off"; return { action, goal: action === "off" ? "" : editor.goal, settings: goalSettingsFromEditor(editor) }; }
export function goalErrorMessage(code?: string, message?: string): string { if (message) return message; switch (code) { case "target_instance_changed": return "That agent restarted. Close this editor and try again."; case "target_not_connected": return "That agent is no longer connected."; case "goal_control_unavailable": return "Goal controls are not available on this router."; case "invalid_settings": return "Check the goal settings and try again."; case "invalid_goal": return "Enter a goal before saving."; case "control_timeout": return "The goal change timed out."; case "router_reconnecting": return "The router is reconnecting."; default: return "The goal change could not be saved."; } }
export function goalResultMatches(pending: { controlId: string; peer: string; instanceId: string; revision?: number }, result: { controlId: string; targetAgentId: string; targetInstanceId: string; revision?: number }): boolean { return pending.controlId === result.controlId && pending.peer === result.targetAgentId && pending.instanceId === result.targetInstanceId && (pending.revision === undefined || pending.revision === result.revision); }
