import { test, expect } from "@playwright/test";
import { workflowHarness, createRunningWorkflow, settled, type WorkflowHarness } from "./workflow-implicit-goals-harness";

test("SCN-WFIG-018: cold restart does not replay a started ambiguous workflow nudge", async ({ page }) => {
  test.setTimeout(180000);
  let f: WorkflowHarness | undefined;
  let failure: unknown;
  try {
    f = await workflowHarness({ restartCodexConsumerFixture: true });
    f.replies.push({ tool: "workflow_snooze", args: { duration_ms: 300000 } }, { answer: "Wait saved" });
    const current = await createRunningWorkflow(f, page, 600000, false);
    await expect(page.getByText("Wait saved", { exact: true }).first()).toBeVisible({ timeout: 30000 });
    await expect.poll(() => settled(f!, current.agent).length).toBe(1);
    const reservation = settled(f, current.agent)[0];
    expect(reservation.continuation_state).toBe("reserved");
    const nudgeRequests = () => f!.requests.filter(request => {
      const input = request.input;
      return Array.isArray(input) && JSON.stringify(input.filter(item => item.role === "user").at(-1)?.content).includes(`The workflow ${current.workflow.name} is currently active in state A.`);
    });
    f.replies.push({ hold: true });
    const anchor = await f.api("/v1/workflows", { name: `Clock ${current.agent}` });
    f.clock = Date.parse(anchor.created_at);
    await f.advance(Date.parse(String(reservation.due_at)) - f.clock);
    await expect.poll(() => nudgeRequests().length, { timeout: 30000 }).toBe(1);
    await expect.poll(() => settled(f!, current.agent)[0].continuation_state).toBe("started");
    const started = settled(f, current.agent)[0];
    expect((await f.api(`/v1/managed-agents/${current.agent}`)).agent.workflow_continuation_hold).toBeFalsy();
    expect(started.started_at).toBeTruthy();
    const oldProcess = f.router.process;
    oldProcess.kill("SIGKILL");
    await new Promise<void>(resolve => oldProcess.once("exit", () => resolve()));
    f.replies.push({ answer: "Restart context restored" });
    await f.restart();
    expect(oldProcess.exitCode !== null || oldProcess.signalCode !== null).toBe(true);
    expect(f.router.process.pid).not.toBe(oldProcess.pid);
    await expect.poll(() => f!.requests.length, { timeout: 15000 }).toBe(4);
    await expect.poll(() => f!.rows("SELECT execution_json FROM embedded_sessions WHERE agent_id=? AND current=1", current.agent)[0]?.execution_json, { timeout: 15000 }).toBeNull();
    await expect(page.getByText("Restart context restored", { exact: true }).first()).toBeVisible({ timeout: 15000 });
    for (let i = 0; i < 3; i++) await f.advance(600000);
    expect(f.requests).toHaveLength(4);
    expect(nudgeRequests()).toHaveLength(1);
    const after = settled(f, current.agent)[0];
    expect(after.started_at).toBe(started.started_at);
    expect(after.continuation_state).toBe("started");
    const hold = (await f.api(`/v1/managed-agents/${current.agent}`)).agent.workflow_continuation_hold;
    expect(hold.workflow_id).toBe(current.workflow.id);
    expect(hold.workflow_state_id).toBeTruthy();
    expect(hold.owner_token).toBeGreaterThan(0);
    expect(hold.execution_token).toBeGreaterThan(0);
    expect(hold.key.FinalSeq).toBeGreaterThan(0);
    await page.reload();
    await page.locator('button[role="tab"]:has-text("Background")').click();
    await expect(page.locator(`[data-agent-id="${current.agent}"]`)).toBeVisible();
    expect(f.violations).toEqual([]);
  } catch (error) { failure = error; throw error; }
  finally {
    try { await f?.close(); } catch (error) { if (!failure) throw error; console.error("fixture cleanup:", error); }
  }
});
