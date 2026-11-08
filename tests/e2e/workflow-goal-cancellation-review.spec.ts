import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { workflowHarness, model, userTurn } from "./workflow-implicit-goals-harness";

for (const verdict of ["COMPLETED", "NOT_DONE"] as const) {
  test(`workflow activation suppresses a stale Pi ${verdict} evaluator`, async ({ page }) => {
    test.setTimeout(90_000);
    const f = await workflowHarness({ piReviewBarrier: true });
    try {
      await f.open(page);
      const agent = `workflow-cancel-${randomUUID()}`;
      await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model });
      await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${agent}"]`).click();
      f.replies.push({ tool: "goal", args: { action: "set", goal: "Original goal" } }, { answer: "Original turn done" },
        { evaluation: true, answer: `VERDICT: ${verdict}\nREASON: stale` }, { hold: true });
      await userTurn(page, "Set the goal and work.");
      await expect.poll(async () => { try { return await readFile(join(f.router.root, "parsed"), "utf8"); } catch { return ""; } }, { timeout: 15000 }).toBe(`${agent}\n${verdict}`);
      expect(f.requests).toHaveLength(3);
      expect(f.requests[2].instructions).toMatch(/^goal: Original goal/i);
      const workflow = await f.api("/v1/workflows", { name: "New workflow" });
      const { workflow_state: state } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "Start", allow_exit: true });
      await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: "activate", workflow_id: workflow.id, workflow_state_id: state.id }, "PUT");
      expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`))).toMatchObject({ active_workflow_id: workflow.id, active_workflow_state_id: state.id });
      await writeFile(join(f.router.root, "release"), "go");
      // Guidance cannot start until the stale evaluator has settled and left the lane.
      await expect.poll(() => f.requests.length, { timeout: 15000 }).toBe(4);
      expect(JSON.stringify(f.requests[3].input)).toContain("The workflow New workflow is currently active in state Start. You must either make progress towards completing the workflow, call workflow_snooze, exit the workflow with mecha_workflow_transition (action exit) when an exit is allowed, or call mecha_blocked with the reason.");
      expect((await f.api(`/v1/managed-agents/${agent}/workflow-state`))).toMatchObject({ active_workflow_id: workflow.id, active_workflow_state_id: state.id });
      expect(f.rows("SELECT state FROM embedded_goal_evaluations WHERE agent_id=? AND goal_revision=1", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
      // Lane release precedes the continuation decision; wait for the evaluator task to finish.
      await expect.poll(async () => { try { return await readFile(join(f.router.root, "decision-complete"), "utf8"); } catch { return ""; } }, { timeout: 15000 }).toBe(agent);
      expect(f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=? AND goal_revision=1", agent)).toEqual([]);
      expect(f.rows("SELECT desired_revision, goal_enabled FROM managed_agent_policies WHERE agent_id=?", agent)).toEqual([expect.objectContaining({ desired_revision: 2, goal_enabled: 1 })]);
      expect(f.violations).toEqual([]);
    } finally { await writeFile(join(f.router.root, "release"), "go").catch(() => {}); await f.close(); }
  });
}
