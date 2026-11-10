import { test, expect } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("IR-007: explicit target name wins over implicit predecessor ID", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness();
  const owns = (text: string) => (request: Record<string, unknown>) => Array.isArray(request.input) &&
    JSON.stringify(request.input.filter(item => item?.role === "user").at(-1)).includes(text);
  async function wait(agent: string, answer: string) {
    await expect(page.getByText(answer, { exact: true }).first()).toBeVisible();
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent)[0]?.activity_state).toBe("idle");
  }
  async function transition(agent: string, text: string, target: string, answer: string) {
    const owner = owns(text);
    f.replies.push(
      { tool: "mecha_workflow_transition", args: { action: "transition", target_state: target }, ownsRequest: owner },
      { tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: owner },
      { answer, ownsRequest: owner },
    );
    await userTurn(page, text);
    await wait(agent, answer);
  }
  try {
    const activation = owns("[Workflow Activated: A]");
    f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 5 }, ownsRequest: activation }, { answer: "Ready", ownsRequest: activation });
    const { agent, workflow, a, b } = await createRunningWorkflow(f, page, 600000, false);
    await wait(agent, "Ready");
    const { workflow_state: collision } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: a.id });
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", allow_exit: true, allow_implicit_return: true,
      transitions: [{ target_state_id: collision.id, description: "Explicit collision" }] }, "PUT");
    await transition(agent, "Advance to B", b.id, "At B");
    expect(f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id FROM managed_agents WHERE agent_id=?", agent)[0]).toMatchObject({ active_workflow_state_id: b.id, workflow_predecessor_state_id: a.id });
    await transition(agent, "Resolve collision", a.id, "Collision resolved");
    expect(f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id FROM managed_agents WHERE agent_id=?", agent)[0]).toMatchObject({ active_workflow_state_id: collision.id, workflow_predecessor_state_id: b.id });
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
