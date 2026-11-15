import { writeFile, rm } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { serialBarrier } from "./claude-code-serial-barrier";
import { workflowHarness, createRunningWorkflow, settled } from "./workflow-implicit-goals-harness";

test("SCN-WHO-006: prompt and interruption retain the workflow captured at start", async ({ page }) => {
  test.setTimeout(180000);
  const f = await workflowHarness({ restartCodexConsumerFixture: true });
  const barrier = await serialBarrier("/continuation-prompt");
  let config: string | undefined;
  try {
    f.replies.push({ tool: "workflow_snooze", args: { duration_ms: 300000 } }, { answer: "Wait saved" });
    const current = await createRunningWorkflow(f, page, 600000, false);
    await expect(page.getByText("Wait saved", { exact: true }).first()).toBeVisible({ timeout: 30000 });
    await expect.poll(() => settled(f, current.agent).length).toBe(1);
    const reservation = settled(f, current.agent)[0];
    config = f.router.databasePath + ".restart-mail-barrier.json";
    await writeFile(config, JSON.stringify({ enabled: "1", endpoint: barrier.endpoint, token: barrier.token, agent_id: current.agent }), { mode: 0o600 });
    f.replies.push({ hold: true });
    const anchor = await f.api("/v1/workflows", { name: `Clock ${current.agent}` });
    f.clock = Date.parse(anchor.created_at);
    await f.advance(Date.parse(String(reservation.due_at)) - f.clock);
    expect(await barrier.arrived).toBe(current.agent);
    await f.api(`/v1/managed-agents/${current.agent}/workflow-state`, { action: "deactivate" }, "PUT");
    await rm(config); config = undefined; barrier.release();
    await expect.poll(() => f.requests.filter(request => JSON.stringify(request.input).includes(`The workflow ${current.workflow.name} is currently active in state A.`)).length, { timeout: 30000 }).toBe(1);
    const old = f.router.process; old.kill("SIGKILL"); await new Promise<void>(resolve => old.once("exit", () => resolve()));
    f.replies.push({ answer: "Restored" }); await f.restart();
    await expect.poll(async () => (await f.api(`/v1/managed-agents/${current.agent}`)).agent.workflow_continuation_hold?.workflow_id, { timeout: 30000 }).toBe(current.workflow.id);
    const hold = (await f.api(`/v1/managed-agents/${current.agent}`)).agent.workflow_continuation_hold;
    expect(hold.workflow_state_id).toBe(current.a.id);
    expect(f.violations).toEqual([]);
  } finally {
    if (config) await rm(config, { force: true });
    await barrier.close(); await f.close();
  }
});
