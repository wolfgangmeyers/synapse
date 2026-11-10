import { test, expect } from "@playwright/test";
import { createRunningWorkflow, lastToolOutput, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("IR-012/015: revoking return before a transition rejects without lifecycle effects", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await workflowHarness();
  try {
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 } }, { answer: "Ready" });
    const { agent, a, b } = await createRunningWorkflow(f, page, 600000, false);
    const wait = async (text: string) => {
      await expect(page.getByText(text, { exact: true }).first()).toBeVisible();
      await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    };
    await wait("Ready");
    const definition = { name: "B", entry_text: "B guidance", allow_exit: true, allowed_tools: ["read", "workflow_snooze"],
      reminders: [{ id: "b", type: "one_shot", body: "B reminder", delay_seconds: 600 }],
      monitors: [{ name: "B monitor", question_instructions: "Check B", nudge_template: "B nudge", interval_seconds: 3600 }] };
    await f.api(`/v1/workflow-states/${b.id}`, { ...definition, allow_implicit_return: true }, "PUT");
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: b.id } },
      { tool: "workflow_snooze", args: { duration_minutes: 5 } }, { answer: "At B" });
    await userTurn(page, "Advance to B"); await wait("At B");
    await expect.poll(() => f.rows("SELECT last_evaluated_at FROM managed_agent_monitors WHERE agent_id=?", agent)[0]?.last_evaluated_at).toBeTruthy();
    const forward = f.rows("SELECT sequence FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence DESC LIMIT 1", agent)[0];
    await expect.poll(() => f.wsFrames.map(raw => JSON.parse(raw)).some(frame =>
      frame.type === "history_entry" && frame.view === "complete" && frame.entry?.id === `complete:${forward.sequence}`)).toBe(true);
    const saved = await f.api(`/v1/workflow-states/${b.id}`, { ...definition, allow_implicit_return: false }, "PUT");
    expect(saved.workflow_state.allow_implicit_return).toBe(false);
    const effects = () => ({
      binding: f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?", agent),
      reminders: f.rows("SELECT * FROM managed_agent_workflow_reminders WHERE agent_id=? ORDER BY reminder_id", agent),
      reminderRows: f.rows("SELECT r.* FROM reminders r JOIN managed_agent_workflow_reminders w ON w.reminder_id=r.id WHERE w.agent_id=? ORDER BY r.id", agent),
      monitors: f.rows("SELECT * FROM managed_agent_monitors WHERE agent_id=? ORDER BY id", agent),
      history: f.rows("SELECT sequence FROM managed_complete_history WHERE agent_id=? AND message IN ('Workflow state transitioned','Workflow exited') ORDER BY sequence", agent),
    });
    const before = effects();
    expect(before.binding[0]).toMatchObject({ active_workflow_state_id: b.id, workflow_predecessor_state_id: a.id });
    expect(before.reminders).toHaveLength(1); expect(before.reminderRows).toHaveLength(1); expect(before.monitors).toHaveLength(1);
    const goalBefore = (await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state;
    const stableGoal = (goal: typeof goalBefore) => ({
      enabled: goal.enabled, goal: goal.goal,
      desired_revision: goal.desired_revision, applied_revision: goal.applied_revision,
      projection_state: goal.projection_state, snooze_enabled: goal.snooze_enabled,
      snooze_duration_ms: goal.snooze_duration_ms, snooze_turn_limit: goal.snooze_turn_limit,
      snooze_budget_cap_ms: goal.snooze_budget_cap_ms, snooze_refill_yield_ms: goal.snooze_refill_yield_ms,
      snooze_refill_elapsed_ms: goal.snooze_refill_elapsed_ms,
    });
    expect(goalBefore).toMatchObject({ enabled: true, goal: expect.stringContaining("Complete active workflow:") });
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: a.id } }, { answer: "Return rejected" });
    const frames = f.wsFrames.length;
    await userTurn(page, "Return to A");
    await wait("Return rejected");
    expect(lastToolOutput(f)).toMatch(/not permitted|no outgoing transitions/i);
    const transitionNotifications = () => f.wsFrames.slice(frames).map(raw => JSON.parse(raw)).filter(frame =>
      frame.type === "history_entry" && frame.view === "complete" &&
      (frame.entry?.message === "Workflow state transitioned" || frame.entry?.message === "Workflow exited"));
    expect(transitionNotifications()).toEqual([]);
    expect(effects()).toEqual(before);
    expect(stableGoal((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state)).toEqual(stableGoal(goalBefore));
    // Both the rejected tool turn and a plain control turn settle normally; their
    // diagnostics are turn outcomes, not workflow lifecycle mutations.
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.outcome_counts.NOT_DONE).toBeGreaterThan(goalBefore.outcome_counts.NOT_DONE);
    const rejectedCount = (await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.outcome_counts.NOT_DONE;
    f.replies.push({ answer: "Control turn settled" });
    await userTurn(page, "Acknowledge without a workflow operation");
    await wait("Control turn settled");
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.outcome_counts.NOT_DONE).toBeGreaterThan(rejectedCount);
    expect(effects()).toEqual(before);
    expect(stableGoal((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state)).toEqual(stableGoal(goalBefore));
    // This is a finite observation, not a delivery barrier. Fail if the shared
    // capture reached its cap, where subsequent frames could be truncated.
    expect(f.wsFrames.length).toBeLessThan(2048);
    expect(transitionNotifications()).toEqual([]);
    expect(f.replies).toEqual([]); expect(f.violations).toEqual([]);
  } finally {
    await f.close();
  }
});
