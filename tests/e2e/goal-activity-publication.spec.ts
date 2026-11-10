import { DatabaseSync } from "node:sqlite";
import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { workflowHarness, settled, model } from "./workflow-implicit-goals-harness";

// goal-activity-publication.spec.md: current automatic NOT_DONE publication and
// no duplicate publication on restart.
// Browser -> proxy -> router -> disposable SQLite; only the provider is mocked.
test("goal activity is published once and ledger prevents replay after restart", async ({ page }) => {
  test.setTimeout(120000);
  const f = await workflowHarness();
  try {
    await f.open(page);
    const agent = `goal-publication-${randomUUID()}`;
    await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model });
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    f.replies.push({ tool: "goal_snooze", args: { duration_ms: 30000 } }, { answer: "Work waiting" });
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Finish the fixture task" }, "PUT");
    await expect.poll(() => settled(f, agent).length).toBe(1);
    const ledger = () => f.rows("SELECT event_key, reason FROM embedded_goal_activity_publications WHERE agent_id=? ORDER BY user_seq", agent);
    const history = () => f.rows("SELECT event_key, message, message_content FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation' ORDER BY sequence", agent);
    await expect.poll(() => ledger().length).toBe(1);
    await expect.poll(() => history().length).toBe(1);
    const beforeLedger = ledger();
    const beforeHistory = history();
    expect(beforeHistory.map(row => row.event_key)).toEqual(beforeLedger.map(row => row.event_key));
    expect(JSON.stringify(beforeHistory)).toContain("Goal active after the worker settled; continuing automatically.");
    expect(f.wsFrames.some(frame => frame.includes("goal_evaluation"))).toBe(true);
    // Simulate display retention eviction only while the disposable router is stopped.
    // The publication ledger must prevent startup recovery from resurrecting it.
    await f.router.restart(async path => {
      const db = new DatabaseSync(path);
      try { db.prepare("DELETE FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'").run(agent); }
      finally { db.close(); }
    });
    await expect.poll(() => ledger()).toEqual(beforeLedger);
    await expect.poll(() => history()).toEqual([]);
    await f.advance(60000);
    expect(ledger()).toEqual(beforeLedger);
    expect(history()).toEqual([]);
    expect(f.replies).toEqual([]);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
