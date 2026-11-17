import { test, expect } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("IR-012/014: edits during a held turn do not queue guidance and the next transition uses current permission", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness();
  let release!: () => void;
  const barrier = new Promise<void>(resolve => { release = resolve; });
  try {
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 } }, { answer: "Ready" });
    const { agent, a, b } = await createRunningWorkflow(f, page, 600000, false);
    await expect(page.getByText("Ready", { exact: true }).first()).toBeVisible();
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: b.id } },
      { tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 } }, { answer: "Reached B" });
    await userTurn(page, "Advance to B");
    await expect(page.getByText("Reached B", { exact: true }).first()).toBeVisible();
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${agent}/workflow-state`)).active_workflow_state_id).toBe(b.id);

    const before = f.requests.length;
    f.replies.push({ answer: "Held turn finished", barrier });
    await userTurn(page, "Hold this turn");
    await expect.poll(() => f.requests.length).toBe(before + 1);
    const heldCount = f.requests.length;
    // The saves must finish before the provider response is released.
    for (const [marker, enabled] of [["Intermediate marker", true], ["Final marker", false]] as const) {
      const saved = await Promise.race([
        f.api(`/v1/workflow-states/${b.id}`, { name: "B", entry_text: marker, allow_exit: true, allow_implicit_return: enabled }, "PUT"),
        new Promise<never>((_, reject) => setTimeout(() => reject(Error("definition save blocked by held turn")), 5000)),
      ]);
      expect(saved.workflow_state.allow_implicit_return).toBe(enabled);
      expect(f.requests.length).toBe(heldCount);
    }
    release();
    await expect(page.getByText("Held turn finished", { exact: true }).first()).toBeVisible();
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
    expect(f.requests.length).toBe(heldCount);

    const effects = () => ({
      binding: f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?", agent),
      reminders: f.rows("SELECT * FROM managed_agent_workflow_reminders WHERE agent_id=? ORDER BY reminder_id", agent),
      monitors: f.rows("SELECT * FROM managed_agent_monitors WHERE agent_id=? ORDER BY id", agent),
      history: f.rows("SELECT sequence FROM managed_complete_history WHERE agent_id=? AND message='Workflow state transitioned' ORDER BY sequence", agent),
    });
    const prior = effects();
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: a.id } }, { answer: "Return rejected" });
    await userTurn(page, "Try the return");
    await expect(page.getByText("Return rejected", { exact: true }).first()).toBeVisible();
    const guidance = String(f.requests.at(-2)?.instructions);
    expect(guidance).toContain("Final marker");
    expect(guidance).not.toContain("Intermediate marker");
    expect(JSON.stringify(f.requests.at(-1)?.input)).toMatch(/not allowed|not permitted|invalid|denied|no outgoing transitions|Allowed targets: none/i);
    expect(effects()).toEqual(prior);
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally {
    release();
    await f.close();
  }
});
