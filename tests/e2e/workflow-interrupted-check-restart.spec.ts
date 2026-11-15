import { DatabaseSync } from "node:sqlite";
import { test, expect } from "@playwright/test";
import { workflowHarness, createRunningWorkflow, settled, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// Test-only SQLite fault: reject settlement after the real UNKNOWN claim commits.
// No evidence rows are synthesized or edited by the fixture.
test("SCN-WFIG-018: recovered reservation survives a second crash after recovery commit", async ({ page }) => {
  test.setTimeout(180000);
  let f: WorkflowHarness | undefined;
  let failure: unknown;
  try {
    f = await workflowHarness({ restartCodexConsumerFixture: true });
    await f.router.restart(async path => {
      const db = new DatabaseSync(path);
      try { db.exec(`CREATE TRIGGER test_hold_workflow_settlement BEFORE UPDATE OF state ON embedded_goal_evaluations
        WHEN OLD.state='UNKNOWN' AND NEW.state='NOT_DONE' BEGIN SELECT RAISE(ABORT, 'test settlement interruption'); END`); }
      finally { db.close(); }
    });
    f.replies.push({ tool: "workflow_snooze", args: { duration_ms: 300000 } }, { answer: "Interrupted final" });
    const current = await createRunningWorkflow(f, page, 600000, false);
    await expect(page.getByText("Interrupted final", { exact: true }).first()).toBeVisible({ timeout: 30000 });
    await expect.poll(() => f!.rows("SELECT state FROM embedded_goal_evaluations WHERE agent_id=?", current.agent).map(row => row.state)).toEqual(["UNKNOWN"]);
    expect(f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=?", current.agent)).toHaveLength(0);
    const claim = f.rows("SELECT owner_token,final_seq,final_digest FROM embedded_goal_evaluations WHERE agent_id=?", current.agent)[0];
    const first = f.router.process;
    first.kill("SIGKILL");
    await new Promise<void>(resolve => first.once("exit", () => resolve()));
    const cleanup = new DatabaseSync(f.router.databasePath);
    try { cleanup.exec("DROP TRIGGER test_hold_workflow_settlement"); } finally { cleanup.close(); }
    await f.restart();
    await expect.poll(() => settled(f!, current.agent).length, { timeout: 15000 }).toBe(1);
    const recovered = settled(f, current.agent)[0];
    expect(recovered.state).toBe("NOT_DONE");
    expect(recovered.continuation_state).toBe("reserved");
    expect(recovered.started_at).toBeNull();
    expect(recovered.owner_token).toBe(claim.owner_token);
    expect(recovered.final_seq).toBe(claim.final_seq);
    expect(recovered.final_digest).toBe(claim.final_digest);
    const successor = f.rows("SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND current=1", current.agent)[0];
    expect(Number(successor.owner_token)).toBeGreaterThan(Number(claim.owner_token));
    const second = f.router.process;
    second.kill("SIGKILL");
    await new Promise<void>(resolve => second.once("exit", () => resolve()));
    f.replies.push({ answer: "Restart context restored" }, { hold: true });
    await f.restart();
    const requests = () => f!.requests.filter(r => {
      const input = r.input;
      return Array.isArray(input) && JSON.stringify(input.filter(item => item.role === "user").at(-1)?.content).includes(`The workflow ${current.workflow.name} is currently active in state A.`);
    });
    await f.advance(Math.max(10000, Date.parse(String(recovered.due_at)) - f.clock));
    await expect.poll(() => requests().length, { timeout: 15000 }).toBe(1);
    await expect.poll(() => settled(f!, current.agent)[0]?.continuation_state).toBe("started");
    await f.advance(60000);
    expect(requests()).toHaveLength(1);
    expect(f.violations).toEqual([]);
  } catch (error) { failure = error; throw error; }
  finally {
    try { await f?.close(); } catch (error) { if (!failure) throw error; console.error("fixture cleanup:", error); }
  }
});

