import { DatabaseSync } from "node:sqlite";
import { test, expect } from "@playwright/test";
import { workflowHarness, createRunningWorkflow, userTurn, type WorkflowHarness } from "./workflow-implicit-goals-harness";

// A real settled turn creates the claim; the trigger simulates interruption at
// the settlement boundary. The detached case removes workflow authority
// while the router is down; it does not manufacture the UNKNOWN claim.
for (const mode of ["superseded", "detached-workflow"] as const) {
  test(`SCN-WFIG-018: ${mode} UNKNOWN does not prevent startup or replay`, async ({ page }) => {
    test.setTimeout(180000);
    let f: WorkflowHarness | undefined;
    let failure: unknown;
    try {
      f = await workflowHarness({ restartCodexConsumerFixture: true });
      await f.router.restart(async path => {
        const db = new DatabaseSync(path);
        try { db.exec(`CREATE TRIGGER test_unknown_startup_hold BEFORE UPDATE OF state ON embedded_goal_evaluations
          WHEN OLD.state='UNKNOWN' AND NEW.state='NOT_DONE' BEGIN SELECT RAISE(ABORT, 'test interruption'); END`); }
        finally { db.close(); }
      });
      f.replies.push({ tool: "workflow_snooze", args: { duration_ms: 300000 } }, { answer: "Unknown startup final" });
      const current = await createRunningWorkflow(f, page, 600000, false);
      await expect(page.getByText("Unknown startup final", { exact: true }).first()).toBeVisible({ timeout: 30000 });
      await expect.poll(() => f!.rows("SELECT state FROM embedded_goal_evaluations WHERE agent_id=?", current.agent)).toEqual([{ state: "UNKNOWN" }]);
      const claim = f.rows("SELECT final_seq,final_digest,owner_token,state FROM embedded_goal_evaluations WHERE agent_id=?", current.agent)[0];
      if (mode === "superseded") {
        f.replies.push({ hold: true });
        await userTurn(page, "Newer work supersedes the interrupted check.");
        await expect.poll(() => f!.rows("SELECT MAX(seq) AS seq FROM embedded_entries WHERE agent_id=?", current.agent)[0].seq).toBeGreaterThan(Number(claim.final_seq));
        await expect.poll(() => f!.requests.length).toBe(3);
      }
      const before = f.requests.length;
      const old = f.router.process;
      const exited = new Promise<void>(resolve => old.once("exit", () => resolve()));
      old.kill("SIGKILL");
      await exited;
      const db = new DatabaseSync(f.router.databasePath);
      try {
        db.exec("DROP TRIGGER test_unknown_startup_hold");
        if (mode === "detached-workflow") db.prepare("UPDATE managed_agents SET active_workflow_id=NULL, active_workflow_state_id=NULL WHERE agent_id=?").run(current.agent);
      } finally { db.close(); }
      f.replies.push({ answer: "Startup context restored" });
      await f.restart();
      console.log("REQUESTS SO FAR:", f.requests.length, f.requests.map(r => (r.input as any)?.at(-1)));
      await expect.poll(() => Number(f!.rows("SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND current=1", current.agent)[0]?.owner_token), { timeout: 15000 }).toBeGreaterThan(Number(claim.owner_token));
      await expect.poll(async () => (await f!.api(`/v1/managed-agents/${current.agent}`)).agent.lifecycle_state, { timeout: 15000 }).toBe("running");
      if (mode === "detached-workflow") {
        f.replies.push({ hold: true });
      }
      await f.advance(900000);
      await page.waitForTimeout(500);
      expect(f.rows("SELECT final_seq,final_digest,owner_token,state FROM embedded_goal_evaluations WHERE agent_id=? AND final_seq=?", current.agent, claim.final_seq)).toEqual([claim]);
      expect(f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=? AND final_seq=?", current.agent, claim.final_seq)).toHaveLength(0);
      const nudges = f.requests.slice(before).filter(r => JSON.stringify(r.input).includes(`The workflow ${current.workflow.name} is currently active in state A.`));
      expect(nudges).toHaveLength(0);
      console.log("REQUESTS AT END:", f.requests.length, f.requests.map((r, i) => `${i+1}: ${JSON.stringify((r.input as any)?.at(-1))}`));
      console.log("VIOLATIONS AT END:", f.violations);
      expect(f.violations).toEqual([]);
    } catch (error) { failure = error; throw error; }
    finally { try { await f?.close(); } catch (error) { if (!failure) throw error; console.error("fixture cleanup:", error); } }
  });
}
