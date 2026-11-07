import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { workflowHarness, model, userTurn } from "./workflow-implicit-goals-harness";

for (const verdict of ["COMPLETED", "NOT_DONE"] as const) {
  test(`Pi foreground preserves revised goal after stale ${verdict} evaluator response`, async ({ page }) => {
    test.setTimeout(90_000);
    const f = await workflowHarness({ piReviewBarrier: true });
    try {
      await f.open(page);
      const agent = `pi-scope-${randomUUID()}`;
      await f.api("/v1/managed-agents", { name: agent, cwd: `${f.router.root}/work`, runtime: "embedded", model });
      await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${agent}"]`).click();
      f.replies.push({ tool: "goal", args: { action: "set", goal: "Original goal" } },
        { answer: "Original turn done" },
        { evaluation: true, answer: `VERDICT: ${verdict}\nREASON: obsolete verdict` }, { hold: true });
      await userTurn(page, "Set the goal and work.");
      await expect.poll(() => f.requests.length, { timeout: 15000 }).toBe(3);
      expect(f.requests[0].instructions).toContain("You are an expert coding assistant operating inside pi");
      expect(f.requests[0].instructions).toContain("Responsiveness & Communication");
      expect(f.requests[0].instructions).toContain("embedded worker running");
      expect(f.requests[0].instructions).not.toContain("embedded root running");
      expect(f.requests[2].instructions).toMatch(/^goal: Original goal/i);
      await expect.poll(async () => { try { return await readFile(join(f.router.root, "parsed"), "utf8"); } catch { return ""; } }, { timeout: 15000 }).toBe(`${agent}\n${verdict}`);
      await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Revised goal" }, "PUT");
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: true, goal: "Revised goal" });
      await writeFile(join(f.router.root, "release"), "go");
      // The revised worker request can only start after the old evaluator leaves the lane.
      // Its response remains held: the new flight has not completed.
      await expect.poll(() => f.requests.length, { timeout: 15000 }).toBe(4);
      expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state).toMatchObject({ enabled: true, goal: "Revised goal" });
      expect(f.rows("SELECT desired_revision, goal_text, goal_enabled FROM managed_agent_policies WHERE agent_id=?", agent)).toEqual([{ desired_revision: 2, goal_text: "Revised goal", goal_enabled: 1 }]);
      expect(JSON.stringify(f.requests[3].input)).toContain("Revised goal");
      expect(f.rows("SELECT state FROM embedded_goal_evaluations WHERE agent_id=? AND goal_revision=1", agent).map(row => row.state)).toEqual(["UNKNOWN"]);
      expect(f.rows("SELECT * FROM embedded_goal_continuations WHERE agent_id=? AND goal_revision=1", agent)).toEqual([]);
      expect(f.violations).toEqual([]);
    } finally { await writeFile(join(f.router.root, "release"), "go").catch(() => {}); await f.close(); }
  });
}

 test("Pi root sends its baseline prompt to the provider", async () => {
  test.setTimeout(90_000);
  const agent = `pi-root-${randomUUID()}`;
  const f = await workflowHarness({
    embeddedRoot: { name: agent, requestId: randomUUID(), model, instruction: "Root bootstrap" },
    initialReplies: [{ answer: "Root ready" }],
  });
  try {
    await expect.poll(() => f.requests.length, { timeout: 20000 }).toBe(1);
    await expect.poll(() => f.rows("SELECT kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('user','assistant') ORDER BY seq", agent)
      .map(row => `${row.kind}:${row.body}`).join("\n"), { timeout: 20000 }).toMatch(/user:.*Root bootstrap[\s\S]*assistant:.*Root ready/);
    expect(f.rows("SELECT role FROM embedded_sessions WHERE agent_id=? AND current=1", agent)).toEqual([{ role: "root" }]);
    expect(f.requests[0].instructions).toContain("You are an expert coding assistant operating inside pi");
    expect(f.requests[0].instructions).toContain("Responsiveness & Communication");
    expect(f.requests[0].instructions).toContain("embedded worker running");
    expect(f.requests[0].instructions).not.toContain("embedded root running");
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
