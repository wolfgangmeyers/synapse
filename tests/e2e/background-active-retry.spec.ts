import { test, expect } from "@playwright/test";
import { DatabaseSync } from "node:sqlite";
import { createRunningWorkflow, workflowHarness } from "./workflow-implicit-goals-harness";

// SCN-BWC-001/005: live workflow turn, failed admission, and later retry.
test("expiry retries failed termination of an active background workflow", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await workflowHarness({ backgroundExpiryInterval: "250ms" });
  let db: DatabaseSync | undefined;
  try {
    f.replies.push({ hold: true });
    const { agent, workflow } = await createRunningWorkflow(f, page);
    await expect.poll(() => f.requests.length).toBe(1);
    await expect.poll(() => f.rows("SELECT activity_state FROM managed_agents WHERE agent_id=?", agent))
      .toEqual([{ activity_state: "active" }]);
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();

    // Reject real lifecycle reservations at SQLite's boundary until explicitly
    // released. No termination implementation or worker state is substituted.
    db = new DatabaseSync(f.router.databasePath);
    db.exec("PRAGMA busy_timeout = 5000");
    db.exec(`CREATE TRIGGER e2e_reject_expiry BEFORE INSERT ON managed_lifecycle_actions
      WHEN NEW.kind = 'terminate' BEGIN SELECT RAISE(ABORT, 'e2e termination admission failure'); END`);
    await f.advance(24 * 60 * 60 * 1000 + 1000);
    // Observe dispatch for this worker while the provider still holds its turn.
    await expect.poll(() => f.router.stderr().includes(`test background expiry dispatch: ${agent}`), { timeout: 10000 }).toBe(true);
    // Expiry must not report success while the real provider turn holds the lane.
    await expect.poll(() => f.rows("SELECT desired_state FROM managed_agents WHERE agent_id=?", agent)).toEqual([{ desired_state: "running" }]);
    // End the external provider connection so the existing lane can drain.
    f.releaseHeld();
    // The real periodic sweep reports the rejected reservation and retries.
    await expect.poll(() => (f.router.stderr().match(/background termination:/g) ?? []).length, { timeout: 10000 }).toBeGreaterThanOrEqual(2);
    expect(f.rows("SELECT desired_state FROM managed_agents WHERE agent_id=?", agent)).toEqual([{ desired_state: "running" }]);
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    db.exec("DROP TRIGGER e2e_reject_expiry");
    await expect.poll(() => f.rows("SELECT kind FROM managed_lifecycle_actions WHERE agent_id=? AND kind='terminate'", agent), { timeout: 15000 }).not.toEqual([]);
    await expect.poll(() => f.rows("SELECT agent_id FROM managed_agents WHERE agent_id=?", agent), { timeout: 15000 }).toEqual([]);
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toHaveCount(0);
    expect((await f.api(`/v1/workflows/${workflow.id}`)).workflow.id).toBe(workflow.id);
    expect(f.violations).toEqual([]);
  } finally {
    db?.close();
    await f.close();
  }
});
